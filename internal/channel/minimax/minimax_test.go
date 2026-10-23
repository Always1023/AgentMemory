package minimax

import (
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Always1023/AgentMemory/internal/channel"
	_ "modernc.org/sqlite"
)

func newFixtureDB(t *testing.T, fixture string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", fixture))
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "runtime-state.sqlite")
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(path))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(string(raw)); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestDetectNotInstalledWhenDBMissing(t *testing.T) {
	got := Adapter{Root: filepath.Join(t.TempDir(), "missing.sqlite")}.Detect()
	if got.Status != channel.StatusNotInstalled || got.Err != nil {
		t.Fatalf("状态 = %q / err=%v，想要未安装", got.Status, got.Err)
	}
}

func TestDetectVisibleRootSessions(t *testing.T) {
	path := newFixtureDB(t, "minimax.sql")
	got := Adapter{Root: path}.Detect()
	if got.Status != channel.StatusAvailable {
		t.Fatalf("状态 = %q（err=%v）", got.Status, got.Err)
	}
	if len(got.Files) != 1 || !strings.Contains(got.Files[0].Path, "#mvs_public") {
		t.Fatalf("可见根会话筛选不对: %+v", got.Files)
	}
}

func TestParseMigratedSession(t *testing.T) {
	path := newFixtureDB(t, "minimax.sql")
	adapter := Adapter{Root: path}
	session, err := adapter.Parse(adapter.Detect().Files[0])
	if err != nil {
		t.Fatal(err)
	}
	if session.Channel != ChannelID || session.ID != "mvs_public" || session.Title != "项目检查" {
		t.Fatalf("身份不对: %+v", session)
	}
	if session.ProjectPath != "D:/work" || session.StartedAt.IsZero() {
		t.Fatalf("项目或时间不对: %+v", session)
	}
	if len(session.Messages) != 2 || session.Messages[0].Text != "修复这个项目" || session.Messages[1].Text != "测试通过" {
		t.Fatalf("消息解析不对: %+v", session.Messages)
	}
}

func TestParseLegacyDisplayMessages(t *testing.T) {
	path := newFixtureDB(t, "minimax_legacy.sql")
	adapter := Adapter{Root: path}
	files := adapter.Detect().Files
	if len(files) != 1 {
		t.Fatalf("发现 %d 条会话，想要 1 条", len(files))
	}
	session, err := adapter.Parse(files[0])
	if err != nil {
		t.Fatal(err)
	}
	if len(session.Messages) != 2 || session.Messages[0].Text != "旧问题" || session.Messages[1].Text != "旧答案" {
		t.Fatalf("旧库消息解析不对: %+v", session.Messages)
	}
}
