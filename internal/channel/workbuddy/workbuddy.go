// Package workbuddy 实现对 WorkBuddy（腾讯 CodeBuddy 桌面端）会话记录的适配器。
//
// 数据来源：<用户目录>/.workbuddy-ai/projects/<项目>/<会话 ID>.jsonl，一个文件一次会话。
// 每行一个 JSON 对象，type 字段区分行种类：
//
//   - session-meta：会话元信息，没有正文；
//   - message：一条消息，role 为 user / assistant，正文在 content 数组里
//     （块类型 input_text / output_text）；
//   - ai-title：模型给这个会话起的标题；
//   - function_call：一次工具调用（name + arguments），parentId 指向它属于的那条消息；
//   - function_call_result：工具回执，callId 与前面的调用对得上；
//   - reasoning / file-history-snapshot：内部记录，不算对话内容。
//
// 认路别认错：同目录下的 workbuddy.db 里只有会话清单（标题、项目、时间），**没有正文**；
// 正文只在这些 .jsonl 里。所以这个渠道认的是 projects 目录。
package workbuddy

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
	ChannelID = "workbuddy"
	Name      = "WorkBuddy"
)

const (
	// dataRelPath 是会话正文在用户目录下的相对位置。
	dataRelPath = ".workbuddy-ai/projects"
	// maxLineBytes 是单行的上限（AC-07：读取渠道文件必设上限）。
	// 超过就当作文件坏了，跳过它——不猜、不截断。
	maxLineBytes = 8 << 20
)

// Adapter 实现 channel.Adapter。
type Adapter struct {
	// Root 是数据根目录；留空时取 <用户目录>/.workbuddy-ai/projects。
	// 给测试与自定义安装位置留的口子。
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
		dir := filepath.Join(root, project.Name())
		items, err := os.ReadDir(dir)
		if err != nil {
			// 单个项目目录读不了，不影响其余项目。
			continue
		}
		for _, item := range items {
			if item.IsDir() || !strings.HasSuffix(item.Name(), ".jsonl") {
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

	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	return channel.DetectResult{Status: channel.StatusAvailable, Root: root, Files: files}
}

// Parse 把一个会话文件解析成统一会话模型。
//
// 任何一行读不懂就整份跳过（返回错误）——宁可漏一次，也不归档半份对话。
func (a Adapter) Parse(sf channel.SourceFile) (channel.Session, error) {
	f, err := os.Open(sf.Path)
	if err != nil {
		return channel.Session{}, fmt.Errorf("打开 %s: %w", sf.Path, err)
	}
	defer func() { _ = f.Close() }()

	session := channel.Session{Channel: ChannelID, SourceFile: sf.Path}

	// 工具调用与回执分两行，靠 ID 挂回去：
	// 消息按 id 记账，调用按 callId 记账，回执按 callId 找到那个调用补上输出。
	byMessage := map[string]int{}
	byCall := map[string]toolRef{}
	var titleCandidates []string
	aiTitle := ""

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), maxLineBytes)

	for lineNo := 1; scanner.Scan(); lineNo++ {
		raw := bytes.TrimSpace(scanner.Bytes())
		if len(raw) == 0 || raw[0] == '#' {
			// 空行与夹具里的注释行跳过（真数据没有注释）。
			continue
		}

		var row rawLine
		if err := json.Unmarshal(raw, &row); err != nil {
			return channel.Session{}, fmt.Errorf("%s 第 %d 行解析失败: %w", sf.Path, lineNo, err)
		}

		if session.ID == "" {
			session.ID = row.SessionID
		}
		if session.ProjectPath == "" && row.Cwd != "" {
			session.ProjectPath = row.Cwd
		}
		if session.StartedAt.IsZero() && row.Timestamp > 0 {
			session.StartedAt = time.UnixMilli(row.Timestamp)
		}

		switch row.Type {
		case "message":
			text := row.messageText()
			if strings.TrimSpace(text) == "" {
				continue
			}
			role := channel.RoleAssistant
			if row.Role == channel.RoleUser {
				role = channel.RoleUser
			}
			session.Messages = append(session.Messages, channel.Message{
				Role: role,
				Time: unixMilli(row.Timestamp),
				Text: text,
			})
			if row.ID != "" {
				byMessage[row.ID] = len(session.Messages) - 1
			}
			if role == channel.RoleUser {
				titleCandidates = append(titleCandidates, text)
			}
		case "function_call":
			call := channel.ToolCall{Name: row.Name, Input: argumentsText(row.Arguments)}
			target, ok := byMessage[row.ParentID]
			if !ok {
				// 归属的那条消息不在（例如只有工具调用的轮次）：单起一条助手消息装它。
				session.Messages = append(session.Messages, channel.Message{
					Role: channel.RoleAssistant,
					Time: unixMilli(row.Timestamp),
				})
				target = len(session.Messages) - 1
			}
			session.Messages[target].Tools = append(session.Messages[target].Tools, call)
			if row.CallID != "" {
				byCall[row.CallID] = toolRef{msg: target, index: len(session.Messages[target].Tools) - 1}
			}
		case "function_call_result":
			text := row.outputText()
			ref, ok := byCall[row.CallID]
			if !ok {
				// 没找到对应的调用（数据缺半截）：留成一条工具结果，别丢。
				session.Messages = append(session.Messages, channel.Message{
					Role: channel.RoleTool,
					Time: unixMilli(row.Timestamp),
					Text: text,
				})
				continue
			}
			session.Messages[ref.msg].Tools[ref.index].Output = text
		case "ai-title":
			if aiTitle == "" {
				aiTitle = strings.TrimSpace(row.AITitle)
			}
		default:
			// session-meta / reasoning / file-history-snapshot 等不是对话内容，跳过。
		}
	}
	if err := scanner.Err(); err != nil {
		return channel.Session{}, fmt.Errorf("读 %s: %w", sf.Path, err)
	}

	// 标题：模型起的优先，其次取用户发言，最后退到文件名 / 会话 ID。
	if aiTitle != "" {
		session.Title = channel.Shorten(aiTitle, channel.TitleLimit)
	}
	if session.Title == "" {
		session.Title = channel.TitleFrom(titleCandidates, sf.Path, session.ID)
	}
	// 一条消息都没有 = 空会话（打开过没说话）：不是错，由编排层跳过、不计失败（I-039）。
	return session, nil
}

// Probe 只判断这个文件里有没有会话消息（I-056）。
//
// 与 Parse 同一套判据：普通消息要有正文；工具调用 / 工具结果本身也算一条消息。
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
		if len(raw) == 0 || raw[0] == '#' {
			continue
		}

		var row rawLine
		if err := json.Unmarshal(raw, &row); err != nil {
			return false, fmt.Errorf("%s 第 %d 行解析失败: %w", sf.Path, lineNo, err)
		}
		switch row.Type {
		case "message":
			if strings.TrimSpace(row.messageText()) != "" {
				return true, nil
			}
		case "function_call", "function_call_result":
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
	return filepath.Join(home, filepath.FromSlash(dataRelPath)), nil
}

// toolRef 指向某条消息里的某次工具调用。
type toolRef struct {
	msg   int
	index int
}

// rawLine 是 JSONL 里一行中我们关心的字段。
type rawLine struct {
	Type      string          `json:"type"`
	ID        string          `json:"id"`
	ParentID  string          `json:"parentId"`
	SessionID string          `json:"sessionId"`
	Cwd       string          `json:"cwd"`
	Timestamp int64           `json:"timestamp"` // 毫秒
	Role      string          `json:"role"`
	Content   json.RawMessage `json:"content"`
	AITitle   string          `json:"aiTitle"`
	CallID    string          `json:"callId"`
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
	Output    json.RawMessage `json:"output"`
}

// block 是 message.content 数组里的一块。
type block struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// messageText 把消息的正文块拼起来。
func (l rawLine) messageText() string {
	var blocks []block
	if err := json.Unmarshal(bytes.TrimSpace(l.Content), &blocks); err != nil {
		// content 是字符串（少数形态）时也认一下。
		var s string
		if err := json.Unmarshal(bytes.TrimSpace(l.Content), &s); err == nil {
			return strings.TrimSpace(s)
		}
		return ""
	}

	var sb strings.Builder
	for _, b := range blocks {
		switch b.Type {
		case "input_text", "output_text", "text":
			writeText(&sb, b.Text)
		default:
			// 图片等非文本块跳过。
		}
	}
	return sb.String()
}

// outputText 取工具回执的正文。
func (l rawLine) outputText() string {
	raw := bytes.TrimSpace(l.Output)
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return ""
	}
	var out struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal(raw, &out); err == nil && out.Text != "" {
		return out.Text
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	return string(raw)
}

// argumentsText 取工具调用入参：真数据里是「装着 JSON 的字符串」，也认直接给对象的情况。
func argumentsText(raw json.RawMessage) string {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return ""
	}
	if trimmed[0] == '"' {
		var s string
		if err := json.Unmarshal(trimmed, &s); err == nil {
			return s
		}
	}
	return string(trimmed)
}

// writeText 把一段正文并进去，段与段之间空一行。
func writeText(sb *strings.Builder, body string) {
	body = strings.TrimSpace(body)
	if body == "" {
		return
	}
	if sb.Len() > 0 {
		sb.WriteString("\n\n")
	}
	sb.WriteString(body)
}

// unixMilli 把毫秒时间戳转成时间；非正数返回零值。
func unixMilli(ms int64) time.Time {
	if ms <= 0 {
		return time.Time{}
	}
	return time.UnixMilli(ms)
}
