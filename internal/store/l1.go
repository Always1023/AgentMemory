package store

// L1 归一化事件流：sessions / events 两张表，唯一真源，只追加（AC-14）。
//
// 写入规则（幂等回灌的核心）：
//
//   - 同一会话再次入库时，先取最新 revision 的事件指纹逐位比对：
//     新事件列表恰好是旧的**延长**（前缀一致且更长）→ 只追加多出来的事件；
//     完全一致 → 什么都不做；只要有一位对不上（历史被改写、压缩、缩短）→
//     新开一个 revision，旧 revision 的账原样保留。
//   - 会话行一旦写入不再修改（标题变了也走新 revision）；message_count
//     记的是「这个 revision 建立时的条数」，读取方一律以 COUNT 事件为准。
//
// 消息与工具调用的展开：一条 message 事件跟它的工具事件（tool_call 与
// 配对的 tool_result，parent_seq 指回调用）共用一段连续 seq。

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/Always1023/AgentMemory/internal/channel"
)

// 事件种类。message 是一条对话消息；tool_call / tool_result 是消息里的
// 工具调用与结果；其余（thinking、附件等）各适配器愿意给就进 other，
// 第一批先只展开统一会话模型里有的东西。
const (
	KindMessage    = "message"
	KindToolCall   = "tool_call"
	KindToolResult = "tool_result"
)

// eventRow 是一条待写事件。
type eventRow struct {
	seq        int
	kind       string
	ts         string
	parentSeq  int // 0 表示没有
	role       string
	name       string
	content    string
	contentSha string
}

// expandEvents 把统一会话模型展开成事件行（seq 从 1 连续递增）。
// tool_result 的 parent_seq 回填为它前面最近的 tool_call 的 seq。
func expandEvents(s channel.Session) []eventRow {
	out := make([]eventRow, 0, len(s.Messages)*2)
	for _, m := range s.Messages {
		out = append(out, eventRow{
			kind:    KindMessage,
			ts:      fmtTimeOrEmpty(m.Time),
			role:    m.Role,
			content: m.Text,
		})
		for _, tc := range m.Tools {
			out = append(out, eventRow{
				kind:    KindToolCall,
				ts:      fmtTimeOrEmpty(m.Time),
				role:    channel.RoleTool,
				name:    tc.Name,
				content: tc.Input,
			})
			out = append(out, eventRow{
				kind:    KindToolResult,
				ts:      fmtTimeOrEmpty(m.Time),
				role:    channel.RoleTool,
				content: tc.Output,
			})
		}
	}
	for i := range out {
		out[i].seq = i + 1
	}
	lastCall := 0
	for i := range out {
		switch out[i].kind {
		case KindToolCall:
			lastCall = out[i].seq
		case KindToolResult:
			out[i].parentSeq = lastCall
		}
	}
	for i := range out {
		out[i].contentSha = eventFingerprint(out[i])
	}
	return out
}

// eventFingerprint 是一个事件的内容指纹：同一位置上指纹一致才算「没变」。
func eventFingerprint(e eventRow) string {
	h := sha256.New()
	fmt.Fprintf(h, "%s\x1f%s\x1f%d\x1f%s\x1f%s\x1f%s", e.kind, e.ts, e.parentSeq, e.role, e.name, e.content)
	return hex.EncodeToString(h.Sum(nil))
}

// fmtTimeOrEmpty 零值时间写空串，读回来还是零值，往返不失真。
func fmtTimeOrEmpty(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.Format(timeLayout)
}

// sessionIdentity 定位一个会话：渠道 + 来源 + 渠道内标识。
type sessionIdentity struct {
	Channel    string
	SourceRef  string
	SessionKey string
}

// WriteSession 把一个统一会话模型写进 L1（幂等）。
//
// now 只用于 ingested_at；事务内完成「比对 → 追加 / 新 revision → FTS 同步」，
// 半途失败整体回滚，该会话下一轮重收。
func (d *DB) WriteSession(s channel.Session, now time.Time) error {
	if s.Channel == "" || s.ID == "" {
		return errors.New("会话缺渠道或会话标识，拒绝入库")
	}
	return retryBusy(func() error { return d.writeSessionOnce(s, now) })
}

// writeSessionOnce 是一次完整的入库尝试；撞锁时由 WriteSession 重来。
func (d *DB) writeSessionOnce(s channel.Session, now time.Time) error {
	ident := sessionIdentity{Channel: s.Channel, SourceRef: s.SourceFile, SessionKey: s.ID}
	events := expandEvents(s)

	tx, err := d.handle.Begin()
	if err != nil {
		return fmt.Errorf("开入库事务: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	rowID, revision, oldShas, err := d.latestRevision(tx, ident)
	if err != nil {
		return err
	}

	switch {
	case rowID == 0:
		// 新会话：建 revision 1。
		rowID, err = d.insertSession(tx, ident, 1, s, len(events), now)
		if err != nil {
			return err
		}
		if err := d.insertEvents(tx, rowID, events); err != nil {
			return err
		}
	case extends(oldShas, events):
		// 前缀一致：完全相同 → 无事可做；变长 → 只追加尾部。
		if len(events) > len(oldShas) {
			if err := d.insertEvents(tx, rowID, events[len(oldShas):]); err != nil {
				return err
			}
		}
	default:
		// 历史被改写：新开 revision，旧账保留；FTS 只跟最新 revision（旧的那份撤下）。
		if _, err := tx.Exec(`DELETE FROM events_fts WHERE session_id = ?`, rowID); err != nil {
			return fmt.Errorf("撤下旧 revision 的全文索引: %w", err)
		}
		rowID, err = d.insertSession(tx, ident, revision+1, s, len(events), now)
		if err != nil {
			return err
		}
		if err := d.insertEvents(tx, rowID, events); err != nil {
			return err
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("提交入库事务: %w", err)
	}
	return nil
}

// latestRevision 查一个会话最新 revision 的行 ID 与事件指纹（按 seq 排好）。
// 没有时 rowID = 0。
func (d *DB) latestRevision(tx *sql.Tx, ident sessionIdentity) (rowID, revision int64, shas []string, err error) {
	err = tx.QueryRow(`SELECT id, revision FROM sessions
		WHERE channel = ? AND source_ref = ? AND session_key = ?
		ORDER BY revision DESC LIMIT 1`,
		ident.Channel, ident.SourceRef, ident.SessionKey).Scan(&rowID, &revision)
	if err == sql.ErrNoRows {
		return 0, 0, nil, nil
	}
	if err != nil {
		return 0, 0, nil, fmt.Errorf("查会话最新版本: %w", err)
	}
	rows, err := tx.Query(`SELECT content_sha FROM events WHERE session_id = ? ORDER BY seq`, rowID)
	if err != nil {
		return 0, 0, nil, fmt.Errorf("查事件指纹: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var sha string
		if err := rows.Scan(&sha); err != nil {
			return 0, 0, nil, fmt.Errorf("读事件指纹: %w", err)
		}
		shas = append(shas, sha)
	}
	return rowID, revision, shas, rows.Err()
}

// extends 报告 newEvents 是否是 oldShas 的原样延长（前缀逐位一致）。
func extends(oldShas []string, newEvents []eventRow) bool {
	if len(newEvents) < len(oldShas) {
		return false
	}
	for i, sha := range oldShas {
		if newEvents[i].contentSha != sha {
			return false
		}
	}
	return true
}

func (d *DB) insertSession(tx *sql.Tx, ident sessionIdentity, revision int64, s channel.Session, eventCount int, now time.Time) (int64, error) {
	res, err := tx.Exec(`INSERT INTO sessions
		(channel, source_ref, session_key, revision, title, project_path, started_at, message_count, ingested_at)
		VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		ident.Channel, ident.SourceRef, ident.SessionKey, revision,
		s.Title, s.ProjectPath, fmtTimeOrEmpty(s.StartedAt), eventCount, fmtTime(now))
	if err != nil {
		return 0, fmt.Errorf("写会话行: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("取会话行 ID: %w", err)
	}
	return id, nil
}

// insertEvents 批量写事件行，并给有正文的条目同步全文索引。
func (d *DB) insertEvents(tx *sql.Tx, sessionID int64, events []eventRow) error {
	for _, e := range events {
		var parent any
		if e.parentSeq != 0 {
			parent = e.parentSeq
		}
		if _, err := tx.Exec(`INSERT INTO events
			(session_id, seq, kind, ts, parent_seq, role, name, content, content_sha)
			VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			sessionID, e.seq, e.kind, e.ts, parent, e.role, e.name, e.content, e.contentSha); err != nil {
			return fmt.Errorf("写事件 seq=%d: %w", e.seq, err)
		}
		if e.content != "" {
			if _, err := tx.Exec(`INSERT INTO events_fts(content, session_id, seq) VALUES(?, ?, ?)`,
				ftsSegment(e.content), sessionID, e.seq); err != nil {
				return fmt.Errorf("写全文索引 seq=%d: %w", e.seq, err)
			}
		}
	}
	return nil
}

// ReadSession 从 L1 回组一个统一会话模型（最新 revision）。
//
// 找不到返回 ok = false；回组结果与当初写入的模型逐字段一致——这是
// I-085「导出等价切换」的地基，l1_test 里有逐字段对比钉着。
func (d *DB) ReadSession(channelID, sourceRef, sessionKey string) (channel.Session, bool, error) {
	var (
		rowID, revision               int64
		title, projectPath, startedAt string
	)
	err := d.handle.QueryRow(`SELECT id, revision, title, project_path, started_at FROM sessions
		WHERE channel = ? AND source_ref = ? AND session_key = ?
		ORDER BY revision DESC LIMIT 1`,
		channelID, sourceRef, sessionKey).
		Scan(&rowID, &revision, &title, &projectPath, &startedAt)
	if err == sql.ErrNoRows {
		return channel.Session{}, false, nil
	}
	if err != nil {
		return channel.Session{}, false, fmt.Errorf("查会话: %w", err)
	}

	rows, err := d.handle.Query(`SELECT seq, kind, ts, parent_seq, role, name, content FROM events
		WHERE session_id = ? ORDER BY seq`, rowID)
	if err != nil {
		return channel.Session{}, false, fmt.Errorf("读事件: %w", err)
	}
	defer rows.Close()

	out := channel.Session{
		Channel:     channelID,
		ID:          sessionKey,
		Title:       title,
		ProjectPath: projectPath,
		StartedAt:   parseTime(startedAt),
		SourceFile:  sourceRef,
	}
	// toolIndex 记「tool_call 的 seq → 本消息里第几个工具」，tool_result 按它配对。
	toolIndex := map[int]int{}
	for rows.Next() {
		var (
			kind, ts, role, name, content string
			seq                           int
		)
		var parent sql.NullInt64
		if err := rows.Scan(&seq, &kind, &ts, &parent, &role, &name, &content); err != nil {
			return channel.Session{}, false, fmt.Errorf("读事件行: %w", err)
		}
		switch kind {
		case KindMessage:
			out.Messages = append(out.Messages, channel.Message{
				Role: role, Time: parseTime(ts), Text: content,
			})
			toolIndex = map[int]int{}
		case KindToolCall:
			if len(out.Messages) == 0 {
				continue // 没有宿主消息的工具事件不成话，防御性跳过
			}
			last := &out.Messages[len(out.Messages)-1]
			last.Tools = append(last.Tools, channel.ToolCall{Name: name, Input: content})
			toolIndex[seq] = len(last.Tools) - 1
		case KindToolResult:
			if len(out.Messages) == 0 {
				continue
			}
			last := &out.Messages[len(out.Messages)-1]
			if idx, ok := toolIndex[int(parent.Int64)]; ok && idx < len(last.Tools) {
				last.Tools[idx].Output = content
			}
		}
	}
	if err := rows.Err(); err != nil {
		return channel.Session{}, false, fmt.Errorf("遍历事件: %w", err)
	}
	return out, true, nil
}

// SessionRef 是「主库里现有一个会话」的定位与摘要，供导出与维度提取遍历用。
type SessionRef struct {
	// RowID 是最新 revision 的行 ID。
	RowID int64
	// Channel / SourceRef / SessionKey 定位这个会话。
	Channel    string
	SourceRef  string
	SessionKey string
	// Title / ProjectPath / StartedAt 是会话头的当前值。
	Title       string
	ProjectPath string
	StartedAt   time.Time
	// IngestedAt 是这个 revision 入库的时间。
	IngestedAt time.Time
}

// LatestSessions 列出所有会话的最新 revision，按渠道与来源排好。
func (d *DB) LatestSessions() ([]SessionRef, error) {
	rows, err := d.handle.Query(`SELECT s.id, s.channel, s.source_ref, s.session_key, s.title, s.project_path, s.started_at, s.ingested_at
		FROM sessions s
		JOIN (SELECT channel, source_ref, session_key, MAX(revision) AS revision
			FROM sessions GROUP BY channel, source_ref, session_key) latest
		ON s.channel = latest.channel AND s.source_ref = latest.source_ref
			AND s.session_key = latest.session_key AND s.revision = latest.revision
		ORDER BY s.channel, s.source_ref, s.session_key`)
	if err != nil {
		return nil, fmt.Errorf("列会话: %w", err)
	}
	defer rows.Close()

	var out []SessionRef
	for rows.Next() {
		var ref SessionRef
		var startedAt, ingestedAt string
		if err := rows.Scan(&ref.RowID, &ref.Channel, &ref.SourceRef, &ref.SessionKey,
			&ref.Title, &ref.ProjectPath, &startedAt, &ingestedAt); err != nil {
			return nil, fmt.Errorf("读会话行: %w", err)
		}
		ref.StartedAt = parseTime(startedAt)
		ref.IngestedAt = parseTime(ingestedAt)
		out = append(out, ref)
	}
	return out, rows.Err()
}

// SessionEvents 读一个会话（行 ID）的全部事件，按 seq 排好。切片导出（F14）用。
type Event struct {
	// Seq 是会话内序号。
	Seq int
	// Kind / Ts / ParentSeq / Role / Name / Content 是事件字段。
	Kind      string
	Ts        time.Time
	ParentSeq int
	Role      string
	Name      string
	Content   string
}

// Events 读回一个会话的全部事件。
func (d *DB) Events(sessionID int64) ([]Event, error) {
	rows, err := d.handle.Query(`SELECT seq, kind, ts, parent_seq, role, name, content FROM events
		WHERE session_id = ? ORDER BY seq`, sessionID)
	if err != nil {
		return nil, fmt.Errorf("读事件: %w", err)
	}
	defer rows.Close()

	var out []Event
	for rows.Next() {
		var e Event
		var ts string
		var parent sql.NullInt64
		if err := rows.Scan(&e.Seq, &e.Kind, &ts, &parent, &e.Role, &e.Name, &e.Content); err != nil {
			return nil, fmt.Errorf("读事件行: %w", err)
		}
		e.Ts = parseTime(ts)
		e.ParentSeq = int(parent.Int64)
		out = append(out, e)
	}
	return out, rows.Err()
}
