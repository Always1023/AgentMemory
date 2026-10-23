package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/Always1023/AgentMemory/internal/channel"
)

// CensusFileName 是首页统计缓存的文件名。
//
// 它与收集游标 index.json 分开：游标记的是「这个文件已经处理进归档」，
// 统计缓存只记「这个文件里有没有会话消息」——混在一起会让收集跳过还没归档的文件。
const CensusFileName = "census.json"

// censusVersion 是统计缓存的结构版本；对不上就按空缓存重建。
const censusVersion = 1

const (
	// CensusValid 表示这个源文件解析后至少有一条消息。
	CensusValid = "valid"
	// CensusEmpty 表示这个源文件没有任何会话消息（打开过没说话）。
	CensusEmpty = "empty"
)

// CensusEntry 是一条首页统计缓存：某个源文件上次判定时的样子与结论。
type CensusEntry struct {
	Path      string    `json:"path"`
	Size      int64     `json:"size"`
	ModTime   time.Time `json:"mod_time"`
	State     string    `json:"state"`
	CheckedAt time.Time `json:"checked_at"`
}

type censusFile struct {
	Version int           `json:"version"`
	Entries []CensusEntry `json:"entries"`
}

// CensusCache 是首页统计缓存的读写句柄。
type CensusCache struct {
	dir     string
	entries map[string]CensusEntry
	rebuilt bool
}

// OpenCensus 打开 dir 下的统计缓存。
//
// 文件不在、读坏了、版本对不上，都按空缓存重建——它只是加速用的缓存，
// 重读一遍不会有任何数据损失。只有路径被指到普通文件上时才算错。
func OpenCensus(dir string) (*CensusCache, error) {
	c := &CensusCache{dir: dir, entries: map[string]CensusEntry{}}

	if info, err := os.Stat(dir); err == nil && !info.IsDir() {
		return nil, fmt.Errorf("统计缓存目录 %s 不是目录", dir)
	}

	raw, err := os.ReadFile(c.path())
	if errors.Is(err, fs.ErrNotExist) {
		c.rebuilt = true
		return c, nil
	}
	if err != nil {
		return nil, fmt.Errorf("读统计缓存 %s: %w", c.path(), err)
	}

	var f censusFile
	if err := json.Unmarshal(raw, &f); err != nil || f.Version != censusVersion {
		c.rebuilt = true
		return c, nil
	}

	for _, e := range f.Entries {
		if e.Path == "" || (e.State != CensusValid && e.State != CensusEmpty) {
			continue
		}
		c.entries[e.Path] = e
	}
	return c, nil
}

func (c *CensusCache) path() string { return filepath.Join(c.dir, CensusFileName) }

// Rebuilt 报告这次打开是否从头重建（文件不在或内容坏了）。
func (c *CensusCache) Rebuilt() bool {
	if c == nil {
		return false
	}
	return c.rebuilt
}

// Len 返回缓存里的记录条数。
func (c *CensusCache) Len() int {
	if c == nil {
		return 0
	}
	return len(c.entries)
}

// Lookup 查一个源文件的旧结论；大小或修改时间变了就当没有。
func (c *CensusCache) Lookup(sf channel.SourceFile) (string, bool) {
	if c == nil {
		return "", false
	}
	prev, seen := c.entries[sf.Path]
	if !seen || prev.Size != sf.Size || !prev.ModTime.Equal(sf.ModTime) {
		return "", false
	}
	if prev.State != CensusValid && prev.State != CensusEmpty {
		return "", false
	}
	return prev.State, true
}

// Mark 记下这个源文件这次的结论（只记有效 / 空；解析失败不要写，下一轮还要重试）。
func (c *CensusCache) Mark(sf channel.SourceFile, state string) {
	if c == nil || (state != CensusValid && state != CensusEmpty) {
		return
	}
	c.entries[sf.Path] = CensusEntry{
		Path:      sf.Path,
		Size:      sf.Size,
		ModTime:   sf.ModTime,
		State:     state,
		CheckedAt: time.Now(),
	}
}

// KeepOnly 删掉这次没见到的源文件，避免换了渠道目录后旧记录无限积累。
func (c *CensusCache) KeepOnly(seen map[string]bool) {
	if c == nil {
		return
	}
	for path := range c.entries {
		if !seen[path] {
			delete(c.entries, path)
		}
	}
}

// Entries 返回全部记录，按路径排序。
func (c *CensusCache) Entries() []CensusEntry {
	if c == nil {
		return nil
	}
	out := make([]CensusEntry, 0, len(c.entries))
	for _, e := range c.entries {
		out = append(out, e)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}

// Save 把统计缓存落盘（先写临时文件再改名替换）。
func (c *CensusCache) Save() error {
	if c == nil {
		return nil
	}
	raw, err := json.MarshalIndent(censusFile{Version: censusVersion, Entries: c.Entries()}, "", "  ")
	if err != nil {
		return fmt.Errorf("序列化统计缓存: %w", err)
	}
	raw = append(raw, '\n')

	if err := os.MkdirAll(c.dir, 0o755); err != nil {
		return fmt.Errorf("建统计缓存目录 %s: %w", c.dir, err)
	}

	tmp := c.path() + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return fmt.Errorf("写统计缓存临时文件 %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, c.path()); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("替换统计缓存文件 %s: %w", c.path(), err)
	}
	return nil
}
