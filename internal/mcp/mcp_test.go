package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Always1023/AgentMemory/internal/channel"
	"github.com/Always1023/AgentMemory/internal/store"
	"github.com/Always1023/AgentMemory/internal/store/storetest"
)

// newServer 造一个带一份会话的服务端。
func newServer(t *testing.T) (*Server, *store.DB, string) {
	t.Helper()
	db := storetest.Open(t)
	at := time.Date(2026, 10, 27, 9, 30, 0, 0, time.UTC)
	s := channel.Session{
		Channel: "claude-code", ID: "s1", Title: "搜索与记忆库",
		ProjectPath: `D:\work\demo`, StartedAt: at, SourceFile: `C:/fake/s1.jsonl`,
		Messages: []channel.Message{
			{Role: channel.RoleUser, Time: at, Text: "记忆库怎么搜？"},
			{Role: channel.RoleAssistant, Time: at.Add(time.Minute), Text: "按整词短语匹配。"},
			{Role: channel.RoleUser, Time: at.Add(2 * time.Minute), Text: "那切片呢？"},
		},
	}
	if err := db.WriteSession(s, time.Now()); err != nil {
		t.Fatalf("写会话: %v", err)
	}
	archive := t.TempDir()
	return &Server{DB: db, ArchiveDir: archive, Version: "test"}, db, archive
}

// converse 跑一轮消息，返回按顺序的回复。
func converse(t *testing.T, srv *Server, lines ...string) []map[string]any {
	t.Helper()
	in := strings.NewReader(strings.Join(lines, "\n") + "\n")
	var out bytes.Buffer
	if err := Serve(in, &out, srv); err != nil {
		t.Fatalf("Serve: %v", err)
	}
	var got []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(out.String()), "\n") {
		if line == "" {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatalf("回复不是 JSON: %q", line)
		}
		got = append(got, m)
	}
	return got
}

// toolText 取 tools/call 结果里的文本。
func toolText(t *testing.T, resp map[string]any) (string, bool) {
	t.Helper()
	result, ok := resp["result"].(map[string]any)
	if !ok {
		t.Fatalf("没有 result: %+v", resp)
	}
	content, _ := result["content"].([]any)
	if len(content) == 0 {
		t.Fatalf("没有 content: %+v", result)
	}
	first := content[0].(map[string]any)
	failed, _ := result["isError"].(bool)
	return first["text"].(string), failed
}

func TestInitializeAndToolsList(t *testing.T) {
	srv, _, _ := newServer(t)
	replies := converse(t, srv,
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`,
	)
	if len(replies) != 2 {
		t.Fatalf("通知不该有回复，实际 %d 条", len(replies))
	}
	info := replies[0]["result"].(map[string]any)
	if info["protocolVersion"] == "" || info["serverInfo"] == nil {
		t.Fatalf("初始化结果不全: %+v", info)
	}
	tools := replies[1]["result"].(map[string]any)["tools"].([]any)
	if len(tools) != 6 {
		t.Fatalf("应当有 6 个工具，实际 %d", len(tools))
	}
	names := map[string]bool{}
	for _, one := range tools {
		names[one.(map[string]any)["name"].(string)] = true
	}
	for _, want := range []string{"memory_search", "memory_get_session", "memory_facets", "memory_export_ndjson", "memory_interpret", "memory_stats"} {
		if !names[want] {
			t.Errorf("工具清单缺 %s", want)
		}
	}
}

func TestSearchTool(t *testing.T) {
	srv, _, _ := newServer(t)
	replies := converse(t, srv,
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"memory_search","arguments":{"query":"记忆库"}}}`,
	)
	text, failed := toolText(t, replies[0])
	if failed {
		t.Fatalf("检索不该失败: %s", text)
	}
	var out struct {
		Count int `json:"count"`
		Hits  []struct {
			Channel string `json:"channel"`
			Title   string `json:"session_title"`
			Snippet string `json:"snippet"`
		} `json:"hits"`
	}
	if err := json.Unmarshal([]byte(text), &out); err != nil {
		t.Fatalf("结果不是 JSON: %v (%s)", err, text)
	}
	if out.Count != 1 || len(out.Hits) != 1 {
		t.Fatalf("应当命中 1 条，实际 %+v", out)
	}
	if out.Hits[0].Channel != "claude-code" || !strings.Contains(out.Hits[0].Snippet, "记忆库") {
		t.Fatalf("命中内容不对: %+v", out.Hits[0])
	}
}

func TestSearchRequiresQuery(t *testing.T) {
	srv, _, _ := newServer(t)
	replies := converse(t, srv,
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"memory_search","arguments":{"query":"  "}}}`,
	)
	if _, failed := toolText(t, replies[0]); !failed {
		t.Fatal("空查询应当按错误返回")
	}
}

func TestGetSessionPaginates(t *testing.T) {
	srv, _, _ := newServer(t)
	first := converse(t, srv,
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"memory_get_session","arguments":{"channel":"claude-code","source_ref":"C:/fake/s1.jsonl","session_key":"s1","limit":2}}}`,
	)
	text, failed := toolText(t, first[0])
	if failed {
		t.Fatalf("读会话失败: %s", text)
	}
	var page struct {
		Total   int  `json:"total_events"`
		Count   int  `json:"count"`
		HasMore bool `json:"has_more"`
	}
	if err := json.Unmarshal([]byte(text), &page); err != nil {
		t.Fatal(err)
	}
	if page.Total != 3 || page.Count != 2 || !page.HasMore {
		t.Fatalf("分页不对: %+v", page)
	}
}

func TestFacetsAndStats(t *testing.T) {
	srv, db, _ := newServer(t)
	// 维度表由 derive 填；这里直接写一行，验证读取路径。
	if err := db.ReplaceDimension("by_channel", []store.DimensionRow{{Key: "claude-code", Value: "1"}}); err != nil {
		t.Fatal(err)
	}
	replies := converse(t, srv,
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"memory_facets","arguments":{}}}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"memory_facets","arguments":{"name":"by_channel"}}}`,
		`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"memory_stats","arguments":{}}}`,
	)
	namesText, _ := toolText(t, replies[0])
	if !strings.Contains(namesText, "by_channel") {
		t.Fatalf("维度名列表缺 by_channel: %s", namesText)
	}
	rowsText, _ := toolText(t, replies[1])
	if !strings.Contains(rowsText, "claude-code") {
		t.Fatalf("维度内容不对: %s", rowsText)
	}
	statsText, _ := toolText(t, replies[2])
	if !strings.Contains(statsText, `"sessions":1`) {
		t.Fatalf("概况不对: %s", statsText)
	}
}

func TestExportNDJSONStaysInSliceDir(t *testing.T) {
	srv, _, archive := newServer(t)
	replies := converse(t, srv,
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"memory_export_ndjson","arguments":{"query":"记忆库"}}}`,
	)
	text, failed := toolText(t, replies[0])
	if failed {
		t.Fatalf("导出失败: %s", text)
	}
	var out struct {
		Count int    `json:"count"`
		Path  string `json:"path"`
	}
	if err := json.Unmarshal([]byte(text), &out); err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(archive, "切片")
	if filepath.Dir(out.Path) != want {
		t.Fatalf("切片落点应当在 %s，实际 %s", want, out.Path)
	}
	if out.Count == 0 {
		t.Fatal("切片没有内容")
	}
	if _, err := os.Stat(out.Path); err != nil {
		t.Fatalf("切片文件没落地: %v", err)
	}
}

func TestUnknownToolIsError(t *testing.T) {
	srv, _, _ := newServer(t)
	replies := converse(t, srv,
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"memory_delete_all","arguments":{}}}`,
	)
	if _, failed := toolText(t, replies[0]); !failed {
		t.Fatal("不存在的工具应当按错误返回")
	}
}

// AC-17：本包只读——源码里不得出现写库、删文件、碰渠道目录的调用。
func TestPackageIsReadOnly(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	forbidden := []string{
		"WriteSession(", "ReplaceDimension(", "RebuildFTS(", "WipeDimensions(",
		"AppendRun(", "FlushWatermark(", "MarkL1Backfilled(", "os.Remove", "os.RemoveAll",
	}
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		raw, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		src := string(raw)
		for _, bad := range forbidden {
			if strings.Contains(src, bad) {
				t.Errorf("%s 里出现了写操作 %q：MCP 必须只读（AC-17）", name, bad)
			}
		}
	}
}

// fakeInterp 代替真模型：只记调用次数。
type fakeInterp struct {
	calls  int
	reply  string
	config error
	fail   bool
}

func (f *fakeInterp) Configured() error { return f.config }
func (f *fakeInterp) ModelTag() string  { return "fake-model" }
func (f *fakeInterp) PromptTag() string { return "v-test" }
func (f *fakeInterp) Interpret(ctx context.Context, perspective, transcript string) (string, error) {
	f.calls++
	if f.fail {
		return "", errors.New("模型超时")
	}
	return f.reply, nil
}

// I-093：按需视角——第一次调模型，第二次命中缓存，force 时重新解读。
func TestInterpretCachesBySession(t *testing.T) {
	srv, _, _ := newServer(t)
	fake := &fakeInterp{reply: `{"items":[{"topic":"记忆库"}]}`}
	srv.LLM = fake

	args := `{"perspective":"topic","channel":"claude-code","source_ref":"C:/fake/s1.jsonl","session_key":"s1"}`
	first := converse(t, srv, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"memory_interpret","arguments":`+args+`}}`)
	text, failed := toolText(t, first[0])
	if failed {
		t.Fatalf("第一次解读失败: %s", text)
	}
	if fake.calls != 1 || !strings.Contains(text, `"cached":false`) {
		t.Fatalf("第一次应当真的调模型: calls=%d text=%s", fake.calls, text)
	}

	second := converse(t, srv, `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"memory_interpret","arguments":`+args+`}}`)
	text, _ = toolText(t, second[0])
	if fake.calls != 1 || !strings.Contains(text, `"cached":true`) {
		t.Fatalf("第二次应当命中缓存: calls=%d text=%s", fake.calls, text)
	}

	forced := `{"perspective":"topic","channel":"claude-code","source_ref":"C:/fake/s1.jsonl","session_key":"s1","force":true}`
	third := converse(t, srv, `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"memory_interpret","arguments":`+forced+`}}`)
	if text, _ := toolText(t, third[0]); !strings.Contains(text, `"cached":false`) || fake.calls != 2 {
		t.Fatalf("force 应当重新调模型: calls=%d text=%s", fake.calls, text)
	}
}

// 没打开开关时明确说「没打开」，不是静默失败。
func TestInterpretWithoutSwitch(t *testing.T) {
	srv, _, _ := newServer(t)
	replies := converse(t, srv,
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"memory_interpret","arguments":{"perspective":"topic","channel":"claude-code","source_ref":"C:/fake/s1.jsonl","session_key":"s1"}}}`,
	)
	text, failed := toolText(t, replies[0])
	if !failed || !strings.Contains(text, "没打开") {
		t.Fatalf("应当提示没打开: failed=%v text=%s", failed, text)
	}
}

// 缺环境变量时把人该配什么原样说出来。
func TestInterpretReportsMissingConfig(t *testing.T) {
	srv, _, _ := newServer(t)
	srv.LLM = &fakeInterp{config: errors.New("LLM 视角没配置：缺环境变量 AGENTMEMORY_LLM_API_KEY")}
	replies := converse(t, srv,
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"memory_interpret","arguments":{"perspective":"todo","channel":"claude-code","source_ref":"C:/fake/s1.jsonl","session_key":"s1"}}}`,
	)
	text, failed := toolText(t, replies[0])
	if !failed || !strings.Contains(text, "AGENTMEMORY_LLM_API_KEY") {
		t.Fatalf("应当说清缺哪个变量: failed=%v text=%s", failed, text)
	}
}

// 模型调用失败时按工具错误返回，不影响其它工具。
func TestInterpretReportsModelFailure(t *testing.T) {
	srv, _, _ := newServer(t)
	srv.LLM = &fakeInterp{fail: true}
	replies := converse(t, srv,
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"memory_interpret","arguments":{"perspective":"decision","channel":"claude-code","source_ref":"C:/fake/s1.jsonl","session_key":"s1"}}}`,
	)
	text, failed := toolText(t, replies[0])
	if !failed || !strings.Contains(text, "解读没做成") {
		t.Fatalf("应当报解读失败: failed=%v text=%s", failed, text)
	}
	// 之后的普通工具照常工作。
	again := converse(t, srv, `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"memory_stats","arguments":{}}}`)
	if _, failed := toolText(t, again[0]); failed {
		t.Fatal("一次解读失败不该影响其它工具")
	}
}
