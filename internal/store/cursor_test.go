package store

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Always1023/AgentMemory/internal/channel"
)

// countingAdapter 记下自己被读了几次、读了多少字节——用来量「第二轮读了几次」。
type countingAdapter struct {
	reads int
	bytes int64
}

func (a *countingAdapter) Detect() channel.DetectResult {
	return channel.DetectResult{Status: channel.StatusAvailable}
}

func (a *countingAdapter) Parse(sf channel.SourceFile) (channel.Session, error) {
	raw, err := os.ReadFile(sf.Path)
	if err != nil {
		return channel.Session{}, err
	}
	a.reads++
	a.bytes += int64(len(raw))
	return channel.Session{ID: filepath.Base(sf.Path), SourceFile: sf.Path}, nil
}

// makeSourceFiles 在 dir 下造几个真文件，返回它们的源文件描述。
func makeSourceFiles(t *testing.T, dir string, names ...string) []channel.SourceFile {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	var out []channel.SourceFile
	for _, name := range names {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte("内容 "+name+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, channel.SourceFile{Path: path, Size: info.Size(), ModTime: info.ModTime()})
	}
	return out
}

// collectOnce 模拟一轮收集：只对「需要处理」的文件调 Parse。
func collectOnce(t *testing.T, c *Cursor, files []channel.SourceFile, ad *countingAdapter) {
	t.Helper()
	for _, sf := range files {
		if !c.NeedsProcessing(sf) {
			continue
		}
		if _, err := ad.Parse(sf); err != nil {
			t.Fatalf("Parse(%s): %v", sf.Path, err)
		}
		c.MarkProcessed(sf, "归档/"+filepath.Base(sf.Path)+".md")
	}
}

// 验收标准 1：连续两次收集，第二次对未变化文件的读取次数为 0。
func TestSecondCollectReadsNothing(t *testing.T) {
	root := t.TempDir()
	files := makeSourceFiles(t, filepath.Join(root, "sources"), "a.jsonl", "b.jsonl", "c.jsonl")
	cursorDir := filepath.Join(root, "state")

	adapter := &countingAdapter{}

	first, err := Open(cursorDir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if !first.Rebuilt() {
		t.Error("第一次打开（还没有游标文件）应当标为重建")
	}
	collectOnce(t, first, files, adapter)
	if adapter.reads != len(files) {
		t.Fatalf("第一轮读了 %d 次，想要 %d 次", adapter.reads, len(files))
	}
	firstReads, firstBytes := adapter.reads, adapter.bytes
	if err := first.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}

	// 第二轮：重新打开游标，文件一个都没变。
	second, err := Open(cursorDir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if second.Rebuilt() {
		t.Error("第二轮不该是重建——游标就在那儿")
	}
	if second.Len() != len(files) {
		t.Errorf("游标里有 %d 条，想要 %d 条", second.Len(), len(files))
	}
	collectOnce(t, second, files, adapter)

	if got := adapter.reads - firstReads; got != 0 {
		t.Errorf("第二次对未变化的文件读了 %d 次，想要 0 次", got)
	}
	if got := adapter.bytes - firstBytes; got != 0 {
		t.Errorf("第二次又读了 %d 字节，想要 0 字节", got)
	}
	t.Logf("第一轮读 %d 次 / %d 字节；第二轮读 %d 次 / %d 字节",
		firstReads, firstBytes, adapter.reads-firstReads, adapter.bytes-firstBytes)
}

// 验收标准 2：文件修改时间或大小变化后，该文件重新进入待处理。
func TestReprocessesWhenSizeOrModTimeChanges(t *testing.T) {
	dir := t.TempDir()
	base := channel.SourceFile{
		Path:    filepath.Join(dir, "a.jsonl"),
		Size:    100,
		ModTime: time.Date(2026, 1, 5, 10, 0, 0, 0, time.UTC),
	}

	c, err := Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	c.MarkProcessed(base, "归档/a.md")

	if c.NeedsProcessing(base) {
		t.Error("刚记过的文件不该又要处理")
	}

	grown := base
	grown.Size = base.Size + 1
	if !c.NeedsProcessing(grown) {
		t.Error("大小变了应当重新进入待处理")
	}

	touched := base
	touched.ModTime = base.ModTime.Add(time.Second)
	if !c.NeedsProcessing(touched) {
		t.Error("修改时间变了应当重新进入待处理")
	}

	other := base
	other.Path = filepath.Join(dir, "b.jsonl")
	if !c.NeedsProcessing(other) {
		t.Error("没见过的文件应当处理")
	}
}

// 大小相同、只有修改时间变了：内容一致就不重新处理，只刷新游标元数据（I-062）。
func TestFingerprintIgnoresModTimeOnlyChange(t *testing.T) {
	root := t.TempDir()
	files := makeSourceFiles(t, filepath.Join(root, "sources"), "a.jsonl")
	sf := files[0]

	dir := filepath.Join(root, "state")
	c, err := Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	c.MarkProcessed(sf, "归档/a.md")
	entry, ok := c.Entry(sf.Path)
	if !ok || entry.Fingerprint == "" {
		t.Fatalf("处理过之后应当记下内容指纹: %+v", entry)
	}
	if err := c.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}

	// 只把修改时间推到未来，字节一个不动。
	touchedTime := sf.ModTime.Add(2 * time.Hour)
	if err := os.Chtimes(sf.Path, touchedTime, touchedTime); err != nil {
		t.Fatal(err)
	}
	touched := sf
	touched.ModTime = touchedTime

	c2, err := Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if change := c2.Check(touched); change != ChangeNone {
		t.Errorf("内容没变，Check = %v，想要 ChangeNone", change)
	}
	if !c2.Dirty() {
		t.Error("刷新修改时间后应当标为有改动，等调用方 Save")
	}
	if c2.NeedsProcessing(touched) {
		t.Error("内容没变时不该重新处理")
	}

	if err := c2.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}
	c3, err := Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	entry, _ = c3.Entry(sf.Path)
	if !entry.ModTime.Equal(touchedTime) {
		t.Errorf("游标里的修改时间没刷新：%v", entry.ModTime)
	}
	if entry.ProcessedAt.IsZero() {
		t.Error("刷新元数据不该丢掉处理时刻")
	}
}

// 大小相同、内容真的变了：仍然要重新处理（I-062）。
func TestFingerprintDetectsSameSizeContentChange(t *testing.T) {
	root := t.TempDir()
	files := makeSourceFiles(t, filepath.Join(root, "sources"), "a.jsonl")
	sf := files[0]

	c, err := Open(filepath.Join(root, "state"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	c.MarkProcessed(sf, "归档/a.md")

	// 改成同样长度的另一段内容，并把修改时间推到未来。
	if err := os.WriteFile(sf.Path, []byte("内容 b.jsonl\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	touchedTime := sf.ModTime.Add(2 * time.Hour)
	if err := os.Chtimes(sf.Path, touchedTime, touchedTime); err != nil {
		t.Fatal(err)
	}
	changed := sf
	changed.ModTime = touchedTime

	if change := c.Check(changed); change != ChangeUpdated {
		t.Errorf("内容变了，Check = %v，想要 ChangeUpdated", change)
	}
	if !c.NeedsProcessing(changed) {
		t.Error("内容变了应当重新处理")
	}
}

// 旧游标没有指纹：大小相同、修改时间不同时返回 Unknown，交上层比归档；Adopt 后不再处理。
func TestOldCursorWithoutFingerprintIsUnknown(t *testing.T) {
	root := t.TempDir()
	files := makeSourceFiles(t, filepath.Join(root, "sources"), "a.jsonl")
	sf := files[0]

	dir := filepath.Join(root, "state")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	old := fmt.Sprintf(`{"version":1,"entries":[{"path":%q,"size":%d,"mod_time":%q,"archive_path":"归档/a.md"}]}`,
		sf.Path, sf.Size, sf.ModTime.UTC().Format(time.RFC3339Nano))
	if err := os.WriteFile(filepath.Join(dir, FileName), []byte(old), 0o600); err != nil {
		t.Fatal(err)
	}

	touchedTime := sf.ModTime.Add(2 * time.Hour)
	if err := os.Chtimes(sf.Path, touchedTime, touchedTime); err != nil {
		t.Fatal(err)
	}
	touched := sf
	touched.ModTime = touchedTime

	c, err := Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if change := c.Check(touched); change != ChangeUnknown {
		t.Errorf("旧游标缺指纹，Check = %v，想要 ChangeUnknown", change)
	}
	if !c.NeedsProcessing(touched) {
		t.Error("比不了内容时要保守地重新处理")
	}

	c.Adopt(touched)
	if change := c.Check(touched); change != ChangeNone {
		t.Errorf("补过指纹之后 Check = %v，想要 ChangeNone", change)
	}
	entry, _ := c.Entry(sf.Path)
	if entry.Fingerprint == "" {
		t.Error("Adopt 应当补上内容指纹")
	}
}

// 虚拟路径取不到文件内容：记不下指纹，退回大小 + 修改时间判断，不报错（I-062）。
func TestVirtualPathFallsBackToSizeAndModTime(t *testing.T) {
	sf := channel.SourceFile{
		Path:    filepath.Join(t.TempDir(), "opencode.db#session_v2:ses_x"),
		Size:    12,
		ModTime: time.Date(2026, 10, 22, 10, 0, 0, 0, time.UTC),
	}

	c, err := Open(filepath.Join(t.TempDir(), "state"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	c.MarkProcessed(sf, "")
	entry, _ := c.Entry(sf.Path)
	if entry.Fingerprint != "" {
		t.Errorf("虚拟路径不该拿到指纹，实际 %q", entry.Fingerprint)
	}

	if change := c.Check(sf); change != ChangeNone {
		t.Errorf("大小、修改时间都没变，Check = %v，想要 ChangeNone", change)
	}

	touched := sf
	touched.ModTime = sf.ModTime.Add(time.Minute)
	if change := c.Check(touched); change != ChangeUnknown {
		t.Errorf("没有指纹时 Check = %v，想要 ChangeUnknown（交上层保守处理）", change)
	}
	if !c.NeedsProcessing(touched) {
		t.Error("没有指纹、修改时间变了时应当保守地重新处理")
	}
}

// 验收标准 4：游标文件损坏时重建，不导致整轮失败。
func TestCorruptCursorRebuilds(t *testing.T) {
	cases := map[string]string{
		"完全不是 JSON": "这不是 JSON",
		"半截 JSON":   `{"version":1,"entries":[{"path":"x"`,
		"空文件":       "",
		"版本对不上":     `{"version":99,"entries":[{"path":"x","size":1}]}`,
		"顶层是数组":     `[1,2,3]`,
	}

	for name, content := range cases {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, FileName), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}

		c, err := Open(dir)
		if err != nil {
			t.Fatalf("%s：不该让整轮失败: %v", name, err)
		}
		if !c.Rebuilt() {
			t.Errorf("%s：应当标为重建", name)
		}

		sf := channel.SourceFile{Path: filepath.Join(dir, "a.jsonl"), Size: 1, ModTime: time.Unix(0, 0)}
		if !c.NeedsProcessing(sf) {
			t.Errorf("%s：重建后应当什么都待处理", name)
		}
		if err := c.Save(); err != nil {
			t.Errorf("%s：重建后应当能正常存回去: %v", name, err)
		}
	}
}

// 结构还认得，只是某一条记录缺字段：丢掉那一条，其余照用，不算「重建」。
func TestEntryWithoutPathIsDroppedNotRebuilt(t *testing.T) {
	dir := t.TempDir()
	content := `{"version":1,"entries":[{"size":5},{"path":"D:/a.jsonl","size":1,"mod_time":"2026-01-05T10:00:00Z"}]}`
	if err := os.WriteFile(filepath.Join(dir, FileName), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	c, err := Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if c.Rebuilt() {
		t.Error("只是有一条缺字段，不该整份重建")
	}
	if c.Len() != 1 {
		t.Errorf("记录数 = %d，想要 1（缺路径的那条丢掉）", c.Len())
	}
	kept := channel.SourceFile{
		Path: "D:/a.jsonl", Size: 1,
		ModTime: time.Date(2026, 1, 5, 10, 0, 0, 0, time.UTC),
	}
	if c.NeedsProcessing(kept) {
		t.Error("保留下来的那条应当还在起作用")
	}
}

func TestOnlyCorruptFileFailsWholeRunNever(t *testing.T) {
	// 游标路径被指到一个普通文件上：这是配置错了，要报出来。
	// 装作空游标的话，坏消息会推迟到 Save 才炸，且那时已经白读了一遍。
	file := filepath.Join(t.TempDir(), "not-a-dir")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(file); err == nil {
		t.Error("路径不是目录时应当报错，而不是假装空游标")
	}
}

func TestSaveCreatesDirAndLeavesNoTemp(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "state", "nested")

	c, err := Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	c.MarkProcessed(channel.SourceFile{Path: "D:/a.jsonl", Size: 1, ModTime: time.Unix(1, 0)}, "归档/a.md")
	c.MarkProcessed(channel.SourceFile{Path: "D:/b.jsonl", Size: 2, ModTime: time.Unix(2, 0)}, "归档/b.md")
	if err := c.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	if len(names) != 1 || names[0] != FileName {
		t.Errorf("游标目录里应当只有 %s，实际 %v", FileName, names)
	}

	// 再存一次（覆盖已存在的文件）也要正常。
	c.MarkProcessed(channel.SourceFile{Path: "D:/c.jsonl", Size: 3, ModTime: time.Unix(3, 0)}, "")
	if err := c.Save(); err != nil {
		t.Fatalf("第二次 Save: %v", err)
	}

	raw, err := os.ReadFile(filepath.Join(dir, FileName))
	if err != nil {
		t.Fatal(err)
	}
	var f cursorFile
	if err := json.Unmarshal(raw, &f); err != nil {
		t.Fatalf("存出来的不是合法 JSON: %v", err)
	}
	if f.Version != fileVersion {
		t.Errorf("版本 = %d，想要 %d", f.Version, fileVersion)
	}
	if len(f.Entries) != 3 {
		t.Errorf("落盘 %d 条，想要 3 条", len(f.Entries))
	}
}

func TestEntriesSortedAndKeepArchivePath(t *testing.T) {
	dir := t.TempDir()
	c, err := Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	c.MarkProcessed(channel.SourceFile{Path: "D:/c.jsonl", Size: 3, ModTime: time.Unix(3, 0)}, "归档/c.md")
	c.MarkProcessed(channel.SourceFile{Path: "D:/a.jsonl", Size: 1, ModTime: time.Unix(1, 0)}, "归档/a.md")
	c.MarkProcessed(channel.SourceFile{Path: "D:/b.jsonl", Size: 2, ModTime: time.Unix(2, 0)}, "归档/b.md")

	got := c.Entries()
	if len(got) != 3 {
		t.Fatalf("记录数 = %d，想要 3", len(got))
	}
	if got[0].Path != "D:/a.jsonl" || got[2].Path != "D:/c.jsonl" {
		t.Errorf("应当按路径排序，实际 %s … %s", got[0].Path, got[2].Path)
	}
	if got[0].ArchivePath != "归档/a.md" {
		t.Errorf("归档路径没留住: %+v", got[0])
	}
	if got[0].ProcessedAt.IsZero() {
		t.Error("处理时刻没填")
	}
}

// I-048：解析失败的文件下一轮重试；连续 3 次失败后停；文件变化后重新给机会。
func TestFailedFileRetriesThenStopsUntilItChanges(t *testing.T) {
	root := t.TempDir()
	sf := makeSourceFiles(t, filepath.Join(root, "sources"), "a.jsonl")[0]

	c, err := Open(filepath.Join(root, "state"))
	if err != nil {
		t.Fatal(err)
	}

	if change := c.Check(sf); change != ChangeNew {
		t.Fatalf("第一次 Check = %v，想要 ChangeNew", change)
	}

	c.MarkFailed(sf)
	if change := c.Check(sf); change != ChangeUpdated {
		t.Errorf("失败之后下一轮应当重试，Check = %v", change)
	}
	c.MarkFailed(sf)
	c.MarkFailed(sf)
	if change := c.Check(sf); change != ChangeNone {
		t.Errorf("连续失败 %d 次后应当停手，Check = %v", maxParseFailures, change)
	}

	// 文件内容与大小都变了：重新获得 3 次机会。
	if err := os.WriteFile(sf.Path, []byte("内容变长了一些 a.jsonl\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(sf.Path)
	if err != nil {
		t.Fatal(err)
	}
	changed := channel.SourceFile{Path: sf.Path, Size: info.Size(), ModTime: info.ModTime()}
	if change := c.Check(changed); change != ChangeUpdated {
		t.Fatalf("文件变化后 Check = %v，想要 ChangeUpdated", change)
	}
	if entry, _ := c.Entry(sf.Path); entry.Failures != 0 {
		t.Errorf("文件变化后失败次数应当归零，实际 %d", entry.Failures)
	}
	c.MarkFailed(changed)
	if entry, _ := c.Entry(sf.Path); entry.Failures != 1 {
		t.Errorf("重置后再失败应当从 1 开始，实际 %d", entry.Failures)
	}
}

// 成功处理会把失败次数清零（I-048）。
func TestMarkProcessedClearsFailures(t *testing.T) {
	root := t.TempDir()
	sf := makeSourceFiles(t, filepath.Join(root, "sources"), "a.jsonl")[0]

	c, err := Open(filepath.Join(root, "state"))
	if err != nil {
		t.Fatal(err)
	}
	c.MarkFailed(sf)
	c.MarkProcessed(sf, "归档/a.md")

	entry, ok := c.Entry(sf.Path)
	if !ok {
		t.Fatal("游标里应当有记录")
	}
	if entry.Failures != 0 {
		t.Errorf("成功处理之后失败次数应当归零，实际 %d", entry.Failures)
	}
}

func TestOpenMissingCursorIsNotAnError(t *testing.T) {
	c, err := Open(filepath.Join(t.TempDir(), "state"))
	if err != nil {
		t.Fatalf("游标不存在不该报错: %v", err)
	}
	if !c.Rebuilt() {
		t.Error("游标不存在应当标为重建")
	}
	if c.Len() != 0 {
		t.Errorf("应当是空游标，实际 %d 条", c.Len())
	}
}

// 验收标准 3：写入者唯一——除本包外，没有别的包碰这个文件。
//
// 写法是扫源码：除 internal/store 之外，任何 .go 文件都不该出现游标文件名。
// 谁绕过 store 的接口直接拼路径读写，这条就会红。
func TestOnlyThisPackageTouchesTheCursorFile(t *testing.T) {
	root := filepath.Join("..", "..")

	var offenders []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if d.Name() == ".git" || d.Name() == "node_modules" {
				return fs.SkipDir
			}
			return nil
		}
		slashed := filepath.ToSlash(path)
		if filepath.Ext(path) != ".go" || strings.Contains(slashed, "/internal/store/") {
			return nil
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		if strings.Contains(string(raw), FileName) {
			offenders = append(offenders, slashed)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(offenders) > 0 {
		t.Errorf("除 internal/store 外还有地方提到 %s，说明绕过了本包接口: %v", FileName, offenders)
	}
}
