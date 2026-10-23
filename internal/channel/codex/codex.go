// Package codex 实现对 Codex CLI 会话记录的适配器。
//
// 数据来源：<用户目录>/.codex/sessions/<年>/<月>/<日>/rollout-*.jsonl，一个文件一次会话。
// 每行一个 JSON 对象；只有 type=response_item 的行里才有对话内容，
// session_meta 行给出会话 ID 与工作目录。
package codex

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
	ChannelID = "codex"
	Name      = "Codex CLI"
)

// maxLineBytes 是单行的上限（AC-07）。超了就当作文件坏了，跳过它。
// 这个渠道的记录里会有整段补丁内容，所以上限给得比别处宽。
const maxLineBytes = 16 << 20

// Adapter 实现 channel.Adapter。
type Adapter struct {
	// Root 是数据根目录；留空时取 <用户目录>/.codex/sessions。
	Root string
}

// Detect 递归发现全部会话文件。这个渠道按 年/月/日 分目录存放。
func (a Adapter) Detect() channel.DetectResult {
	root, err := a.root()
	if err != nil {
		return channel.DetectResult{Status: channel.StatusFailed, Err: err}
	}

	if info, err := os.Stat(root); errors.Is(err, fs.ErrNotExist) {
		return channel.DetectResult{Status: channel.StatusNotInstalled}
	} else if err != nil {
		return channel.DetectResult{Status: channel.StatusFailed, Root: root, Err: fmt.Errorf("看 %s: %w", root, err)}
	} else if !info.IsDir() {
		return channel.DetectResult{Status: channel.StatusFailed, Root: root, Err: fmt.Errorf("%s 不是目录", root)}
	}

	var files []channel.SourceFile
	walkErr := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			// 单个子目录读不了不影响其余。
			return nil
		}
		if d.IsDir() || !strings.HasSuffix(d.Name(), ".jsonl") {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		files = append(files, channel.SourceFile{Path: path, Size: info.Size(), ModTime: info.ModTime()})
		return nil
	})
	if walkErr != nil {
		return channel.DetectResult{Status: channel.StatusFailed, Root: root, Err: fmt.Errorf("遍历 %s: %w", root, walkErr)}
	}

	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	return channel.DetectResult{Status: channel.StatusAvailable, Root: root, Files: files}
}

// Parse 把一个会话文件解析成统一会话模型。
//
// 逐行流式读，不把整个文件读进内存（这个渠道的单个文件常见几十兆）。
// 任何一行读不懂就整份跳过——宁可漏一次，也不归档半份对话。
func (a Adapter) Parse(sf channel.SourceFile) (channel.Session, error) {
	f, err := os.Open(sf.Path)
	if err != nil {
		return channel.Session{}, fmt.Errorf("打开 %s: %w", sf.Path, err)
	}
	defer func() { _ = f.Close() }()

	session := channel.Session{Channel: ChannelID, SourceFile: sf.Path}
	var titleCandidates []string

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), maxLineBytes)

	for lineNo := 1; scanner.Scan(); lineNo++ {
		raw := bytes.TrimSpace(scanner.Bytes())
		if len(raw) == 0 {
			continue
		}

		var row rawLine
		if err := json.Unmarshal(raw, &row); err != nil {
			return channel.Session{}, fmt.Errorf("%s 第 %d 行解析失败: %w", sf.Path, lineNo, err)
		}
		if session.StartedAt.IsZero() && !row.Timestamp.IsZero() {
			session.StartedAt = row.Timestamp
		}

		switch row.Type {
		case "session_meta":
			var meta sessionMeta
			if err := json.Unmarshal(row.Payload, &meta); err != nil {
				continue
			}
			if session.ID == "" {
				session.ID = meta.SessionID
			}
			if session.ProjectPath == "" {
				session.ProjectPath = meta.Cwd
			}
			if session.StartedAt.IsZero() && !meta.Timestamp.IsZero() {
				session.StartedAt = meta.Timestamp
			}

		case "response_item":
			msgs, texts := responseItem(row.Payload, row.Timestamp)
			session.Messages = append(session.Messages, msgs...)
			titleCandidates = append(titleCandidates, texts...)

		default:
			// turn_context / event_msg 等不是对话内容，跳过。
		}
	}
	if err := scanner.Err(); err != nil {
		return channel.Session{}, fmt.Errorf("读 %s: %w", sf.Path, err)
	}

	// 标题先兜底：空会话也要有标题。
	session.Title = channel.TitleFrom(titleCandidates, sf.Path, session.ID)
	// 一条消息都没有 = 空会话（打开过没说话）：不是错，由编排层跳过、不计失败（I-039）。
	return session, nil
}

// Probe 只判断这个文件里有没有会话消息（I-056）。
//
// 与 Parse 用同一套「哪一行算消息」的判据；只要读到第一条消息就返回，
// 不继续读这个会话剩下的几十兆内容。
func (a Adapter) Probe(sf channel.SourceFile) (bool, error) {
	f, err := os.Open(sf.Path)
	if err != nil {
		return false, fmt.Errorf("打开 %s: %w", sf.Path, err)
	}
	defer func() { _ = f.Close() }()

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), maxLineBytes)

	for lineNo := 1; scanner.Scan(); lineNo++ {
		raw := bytes.TrimSpace(scanner.Bytes())
		if len(raw) == 0 {
			continue
		}

		var row rawLine
		if err := json.Unmarshal(raw, &row); err != nil {
			return false, fmt.Errorf("%s 第 %d 行解析失败: %w", sf.Path, lineNo, err)
		}
		if row.Type != "response_item" {
			continue
		}
		if msgs, _ := responseItem(row.Payload, row.Timestamp); len(msgs) > 0 {
			return true, nil
		}
	}
	if err := scanner.Err(); err != nil {
		return false, fmt.Errorf("读 %s: %w", sf.Path, err)
	}
	return false, nil
}

func (a Adapter) root() (string, error) {
	if a.Root != "" {
		return a.Root, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("取用户目录: %w", err)
	}
	return filepath.Join(home, ".codex", "sessions"), nil
}

// rawLine 是 JSONL 里一行中我们关心的字段。
type rawLine struct {
	Type      string          `json:"type"`
	Timestamp time.Time       `json:"timestamp"`
	Payload   json.RawMessage `json:"payload"`
}

type sessionMeta struct {
	SessionID string    `json:"session_id"`
	Cwd       string    `json:"cwd"`
	Timestamp time.Time `json:"timestamp"`
}

// payload 是 response_item 行里我们关心的字段。
type payload struct {
	Type string `json:"type"`
	// message
	Role    string `json:"role"`
	Content []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
	// function_call / custom_tool_call
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
	Input     string `json:"input"`
	CallID    string `json:"call_id"`
	// function_call_output / custom_tool_call_output
	Output string `json:"output"`
}

// responseItem 把一个 response_item 变成消息（可能 0 到 1 条）与标题候选。
func responseItem(rawPayload json.RawMessage, at time.Time) ([]channel.Message, []string) {
	var p payload
	if err := json.Unmarshal(rawPayload, &p); err != nil {
		return nil, nil
	}

	switch p.Type {
	case "message":
		var text strings.Builder
		for _, block := range p.Content {
			if strings.TrimSpace(block.Text) == "" {
				continue
			}
			if text.Len() > 0 {
				text.WriteString("\n")
			}
			text.WriteString(block.Text)
		}
		body := text.String()
		if strings.TrimSpace(body) == "" {
			return nil, nil
		}

		role := channel.RoleAssistant
		if p.Role == "user" {
			role = channel.RoleUser
		}
		msg := channel.Message{Role: role, Time: at, Text: body}
		if role == channel.RoleUser {
			return []channel.Message{msg}, []string{body}
		}
		return []channel.Message{msg}, nil

	case "function_call", "custom_tool_call":
		input := p.Arguments
		if input == "" {
			input = p.Input
		}
		return []channel.Message{{
			Role:  channel.RoleAssistant,
			Time:  at,
			Tools: []channel.ToolCall{{Name: p.Name, Input: input}},
		}}, nil

	case "function_call_output", "custom_tool_call_output":
		return []channel.Message{{Role: channel.RoleTool, Time: at, Text: p.Output}}, nil

	default:
		// reasoning 等内部块不属于对话内容，跳过。
		return nil, nil
	}
}
