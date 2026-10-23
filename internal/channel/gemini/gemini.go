// Package gemini 实现对 Gemini CLI 会话记录的适配器。
//
// 数据来源：<用户目录>/.gemini/tmp/<项目>/chats/session-*.json 或 session-*.jsonl。
// 旧版会话是一整份 JSON；新版会话是 JSONL 编辑记录，按 Gemini CLI 的回放规则还原：
// 同 id 原位覆盖、$set 合并元数据或整体替换 messages、$rewindTo 删除该条及之后的消息。
package gemini

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/Always1023/AgentMemory/internal/channel"
)

// ChannelID 与 Name 是注册表用的身份。
const (
	ChannelID = "gemini"
	Name      = "Gemini CLI"
)

const (
	// maxFileBytes 是单份会话的上限（AC-07）。整份 JSON 会先量大小。
	maxFileBytes = 64 << 20
	// maxLineBytes 是 JSONL 单行的上限（AC-07）：一条记录可能带整段正文。
	maxLineBytes = 8 << 20
)

// Adapter 实现 channel.Adapter。
type Adapter struct {
	// Root 是 tmp 目录；留空时取 <用户目录>/.gemini/tmp。
	Root string
}

// Detect 发现全部会话文件。
func (a Adapter) Detect() channel.DetectResult {
	root, err := a.root()
	if err != nil {
		return channel.DetectResult{Status: channel.StatusFailed, Err: err}
	}

	projects, err := os.ReadDir(root)
	if errors.Is(err, fs.ErrNotExist) {
		return channel.DetectResult{Status: channel.StatusNotInstalled}
	}
	if err != nil {
		return channel.DetectResult{Status: channel.StatusFailed, Root: root, Err: fmt.Errorf("读 %s: %w", root, err)}
	}

	var files []channel.SourceFile
	for _, project := range projects {
		if !project.IsDir() {
			continue
		}
		chats := filepath.Join(root, project.Name(), "chats")
		items, err := os.ReadDir(chats)
		if err != nil {
			// 单个项目读不了，不影响其余项目。
			continue
		}
		for _, item := range items {
			if item.IsDir() || !isSessionFile(filepath.Join(chats, item.Name())) {
				continue
			}
			info, err := item.Info()
			if err != nil {
				continue
			}
			files = append(files, channel.SourceFile{
				Path:    filepath.Join(chats, item.Name()),
				Size:    info.Size(),
				ModTime: info.ModTime(),
			})
		}
	}

	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	return channel.DetectResult{Status: channel.StatusAvailable, Root: root, Files: files}
}

// Parse 把一份会话文件解析成统一会话模型。
func (a Adapter) Parse(sf channel.SourceFile) (channel.Session, error) {
	if sf.Size > maxFileBytes {
		return channel.Session{}, fmt.Errorf("%s 有 %d 字节，超过上限 %d，跳过", sf.Path, sf.Size, maxFileBytes)
	}

	var (
		doc sessionDocument
		err error
	)
	if strings.EqualFold(filepath.Ext(sf.Path), ".jsonl") {
		doc, err = readJSONL(sf.Path)
	} else {
		doc, err = readJSON(sf.Path)
	}
	if err != nil {
		return channel.Session{}, err
	}

	id := strings.TrimSpace(doc.SessionID)
	if id == "" {
		id = strings.TrimSuffix(filepath.Base(sf.Path), filepath.Ext(sf.Path))
	}
	session := channel.Session{
		Channel:     ChannelID,
		ID:          id,
		ProjectPath: readProjectRoot(sf.Path),
		SourceFile:  sf.Path,
		StartedAt:   parseTime(doc.StartTime),
	}
	if session.StartedAt.IsZero() {
		session.StartedAt = sf.ModTime
	}

	var candidates []string
	for _, item := range doc.Messages {
		text := contentText(item.Content)
		when := parseTime(item.Timestamp)
		switch item.Type {
		case "user":
			if isIgnoredUserText(text) {
				continue
			}
			session.Messages = append(session.Messages, channel.Message{
				Role: channel.RoleUser,
				Time: when,
				Text: text,
			})
			candidates = append(candidates, text)
		case "gemini":
			msg := channel.Message{Role: channel.RoleAssistant, Time: when, Text: text}
			for _, call := range item.ToolCalls {
				msg.Tools = append(msg.Tools, channel.ToolCall{
					Name:   call.Name,
					Input:  string(call.Args),
					Output: call.output(),
				})
			}
			if strings.TrimSpace(msg.Text) == "" && len(msg.Tools) == 0 {
				continue
			}
			session.Messages = append(session.Messages, msg)
		case "info", "error":
			if strings.TrimSpace(text) == "" {
				continue
			}
			session.Messages = append(session.Messages, channel.Message{
				Role: channel.RoleSystem,
				Time: when,
				Text: text,
			})
		}
	}

	session.Title = channel.TitleFrom(candidates, sf.Path, session.ID)
	// 一条消息都没有 = 空会话（打开过没说话）：不是错，由编排层跳过（I-039）。
	return session, nil
}

func (a Adapter) root() (string, error) {
	if a.Root != "" {
		return a.Root, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("取用户目录: %w", err)
	}
	return filepath.Join(home, ".gemini", "tmp"), nil
}

func isSessionFile(path string) bool {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".jsonl":
		return true
	case ".json":
		return !fileExists(path + "l")
	default:
		return false
	}
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

type sessionDocument struct {
	SessionID   string           `json:"sessionId"`
	StartTime   string           `json:"startTime"`
	LastUpdated string           `json:"lastUpdated"`
	Messages    []sessionMessage `json:"messages"`
}

type sessionMessage struct {
	ID        string          `json:"id"`
	Type      string          `json:"type"`
	Timestamp string          `json:"timestamp"`
	Content   json.RawMessage `json:"content"`
	ToolCalls []toolCall      `json:"toolCalls"`
}

type toolCall struct {
	Name          string          `json:"name"`
	Args          json.RawMessage `json:"args"`
	ResultDisplay json.RawMessage `json:"resultDisplay"`
	Result        json.RawMessage `json:"result"`
}

func (c toolCall) output() string {
	if len(c.ResultDisplay) > 0 {
		var text string
		if err := json.Unmarshal(c.ResultDisplay, &text); err == nil {
			return text
		}
		return string(c.ResultDisplay)
	}
	if len(c.Result) > 0 {
		return string(c.Result)
	}
	return ""
}

func readJSON(path string) (sessionDocument, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return sessionDocument{}, fmt.Errorf("读 %s: %w", path, err)
	}
	var doc sessionDocument
	if err := json.Unmarshal(raw, &doc); err != nil {
		return sessionDocument{}, fmt.Errorf("解析 %s: %w", path, err)
	}
	return doc, nil
}

func readJSONL(path string) (sessionDocument, error) {
	file, err := os.Open(path)
	if err != nil {
		return sessionDocument{}, fmt.Errorf("读 %s: %w", path, err)
	}
	defer file.Close()

	var doc sessionDocument
	var messages []sessionMessage
	index := map[string]int{}

	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 64<<10), maxLineBytes)
	for lineNo := 1; scanner.Scan(); lineNo++ {
		raw := bytes.TrimSpace(scanner.Bytes())
		if len(raw) == 0 {
			continue
		}
		var record map[string]json.RawMessage
		if err := json.Unmarshal(raw, &record); err != nil {
			return sessionDocument{}, fmt.Errorf("解析 %s 第 %d 行: %w", path, lineNo, err)
		}

		if rewind, ok := record["$rewindTo"]; ok {
			var id string
			if err := json.Unmarshal(rewind, &id); err != nil {
				return sessionDocument{}, fmt.Errorf("解析 %s 第 %d 行 $rewindTo: %w", path, lineNo, err)
			}
			cut := index[id]
			messages = messages[:cut]
			index = map[string]int{}
			for i, msg := range messages {
				if msg.ID != "" {
					index[msg.ID] = i
				}
			}
			continue
		}

		if set, ok := record["$set"]; ok {
			var update map[string]json.RawMessage
			if err := json.Unmarshal(set, &update); err != nil {
				return sessionDocument{}, fmt.Errorf("解析 %s 第 %d 行 $set: %w", path, lineNo, err)
			}
			if rawMessages, ok := update["messages"]; ok {
				var list []sessionMessage
				if err := json.Unmarshal(rawMessages, &list); err != nil {
					return sessionDocument{}, fmt.Errorf("解析 %s 第 %d 行 $set.messages: %w", path, lineNo, err)
				}
				messages = messages[:0]
				index = map[string]int{}
				for _, msg := range list {
					upsertMessage(&messages, index, msg)
				}
			}
			mergeMetadata(&doc, update)
			continue
		}

		var msg sessionMessage
		if idRaw, ok := record["id"]; ok {
			if err := json.Unmarshal(idRaw, &msg.ID); err != nil || msg.ID == "" {
				continue
			}
			if err := json.Unmarshal(raw, &msg); err != nil {
				return sessionDocument{}, fmt.Errorf("解析 %s 第 %d 行消息: %w", path, lineNo, err)
			}
			upsertMessage(&messages, index, msg)
			continue
		}

		mergeMetadata(&doc, record)
		if rawMessages, ok := record["messages"]; ok {
			var list []sessionMessage
			if err := json.Unmarshal(rawMessages, &list); err != nil {
				return sessionDocument{}, fmt.Errorf("解析 %s 第 %d 行 messages: %w", path, lineNo, err)
			}
			for _, msg := range list {
				upsertMessage(&messages, index, msg)
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return sessionDocument{}, fmt.Errorf("读 %s: %w", path, err)
	}
	doc.Messages = messages
	return doc, nil
}

func upsertMessage(messages *[]sessionMessage, index map[string]int, msg sessionMessage) {
	if msg.ID == "" {
		*messages = append(*messages, msg)
		return
	}
	if at, ok := index[msg.ID]; ok {
		(*messages)[at] = msg
		return
	}
	index[msg.ID] = len(*messages)
	*messages = append(*messages, msg)
}

func mergeMetadata(doc *sessionDocument, values map[string]json.RawMessage) {
	for key, raw := range values {
		switch key {
		case "sessionId":
			_ = json.Unmarshal(raw, &doc.SessionID)
		case "startTime":
			_ = json.Unmarshal(raw, &doc.StartTime)
		case "lastUpdated":
			_ = json.Unmarshal(raw, &doc.LastUpdated)
		}
	}
}

func contentText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		return text
	}
	var blocks []struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal(raw, &blocks); err != nil {
		return ""
	}
	var parts []string
	for _, block := range blocks {
		if strings.TrimSpace(block.Text) != "" {
			parts = append(parts, block.Text)
		}
	}
	return strings.Join(parts, "\n")
}

func isIgnoredUserText(text string) bool {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return true
	}
	return strings.HasPrefix(trimmed, "<session_context>") ||
		strings.HasPrefix(trimmed, "<hook_context>") ||
		strings.HasPrefix(trimmed, "/") ||
		strings.HasPrefix(trimmed, "?")
}

func readProjectRoot(sessionPath string) string {
	// <tmp>/<项目>/chats/<会话> -> <tmp>/<项目>/.project_root
	projectDir := filepath.Dir(filepath.Dir(sessionPath))
	raw, err := os.ReadFile(filepath.Join(projectDir, ".project_root"))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(raw))
}

func parseTime(raw string) time.Time {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return time.Time{}
	}
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339} {
		if t, err := time.Parse(layout, raw); err == nil {
			return t
		}
	}
	return time.Time{}
}
