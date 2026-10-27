package store

// 首页统计缓存（原 census.json，现为主库 census 表）：按源文件复用
// 「有效 / 空」的解析结论。它与收集水位分开：水位记的是「这个文件已经
// 处理进库」，统计缓存只记「这个文件里有没有会话消息」——混在一起会让
// 收集跳过还没入库的文件。
//
// 旧 census.json 只在首次导入时读一次，不再有写入者。

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/Always1023/AgentMemory/internal/channel"
)

// CensusFileName 是旧统计缓存的文件名；升级后只读导入一次。
const CensusFileName = "census.json"

// legacyCensusVersion 是旧统计缓存的结构版本；对不上就按空缓存导入。
const legacyCensusVersion = 1

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

// CensusCache 是首页统计缓存的读写句柄。账在内存里改，Save 经 persist 出口
// 落进主库；旧 JSON 只在导入时读一次。所有方法都可 Nil 安全——
// 测试里注入了渠道又没给数据目录时，调用方直接不开缓存。
type CensusCache struct {
	entries map[string]CensusEntry
	rebuilt bool
	persist func([]CensusEntry) error
}

// OpenCensus 从主库打开统计缓存。旧 census.json 的只读导入集中在 OpenDB 的
// importLedgers 里做，这里只管从主库装账。
func (d *DB) OpenCensus() (*CensusCache, error) {
	entries, err := d.loadCensus()
	if err != nil {
		return nil, err
	}
	return &CensusCache{entries: entries, rebuilt: len(entries) == 0, persist: d.flushCensus}, nil
}

// Rebuilt 报告这次打开是否从头重建（主库没账、旧文件也没有或读不出）。
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

// Save 把统计缓存经 persist 出口落盘（生产是主库 census 表，整表替换）。
func (c *CensusCache) Save() error {
	if c == nil || c.persist == nil {
		return nil
	}
	if err := c.persist(c.Entries()); err != nil {
		return err
	}
	return nil
}

// readLegacyCensus 读旧 census.json。文件不在返回 ok = false；
// 读坏了、版本对不上都返回空账——缓存坏了重建，没有数据损失。
func readLegacyCensus(dir string) (entries map[string]CensusEntry, ok bool, err error) {
	raw, readErr := os.ReadFile(filepath.Join(dir, CensusFileName))
	if readErr != nil {
		if errors.Is(readErr, os.ErrNotExist) {
			return nil, false, nil
		}
		return nil, false, fmt.Errorf("读旧统计缓存 %s: %w", dir, readErr)
	}
	entries = map[string]CensusEntry{}
	var f censusFile
	if json.Unmarshal(raw, &f) != nil || f.Version != legacyCensusVersion {
		return entries, true, nil
	}
	for _, e := range f.Entries {
		if e.Path == "" || (e.State != CensusValid && e.State != CensusEmpty) {
			continue
		}
		entries[e.Path] = e
	}
	return entries, true, nil
}
