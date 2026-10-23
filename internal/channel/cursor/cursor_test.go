package cursor

import (
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Always1023/AgentMemory/internal/channel"
	_ "modernc.org/sqlite"
)

// newFixtureDB 用 testdata/cursor.sql 造一个临时库：夹具是真实会话裁出来的 SQL 文本
// （仓库里不放二进制库文件）。
func newFixtureDB(t *testing.T) string {
	t.Helper()

	stmt, err := os.ReadFile(filepath.Join("testdata", "cursor.sql"))
	if err != nil {
		t.Fatal(err)
	}

	path := filepath.Join(t.TempDir(), "state.vscdb")
	handle, err := sql.Open("sqlite", "file:"+filepath.ToSlash(path))
	if err != nil {
		t.Fatal(err)
	}
	defer handle.Close()
	if _, err := handle.Exec(string(stmt)); err != nil {
		t.Fatalf("建夹具库: %v", err)
	}
	return path
}

// findFile 在检测结果里按会话键找一个源文件。
func findFile(t *testing.T, files []channel.SourceFile, keyPart string) channel.SourceFile {
	t.Helper()
	for _, f := range files {
		if strings.Contains(f.Path, keyPart) {
			return f
		}
	}
	t.Fatalf("检测结果里没有 %q：%+v", keyPart, files)
	return channel.SourceFile{}
}

func TestDetectFindsOnlyComposerRows(t *testing.T) {
	db := newFixtureDB(t)
	got := Adapter{Root: db}.Detect()

	if got.Status != channel.StatusAvailable {
		t.Fatalf("状态 = %q，想要 %q（err=%v）", got.Status, channel.StatusAvailable, got.Err)
	}
	if len(got.Files) != 2 {
		t.Fatalf("发现 %d 条会话，想要 2 条（inlineDiffsData 那条不是会话）", len(got.Files))
	}
	for _, f := range got.Files {
		if !strings.Contains(f.Path, "#"+composerPrefix) {
			t.Errorf("源文件路径应当是「库#composerData:…」，实际 %s", f.Path)
		}
		if f.Size <= 0 {
			t.Errorf("%s 的大小没填", f.Path)
		}
		if f.ModTime.IsZero() {
			t.Errorf("%s 的更新时间没填（要按会话自己算，不然整库一变全会话都要重收）", f.Path)
		}
	}
}

func TestParseSessionKeepsUserAndAssistant(t *testing.T) {
	db := newFixtureDB(t)
	adapter := Adapter{Root: db}

	// 夹具里那份有对话的会话：两条消息（一问一答）。
	var target channel.SourceFile
	for _, f := range adapter.Detect().Files {
		session, err := adapter.Parse(f)
		if err != nil {
			t.Fatalf("Parse %s: %v", f.Path, err)
		}
		if session.MessageCount() > 0 {
			target = f
			if session.Channel != ChannelID {
				t.Errorf("渠道 = %q，想要 %q", session.Channel, ChannelID)
			}
			if session.ID == "" || session.Title == "" {
				t.Errorf("会话 ID / 标题不该为空：%q / %q", session.ID, session.Title)
			}
			if session.StartedAt.IsZero() {
				t.Error("开始时间应当取 createdAt")
			}
			if session.Messages[0].Role != channel.RoleUser {
				t.Errorf("第一条角色 = %q，想要用户（type 1）", session.Messages[0].Role)
			}
			if session.Messages[1].Role != channel.RoleAssistant {
				t.Errorf("第二条角色 = %q，想要助手（type 2）", session.Messages[1].Role)
			}
			if strings.TrimSpace(session.Messages[0].Text) == "" {
				t.Error("第一条没有正文")
			}
			break
		}
	}
	if target.Path == "" {
		t.Fatal("夹具里应当有一份带对话的会话")
	}
}

// 空会话（点开过没说话）：不报错、0 条消息、有兜底标题——由编排层跳过（I-039）。
func TestParseEmptySession(t *testing.T) {
	db := newFixtureDB(t)
	adapter := Adapter{Root: db}

	found := false
	for _, f := range adapter.Detect().Files {
		session, err := adapter.Parse(f)
		if err != nil {
			t.Fatalf("Parse %s: %v", f.Path, err)
		}
		if session.MessageCount() == 0 {
			found = true
			if session.Title == "" {
				t.Error("空会话也要有兜底标题（它照样要进游标）")
			}
		}
	}
	if !found {
		t.Fatal("夹具里应当留了一份空壳会话")
	}
}

func TestDetectNotInstalledWhenDBMissing(t *testing.T) {
	got := Adapter{Root: filepath.Join(t.TempDir(), "没有这个库")}.Detect()
	if got.Status != channel.StatusNotInstalled {
		t.Fatalf("状态 = %q，想要 %q", got.Status, channel.StatusNotInstalled)
	}
	if got.Err != nil {
		t.Errorf("库不在不是错误: %v", got.Err)
	}
}

func TestDetectWithoutCursorTableIsAvailable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.vscdb")
	handle, err := sql.Open("sqlite", "file:"+filepath.ToSlash(path))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := handle.Exec(`create table ItemTable (key text primary key, value blob)`); err != nil {
		t.Fatal(err)
	}
	handle.Close()

	got := Adapter{Root: path}.Detect()
	if got.Status != channel.StatusAvailable || len(got.Files) != 0 {
		t.Errorf("没有 cursorDiskKV 表时应当是「可用但没有会话」，实际 %q / %d 条（err=%v）",
			got.Status, len(got.Files), got.Err)
	}
}

func TestParseRejectsPathsWithoutAKey(t *testing.T) {
	adapter := Adapter{}
	for _, virtual := range []string{
		"",
		"没有井号",
		"前缀#",
		"#没有前缀",
		"库#inlineDiffsData:xxx", // 不是会话
	} {
		if _, err := adapter.Parse(channel.SourceFile{Path: virtual}); err == nil {
			t.Errorf("%q 应当被拒", virtual)
		}
	}
}

func TestSplitVirtualPath(t *testing.T) {
	db, key, ok := splitVirtualPath("/库/state.vscdb#composerData:abc")
	if !ok || db != "/库/state.vscdb" || key != "composerData:abc" {
		t.Errorf("拆出来 = %q / %q / %v", db, key, ok)
	}
	if _, _, ok := splitVirtualPath("没有井号"); ok {
		t.Error("没有井号的路径不该被当成会话")
	}
}

// 库文件在、但根本不是库：要么报错（带原因）、要么当空库（没有会话），都不许崩。
func TestDetectGarbageDB(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.vscdb")
	if err := os.WriteFile(path, []byte("这不是一个库"), 0o600); err != nil {
		t.Fatal(err)
	}
	got := Adapter{Root: path}.Detect()
	switch got.Status {
	case channel.StatusFailed:
		if got.Err == nil {
			t.Error("报失败时要给原因")
		}
	case channel.StatusAvailable:
		if len(got.Files) != 0 {
			t.Errorf("当空库打开时不该有会话，实际 %d 条", len(got.Files))
		}
	default:
		t.Errorf("状态 = %q，想要 failed 或 available（err=%v）", got.Status, got.Err)
	}
}
