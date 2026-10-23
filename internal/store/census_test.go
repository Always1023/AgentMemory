package store

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Always1023/AgentMemory/internal/channel"
)

func censusFileSource(path string, size int64, mod time.Time) channel.SourceFile {
	return channel.SourceFile{Path: path, Size: size, ModTime: mod}
}

// 缓存命中要同时看路径、大小、修改时间；任何一项变了都算没命中。
func TestCensusCacheReusesOnlyUnchangedFiles(t *testing.T) {
	dir := t.TempDir()
	mod := time.Now().Add(-time.Hour)
	sf := censusFileSource(`D:\会话\a.jsonl`, 12, mod)

	cache, err := OpenCensus(dir)
	if err != nil {
		t.Fatalf("OpenCensus: %v", err)
	}
	if !cache.Rebuilt() {
		t.Error("文件不在时应当标成重建")
	}
	if _, ok := cache.Lookup(sf); ok {
		t.Error("空缓存不该命中")
	}

	cache.Mark(sf, CensusValid)
	if err := cache.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}

	again, err := OpenCensus(dir)
	if err != nil {
		t.Fatalf("OpenCensus again: %v", err)
	}
	if again.Rebuilt() {
		t.Error("正常缓存不该标成重建")
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

// 坏文件与旧版本都按空缓存重建，不把整个界面拦住。
func TestCensusCacheRebuildsOnDamage(t *testing.T) {
	for _, body := range []string{
		"{不是 JSON",
		`{"version": 999, "entries": [{"path":"a","size":1,"state":"valid"}]}`,
	} {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, CensusFileName), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		cache, err := OpenCensus(dir)
		if err != nil {
			t.Fatalf("OpenCensus(%q): %v", body, err)
		}
		if !cache.Rebuilt() || cache.Len() != 0 {
			t.Errorf("损坏或旧版本应当重建为空缓存，实际 rebuilt=%v len=%d", cache.Rebuilt(), cache.Len())
		}
	}
}

// 只剩这次见到的文件：换渠道目录后旧记录不该无限积累。
func TestCensusCacheKeepsOnlySeenFiles(t *testing.T) {
	dir := t.TempDir()
	mod := time.Now().Add(-time.Minute)
	a := censusFileSource("a", 1, mod)
	b := censusFileSource("b", 2, mod)
	cache, err := OpenCensus(dir)
	if err != nil {
		t.Fatal(err)
	}
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
	dir := t.TempDir()
	cache, err := OpenCensus(dir)
	if err != nil {
		t.Fatal(err)
	}
	cache.Mark(censusFileSource("a", 1, time.Now()), "failed")
	if cache.Len() != 0 {
		t.Error("失败不该写进缓存")
	}
}
