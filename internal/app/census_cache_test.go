package app

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Always1023/AgentMemory/internal/channel"
	"github.com/Always1023/AgentMemory/internal/store"
)

// probeAdapter 实现轻量探测：Parse 与 Probe 各自计数。
type probeAdapter struct {
	id      string
	sources []channel.SourceFile
	has     bool
	err     error
	parses  *int
	probes  *int
}

func (a probeAdapter) Detect() channel.DetectResult {
	return channel.DetectResult{Status: channel.StatusAvailable, Root: "/根", Files: a.sources}
}

func (a probeAdapter) Parse(sf channel.SourceFile) (channel.Session, error) {
	if a.parses != nil {
		*a.parses++
	}
	return channel.Session{
		Channel:    a.id,
		SourceFile: sf.Path,
		Messages:   []channel.Message{{Role: channel.RoleUser, Text: "正文"}},
	}, nil
}

func (a probeAdapter) Probe(channel.SourceFile) (bool, error) {
	if a.probes != nil {
		*a.probes++
	}
	return a.has, a.err
}

func censusRegistry(t *testing.T, entry channel.Entry) *channel.Registry {
	t.Helper()
	reg := channel.NewRegistry()
	if err := reg.Register(entry); err != nil {
		t.Fatal(err)
	}
	return reg
}

func censusSources(n int) []channel.SourceFile {
	mod := time.Now().Add(-time.Hour)
	out := make([]channel.SourceFile, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, channel.SourceFile{
			Path:    filepath.Join(`D:\会话`, fmt.Sprintf("a-%d.jsonl", i)),
			Size:    int64(10 + i),
			ModTime: mod,
		})
	}
	return out
}

// 第一次走探测、第二次走缓存、改过的文件只再探测一次。
func TestCensusUsesProbeThenCache(t *testing.T) {
	files := censusSources(3)
	cacheDir := t.TempDir()
	probes, parses := 0, 0
	entry := channel.Entry{
		ID:   "probe",
		Name: "探测渠道",
		Adapter: probeAdapter{
			id:      "probe",
			sources: files,
			has:     true,
			parses:  &parses,
			probes:  &probes,
		},
	}
	deps := Deps{Channels: censusRegistry(t, entry), BaseDir: cacheDir}

	first, err := Census(deps, nil)
	if err != nil {
		t.Fatalf("Census: %v", err)
	}
	if first.Valid["probe"] != 3 || first.Empty["probe"] != 0 {
		t.Errorf("第一次应当 3 条有效，实际 %+v", first)
	}
	if probes != 3 || parses != 0 {
		t.Errorf("第一次应当探测 3 次、解析 0 次，实际 probes=%d parses=%d", probes, parses)
	}

	second, err := Census(deps, nil)
	if err != nil {
		t.Fatalf("Census second: %v", err)
	}
	if second.Valid["probe"] != 3 {
		t.Errorf("第二次应当仍是 3 条有效，实际 %+v", second)
	}
	if probes != 3 || parses != 0 {
		t.Errorf("未变文件应当全部命中缓存，实际 probes=%d parses=%d", probes, parses)
	}

	// 改一个文件的大小：只该重新探测这一个。
	files[0].Size++
	third, err := Census(deps, nil)
	if err != nil {
		t.Fatalf("Census third: %v", err)
	}
	if third.Valid["probe"] != 3 {
		t.Errorf("第三次应当仍是 3 条有效，实际 %+v", third)
	}
	if probes != 4 || parses != 0 {
		t.Errorf("改过一个文件应当只多探测一次，实际 probes=%d parses=%d", probes, parses)
	}
}

// 探测失败不写缓存：下一轮还要再试（数据错不能变成永久结论）。
func TestCensusProbeFailureIsNotCached(t *testing.T) {
	files := censusSources(1)
	cacheDir := t.TempDir()
	probes := 0
	entry := channel.Entry{
		ID:   "probe",
		Name: "探测渠道",
		Adapter: probeAdapter{
			id:      "probe",
			sources: files,
			err:     errors.New("库被占着"),
			probes:  &probes,
		},
	}
	deps := Deps{Channels: censusRegistry(t, entry), BaseDir: cacheDir}

	first, err := Census(deps, nil)
	if err != nil {
		t.Fatalf("Census: %v", err)
	}
	if first.Failed["probe"] != 1 {
		t.Errorf("探测失败应当计 1 条失败，实际 %+v", first)
	}
	if probes != 1 {
		t.Errorf("第一次应当探测 1 次，实际 %d", probes)
	}
	if _, err := Census(deps, nil); err != nil {
		t.Fatalf("Census second: %v", err)
	}
	if probes != 2 {
		t.Errorf("失败不该进缓存，第二次应当再探测一次，实际 %d", probes)
	}
}

// 没实现 Probe 的渠道退回 Parse；第二次同样走缓存。
func TestCensusFallsBackToParseAndCaches(t *testing.T) {
	files := censusSources(2)
	cacheDir := t.TempDir()
	parses := 0
	entry := channel.Entry{
		ID:   "plain",
		Name: "普通渠道",
		Adapter: scriptedAdapter{
			id:      "plain",
			status:  channel.StatusAvailable,
			sources: files,
			parses:  &parses,
		},
	}
	deps := Deps{Channels: censusRegistry(t, entry), BaseDir: cacheDir}

	if _, err := Census(deps, nil); err != nil {
		t.Fatalf("Census: %v", err)
	}
	if parses != 2 {
		t.Fatalf("第一次应当解析 2 次，实际 %d", parses)
	}
	if _, err := Census(deps, nil); err != nil {
		t.Fatalf("Census second: %v", err)
	}
	if parses != 2 {
		t.Errorf("未变文件应当命中缓存，实际又解析到 %d 次", parses)
	}
}

// 统计缓存不能写进收集游标：未归档的文件仍要能被收集。
func TestCensusCacheDoesNotTouchCollectCursor(t *testing.T) {
	files := censusSources(1)
	cacheDir := t.TempDir()
	entry := channel.Entry{
		ID:      "probe",
		Name:    "探测渠道",
		Adapter: probeAdapter{id: "probe", sources: files, has: true},
	}
	if _, err := Census(Deps{Channels: censusRegistry(t, entry), BaseDir: cacheDir}, nil); err != nil {
		t.Fatalf("Census: %v", err)
	}

	cursor, err := store.Open(cacheDir)
	if err != nil {
		t.Fatalf("Open cursor: %v", err)
	}
	if cursor.Len() != 0 {
		t.Errorf("统计读取不该写收集游标，实际有 %d 条", cursor.Len())
	}
}

// 坏掉的统计缓存按空缓存重来，不影响本次统计。
func TestCensusCacheDamageFallsBack(t *testing.T) {
	files := censusSources(1)
	cacheDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(cacheDir, store.CensusFileName), []byte("{不是 JSON"), 0o600); err != nil {
		t.Fatal(err)
	}
	probes := 0
	entry := channel.Entry{
		ID:      "probe",
		Name:    "探测渠道",
		Adapter: probeAdapter{id: "probe", sources: files, has: true, probes: &probes},
	}
	got, err := Census(Deps{Channels: censusRegistry(t, entry), BaseDir: cacheDir}, nil)
	if err != nil {
		t.Fatalf("Census: %v", err)
	}
	if got.Valid["probe"] != 1 || probes != 1 {
		t.Errorf("坏缓存应当退化为探测，实际 %+v probes=%d", got, probes)
	}
}
