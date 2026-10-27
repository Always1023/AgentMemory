package store

import (
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/Always1023/AgentMemory/internal/channel"
)

// fixtureSession 造一份有代表性会话：中英混排、工具调用。共 3 条消息、2 个工具，
// 展开后 7 个事件（1 + 1+4 + 1）。
func fixtureSession() channel.Session {
	at := time.Date(2026, 10, 27, 9, 30, 0, 0, time.UTC)
	return channel.Session{
		Channel:     "claude-code",
		ID:          "sess-001",
		Title:       "修归档撞名",
		ProjectPath: "D:\\work\\demo",
		StartedAt:   at,
		SourceFile:  "C:\\fake\\.claude\\projects\\demo\\sess-001.jsonl",
		Messages: []channel.Message{
			{Role: channel.RoleUser, Time: at, Text: "归档为什么撞名了？"},
			{Role: channel.RoleAssistant, Time: at.Add(time.Minute), Text: "路径是纯函数算出来的，检查 Plan。",
				Tools: []channel.ToolCall{
					{Name: "read_file", Input: `{"path":"plan.go"}`, Output: "func Plan(...)"},
					{Name: "grep", Input: "Plan(", Output: "plan.go:12"},
				}},
			{Role: channel.RoleUser, Time: at.Add(2 * time.Minute), Text: "那改哪一行？"},
		},
	}
}

func identOf(s channel.Session) sessionIdentity {
	return sessionIdentity{Channel: s.Channel, SourceRef: s.SourceFile, SessionKey: s.ID}
}

// assertLedger 校验 revision 个数与事件总行数。
func assertLedger(t *testing.T, db *DB, revisions, events int) {
	t.Helper()
	var revCount, evCount int
	if err := db.handle.QueryRow(`SELECT COUNT(*) FROM sessions`).Scan(&revCount); err != nil {
		t.Fatalf("数会话: %v", err)
	}
	if err := db.handle.QueryRow(`SELECT COUNT(*) FROM events`).Scan(&evCount); err != nil {
		t.Fatalf("数事件: %v", err)
	}
	if revCount != revisions || evCount != events {
		t.Fatalf("revision 数 = %d（要 %d），事件数 = %d（要 %d）", revCount, revisions, evCount, events)
	}
}

// A1：写入后回组的会话模型与原模型逐字段一致（含工具调用与零值时间之外的字段）。
func TestWriteThenReadRoundTrips(t *testing.T) {
	db := openTestDB(t)
	s := fixtureSession()

	if err := db.WriteSession(s, time.Now()); err != nil {
		t.Fatalf("写入: %v", err)
	}

	got, ok, err := db.ReadSession(s.Channel, s.SourceFile, s.ID)
	if err != nil || !ok {
		t.Fatalf("回组: ok=%v, err=%v", ok, err)
	}
	if !reflect.DeepEqual(got, s) {
		t.Fatalf("回组不一致:\n原 %+v\n回 %+v", s, got)
	}
}

// A2（前半）：同一会话重复入库零重复——还是 1 个 revision、7 行事件。
func TestWriteSessionIdempotent(t *testing.T) {
	db := openTestDB(t)
	s := fixtureSession()

	if err := db.WriteSession(s, time.Now()); err != nil {
		t.Fatalf("第一次写入: %v", err)
	}
	if err := db.WriteSession(s, time.Now()); err != nil {
		t.Fatalf("重复写入: %v", err)
	}
	assertLedger(t, db, 1, 7)
}

// A2（后半）：会话延长只追加新事件；历史被改写则新开 revision，旧账保留。
func TestAppendDeltaAndDivergence(t *testing.T) {
	db := openTestDB(t)
	s := fixtureSession()
	short := s
	short.Messages = s.Messages[:2] // 只有前两条：6 个事件

	if err := db.WriteSession(short, time.Now()); err != nil {
		t.Fatalf("写短版: %v", err)
	}
	assertLedger(t, db, 1, 6)

	// 延长：追加第三条，不新开 revision。
	if err := db.WriteSession(s, time.Now()); err != nil {
		t.Fatalf("写全量: %v", err)
	}
	assertLedger(t, db, 1, 7)

	// 改写：中间消息变了 → 新开 revision，两份账都在（AC-14）。
	rewritten := s
	rewritten.Messages[1].Text = "这条回答被改写过"
	if err := db.WriteSession(rewritten, time.Now()); err != nil {
		t.Fatalf("改写后写入: %v", err)
	}
	assertLedger(t, db, 2, 14)

	// 回组拿到的是最新 revision。
	got, ok, err := db.ReadSession(s.Channel, s.SourceFile, s.ID)
	if err != nil || !ok {
		t.Fatalf("回组: %v", err)
	}
	if got.Messages[1].Text != "这条回答被改写过" {
		t.Fatalf("回组不是最新 revision: %q", got.Messages[1].Text)
	}

	// FTS 只跟最新 revision：旧版独有内容撤下后不再命中。
	//（改写前后都含「归档」相关词，这里搜只在旧版出现过的完整原句片段。）
	var n int
	if err := db.handle.QueryRow(`SELECT COUNT(*) FROM events_fts WHERE events_fts MATCH ?`,
		ftsSegment("纯函数算出来")).Scan(&n); err != nil {
		t.Fatalf("查 FTS: %v", err)
	}
	if n != 0 {
		t.Fatalf("旧 revision 的全文索引没撤下，命中 %d", n)
	}
}

// A3：双时间戳——ts 取事件时间，ingested_at 取入库时刻。
func TestDualTimestamps(t *testing.T) {
	db := openTestDB(t)
	s := fixtureSession()
	ingested := time.Date(2026, 10, 27, 20, 0, 0, 0, time.UTC)
	if err := db.WriteSession(s, ingested); err != nil {
		t.Fatalf("写入: %v", err)
	}

	var ts, ingestedAt string
	if err := db.handle.QueryRow(`SELECT events.ts, sessions.ingested_at FROM events
		JOIN sessions ON events.session_id = sessions.id
		WHERE sessions.channel = 'claude-code' LIMIT 1`).Scan(&ts, &ingestedAt); err != nil {
		t.Fatalf("查时间戳: %v", err)
	}
	if ts != "2026-10-27T09:30:00Z" {
		t.Fatalf("事件 ts = %q", ts)
	}
	if ingestedAt != "2026-10-27T20:00:00Z" {
		t.Fatalf("ingested_at = %q", ingestedAt)
	}
}

// A4：L1 只追加——本包源码里不得出现对 sessions / events 的 UPDATE / DELETE 语句。
// （events_fts 是 L2 派生视图，清它不算；\b 让 events_fts 不被误伤。）
func TestL1HasNoUpdateOrDelete(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("读包目录: %v", err)
	}
	forbidden := []string{
		`UPDATE\s+(sessions|events)\b`,
		`DELETE\s+FROM\s+(sessions|events)\b`,
	}
	patterns := make([]*regexp.Regexp, len(forbidden))
	for i, p := range forbidden {
		patterns[i] = regexp.MustCompile(p)
	}
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(".", name))
		if err != nil {
			t.Fatalf("读 %s: %v", name, err)
		}
		text := string(raw)
		for i, pattern := range patterns {
			if pattern.MatchString(text) {
				t.Errorf("%s 含违禁语句 %q（AC-14：L1 只追加）", name, forbidden[i])
			}
		}
	}
}
