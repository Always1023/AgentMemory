package store

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// openTestDB 开一个临时主库，测试结束自动关闭。
func openTestDB(t *testing.T) *DB {
	t.Helper()
	db, err := OpenDB(t.TempDir())
	if err != nil {
		t.Fatalf("打开主库: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// A1（前半）：空库自动建表，版本到最新。
func TestOpenDBCreatesSchema(t *testing.T) {
	db := openTestDB(t)

	got, err := db.SchemaVersion()
	if err != nil {
		t.Fatalf("读版本: %v", err)
	}
	if got != len(migrations) {
		t.Fatalf("版本 = %d, 要 %d", got, len(migrations))
	}

	// L1 两张表与三本账都该在：各插一行探针确认存在且形态对。
	if _, err := db.handle.Exec(`INSERT INTO sessions(channel, source_ref, session_key, revision, ingested_at)
		VALUES('c', 's', 'k', 1, '2026-01-01T00:00:00Z')`); err != nil {
		t.Fatalf("sessions 表不存在或形态不对: %v", err)
	}
	if _, err := db.handle.Exec(`INSERT INTO watermark(path, size, mod_time, processed_at)
		VALUES('p', 1, '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`); err != nil {
		t.Fatalf("watermark 表不存在或形态不对: %v", err)
	}
}

// A1（后半）：迁移可重入——把版本拨回去再开一遍，结果一致、数据还在。
func TestMigrationIsReentrant(t *testing.T) {
	dir := t.TempDir()
	db, err := OpenDB(dir)
	if err != nil {
		t.Fatalf("第一次打开: %v", err)
	}
	if _, err := db.handle.Exec(`INSERT INTO watermark(path, size, mod_time, processed_at)
		VALUES('p', 1, '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`); err != nil {
		t.Fatalf("插探针: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("关闭: %v", err)
	}

	// 人为把版本拨回 0，模拟「旧版本库」：再开一遍要走完整迁移。
	db2, err := OpenDB(dir)
	if err != nil {
		t.Fatalf("第二次打开: %v", err)
	}
	defer db2.Close()
	if _, err := db2.handle.Exec(`UPDATE meta SET value = '0' WHERE key = 'schema_version'`); err != nil {
		t.Fatalf("拨回版本: %v", err)
	}
	if err := db2.Close(); err != nil {
		t.Fatalf("关闭: %v", err)
	}

	db3, err := OpenDB(dir)
	if err != nil {
		t.Fatalf("第三次打开（迁移应重入成功）: %v", err)
	}
	defer db3.Close()

	v, err := db3.SchemaVersion()
	if err != nil || v != len(migrations) {
		t.Fatalf("版本 = %d, %v; 要 %d", v, err, len(migrations))
	}
	var n int
	if err := db3.handle.QueryRow(`SELECT COUNT(*) FROM watermark`).Scan(&n); err != nil {
		t.Fatalf("数水位: %v", err)
	}
	if n != 1 {
		t.Fatalf("迁移重入后探针行数 = %d, 要 1（不得重复插入）", n)
	}
}

// A2：FTS5 在 modernc.org/sqlite 下可用——建虚表（迁移已做）、写入、中文单字命中。
func TestFTS5AvailableAndMatchesChinese(t *testing.T) {
	db := openTestDB(t)

	if _, err := db.handle.Exec(`INSERT INTO events_fts(content, session_id, seq) VALUES(?, 1, 1)`,
		ftsSegment("把增量游标改成参数化查询，归档目录照旧")); err != nil {
		t.Fatalf("FTS5 不可用或写入失败: %v", err)
	}

	// 中文按单字切词后，「归档」两个字都要命中。
	var n int
	if err := db.handle.QueryRow(`SELECT COUNT(*) FROM events_fts WHERE events_fts MATCH ?`,
		ftsSegment("归档")).Scan(&n); err != nil {
		t.Fatalf("FTS5 MATCH 失败: %v", err)
	}
	if n != 1 {
		t.Fatalf("中文命中数 = %d, 要 1", n)
	}

	// 不相关的词不命中。
	if err := db.handle.QueryRow(`SELECT COUNT(*) FROM events_fts WHERE events_fts MATCH ?`,
		ftsSegment("向量")).Scan(&n); err != nil {
		t.Fatalf("FTS5 MATCH 失败: %v", err)
	}
	if n != 0 {
		t.Fatalf("不相关词命中数 = %d, 要 0", n)
	}
}

// A3：库文件损坏时报可读错误，绝不静默重建。
func TestOpenDBRejectsCorruptFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, DBFileName)
	if err := os.WriteFile(path, []byte("这不是一个数据库，只是一段文本"), 0o644); err != nil {
		t.Fatalf("写坏文件: %v", err)
	}

	db, err := OpenDB(dir)
	if err == nil {
		_ = db.Close()
		t.Fatal("打开损坏的库竟然成功了")
	}
	if !strings.Contains(err.Error(), "损坏") && !strings.Contains(err.Error(), "打不开") {
		t.Fatalf("错误里没有说清文件坏了: %v", err)
	}
	// 坏文件必须原样保留——静默重建等于替人丢数据。
	raw, readErr := os.ReadFile(path)
	if readErr != nil || string(raw) != "这不是一个数据库，只是一段文本" {
		t.Fatalf("损坏文件被改动了: %v, %q", readErr, raw)
	}
}
