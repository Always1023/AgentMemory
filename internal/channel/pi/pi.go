// Package pi 实现对 Pi 会话记录的适配器。
//
// 数据来源：<用户目录>/.pi/agent/sessions/<项目>/<会话>.jsonl；
// PI_CODING_AGENT_SESSION_DIR 可以覆盖会话根目录。
// 第一行是 session header，后续 type=message 的行是用户、助手或工具消息。
package pi

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
	ChannelID = "pi"
	Name      = "Pi"
)

const (
	maxLineBytes = 8 << 20
	maxFiles     = 20000
)

// Adapter 实现 channel.Adapter。
type Adapter struct {
	// Root 是 sessions 目录；留空时取 <用户目录>/.pi/agent/sessions。
	Root string
}

// Detect 发现全部 Pi 会话文件。
func (a Adapter) Detect() channel.DetectResult {
	root, err := a.root()
	if err != nil {
		return channel.DetectResult{Status: channel.StatusFailed, Err: err}
	}
	if _, err := os.Stat(root); errors.Is(err, fs.ErrNotExist) {
		return channel.DetectResult{Status: channel.StatusNotInstalled}
	}

	var files []channel.SourceFile
	err = filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return nil
		}
		if entry.IsDir() {
			if depth(root, path) > 3 {
				return fs.SkipDir
			}
			return nil
		}
		if !strings.EqualFold(filepath.Ext(entry.Name()), ".jsonl") {
			return nil
		}
		if len(files) >= maxFiles {
			return fmt.Errorf("%s 下的会话超过 %d 个上限", root, maxFiles)
		}
		info, err := entry.Info()
		if err != nil {
			return nil
		}
		files = append(files, channel.SourceFile{
			Path:    path,
			Size:    info.Size(),
			ModTime: info.ModTime(),
		})
		return nil
	})
	if err != nil {
		return channel.DetectResult{Status: channel.StatusFailed, Root: root, Err: fmt.Errorf("扫 %s: %w", root, err)}
	}

	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	return channel.DetectResult{Status: channel.StatusAvailable, Root: root, Files: files}
}

// Parse 解析一份 Pi 会话。
func (a Adapter) Parse(sf channel.SourceFile) (channel.Session, error) {
	file, err := os.Open(sf.Path)
	if err != nil {
		return channel.Session{}, fmt.Errorf("读 %s: %w", sf.Path, err)
	}
	defer file.Close()

	var entries []entry
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 64<<10), maxLineBytes)
	for lineNo := 1; scanner.Scan(); lineNo++ {
		raw := bytes.TrimSpace(scanner.Bytes())
		if len(raw) == 0 {
			continue
		}
		var item entry
		if err := json.Unmarshal(raw, &item); err != nil {
			return channel.Session{}, fmt.Errorf("解析 %s 第 %d 行: %w", sf.Path, lineNo, err)
		}
		entries = append(entries, item)
	}
	if err := scanner.Err(); err != nil {
		return channel.Session{}, fmt.Errorf("读 %s: %w", sf.Path, err)
	}
	if len(entries) == 0 || entries[0].Type != "session" {
		return channel.Session{}, fmt.Errorf("%s 不是 Pi 会话：第一行不是 session header", sf.Path)
	}

	header := entries[0]
	active := activeEntries(entries)
	session := channel.Session{
		Channel:     ChannelID,
		ID:          header.ID,
		ProjectPath: header.Cwd,
		SourceFile:  sf.Path,
		StartedAt:   parseRawTime(header.Timestamp),
	}
	if session.ID == "" {
		session.ID = strings.TrimSuffix(filepath.Base(sf.Path), filepath.Ext(sf.Path))
	}
	if session.StartedAt.IsZero() {
		session.StartedAt = sf.ModTime
	}

	var candidates []string
	var explicitName string
	for _, item := range active {
		if item.Type == "session_info" {
			if name := strings.TrimSpace(item.Name); name != "" {
				explicitName = name
			}
			continue
		}
		if item.Type != "message" {
			continue
		}
		text, tools := decodeContent(item.Message.Content)
		when := parseRawTime(item.Message.Timestamp)
		if when.IsZero() {
			when = parseRawTime(item.Timestamp)
		}
		role := roleOf(item.Message.Role)
		if role == channel.RoleAssistant && item.Message.Role == "bashExecution" {
			command := strings.TrimSpace(item.Message.Command)
			output := strings.TrimSpace(item.Message.Output)
			text = strings.TrimSpace("$ " + command + "\n" + output)
		}
		if strings.TrimSpace(text) == "" && len(tools) == 0 {
			continue
		}
		msg := channel.Message{Role: role, Time: when, Text: text, Tools: tools}
		if msg.Role == channel.RoleUser {
			candidates = append(candidates, text)
		}
		session.Messages = append(session.Messages, msg)
	}

	if explicitName != "" {
		session.Title = channel.Shorten(explicitName, channel.TitleLimit)
	} else {
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
	if raw := strings.TrimSpace(os.Getenv("PI_CODING_AGENT_SESSION_DIR")); raw != "" {
		if raw == "~" {
			return home, nil
		}
		if strings.HasPrefix(raw, "~/") || strings.HasPrefix(raw, `~\`) {
			return filepath.Join(home, raw[2:]), nil
		}
		return filepath.Clean(raw), nil
	}
	return filepath.Join(home, ".pi", "agent", "sessions"), nil
}

type entry struct {
	Type      string          `json:"type"`
	Version   int             `json:"version"`
	ID        string          `json:"id"`
	ParentID  string          `json:"parentId"`
	Name      string          `json:"name"`
	Cwd       string          `json:"cwd"`
	Timestamp json.RawMessage `json:"timestamp"`
	Message   struct {
		Role      string          `json:"role"`
		Content   json.RawMessage `json:"content"`
		Timestamp json.RawMessage `json:"timestamp"`
		Command   string          `json:"command"`
		Output    string          `json:"output"`
	} `json:"message"`
}

// activeEntries 只保留最后一条叶子的父链，尽量不把废弃分支收进归档。
// 旧格式没有 id 时，按文件顺序返回。
func activeEntries(entries []entry) []entry {
	header := entries[0]
	if header.Version < 2 {
		return entries
	}
	byID := map[string]int{}
	lastID := ""
	for i, item := range entries[1:] {
		if item.ID != "" {
			byID[item.ID] = i + 1
			lastID = item.ID
		}
	}
	if lastID == "" {
		return entries
	}

	var indexes []int
	seen := map[string]bool{}
	for id := lastID; id != ""; {
		if seen[id] {
			break
		}
		seen[id] = true
		index, ok := byID[id]
		if !ok {
			break
		}
		indexes = append(indexes, index)
		id = entries[index].ParentID
	}
	out := make([]entry, 0, len(indexes)+1)
	out = append(out, header)
	for i := len(indexes) - 1; i >= 0; i-- {
		out = append(out, entries[indexes[i]])
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

func roleOf(role string) string {
	switch role {
	case "user":
		return channel.RoleUser
	case "toolResult", "bashExecution":
		return channel.RoleTool
	case "branchSummary", "compactionSummary":
		return channel.RoleSystem
	default:
		return channel.RoleAssistant
	}
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

func depth(root, path string) int {
	rel, err := filepath.Rel(root, path)
	if err != nil || rel == "." {
		return 0
	}
	return len(strings.Split(filepath.ToSlash(rel), "/"))
}
