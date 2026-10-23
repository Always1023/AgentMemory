// Package grokbuild 实现对 Grok Build 会话记录的适配器。
//
// 数据来源：<用户目录>/.grok/sessions/**/summary.json 与 archived_sessions。
// summary.json 记录会话身份、项目与标题；正文在同目录的 chat_history.jsonl。
package grokbuild

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
	ChannelID = "grok-build"
	Name      = "Grok Build"
)

const (
	maxFileBytes = 64 << 20
	maxLineBytes = 8 << 20
)

// Adapter 实现 channel.Adapter。
type Adapter struct {
	// Root 是 .grok 目录；留空时取 <用户目录>/.grok。
	Root string
}

// Detect 发现 sessions 与 archived_sessions 下的全部 summary.json。
func (a Adapter) Detect() channel.DetectResult {
	root, err := a.root()
	if err != nil {
		return channel.DetectResult{Status: channel.StatusFailed, Err: err}
	}
	if _, err := os.Stat(root); errors.Is(err, fs.ErrNotExist) {
		return channel.DetectResult{Status: channel.StatusNotInstalled}
	}

	var files []channel.SourceFile
	for _, dir := range []string{filepath.Join(root, "sessions"), filepath.Join(root, "archived_sessions")} {
		err := filepath.WalkDir(dir, func(path string, entry fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return nil
			}
			if entry.IsDir() || entry.Name() != "summary.json" {
				return nil
			}
			info, err := entry.Info()
			if err != nil {
				return nil
			}
			sourceInfo := info
			if chatInfo, err := os.Stat(filepath.Join(filepath.Dir(path), "chat_history.jsonl")); err == nil {
				sourceInfo = chatInfo
			}
			files = append(files, channel.SourceFile{
				Path:    path,
				Size:    sourceInfo.Size(),
				ModTime: sourceInfo.ModTime(),
			})
			return nil
		})
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			return channel.DetectResult{Status: channel.StatusFailed, Root: root, Err: fmt.Errorf("扫 %s: %w", dir, err)}
		}
	}

	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	return channel.DetectResult{Status: channel.StatusAvailable, Root: root, Files: files}
}

// Parse 解析一份 Grok Build 会话。
func (a Adapter) Parse(sf channel.SourceFile) (channel.Session, error) {
	summary, err := readSummary(sf.Path)
	if err != nil {
		return channel.Session{}, err
	}
	id := strings.TrimSpace(summary.Info.ID)
	if id == "" {
		id = filepath.Base(filepath.Dir(sf.Path))
	}
	session := channel.Session{
		Channel:     ChannelID,
		ID:          id,
		ProjectPath: summary.Info.Cwd,
		SourceFile:  sf.Path,
		StartedAt:   parseRawTime(summary.CreatedAt),
	}
	if session.StartedAt.IsZero() {
		session.StartedAt = sf.ModTime
	}

	chatPath := filepath.Join(filepath.Dir(sf.Path), "chat_history.jsonl")
	messages, err := readChat(chatPath)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return channel.Session{}, err
	}
	session.Messages = messages

	var candidates []string
	for _, msg := range messages {
		if msg.Role == channel.RoleUser {
			candidates = append(candidates, msg.Text)
		}
	}
	switch {
	case strings.TrimSpace(summary.GeneratedTitle) != "":
		session.Title = channel.Shorten(summary.GeneratedTitle, channel.TitleLimit)
	case strings.TrimSpace(summary.SessionSummary) != "":
		session.Title = channel.Shorten(summary.SessionSummary, channel.TitleLimit)
	default:
		session.Title = channel.TitleFrom(candidates, sf.Path, session.ID)
	}
	// 没有消息的会话不是错误，交给编排层跳过（I-039）。
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
	return filepath.Join(home, ".grok"), nil
}

type summaryDocument struct {
	Info struct {
		ID  string `json:"id"`
		Cwd string `json:"cwd"`
	} `json:"info"`
	SessionSummary string          `json:"session_summary"`
	GeneratedTitle string          `json:"generated_title"`
	CreatedAt      json.RawMessage `json:"created_at"`
	UpdatedAt      json.RawMessage `json:"updated_at"`
	LastActiveAt   json.RawMessage `json:"last_active_at"`
}

func readSummary(path string) (summaryDocument, error) {
	info, err := os.Stat(path)
	if err != nil {
		return summaryDocument{}, fmt.Errorf("读 %s: %w", path, err)
	}
	if info.Size() > maxFileBytes {
		return summaryDocument{}, fmt.Errorf("%s 超过上限 %d", path, maxFileBytes)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return summaryDocument{}, fmt.Errorf("读 %s: %w", path, err)
	}
	var summary summaryDocument
	if err := json.Unmarshal(raw, &summary); err != nil {
		return summaryDocument{}, fmt.Errorf("解析 %s: %w", path, err)
	}
	return summary, nil
}

type chatLine struct {
	Type       string          `json:"type"`
	Content    json.RawMessage `json:"content"`
	Timestamp  json.RawMessage `json:"timestamp"`
	ToolCalls  []openAITool    `json:"tool_calls"`
	ToolCallID string          `json:"tool_call_id"`
	CallID     string          `json:"call_id"`
}

type openAITool struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

func readChat(path string) ([]channel.Message, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	var messages []channel.Message
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 64<<10), maxLineBytes)
	for lineNo := 1; scanner.Scan(); lineNo++ {
		raw := bytes.TrimSpace(scanner.Bytes())
		if len(raw) == 0 {
			continue
		}
		var row chatLine
		if err := json.Unmarshal(raw, &row); err != nil {
			return nil, fmt.Errorf("解析 %s 第 %d 行: %w", path, lineNo, err)
		}
		text := contentText(row.Content)
		when := parseRawTime(row.Timestamp)
		switch row.Type {
		case "user":
			if strings.TrimSpace(text) != "" {
				messages = append(messages, channel.Message{Role: channel.RoleUser, Time: when, Text: text})
			}
		case "assistant":
			msg := channel.Message{Role: channel.RoleAssistant, Time: when, Text: text}
			for _, call := range row.ToolCalls {
				msg.Tools = append(msg.Tools, channel.ToolCall{
					Name:  call.Function.Name,
					Input: call.Function.Arguments,
				})
			}
			if strings.TrimSpace(msg.Text) != "" || len(msg.Tools) > 0 {
				messages = append(messages, msg)
			}
		case "tool":
			if strings.TrimSpace(text) == "" {
				continue
			}
			messages = append(messages, channel.Message{Role: channel.RoleTool, Time: when, Text: text})
		default:
			// system 与 reasoning 不是对话正文。
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("读 %s: %w", path, err)
	}
	return messages, nil
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
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal(raw, &blocks); err != nil {
		return ""
	}
	var parts []string
	for _, block := range blocks {
		if block.Type == "text" && strings.TrimSpace(block.Text) != "" {
			parts = append(parts, block.Text)
		}
	}
	return strings.Join(parts, "\n")
}

func parseRawTime(raw json.RawMessage) time.Time {
	if len(raw) == 0 {
		return time.Time{}
	}
	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		for _, layout := range []string{time.RFC3339Nano, time.RFC3339} {
			if t, err := time.Parse(layout, text); err == nil {
				return t
			}
		}
	}
	var number float64
	if err := json.Unmarshal(raw, &number); err == nil && number > 0 {
		if number > 1e12 {
			return time.UnixMilli(int64(number))
		}
		return time.Unix(int64(number), 0)
	}
	return time.Time{}
}
