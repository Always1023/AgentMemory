package store

// 组合查询（F17）：按「过滤 + 分组 + 度量」聚合 L1，不落地。
//
// 安全边界（AC-19）：
//   - 列名、聚合表达式只来自本文件的白名单，外部参数一律走占位符绑定；
//   - 行数有上限（默认 100 / 最多 1000）、执行有超时（5 秒）。
//
// 这里只读：不写库、不改水位、不碰渠道目录。

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"
)

// QueryFilter 是组合查询的过滤条件；零值字段表示不过滤。
type QueryFilter struct {
	// Channels 为真时只算这些渠道。
	Channels []string `json:"channel"`
	// ProjectPrefix 是项目路径前缀。
	ProjectPrefix string `json:"project_prefix"`
	// Since / Until 限定会话开始时间（含边界）。
	Since time.Time `json:"since"`
	Until time.Time `json:"until"`
	// Kinds 限定事件类型（message / tool_call / tool_result）。
	Kinds []string `json:"kind"`
	// Roles 限定角色（user / assistant / tool）。
	Roles []string `json:"role"`
	// Tools 限定工具名（只对工具事件有意义）。
	Tools []string `json:"tool"`
	// Keyword 走全文索引：只算命中的事件。
	Keyword string `json:"query"`
}

// QueryRequest 是一次组合查询。
type QueryRequest struct {
	Filter   QueryFilter `json:"filter"`
	GroupBy  []string    `json:"group_by"`
	Measures []string    `json:"measures"`
	Limit    int         `json:"limit"`
	Offset   int         `json:"offset"`
}

// QueryRow 是一行聚合结果：分组键 + 度量值。
type QueryRow struct {
	Keys   map[string]string `json:"keys"`
	Values map[string]int64  `json:"values"`
}

// QueryResult 是组合查询的返回。
type QueryResult struct {
	Rows   []QueryRow `json:"rows"`
	Total  int        `json:"total_rows"`
	Limit  int        `json:"limit"`
	Offset int        `json:"offset"`
	// Limited 为真表示还有更多行没返回。
	Limited bool `json:"limited"`
}

const (
	queryDefaultLimit = 100
	queryMaxLimit     = 1000
	queryTimeout      = 5 * time.Second
)

// 分组键 → SQL 表达式。列名只在白名单里出现（AC-19）。
var queryGroupExpr = map[string]string{
	"channel": "s.channel",
	"project": "s.project_path",
	"day":     "substr(s.started_at, 1, 10)",
	"week":    "date(s.started_at, 'weekday 0', '-6 days')",
	"kind":    "e.kind",
	"tool":    "e.name",
}

// 度量名 → 聚合表达式。
var queryMeasureExpr = map[string]string{
	"sessions": "COUNT(DISTINCT s.id)",
	"events":   "COUNT(*)",
	"chars":    "COALESCE(SUM(LENGTH(e.content)), 0)",
}

// Query 执行一次组合查询。
func (d *DB) Query(ctx context.Context, req QueryRequest) (QueryResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithTimeout(ctx, queryTimeout)
	defer cancel()

	groupBy, groupExprs, err := resolveGroupBy(req.GroupBy)
	if err != nil {
		return QueryResult{}, err
	}
	measures, measureExprs, err := resolveMeasures(req.Measures)
	if err != nil {
		return QueryResult{}, err
	}

	limit := req.Limit
	if limit <= 0 {
		limit = queryDefaultLimit
	}
	if limit > queryMaxLimit {
		limit = queryMaxLimit
	}
	offset := req.Offset
	if offset < 0 {
		offset = 0
	}

	where, args := buildQueryWhere(req.Filter)

	selects := append([]string{}, groupExprs...)
	selects = append(selects, measureExprs...)

	var b strings.Builder
	b.WriteString("SELECT ")
	b.WriteString(strings.Join(selects, ", "))
	b.WriteString(" FROM events e JOIN sessions s ON s.id = e.session_id")
	// 只算每个会话的最新 revision。
	b.WriteString(` JOIN (SELECT channel, source_ref, session_key, MAX(revision) AS revision
		FROM sessions GROUP BY channel, source_ref, session_key) latest
		ON s.channel = latest.channel AND s.source_ref = latest.source_ref
		AND s.session_key = latest.session_key AND s.revision = latest.revision`)
	if strings.TrimSpace(req.Filter.Keyword) != "" {
		b.WriteString(" JOIN events_fts f ON f.session_id = e.session_id AND f.seq = e.seq")
		where = append([]string{"events_fts MATCH ?"}, where...)
		args = append([]any{ftsQuery(req.Filter.Keyword)}, args...)
	}
	if len(where) > 0 {
		b.WriteString(" WHERE ")
		b.WriteString(strings.Join(where, " AND "))
	}
	if len(groupExprs) > 0 {
		orders := make([]string, len(groupExprs))
		for i := range groupExprs {
			orders[i] = fmt.Sprintf("%d", i+1)
		}
		b.WriteString(" GROUP BY ")
		b.WriteString(strings.Join(orders, ", "))
		b.WriteString(" ORDER BY ")
		b.WriteString(strings.Join(orders, ", "))
	}
	// 多取一行用来判断还有没有更多。
	b.WriteString(" LIMIT ? OFFSET ?")
	args = append(args, limit+1, offset)

	rows, err := d.handle.QueryContext(ctx, b.String(), args...)
	if err != nil {
		return QueryResult{}, fmt.Errorf("组合查询: %w", err)
	}
	defer rows.Close()

	out := QueryResult{Limit: limit, Offset: offset, Rows: []QueryRow{}}
	for rows.Next() {
		keys := make(map[string]string, len(groupBy))
		values := make(map[string]int64, len(measures))
		dest := make([]any, 0, len(groupBy)+len(measures))
		keyVals := make([]string, len(groupBy))
		measureVals := make([]int64, len(measures))
		for i := range keyVals {
			dest = append(dest, &keyVals[i])
		}
		for i := range measureVals {
			dest = append(dest, &measureVals[i])
		}
		if err := rows.Scan(dest...); err != nil {
			return QueryResult{}, fmt.Errorf("读查询结果: %w", err)
		}
		for i, name := range groupBy {
			keys[name] = keyVals[i]
		}
		for i, name := range measures {
			values[name] = measureVals[i]
		}
		if len(out.Rows) == limit {
			out.Limited = true
			break
		}
		out.Rows = append(out.Rows, QueryRow{Keys: keys, Values: values})
	}
	if err := rows.Err(); err != nil {
		return QueryResult{}, fmt.Errorf("遍历查询结果: %w", err)
	}
	out.Total = len(out.Rows)
	return out, nil
}

// resolveGroupBy 校验分组键并返回顺序稳定的表达式。
func resolveGroupBy(names []string) ([]string, []string, error) {
	seen := map[string]bool{}
	outNames := make([]string, 0, len(names))
	outExprs := make([]string, 0, len(names))
	for _, name := range names {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		expr, ok := queryGroupExpr[name]
		if !ok {
			return nil, nil, fmt.Errorf("不支持的 group_by：%q（可用：%s）", name, sortedKeys(queryGroupExpr))
		}
		if seen[name] {
			return nil, nil, fmt.Errorf("group_by 重复：%q", name)
		}
		seen[name] = true
		outNames = append(outNames, name)
		outExprs = append(outExprs, expr)
	}
	return outNames, outExprs, nil
}

// resolveMeasures 校验度量名；不传时默认「会话数 + 事件数」。
func resolveMeasures(names []string) ([]string, []string, error) {
	if len(names) == 0 {
		names = []string{"sessions", "events"}
	}
	seen := map[string]bool{}
	outNames := make([]string, 0, len(names))
	outExprs := make([]string, 0, len(names))
	for _, name := range names {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		expr, ok := queryMeasureExpr[name]
		if !ok {
			return nil, nil, fmt.Errorf("不支持的 measures：%q（可用：%s）", name, sortedKeys(queryMeasureExpr))
		}
		if seen[name] {
			return nil, nil, fmt.Errorf("measures 重复：%q", name)
		}
		seen[name] = true
		outNames = append(outNames, name)
		outExprs = append(outExprs, expr)
	}
	if len(outNames) == 0 {
		outNames = []string{"sessions", "events"}
		outExprs = []string{queryMeasureExpr["sessions"], queryMeasureExpr["events"]}
	}
	return outNames, outExprs, nil
}

// buildQueryWhere 拼过滤条件；值一律进 args（AC-19）。
func buildQueryWhere(f QueryFilter) ([]string, []any) {
	var where []string
	var args []any
	if len(f.Channels) > 0 {
		where = append(where, "s.channel IN ("+placeholders(len(f.Channels))+")")
		for _, v := range f.Channels {
			args = append(args, v)
		}
	}
	if p := strings.TrimSpace(f.ProjectPrefix); p != "" {
		where = append(where, "s.project_path LIKE ?")
		args = append(args, p+"%")
	}
	if !f.Since.IsZero() {
		where = append(where, "s.started_at >= ?")
		args = append(args, fmtTime(f.Since))
	}
	if !f.Until.IsZero() {
		where = append(where, "s.started_at <= ?")
		args = append(args, fmtTime(f.Until))
	}
	if len(f.Kinds) > 0 {
		where = append(where, "e.kind IN ("+placeholders(len(f.Kinds))+")")
		for _, v := range f.Kinds {
			args = append(args, v)
		}
	}
	if len(f.Roles) > 0 {
		where = append(where, "e.role IN ("+placeholders(len(f.Roles))+")")
		for _, v := range f.Roles {
			args = append(args, v)
		}
	}
	if len(f.Tools) > 0 {
		where = append(where, "e.name IN ("+placeholders(len(f.Tools))+")")
		for _, v := range f.Tools {
			args = append(args, v)
		}
	}
	return where, args
}

func placeholders(n int) string {
	parts := make([]string, n)
	for i := range parts {
		parts[i] = "?"
	}
	return strings.Join(parts, ", ")
}

func sortedKeys(m map[string]string) string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return strings.Join(keys, " / ")
}
