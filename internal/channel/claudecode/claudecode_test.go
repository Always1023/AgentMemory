package claudecode

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Always1023/AgentMemory/internal/channel"
)

func TestDetectNotInstalledWhenRootMissing(t *testing.T) {
	a := Adapter{Root: filepath.Join(t.TempDir(), "not-there")}

	got := a.Detect()
	if got.Status != channel.StatusNotInstalled {
		t.Fatalf("状态 = %q，想要 %q", got.Status, channel.StatusNotInstalled)
	}
	if got.Err != nil {
		t.Errorf("未安装不是错误: %v", got.Err)
	}
	if len(got.Files) != 0 {
		t.Errorf("未安装不该带文件: %+v", got.Files)
	}
}

func TestDetectFindsProjectJSONLFilesOnly(t *testing.T) {
	root := t.TempDir()
	project := filepath.Join(root, "D--work-demo")
	if err := os.MkdirAll(project, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"a.jsonl", "b.jsonl", "notes.txt"} {
		if err := os.WriteFile(filepath.Join(project, name), []byte("{}\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	// 直接躺在根目录下的 jsonl 不在项目目录里，不算这个渠道的会话。
	if err := os.WriteFile(filepath.Join(root, "stray.jsonl"), []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	got := Adapter{Root: root}.Detect()
	if got.Status != channel.StatusAvailable {
		t.Fatalf("状态 = %q，想要 %q", got.Status, channel.StatusAvailable)
	}
	if got.Root != root {
		t.Errorf("Root = %q，想要 %q", got.Root, root)
	}
	if len(got.Files) != 2 {
		t.Fatalf("发现 %d 个文件，想要 2 个: %+v", len(got.Files), got.Files)
	}
	for _, f := range got.Files {
		base := filepath.Base(f.Path)
		if base == "notes.txt" || base == "stray.jsonl" {
			t.Errorf("不该收进来的文件: %s", f.Path)
		}
		if f.Size == 0 {
			t.Errorf("%s 的大小没填", f.Path)
		}
		if f.ModTime.IsZero() {
			t.Errorf("%s 的修改时间没填", f.Path)
		}
	}
}

func parseFixture(t *testing.T, name string) channel.Session {
	t.Helper()
	path := filepath.Join("testdata", name)
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	session, err := (Adapter{}).Parse(channel.SourceFile{Path: path, Size: info.Size(), ModTime: info.ModTime()})
	if err != nil {
		t.Fatalf("Parse(%s): %v", name, err)
	}
	return session
}

func TestParseProducesUnifiedSession(t *testing.T) {
	session := parseFixture(t, "session.jsonl")

	if session.Channel != ChannelID {
		t.Errorf("渠道 = %q，想要 %q", session.Channel, ChannelID)
	}
	if session.ID != "11111111-2222-3333-4444-555555555555" {
		t.Errorf("会话 ID = %q", session.ID)
	}
	if session.ProjectPath != `D:\work\demo` {
		t.Errorf("项目路径 = %q", session.ProjectPath)
	}
	if !strings.HasSuffix(session.SourceFile, "session.jsonl") {
		t.Errorf("源文件 = %q", session.SourceFile)
	}
	if session.Title != "帮我看一下这个项目的结构" {
		t.Errorf("标题 = %q，想要第一条用户发言", session.Title)
	}

	want := time.Date(2026, 10, 9, 13, 50, 56, 642000000, time.UTC)
	if !session.StartedAt.Equal(want) {
		t.Errorf("开始时间 = %v，想要 %v", session.StartedAt, want)
	}

	// 只有 4 条真消息：mode / attachment / system / last-prompt 都不算。
	if len(session.Messages) != 4 {
		for i, m := range session.Messages {
			t.Logf("  消息 %d: role=%s text=%q tools=%d", i, m.Role, m.Text, len(m.Tools))
		}
		t.Fatalf("消息数 = %d，想要 4", len(session.Messages))
	}

	if m := session.Messages[0]; m.Role != channel.RoleUser || m.Text != "帮我看一下这个项目的结构" {
		t.Errorf("第 1 条 = %+v", m)
	}

	// 第 2 条：只有工具调用；thinking 不进正文。
	m := session.Messages[1]
	if m.Role != channel.RoleAssistant {
		t.Errorf("第 2 条角色 = %q", m.Role)
	}
	if strings.Contains(m.Text, "先列目录看看") {
		t.Error("thinking 不该进正文")
	}
	if len(m.Tools) != 1 {
		t.Fatalf("第 2 条工具调用数 = %d，想要 1", len(m.Tools))
	}
	if m.Tools[0].Name != "Bash" || !strings.Contains(m.Tools[0].Input, "ls -la") {
		t.Errorf("工具调用 = %+v", m.Tools[0])
	}

	// 第 3 条：工具回执。
	if m := session.Messages[2]; m.Role != channel.RoleTool || !strings.Contains(m.Text, "main.go") {
		t.Errorf("第 3 条 = %+v", m)
	}

	// 第 4 条：助手正文。
	if m := session.Messages[3]; m.Role != channel.RoleAssistant || !strings.Contains(m.Text, "Go 项目") {
		t.Errorf("第 4 条 = %+v", m)
	}
}

// 验收标准 2：文件被截断一半时不 panic，整份跳过。
func TestParseRejectsHalfTruncatedFile(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("testdata", "session.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "half.jsonl")
	if err := os.WriteFile(path, raw[:len(raw)/2], 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := (Adapter{}).Parse(channel.SourceFile{Path: path}); err == nil {
		t.Fatal("截断一半的文件应当整份跳过，而不是归档半份对话")
	}
}

func TestParseRejectsBrokenLineAndSaysWhich(t *testing.T) {
	_, err := (Adapter{}).Parse(channel.SourceFile{Path: filepath.Join("testdata", "broken.jsonl")})
	if err == nil {
		t.Fatal("写坏的文件应当整份跳过")
	}
	if !strings.Contains(err.Error(), "第 2 行") {
		t.Errorf("错误应当指出坏在第几行: %v", err)
	}
}

// 空文件（一条消息都没有）是「空会话」：不当错误，交给编排层跳过（I-039）。
// 文件打不开才是错误。
func TestParseEmptyAndMissingFiles(t *testing.T) {
	empty := filepath.Join(t.TempDir(), "empty.jsonl")
	if err := os.WriteFile(empty, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	session, err := (Adapter{}).Parse(channel.SourceFile{Path: empty})
	if err != nil {
		t.Errorf("空会话不该报错: %v", err)
	}
	if session.MessageCount() != 0 {
		t.Errorf("空会话不该有消息，实际 %d 条", session.MessageCount())
	}
	if session.Title == "" {
		t.Error("空会话也要有兜底标题")
	}

	missing := filepath.Join(t.TempDir(), "nope.jsonl")
	if _, err := (Adapter{}).Parse(channel.SourceFile{Path: missing}); err == nil {
		t.Error("文件不存在应当报错")
	}
}

func TestParseNeverPanicsOnGarbage(t *testing.T) {
	path := filepath.Join(t.TempDir(), "garbage.jsonl")
	blob := make([]byte, 4096)
	for i := range blob {
		blob[i] = byte(i % 251)
	}
	if err := os.WriteFile(path, blob, 0o600); err != nil {
		t.Fatal(err)
	}

	// 只要求「有结论、不崩」：要么报错跳过，要么解出点东西，都不许 panic。
	if _, err := (Adapter{}).Parse(channel.SourceFile{Path: path}); err == nil {
		t.Log("乱码文件没报错——可以接受，但不该吃掉别的信息")
	}
}

// 验收标准 3：只读打开，不写渠道目录。
func TestParseDoesNotTouchSourceFile(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("testdata", "session.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "s.jsonl")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := (Adapter{}).Parse(channel.SourceFile{Path: path}); err != nil {
		t.Fatalf("Parse: %v", err)
	}

	after, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if after.Size() != before.Size() || !after.ModTime().Equal(before.ModTime()) {
		t.Errorf("解析动了源文件：大小 %d→%d，修改时间 %v→%v",
			before.Size(), after.Size(), before.ModTime(), after.ModTime())
	}
	now, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(now, raw) {
		t.Error("解析改了源文件内容")
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Errorf("渠道目录里多出了东西: %v", names)
	}
}
