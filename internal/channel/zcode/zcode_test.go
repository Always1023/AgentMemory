package zcode

import (
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Always1023/AgentMemory/internal/channel"
	_ "modernc.org/sqlite"
)

// newFixtureDB 用 testdata/zcode.sql 造一个临时库（夹具是真实会话裁出来的，
// 仓库里不放二进制库文件）。
func newFixtureDB(t *testing.T) string {
	t.Helper()

	stmt, err := os.ReadFile(filepath.Join("testdata", "zcode.sql"))
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "db.sqlite")
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

func exec(t *testing.T, path, stmt string) {
	t.Helper()
	handle, err := sql.Open("sqlite", "file:"+filepath.ToSlash(path))
	if err != nil {
		t.Fatal(err)
	}
	defer handle.Close()
	if _, err := handle.Exec(stmt); err != nil {
		t.Fatalf("补数据: %v", err)
	}
}

func TestDetectFindsSessions(t *testing.T) {
	db := newFixtureDB(t)
	got := Adapter{Root: db}.Detect()

	if got.Status != channel.StatusAvailable {
		t.Fatalf("状态 = %q，想要 available（err=%v）", got.Status, got.Err)
	}
	if len(got.Files) != 1 {
		t.Fatalf("发现 %d 条会话，想要 1 条", len(got.Files))
	}
	f := got.Files[0]
	if !strings.Contains(f.Path, "#sess_") {
		t.Errorf("虚拟路径应当是「库#会话 ID」，实际 %s", f.Path)
	}
	if f.Size <= 0 {
		t.Errorf("大小（消息条数）应当大于 0，实际 %d", f.Size)
	}
	if f.ModTime.IsZero() {
		t.Error("更新时间没填——那样库一变所有会话都会重收")
	}
}

func TestParseSession(t *testing.T) {
	db := newFixtureDB(t)
	adapter := Adapter{Root: db}

	session, err := adapter.Parse(adapter.Detect().Files[0])
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if session.Channel != ChannelID || session.ID == "" || session.Title == "" {
		t.Errorf("身份信息不全：%q / %q / %q", session.Channel, session.ID, session.Title)
	}
	if session.ProjectPath == "" {
		t.Error("项目路径应当取会话的 directory")
	}
	if session.StartedAt.IsZero() {
		t.Error("开始时间应当取 time_created")
	}
	if session.MessageCount() == 0 {
		t.Fatal("一条消息都没拼出来（片段要按 message_id 捞回来）")
	}

	user, tools := false, 0
	for _, msg := range session.Messages {
		if msg.Role == channel.RoleUser && strings.TrimSpace(msg.Text) != "" {
			user = true
		}
		tools += len(msg.Tools)
	}
	if !user {
		t.Error("应当有一条带正文的用户消息")
	}
	if tools == 0 {
		t.Error("夹具里那条工具调用没被读出来")
	}
	for _, msg := range session.Messages {
		if strings.Contains(msg.Text, "The user seems to be") {
			t.Error("reasoning 那段混进正文了")
		}
	}
}

// 空会话：0 条消息、有兜底标题、不报错（交给 I-039 那套跳过）。
func TestParseEmptySession(t *testing.T) {
	db := newFixtureDB(t)
	exec(t, db, `insert into session (id, directory, title, time_created, time_updated)
		values ('sess_test_empty', 'D:/empty', null, 1700000000000, 1700000000000)`)

	adapter := Adapter{Root: db}
	var target channel.SourceFile
	for _, f := range adapter.Detect().Files {
		if strings.Contains(f.Path, "sess_test_empty") {
			target = f
		}
	}
	if target.Path == "" {
		t.Fatal("没找到刚造的空会话")
	}
	session, err := adapter.Parse(target)
	if err != nil {
		t.Fatalf("空会话不该报错: %v", err)
	}
	if session.MessageCount() != 0 {
		t.Errorf("空会话该是 0 条消息，实际 %d", session.MessageCount())
	}
	if session.Title == "" {
		t.Error("空会话也要有兜底标题（它照样要进游标）")
	}
}

func TestDetectNotInstalledWhenDBMissing(t *testing.T) {
	got := Adapter{Root: filepath.Join(t.TempDir(), "没有这个库")}.Detect()
	if got.Status != channel.StatusNotInstalled {
		t.Fatalf("状态 = %q，想要 not_installed", got.Status)
	}
	if got.Err != nil {
		t.Errorf("库不在不是错误: %v", got.Err)
	}
}

// 库在但没有认得的表：可用但没有会话，不是失败。
func TestDetectWithoutKnownTables(t *testing.T) {
	path := filepath.Join(t.TempDir(), "db.sqlite")
	handle, err := sql.Open("sqlite", "file:"+filepath.ToSlash(path))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := handle.Exec(`create table local_setting (key text primary key, value text)`); err != nil {
		t.Fatal(err)
	}
	handle.Close()

	got := Adapter{Root: path}.Detect()
	if got.Status != channel.StatusAvailable || len(got.Files) != 0 {
		t.Errorf("应当是「可用但没有会话」，实际 %q / %d 条（err=%v）", got.Status, len(got.Files), got.Err)
	}
}

func TestParseRejectsBadPaths(t *testing.T) {
	adapter := Adapter{}
	for _, virtual := range []string{"", "没有井号", "前缀#", "#没有前缀"} {
		if _, err := adapter.Parse(channel.SourceFile{Path: virtual}); err == nil {
			t.Errorf("%q 应当被拒", virtual)
		}
	}
}

// Probe 与 Parse 对「有没有消息」必须给出同一个答案（I-056）。
func TestProbeMatchesParse(t *testing.T) {
	db := newFixtureDB(t)
	exec(t, db, `insert into session (id, directory, title, time_created, time_updated)
		values ('sess_probe_empty', 'D:/empty', null, 1700000000000, 1700000000000)`)
	adapter := Adapter{Root: db}

	for _, f := range adapter.Detect().Files {
		session, err := adapter.Parse(f)
		if err != nil {
			t.Fatalf("Parse(%s): %v", f.Path, err)
		}
		has, err := adapter.Probe(f)
		if err != nil {
			t.Fatalf("Probe(%s): %v", f.Path, err)
		}
		if has != (session.MessageCount() > 0) {
			t.Errorf("%s Probe=%v 与 Parse 的 %d 条消息不一致", f.Path, has, session.MessageCount())
		}
	}
}
