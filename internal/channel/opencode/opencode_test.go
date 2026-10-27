package opencode

import (
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Always1023/AgentMemory/internal/channel"
	_ "modernc.org/sqlite"
)

// newFixtureDB 用 testdata 里的 SQL 文本造一个临时库
// （夹具是真实会话裁出来的，仓库里不放二进制库）。
func newFixtureDB(t *testing.T, name string) string {
	t.Helper()

	stmt, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "opencode.db")
	handle, err := sql.Open("sqlite", "file:"+filepath.ToSlash(path))
	if err != nil {
		t.Fatal(err)
	}
	defer handle.Close()
	if _, err := handle.Exec(string(stmt)); err != nil {
		t.Fatalf("建夹具库 %s: %v", name, err)
	}
	return path
}

// exec 往夹具库里补几行（给用例造特殊场景用）。
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

// 新格式（session_v2 + session_message）：认得出、解析得动。
func TestDetectAndParseNewFormat(t *testing.T) {
	db := newFixtureDB(t, "opencode_v2.sql")
	adapter := Adapter{Root: db}

	detected := adapter.Detect()
	if detected.Status != channel.StatusAvailable {
		t.Fatalf("状态 = %q，想要 available（err=%v）", detected.Status, detected.Err)
	}
	if len(detected.Files) != 1 {
		t.Fatalf("发现 %d 条会话，想要 1 条", len(detected.Files))
	}
	f := detected.Files[0]
	if !strings.Contains(f.Path, "#session_v2:") {
		t.Errorf("源文件路径里要有表名（Parse 靠它认路），实际 %s", f.Path)
	}
	if f.Size <= 0 {
		t.Errorf("大小（消息条数）应当大于 0，实际 %d", f.Size)
	}
	if f.ModTime.IsZero() {
		t.Error("更新时间没填——那样库一变所有会话都会重收")
	}

	session, err := adapter.Parse(f)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if session.Channel != ChannelID || session.ID == "" || session.Title == "" {
		t.Errorf("身份信息不全：%q / %q / %q", session.Channel, session.ID, session.Title)
	}
	if !strings.Contains(session.ProjectPath, "Coding") && session.ProjectPath == "" {
		t.Errorf("项目路径应当取会话的 directory，实际 %q", session.ProjectPath)
	}
	if session.StartedAt.IsZero() {
		t.Error("开始时间应当取 time_created")
	}
	if session.MessageCount() < 2 {
		t.Fatalf("消息 %d 条，想要至少 2 条", session.MessageCount())
	}
	if session.Messages[0].Role != channel.RoleUser {
		t.Errorf("第一条角色 = %q，想要用户", session.Messages[0].Role)
	}
	if strings.TrimSpace(session.Messages[0].Text) == "" {
		t.Error("用户那条没有正文（正文在 data.text 里）")
	}
	assistantWithContent, tools := false, 0
	for _, msg := range session.Messages[1:] {
		if strings.TrimSpace(msg.Text) != "" || len(msg.Tools) > 0 {
			assistantWithContent = true
		}
		tools += len(msg.Tools)
	}
	if !assistantWithContent {
		t.Error("助手那边一条有内容（正文或工具调用）的都没有")
	}
	if tools == 0 {
		t.Error("夹具里那条工具调用没被读出来（content 里的 type=tool 要变成工具调用）")
	}
}

// 旧格式（session + message + part）：也要认得出，碎片要拼回来。
func TestDetectAndParseLegacyFormat(t *testing.T) {
	db := newFixtureDB(t, "opencode_legacy.sql")
	adapter := Adapter{Root: db}

	detected := adapter.Detect()
	if detected.Status != channel.StatusAvailable || len(detected.Files) != 1 {
		t.Fatalf("状态=%q，发现 %d 条会话（err=%v）", detected.Status, len(detected.Files), detected.Err)
	}
	if !strings.Contains(detected.Files[0].Path, "#session:") {
		t.Errorf("旧格式的键里该带表名 session，实际 %s", detected.Files[0].Path)
	}

	session, err := adapter.Parse(detected.Files[0])
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if session.MessageCount() == 0 {
		t.Fatal("旧格式应当也能拼出消息")
	}
	if strings.TrimSpace(session.Messages[0].Text) == "" && len(session.Messages[0].Tools) == 0 {
		t.Error("第一条消息既没正文也没工具调用——说明片段没捞回来")
	}
}

// 两套表同时在（本机就是这样）：两边都要收，不能只认新的。
func TestDetectSeesBothFormatsAtOnce(t *testing.T) {
	db := newFixtureDB(t, "opencode_v2.sql")
	legacy, err := os.ReadFile(filepath.Join("testdata", "opencode_legacy.sql"))
	if err != nil {
		t.Fatal(err)
	}
	exec(t, db, string(legacy))

	detected := Adapter{Root: db}.Detect()
	if detected.Status != channel.StatusAvailable {
		t.Fatalf("状态 = %q（err=%v）", detected.Status, detected.Err)
	}
	if len(detected.Files) != 2 {
		t.Fatalf("发现 %d 条会话，想要 2 条（新旧各一条）", len(detected.Files))
	}
	var seenV2, seenLegacy bool
	for _, f := range detected.Files {
		seenV2 = seenV2 || strings.Contains(f.Path, "#session_v2:")
		seenLegacy = seenLegacy || strings.Contains(f.Path, "#session:")
	}
	if !seenV2 || !seenLegacy {
		t.Errorf("两套表都要扫到，实际 v2=%v legacy=%v", seenV2, seenLegacy)
	}
}

// 内部块（reasoning 之类）不算对话内容：不能混进正文，也不能多出消息。
func TestParseSkipsInternalParts(t *testing.T) {
	db := newFixtureDB(t, "opencode_v2.sql")
	exec(t, db, `insert into session_v2 (id, title, directory, time_created, time_updated)
		values ('ses_test_internal', '内部块用例', 'D:/x', 1700000000000, 1700000000001)`)
	exec(t, db, `insert into session_message (id, session_id, type, seq, time_created, time_updated, data)
		values ('msg_test_1', 'ses_test_internal', 'user', 0, 1700000000000, 1700000000000,
			'{"text":"问题","time":{"created":1700000000000}}')`)
	exec(t, db, `insert into session_message (id, session_id, type, seq, time_created, time_updated, data)
		values ('msg_test_2', 'ses_test_internal', 'assistant', 1, 1700000000001, 1700000000001,
			'{"content":[{"type":"reasoning","text":"这段内心戏不该出现"},{"type":"text","text":"这是回答"},{"type":"step-start"}]}')`)

	adapter := Adapter{Root: db}
	var target channel.SourceFile
	for _, f := range adapter.Detect().Files {
		if strings.Contains(f.Path, "ses_test_internal") {
			target = f
		}
	}
	if target.Path == "" {
		t.Fatal("没找到刚造的那条会话")
	}

	session, err := adapter.Parse(target)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if session.MessageCount() != 2 {
		t.Fatalf("消息 %d 条，想要 2 条（内部块不该变成消息）", session.MessageCount())
	}
	if got := session.Messages[1].Text; got != "这是回答" {
		t.Errorf("助手正文 = %q，想要只要 text 那段", got)
	}
	if strings.Contains(session.Messages[1].Text, "内心戏") {
		t.Error("reasoning 那段混进正文了")
	}
}

// 空会话：0 条消息、有兜底标题、不报错（交给 I-039 那套跳过）。
func TestParseEmptySession(t *testing.T) {
	db := newFixtureDB(t, "opencode_v2.sql")
	exec(t, db, `insert into session_v2 (id, title, directory, time_created, time_updated)
		values ('ses_test_empty', null, 'D:/empty', 1700000000000, 1700000000000)`)

	adapter := Adapter{Root: db}
	var target channel.SourceFile
	for _, f := range adapter.Detect().Files {
		if strings.Contains(f.Path, "ses_test_empty") {
			target = f
		}
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
	path := filepath.Join(t.TempDir(), "opencode.db")
	handle, err := sql.Open("sqlite", "file:"+filepath.ToSlash(path))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := handle.Exec(`create table kv (key text primary key, value text)`); err != nil {
		t.Fatal(err)
	}
	handle.Close()

	got := Adapter{Root: path}.Detect()
	if got.Status != channel.StatusAvailable || len(got.Files) != 0 {
		t.Errorf("应当是「可用但没有会话」，实际 %q / %d 条（err=%v）", got.Status, len(got.Files), got.Err)
	}
}

func TestParseRejectsBadPathsAndKeys(t *testing.T) {
	adapter := Adapter{}
	for _, virtual := range []string{
		"",
		"没有井号",
		"前缀#",
		"#没有前缀",
		"库#没有冒号",
		"库#:会话ID",
	} {
		if _, err := adapter.Parse(channel.SourceFile{Path: virtual}); err == nil {
			t.Errorf("%q 应当被拒", virtual)
		}
	}
}

// Probe 与 Parse 对「有没有消息」必须给出同一个答案（I-056）。
func TestProbeMatchesParse(t *testing.T) {
	db := newFixtureDB(t, "opencode_v2.sql")
	exec(t, db, `insert into session_v2 (id, title, directory, time_created, time_updated)
		values ('ses_probe_empty', null, 'D:/empty', 1700000000000, 1700000000000)`)
	for _, name := range []string{"opencode_v2.sql", "opencode_legacy.sql"} {
		adapter := Adapter{Root: db}
		if name == "opencode_legacy.sql" {
			adapter = Adapter{Root: newFixtureDB(t, name)}
		}
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
}
