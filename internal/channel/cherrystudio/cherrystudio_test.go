package cherrystudio

import (
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Always1023/AgentMemory/internal/channel"
	_ "modernc.org/sqlite"
)

// newFixtureDB 用 testdata/cherrystudio.sql 造一个临时库（不往仓库里放真实库）。
func newFixtureDB(t *testing.T) string {
	t.Helper()

	stmt, err := os.ReadFile(filepath.Join("testdata", "cherrystudio.sql"))
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "cherrystudio.sqlite")
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

func findFile(t *testing.T, files []channel.SourceFile, topicID string) channel.SourceFile {
	t.Helper()
	for _, f := range files {
		if strings.Contains(f.Path, "#"+topicID) {
			return f
		}
	}
	t.Fatalf("没找到 topic %s 的源文件", topicID)
	return channel.SourceFile{}
}

func TestDetectFindsTopics(t *testing.T) {
	db := newFixtureDB(t)
	got := Adapter{Root: db}.Detect()

	if got.Status != channel.StatusAvailable {
		t.Fatalf("状态 = %q，想要 available（err=%v）", got.Status, got.Err)
	}
	if len(got.Files) != 2 {
		t.Fatalf("发现 %d 个话题，想要 2 个（删掉的不算）", len(got.Files))
	}
	f := findFile(t, got.Files, "topic-1")
	if f.Size != 6 {
		t.Errorf("话题大小（消息条数）= %d，想要 6", f.Size)
	}
	want := time.UnixMilli(1760000006000)
	if !f.ModTime.Equal(want) {
		t.Errorf("更新时间 = %v，想要 %v", f.ModTime, want)
	}
}

func TestParseTopic(t *testing.T) {
	db := newFixtureDB(t)
	adapter := Adapter{Root: db}
	f := findFile(t, adapter.Detect().Files, "topic-1")

	session, err := adapter.Parse(f)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if session.Channel != ChannelID || session.ID != "topic-1" {
		t.Errorf("身份 = %q / %q", session.Channel, session.ID)
	}
	if session.Title != "示例会话" {
		t.Errorf("标题 = %q，想要话题名", session.Title)
	}
	if !session.StartedAt.Equal(time.UnixMilli(1760000000000)) {
		t.Errorf("开始时间 = %v", session.StartedAt)
	}
	if len(session.Messages) != 5 {
		for i, m := range session.Messages {
			t.Logf("  消息 %d: role=%s text=%q tools=%d", i, m.Role, m.Text, len(m.Tools))
		}
		t.Fatalf("消息数 = %d，想要 5（root 跳过、兄弟回复不收、工具结果单列）", len(session.Messages))
	}

	if m := session.Messages[0]; m.Role != channel.RoleUser || m.Text != "你好" {
		t.Errorf("第 1 条 = %+v", m)
	}
	if m := session.Messages[1]; m.Role != channel.RoleAssistant || m.Text != "你好，有什么可以帮你？" {
		t.Errorf("第 2 条 = %+v", m)
	}
	if m := session.Messages[2]; m.Role != channel.RoleUser || !strings.Contains(m.Text, "看下文件") || !strings.Contains(m.Text, "report.txt") {
		t.Errorf("第 3 条 = %+v", m)
	}
	m := session.Messages[3]
	if m.Role != channel.RoleAssistant || m.Text != "好的" {
		t.Errorf("第 4 条 = %+v", m)
	}
	if len(m.Tools) != 1 || m.Tools[0].Name != "read" || !strings.Contains(m.Tools[0].Input, `"path":"a.txt"`) {
		t.Errorf("工具调用 = %+v", m.Tools)
	}
	if m := session.Messages[4]; m.Role != channel.RoleTool || m.Text != "文件内容" {
		t.Errorf("第 5 条应当是工具结果，实际 %+v", m)
	}

	for _, m := range session.Messages {
		if strings.Contains(m.Text, "思考") || strings.Contains(m.Text, "另一家的回答") || strings.Contains(m.Text, "pause_placeholder") {
			t.Errorf("不该进正文的内容混进来了: %q", m.Text)
		}
	}
}

// 空话题：只有 root，返回 0 条消息、有兜底标题、不报错（I-039）。
func TestParseEmptyTopic(t *testing.T) {
	db := newFixtureDB(t)
	adapter := Adapter{Root: db}
	f := findFile(t, adapter.Detect().Files, "topic-2")

	session, err := adapter.Parse(f)
	if err != nil {
		t.Fatalf("空话题不该报错: %v", err)
	}
	if session.MessageCount() != 0 {
		t.Errorf("空话题该是 0 条消息，实际 %d", session.MessageCount())
	}
	if session.Title == "" {
		t.Error("空话题也要有兜底标题")
	}
}

func TestParseBrokenDataIsAnError(t *testing.T) {
	db := newFixtureDB(t)
	exec(t, db, `insert into topic (id, name, active_node_id, created_at, updated_at, deleted_at)
		values ('topic-bad', '坏数据', 'bad1', 1760000000000, 1760000001000, null)`)
	exec(t, db, `insert into message (id, parent_id, topic_id, role, data, searchable_text, status, siblings_group_id, created_at, updated_at, deleted_at)
		values ('bad1', null, 'topic-bad', 'assistant', '{not json', '', 'success', 0, 1760000001000, 1760000001000, null)`)

	adapter := Adapter{Root: db}
	f := findFile(t, adapter.Detect().Files, "topic-bad")
	_, err := adapter.Parse(f)
	if err == nil {
		t.Fatal("坏 data 应当报错，交给上层按数据错跳过")
	}
	if !strings.Contains(err.Error(), "data 解析失败") {
		t.Errorf("错误要指出坏在哪: %v", err)
	}
}

func TestParseDeletedTopicIsMissing(t *testing.T) {
	db := newFixtureDB(t)
	if _, err := (Adapter{Root: db}).Parse(channel.SourceFile{
		Path: filepath.ToSlash(db) + "#topic-3",
	}); err == nil {
		t.Error("已删除的话题应当报找不到")
	}
}

func TestProbe(t *testing.T) {
	db := newFixtureDB(t)
	adapter := Adapter{Root: db}
	files := adapter.Detect().Files

	if ok, err := adapter.Probe(findFile(t, files, "topic-1")); err != nil || !ok {
		t.Fatalf("有内容的话题 Probe = %v, %v，想要 true", ok, err)
	}
	if ok, err := adapter.Probe(findFile(t, files, "topic-2")); err != nil || ok {
		t.Fatalf("空话题 Probe = %v, %v，想要 false", ok, err)
	}
}

func TestDetectNotInstalledWhenDBMissing(t *testing.T) {
	got := Adapter{Root: filepath.Join(t.TempDir(), "没有这个库")}.Detect()
	if got.Status != channel.StatusNotInstalled {
		t.Errorf("状态 = %q，想要 %q", got.Status, channel.StatusNotInstalled)
	}
	if got.Err != nil {
		t.Errorf("未安装不是错误: %v", got.Err)
	}
}

// 库在、但不是认得的表：算「可用但没有会话」，不是失败（版本差异）。
func TestDetectUnknownSchemaIsAvailableWithoutSessions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "other.sqlite")
	exec(t, path, `create table other (id text primary key)`)

	got := Adapter{Root: path}.Detect()
	if got.Status != channel.StatusAvailable {
		t.Errorf("状态 = %q，想要 %q（err=%v）", got.Status, channel.StatusAvailable, got.Err)
	}
	if len(got.Files) != 0 {
		t.Errorf("不该发现会话: %+v", got.Files)
	}
}
