package sqlite

import (
	"database/sql"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	_ "modernc.org/sqlite"
)

// newFixture 造一个小库：写的时候用驱动，读的时候走本包（只读）。
func newFixture(t *testing.T) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "channels.db")
	handle, err := sql.Open(driverName, "file:"+filepath.ToSlash(path))
	if err != nil {
		t.Fatal(err)
	}
	defer handle.Close()

	statements := []string{
		`create table session (id text primary key, title text, created integer, blob blob)`,
		`create table message (session_id text, role text, body text)`,
		`insert into session (id, title, created, blob) values ('s1', '第一段', 1765874170168, x'00ff')`,
		`insert into session (id, title, created, blob) values ('s2', null, 1765874218509, null)`,
		`insert into message (session_id, role, body) values ('s1', 'user', '你好')`,
	}
	for _, stmt := range statements {
		if _, err := handle.Exec(stmt); err != nil {
			t.Fatalf("建夹具 %q: %v", stmt, err)
		}
	}
	return path
}

func TestOpenReadsTablesColumnsAndRows(t *testing.T) {
	db, err := Open(newFixture(t))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	tables, err := db.Tables()
	if err != nil {
		t.Fatalf("Tables: %v", err)
	}
	if len(tables) != 2 || tables[0] != "message" || tables[1] != "session" {
		t.Fatalf("表 = %v，想要 [message session]（按名字排序）", tables)
	}

	if ok, err := db.HasTable("session"); err != nil || !ok {
		t.Errorf("HasTable(session) = %v / %v，想要 true", ok, err)
	}
	if ok, err := db.HasTable("nope"); err != nil || ok {
		t.Errorf("HasTable(nope) = %v / %v，想要 false（表不存在不是错误）", ok, err)
	}

	columns, err := db.Columns("session")
	if err != nil {
		t.Fatalf("Columns: %v", err)
	}
	if strings.Join(columns, ",") != "id,title,created,blob" {
		t.Errorf("列 = %v，想要 id,title,created,blob", columns)
	}
	if ok, err := db.HasColumns("session", "id", "title"); err != nil || !ok {
		t.Errorf("HasColumns(id,title) = %v / %v，想要 true", ok, err)
	}
	// 缺列时要给 false，不报错——各家版本列名会变，适配器靠它决定走哪条路。
	if ok, err := db.HasColumns("session", "id", "不存在的列"); err != nil || ok {
		t.Errorf("HasColumns 缺列时应给 false / nil，实际 %v / %v", ok, err)
	}
	if columns, err := db.Columns("nope"); err != nil || len(columns) != 0 {
		t.Errorf("表不存在时 Columns 应给空切片 / nil，实际 %v / %v", columns, err)
	}

	rows, err := db.All(`select id, title, created, blob from session order by id`)
	if err != nil {
		t.Fatalf("All: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("读到 %d 行，想要 2 行", len(rows))
	}
	if got := rows[0].Text("title"); got != "第一段" {
		t.Errorf("第一行标题 = %q", got)
	}
	if got := rows[0].Text("id"); got != "s1" {
		t.Errorf("第一行 id = %q", got)
	}
	// 空值给空串，适配器不用到处判 nil。
	if got := rows[1].Text("title"); got != "" {
		t.Errorf("空值应当给空串，实际 %q", got)
	}
	// 二进制列转成 string：驱动层给的就是 []byte。
	if _, ok := rows[0]["blob"].(string); !ok {
		t.Errorf("blob 应当收成 string，实际 %T", rows[0]["blob"])
	}
}

func TestOpenMissingFileReportsNotExist(t *testing.T) {
	_, err := Open(filepath.Join(t.TempDir(), "没有这个库.db"))
	if err == nil {
		t.Fatal("库不在时应当报错")
	}
	if !ErrNotInstalled(err) {
		t.Errorf("库不在的错误要能 errors.Is(err, fs.ErrNotExist) 判出来，实际 %v", err)
	}
	if !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("同一个错误直接 errors.Is 也该成立，实际 %v", err)
	}
}

// 只读是硬要求（AC-01）：写语句必须被拒。
func TestOpenRefusesToWrite(t *testing.T) {
	db, err := Open(newFixture(t))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	for _, stmt := range []string{
		`delete from session`,
		`update session set title = '改掉'`,
		`insert into session (id) values ('x')`,
		`drop table session`,
	} {
		if _, err := db.All(stmt); err == nil {
			t.Errorf("%q 应当被拒（只读打开）", stmt)
		}
	}

	// 拒绝之后库还得是好的、数据还在。
	rows, err := db.All(`select count(*) as n from session`)
	if err != nil {
		t.Fatalf("写被拒之后读还要能用: %v", err)
	}
	if len(rows) != 1 || rows[0]["n"] != int64(2) {
		t.Errorf("数据应当原样还在，实际 %v", rows)
	}
}

// 文件在但不是库：报错，不崩（AC-05 的精神）。
func TestOpenGarbageFileFails(t *testing.T) {
	path := filepath.Join(t.TempDir(), "garbage.db")
	if err := os.WriteFile(path, []byte("这不是一个 SQLite 库，只是几句话"), 0o600); err != nil {
		t.Fatal(err)
	}
	db, err := Open(path)
	if err != nil {
		return // 打开就报错，也算合格
	}
	defer db.Close()
	if _, err := db.Tables(); err == nil {
		t.Error("不是库的文件读表时应当报错")
	}
}

func TestColumnsRejectsWeirdTableName(t *testing.T) {
	db, err := Open(newFixture(t))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	// 表名要拼进 pragma，带引号或分号的一律不放行。
	if _, err := db.Columns("session; drop table message"); err == nil {
		t.Error("怪表名应当被拒")
	}
	if _, err := db.Columns(""); err == nil {
		t.Error("空表名应当被拒")
	}
}
