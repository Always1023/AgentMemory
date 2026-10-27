package store

// 主库转储（F5 / I-088）：把 L1 与三本账写成 NDJSON，随备份进归档目录。
//
// 为什么不直接备 .db：库是二进制、且含可重建的 FTS 索引（本机 887 MB），
// 每天提交一份等于仓库爆炸。转储只留真数据（会话头 + 事件 + 三本账），
// 并且每个会话一个压缩文件：内容没变的会话在 git 里一个字节都不动。
//
// 布局：
//
//	<dir>/sessions.ndjson            会话头（明文、只追加）
//	<dir>/events/<rowid>.ndjson.zst  每个会话一个 zstd 流，按 seq 追加（多帧拼接）
//	<dir>/watermark.ndjson           水位（整表重写，量小）
//	<dir>/census.ndjson              统计缓存（整表重写）
//	<dir>/runs.ndjson                运行记录（整表重写）
//	<dir>/meta.json                  schema 版本、导出时间、每个会话已导出到第几条
//
// 转储备份含全部对话正文，只能进私有仓库（PRD F5）。

import (
	"bufio"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/klauspost/compress/zstd"
)

// DumpStats 是一次转储的结果。
type DumpStats struct {
	// Sessions 是库里会话总数（不是这次新写的）。
	Sessions int `json:"sessions"`
	// Events 是这次新写的事件数。
	Events int `json:"events"`
	// Appended 为真表示这次真的写了新内容（git 会有变化）。
	Appended bool `json:"appended"`
}

type dumpMeta struct {
	SchemaVersion int              `json:"schema_version"`
	ExportedAt    string           `json:"exported_at"`
	EventSeq      map[string]int64 `json:"event_seq"`
}

type dumpSession struct {
	ID           int64  `json:"id"`
	Channel      string `json:"channel"`
	SourceRef    string `json:"source_ref"`
	SessionKey   string `json:"session_key"`
	Revision     int64  `json:"revision"`
	Title        string `json:"title"`
	ProjectPath  string `json:"project_path"`
	StartedAt    string `json:"started_at"`
	MessageCount int    `json:"message_count"`
	IngestedAt   string `json:"ingested_at"`
}

type dumpEvent struct {
	ID         int64  `json:"id"`
	SessionID  int64  `json:"session_id"`
	Seq        int    `json:"seq"`
	Kind       string `json:"kind"`
	TS         string `json:"ts"`
	ParentSeq  *int   `json:"parent_seq,omitempty"`
	Role       string `json:"role"`
	Name       string `json:"name"`
	Content    string `json:"content"`
	RawLine    string `json:"raw_line,omitempty"`
	RawSHA256  string `json:"raw_sha256,omitempty"`
	ContentSHA string `json:"content_sha"`
}

// DumpNDJSON 把主库增量转储到 dir。
func (d *DB) DumpNDJSON(dir string) (DumpStats, error) {
	stats := DumpStats{}
	if err := os.MkdirAll(filepath.Join(dir, "events"), 0o755); err != nil {
		return stats, fmt.Errorf("建转储目录: %w", err)
	}
	meta := readDumpMeta(dir)

	sessions, err := d.allSessions()
	if err != nil {
		return stats, err
	}
	stats.Sessions = len(sessions)

	// 一、会话头：按 id 只追加。
	seen, err := readDumpSessionIDs(filepath.Join(dir, "sessions.ndjson"))
	if err != nil {
		return stats, err
	}
	appended, err := appendSessions(filepath.Join(dir, "sessions.ndjson"), sessions, seen)
	if err != nil {
		return stats, err
	}
	stats.Appended = appended

	// 二、事件：每个会话一个 zstd 流，按 seq 追加。
	for _, s := range sessions {
		key := strconv.FormatInt(s.ID, 10)
		last := meta.EventSeq[key]
		if last == 0 {
			last, err = lastSeqInDump(filepath.Join(dir, "events", key+".ndjson.zst"))
			if err != nil {
				return stats, err
			}
		}
		n, lastSeq, wrote, err := d.appendEvents(filepath.Join(dir, "events", key+".ndjson.zst"), s.ID, last)
		if err != nil {
			return stats, err
		}
		stats.Events += n
		if wrote {
			stats.Appended = true
		}
		if lastSeq > 0 {
			meta.EventSeq[key] = lastSeq
		}
	}

	// 三、三本账整表重写（量小）。
	if err := d.rewriteLedgers(dir); err != nil {
		return stats, err
	}

	// 四、meta：记录导出时间与每个会话的进度。
	meta.SchemaVersion = len(migrations)
	meta.ExportedAt = time.Now().Format(time.RFC3339Nano)
	if meta.EventSeq == nil {
		meta.EventSeq = map[string]int64{}
	}
	if err := writeJSONFile(filepath.Join(dir, "meta.json"), meta); err != nil {
		return stats, err
	}
	return stats, nil
}

func (d *DB) allSessions() ([]dumpSession, error) {
	rows, err := d.handle.Query(`SELECT id, channel, source_ref, session_key, revision,
		title, project_path, started_at, message_count, ingested_at FROM sessions ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("读会话: %w", err)
	}
	defer rows.Close()
	var out []dumpSession
	for rows.Next() {
		var s dumpSession
		if err := rows.Scan(&s.ID, &s.Channel, &s.SourceRef, &s.SessionKey, &s.Revision,
			&s.Title, &s.ProjectPath, &s.StartedAt, &s.MessageCount, &s.IngestedAt); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

func readDumpSessionIDs(path string) (map[int64]bool, error) {
	out := map[int64]bool{}
	f, err := os.Open(path)
	if os.IsNotExist(err) {
		return out, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64<<10), 8<<20)
	for sc.Scan() {
		var s struct {
			ID int64 `json:"id"`
		}
		if json.Unmarshal(sc.Bytes(), &s) == nil && s.ID > 0 {
			out[s.ID] = true
		}
	}
	return out, sc.Err()
}

func appendSessions(path string, sessions []dumpSession, seen map[int64]bool) (bool, error) {
	var pending []dumpSession
	for _, s := range sessions {
		if !seen[s.ID] {
			pending = append(pending, s)
		}
	}
	if len(pending) == 0 {
		return false, nil
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return false, fmt.Errorf("写会话转储: %w", err)
	}
	defer f.Close()
	enc := json.NewEncoder(f)
	for _, s := range pending {
		if err := enc.Encode(s); err != nil {
			return false, fmt.Errorf("写会话转储: %w", err)
		}
	}
	return true, nil
}

// appendEvents 追加 seq > after 的事件。
func (d *DB) appendEvents(path string, sessionID, after int64) (int, int64, bool, error) {
	// 先看有没有新事件：没有就一个字节都不写（空 zstd 帧也会让 git 认为文件变了）。
	var maxSeq sql.NullInt64
	if err := d.handle.QueryRow(`SELECT MAX(seq) FROM events WHERE session_id = ?`, sessionID).Scan(&maxSeq); err != nil {
		return 0, after, false, fmt.Errorf("读事件序号: %w", err)
	}
	if !maxSeq.Valid || maxSeq.Int64 <= after {
		return 0, after, false, nil
	}

	rows, err := d.handle.Query(`SELECT id, session_id, seq, kind, ts, parent_seq, role, name,
		content, raw_line, raw_sha256, content_sha
		FROM events WHERE session_id = ? AND seq > ? ORDER BY seq`, sessionID, after)
	if err != nil {
		return 0, after, false, fmt.Errorf("读事件: %w", err)
	}
	defer rows.Close()

	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return 0, after, false, fmt.Errorf("写事件转储: %w", err)
	}
	defer f.Close()
	enc, err := zstd.NewWriter(f, zstd.WithEncoderConcurrency(1))
	if err != nil {
		return 0, after, false, err
	}
	data := json.NewEncoder(enc)
	n := 0
	last := after
	for rows.Next() {
		var e dumpEvent
		var parent sql.NullInt64
		if err := rows.Scan(&e.ID, &e.SessionID, &e.Seq, &e.Kind, &e.TS, &parent,
			&e.Role, &e.Name, &e.Content, &e.RawLine, &e.RawSHA256, &e.ContentSHA); err != nil {
			return 0, last, false, err
		}
		if parent.Valid {
			p := int(parent.Int64)
			e.ParentSeq = &p
		}
		if err := data.Encode(e); err != nil {
			return 0, last, false, fmt.Errorf("写事件转储: %w", err)
		}
		n++
		last = int64(e.Seq)
	}
	if err := rows.Err(); err != nil {
		return 0, last, false, err
	}
	if err := enc.Close(); err != nil {
		return 0, last, false, err
	}
	if n == 0 {
		return 0, last, false, nil
	}
	return n, last, true, nil
}

func (d *DB) rewriteLedgers(dir string) error {
	entries, err := d.loadWatermark()
	if err != nil {
		return err
	}
	keys := make([]string, 0, len(entries))
	for k := range entries {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	watermarks := make([]Entry, 0, len(keys))
	for _, k := range keys {
		watermarks = append(watermarks, entries[k])
	}
	if err := writeLines(filepath.Join(dir, "watermark.ndjson"), watermarks); err != nil {
		return err
	}

	census, err := d.loadCensus()
	if err != nil {
		return err
	}
	ckeys := make([]string, 0, len(census))
	for k := range census {
		ckeys = append(ckeys, k)
	}
	sort.Strings(ckeys)
	crows := make([]CensusEntry, 0, len(ckeys))
	for _, k := range ckeys {
		crows = append(crows, census[k])
	}
	if err := writeAnyLines(filepath.Join(dir, "census.ndjson"), crows); err != nil {
		return err
	}

	runs, err := d.ReadRuns(3650, time.Now())
	if err != nil {
		return err
	}
	return writeAnyLines(filepath.Join(dir, "runs.ndjson"), runs)
}

func writeLines(path string, rows []Entry) error {
	f, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("写 %s: %w", path, err)
	}
	defer f.Close()
	enc := json.NewEncoder(f)
	for _, r := range rows {
		if err := enc.Encode(r); err != nil {
			return err
		}
	}
	return nil
}

func writeAnyLines[T any](path string, rows []T) error {
	f, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("写 %s: %w", path, err)
	}
	defer f.Close()
	enc := json.NewEncoder(f)
	for _, r := range rows {
		if err := enc.Encode(r); err != nil {
			return err
		}
	}
	return nil
}

func writeJSONFile(path string, v any) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	enc := json.NewEncoder(f)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

func readDumpMeta(dir string) dumpMeta {
	raw, err := os.ReadFile(filepath.Join(dir, "meta.json"))
	if err != nil {
		return dumpMeta{EventSeq: map[string]int64{}}
	}
	var meta dumpMeta
	if err := json.Unmarshal(raw, &meta); err != nil || meta.EventSeq == nil {
		return dumpMeta{EventSeq: map[string]int64{}}
	}
	return meta
}

// lastSeqInDump 读一个压缩转储文件里最大的 seq；文件不存在返回 0。
func lastSeqInDump(path string) (int64, error) {
	f, err := os.Open(path)
	if os.IsNotExist(err) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	defer f.Close()
	dec, err := zstd.NewReader(f)
	if err != nil {
		return 0, fmt.Errorf("读转储 %s: %w", filepath.Base(path), err)
	}
	defer dec.Close()
	var last int64
	reader := json.NewDecoder(dec)
	for {
		var e struct {
			Seq int64 `json:"seq"`
		}
		if err := reader.Decode(&e); err == io.EOF {
			break
		} else if err != nil {
			return last, err
		}
		if e.Seq > last {
			last = e.Seq
		}
	}
	return last, nil
}

// RestoreNDJSON 把一份转储导回一个空目录下的新主库（换机器 / 灾后恢复）。
func RestoreNDJSON(dir, baseDir string) (*DB, error) {
	db, err := OpenDB(baseDir)
	if err != nil {
		return nil, err
	}
	if err := db.restoreFrom(dir); err != nil {
		_ = db.Close()
		return nil, err
	}
	return db, nil
}

func (d *DB) restoreFrom(dir string) error {
	sessions, err := os.Open(filepath.Join(dir, "sessions.ndjson"))
	if err != nil {
		return fmt.Errorf("读会话转储: %w", err)
	}
	defer sessions.Close()

	files, err := os.ReadDir(filepath.Join(dir, "events"))
	if err != nil {
		return fmt.Errorf("读事件转储: %w", err)
	}
	eventFiles := make([]string, 0, len(files))
	for _, f := range files {
		if !f.IsDir() && strings.HasSuffix(f.Name(), ".ndjson.zst") {
			eventFiles = append(eventFiles, filepath.Join(dir, "events", f.Name()))
		}
	}
	sort.Strings(eventFiles)

	tx, err := d.handle.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	sc := bufio.NewScanner(sessions)
	sc.Buffer(make([]byte, 0, 64<<10), 8<<20)
	for sc.Scan() {
		var s dumpSession
		if err := json.Unmarshal(sc.Bytes(), &s); err != nil {
			return fmt.Errorf("解析会话转储: %w", err)
		}
		if _, err := tx.Exec(`INSERT INTO sessions
			(id, channel, source_ref, session_key, revision, title, project_path, started_at, message_count, ingested_at)
			VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			s.ID, s.Channel, s.SourceRef, s.SessionKey, s.Revision, s.Title, s.ProjectPath,
			s.StartedAt, s.MessageCount, s.IngestedAt); err != nil {
			return fmt.Errorf("恢复会话 %d: %w", s.ID, err)
		}
	}
	if err := sc.Err(); err != nil {
		return err
	}

	for _, path := range eventFiles {
		if err := restoreEventsFile(tx, path); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	// FTS 是派生视图：恢复后重建。
	return d.RebuildFTS()
}

func restoreEventsFile(tx *sql.Tx, path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	dec, err := zstd.NewReader(f)
	if err != nil {
		return fmt.Errorf("解压 %s: %w", filepath.Base(path), err)
	}
	defer dec.Close()

	reader := json.NewDecoder(dec)
	for {
		var e dumpEvent
		if err := reader.Decode(&e); err == io.EOF {
			break
		} else if err != nil {
			return fmt.Errorf("解析事件转储 %s: %w", filepath.Base(path), err)
		}
		var parent any
		if e.ParentSeq != nil {
			parent = *e.ParentSeq
		}
		if _, err := tx.Exec(`INSERT INTO events
			(id, session_id, seq, kind, ts, parent_seq, role, name, content, raw_line, raw_sha256, content_sha)
			VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			e.ID, e.SessionID, e.Seq, e.Kind, e.TS, parent, e.Role, e.Name, e.Content,
			e.RawLine, e.RawSHA256, e.ContentSHA); err != nil {
			return fmt.Errorf("恢复事件 %d/%d: %w", e.SessionID, e.Seq, err)
		}
	}
	return nil
}
