package store

// 主库转储（I-088）：增量、可恢复、只写变了的会话。

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Always1023/AgentMemory/internal/channel"
)

func TestDumpIncremental(t *testing.T) {
	db := openTestDB(t)
	seedTwoSessionsForQuery(t, db)
	dir := t.TempDir()

	first, err := db.DumpNDJSON(dir)
	if err != nil {
		t.Fatalf("第一次转储: %v", err)
	}
	if first.Sessions != 2 || first.Events != 8 || !first.Appended {
		t.Fatalf("第一次转储统计不对: %+v", first)
	}
	if _, err := os.Stat(filepath.Join(dir, "events", "1.ndjson.zst")); err != nil {
		t.Fatalf("会话 1 的转储文件不存在: %v", err)
	}

	// 没有任何变化：第二次不应写内容。
	second, err := db.DumpNDJSON(dir)
	if err != nil {
		t.Fatalf("第二次转储: %v", err)
	}
	if second.Appended || second.Events != 0 {
		t.Fatalf("没有变化时不该写: %+v", second)
	}

	// 加会话：只追加新会话的事件。
	extra := channel.Session{
		Channel: "pi", ID: "s3", Title: "第三个会话",
		StartedAt: time.Now(), SourceFile: `C:ake\s3.jsonl`, Messages: []channel.Message{{Role: channel.RoleUser, Text: "新的一条"}},
	}
	if err := db.WriteSession(extra, time.Now()); err != nil {
		t.Fatal(err)
	}
	third, err := db.DumpNDJSON(dir)
	if err != nil {
		t.Fatalf("第三次转储: %v", err)
	}
	if !third.Appended || third.Sessions != 3 || third.Events == 0 {
		t.Fatalf("新会话应写入: %+v", third)
	}
}

func TestRestoreRoundTrip(t *testing.T) {
	db := openTestDB(t)
	seedTwoSessionsForQuery(t, db)
	dir := t.TempDir()
	if _, err := db.DumpNDJSON(dir); err != nil {
		t.Fatalf("转储: %v", err)
	}

	restored, err := RestoreNDJSON(dir, t.TempDir())
	if err != nil {
		t.Fatalf("恢复: %v", err)
	}
	defer restored.Close()

	var sessions, events int
	if err := restored.handle.QueryRow(`SELECT COUNT(*) FROM sessions`).Scan(&sessions); err != nil {
		t.Fatal(err)
	}
	if err := restored.handle.QueryRow(`SELECT COUNT(*) FROM events`).Scan(&events); err != nil {
		t.Fatal(err)
	}
	if sessions != 2 || events != 8 {
		t.Fatalf("恢复后行数不对: sessions=%d events=%d", sessions, events)
	}

	// 会话内容逐字段回来（回组一致）。
	want := fixtureSession()
	got, ok, err := restored.ReadSession(want.Channel, want.SourceFile, want.ID)
	if err != nil || !ok {
		t.Fatalf("恢复后读不到会话: ok=%v err=%v", ok, err)
	}
	if got.Title != want.Title || len(got.Messages) != len(want.Messages) {
		t.Fatalf("恢复的会话不对: %+v", got)
	}
	if got.Messages[1].Tools[0].Output != want.Messages[1].Tools[0].Output {
		t.Fatalf("工具结果没回来: %+v", got.Messages[1].Tools)
	}

	// 派生视图重建：关键词能搜到。
	hits, err := restored.Search("撞名", SearchFilter{}, 0)
	if err != nil || len(hits) == 0 {
		t.Fatalf("恢复后搜索应当有结果: %d %v", len(hits), err)
	}
}
