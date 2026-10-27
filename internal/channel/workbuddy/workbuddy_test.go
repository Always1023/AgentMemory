package workbuddy

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Always1023/AgentMemory/internal/channel"
)

const fixtureFile = "workbuddy.jsonl"

// writeFixture 把 testdata 里的夹具拷进一个临时的「projects/<项目>/」结构，
// 好让 Detect 走真实的那套目录规则。
func writeFixture(t *testing.T, project, name string) string {
	t.Helper()

	raw, err := os.ReadFile(filepath.Join("testdata", fixtureFile))
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(t.TempDir(), project)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// writeSession 造一个只有给定行的会话文件（用来验空会话、坏行这些边界）。
func writeSession(t *testing.T, lines ...string) string {
	t.Helper()

	dir := filepath.Join(t.TempDir(), "d-Coding-demo")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "sess-demo.jsonl")
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestDetectFindsSessions(t *testing.T) {
	root := t.TempDir()
	for _, project := range []string{"d-Coding-alpha", "c-Users-Always-WorkBuddy AI-2026-10-05"} {
		dir := filepath.Join(root, project)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		raw, err := os.ReadFile(filepath.Join("testdata", fixtureFile))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "sess.jsonl"), raw, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// 顺手放一个不该被认的文件：非 .jsonl。
	if err := os.WriteFile(filepath.Join(root, "d-Coding-alpha", "notes.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	got := Adapter{Root: root}.Detect()
	if got.Status != channel.StatusAvailable {
		t.Fatalf("状态 = %q，想要 available（err=%v）", got.Status, got.Err)
	}
	if len(got.Files) != 2 {
		t.Fatalf("发现 %d 个会话文件，想要 2", len(got.Files))
	}
	for _, f := range got.Files {
		if !strings.HasSuffix(f.Path, ".jsonl") {
			t.Errorf("只该认 .jsonl：%s", f.Path)
		}
		if f.Size <= 0 || f.ModTime.IsZero() {
			t.Errorf("%s 的大小 / 修改时间没填：%d / %v", f.Path, f.Size, f.ModTime)
		}
	}
	if got.Files[0].Path > got.Files[1].Path {
		t.Error("结果应当按路径排序")
	}
}

func TestDetectNotInstalledWhenRootMissing(t *testing.T) {
	got := Adapter{Root: filepath.Join(t.TempDir(), "没有这个目录")}.Detect()
	if got.Status != channel.StatusNotInstalled {
		t.Fatalf("状态 = %q，想要 not_installed", got.Status)
	}
	if got.Err != nil {
		t.Errorf("没装不是错误: %v", got.Err)
	}
}

func TestParseSession(t *testing.T) {
	path := writeFixture(t, "d-Coding-logseq_ai", "a7ec9a44-0ac1-45f5-b6e2-7c1ad0410d0f.jsonl")
	session, err := Adapter{}.Parse(channel.SourceFile{Path: path})
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	if session.Channel != ChannelID {
		t.Errorf("渠道 = %q，想要 %q", session.Channel, ChannelID)
	}
	if session.ID != "a7ec9a44-0ac1-45f5-b6e2-7c1ad0410d0f" {
		t.Errorf("会话 ID = %q", session.ID)
	}
	// 标题优先用模型起的那条（ai-title），不是用户发言。
	if session.Title != "调研为 Logseq 增加 AI 插件" {
		t.Errorf("标题 = %q，想要 ai-title 那条", session.Title)
	}
	if session.ProjectPath != `d:\Coding\logseq_ai` {
		t.Errorf("项目路径 = %q", session.ProjectPath)
	}
	if !session.StartedAt.Equal(time.UnixMilli(1791968660003)) {
		t.Errorf("开始时间 = %v，应当是文件里第一个毫秒时间戳", session.StartedAt)
	}
	if session.SourceFile != path {
		t.Errorf("源文件 = %q，想要 %q", session.SourceFile, path)
	}

	// 工具调用挂回它所属的那条消息：line 11 的 Bash 挂在 line 10 那条助手消息上，
	// line 13 的回执靠 callId 补进同一个调用的 Output。
	// line 12 的 WebSearch 的 parentId 是上一个调用（不是消息），所以单起一条助手消息装它。
	var bash, search, fetch *channel.ToolCall
	for i := range session.Messages {
		for j := range session.Messages[i].Tools {
			switch session.Messages[i].Tools[j].Name {
			case "Bash":
				bash = &session.Messages[i].Tools[j]
			case "WebSearch":
				search = &session.Messages[i].Tools[j]
			case "WebFetch":
				fetch = &session.Messages[i].Tools[j]
			}
		}
	}
	if bash == nil || search == nil || fetch == nil {
		t.Fatalf("三次工具调用没都读出来：bash=%v search=%v fetch=%v", bash != nil, search != nil, fetch != nil)
	}
	if !strings.Contains(bash.Input, "ls -la") {
		t.Errorf("Bash 入参 = %q", bash.Input)
	}
	if !strings.Contains(bash.Output, "Stdout") {
		t.Errorf("Bash 的输出没按 callId 补回来: %q", bash.Output)
	}
	if !strings.Contains(search.Output, "Search Results") {
		t.Errorf("WebSearch 的输出没补回来: %q", search.Output)
	}
	if search.Output == bash.Output {
		t.Error("两次调用的输出串了")
	}

	// 正文：一条用户消息 + 助手消息，内部记录（reasoning / file-history-snapshot /
	// session-meta）都不该变成消息。
	var user, assistant int
	for _, msg := range session.Messages {
		switch msg.Role {
		case channel.RoleUser:
			user++
			if !strings.Contains(msg.Text, "system-reminder") {
				t.Errorf("用户消息正文不对: %q", msg.Text)
			}
		case channel.RoleAssistant:
			assistant++
		}
	}
	if user != 1 || assistant != 3 {
		t.Errorf("用户消息 %d 条（想要 1）、助手消息 %d 条（想要 3）", user, assistant)
	}
	for _, msg := range session.Messages {
		if msg.Time.IsZero() {
			t.Errorf("消息没时间：%+v", msg)
		}
	}
}

// 空会话：只有一个 session-meta、没有说话，照样要有兜底标题（它要进游标）。
func TestParseEmptySession(t *testing.T) {
	path := writeSession(t,
		`{"type": "session-meta", "id": "x", "sessionId": "sess-demo", "timestamp": 1791968660003}`)

	session, err := Adapter{}.Parse(channel.SourceFile{Path: path})
	if err != nil {
		t.Fatalf("空会话不该报错: %v", err)
	}
	if session.MessageCount() != 0 {
		t.Errorf("空会话该是 0 条消息，实际 %d", session.MessageCount())
	}
	if session.Title == "" {
		t.Error("空会话也要有兜底标题")
	}
	if session.ID != "sess-demo" {
		t.Errorf("会话 ID = %q，想要 sess-demo", session.ID)
	}
}

// 坏行：整份跳过（返回错误），不归档半份对话。
func TestParseRejectsBrokenLine(t *testing.T) {
	path := writeSession(t,
		`{"type": "session-meta", "sessionId": "sess-demo", "timestamp": 1791968660003}`,
		`{"type": "message", "role": "user", 这一行不是合法 JSON`)

	if _, err := (Adapter{}).Parse(channel.SourceFile{Path: path}); err == nil {
		t.Fatal("坏行应当让整份会话跳过")
	}
}

// 回执找不到对应调用时：留成一条工具结果，别丢内容。
func TestParseOrphanToolResult(t *testing.T) {
	path := writeSession(t,
		`{"type": "message", "id": "m1", "role": "assistant", "timestamp": 1791968660003,`+
			` "content": [{"type": "output_text", "text": "开始"}]}`,
		`{"type": "function_call_result", "name": "Bash", "callId": "call_lost", "timestamp": 1791968661000,`+
			` "output": {"type": "text", "text": "找不到调用的输出"}}`)

	session, err := Adapter{}.Parse(channel.SourceFile{Path: path})
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if session.MessageCount() != 2 {
		t.Fatalf("消息 = %d 条，想要 2（助手 1 + 孤儿回执 1）", session.MessageCount())
	}
	last := session.Messages[1]
	if last.Role != channel.RoleTool || !strings.Contains(last.Text, "找不到调用的输出") {
		t.Errorf("孤儿回执没留成工具结果: %+v", last)
	}
}

// Probe 与 Parse 对「有没有消息」必须给出同一个答案（I-056）。
func TestProbeMatchesParse(t *testing.T) {
	path := writeFixture(t, "d-Coding-logseq_ai", "probe.jsonl")
	sf := channel.SourceFile{Path: path}
	session, err := Adapter{}.Parse(sf)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	has, err := Adapter{}.Probe(sf)
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}
	if has != (session.MessageCount() > 0) {
		t.Errorf("Probe=%v 与 Parse 的 %d 条消息不一致", has, session.MessageCount())
	}

	empty := writeSession(t, `{"type":"session-meta","sessionId":"sess-empty"}`)
	has, err = Adapter{}.Probe(channel.SourceFile{Path: empty})
	if err != nil || has {
		t.Errorf("空会话应当探测为没有消息，实际 has=%v err=%v", has, err)
	}
}
