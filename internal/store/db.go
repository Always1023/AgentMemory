package store

// 主库（memory.db）：L1 归一化事件流（sessions / events，只追加）+ 三本账
//（水位 / 统计 / 运行记录）+ FTS5 全文索引，全部在这一个 SQLite 文件里。
//
// 三条规矩：
//
//   - **L1 只追加**（AC-14）：任何代码不得 UPDATE / DELETE sessions / events 行。
//     会话有更新时按「追加新事件」或「新开一个 revision」处理，旧账保留；
//     派生视图坏了就重建，真源永远不回头改。
//   - **可迁移**：schema_version 逐版前进，每步可重入；库版本高于程序时拒绝启动。
//   - **坏了要说**：库文件损坏时报可读错误，绝不静默重建——重建是人的决定。
//
// 依赖方向：本包只依赖共享的会话模型类型；主库放哪由调用方传入（不 import config）。

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	_ "modernc.org/sqlite" // 纯 Go 驱动，注册名为 "sqlite"
)

// DBFileName 是主库文件名。本包是它唯一的写入者。
const DBFileName = "memory.db"

// DBPath 给出主库的完整路径（baseDir 由调用方从配置取）。
func DBPath(baseDir string) string { return filepath.Join(baseDir, DBFileName) }

// busyTimeout 是等锁的时间：万一有两个进程同时跑（定时 + 手动），后到的等一等。
const busyTimeout = 15 * time.Second

// timeLayout 是库里时间戳的统一写法；Nano 保住子秒精度，往返不失真。
const timeLayout = time.RFC3339Nano

// migrations 是按版本排列的 DDL；一个元素就是一个版本，语句之间以「;」加换行分隔
// （applyMigration 按这个约定切开逐条执行）。每一步都必须可重入：在已迁移的库上
// 再跑一遍，结果不变。
var migrations = []string{
	// v1：L1 两张表（sessions / events，只追加）+ 三本账（水位 / 统计 / 运行记录）
	// + FTS5 虚表。FTS 是 L2 派生视图：DROP 后可全量重刷（AC-15）。
	`CREATE TABLE IF NOT EXISTS sessions (
		id            INTEGER PRIMARY KEY AUTOINCREMENT,
		channel       TEXT NOT NULL,
		source_ref    TEXT NOT NULL,
		session_key   TEXT NOT NULL,
		revision      INTEGER NOT NULL DEFAULT 1,
		title         TEXT NOT NULL DEFAULT '',
		project_path  TEXT NOT NULL DEFAULT '',
		started_at    TEXT NOT NULL DEFAULT '',
		message_count INTEGER NOT NULL DEFAULT 0,
		ingested_at   TEXT NOT NULL,
		UNIQUE(channel, source_ref, session_key, revision)
	);
	CREATE TABLE IF NOT EXISTS events (
		id            INTEGER PRIMARY KEY AUTOINCREMENT,
		session_id    INTEGER NOT NULL REFERENCES sessions(id),
		seq           INTEGER NOT NULL,
		kind          TEXT NOT NULL,
		ts            TEXT NOT NULL DEFAULT '',
		parent_seq    INTEGER,
		role          TEXT NOT NULL DEFAULT '',
		name          TEXT NOT NULL DEFAULT '',
		content       TEXT NOT NULL DEFAULT '',
		raw_line      TEXT NOT NULL DEFAULT '',
		raw_sha256    TEXT NOT NULL DEFAULT '',
		content_sha   TEXT NOT NULL DEFAULT '',
		UNIQUE(session_id, seq)
	);
	CREATE INDEX IF NOT EXISTS idx_events_session ON events(session_id, seq);
	CREATE INDEX IF NOT EXISTS idx_events_kind ON events(kind);
	CREATE TABLE IF NOT EXISTS watermark (
		path         TEXT PRIMARY KEY,
		size         INTEGER NOT NULL,
		mod_time     TEXT NOT NULL,
		fingerprint  TEXT NOT NULL DEFAULT '',
		failures     INTEGER NOT NULL DEFAULT 0,
		archive_path TEXT NOT NULL DEFAULT '',
		processed_at TEXT NOT NULL
	);
	CREATE TABLE IF NOT EXISTS census (
		path       TEXT PRIMARY KEY,
		size       INTEGER NOT NULL,
		mod_time   TEXT NOT NULL,
		state      TEXT NOT NULL,
		checked_at TEXT NOT NULL
	);
	CREATE TABLE IF NOT EXISTS runs (
		id            INTEGER PRIMARY KEY AUTOINCREMENT,
		at            TEXT NOT NULL,
		source        TEXT NOT NULL DEFAULT '',
		scope         TEXT NOT NULL DEFAULT '',
		found         INTEGER NOT NULL DEFAULT 0,
		new           INTEGER NOT NULL DEFAULT 0,
		archived      INTEGER NOT NULL DEFAULT 0,
		failures      INTEGER NOT NULL DEFAULT 0,
		failed        INTEGER NOT NULL DEFAULT 0,
		empty         INTEGER NOT NULL DEFAULT 0,
		backup        TEXT NOT NULL DEFAULT '',
		backup_failed INTEGER NOT NULL DEFAULT 0
	);
	CREATE INDEX IF NOT EXISTS idx_runs_at ON runs(at);
	CREATE VIRTUAL TABLE IF NOT EXISTS events_fts USING fts5(
		content,
		session_id UNINDEXED,
		seq UNINDEXED
	)`,
	// v2：维度表（L2a）。键值对形态，每个维度一个 name；全部可从 L1 重建（AC-15）。
	`CREATE TABLE IF NOT EXISTS dimensions (
		name  TEXT NOT NULL,
		key   TEXT NOT NULL,
		value TEXT NOT NULL,
		PRIMARY KEY(name, key)
	)`,
	// v3：视角表（I-093）。LLM 给出的结论是可重建的派生数据，缓存键里带模型与
	// prompt 版本——换了模型或提示词就是另一条结论，不覆盖旧的。
	`CREATE TABLE IF NOT EXISTS facets (
		name           TEXT NOT NULL,
		key            TEXT NOT NULL,
		value          TEXT NOT NULL,
		model          TEXT NOT NULL,
		prompt_version TEXT NOT NULL,
		created_at     TEXT NOT NULL DEFAULT '',
		PRIMARY KEY(name, key, model, prompt_version)
	)`,
}

// DB 是主库的读写句柄。
//
// 同一进程里，同一个库文件只开一条底层连接（见 openShared）：收集正在写的时候
// 界面还在读状态、数待收、存统计缓存，各开各的连接就会撞锁——WAL 下「先读后写」
// 的事务升级失败是**立刻**报 SQLITE_BUSY 的，busy_timeout 兜不住（I-081 验收反馈）。
type DB struct {
	handle  *sql.DB
	conn    *sharedConn
	path    string
	baseDir string // 主库所在目录：旧 JSON 三件套的只读导入从这里找
}

// sharedConn 是一个库文件的唯一底层连接 + 引用计数。
type sharedConn struct {
	handle *sql.DB
	refs   int
}

var (
	sharedMu sync.Mutex
	shared   = map[string]*sharedConn{}
)

// OpenDB 打开（必要时创建）baseDir 下的主库，并把它迁移到最新版本。
//
// 库文件存在但不是 SQLite、或损坏到读不出版本时，返回可读错误——
// 绝不静默删掉重建，那等于替人丢数据。
//
// 同一个库文件在当前进程里只建一条连接，后来者共用它（MaxOpenConns(1) 因此
// 真正串行化了全部访问）；最后一个 Close 才真的关掉。
func OpenDB(baseDir string) (*DB, error) {
	path := DBPath(baseDir)
	if info, err := os.Stat(baseDir); err == nil && !info.IsDir() {
		return nil, fmt.Errorf("主库目录 %s 不是目录", baseDir)
	}
	if err := os.MkdirAll(baseDir, 0o755); err != nil {
		return nil, fmt.Errorf("建数据目录 %s: %w", baseDir, err)
	}

	sharedMu.Lock()
	defer sharedMu.Unlock()
	if sc, ok := shared[path]; ok {
		sc.refs++
		return &DB{handle: sc.handle, conn: sc, path: path, baseDir: baseDir}, nil
	}

	dsn := fmt.Sprintf("file:%s?_pragma=busy_timeout(%d)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)",
		filepath.ToSlash(path), busyTimeout.Milliseconds())
	handle, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("打开主库 %s: %w", path, err)
	}
	// 本地单写者：一条连接串行化所有访问，不给锁竞争留口子。
	handle.SetMaxOpenConns(1)

	sc := &sharedConn{handle: handle, refs: 1}
	db := &DB{handle: handle, conn: sc, path: path, baseDir: baseDir}
	if err := db.migrate(); err != nil {
		_ = handle.Close()
		return nil, err
	}
	// 旧 JSON 三件套只在这里只读导入一次；此后它们不再是账本。
	if err := db.importLedgers(); err != nil {
		_ = handle.Close()
		return nil, fmt.Errorf("导入旧账本: %w", err)
	}
	// 换代第一次打开：旧水位说「这些源文件早收过了」，可 L1 是空的——照它跳过，
	// 主库就永远是空的（I-082 验收反馈）。第一轮收集不看水位，把源文件重读一遍，
	// 收完由 app 记下回填完成（见 L1Backfilled / MarkL1Backfilled）。
	shared[path] = sc
	return db, nil
}

// L1Backfilled 报告这个库是否已经完成「换代回填」。
//
// 旧 index.json 的水位搬进主库后，它说源文件都处理过了，但 L1 一份会话都没有。
// 回填完成前，水位只用来保「已收集」这类数字，不参与「要不要读这个文件」的判断。
func (d *DB) L1Backfilled() bool {
	var v string
	err := d.handle.QueryRow(`SELECT value FROM meta WHERE key = 'l1_backfill'`).Scan(&v)
	return err == nil
}

// MarkL1Backfilled 记下「L1 已经按源文件填过一遍」。
// 只在一轮完整的收集末尾调用；半路出错不记，下一轮继续回填。
func (d *DB) MarkL1Backfilled() error {
	_, err := d.handle.Exec(`INSERT INTO meta(key, value) VALUES('l1_backfill', '1')
		ON CONFLICT(key) DO UPDATE SET value = excluded.value`)
	if err != nil {
		return fmt.Errorf("记换代回填完成: %w", err)
	}
	return nil
}

// Path 返回主库文件路径。
func (d *DB) Path() string { return d.path }

// Close 放掉一个引用；最后一个引用才真的关句柄。
func (d *DB) Close() error {
	if d == nil || d.conn == nil {
		return nil
	}
	sharedMu.Lock()
	defer sharedMu.Unlock()
	if shared[d.path] != d.conn {
		return nil // 已经关过了
	}
	d.conn.refs--
	if d.conn.refs > 0 {
		return nil
	}
	delete(shared, d.path)
	return d.conn.handle.Close()
}

// migrate 把库迁到最新版本。每一步在事务里跑，可重入。
func (d *DB) migrate() error {
	// 先探一下这个文件到底是不是库：损坏的库连 schema 都读不出来。
	var one int
	if err := d.handle.QueryRow(`SELECT count(*) FROM sqlite_master`).Scan(&one); err != nil {
		return fmt.Errorf("主库 %s 打不开（文件可能损坏，不会自动重建）：%w", d.path, err)
	}

	if err := d.ensureMeta(); err != nil {
		return err
	}
	current, err := d.schemaVersion()
	if err != nil {
		return err
	}
	for i := current; i < len(migrations); i++ {
		if err := d.applyMigration(i + 1); err != nil {
			return err
		}
	}
	return nil
}

func (d *DB) ensureMeta() error {
	// meta 表自身用「不存在才建」的方式兜住第一次打开。
	if _, err := d.handle.Exec(`CREATE TABLE IF NOT EXISTS meta (
		key   TEXT PRIMARY KEY,
		value TEXT NOT NULL
	)`); err != nil {
		return fmt.Errorf("主库 %s 初始化 meta 表: %w", d.path, err)
	}
	return nil
}

func (d *DB) schemaVersion() (int, error) {
	var raw string
	err := d.handle.QueryRow(`SELECT value FROM meta WHERE key = 'schema_version'`).Scan(&raw)
	if err == sql.ErrNoRows {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("读主库版本: %w", err)
	}
	var v int
	if _, err := fmt.Sscanf(raw, "%d", &v); err != nil {
		return 0, fmt.Errorf("主库版本值不认识（%q）", raw)
	}
	return v, nil
}

func (d *DB) applyMigration(version int) error {
	tx, err := d.handle.Begin()
	if err != nil {
		return fmt.Errorf("开迁移事务（v%d）: %w", version, err)
	}
	defer func() { _ = tx.Rollback() }()

	for _, stmt := range strings.Split(migrations[version-1], ";\n") {
		stmt = strings.TrimSpace(stmt)
		if stmt == "" {
			continue
		}
		if _, err := tx.Exec(stmt); err != nil {
			return fmt.Errorf("迁移到 v%d 执行失败: %w", version, err)
		}
	}
	if _, err := tx.Exec(`INSERT INTO meta(key, value) VALUES('schema_version', ?)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value`, fmt.Sprint(version)); err != nil {
		return fmt.Errorf("迁移到 v%d 记版本: %w", version, err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("提交迁移 v%d: %w", version, err)
	}
	return nil
}

// SchemaVersion 返回当前库版本。测试与诊断用。
func (d *DB) SchemaVersion() (int, error) { return d.schemaVersion() }

// fmtTime 把时间写成库里的统一格式。
func fmtTime(t time.Time) string { return t.Format(timeLayout) }

// parseTime 从库里读回时间；空串与坏值回零值（调用方按「没有时间」处理）。
func parseTime(s string) time.Time {
	if s == "" {
		return time.Time{}
	}
	t, err := time.Parse(timeLayout, s)
	if err != nil {
		return time.Time{}
	}
	return t
}
