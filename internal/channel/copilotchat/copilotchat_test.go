package copilotchat

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Always1023/AgentMemory/internal/channel"
)

func TestDetectNotInstalledWhenRootMissing(t *testing.T) {
	got := Adapter{Root: filepath.Join(t.TempDir(), "not-there")}.Detect()
	if got.Status != channel.StatusNotInstalled {
		t.Fatalf("状态 = %q，想要 %q", got.Status, channel.StatusNotInstalled)
	}
	if got.Err != nil {
		t.Errorf("未安装不是错误: %v", got.Err)
	}
}

func TestDetectFindsOnlyChatSessionFiles(t *testing.T) {
	root := t.TempDir()
	sessions := filepath.Join(root, "ws-a", sessionsDirName)
	if err := os.MkdirAll(sessions, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"a.json", "b.jsonl", "notes.txt"} {
		if err := os.WriteFile(filepath.Join(sessions, name), []byte("{}"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	// 没有 chatSessions 的工作区、根目录下的散文件，都不算。
	if err := os.MkdirAll(filepath.Join(root, "ws-b"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "workspace.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}

	got := Adapter{Root: root}.Detect()
	if got.Status != channel.StatusAvailable {
		t.Fatalf("状态 = %q，想要 %q（err=%v）", got.Status, channel.StatusAvailable, got.Err)
	}
	if len(got.Files) != 2 {
		names := []string{}
		for _, f := range got.Files {
			names = append(names, filepath.Base(f.Path))
		}
		t.Fatalf("发现 %v，想要只有 a.json 与 b.jsonl", names)
	}
	for _, f := range got.Files {
		if f.Size == 0 {
			t.Errorf("%s 的大小没填", f.Path)
		}
		if !strings.HasSuffix(f.Path, sessionsDirName+string(filepath.Separator)+"a.json") &&
			!strings.HasSuffix(f.Path, sessionsDirName+string(filepath.Separator)+"b.jsonl") {
			t.Errorf("多收了不该收的文件: %s", f.Path)
		}
	}
}

func parseFixture(t *testing.T, rel string) channel.Session {
	t.Helper()
	path := filepath.Join("testdata", filepath.FromSlash(rel))
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	session, err := Adapter{}.Parse(channel.SourceFile{
		Path: path, Size: info.Size(), ModTime: info.ModTime(),
	})
	if err != nil {
		t.Fatalf("解析 %s: %v", rel, err)
	}
	return session
}

// 整份 .json 的会话：夹具裁自本机真实会话。
func TestParseWholeJSONSession(t *testing.T) {
	session := parseFixture(t, "ws-json/chatSessions/session.json")

	if session.Channel != ChannelID {
		t.Errorf("渠道 = %q，想要 %q", session.Channel, ChannelID)
	}
	if session.ID != "0f5a8771-f5e5-4cee-a16f-98416ba46ef7" {
		t.Errorf("会话 ID = %q", session.ID)
	}
	if !strings.Contains(session.Title, "PyInstaller") {
		t.Errorf("标题 = %q，应当取 customTitle", session.Title)
	}
	if !strings.Contains(session.ProjectPath, "Desktop") || !strings.Contains(session.ProjectPath, "PDF") {
		t.Errorf("项目路径 = %q，应当从 ../workspace.json 还原出来", session.ProjectPath)
	}
	if want := time.UnixMilli(1768040554676); !session.StartedAt.Equal(want) {
		t.Errorf("开始时间 = %s，想要 %s", session.StartedAt, want)
	}
	if session.SourceFile == "" {
		t.Error("没有填源文件路径")
	}

	// 两条请求 = 提问 + 回答 各两条。思考过程（thinking）那些内部块不该变成消息，
	// 否则这里会多出来。
	if got := session.MessageCount(); got != 4 {
		t.Fatalf("消息 %d 条，想要 4 条（2 问 2 答）", got)
	}
	if session.Messages[0].Role != channel.RoleUser || !strings.Contains(session.Messages[0].Text, "pyinstaller") {
		t.Errorf("第一条 = %q / %q，想要用户的提问", session.Messages[0].Role, session.Messages[0].Text)
	}
	if session.Messages[1].Role != channel.RoleAssistant {
		t.Errorf("第二条角色 = %q，想要助手", session.Messages[1].Role)
	}
	if !strings.Contains(session.Messages[1].Text, "PyInstaller") {
		t.Errorf("助手正文 = %q，应当把正文块合起来", session.Messages[1].Text)
	}
	if len(session.Messages[1].Tools) == 0 {
		t.Fatal("助手那条没有工具调用，想要 manage_todo_list")
	}
	if name := session.Messages[1].Tools[0].Name; name != "manage_todo_list" {
		t.Errorf("工具名 = %q，想要 manage_todo_list", name)
	}
	if session.Messages[3].Role != channel.RoleAssistant {
		t.Errorf("第四条角色 = %q，想要助手", session.Messages[3].Role)
	}
}

// 编辑记录的会话：`kind:1` 设值、`kind:2` 追加两种都要能还原。
func TestParseEditLogSession(t *testing.T) {
	session := parseFixture(t, "ws-log/chatSessions/session.jsonl")

	if session.ID != "6069d0a0-e61e-462c-9b10-999f4f635f04" {
		t.Errorf("会话 ID = %q", session.ID)
	}
	// customTitle 是文件第 2 行 kind:1 写进去的——读不到就说明「设值」没生效。
	if !strings.Contains(session.Title, "code") {
		t.Errorf("标题 = %q，应当来自 kind:1 设的 customTitle", session.Title)
	}
	if !strings.Contains(session.ProjectPath, "zhihu_download_md") {
		t.Errorf("项目路径 = %q", session.ProjectPath)
	}
	if want := time.UnixMilli(1774354575786); !session.StartedAt.Equal(want) {
		t.Errorf("开始时间 = %s，想要 %s", session.StartedAt, want)
	}

	// 两条请求：第一条的回应只有内部块，没有正文也没有工具 → 不该产出消息。
	if got := session.MessageCount(); got != 3 {
		for i, m := range session.Messages {
			t.Logf("  消息 %d: %s / %q", i, m.Role, m.Text)
		}
		t.Fatalf("消息 %d 条，想要 3 条（2 问 + 1 答）", got)
	}
	if session.Messages[0].Role != channel.RoleUser || !strings.Contains(session.Messages[0].Text, "shell") {
		t.Errorf("第一条 = %q / %q", session.Messages[0].Role, session.Messages[0].Text)
	}
	last := session.Messages[2]
	if last.Role != channel.RoleAssistant || strings.TrimSpace(last.Text) == "" {
		t.Errorf("最后一条 = %q / %q，想要带正文的助手消息", last.Role, last.Text)
	}
}

func TestParseBrokenFile(t *testing.T) {
	path := filepath.Join("testdata", "broken.jsonl")
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := (Adapter{}).Parse(channel.SourceFile{Path: path, Size: info.Size(), ModTime: info.ModTime()}); err == nil {
		t.Fatal("坏文件应当报错")
	}
}

// 第一行不是底稿（kind:0）的编辑记录，要报错而不是当成底稿读。
func TestParseEditLogWithoutBase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bad.jsonl")
	if err := os.WriteFile(path, []byte("{\"kind\":1,\"k\":[\"customTitle\"],\"v\":\"x\"}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := (Adapter{}).Parse(channel.SourceFile{Path: path}); err == nil {
		t.Fatal("第一行不是底稿时应当报错")
	}
}

// 一条消息都没有的会话是「空会话」：不报错，交给编排层跳过（I-039）。
func TestParseSessionWithoutMessages(t *testing.T) {
	path := filepath.Join(t.TempDir(), "empty.json")
	if err := os.WriteFile(path, []byte(`{"sessionId":"x","requests":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	session, err := Adapter{}.Parse(channel.SourceFile{Path: path})
	if err != nil {
		t.Fatalf("空会话不该报错: %v", err)
	}
	if session.MessageCount() != 0 {
		t.Errorf("空会话该是 0 条消息，实际 %d 条", session.MessageCount())
	}
	if session.Title == "" {
		t.Error("空会话也要有兜底标题")
	}
}

func TestDecodeFileURI(t *testing.T) {
	cases := []struct{ in, want string }{
		{"file:///d%3A/Coding/x", filepath.FromSlash("d:/Coding/x")},
		{"file:///home/me/proj", filepath.FromSlash("/home/me/proj")},
		{"d:\\Coding\\x", "d:\\Coding\\x"},
		{"", ""},
	}
	for _, c := range cases {
		if got := decodeFileURI(c.in); got != c.want {
			t.Errorf("decodeFileURI(%q) = %q，想要 %q", c.in, got, c.want)
		}
	}
}
