package store

// 全文检索（L2b）：FTS5 索引的查询与全量重刷。
//
// 增量维护在 l1.go 的 insertEvents（写入即入索引）与 WriteSession 的
// revision 切换（旧 revision 撤下）；本文件补两件事：
//
//   - Search：关键词 → 命中会话与消息片段。查询词与索引走同一个
//     ftsSegment 切词（中文单字），并对每个词加引号，防 FTS5 语法注入。
//   - RebuildFTS：把索引清掉重刷（AC-15：派生可弃）。

import (
	"fmt"
	"strings"
	"time"
	"unicode/utf8"
)

// 搜索一次多取几倍候选，去重（同一会话只留一条）之后还能凑满一屏。
const (
	searchOverFetch = 6
	searchMaxFetch  = 300
)

// SearchHit 是一条检索命中：哪个会话的哪一行命中了，附上下文片段。
type SearchHit struct {
	// Channel / SourceRef / SessionKey 定位会话。
	Channel    string
	SourceRef  string
	SessionKey string
	// Title / ProjectPath 是会话头当前值。
	Title       string
	ProjectPath string
	// Seq 是命中事件的会话内序号；Kind / Ts 是事件的种类与时间。
	Seq  int
	Kind string
	Ts   time.Time
	// Snippet 是命中片段，命中词前后各留几个词，省得前端截。
	Snippet string
	// Content 是命中事件的完整正文（切片导出用；界面只消费 Snippet）。
	Content string
}

// SearchFilter 是检索的附加过滤（I-084 A2）：渠道精确匹配、项目前缀匹配、
// 时间按会话开始时间不早于某刻。零值字段都表示不过滤。
type SearchFilter struct {
	Channel string
	Project string
	Since   time.Time
}

// Search 按关键词检索消息与工具内容，最多返回 limit 条（limit ≤ 0 用默认 50）。
//
// 命中只来自最新 revision 的会话；查询词为空返回空结果，不报错。
func (d *DB) Search(query string, filter SearchFilter, limit int) ([]SearchHit, error) {
	match := ftsQuery(query)
	if match == "" {
		return nil, nil
	}
	if limit <= 0 {
		limit = 50
	}
	// 同一会话只列一条：多留几倍候选，去重之后仍能凑满一屏。
	fetch := limit * searchOverFetch
	if fetch > searchMaxFetch {
		fetch = searchMaxFetch
	}
	terms := strings.Fields(query)

	conds := []string{"events_fts MATCH ?"}
	args := []any{match}
	if filter.Channel != "" {
		conds = append(conds, "s.channel = ?")
		args = append(args, filter.Channel)
	}
	if filter.Project != "" {
		conds = append(conds, "s.project_path LIKE ?")
		args = append(args, filter.Project+"%")
	}
	if !filter.Since.IsZero() {
		conds = append(conds, "s.started_at != '' AND s.started_at >= ?")
		args = append(args, fmtTime(filter.Since))
	}
	args = append(args, fetch)

	rows, err := d.handle.Query(`SELECT s.channel, s.source_ref, s.session_key, s.title, s.project_path,
			e.seq, e.kind, e.ts, e.content
		FROM events_fts
		JOIN events e ON e.session_id = events_fts.session_id AND e.seq = events_fts.seq
		JOIN sessions s ON s.id = events_fts.session_id
		JOIN (SELECT channel, source_ref, session_key, MAX(revision) AS revision
			FROM sessions GROUP BY channel, source_ref, session_key) latest
		ON s.channel = latest.channel AND s.source_ref = latest.source_ref
			AND s.session_key = latest.session_key AND s.revision = latest.revision
		WHERE `+strings.Join(conds, " AND ")+`
		ORDER BY rank
		LIMIT ?`, args...)
	if err != nil {
		return nil, fmt.Errorf("全文检索: %w", err)
	}
	defer rows.Close()

	out := []SearchHit{}
	seen := map[string]struct{}{}
	for rows.Next() {
		var h SearchHit
		var ts string
		if err := rows.Scan(&h.Channel, &h.SourceRef, &h.SessionKey, &h.Title, &h.ProjectPath,
			&h.Seq, &h.Kind, &ts, &h.Content); err != nil {
			return nil, err
		}
		// 一个会话里命中多处、或者同一段对话在 L1 里有多份（Codex 续写另开
		// rollout、OpenCode v1/v2 双表），对搜索结果来说都是同一条：只留排名最前的那条。
		key := h.Channel + "" + h.SessionKey
		if _, dup := seen[key]; dup {
			continue
		}
		seen[key] = struct{}{}
		h.Ts = parseTime(ts)
		h.Snippet = buildSnippet(h.Content, terms)
		out = append(out, h)
		if len(out) == limit {
			break
		}
	}
	return out, rows.Err()
}

// buildSnippet 从原始正文里截命中片段：用**原样输入的词**定位（中文就是整段字），
// 命中前后各留一截上下文；能在一行内截断就在换行处断，别把一句话掐成半截。
func buildSnippet(content string, terms []string) string {
	const (
		before = 40
		after  = 80
	)
	pos, length := locateTerm(strings.ToLower(content), terms)
	if pos < 0 {
		// 理论上不该发生（FTS 命中了原文却定位不到）；退化为开头一截。
		pos, length = 0, nextBoundary(content, after)
	}
	start := pos - before
	if start < 0 {
		start = 0
	}
	end := pos + length + after
	if end > len(content) {
		end = len(content)
	}
	// 换行处断开：片段从命中所在那一行的行首开始、到行尾结束。
	if nl := strings.Index(content[start:pos], "\n"); nl >= 0 {
		start += nl + 1
	}
	if nl := strings.Index(content[pos:end], "\n"); nl >= 0 {
		end = pos + nl
	}
	// 按字节截断会切到多字节字符中间，中文就变成「…��条」这种乱码（I-084 验收反馈）。
	start, end = snapRunes(content, start, end)
	out := strings.Join(strings.Fields(content[start:end]), " ")
	if start > 0 {
		out = "…" + out
	}
	if end < len(content) {
		out += "…"
	}
	return out
}

// nextBoundary 取第 n 个字符结束处的字节位置，给「定位不到词」的兜底用。
func nextBoundary(s string, n int) int {
	count := 0
	for i := range s {
		if count == n {
			return i
		}
		count++
	}
	return len(s)
}

// snapRunes 把字节区间挪到字符边界上，避免切坏多字节字符。
func snapRunes(s string, start, end int) (int, int) {
	for start > 0 && start < len(s) && !utf8.RuneStart(s[start]) {
		start--
	}
	for end > 0 && end < len(s) && !utf8.RuneStart(s[end]) {
		end--
	}
	return start, end
}

// locateTerm 在（已转小写的）正文里找最早出现的原词，返回字节位置与长度。
func locateTerm(lower string, terms []string) (int, int) {
	best, bestLen := -1, 0
	for _, term := range terms {
		idx := strings.Index(lower, strings.ToLower(term))
		if idx < 0 {
			continue
		}
		if best < 0 || idx < best {
			best, bestLen = idx, len(term)
		}
	}
	return best, bestLen
}

// RebuildFTS 把全文索引清掉、按最新 revision 重刷一遍（AC-15）。
//
// 与增量路径写同一批行；大库上这是一次全量读，批量在一个事务里做。
func (d *DB) RebuildFTS() error {
	sessions, err := d.LatestSessions()
	if err != nil {
		return err
	}
	tx, err := d.handle.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.Exec(`DELETE FROM events_fts`); err != nil {
		return err
	}
	stmt, err := tx.Prepare(`SELECT seq, kind, content FROM events WHERE session_id = ? AND content != '' ORDER BY seq`)
	if err != nil {
		return err
	}
	defer stmt.Close()
	insert, err := tx.Prepare(`INSERT INTO events_fts(content, session_id, seq) VALUES(?, ?, ?)`)
	if err != nil {
		return err
	}
	defer insert.Close()

	for _, s := range sessions {
		rows, err := stmt.Query(s.RowID)
		if err != nil {
			return err
		}
		for rows.Next() {
			var seq int
			var kind, content string
			if err := rows.Scan(&seq, &kind, &content); err != nil {
				rows.Close()
				return err
			}
			if _, err := insert.Exec(ftsSegment(content), s.RowID, seq); err != nil {
				rows.Close()
				return err
			}
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return err
		}
		rows.Close()
	}
	return tx.Commit()
}

// ftsQuery 把用户输入整理成 FTS5 的 MATCH 词法。
//
// 中文按「相邻单字组成短语」：索引时每个汉字两侧都加了空格，「记忆库」在索引里
// 是三个相邻的词，所以 `"记 忆 库"` 正好是它的精确短语。早先每个字单独加引号再用
// AND 连起来，任意位置出现这三个字的文档都会命中，结果又杂又看不出为什么相关
// （I-084 验收反馈）。西文仍按整词匹配，多个词之间是 AND。
func ftsQuery(q string) string {
	var parts []string
	for _, word := range strings.Fields(q) {
		for _, run := range splitScript(word) {
			raw := strings.ReplaceAll(run.text, `"`, "")
			if raw == "" {
				continue
			}
			if !run.cjk {
				parts = append(parts, `"`+raw+`"`)
				continue
			}
			chars := strings.Fields(ftsSegment(raw))
			if len(chars) == 0 {
				continue
			}
			parts = append(parts, `"`+strings.Join(chars, " ")+`"`)
		}
	}
	return strings.Join(parts, " ")
}

// scriptRun 是输入里一段同文种的文字（连着的中文 / 连着的西文）。
type scriptRun struct {
	text string
	cjk  bool
}

// splitScript 按汉字 / 非汉字把一段文字切开，保持原有顺序。
func splitScript(s string) []scriptRun {
	var (
		out     []scriptRun
		b       strings.Builder
		cur     bool
		started bool
	)
	flush := func() {
		if b.Len() == 0 {
			return
		}
		out = append(out, scriptRun{text: b.String(), cjk: cur})
		b.Reset()
	}
	for _, r := range s {
		isCJK := isCJK(r)
		if started && isCJK != cur {
			flush()
		}
		cur = isCJK
		started = true
		b.WriteRune(r)
	}
	flush()
	return out
}
