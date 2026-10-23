// Package openclaw 实现对 OpenClaw 会话记录的适配器。
//
// 数据来源：<用户目录>/.openclaw/agents/<agent>/sessions/*.jsonl。
// sessions.json 是显示名索引；会话正文在 JSONL 里，格式与 Pi 同源。
package openclaw

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
	ChannelID = "openclaw"
	Name      = "OpenClaw"
)

const maxLineBytes = 8 << 20

// Adapter 实现 channel.Adapter。
type Adapter struct {
	// Root 是 agents 目录；留空时取 <用户目录>/.openclaw/agents。
	Root string
}

// Detect 发现全部 agent 会话文件。
func (a Adapter) Detect() channel.DetectResult {
	root, err := a.root()
	if err != nil {
		return channel.DetectResult{Status: channel.StatusFailed, Err: err}
	}
	agents, err := os.ReadDir(root)
	if errors.Is(err, fs.ErrNotExist) {
		return channel.DetectResult{Status: channel.StatusNotInstalled}
	}
	if err != nil {
		return channel.DetectResult{Status: channel.StatusFailed, Root: root, Err: fmt.Errorf("读 %s: %w", root, err)}
	}

	var files []channel.SourceFile
	for _, agent := range agents {
		if !agent.IsDir() {
			continue
		}
		sessions := filepath.Join(root, agent.Name(), "sessions")
		items, err := os.ReadDir(sessions)
		if err != nil {
			continue
		}
		for _, item := range items {
			if item.IsDir() || !strings.EqualFold(filepath.Ext(item.Name()), ".jsonl") {
				continue
			}
			info, err := item.Info()
			if err != nil {
				continue
			}
			files = append(files, channel.SourceFile{
				Path:    filepath.Join(sessions, item.Name()),
				Size:    info.Size(),
				ModTime: info.ModTime(),
			})
		}
	}

	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	return channel.DetectResult{Status: channel.StatusAvailable, Root: root, Files: files}
}

// Parse 解析一份 OpenClaw 会话。
func (a Adapter) Parse(sf channel.SourceFile) (channel.Session, error) {
	displayNames := readDisplayNames(filepath.Join(filepath.Dir(sf.Path), "sessions.json"))

	file, err := os.Open(sf.Path)
	if err != nil {
		return channel.Session{}, fmt.Errorf("读 %s: %w", sf.Path, err)
	}
	defer file.Close()

	session := channel.Session{Channel: ChannelID, SourceFile: sf.Path}
	var candidates []string
	var explicitName string

	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 64<<10), maxLineBytes)
	for lineNo := 1; scanner.Scan(); lineNo++ {
		raw := bytes.TrimSpace(scanner.Bytes())
		if len(raw) == 0 {
			continue
		}
		var row sessionLine
		if err := json.Unmarshal(raw, &row); err != nil {
			return channel.Session{}, fmt.Errorf("解析 %s 第 %d 行: %w", sf.Path, lineNo, err)
		}
		switch row.Type {
		case "session":
			if session.ID == "" {
				session.ID = row.ID
			}
			if session.ProjectPath == "" {
				session.ProjectPath = row.Cwd
			}
			if session.StartedAt.IsZero() {
				session.StartedAt = parseTime(row.Timestamp)
			}
		case "session_info":
			if explicitName == "" {
				explicitName = strings.TrimSpace(row.Name)
			}
		case "message":
			text, tools := decodeContent(row.Message.Content)
			text = stripMessageID(text)
			if strings.TrimSpace(text) == "" && len(tools) == 0 {
				continue
			}
			when := parseTime(row.Timestamp)
			if when.IsZero() {
				when = parseTime(row.Message.Timestamp)
			}
			msg := channel.Message{
				Role:  roleOf(row.Message.Role),
				Time:  when,
				Text:  text,
				Tools: tools,
			}
			if msg.Role == channel.RoleUser {
				candidates = append(candidates, text)
			}
			session.Messages = append(session.Messages, msg)
		}
	}
	if err := scanner.Err(); err != nil {
		return channel.Session{}, fmt.Errorf("读 %s: %w", sf.Path, err)
	}

	if session.ID == "" {
		session.ID = strings.TrimSuffix(filepath.Base(sf.Path), filepath.Ext(sf.Path))
	}
	if session.StartedAt.IsZero() {
		session.StartedAt = sf.ModTime
	}
	switch {
	case displayNames[session.ID] != "":
		session.Title = channel.Shorten(displayNames[session.ID], channel.TitleLimit)
	case explicitName != "":
		session.Title = channel.Shorten(explicitName, channel.TitleLimit)
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
	return filepath.Join(home, ".openclaw", "agents"), nil
}

type sessionLine struct {
	Type      string `json:"type"`
	ID        string `json:"id"`
	Cwd       string `json:"cwd"`
	Name      string `json:"name"`
	Timestamp string `json:"timestamp"`
	Message   struct {
		Role      string          `json:"role"`
		Content   json.RawMessage `json:"content"`
		Timestamp string          `json:"timestamp"`
	} `json:"message"`
}

func readDisplayNames(path string) map[string]string {
	out := map[string]string{}
	raw, err := os.ReadFile(path)
	if err != nil {
		return out
	}
	var index map[string]struct {
		SessionID   string `json:"sessionId"`
		DisplayName string `json:"displayName"`
	}
	if err := json.Unmarshal(raw, &index); err != nil {
		return out
	}
	for _, item := range index {
		if item.SessionID != "" && strings.TrimSpace(item.DisplayName) != "" {
			out[item.SessionID] = item.DisplayName
		}
	}
	return out
}

func decodeContent(raw json.RawMessage) (string, []channel.ToolCall) {
	if len(raw) == 0 {
		return "", nil
	}
	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		return text, nil
	}
	var blocks []struct {
		Type      string          `json:"type"`
		Text      string          `json:"text"`
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}
	if err := json.Unmarshal(raw, &blocks); err != nil {
		return "", nil
	}
	var parts []string
	var tools []channel.ToolCall
	for _, block := range blocks {
		switch block.Type {
		case "text":
			if strings.TrimSpace(block.Text) != "" {
				parts = append(parts, block.Text)
			}
		case "toolCall":
			tools = append(tools, channel.ToolCall{
				Name:  block.Name,
				Input: string(block.Arguments),
			})
		}
	}
	return strings.Join(parts, "\n"), tools
}

func stripMessageID(text string) string {
	if at := strings.LastIndex(text, "\n[message_id:"); at >= 0 {
		return strings.TrimSpace(text[:at])
	}
	return text
}

func roleOf(role string) string {
	switch role {
	case "user":
		return channel.RoleUser
	case "system":
		return channel.RoleSystem
	case "tool", "toolResult":
		return channel.RoleTool
	default:
		return channel.RoleAssistant
	}
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
