// Package claudejsonl 解析 Claude 风格的 JSONL 会话文件。
//
// 两套行格式都走这里：CommandCode、Factory 是 type=message（角色在 message.role），
// Claude Code、Qoder 是 type=user / assistant；消息内容都是 content 块数组。
// 抽出来共用，避免每家复制一份解析。
package claudejsonl

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

// maxLineBytes 是单行的上限（AC-07）。超了就当作文件坏了，跳过它。
// 会话里可能有整段工具输出，所以上限给得比普通文件宽。
const maxLineBytes = 16 << 20

// maxFiles 是单次发现的上限，防止异常目录把内存与界面拖垮。
const maxFiles = 20000

// Seed 是适配器在解析前已知的身份与元数据。
type Seed struct {
	// ChannelID 是渠道标识。
	ChannelID string
	// ID 是会话标识；文件里没有 session 行时用它兜底。
	ID string
	// Title 是优先使用的标题；为空时从第一条用户发言取。
	Title string
}

// DetectProjectDir 发现「项目目录 / 会话文件」两层布局里的 .jsonl。
//
// skip 用来排除同目录的 sidecar 文件（如断点快照）。
func DetectProjectDir(root string, skip func(string) bool) channel.DetectResult {
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
		dir := filepath.Join(root, project.Name())
		items, err := os.ReadDir(dir)
		if err != nil {
			// 单个项目目录读不了，不影响其余项目。
			continue
		}
		for _, item := range items {
			if item.IsDir() || !strings.HasSuffix(strings.ToLower(item.Name()), ".jsonl") {
				continue
			}
			if skip != nil && skip(item.Name()) {
				continue
			}
			if len(files) >= maxFiles {
				return channel.DetectResult{
					Status: channel.StatusFailed,
					Root:   root,
					Err:    fmt.Errorf("%s 下的会话超过 %d 个上限", root, maxFiles),
				}
			}
			info, err := item.Info()
			if err != nil {
				continue
			}
			files = append(files, channel.SourceFile{
				Path:    filepath.Join(dir, item.Name()),
				Size:    info.Size(),
				ModTime: info.ModTime(),
			})
		}
	}

	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	return channel.DetectResult{Status: channel.StatusAvailable, Root: root, Files: files}
}

// Parse 把一个 Claude 风格会话文件解析成统一会话模型。
//
// 任何一行读不懂就整份跳过（返回错误）——宁可漏一次，也不归档半份对话。
func Parse(sf channel.SourceFile, seed Seed) (channel.Session, error) {
	f, err := os.Open(sf.Path)
	if err != nil {
		return channel.Session{}, fmt.Errorf("打开 %s: %w", sf.Path, err)
	}
	defer func() { _ = f.Close() }()

	session := channel.Session{
		Channel:    seed.ChannelID,
		SourceFile: sf.Path,
	}
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

		if session.ID == "" && row.SessionID != "" {
			session.ID = row.SessionID
		}
		if session.ProjectPath == "" && row.Cwd != "" {
			session.ProjectPath = row.Cwd
		}
		if session.StartedAt.IsZero() && !row.Timestamp.IsZero() {
			session.StartedAt = row.Timestamp.Time
		}

		switch row.Type {
		case "session", "session_start":
			if row.ID != "" {
				session.ID = row.ID
			}

		case "message":
			switch row.Message.Role {
			case channel.RoleUser:
				msgs, texts := row.userMessages()
				session.Messages = append(session.Messages, msgs...)
				titleCandidates = append(titleCandidates, texts...)
			case channel.RoleAssistant:
				if m, ok := row.assistantMessage(); ok {
					session.Messages = append(session.Messages, m)
				}
			}

		case "user":
			msgs, texts := row.userMessages()
			session.Messages = append(session.Messages, msgs...)
			titleCandidates = append(titleCandidates, texts...)

		case "assistant":
			if m, ok := row.assistantMessage(); ok {
				session.Messages = append(session.Messages, m)
			}

		default:
			// todo_state / compaction_state 等不是对话内容，跳过。
		}
	}
	if err := scanner.Err(); err != nil {
		return channel.Session{}, fmt.Errorf("读 %s: %w", sf.Path, err)
	}

	if session.ID == "" {
		session.ID = seed.ID
	}
	if session.ID == "" {
		session.ID = strings.TrimSuffix(filepath.Base(sf.Path), filepath.Ext(sf.Path))
	}
	if seed.Title != "" {
		session.Title = channel.Shorten(seed.Title, channel.TitleLimit)
	}
	if session.Title == "" {
		session.Title = channel.TitleFrom(titleCandidates, sf.Path, session.ID)
	}
	// 一条消息都没有 = 空会话（只开过、没说话）：不是错，由编排层跳过、不计失败（I-039）。
	return session, nil
}

// Probe 只判断这个文件里有没有会话消息（I-056）。
func Probe(sf channel.SourceFile) (bool, error) {
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
		switch row.Type {
		case "message":
			switch row.Message.Role {
			case channel.RoleUser:
				if msgs, _ := row.userMessages(); len(msgs) > 0 {
					return true, nil
				}
			case channel.RoleAssistant:
				if _, ok := row.assistantMessage(); ok {
					return true, nil
				}
			}
		case "user":
			if msgs, _ := row.userMessages(); len(msgs) > 0 {
				return true, nil
			}
		case "assistant":
			if _, ok := row.assistantMessage(); ok {
				return true, nil
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return false, fmt.Errorf("读 %s: %w", sf.Path, err)
	}
	return false, nil
}

// flexTime 接受三种写法：RFC3339 字符串、epoch 毫秒数、缺失或 null。
//
// Qoder 的元数据行把 timestamp 写成毫秒数，消息行才写成字符串；
// 不能因为元数据行的写法就让整份会话被判失败。
type flexTime struct{ time.Time }

func (t *flexTime) UnmarshalJSON(raw []byte) error {
	s := strings.TrimSpace(string(raw))
	if s == "" || s == "null" || s == `""` {
		t.Time = time.Time{}
		return nil
	}
	if s[0] == '"' {
		var v string
		if err := json.Unmarshal(raw, &v); err != nil {
			return err
		}
		parsed, err := time.Parse(time.RFC3339Nano, v)
		if err != nil {
			return err
		}
		t.Time = parsed
		return nil
	}
	var ms int64
	if err := json.Unmarshal(raw, &ms); err != nil {
		return err
	}
	t.Time = time.UnixMilli(ms).UTC()
	return nil
}

// rawLine 是 JSONL 里一行中我们关心的字段。
type rawLine struct {
	Type      string   `json:"type"`
	ID        string   `json:"id"`
	SessionID string   `json:"sessionId"`
	Timestamp flexTime `json:"timestamp"`
	Cwd       string   `json:"cwd"`
	Message   struct {
		Role    string          `json:"role"`
		Content json.RawMessage `json:"content"`
	} `json:"message"`
}

// block 是 message.content 数组里的一块。
type block struct {
	Type    string          `json:"type"`
	Text    string          `json:"text"`
	Name    string          `json:"name"`
	Input   json.RawMessage `json:"input"`
	Content json.RawMessage `json:"content"`
}

// userMessages 返回这一行产生的消息，以及可当标题的纯文本。
//
// user 行有两种形态：content 是字符串，或 content 是数组
// （text 块＝用户发言，tool_result 块＝工具回执）。
func (l rawLine) userMessages() ([]channel.Message, []string) {
	text, blocks, err := decodeContent(l.Message.Content)
	if err != nil {
		return nil, nil
	}

	var messages []channel.Message
	var texts []string

	if blocks == nil {
		if strings.TrimSpace(text) != "" {
			messages = append(messages, channel.Message{Role: channel.RoleUser, Time: l.Timestamp.Time, Text: text})
			texts = append(texts, text)
		}
		return messages, texts
	}

	var userText strings.Builder
	for _, b := range blocks {
		switch b.Type {
		case "text":
			if userText.Len() > 0 {
				userText.WriteString("\n")
			}
			userText.WriteString(b.Text)
		case "tool_result":
			messages = append(messages, channel.Message{
				Role: channel.RoleTool,
				Time: l.Timestamp.Time,
				Text: flattenText(b.Content),
			})
		}
	}
	if t := strings.TrimSpace(userText.String()); t != "" {
		messages = append([]channel.Message{{Role: channel.RoleUser, Time: l.Timestamp.Time, Text: t}}, messages...)
		texts = append(texts, t)
	}
	return messages, texts
}

// assistantMessage 把一行 assistant 变成一条消息。没有内容时返回 false。
func (l rawLine) assistantMessage() (channel.Message, bool) {
	text, blocks, err := decodeContent(l.Message.Content)
	if err != nil {
		return channel.Message{}, false
	}

	msg := channel.Message{Role: channel.RoleAssistant, Time: l.Timestamp.Time}
	if blocks == nil {
		msg.Text = text
	} else {
		var sb strings.Builder
		for _, b := range blocks {
			switch b.Type {
			case "text":
				if sb.Len() > 0 {
					sb.WriteString("\n")
				}
				sb.WriteString(b.Text)
			case "tool_use":
				msg.Tools = append(msg.Tools, channel.ToolCall{
					Name:  b.Name,
					Input: string(b.Input),
				})
			default:
				// thinking 等内部块不属于对话内容，跳过。
			}
		}
		msg.Text = sb.String()
	}

	if strings.TrimSpace(msg.Text) == "" && len(msg.Tools) == 0 {
		return channel.Message{}, false
	}
	return msg, true
}

// decodeContent 分辨 content 是字符串还是块数组。
func decodeContent(raw json.RawMessage) (string, []block, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return "", nil, nil
	}
	if trimmed[0] == '"' {
		var s string
		if err := json.Unmarshal(trimmed, &s); err != nil {
			return "", nil, err
		}
		return s, nil, nil
	}
	var blocks []block
	if err := json.Unmarshal(trimmed, &blocks); err != nil {
		return "", nil, err
	}
	return "", blocks, nil
}

// flattenText 把 tool_result 的 content 拍平成一段文本。
func flattenText(raw json.RawMessage) string {
	text, blocks, err := decodeContent(raw)
	if err != nil {
		return ""
	}
	if blocks == nil {
		return text
	}
	var sb strings.Builder
	for _, b := range blocks {
		switch b.Type {
		case "text":
			sb.WriteString(b.Text)
		case "image":
			sb.WriteString("（图片）")
		}
	}
	return sb.String()
}
