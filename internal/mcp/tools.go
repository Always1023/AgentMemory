package mcp

// 五个只读工具（F15）：检索 / 读会话 / 读维度 / 导出切片 / 统计。
//
// 每个工具都自带上限：检索最多 50 条、会话一次最多 200 条事件、正文截断到
// 2000 字符、切片最多 500 行。上限是给「一次往返」定的，不给人翻页用——
// 要更多就再调一次，带 offset / limit。

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Always1023/AgentMemory/internal/store"
)

const (
	searchDefaultLimit = 20
	searchMaxLimit     = 50
	sessionMaxEvents   = 200
	contentMaxRunes    = 2000
	exportMaxLimit     = 500
)

// toolDef 是 tools/list 里的一项。
type toolDef struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
}

func obj(props map[string]any, required ...string) map[string]any {
	out := map[string]any{"type": "object", "properties": props}
	if len(required) > 0 {
		out["required"] = required
	}
	return out
}

func str(desc string) map[string]any {
	return map[string]any{"type": "string", "description": desc}
}

func num(desc string) map[string]any {
	return map[string]any{"type": "integer", "description": desc}
}

// toolDefs 返回工具清单；说明写给人看，也写进客户端的工具列表。
func toolDefs() []toolDef {
	return []toolDef{
		{
			Name:        "memory_search",
			Description: "按关键词检索本机记忆库（会话正文与工具内容）。中文按整词短语匹配，同一会话只返回一条。",
			InputSchema: obj(map[string]any{
				"query":      str("关键词；中文整词更准"),
				"channel":    str("只搜这个渠道（可选，值为渠道 ID）"),
				"project":    str("项目路径前缀（可选）"),
				"since_days": num("只看最近多少天（可选，0 或省略表示不限）"),
				"limit":      num("最多返回几条，默认 20，上限 50"),
			}, "query"),
		},
		{
			Name:        "memory_get_session",
			Description: "读一个会话的事件流（分页）。正文按 2000 字截断；offset 从 0 开始。",
			InputSchema: obj(map[string]any{
				"channel":     str("渠道 ID"),
				"source_ref":  str("来源（会话文件路径或虚拟路径）"),
				"session_key": str("渠道内会话标识"),
				"offset":      num("从第几条事件开始，默认 0"),
				"limit":       num("最多返回几条，默认 100，上限 200"),
			}, "channel", "source_ref", "session_key"),
		},
		{
			Name:        "memory_facets",
			Description: "读维度 / 视角。不传 name 时列出库里已有的维度名；传了就返回该维度的全部条目。",
			InputSchema: obj(map[string]any{
				"name": str("维度名，如 by_channel / by_project / session_stats（可选）"),
			}),
		},
		{
			Name:        "memory_export_ndjson",
			Description: "把检索结果导出成 NDJSON 切片，返回落点与条数。落点固定在归档目录的「切片」子目录。",
			InputSchema: obj(map[string]any{
				"query": str("关键词"),
				"limit": num("最多导出几行，默认 200，上限 500"),
			}, "query"),
		},
		{
			Name:        "memory_stats",
			Description: "主库概况：会话 / 事件总数、每个渠道的会话数、最近入库时间。",
			InputSchema: obj(map[string]any{}),
		},
	}
}

// callTool 执行一个工具；failed 为真时把 text 原样交给客户端当错误说明。
func (s *Server) callTool(name string, raw json.RawMessage) (string, bool) {
	started := time.Now()
	text, failed := s.dispatch(name, raw)
	s.log().Info("MCP 工具调用",
		"工具", name,
		"失败", failed,
		"耗时", time.Since(started).Round(time.Millisecond),
		"返回字节", len(text),
	)
	return text, failed
}

func (s *Server) dispatch(name string, raw json.RawMessage) (string, bool) {
	switch name {
	case "memory_search":
		return s.search(raw)
	case "memory_get_session":
		return s.getSession(raw)
	case "memory_facets":
		return s.facets(raw)
	case "memory_export_ndjson":
		return s.exportNDJSON(raw)
	case "memory_stats":
		return s.stats()
	default:
		return "没有这个工具：" + name, true
	}
}

type searchArgs struct {
	Query     string `json:"query"`
	Channel   string `json:"channel"`
	Project   string `json:"project"`
	SinceDays int    `json:"since_days"`
	Limit     int    `json:"limit"`
}

type hitOut struct {
	Channel    string `json:"channel"`
	Chapter    string `json:"session_title"`
	SourceRef  string `json:"source_ref"`
	SessionKey string `json:"session_key"`
	Kind       string `json:"kind"`
	When       string `json:"when"`
	Snippet    string `json:"snippet"`
}

func (s *Server) search(raw json.RawMessage) (string, bool) {
	var args searchArgs
	if err := json.Unmarshal(raw, &args); err != nil {
		return "参数看不懂：需要 query、可选的 channel / project / since_days / limit", true
	}
	args.Query = strings.TrimSpace(args.Query)
	if args.Query == "" {
		return "query 不能为空", true
	}
	limit := args.Limit
	if limit <= 0 {
		limit = searchDefaultLimit
	}
	if limit > searchMaxLimit {
		limit = searchMaxLimit
	}
	filter := store.SearchFilter{Channel: args.Channel, Project: args.Project}
	if args.SinceDays > 0 {
		filter.Since = time.Now().AddDate(0, 0, -args.SinceDays)
	}
	hits, err := s.DB.Search(args.Query, filter, limit)
	if err != nil {
		return "检索没做成：" + err.Error(), true
	}
	out := make([]hitOut, 0, len(hits))
	for _, h := range hits {
		out = append(out, hitOut{
			Channel: h.Channel, Chapter: h.Title, SourceRef: h.SourceRef, SessionKey: h.SessionKey,
			Kind: h.Kind, When: whenText(h.Ts), Snippet: h.Snippet,
		})
	}
	return mustJSON(map[string]any{"count": len(out), "hits": out})
}

type sessionArgs struct {
	Channel    string `json:"channel"`
	SourceRef  string `json:"source_ref"`
	SessionKey string `json:"session_key"`
	Offset     int    `json:"offset"`
	Limit      int    `json:"limit"`
}

type eventOut struct {
	Seq     int    `json:"seq"`
	Kind    string `json:"kind"`
	When    string `json:"when"`
	Role    string `json:"role,omitempty"`
	Name    string `json:"name,omitempty"`
	Content string `json:"content"`
	Cut     bool   `json:"truncated,omitempty"`
}

func (s *Server) getSession(raw json.RawMessage) (string, bool) {
	var args sessionArgs
	if err := json.Unmarshal(raw, &args); err != nil {
		return "参数看不懂：需要 channel、source_ref、session_key", true
	}
	if args.Channel == "" || args.SourceRef == "" || args.SessionKey == "" {
		return "channel / source_ref / session_key 都不能为空", true
	}
	refs, err := s.DB.LatestSessions()
	if err != nil {
		return "读会话目录失败：" + err.Error(), true
	}
	var found *store.SessionRef
	for i := range refs {
		r := refs[i]
		if r.Channel == args.Channel && r.SourceRef == args.SourceRef && r.SessionKey == args.SessionKey {
			found = &refs[i]
			break
		}
	}
	if found == nil {
		return "没找到这个会话（channel / source_ref / session_key 要对得上）", true
	}
	events, err := s.DB.Events(found.RowID)
	if err != nil {
		return "读事件失败：" + err.Error(), true
	}
	offset := args.Offset
	if offset < 0 {
		offset = 0
	}
	limit := args.Limit
	if limit <= 0 {
		limit = 100
	}
	if limit > sessionMaxEvents {
		limit = sessionMaxEvents
	}
	out := make([]eventOut, 0, limit)
	for i := offset; i < len(events) && len(out) < limit; i++ {
		e := events[i]
		content := e.Content
		cut := false
		if r := []rune(content); len(r) > contentMaxRunes {
			content = string(r[:contentMaxRunes])
			cut = true
		}
		out = append(out, eventOut{
			Seq: e.Seq, Kind: e.Kind, When: whenText(e.Ts), Role: e.Role, Name: e.Name,
			Content: content, Cut: cut,
		})
	}
	return mustJSON(map[string]any{
		"channel": args.Channel, "source_ref": args.SourceRef, "session_key": args.SessionKey,
		"title": found.Title, "project_path": found.ProjectPath,
		"total_events": len(events), "offset": offset, "count": len(out),
		"has_more": offset+len(out) < len(events), "events": out,
	})
}

type facetsArgs struct {
	Name string `json:"name"`
}

func (s *Server) facets(raw json.RawMessage) (string, bool) {
	var args facetsArgs
	_ = json.Unmarshal(raw, &args)
	if strings.TrimSpace(args.Name) == "" {
		names, err := s.DB.DimensionNames()
		if err != nil {
			return "读维度名失败：" + err.Error(), true
		}
		return mustJSON(map[string]any{"names": names})
	}
	rows, err := s.DB.Dimension(args.Name)
	if err != nil {
		return "读维度失败：" + err.Error(), true
	}
	return mustJSON(map[string]any{"name": args.Name, "count": len(rows), "rows": rows})
}

type exportArgs struct {
	Query string `json:"query"`
	Limit int    `json:"limit"`
}

func (s *Server) exportNDJSON(raw json.RawMessage) (string, bool) {
	var args exportArgs
	if err := json.Unmarshal(raw, &args); err != nil || strings.TrimSpace(args.Query) == "" {
		return "参数看不懂：需要 query（可选 limit）", true
	}
	if strings.TrimSpace(s.ArchiveDir) == "" {
		return "归档目录还没配置，切片没处落", true
	}
	limit := args.Limit
	if limit <= 0 {
		limit = 200
	}
	if limit > exportMaxLimit {
		limit = exportMaxLimit
	}
	dir := filepath.Join(s.ArchiveDir, "切片")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "建切片目录失败：" + err.Error(), true
	}
	path := filepath.Join(dir, fmt.Sprintf("搜索-%s.ndjson", time.Now().Format("20060102-150405")))
	f, err := os.Create(path)
	if err != nil {
		return "建切片文件失败：" + err.Error(), true
	}
	n, err := s.DB.WriteSearchNDJSON(f, strings.TrimSpace(args.Query), limit)
	closeErr := f.Close()
	if err != nil {
		return "写切片失败：" + err.Error(), true
	}
	if closeErr != nil {
		return "写切片失败：" + closeErr.Error(), true
	}
	return mustJSON(map[string]any{"count": n, "path": path})
}

func (s *Server) stats() (string, bool) {
	st, err := s.DB.Stats()
	if err != nil {
		return "读主库概况失败：" + err.Error(), true
	}
	return mustJSON(st)
}

// whenText 把事件时间写成人和模型都读得懂的本地时间。
func whenText(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.In(time.Local).Format("2006-01-02 15:04")
}

// mustJSON 把结果编码成一行 JSON；编码失败只可能是内部错误，按失败返回。
func mustJSON(v any) (string, bool) {
	raw, err := json.Marshal(v)
	if err != nil {
		return "结果编码失败：" + err.Error(), true
	}
	return string(raw), false
}
