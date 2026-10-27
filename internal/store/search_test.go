package store

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/Always1023/AgentMemory/internal/channel"
)

// A4 + A1：关键词命中消息正文与工具内容，中文按单字切词。
func TestSearchFindsMessagesAndToolContent(t *testing.T) {
	db := openTestDB(t)
	s := fixtureSession()
	if err := db.WriteSession(s, time.Now()); err != nil {
		t.Fatalf("写入: %v", err)
	}

	// 中文命中消息正文。
	hits, err := db.Search("归档 撞名", SearchFilter{}, 0)
	if err != nil {
		t.Fatalf("检索: %v", err)
	}
	if len(hits) != 1 {
		t.Fatalf("命中 %d 条，要 1 条", len(hits))
	}
	hit := hits[0]
	if hit.Channel != "claude-code" || hit.SessionKey != "sess-001" || hit.Kind != KindMessage {
		t.Fatalf("命中定位不对: %+v", hit)
	}
	if !strings.Contains(hit.Snippet, "撞名") {
		t.Fatalf("片段里没有命中词: %q", hit.Snippet)
	}

	// 英文命中工具内容：「plan.go」只出现在工具入参与结果里（消息正文只有单独的 Plan）。
	hits, err = db.Search("plan.go", SearchFilter{}, 0)
	if err != nil {
		t.Fatalf("检索: %v", err)
	}
	// 同一会话只列一条（I-084 验收反馈）：这里要的是「工具内容进了索引」，
	// 留哪一条由排名决定。
	if len(hits) != 1 {
		t.Fatalf("同一会话应合并成 1 条，实际 %d", len(hits))
	}
	if hits[0].Kind != KindToolCall && hits[0].Kind != KindToolResult {
		t.Fatalf("应为工具事件，实际 %s", hits[0].Kind)
	}

	// 空查询返回空结果。
	if hits, _ := db.Search("   ", SearchFilter{}, 0); len(hits) != 0 {
		t.Fatalf("空查询应当没有命中，实际 %d", len(hits))
	}
}

// A3（前半）：索引随入库增量更新——延长会话后新内容立刻能搜到。
func TestSearchReflectsIncrementalUpdates(t *testing.T) {
	db := openTestDB(t)
	s := fixtureSession()
	short := s
	short.Messages = s.Messages[:2]

	if err := db.WriteSession(short, time.Now()); err != nil {
		t.Fatal(err)
	}
	if hits, _ := db.Search("改哪一行", SearchFilter{}, 0); len(hits) != 0 {
		t.Fatalf("延长前不该搜到第三条，实际 %d", len(hits))
	}
	if err := db.WriteSession(s, time.Now()); err != nil {
		t.Fatal(err)
	}
	hits, err := db.Search("改哪一行", SearchFilter{}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 {
		t.Fatalf("追加后应命中 1 条，实际 %d", len(hits))
	}
}

// A3（后半）：改写后旧 revision 的内容撤下，搜不到旧话。
func TestSearchOnlyCoversLatestRevision(t *testing.T) {
	db := openTestDB(t)
	s := fixtureSession()
	if err := db.WriteSession(s, time.Now()); err != nil {
		t.Fatal(err)
	}
	rewritten := s
	rewritten.Messages[1].Text = "这条回答被改写过"
	if err := db.WriteSession(rewritten, time.Now()); err != nil {
		t.Fatal(err)
	}
	if hits, _ := db.Search("纯函数算出来", SearchFilter{}, 0); len(hits) != 0 {
		t.Fatalf("旧 revision 不该再命中，实际 %d", len(hits))
	}
	if hits, _ := db.Search("被改写过", SearchFilter{}, 0); len(hits) != 1 {
		t.Fatalf("新 revision 应命中 1 条，实际 %d", len(hits))
	}
}

// A3（收尾）：索引可以清掉重刷，重刷后结果一致（AC-15）。
func TestRebuildFTSRestoresIndex(t *testing.T) {
	db := openTestDB(t)
	s := fixtureSession()
	if err := db.WriteSession(s, time.Now()); err != nil {
		t.Fatal(err)
	}
	before, err := db.Search("归档 撞名", SearchFilter{}, 0)
	if err != nil || len(before) != 1 {
		t.Fatalf("建索引后命中: %v, %v", before, err)
	}

	// 先把索引真删掉（events_fts 是 L2，允许清），确认搜不到，再重刷找回。
	if _, err := db.handle.Exec(`DELETE FROM events_fts`); err != nil {
		t.Fatal(err)
	}
	if hits, _ := db.Search("归档 撞名", SearchFilter{}, 0); len(hits) != 0 {
		t.Fatalf("删掉索引后不该命中，实际 %d", len(hits))
	}

	if err := db.RebuildFTS(); err != nil {
		t.Fatalf("重刷: %v", err)
	}
	after, err := db.Search("归档 撞名", SearchFilter{}, 0)
	if err != nil || len(after) != 1 {
		t.Fatalf("重刷后命中: %v, %v", after, err)
	}
	if before[0].Seq != after[0].Seq || before[0].Snippet != after[0].Snippet {
		t.Fatalf("重刷前后不一致:\n%+v\n%+v", before[0], after[0])
	}
}

// A2（机制侧）：渠道精确、项目前缀、时间下限三类过滤都生效。
func TestSearchFilters(t *testing.T) {
	db := openTestDB(t)
	at := time.Date(2026, 10, 27, 9, 0, 0, 0, time.UTC)
	first := fixtureSession() // claude-code，项目目录在 D 盘 work 下
	if err := db.WriteSession(first, time.Now()); err != nil {
		t.Fatal(err)
	}
	other := channel.Session{
		Channel: "codex", ID: "s2", Title: "另一渠道也有归档撞名",
		ProjectPath: filepath.Join("D:", "other"),
		StartedAt:   at.Add(48 * time.Hour), SourceFile: filepath.Join("C:", "fake", "s2.jsonl"),
		Messages: []channel.Message{{Role: channel.RoleUser, Time: at.Add(48 * time.Hour), Text: "归档为什么撞名了？"}},
	}
	if err := db.WriteSession(other, time.Now()); err != nil {
		t.Fatal(err)
	}

	// 渠道过滤：只回 claude-code。
	hits, err := db.Search("撞名", SearchFilter{Channel: "claude-code"}, 0)
	if err != nil || len(hits) != 1 || hits[0].Channel != "claude-code" {
		t.Fatalf("渠道过滤: %d 条, %v", len(hits), err)
	}
	// 项目前缀：first 的项目目录命中，other 的不命中。
	hits, err = db.Search("撞名", SearchFilter{Project: "D:" + string(os.PathSeparator) + "work"}, 0)
	if err != nil || len(hits) != 1 || hits[0].SessionKey != "sess-001" {
		t.Fatalf("项目过滤: %d 条, %v", len(hits), err)
	}
	// 时间下限：只回 48 小时后的那条。
	hits, err = db.Search("撞名", SearchFilter{Since: at.Add(24 * time.Hour)}, 0)
	if err != nil || len(hits) != 1 || hits[0].SessionKey != "s2" {
		t.Fatalf("时间过滤: %d 条, %v", len(hits), err)
	}
}

// I-084 验收反馈：中文按整段短语匹配，不能把「记忆」拆成两个任意位置的字。
func TestSearchMatchesCJKPhraseNotScatteredChars(t *testing.T) {
	db := openTestDB(t)
	at := time.Date(2026, 10, 27, 9, 0, 0, 0, time.UTC)
	scattered := channel.Session{
		Channel: "codex", ID: "scattered", Title: "字都在但不成词",
		StartedAt: at, SourceFile: `C:\fake\scattered.jsonl`,
		Messages: []channel.Message{{Role: channel.RoleUser, Time: at, Text: "他记错了，后来才回忆起这件事。"}},
	}
	phrase := channel.Session{
		Channel: "codex", ID: "phrase", Title: "整词在一起",
		StartedAt: at, SourceFile: `C:\fake\phrase.jsonl`,
		Messages: []channel.Message{{Role: channel.RoleUser, Time: at, Text: "这段记忆需要归档。"}},
	}
	for _, s := range []channel.Session{scattered, phrase} {
		if err := db.WriteSession(s, time.Now()); err != nil {
			t.Fatalf("写入 %s: %v", s.ID, err)
		}
	}

	hits, err := db.Search("记忆", SearchFilter{}, 0)
	if err != nil {
		t.Fatalf("检索: %v", err)
	}
	if len(hits) != 1 || hits[0].SessionKey != "phrase" {
		t.Fatalf("「记忆」应只命中整词那条，实际 %+v", hits)
	}
}

// I-084 验收反馈：片段不能切开多字节字符，且要以命中的整词为中心。
func TestSnippetKeepsRunesAndAnchorsOnTerm(t *testing.T) {
	long := strings.Repeat("前面的铺垫。", 20) + "这里出现了记忆库这个词。" + strings.Repeat("后面的收尾。", 20)
	got := buildSnippet(long, []string{"记忆库"})
	if !utf8.ValidString(got) {
		t.Fatalf("片段切坏了多字节字符: %q", got)
	}
	if !strings.Contains(got, "记忆库") {
		t.Fatalf("片段没有以命中词为中心: %q", got)
	}
	if !strings.HasPrefix(got, "…") || !strings.HasSuffix(got, "…") {
		t.Fatalf("截断处应当有省略号: %q", got)
	}
}

// I-084 验收反馈：同一段对话在 L1 里有多份（Codex 续写、OpenCode v1/v2）时只列一条。
func TestSearchDedupesSameSessionAcrossSourceFiles(t *testing.T) {
	db := openTestDB(t)
	at := time.Date(2026, 10, 27, 9, 0, 0, 0, time.UTC)
	for _, file := range []string{`C:\fake\one.jsonl`, `C:\fake\two.jsonl`} {
		s := channel.Session{
			Channel: "codex", ID: "same", Title: "同一段对话",
			StartedAt: at, SourceFile: file,
			Messages: []channel.Message{{Role: channel.RoleUser, Time: at, Text: "记忆库怎么搜？"}},
		}
		if err := db.WriteSession(s, time.Now()); err != nil {
			t.Fatalf("写入: %v", err)
		}
	}
	hits, err := db.Search("记忆库", SearchFilter{}, 0)
	if err != nil {
		t.Fatalf("检索: %v", err)
	}
	if len(hits) != 1 {
		t.Fatalf("同一会话只该列一条，实际 %d 条", len(hits))
	}
}
