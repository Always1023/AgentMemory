package app

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Always1023/AgentMemory/internal/backup"
	"github.com/Always1023/AgentMemory/internal/channel"
	"github.com/Always1023/AgentMemory/internal/store"
)

// fileAdapter 用文件正文当消息，让「内容变没变」真的影响渲染结果。
type fileAdapter struct {
	id      string
	sources []channel.SourceFile
	empty   bool
}

func (a fileAdapter) Detect() channel.DetectResult {
	// 每次检测都重新读大小与修改时间：收集要看到文件刚刚被改动过。
	files := make([]channel.SourceFile, 0, len(a.sources))
	for _, sf := range a.sources {
		info, err := os.Stat(sf.Path)
		if err != nil {
			return channel.DetectResult{Status: channel.StatusFailed, Root: "/根", Err: err}
		}
		files = append(files, channel.SourceFile{Path: sf.Path, Size: info.Size(), ModTime: info.ModTime()})
	}
	return channel.DetectResult{Status: channel.StatusAvailable, Root: "/根", Files: files}
}

func (a fileAdapter) Parse(sf channel.SourceFile) (channel.Session, error) {
	if a.empty {
		return channel.Session{Channel: a.id, ID: filepath.Base(sf.Path), SourceFile: sf.Path, Title: "空会话"}, nil
	}
	raw, err := os.ReadFile(sf.Path)
	if err != nil {
		return channel.Session{}, err
	}
	return channel.Session{
		Channel:    a.id,
		ID:         filepath.Base(sf.Path),
		Title:      "标题",
		SourceFile: sf.Path,
		Messages:   []channel.Message{{Role: channel.RoleUser, Text: strings.TrimSpace(string(raw))}},
	}, nil
}

// reportFor 造一条「检测到这些文件」的渠道报告。
func reportFor(files []channel.SourceFile) []channel.Report {
	return []channel.Report{{
		ID:           "fake",
		Name:         "假渠道",
		DetectResult: channel.DetectResult{Status: channel.StatusAvailable, Files: files},
	}}
}

// statFile 重新读一遍源文件的大小与修改时间。
func statFile(t *testing.T, path string) channel.SourceFile {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return channel.SourceFile{Path: path, Size: info.Size(), ModTime: info.ModTime()}
}

// stripFingerprints 把游标改写成旧版本的样子（没有 fingerprint 字段）。
func stripFingerprints(t *testing.T, baseDir string) {
	t.Helper()
	path := filepath.Join(baseDir, store.FileName)
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var f struct {
		Version int           `json:"version"`
		Entries []store.Entry `json:"entries"`
	}
	if err := json.Unmarshal(raw, &f); err != nil {
		t.Fatal(err)
	}
	for i := range f.Entries {
		f.Entries[i].Fingerprint = ""
	}
	out, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(out, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
}

// touch 只把修改时间推到未来，字节一个不动。
func touch(t *testing.T, path string, by time.Duration) channel.SourceFile {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	at := info.ModTime().Add(by)
	if err := os.Chtimes(path, at, at); err != nil {
		t.Fatal(err)
	}
	return statFile(t, path)
}

// 收集过一轮之后：新文件算「新会话」，已归档又变了的算「有更新」（I-062）。
func TestPendingByChannelSplitsNewAndUpdated(t *testing.T) {
	root := t.TempDir()
	baseDir := filepath.Join(root, "配置")
	archiveDir := filepath.Join(root, "归档")
	sourceDir := filepath.Join(root, "源")

	files := sourceFiles(t, sourceDir, "a.jsonl", "b.jsonl")
	deps := testDeps(t, baseDir, backup.Result{},
		channel.Entry{ID: "fake", Name: "假渠道", Adapter: fileAdapter{id: "fake", sources: files}})
	logger, _ := testLogger()
	if _, err := Collect(testConfig(archiveDir), logger, deps); err != nil {
		t.Fatalf("Collect: %v", err)
	}

	// a 追加内容（变），b 不动，再冒出一个新文件。
	if err := os.WriteFile(files[0].Path, []byte("（假装这是 a.jsonl）\n又补了一句\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	fresh := sourceFiles(t, sourceDir, "c.jsonl")
	current := []channel.SourceFile{
		statFile(t, files[0].Path),
		statFile(t, files[1].Path),
		fresh[0],
	}

	got, err := PendingByChannel(deps, reportFor(current))
	if err != nil {
		t.Fatalf("PendingByChannel: %v", err)
	}
	pending := got["fake"]
	if pending.New != 1 || pending.Updated != 1 {
		t.Errorf("待收应当是 1 个新会话 + 1 个有更新，实际 %+v", pending)
	}
}

// 旧游标没有指纹：只对「大小相同、修改时间不同」的候选比一次归档，比中才算没变（I-062）。
func TestPendingByChannelMigratesOldCursorOnce(t *testing.T) {
	root := t.TempDir()
	baseDir := filepath.Join(root, "配置")
	archiveDir := filepath.Join(root, "归档")
	sourceDir := filepath.Join(root, "源")

	files := sourceFiles(t, sourceDir, "a.jsonl")
	deps := testDeps(t, baseDir, backup.Result{},
		channel.Entry{ID: "fake", Name: "假渠道", Adapter: fileAdapter{id: "fake", sources: files}})
	logger, _ := testLogger()
	if _, err := Collect(testConfig(archiveDir), logger, deps); err != nil {
		t.Fatalf("Collect: %v", err)
	}
	stripFingerprints(t, baseDir)

	// 只动修改时间：重渲染后与现有归档一致 → 不算待收，并把指纹补回游标。
	touched := touch(t, files[0].Path, 2*time.Hour)
	got, err := PendingByChannel(deps, reportFor([]channel.SourceFile{touched}))
	if err != nil {
		t.Fatalf("PendingByChannel: %v", err)
	}
	if pending := got["fake"]; pending.Total() != 0 {
		t.Errorf("内容没变时不该算待收，实际 %+v", pending)
	}

	cursor, err := store.Open(baseDir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	entry, ok := cursor.Entry(files[0].Path)
	if !ok || entry.Fingerprint == "" {
		t.Error("比中之后应当把指纹补进游标")
	}
	if !entry.ModTime.Equal(touched.ModTime) {
		t.Errorf("比中之后应当刷新修改时间：%v", entry.ModTime)
	}

	// 再把指纹抹掉、把内容换成同样长度：这次归档对不上，算「有更新」。
	if err := os.WriteFile(files[0].Path, []byte("（假装这是 b.jsonl）\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	changed := touch(t, files[0].Path, 4*time.Hour)
	stripFingerprints(t, baseDir)
	got, err = PendingByChannel(deps, reportFor([]channel.SourceFile{changed}))
	if err != nil {
		t.Fatalf("PendingByChannel: %v", err)
	}
	if pending := got["fake"]; pending.New != 0 || pending.Updated != 1 {
		t.Errorf("归档对不上时应当算 1 个有更新，实际 %+v", pending)
	}
}

// 游标里有记录但没有归档（上一轮是空会话）：有内容变化时算「新会话」，不算「已收集有更新」。
func TestPendingByChannelCountsUnarchivedChangeAsNew(t *testing.T) {
	root := t.TempDir()
	baseDir := filepath.Join(root, "配置")
	archiveDir := filepath.Join(root, "归档")
	sourceDir := filepath.Join(root, "源")

	files := sourceFiles(t, sourceDir, "a.jsonl")
	deps := testDeps(t, baseDir, backup.Result{},
		channel.Entry{ID: "fake", Name: "假渠道", Adapter: fileAdapter{id: "fake", sources: files, empty: true}})
	logger, _ := testLogger()
	if _, err := Collect(testConfig(archiveDir), logger, deps); err != nil {
		t.Fatalf("Collect: %v", err)
	}

	cursor, err := store.Open(baseDir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	entry, _ := cursor.Entry(files[0].Path)
	if entry.ArchivePath != "" {
		t.Fatalf("空会话不该有归档，实际 %q", entry.ArchivePath)
	}

	if err := os.WriteFile(files[0].Path, []byte("这次有内容了\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := PendingByChannel(deps, reportFor([]channel.SourceFile{statFile(t, files[0].Path)}))
	if err != nil {
		t.Fatalf("PendingByChannel: %v", err)
	}
	if pending := got["fake"]; pending.New != 1 || pending.Updated != 0 {
		t.Errorf("没归档过的变化应当算新会话，实际 %+v", pending)
	}
}

// I-048：上一轮解析失败、还能重试的文件算「新会话」；连续 3 次失败后不再算待收。
func TestPendingCountsRetryableFailures(t *testing.T) {
	root := t.TempDir()
	baseDir := filepath.Join(root, "配置")
	archiveDir := filepath.Join(root, "归档")
	files := sourceFiles(t, filepath.Join(root, "源"), "坏.jsonl")
	deps := testDeps(t, baseDir, backup.Result{},
		channel.Entry{ID: "fake", Name: "假渠道", Adapter: scriptedAdapter{
			id: "fake", status: channel.StatusAvailable, sources: files,
			bad: map[string]bool{files[0].Path: true},
		}})
	logger, _ := testLogger()

	if _, err := Collect(testConfig(archiveDir), logger, deps); err != nil {
		t.Fatalf("第一轮: %v", err)
	}
	got, err := PendingByChannel(deps, reportFor(files))
	if err != nil {
		t.Fatal(err)
	}
	if pending := got["fake"]; pending.New != 1 || pending.Updated != 0 {
		t.Errorf("失败后下一轮可重试的文件应当算 1 个新会话，实际 %+v", pending)
	}

	// 再失败两次达到上限：不再自动重试，也不算待收。
	for i := 0; i < 2; i++ {
		if _, err := Collect(testConfig(archiveDir), logger, deps); err != nil {
			t.Fatalf("补跑第 %d 轮: %v", i+2, err)
		}
	}
	got, err = PendingByChannel(deps, reportFor(files))
	if err != nil {
		t.Fatal(err)
	}
	if pending := got["fake"]; pending.Total() != 0 {
		t.Errorf("连续失败 3 次后不该再算待收，实际 %+v", pending)
	}
}

// 收集这一侧也要认指纹：只有修改时间变了时，不重新解析、不重写归档（I-062）。
func TestCollectSkipsModTimeOnlyChange(t *testing.T) {
	root := t.TempDir()
	baseDir := filepath.Join(root, "配置")
	archiveDir := filepath.Join(root, "归档")
	sourceDir := filepath.Join(root, "源")

	files := sourceFiles(t, sourceDir, "a.jsonl")
	deps := testDeps(t, baseDir, backup.Result{},
		channel.Entry{ID: "fake", Name: "假渠道", Adapter: fileAdapter{id: "fake", sources: files}})
	logger, _ := testLogger()
	cfg := testConfig(archiveDir)

	first, err := Collect(cfg, logger, deps)
	if err != nil {
		t.Fatalf("第一轮 Collect: %v", err)
	}
	if first.Archived() != 1 {
		t.Fatalf("第一轮应当归档 1 份，实际 %d", first.Archived())
	}

	cursor, err := store.Open(baseDir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	entry, _ := cursor.Entry(files[0].Path)
	before, err := os.Stat(entry.ArchivePath)
	if err != nil {
		t.Fatal(err)
	}

	touch(t, files[0].Path, 2*time.Hour)

	second, err := Collect(cfg, logger, deps)
	if err != nil {
		t.Fatalf("第二轮 Collect: %v", err)
	}
	if second.New() != 0 || second.Archived() != 0 {
		t.Errorf("只有修改时间变化时不该重新收：新增 %d / 归档 %d", second.New(), second.Archived())
	}
	after, err := os.Stat(entry.ArchivePath)
	if err != nil {
		t.Fatal(err)
	}
	if !after.ModTime().Equal(before.ModTime()) {
		t.Error("内容没变时不该重写归档")
	}
}
