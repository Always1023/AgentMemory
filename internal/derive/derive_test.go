package derive

import (
	"reflect"
	"testing"
	"time"

	"github.com/Always1023/AgentMemory/internal/channel"
	"github.com/Always1023/AgentMemory/internal/store"
	"github.com/Always1023/AgentMemory/internal/store/storetest"
)

// seedTwoSessions 造两份会话入库：一份带工具调用，一份没有项目路径。
func seedTwoSessions(t *testing.T, db *store.DB) {
	t.Helper()
	at := time.Date(2026, 10, 20, 9, 0, 0, 0, time.UTC)
	first := channel.Session{
		Channel: "claude-code", ID: "a", Title: "甲", ProjectPath: "D:\\work\\demo",
		StartedAt: at, SourceFile: "C:\\fake\\a.jsonl",
		Messages: []channel.Message{
			{Role: channel.RoleUser, Time: at, Text: "第一句"},
			{Role: channel.RoleAssistant, Time: at, Text: "答",
				Tools: []channel.ToolCall{{Name: "read_file", Input: "x", Output: "y"}}},
		},
	}
	second := channel.Session{
		Channel: "codex", ID: "b", Title: "乙",
		StartedAt: at.Add(24 * time.Hour), SourceFile: "C:\\fake\\b.jsonl",
		Messages: []channel.Message{{Role: channel.RoleUser, Time: at.Add(24 * time.Hour), Text: "另一句"}},
	}
	if err := db.WriteSession(first, time.Now()); err != nil {
		t.Fatalf("写会话 a: %v", err)
	}
	if err := db.WriteSession(second, time.Now()); err != nil {
		t.Fatalf("写会话 b: %v", err)
	}
}

// A1：全量重建幂等可重入——跑两遍结果一致。
func TestRebuildIsIdempotent(t *testing.T) {
	db := storetest.Open(t)
	seedTwoSessions(t, db)
	registry, err := Default()
	if err != nil {
		t.Fatalf("Default: %v", err)
	}

	if err := Rebuild(db, registry); err != nil {
		t.Fatalf("第一次重建: %v", err)
	}
	first := snapshot(t, db, registry.Names())
	if err := Rebuild(db, registry); err != nil {
		t.Fatalf("第二次重建: %v", err)
	}
	second := snapshot(t, db, registry.Names())
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("重建不幂等:\n%v\n%v", first, second)
	}

	// 数字抽查：渠道维度两行，工具维度一行。
	byChannel, err := db.Dimension("by_channel")
	if err != nil {
		t.Fatal(err)
	}
	want := []store.DimensionRow{{Key: "claude-code", Value: "1"}, {Key: "codex", Value: "1"}}
	if !reflect.DeepEqual(byChannel, want) {
		t.Fatalf("by_channel = %v, 要 %v", byChannel, want)
	}
	tools, err := db.Dimension("tool_usage")
	if err != nil {
		t.Fatal(err)
	}
	if len(tools) != 1 || tools[0].Key != "claude-code|read_file" || tools[0].Value != "1" {
		t.Fatalf("tool_usage = %v", tools)
	}
}

// A2：清空维度表后重建，内容与之前一致（AC-15）。
func TestRebuildAfterWipeMatches(t *testing.T) {
	db := storetest.Open(t)
	seedTwoSessions(t, db)
	registry, err := Default()
	if err != nil {
		t.Fatal(err)
	}
	if err := Rebuild(db, registry); err != nil {
		t.Fatal(err)
	}
	before := snapshot(t, db, registry.Names())

	// 模拟「派生视图全丢」：清空维度表。
	if err := db.WipeDimensions(); err != nil {
		t.Fatal(err)
	}
	if err := Rebuild(db, registry); err != nil {
		t.Fatalf("重建: %v", err)
	}
	after := snapshot(t, db, registry.Names())
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("清空后重建不一致:\n%v\n%v", before, after)
	}
}

// A3：新增维度 = 注册表一行——用一个测试维度验证扩展点，且可以只重建它一个。
func TestNewDimensionIsPureAddition(t *testing.T) {
	db := storetest.Open(t)
	seedTwoSessions(t, db)
	registry, err := Default()
	if err != nil {
		t.Fatal(err)
	}
	if err := Rebuild(db, registry); err != nil {
		t.Fatal(err)
	}

	// 自带维度不受影响的前提下，加一个测试维度并只重建它。
	custom := testExtractor{name: "custom_test"}
	if err := registry.Register(custom); err != nil {
		t.Fatalf("注册测试维度: %v", err)
	}
	if err := Rebuild(db, registry, "custom_test"); err != nil {
		t.Fatalf("只重建测试维度: %v", err)
	}
	rows, err := db.Dimension("custom_test")
	if err != nil || len(rows) != 1 || rows[0].Key != "k" || rows[0].Value != "v" {
		t.Fatalf("custom_test = %v, %v", rows, err)
	}
	// 重名登记要被拒。
	if err := registry.Register(testExtractor{name: "custom_test"}); err == nil {
		t.Fatal("重名登记应当被拒")
	}
}

type testExtractor struct{ name string }

func (e testExtractor) Name() string { return e.name }

func (testExtractor) Extract(db *store.DB) ([]store.DimensionRow, error) {
	return []store.DimensionRow{{Key: "k", Value: "v"}}, nil
}

// snapshot 把注册表里全部维度的当前内容拍下来，供对比。
func snapshot(t *testing.T, db *store.DB, names []string) map[string][]store.DimensionRow {
	t.Helper()
	out := map[string][]store.DimensionRow{}
	for _, name := range names {
		rows, err := db.Dimension(name)
		if err != nil {
			t.Fatalf("读维度 %s: %v", name, err)
		}
		out[name] = rows
	}
	return out
}

// 同一个会话标识出现在两个源文件里（Codex 续写另开 rollout、OpenCode 的 v1/v2 表）：
// session_stats 的键必须区分来源，否则整段维度落账失败（I-083 验收反馈）。
func TestSessionStatsKeySurvivesSameSessionInTwoFiles(t *testing.T) {
	db := storetest.Open(t)
	at := time.Date(2026, 10, 20, 9, 0, 0, 0, time.UTC)
	for _, file := range []string{`C:\fake\one.jsonl`, `C:\fake\two.jsonl`} {
		s := channel.Session{
			Channel: "codex", ID: "same-key", Title: "同名会话",
			StartedAt: at, SourceFile: file,
			Messages: []channel.Message{{Role: channel.RoleUser, Time: at, Text: "同一句"}},
		}
		if err := db.WriteSession(s, time.Now()); err != nil {
			t.Fatalf("写会话 %s: %v", file, err)
		}
	}

	registry, err := Default()
	if err != nil {
		t.Fatalf("Default: %v", err)
	}
	if err := Rebuild(db, registry); err != nil {
		t.Fatalf("重建维度: %v", err)
	}
	rows, err := db.Dimension("session_stats")
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("session_stats 应有两个来源各一行，实际 %d 行: %v", len(rows), rows)
	}
}

// I-092：三个机械视角的口径——按周活跃度、星期×时段分布、规模分档。
func TestPerspectiveExtractors(t *testing.T) {
	db := storetest.Open(t)
	at := func(y int, m time.Month, d, hour int) time.Time {
		return time.Date(y, m, d, hour, 0, 0, 0, time.Local)
	}
	seed := []channel.Session{
		{ // 2026-W43，周二 17 点，2 条事件
			Channel: "claude-code", ID: "a", Title: "甲", StartedAt: at(2026, 10, 20, 17),
			SourceFile: `C:\fake\a.jsonl`,
			Messages:   []channel.Message{{Role: channel.RoleUser, Text: "一"}, {Role: channel.RoleAssistant, Text: "二"}},
		},
		{ // 2026-W43，周三 22 点，3 条事件（含工具对）
			Channel: "codex", ID: "b", Title: "乙", StartedAt: at(2026, 10, 21, 22),
			SourceFile: `C:\fake\b.jsonl`,
			Messages: []channel.Message{{Role: channel.RoleUser, Text: "三",
				Tools: []channel.ToolCall{{Name: "grep", Input: "x", Output: "y"}}}},
		},
		{ // 2026-W44，周二 9 点，4 条事件
			Channel: "codex", ID: "c", Title: "丙", StartedAt: at(2026, 10, 27, 9),
			SourceFile: `C:\fake\c.jsonl`,
			Messages: []channel.Message{
				{Role: channel.RoleUser, Text: "四"}, {Role: channel.RoleAssistant, Text: "五"},
				{Role: channel.RoleUser, Text: "六"}, {Role: channel.RoleAssistant, Text: "七"},
			},
		},
	}
	for _, s := range seed {
		if err := db.WriteSession(s, time.Now()); err != nil {
			t.Fatalf("写会话 %s: %v", s.ID, err)
		}
	}
	registry, err := Default()
	if err != nil {
		t.Fatal(err)
	}
	if err := Rebuild(db, registry); err != nil {
		t.Fatalf("重建: %v", err)
	}

	week, err := db.Dimension("weekly_activity")
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"2026-W43": "sessions=2 events=5", "2026-W44": "sessions=1 events=4"}
	if len(week) != 2 {
		t.Fatalf("按周应有 2 行，实际 %v", week)
	}
	for _, row := range week {
		if want[row.Key] != row.Value {
			t.Errorf("%s = %s，要 %s", row.Key, row.Value, want[row.Key])
		}
	}

	heat, err := db.Dimension("weekday_hour")
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, row := range heat {
		seen[row.Key] = true
	}
	for _, key := range []string{"周二|17", "周三|22", "周二|09"} {
		if !seen[key] {
			t.Errorf("时段分布缺 %s：%v", key, heat)
		}
	}

	sizes, err := db.Dimension("size_buckets")
	if err != nil {
		t.Fatal(err)
	}
	if len(sizes) != 1 || sizes[0].Key != "1-5" || sizes[0].Value != "3" {
		t.Fatalf("规模分档不对: %v", sizes)
	}
}
