package pi

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/Always1023/AgentMemory/internal/channel"
)

func TestDetectNotInstalledWhenRootMissing(t *testing.T) {
	got := Adapter{Root: filepath.Join(t.TempDir(), "missing")}.Detect()
	if got.Status != channel.StatusNotInstalled {
		t.Fatalf("状态 = %q，想要 %q", got.Status, channel.StatusNotInstalled)
	}
}

func TestDetectFindsJSONL(t *testing.T) {
	got := Adapter{Root: "testdata/sessions"}.Detect()
	if got.Status != channel.StatusAvailable {
		t.Fatalf("状态 = %q，想要 %q（err=%v）", got.Status, channel.StatusAvailable, got.Err)
	}
	if len(got.Files) != 1 {
		t.Fatalf("发现 %d 个会话，想要 1", len(got.Files))
	}
}

func TestParseActiveBranch(t *testing.T) {
	path := filepath.Join("testdata", "sessions", "project", "session-1.jsonl")
	session, err := (Adapter{}).Parse(channel.SourceFile{Path: path})
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if session.Channel != ChannelID || session.ID != "pi-session-1" {
		t.Fatalf("会话身份不对: %+v", session)
	}
	if session.Title != "项目检查" {
		t.Errorf("标题 = %q", session.Title)
	}
	if session.ProjectPath != "D:/work/AgentMemory" {
		t.Errorf("项目路径 = %q", session.ProjectPath)
	}
	if len(session.Messages) != 3 {
		t.Fatalf("消息数 = %d，想要 3", len(session.Messages))
	}
	if session.Messages[0].Text != "检查项目" {
		t.Errorf("第 1 条消息 = %+v", session.Messages[0])
	}
	if len(session.Messages[1].Tools) != 1 || session.Messages[1].Tools[0].Name != "bash" {
		t.Errorf("工具调用 = %+v", session.Messages[1].Tools)
	}
}

func TestRootUsesEnvironmentOverride(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("PI_CODING_AGENT_SESSION_DIR", dir)
	got, err := (Adapter{}).root()
	if err != nil {
		t.Fatal(err)
	}
	if got != dir {
		t.Fatalf("root = %q，想要 %q", got, dir)
	}
}

func TestParseBrokenHeader(t *testing.T) {
	path := filepath.Join(t.TempDir(), "broken.jsonl")
	if err := os.WriteFile(path, []byte(`{"type":"message"}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := (Adapter{}).Parse(channel.SourceFile{Path: path}); err == nil {
		t.Fatal("没有 session header 应当报错")
	}
}
