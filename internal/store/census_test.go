package store

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Always1023/AgentMemory/internal/channel"
)

// openTestCensus 开一个临时主库并取统计缓存句柄。
func openTestCensus(t *testing.T) (*DB, *CensusCache) {
	t.Helper()
	db := openTestDB(t)
	cache, err := db.OpenCensus()
	if err != nil {
		t.Fatalf("打开统计缓存: %v", err)
	}
	return db, cache
}

func censusFileSource(path string, size int64, mod time.Time) channel.SourceFile {
	return channel.SourceFile{Path: path, Size: size, ModTime: mod}
}

// 缓存命中要同时看路径、大小、修改时间；任何一项变了都算没命中。
func TestCensusCacheReusesOnlyUnchangedFiles(t *testing.T) {
	mod := time.Now().Add(-time.Hour)
	sf := censusFileSource(`D:\会话\a.jsonl`, 12, mod)

	db, cache := openTestCensus(t)
	if !cache.Rebuilt() {
		t.Error("没有账时应当标成重建")
	}
	if _, ok := cache.Lookup(sf); ok {
		t.Error("空缓存不该命中")
	}

	cache.Mark(sf, CensusValid)
	if err := cache.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}

	again, err := db.OpenCensus()
	if err != nil {
		t.Fatalf("重开缓存: %v", err)
	}
	if again.Rebuilt() {
		t.Error("有账了不该标成重建")
	}
	if state, ok := again.Lookup(sf); !ok || state != CensusValid {
		t.Errorf("未变文件应命中有效，实际 state=%q ok=%v", state, ok)
	}
	if _, ok := again.Lookup(censusFileSource(sf.Path, sf.Size+1, sf.ModTime)); ok {
		t.Error("大小变了不该命中")
	}
	if _, ok := again.Lookup(censusFileSource(sf.Path, sf.Size, sf.ModTime.Add(time.Second))); ok {
		t.Error("修改时间变了不该命中")
	}
}

// 旧 census.json 坏了或版本对不上：按空账导入，不把统计拦住。
func TestCensusCacheRebuildsOnDamage(t *testing.T) {
	for _, body := range []string{
		"{不是 JSON",
		`{"version": 999, "entries": [{"path":"a","size":1,"state":"valid"}]}`,
	} {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, CensusFileName), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		db, err := OpenDB(dir)
		if err != nil {
			t.Fatalf("OpenDB(%q): %v", body, err)
		}
		cache, err := db.OpenCensus()
		if err != nil {
			t.Fatalf("OpenCensus(%q): %v", body, err)
		}
		if !cache.Rebuilt() || cache.Len() != 0 {
			t.Errorf("损坏或旧版本应当重建为空缓存，实际 rebuilt=%v len=%d", cache.Rebuilt(), cache.Len())
		}
		db.Close()
	}
}

// 只剩这次见到的文件：换渠道目录后旧记录不该无限积累。
func TestCensusCacheKeepsOnlySeenFiles(t *testing.T) {
	mod := time.Now().Add(-time.Minute)
	a := censusFileSource("a", 1, mod)
	b := censusFileSource("b", 2, mod)
	_, cache := openTestCensus(t)
	cache.Mark(a, CensusValid)
	cache.Mark(b, CensusEmpty)
	cache.KeepOnly(map[string]bool{"b": true})
	if cache.Len() != 1 {
		t.Fatalf("应当只剩 1 条，实际 %d", cache.Len())
	}
	if _, ok := cache.Lookup(a); ok {
		t.Error("没见到的文件应当被删掉")
	}
	if _, ok := cache.Lookup(b); !ok {
		t.Error("这次见到的文件应当留着")
	}
}

// 解析失败不写缓存：下一轮还要再试（I-048 同类问题）。
func TestCensusCacheIgnoresUnknownState(t *testing.T) {
	_, cache := openTestCensus(t)
	cache.Mark(censusFileSource("a", 1, time.Now()), "failed")
	if cache.Len() != 0 {
		t.Error("失败不该写进缓存")
	}
}
