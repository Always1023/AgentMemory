// Package sqlite 给「对话存在 SQLite 库里」的那些渠道一个共用的只读入口。
//
// 三条规矩，与项目约定一一对应：
//
//   - **只读**：一律以 `mode=ro` + `query_only` 打开，本包没有任何写渠道库的语句（AC-01）；
//   - **不猜**：表不存在、列不存在就返回「没有」或空结果，不报错——各家版本差异大，
//     一条读不出来不该把整轮收集带崩（AC-05 的精神）；
//   - **有上限**：一次读的行数封顶（AC-07），免得碰上大得离谱的库把内存吃光。
//
// 本包不认识任何一家产品的表结构：那是各个渠道适配器自己的事。
package sqlite

import (
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	_ "modernc.org/sqlite" // 纯 Go 驱动，注册名为 "sqlite"
)

const (
	// driverName 是驱动注册的名字。
	driverName = "sqlite"
	// busyTimeout 是等锁的时间：库可能正被产品自己占着（比如 Cursor 开着）。
	busyTimeout = 5 * time.Second
	// maxRows 是一次查询最多取多少行（AC-07）。
	maxRows = 200_000
)

// Row 是一行数据：键是列名。
//
// 值按「读出来是什么就是什么」给：整数是 int64、浮点是 float64、文本与二进制都转成 string
// （sqlite 里这两种在驱动层都是 []byte）、空值是 nil。
type Row map[string]any

// DB 是一个只读打开的库。
type DB struct {
	handle *sql.DB
	path   string
}

// Open 以只读方式打开一个库。
//
// 文件不存在时返回的错误可以 errors.Is(err, fs.ErrNotExist) 判出来——调用方按「没装」处理，
// 别当成失败。文件在但不是个库时会在这一步或第一步查询时报错。
func Open(path string) (*DB, error) {
	if _, err := os.Stat(path); err != nil {
		return nil, err
	}

	// mode=ro 是只读；query_only 再上一道保险：万一哪里写了 INSERT/UPDATE，SQLite 直接拒绝。
	dsn := fmt.Sprintf("file:%s?mode=ro&_pragma=busy_timeout(%d)&_pragma=query_only(true)",
		filepath.ToSlash(path), busyTimeout.Milliseconds())

	handle, err := sql.Open(driverName, dsn)
	if err != nil {
		return nil, fmt.Errorf("打开 %s: %w", path, err)
	}
	// 渠道库不是我们的事务战场：一次一条连接，省得自己跟自己抢锁。
	handle.SetMaxOpenConns(1)

	if err := handle.Ping(); err != nil {
		_ = handle.Close()
		return nil, fmt.Errorf("打开 %s: %w", path, err)
	}
	return &DB{handle: handle, path: path}, nil
}

// Path 返回这个库的路径。
func (d *DB) Path() string { return d.path }

// Close 关掉句柄。收集结束就该关，别一直占着别人的库。
func (d *DB) Close() error { return d.handle.Close() }

// Tables 返回库里的表名，按名字排序；如果你还想要视图，这里只给表。
func (d *DB) Tables() ([]string, error) {
	rows, err := d.All(`select name from sqlite_master where type = 'table' order by name`)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(rows))
	for _, row := range rows {
		if name, ok := row["name"].(string); ok && name != "" {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out, nil
}

// HasTable 判断有没有这张表。
func (d *DB) HasTable(name string) (bool, error) {
	tables, err := d.Tables()
	if err != nil {
		return false, err
	}
	for _, t := range tables {
		if t == name {
			return true, nil
		}
	}
	return false, nil
}

// Columns 返回一张表的列名；表不存在时返回空切片，不是错误。
func (d *DB) Columns(table string) ([]string, error) {
	if !safeName(table) {
		return nil, fmt.Errorf("表名 %q 里有不认识的字符", table)
	}
	rows, err := d.All(fmt.Sprintf("pragma table_info(%q)", table))
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(rows))
	for _, row := range rows {
		if name, ok := row["name"].(string); ok && name != "" {
			out = append(out, name)
		}
	}
	return out, nil
}

// HasColumns 判断一张表是不是这些列都有——列名各家版本会变，先问再读。
func (d *DB) HasColumns(table string, wanted ...string) (bool, error) {
	have, err := d.Columns(table)
	if err != nil {
		return false, err
	}
	for _, want := range wanted {
		found := false
		for _, h := range have {
			if h == want {
				found = true
				break
			}
		}
		if !found {
			return false, nil
		}
	}
	return true, nil
}

// All 跑一条查询，把结果全读出来；行数超过上限就报错，不硬撑着读。
func (d *DB) All(query string, args ...any) ([]Row, error) {
	rows, err := d.handle.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("查询 %s: %w", d.path, err)
	}
	defer rows.Close()

	columns, err := rows.Columns()
	if err != nil {
		return nil, fmt.Errorf("读列名 %s: %w", d.path, err)
	}

	out := make([]Row, 0, 16)
	for rows.Next() {
		if len(out) >= maxRows {
			return nil, fmt.Errorf("%s 的结果超过 %d 行上限，换个更窄的查询", d.path, maxRows)
		}
		cells := make([]any, len(columns))
		pointers := make([]any, len(columns))
		for i := range cells {
			pointers[i] = &cells[i]
		}
		if err := rows.Scan(pointers...); err != nil {
			return nil, fmt.Errorf("读一行 %s: %w", d.path, err)
		}

		row := make(Row, len(columns))
		for i, name := range columns {
			row[name] = normalize(cells[i])
		}
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("读 %s: %w", d.path, err)
	}
	return out, nil
}

// Text 取一行的某个列当文本用；空值给空串。
//
// 适配器读到的多半是 TEXT 或 JSON 串，统一走这里，省得每个渠道各写一遍断言。
func (r Row) Text(column string) string {
	switch v := r[column].(type) {
	case nil:
		return ""
	case string:
		return v
	default:
		return fmt.Sprint(v)
	}
}

// safeName 只放行普通标识符：表名要拼进 pragma，别让它带进别的字符。
func safeName(name string) bool {
	if name == "" {
		return false
	}
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_':
		default:
			return false
		}
	}
	return true
}

// normalize 把驱动给的值收成人好用的形状：二进制与文本都给 string。
func normalize(value any) any {
	switch v := value.(type) {
	case []byte:
		return string(v)
	case time.Time:
		return v
	default:
		return v
	}
}

// ErrNotInstalled 是「这个库不在」的统一判断。
//
// 适配器拿它包一层就能统一成「未安装」，不必各自 import io/fs。
func ErrNotInstalled(err error) bool {
	return errors.Is(err, fs.ErrNotExist)
}

// VirtualSep 把「库路径」与「库里的某一条记录」隔开，凑出一个虚拟的源文件路径。
//
// 库型渠道没有「一个会话一个文件」这回事——一个库装着很多次会话。适配器把每条会话
// 报成一个虚拟的源文件（`<库路径>#<记录键>`），游标的增量判断才能落在「这一次会话」
// 而不是「整个库」上。
const VirtualSep = "#"

// VirtualPath 拼一个虚拟源文件路径。
func VirtualPath(dbPath, key string) string { return dbPath + VirtualSep + key }

// SplitVirtualPath 拆开虚拟源文件路径；不成对时第二个返回值为 false。
//
// 从**最后一个**分隔符切：Windows 路径里不会有 `#`，而键里可能出现。
func SplitVirtualPath(virtual string) (dbPath, key string, ok bool) {
	idx := strings.LastIndex(virtual, VirtualSep)
	if idx <= 0 || idx == len(virtual)-1 {
		return "", "", false
	}
	return virtual[:idx], virtual[idx+1:], true
}
