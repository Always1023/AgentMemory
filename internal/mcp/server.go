// Package mcp 是对外只读接口（F15 / I-091）：stdio 上的 MCP JSON-RPC。
//
// 规矩（ARCHITECTURE AC-17）：
//   - 只读：只调 store 的读方法与切片导出；不写 L1、不碰渠道原始目录、不触发收集。
//   - 与界面并列的第二个消费出口；不复制 app 的编排逻辑。
//   - 单次返回有上限：检索条数、会话事件分页、正文截断、切片条数。
//
// 协议只用到 initialize / tools/list / tools/call / ping，消息按行分隔（MCP stdio）。
package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"strings"

	"github.com/Always1023/AgentMemory/internal/store"
)

// protocolVersion 是握手时回报的版本。客户端按它决定能力，不认识就退回文字。
const protocolVersion = "2024-11-05"

// Server 是一次 MCP 会话的服务端：一个库、一个归档目录、一个日志出口。
type Server struct {
	DB         DB
	ArchiveDir string
	Logger     *slog.Logger
	Version    string
}

// DB 是本包只需要的只读能力集；用接口收口，方便用例塞假实现，也让「只读」看得见。
type DB interface {
	Search(query string, filter store.SearchFilter, limit int) ([]store.SearchHit, error)
	LatestSessions() ([]store.SessionRef, error)
	Events(sessionID int64) ([]store.Event, error)
	Dimension(name string) ([]store.DimensionRow, error)
	DimensionNames() ([]string, error)
	WriteSearchNDJSON(w io.Writer, query string, limit int) (int, error)
	Stats() (store.Stats, error)
	Query(ctx context.Context, req store.QueryRequest) (store.QueryResult, error)
}

// request / response 是 JSON-RPC 2.0 的最小形态。
type request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// Serve 读 stdin 上的行、写 stdout；客户端断开就返回。
func Serve(in io.Reader, out io.Writer, srv *Server) error {
	scanner := bufio.NewScanner(in)
	// 单条报文可能带很长的正文（切片导出请求），给足缓冲。
	scanner.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	enc := json.NewEncoder(out)

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var req request
		if err := json.Unmarshal([]byte(line), &req); err != nil {
			if err := enc.Encode(response{JSONRPC: "2.0", Error: &rpcError{Code: -32700, Message: "看不懂的报文"}}); err != nil {
				return err
			}
			continue
		}
		resp, reply := srv.handle(req)
		if !reply {
			continue // 通知类消息不回
		}
		if err := enc.Encode(resp); err != nil {
			return err
		}
	}
	return scanner.Err()
}

// handle 分发一条报文；reply = false 表示这是通知，不回包。
func (s *Server) handle(req request) (response, bool) {
	switch req.Method {
	case "initialize":
		return response{JSONRPC: "2.0", ID: req.ID, Result: map[string]any{
			"protocolVersion": protocolVersion,
			"capabilities":    map[string]any{"tools": map[string]any{}},
			"serverInfo":      map[string]any{"name": "agentmemory", "version": s.version()},
		}}, true
	case "notifications/initialized", "initialized":
		return response{}, false
	case "ping":
		return response{JSONRPC: "2.0", ID: req.ID, Result: map[string]any{}}, true
	case "tools/list":
		return response{JSONRPC: "2.0", ID: req.ID, Result: map[string]any{"tools": toolDefs()}}, true
	case "tools/call":
		var params struct {
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
		}
		if err := json.Unmarshal(req.Params, &params); err != nil {
			return response{JSONRPC: "2.0", ID: req.ID, Error: &rpcError{Code: -32602, Message: "参数看不懂"}}, true
		}
		text, failed := s.callTool(params.Name, params.Arguments)
		return response{JSONRPC: "2.0", ID: req.ID, Result: map[string]any{
			"content": []map[string]any{{"type": "text", "text": text}},
			"isError": failed,
		}}, true
	default:
		return response{JSONRPC: "2.0", ID: req.ID, Error: &rpcError{Code: -32601, Message: "没有这个方法：" + req.Method}}, true
	}
}

func (s *Server) version() string {
	if s.Version == "" {
		return "dev"
	}
	return s.Version
}

func (s *Server) log() *slog.Logger {
	if s.Logger == nil {
		return slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	return s.Logger
}
