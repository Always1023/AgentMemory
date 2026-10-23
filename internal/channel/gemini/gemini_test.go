package gemini

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Always1023/AgentMemory/internal/channel"
)

func TestDetectNotInstalledWhenRootMissing(t *testing.T) {
	got := Adapter{Root: filepath.Join(t.TempDir(), "missing")}.Detect()
	if got.Status != channel.StatusNotInstalled {
		t.Fatalf("状态 = %q，想要 %q", got.Status, channel.StatusNotInstalled)
	}
	if got.Err != nil {
		t.Errorf("未安装不是错误: %v", got.Err)
	}
}

func TestDetectPrefersJSONLOverLegacyJSON(t *testing.T) {
	root := t.TempDir()
	chats := filepath.Join(root, "project", "chats")
	if err := os.MkdirAll(chats, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"session-a.json", "session-a.jsonl", "session-b.json"} {
		if err := os.WriteFile(filepath.Join(chats, name), []byte("{}\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	got := Adapter{Root: root}.Detect()
	if got.Status != channel.StatusAvailable {
		t.Fatalf("状态 = %q，想要 %q（err=%v）", got.Status, channel.StatusAvailable, got.Err)
	}
	if len(got.Files) != 2 {
		t.Fatalf("发现 %d 个文件，想要 2 个", len(got.Files))
	}
	for _, f := range got.Files {
		if strings.HasSuffix(f.Path, "session-a.json") {
			t.Errorf("有同名 .jsonl 时不该再认旧版 .json: %s", f.Path)
		}
	}
}

func TestParseJSONLReplaysMessages(t *testing.T) {
	session, err := (Adapter{}).Parse(channel.SourceFile{Path: filepath.Join("testdata", "session.jsonl")})
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if session.Channel != ChannelID || session.ID != "gemini-session-1" {
		t.Fatalf("会话身份不对: %+v", session)
	}
	if session.Title != "解释项目结构" {
		t.Errorf("标题 = %q", session.Title)
	}
	if len(session.Messages) != 3 {
		t.Fatalf("消息数 = %d，想要 3", len(session.Messages))
	}
	if session.Messages[0].Role != channel.RoleUser || session.Messages[0].Text != "解释项目结构" {
		t.Errorf("第 1 条消息 = %+v", session.Messages[0])
	}
	if session.Messages[1].Role != channel.RoleAssistant || !strings.Contains(session.Messages[1].Text, "先看目录") {
		t.Errorf("第 2 条消息 = %+v", session.Messages[1])
	}
	if len(session.Messages[1].Tools) != 1 || session.Messages[1].Tools[0].Name != "run_shell_command" {
		t.Errorf("工具调用没解析出来: %+v", session.Messages[1].Tools)
	}
}

func TestParseEmptyJSONLIsNotError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session-empty.jsonl")
	if err := os.WriteFile(path, []byte(`{"sessionId":"empty","messages":[]}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	session, err := (Adapter{}).Parse(channel.SourceFile{Path: path})
	if err != nil {
		t.Fatalf("空会话不该报错: %v", err)
	}
	if session.MessageCount() != 0 {
		t.Fatalf("消息数 = %d，想要 0", session.MessageCount())
	}
}
