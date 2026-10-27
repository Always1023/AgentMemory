package store

// 组合查询（F17）的口径与边界：分组、度量、过滤、上限、白名单。

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"testing"
	"time"

	"github.com/Always1023/AgentMemory/internal/channel"
)

// seedTwoSessions 造两份会话：claude-code（含工具）与 codex（一条消息）。
func seedTwoSessionsForQuery(t *testing.T, db *DB) {
	t.Helper()
	if err := db.WriteSession(fixtureSession(), time.Now()); err != nil {
		t.Fatalf("写 claude-code: %v", err)
	}
	at := time.Date(2026, 10, 20, 9, 0, 0, 0, time.Local)
	second := channel.Session{
		Channel: "codex", ID: "s2", Title: "另一渠道也有归档撞名",
		ProjectPath: `D:\other`, StartedAt: at, SourceFile: `C:\fake\s2.jsonl`,
		Messages: []channel.Message{{Role: channel.RoleUser, Time: at, Text: "另一渠道也有归档撞名"}},
	}
	if err := db.WriteSession(second, time.Now()); err != nil {
		t.Fatalf("写 codex: %v", err)
	}
}

func TestQueryGroupsAndMeasures(t *testing.T) {
	db := openTestDB(t)
	seedTwoSessionsForQuery(t, db)

	got, err := db.Query(context.Background(), QueryRequest{
		GroupBy:  []string{"channel"},
		Measures: []string{"sessions", "events", "chars"},
	})
	if err != nil {
		t.Fatalf("查询: %v", err)
	}
	if len(got.Rows) != 2 {
		t.Fatalf("按渠道应两行，实际 %d: %+v", len(got.Rows), got.Rows)
	}
	byChannel := map[string]QueryRow{}
	for _, row := range got.Rows {
		byChannel[row.Keys["channel"]] = row
	}
	if r := byChannel["claude-code"]; r.Values["sessions"] != 1 || r.Values["events"] != 7 {
		t.Fatalf("claude-code 口径不对: %+v", r)
	}
	if r := byChannel["codex"]; r.Values["events"] != 1 {
		t.Fatalf("codex 口径不对: %+v", r)
	}
	if byChannel["claude-code"].Values["chars"] == 0 {
		t.Fatal("字符数不该是 0")
	}

	// 按周（ISO 周，键是周一日期）+ 默认度量（会话数、事件数）。
	week, err := db.Query(context.Background(), QueryRequest{GroupBy: []string{"week"}})
	if err != nil {
		t.Fatalf("按周查询: %v", err)
	}
	if len(week.Rows) != 2 {
		t.Fatalf("按周应两行，实际 %+v", week.Rows)
	}
	if week.Rows[0].Keys["week"] != "2026-10-19" || week.Rows[1].Keys["week"] != "2026-10-26" {
		t.Fatalf("周键不对: %+v", week.Rows)
	}
	if week.Rows[0].Values["events"] != 1 || week.Rows[1].Values["events"] != 7 {
		t.Fatalf("周事件数不对: %+v", week.Rows)
	}
}

func TestQueryFilters(t *testing.T) {
	db := openTestDB(t)
	seedTwoSessionsForQuery(t, db)

	cases := []struct {
		name   string
		filter QueryFilter
		want   int64 // events 总数
	}{
		{"只算工具调用", QueryFilter{Kinds: []string{KindToolCall}}, 2},
		{"只算某个工具", QueryFilter{Tools: []string{"grep"}}, 1},
		{"只算助手消息", QueryFilter{Kinds: []string{KindMessage}, Roles: []string{channel.RoleAssistant}}, 1},
		{"按渠道", QueryFilter{Channels: []string{"codex"}}, 1},
		{"项目前缀", QueryFilter{ProjectPrefix: `D:\work`}, 7},
		{"关键词", QueryFilter{Keyword: "撞名"}, 2},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := db.Query(context.Background(), QueryRequest{Filter: tc.filter, Measures: []string{"events"}})
			if err != nil {
				t.Fatalf("查询: %v", err)
			}
			if len(got.Rows) != 1 {
				t.Fatalf("不分组的查询应只有一行: %+v", got.Rows)
			}
			if got.Rows[0].Values["events"] != tc.want {
				t.Fatalf("events = %d，要 %d", got.Rows[0].Values["events"], tc.want)
			}
		})
	}
}

func TestQueryLimits(t *testing.T) {
	db := openTestDB(t)
	seedTwoSessionsForQuery(t, db)

	one, err := db.Query(context.Background(), QueryRequest{GroupBy: []string{"channel"}, Limit: 1})
	if err != nil {
		t.Fatalf("查询: %v", err)
	}
	if len(one.Rows) != 1 || !one.Limited {
		t.Fatalf("limit=1 应截断并标 limited: %+v", one)
	}

	capped, err := db.Query(context.Background(), QueryRequest{GroupBy: []string{"channel"}, Limit: 5000})
	if err != nil {
		t.Fatalf("查询: %v", err)
	}
	if capped.Limit != queryMaxLimit {
		t.Fatalf("上限没生效: %d", capped.Limit)
	}
}

func TestQueryRejectsUnknownFields(t *testing.T) {
	db := openTestDB(t)
	if _, err := db.Query(context.Background(), QueryRequest{GroupBy: []string{"user_id"}}); err == nil {
		t.Fatal("不支持的 group_by 应当被拒")
	}
	if _, err := db.Query(context.Background(), QueryRequest{Measures: []string{"password"}}); err == nil {
		t.Fatal("不支持的 measures 应当被拒")
	}
}

// 过滤值是参数绑定，不是拼进 SQL：带引号的项目前缀不会报错，也不会改变语义。
func TestQueryBindsValues(t *testing.T) {
	db := openTestDB(t)
	seedTwoSessionsForQuery(t, db)
	got, err := db.Query(context.Background(), QueryRequest{
		Filter:   QueryFilter{ProjectPrefix: `D:\work'; DROP TABLE events; --`},
		Measures: []string{"events"},
	})
	if err != nil {
		t.Fatalf("带引号的前缀不该报错: %v", err)
	}
	if len(got.Rows) != 1 || got.Rows[0].Values["events"] != 0 {
		t.Fatalf("应命中 0 条: %+v", got.Rows)
	}
	var n int
	if err := db.handle.QueryRow(`SELECT COUNT(*) FROM events`).Scan(&n); err != nil || n == 0 {
		t.Fatalf("表被影响了: n=%d err=%v", n, err)
	}
}

// A5：查询只读——跑一次组合查询，库文件（含 WAL）内容不变。
func TestQueryDoesNotTouchDatabaseFile(t *testing.T) {
	db := openTestDB(t)
	seedTwoSessionsForQuery(t, db)

	before := hashDBFiles(t, db.Path())
	if _, err := db.Query(context.Background(), QueryRequest{
		GroupBy: []string{"channel", "week"}, Measures: []string{"sessions", "events", "chars"},
	}); err != nil {
		t.Fatalf("查询: %v", err)
	}
	after := hashDBFiles(t, db.Path())
	if before != after {
		t.Fatal("组合查询改动了库文件：应当只读")
	}
}

// hashDBFiles 把主库与它的 WAL 一起算个指纹（-shm 是锁文件，会随读变化，不算）。
func hashDBFiles(t *testing.T, path string) string {
	t.Helper()
	h := sha256.New()
	for _, p := range []string{path, path + "-wal"} {
		raw, err := os.ReadFile(p)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			t.Fatal(err)
		}
		h.Write(raw)
	}
	return hex.EncodeToString(h.Sum(nil))
}
