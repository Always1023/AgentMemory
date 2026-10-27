// Package dsh 实现对 DeepSeek Harness（dsh）会话记录的适配器。
//
// 数据来源：<用户目录>/.dsh/sessions/<工作区>/session-<id>/session.v4.jsonl.zstd。
// 每个文件是一段会话：首行是会话头（id / cwd / createdAt），后面是事件行
// （type / seq / time / data）。消息分散在几类事件里：
//
//	agent/inbox/spliced  data.inserted[]  用户消息（可能一次插入多条）
//	assistant/message    data.message     助手消息（content 里含 text / reasoning / tool-call）
//	tool/result          data             工具结果（按 toolCallId 回到对应的 tool-call）
//	session/title        data.title       会话标题
//
// 正文是 zstd 压缩的；解压前先看文件大小，解压时也设上限（AC-07）。
package dsh

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/Always1023/AgentMemory/internal/channel"
	"github.com/klauspost/compress/zstd"
)

// ChannelID 与 Name 是注册表用的身份。
const (
	ChannelID = "dsh"
	Name      = "DeepSeek Harness"
)

const (
	// maxCompressed 是单个会话文件的大小上限；maxDecompressed 是解压后的上限。
	maxCompressed   = 64 << 20
	maxDecompressed = 256 << 20
	// maxLineBytes 是单行事件的上限（工具结果可能很长）。
	maxLineBytes = 8 << 20
)

// Adapter 实现 channel.Adapter。
type Adapter struct {
	// Root 是 sessions 目录；留空时取 <用户目录>/.dsh/sessions。
	Root string
}

// Detect 找出全部会话文件。
func (a Adapter) Detect() channel.DetectResult {
	root, err := a.root()
	if err != nil {
		return channel.DetectResult{Status: channel.StatusFailed, Err: err}
	}
	entries, err := os.ReadDir(root)
	if errors.Is(err, fs.ErrNotExist) {
		return channel.DetectResult{Status: channel.StatusNotInstalled}
	}
	if err != nil {
		return channel.DetectResult{Status: channel.StatusFailed, Root: root, Err: fmt.Errorf("读 %s: %w", root, err)}
	}

	var files []channel.SourceFile
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		workspace := filepath.Join(root, entry.Name())
		sessions, err := os.ReadDir(workspace)
		if err != nil {
			continue
		}
		for _, session := range sessions {
			if !session.IsDir() {
				continue
			}
			dir := filepath.Join(workspace, session.Name())
			items, err := os.ReadDir(dir)
			if err != nil {
				continue
			}
			for _, item := range items {
				if item.IsDir() || !isSessionFile(item.Name()) {
					continue
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
	}

	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	status := channel.StatusAvailable
	if len(files) == 0 {
		status = channel.StatusAvailable
	}
	return channel.DetectResult{Status: status, Root: root, Files: files}
}

// isSessionFile 认 session.v4.jsonl.zstd 这类文件名（以后版本号变大也认）。
func isSessionFile(name string) bool {
	lower := strings.ToLower(name)
	if !strings.HasPrefix(lower, "session.") {
		return false
	}
	return strings.Contains(lower, ".jsonl")
}

// Parse 解析一份 dsh 会话。
func (a Adapter) Parse(sf channel.SourceFile) (channel.Session, error) {
	if sf.Size > maxCompressed {
		return channel.Session{}, fmt.Errorf("%s 太大（%d 字节，上限 %d），先不解析", sf.Path, sf.Size, maxCompressed)
	}
	file, err := os.Open(sf.Path)
	if err != nil {
		return channel.Session{}, fmt.Errorf("读 %s: %w", sf.Path, err)
	}
	defer file.Close()

	session := channel.Session{Channel: ChannelID, SourceFile: sf.Path}
	var (
		title      string
		candidates []string
		callIndex  = map[string][2]int{} // toolCall id → 消息下标 + 工具下标
	)

	reader := io.Reader(file)
	if strings.HasSuffix(strings.ToLower(sf.Path), ".zstd") || strings.HasSuffix(strings.ToLower(sf.Path), ".zst") {
		dec, err := zstd.NewReader(file, zstd.WithDecoderMaxMemory(maxDecompressed))
		if err != nil {
			return channel.Session{}, fmt.Errorf("解压 %s: %w", sf.Path, err)
		}
		defer dec.Close()
		reader = io.LimitReader(dec, maxDecompressed)
	}

	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 0, 64<<10), maxLineBytes)
	for lineNo := 1; scanner.Scan(); lineNo++ {
		raw := bytes.TrimSpace(scanner.Bytes())
		if len(raw) == 0 {
			continue
		}
		var row line
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
			if session.StartedAt.IsZero() && row.CreatedAt > 0 {
				session.StartedAt = time.UnixMilli(row.CreatedAt)
			}
		case "session/title":
			var data struct {
				Title string `json:"title"`
			}
			if json.Unmarshal(row.Data, &data) == nil && strings.TrimSpace(data.Title) != "" {
				title = data.Title
			}
		case "agent/inbox/spliced":
			var data struct {
				Inserted []message `json:"inserted"`
			}
			if err := json.Unmarshal(row.Data, &data); err != nil {
				continue
			}
			for _, m := range data.Inserted {
				if msg, ok := buildMessage(m, row.at()); ok {
					candidates = append(candidates, msg.Text)
					session.Messages = append(session.Messages, msg)
				}
			}
		case "assistant/message":
			var data struct {
				Message message `json:"message"`
			}
			if err := json.Unmarshal(row.Data, &data); err != nil {
				continue
			}
			msg, ok := buildMessage(data.Message, row.at())
			if !ok {
				continue
			}
			session.Messages = append(session.Messages, msg)
			msgIdx := len(session.Messages) - 1
			toolIdx := 0
			for _, p := range data.Message.Content {
				if p.Type != "tool-call" {
					continue
				}
				if p.ID != "" {
					callIndex[p.ID] = [2]int{msgIdx, toolIdx}
				}
				toolIdx++
			}
		case "tool/result":
			var data message
			if err := json.Unmarshal(row.Data, &data); err != nil {
				continue
			}
			idx, ok := callIndex[data.ToolCallID]
			if !ok {
				continue
			}
			if idx[0] >= 0 && idx[0] < len(session.Messages) {
				tools := session.Messages[idx[0]].Tools
				if idx[1] >= 0 && idx[1] < len(tools) {
					tools[idx[1]].Output = textOf(data.Content)
				}
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return channel.Session{}, fmt.Errorf("读 %s: %w", sf.Path, err)
	}

	if session.ID == "" {
		session.ID = filepath.Base(filepath.Dir(sf.Path))
	}
	if session.StartedAt.IsZero() {
		session.StartedAt = sf.ModTime
	}
	switch {
	case strings.TrimSpace(title) != "":
		session.Title = channel.Shorten(title, channel.TitleLimit)
	default:
		session.Title = channel.TitleFrom(candidates, sf.Path, session.ID)
	}
	return session, nil
}

// line 是事件行。会话头（type=session）的 id / cwd / createdAt 在顶层。
type line struct {
	Type      string          `json:"type"`
	Seq       int             `json:"seq"`
	Time      int64           `json:"time"`
	ID        string          `json:"id"`
	Cwd       string          `json:"cwd"`
	CreatedAt int64           `json:"createdAt"`
	Data      json.RawMessage `json:"data"`
}

// at 把毫秒时间戳换成 time.Time；没有就返回零值。
func (l line) at() time.Time {
	if l.Time <= 0 {
		return time.Time{}
	}
	return time.UnixMilli(l.Time)
}

// message 同时覆盖三种形态：assistant/message 里的 message、inbox 里的 inserted 项、
// 以及 system/message 与 tool/result 的 data 本身。
type message struct {
	ID         string `json:"id"`
	Role       string `json:"role"`
	Content    []part `json:"content"`
	ToolCallID string `json:"toolCallId"`
}

// part 是消息内容的一段：正文、思考过程或工具调用。
type part struct {
	Type      string `json:"type"`
	Text      string `json:"text"`
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
	ID        string `json:"id"`
}

// buildMessage 把一条消息转成统一模型；没有正文也没有工具调用时丢弃。
func buildMessage(m message, when time.Time) (channel.Message, bool) {
	role := roleOf(m.Role)
	text := textOf(m.Content)
	tools := toolsOf(m.Content)
	if strings.TrimSpace(text) == "" && len(tools) == 0 {
		return channel.Message{}, false
	}
	return channel.Message{Role: role, Time: when, Text: text, Tools: tools}, true
}

// textOf 取正文段落，跳过思考过程（reasoning）与附件。
func textOf(parts []part) string {
	var b strings.Builder
	for _, p := range parts {
		if p.Type != "text" {
			continue
		}
		if b.Len() > 0 {
			b.WriteString("\n")
		}
		b.WriteString(p.Text)
	}
	return strings.TrimSpace(b.String())
}

// toolsOf 取工具调用；结果稍后由 tool/result 回填。
func toolsOf(parts []part) []channel.ToolCall {
	var out []channel.ToolCall
	for _, p := range parts {
		if p.Type != "tool-call" {
			continue
		}
		out = append(out, channel.ToolCall{Name: p.Name, Input: p.Arguments})
	}
	return out
}

// roleOf 把 dsh 的角色名映射到统一模型。
func roleOf(role string) string {
	switch strings.ToLower(role) {
	case channel.RoleUser:
		return channel.RoleUser
	case channel.RoleAssistant:
		return channel.RoleAssistant
	case channel.RoleTool:
		return channel.RoleTool
	default:
		return channel.RoleAssistant
	}
}

func (a Adapter) root() (string, error) {
	if a.Root != "" {
		return a.Root, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("取用户目录: %w", err)
	}
	return filepath.Join(home, ".dsh", "sessions"), nil
}
