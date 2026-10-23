package factory

import (
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
}

func TestDetectFindsSessionJSONLOnly(t *testing.T) {
	root := t.TempDir()
	project := filepath.Join(root, "-D-work-demo")
	if err := os.MkdirAll(project, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"a.jsonl", "a.settings.json", "notes.txt"} {
		if err := os.WriteFile(filepath.Join(project, name), []byte("{}\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	got := Adapter{Root: root}.Detect()
	if got.Status != channel.StatusAvailable {
		t.Fatalf("状态 = %q，想要 %q", got.Status, channel.StatusAvailable)
	}
	if len(got.Files) != 1 {
		t.Fatalf("发现 %d 个文件，想要 1 个: %+v", len(got.Files), got.Files)
	}
	if base := filepath.Base(got.Files[0].Path); base != "a.jsonl" {
		t.Errorf("收错了文件: %s", base)
	}
}

func parseFixture(t *testing.T, path string) channel.Session {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	session, err := (Adapter{}).Parse(channel.SourceFile{Path: path, Size: info.Size(), ModTime: info.ModTime()})
	if err != nil {
		t.Fatalf("Parse(%s): %v", path, err)
	}
	return session
}

func TestParseProducesUnifiedSession(t *testing.T) {
	session := parseFixture(t, filepath.Join("testdata", "session.jsonl"))

	if session.Channel != ChannelID {
		t.Errorf("渠道 = %q，想要 %q", session.Channel, ChannelID)
	}
	if session.ID != "294db98d-931b-4e6e-bea7-7e076720787f" {
		t.Errorf("会话 ID = %q", session.ID)
	}
	if session.ProjectPath != `D:\work\demo` {
		t.Errorf("项目路径 = %q", session.ProjectPath)
	}
	if session.Title != "看一下这个报错" {
		t.Errorf("标题 = %q，想要跳过 system-reminder 后的第一条用户发言", session.Title)
	}

	want := time.Date(2026, 4, 2, 13, 42, 6, 340000000, time.UTC)
	if !session.StartedAt.Equal(want) {
		t.Errorf("开始时间 = %v，想要 %v", session.StartedAt, want)
	}

	// system-reminder 用户行、用户发言、助手工具调用、工具回执、助手正文。
	// todo_state / compaction_state 不算。
	if len(session.Messages) != 5 {
		for i, m := range session.Messages {
			t.Logf("  消息 %d: role=%s text=%q tools=%d", i, m.Role, m.Text, len(m.Tools))
		}
		t.Fatalf("消息数 = %d，想要 5", len(session.Messages))
	}

	if m := session.Messages[1]; m.Role != channel.RoleUser || m.Text != "看一下这个报错" {
		t.Errorf("第 2 条 = %+v", m)
	}

	m := session.Messages[2]
	if m.Role != channel.RoleAssistant {
		t.Errorf("第 3 条角色 = %q", m.Role)
	}
	if strings.Contains(m.Text, "先读日志") {
		t.Error("thinking 不该进正文")
	}
	if len(m.Tools) != 1 {
		t.Fatalf("第 3 条工具调用数 = %d，想要 1", len(m.Tools))
	}
	if m.Tools[0].Name != "Read" || !strings.Contains(m.Tools[0].Input, "app.log") {
		t.Errorf("工具调用 = %+v", m.Tools[0])
	}

	if m := session.Messages[3]; m.Role != channel.RoleTool || !strings.Contains(m.Text, "line 42") {
		t.Errorf("第 4 条 = %+v", m)
	}

	if m := session.Messages[4]; m.Role != channel.RoleAssistant || !strings.Contains(m.Text, "空指针") {
		t.Errorf("第 5 条 = %+v", m)
	}
}

func TestParseEmptySession(t *testing.T) {
	path := filepath.Join(t.TempDir(), "only-header.jsonl")
	line := `{"type":"session_start","id":"empty-1","title":"New Session","cwd":"D:\\work\\demo"}` + "\n"
	if err := os.WriteFile(path, []byte(line), 0o600); err != nil {
		t.Fatal(err)
	}

	session := parseFixture(t, path)
	if session.MessageCount() != 0 {
		t.Errorf("空会话消息数 = %d，想要 0", session.MessageCount())
	}
	if session.ID != "empty-1" {
		t.Errorf("会话 ID = %q", session.ID)
	}
	if session.Title == "" {
		t.Error("空会话也要有兜底标题")
	}
}

func TestParseRejectsBrokenLine(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("testdata", "session.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "broken.jsonl")
	body := string(raw) + "{not json}\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	_, err = (Adapter{}).Parse(channel.SourceFile{Path: path})
	if err == nil {
		t.Fatal("写坏的行应当让整份会话跳过")
	}
	if !strings.Contains(err.Error(), "第 9 行") {
		t.Errorf("错误应当指出坏在第几行: %v", err)
	}
}

func TestParseMissingFile(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "nope.jsonl")
	if _, err := (Adapter{}).Parse(channel.SourceFile{Path: missing}); err == nil {
		t.Error("文件不存在应当报错")
	}
}

func TestProbeSeesMessages(t *testing.T) {
	path := filepath.Join("testdata", "session.jsonl")
	if ok, err := (Adapter{}).Probe(channel.SourceFile{Path: path}); err != nil || !ok {
		t.Fatalf("Probe = %v, %v，想要 true", ok, err)
	}

	empty := filepath.Join(t.TempDir(), "empty.jsonl")
	if err := os.WriteFile(empty, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if ok, err := (Adapter{}).Probe(channel.SourceFile{Path: empty}); err != nil || ok {
		t.Fatalf("空文件 Probe = %v, %v，想要 false", ok, err)
	}
}
