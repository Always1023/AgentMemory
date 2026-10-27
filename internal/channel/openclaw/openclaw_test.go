package openclaw

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

func TestDetectFindsSession(t *testing.T) {
	got := Adapter{Root: "testdata/agents"}.Detect()
	if got.Status != channel.StatusAvailable {
		t.Fatalf("状态 = %q，想要 %q（err=%v）", got.Status, channel.StatusAvailable, got.Err)
	}
	if len(got.Files) != 1 {
		t.Fatalf("发现 %d 个会话，想要 1", len(got.Files))
	}
}

func TestParseUsesDisplayNameAndMessages(t *testing.T) {
	path := filepath.Join("testdata", "agents", "main", "sessions", "session-1.jsonl")
	session, err := (Adapter{}).Parse(channel.SourceFile{Path: path})
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if session.Channel != ChannelID || session.ID != "session-1" {
		t.Fatalf("会话身份不对: %+v", session)
	}
	if session.Title != "部署检查" {
		t.Errorf("标题 = %q", session.Title)
	}
	if session.ProjectPath != "D:/work/demo" {
		t.Errorf("项目路径 = %q", session.ProjectPath)
	}
	if len(session.Messages) != 2 {
		t.Fatalf("消息数 = %d，想要 2", len(session.Messages))
	}
	if session.Messages[0].Text != "怎么部署？" {
		t.Errorf("用户消息 = %q", session.Messages[0].Text)
	}
	if len(session.Messages[1].Tools) != 1 || session.Messages[1].Tools[0].Name != "terminal" {
		t.Errorf("工具调用 = %+v", session.Messages[1].Tools)
	}
}
