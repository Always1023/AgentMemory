// Package store 维护本机的两份账：渠道源文件的增量游标（index.json），
// 以及每次收集的运行记录（runs.jsonl，见 runs.go）。
//
// 一句话：游标记住「这个文件上次处理时的样子」，下次没变就不再读它；
// 运行记录一行一次收集，界面按天读回去。
//
// 本机实测 Codex 一个渠道就有 563.9 MB / 256 个文件，没有增量就得每天全量重读。
//
// 依赖方向（docs/ARCHITECTURE.md 模块表）：只依赖共享的会话模型类型，
// 不 import config——文件放在哪个目录由调用方传入。
package store

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/Always1023/AgentMemory/internal/channel"
)

// FileName 是游标文件名。本包是它唯一的写入者（验收标准 3）。
const FileName = "index.json"

// fileVersion 是游标文件的结构版本。
//
// 版本对不上就当作空游标重建：游标只是加速用的缓存，重建的代价是重读一遍，
// 比试着把旧结构猜出来安全。
const fileVersion = 1

// maxParseFailures 是同一个源文件连续解析失败的次数上限（I-048）。
//
// 达到之后不再自动重试，直到文件大小或内容变化；手动「重试」不受此限。
const maxParseFailures = 3

// Entry 是一条游标记录：某个源文件上次处理时的样子，以及它产出的归档文件。
type Entry struct {
	// Path 是源文件的绝对路径。
	Path string `json:"path"`
	// Size 是上次处理时的字节数。
	Size int64 `json:"size"`
	// ModTime 是上次处理时的修改时间。
	ModTime time.Time `json:"mod_time"`
	// Fingerprint 是源文件原始字节的 SHA-256，用来分辨「修改时间被动过、
	// 内容其实没变」的文件。旧游标里没有这一项；虚拟路径（如 SQLite 会话）
	// 取不到文件内容，也留空。
	Fingerprint string `json:"fingerprint,omitempty"`
	// Failures 是这个源文件连续解析失败的次数；成功、或文件大小 / 内容变化后归零（I-048）。
	Failures int `json:"failures,omitempty"`
	// ArchivePath 是这份源文件产出的归档文件；还没归档时为空。
	ArchivePath string `json:"archive_path,omitempty"`
	// ProcessedAt 是上次处理的时刻。
	ProcessedAt time.Time `json:"processed_at"`
}

// Change 是一次增量判定的结果。
type Change int

const (
	// ChangeNone 不用处理：游标里有它，大小、修改时间与内容都没变。
	ChangeNone Change = iota
	// ChangeNew 游标里没有这个源文件，从来没有处理过。
	ChangeNew
	// ChangeUpdated 源文件变了：大小变了、内容变了，或者认不出内容变没变
	// （取不到指纹），保守当作变了。
	ChangeUpdated
	// ChangeUnknown 旧游标没有内容指纹，大小相同、只有修改时间不同。
	// 上层要拿现有归档比一次才能下结论（见 app 的待收统计）。
	ChangeUnknown
)

// cursorFile 是游标文件的磁盘形态。
type cursorFile struct {
	Version int     `json:"version"`
	Entries []Entry `json:"entries"`
}

// Cursor 是游标文件的读写句柄。
type Cursor struct {
	dir     string
	entries map[string]Entry
	rebuilt bool
	dirty   bool
}

// Open 打开 dir 下的游标文件。
//
// 文件不存在、内容不是 JSON、结构版本对不上——这些都算「需要重建」：
// 返回一个空游标，`Err()` 仍是 nil，`Rebuilt()` 为 true（由调用方决定要不要记一笔日志）。
// 游标丢了只是多读一遍，不该让整轮收集失败（验收标准 4）。
//
// 返回错误只在真读不动的时候（如权限不足）。
func Open(dir string) (*Cursor, error) {
	c := &Cursor{dir: dir, entries: map[string]Entry{}}

	// 游标目录被指到一个普通文件上——这是配置错了，得当场报出来。
	// 装作空游标的话，坏消息会推迟到 Save 才炸，而那时已经白读了一遍。
	if info, err := os.Stat(dir); err == nil && !info.IsDir() {
		return nil, fmt.Errorf("游标目录 %s 不是目录", dir)
	}

	raw, err := os.ReadFile(c.path())
	if errors.Is(err, fs.ErrNotExist) {
		c.rebuilt = true
		return c, nil
	}
	if err != nil {
		return nil, fmt.Errorf("读游标 %s: %w", c.path(), err)
	}

	var f cursorFile
	if err := json.Unmarshal(raw, &f); err != nil || f.Version != fileVersion {
		c.rebuilt = true
		return c, nil
	}

	for _, e := range f.Entries {
		if e.Path == "" {
			continue
		}
		c.entries[e.Path] = e
	}
	return c, nil
}

// path 返回游标文件的完整路径。
func (c *Cursor) path() string { return filepath.Join(c.dir, FileName) }

// Rebuilt 报告这次打开是否是从头重建的（文件不在或内容坏了）。
func (c *Cursor) Rebuilt() bool { return c.rebuilt }

// Len 返回游标里的记录条数。
func (c *Cursor) Len() int { return len(c.entries) }

// Check 判断一个源文件要不要处理。
//
// 大小和修改时间都没变 → 不用处理。大小变了 → 要处理。大小相同、只有修改时间
// 变了时先比内容指纹：一致就只刷新游标里的修改时间并返回 ChangeNone（这时
// Dirty 为真，调用方要负责 Save）；不一致返回 ChangeUpdated。
//
// 旧游标没有指纹时返回 ChangeUnknown，由调用方拿现有归档比一次；取不到指纹
// （虚拟路径、文件已删）也按「变了」保守处理。
func (c *Cursor) Check(sf channel.SourceFile) Change {
	prev, seen := c.entries[sf.Path]
	if !seen {
		return ChangeNew
	}
	if prev.Size == sf.Size && prev.ModTime.Equal(sf.ModTime) {
		// 上一轮解析失败、还没到上限：下一轮再试一次（I-048）。
		if prev.Failures > 0 && prev.Failures < maxParseFailures {
			return ChangeUpdated
		}
		return ChangeNone
	}
	if prev.Size != sf.Size {
		c.resetFailures(sf.Path)
		return ChangeUpdated
	}
	if prev.Fingerprint == "" {
		return ChangeUnknown
	}

	now, err := fingerprint(sf.Path)
	if err != nil {
		return ChangeUpdated
	}
	if now != prev.Fingerprint {
		c.resetFailures(sf.Path)
		return ChangeUpdated
	}
	c.adopt(sf, now)
	return ChangeNone
}

// NeedsProcessing 是 Check 的布尔版：只要不是「明确不用处理」都算要处理。
//
// ChangeUnknown 也算要处理——旧游标缺指纹时，不能因为比不了就当作没变。
func (c *Cursor) NeedsProcessing(sf channel.SourceFile) bool {
	return c.Check(sf) != ChangeNone
}

// MarkProcessed 记下这个源文件这一轮已被处理，以及它产出的归档文件。
//
// 同时记下内容指纹；虚拟路径取不到文件内容时指纹留空，下次退回大小 + 修改时间判断。
func (c *Cursor) MarkProcessed(sf channel.SourceFile, archivePath string) {
	fp, _ := fingerprint(sf.Path)
	c.entries[sf.Path] = Entry{
		Path:        sf.Path,
		Size:        sf.Size,
		ModTime:     sf.ModTime,
		Fingerprint: fp,
		ArchivePath: archivePath,
		ProcessedAt: time.Now(),
	}
	c.dirty = true
}

// MarkFailed 记下这个源文件这一轮解析失败，连续失败次数加一。
//
// 失败次数达到上限后 Check 不再返回待处理；文件大小或内容变化会把它归零。
// 归档路径保持原样：之前产出过的归档仍然有效。
func (c *Cursor) MarkFailed(sf channel.SourceFile) {
	prev := c.entries[sf.Path]
	fp, _ := fingerprint(sf.Path)
	c.entries[sf.Path] = Entry{
		Path:        sf.Path,
		Size:        sf.Size,
		ModTime:     sf.ModTime,
		Fingerprint: fp,
		ArchivePath: prev.ArchivePath,
		Failures:    prev.Failures + 1,
		ProcessedAt: time.Now(),
	}
	c.dirty = true
}

// resetFailures 把连续失败次数归零（文件变了，重新给机会）。
func (c *Cursor) resetFailures(path string) {
	prev, ok := c.entries[path]
	if !ok || prev.Failures == 0 {
		return
	}
	prev.Failures = 0
	c.entries[path] = prev
	c.dirty = true
}

// Adopt 认定这个源文件的内容与上次处理时相同，只刷新大小、修改时间与指纹。
//
// 给旧游标的迁移用：上层重新解析、与现有归档逐字节比过，确认没有变化。
// 归档路径与处理时刻不动——这次没有重新处理。
func (c *Cursor) Adopt(sf channel.SourceFile) {
	fp, err := fingerprint(sf.Path)
	if err != nil {
		// 虚拟路径 / 文件读不到：保留原来的指纹（通常是空的），下次仍走保守判断。
		fp = c.entries[sf.Path].Fingerprint
	}
	c.adopt(sf, fp)
}

// adopt 刷新一条记录的元数据，保留归档路径与处理时刻。
func (c *Cursor) adopt(sf channel.SourceFile, fp string) {
	prev := c.entries[sf.Path]
	prev.Path = sf.Path
	prev.Size = sf.Size
	prev.ModTime = sf.ModTime
	prev.Fingerprint = fp
	c.entries[sf.Path] = prev
	c.dirty = true
}

// Entry 返回某个源文件的游标记录。
func (c *Cursor) Entry(path string) (Entry, bool) {
	e, ok := c.entries[path]
	return e, ok
}

// Dirty 报告打开之后有没有改动过（包括 Check 顺手刷新的修改时间）。
func (c *Cursor) Dirty() bool { return c.dirty }

// fingerprint 算一个源文件原始字节的 SHA-256。
//
// 虚拟路径（`<库>#<表>:<会话>`）打不开，返回错误，由调用方退化处理。
func fingerprint(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()

	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// Entries 返回全部记录，按路径排序。
func (c *Cursor) Entries() []Entry {
	out := make([]Entry, 0, len(c.entries))
	for _, e := range c.entries {
		out = append(out, e)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}

// Save 把游标落盘。
//
// 先写同目录的临时文件再改名替换：中途崩了也只会留下一个临时文件，
// 不会让游标本身变成半截 JSON。
func (c *Cursor) Save() error {
	raw, err := json.MarshalIndent(cursorFile{Version: fileVersion, Entries: c.Entries()}, "", "  ")
	if err != nil {
		return fmt.Errorf("序列化游标: %w", err)
	}
	raw = append(raw, '\n')

	if err := os.MkdirAll(c.dir, 0o755); err != nil {
		return fmt.Errorf("建游标目录 %s: %w", c.dir, err)
	}

	tmp := c.path() + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return fmt.Errorf("写游标临时文件 %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, c.path()); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("替换游标文件 %s: %w", c.path(), err)
	}
	c.dirty = false
	return nil
}
