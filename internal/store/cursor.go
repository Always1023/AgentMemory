// Package store 维护本机的账本：L1 事件流（l1.go）、渠道源文件的增量水位
// （本文件）、首页统计缓存（census.go）与运行记录（runs.go）。
//
// 一句话：水位记住「这个文件上次处理时的样子」，下次没变就不再读它；
// 运行记录一行一次收集，界面按天读回去。
//
// 本机实测 Codex 一个渠道就有 563.9 MB / 256 个文件，没有增量就得每天全量重读。
//
// 依赖方向（docs/ARCHITECTURE.md 模块表）：只依赖共享的会话模型类型，
// 不 import config——主库放哪由调用方传入。
package store

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/Always1023/AgentMemory/internal/channel"
)

// FileName 是旧版游标文件名（index.json）。升级后它只被**读一次**用于导入主库，
// 不再有写入者；常量保留，给「只有本包碰它」的守卫用例用。
const FileName = "index.json"

// legacyFileVersion 是旧游标文件的结构版本。
const legacyFileVersion = 1

// maxParseFailures 是同一个源文件连续解析失败的次数上限（I-048）。
//
// 达到之后不再自动重试，直到文件大小或内容变化；手动「重试」不受此限。
const maxParseFailures = 3

// Entry 是一条水位记录：某个源文件上次处理时的样子，以及它产出的导出文件。
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
	// ArchivePath 是这份源文件产出的导出文件；还没导出时为空。
	ArchivePath string `json:"archive_path,omitempty"`
	// ProcessedAt 是上次处理的时刻。
	ProcessedAt time.Time `json:"processed_at"`
}

// Change 是一次增量判定的结果。
type Change int

const (
	// ChangeNone 不用处理：水位里有它，大小、修改时间与内容都没变。
	ChangeNone Change = iota
	// ChangeNew 水位里没有这个源文件，从来没有处理过。
	ChangeNew
	// ChangeUpdated 源文件变了：大小变了、内容变了，或者认不出内容变没变
	// （取不到指纹），保守当作变了。
	ChangeUpdated
	// ChangeUnknown 旧账没有内容指纹，大小相同、只有修改时间不同。
	// 上层要拿现有导出比一次才能下结论（见 app 的待收统计）。
	ChangeUnknown
)

// cursorFile 是旧游标文件的磁盘形态，只用于导入。
type cursorFile struct {
	Version int     `json:"version"`
	Entries []Entry `json:"entries"`
}

// Cursor 是增量水位的读写句柄。账在内存里改，Save 时经 persist 出口
// 落进主库（生产）或测试桩；旧的 index.json 只在首次导入时读一次。
type Cursor struct {
	entries map[string]Entry
	rebuilt bool
	dirty   bool
	persist func([]Entry) error
	// force 为真时不说「不用处理」：换代回填完成前，L1 还没有这些文件的内容，
	// 水位只用来保数字（I-082 验收反馈）。
	force bool
}

// newCursor 用已知记录构造；测试与导入共用。
func newCursor(entries map[string]Entry, rebuilt bool, persist func([]Entry) error) *Cursor {
	if entries == nil {
		entries = map[string]Entry{}
	}
	return &Cursor{entries: entries, rebuilt: rebuilt, persist: persist}
}

// NewCursor 用给定记录构造水位句柄（测试与迁移工具用）；persist 决定 Save 落到哪。
func NewCursor(entries []Entry, persist func([]Entry) error) *Cursor {
	m := map[string]Entry{}
	for _, e := range entries {
		if e.Path == "" {
			continue
		}
		m[e.Path] = e
	}
	return newCursor(m, false, persist)
}

// OpenCursor 从主库打开水位。旧 index.json 的只读导入集中在 OpenDB 的
// importLedgers 里做，这里只管从主库装账。
func (d *DB) OpenCursor() (*Cursor, error) {
	entries, err := d.loadWatermark()
	if err != nil {
		return nil, err
	}
	c := newCursor(entries, len(entries) == 0, d.flushWatermark)
	// 换代回填：库里的水位是旧 JSON 搬来的，L1 却还是空的，这一轮全读一遍。
	c.force = !d.L1Backfilled()
	return c, nil
}

// Forced 报告这次打开是否在「换代回填」——不看水位，全部文件都会重读。
func (c *Cursor) Forced() bool { return c.force }

// Rebuilt 报告这次打开是否是空的（主库没账、旧文件也没有或读不出）。
func (c *Cursor) Rebuilt() bool { return c.rebuilt }

// Len 返回水位里的记录条数。
func (c *Cursor) Len() int { return len(c.entries) }

// Check 判断一个源文件要不要处理。
//
// 大小和修改时间都没变 → 不用处理。大小变了 → 要处理。大小相同、只有修改时间
// 变了时先比内容指纹：一致就只刷新水位里的修改时间并返回 ChangeNone（这时
// Dirty 为真，调用方要负责 Save）；不一致返回 ChangeUpdated。
//
// 旧账没有指纹时返回 ChangeUnknown，由调用方拿现有导出比一次；取不到指纹
// （虚拟路径、文件已删）也按「变了」保守处理。
func (c *Cursor) Check(sf channel.SourceFile) Change {
	prev, seen := c.entries[sf.Path]
	if !seen {
		return ChangeNew
	}
	// 换代回填：见识过的文件也要再读一遍（L1 里还没有它的内容）。
	if c.force {
		return ChangeUpdated
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
// ChangeUnknown 也算要处理——旧账缺指纹时，不能因为比不了就当作没变。
func (c *Cursor) NeedsProcessing(sf channel.SourceFile) bool {
	return c.Check(sf) != ChangeNone
}

// MarkProcessed 记下这个源文件这一轮已被处理，以及它产出的导出文件。
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
// 导出路径保持原样：之前产出过的导出仍然有效。
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
// 给旧账的迁移用：上层重新解析、与现有导出逐字节比过，确认没有变化。
// 导出路径与处理时刻不动——这次没有重新处理。
func (c *Cursor) Adopt(sf channel.SourceFile) {
	fp, err := fingerprint(sf.Path)
	if err != nil {
		// 虚拟路径 / 文件读不到：保留原来的指纹（通常是空的），下次仍走保守判断。
		fp = c.entries[sf.Path].Fingerprint
	}
	c.adopt(sf, fp)
}

// adopt 刷新一条记录的元数据，保留导出路径与处理时刻。
func (c *Cursor) adopt(sf channel.SourceFile, fp string) {
	prev := c.entries[sf.Path]
	prev.Path = sf.Path
	prev.Size = sf.Size
	prev.ModTime = sf.ModTime
	prev.Fingerprint = fp
	c.entries[sf.Path] = prev
	c.dirty = true
}

// Entry 返回某个源文件的水位记录。
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

// Save 把水位经 persist 出口落盘（生产是主库 watermark 表，整表替换）。
func (c *Cursor) Save() error {
	if c.persist == nil {
		return nil
	}
	if err := c.persist(c.Entries()); err != nil {
		return err
	}
	c.dirty = false
	return nil
}

// readLegacyCursor 读旧 index.json。文件不在返回 ok = false；
// 读坏了、版本对不上都返回空账（ok = true，entries 为空）——
// 导入只是加速，坏了重收一遍就是，不让整轮失败。
func readLegacyCursor(dir string) (entries map[string]Entry, ok bool, err error) {
	if info, statErr := os.Stat(dir); statErr == nil && !info.IsDir() {
		return nil, false, fmt.Errorf("数据目录 %s 不是目录", dir)
	}
	raw, readErr := os.ReadFile(filepath.Join(dir, FileName))
	if readErr != nil {
		if errors.Is(readErr, os.ErrNotExist) {
			return nil, false, nil
		}
		return nil, false, fmt.Errorf("读旧游标 %s: %w", dir, readErr)
	}
	entries = map[string]Entry{}
	var f cursorFile
	if json.Unmarshal(raw, &f) != nil || f.Version != legacyFileVersion {
		return entries, true, nil
	}
	for _, e := range f.Entries {
		if e.Path == "" {
			continue
		}
		entries[e.Path] = e
	}
	return entries, true, nil
}
