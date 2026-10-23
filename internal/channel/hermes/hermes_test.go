package hermes

import (
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Always1023/AgentMemory/internal/channel"
	_ "modernc.org/sqlite"
)

func newFixtureDB(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", "hermes.sql"))
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "state.db")
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

func TestDetectNotInstalledWhenRootMissing(t *testing.T) {
	got := Adapter{Root: filepath.Join(t.TempDir(), "missing")}.Detect()
	if got.Status != channel.StatusNotInstalled || got.Err != nil {
		t.Fatalf("状态 = %q / err=%v，想要未安装", got.Status, got.Err)
	}
}

func TestDetectMergesSQLiteAndJSONLBySessionID(t *testing.T) {
	db := newFixtureDB(t)
	root := filepath.Dir(db)
	sessions := filepath.Join(root, "sessions")
	if err := os.MkdirAll(sessions, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sessions, "same.jsonl"), []byte(`{"type":"session","id":"s1"}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sessions, "file-only.jsonl"), []byte(`{"type":"session","id":"file-only"}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	got := Adapter{Root: root}.Detect()
	if got.Status != channel.StatusAvailable {
		t.Fatalf("状态 = %q（err=%v）", got.Status, got.Err)
	}
	if len(got.Files) != 2 {
		t.Fatalf("发现 %d 条会话，想要 2 条（SQLite 优先去重）: %+v", len(got.Files), got.Files)
	}
	var hasSQLite, hasFile bool
	for _, file := range got.Files {
		if strings.Contains(file.Path, "#s1") {
			hasSQLite = true
		}
		if strings.HasSuffix(file.Path, "file-only.jsonl") {
			hasFile = true
		}
	}
	if !hasSQLite || !hasFile {
		t.Fatalf("合并结果不对: %+v", got.Files)
	}
}

func TestParseSQLiteSession(t *testing.T) {
	db := newFixtureDB(t)
	adapter := Adapter{Root: filepath.Dir(db)}
	var target channel.SourceFile
	for _, file := range adapter.Detect().Files {
		if strings.Contains(file.Path, "#s1") {
			target = file
		}
	}
	if target.Path == "" {
		t.Fatal("没有发现 SQLite 会话")
	}
	session, err := adapter.Parse(target)
	if err != nil {
		t.Fatal(err)
	}
	if session.Channel != ChannelID || session.ID != "s1" || session.Title != "Hermes 会话" {
		t.Fatalf("身份不对: %+v", session)
	}
	if session.ProjectPath != "D:/work" || session.StartedAt.IsZero() {
		t.Fatalf("项目或时间不对: %+v", session)
	}
	if len(session.Messages) != 3 {
		t.Fatalf("消息数 = %d，想要 3", len(session.Messages))
	}
	if session.Messages[0].Text != "看一下磁盘" {
		t.Errorf("用户消息 = %q", session.Messages[0].Text)
	}
	if len(session.Messages[1].Tools) != 1 || session.Messages[1].Tools[0].Name != "terminal" {
		t.Errorf("工具调用 = %+v", session.Messages[1].Tools)
	}
}

func TestParseNestedJSONLAndToolCall(t *testing.T) {
	root := t.TempDir()
	sessions := filepath.Join(root, "sessions")
	if err := os.MkdirAll(sessions, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(sessions, "nested.jsonl")
	body := strings.Join([]string{
		`{"type":"session","id":"file-1","title":"文件会话","cwd":"D:/file"}`,
		`{"type":"message","timestamp":"2026-10-17T01:00:00Z","message":{"role":"user","content":"列一下文件"}}`,
		`{"type":"message","timestamp":"2026-10-17T01:01:00Z","message":{"role":"assistant","content":"","tool_calls":[{"id":"c1","function":{"name":"terminal","arguments":"{\"command\":\"ls\"}"}}]}}`,
	}, "\n") + "\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	session, err := (Adapter{}).Parse(channel.SourceFile{Path: path})
	if err != nil {
		t.Fatal(err)
	}
	if session.ID != "file-1" || session.Title != "文件会话" || session.ProjectPath != "D:/file" {
		t.Fatalf("元数据不对: %+v", session)
	}
	if len(session.Messages) != 2 || len(session.Messages[1].Tools) != 1 {
		t.Fatalf("消息或工具调用不对: %+v", session.Messages)
	}
}

func TestParseFlatJSONL(t *testing.T) {
	path := filepath.Join(t.TempDir(), "flat.jsonl")
	body := `{"role":"user","content":"问题","ts":1700000000}` + "\n" +
		`{"role":"assistant","content":"答案","ts":1700000001}` + "\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	session, err := (Adapter{}).Parse(channel.SourceFile{Path: path})
	if err != nil {
		t.Fatal(err)
	}
	if len(session.Messages) != 2 || session.Messages[0].Text != "问题" || session.Title != "问题" {
		t.Fatalf("扁平格式解析不对: %+v", session)
	}
}
