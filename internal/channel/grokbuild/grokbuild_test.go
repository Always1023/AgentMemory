package grokbuild

import (
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

func TestDetectFindsSummary(t *testing.T) {
	got := Adapter{Root: "testdata"}.Detect()
	if got.Status != channel.StatusAvailable {
		t.Fatalf("状态 = %q，想要 %q（err=%v）", got.Status, channel.StatusAvailable, got.Err)
	}
	if len(got.Files) != 1 {
		t.Fatalf("发现 %d 个会话，想要 1", len(got.Files))
	}
	if filepath.Base(got.Files[0].Path) != "summary.json" {
		t.Fatalf("源文件 = %s", got.Files[0].Path)
	}
}

func TestParseMessages(t *testing.T) {
	path := filepath.Join("testdata", "sessions", "project", "session-1", "summary.json")
	session, err := (Adapter{}).Parse(channel.SourceFile{Path: path})
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if session.Channel != ChannelID || session.ID != "grok-session-1" {
		t.Fatalf("会话身份不对: %+v", session)
	}
	if session.Title != "Grok 会话" {
		t.Errorf("标题 = %q", session.Title)
	}
	if session.ProjectPath != "D:/work/demo" {
		t.Errorf("项目路径 = %q", session.ProjectPath)
	}
	if len(session.Messages) != 4 {
		t.Fatalf("消息数 = %d，想要 4", len(session.Messages))
	}
	if session.Messages[0].Role != channel.RoleUser || session.Messages[0].Text != "看看目录" {
		t.Errorf("第 1 条消息 = %+v", session.Messages[0])
	}
	if len(session.Messages[1].Tools) != 1 || session.Messages[1].Tools[0].Name != "run_terminal_cmd" {
		t.Errorf("工具调用 = %+v", session.Messages[1].Tools)
	}
}
