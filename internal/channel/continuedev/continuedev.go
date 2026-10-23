// Package continuedev 实现对 Continue 会话记录的适配器。
//
// 数据来源：<用户目录>/.continue/sessions/<会话>.json，一个文件一次会话。
// 与另两个渠道不同，这里是**整份 JSON**，不是逐行 JSONL；
// 同目录下的 sessions.json 是索引，不是会话。
package continuedev

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/Always1023/AgentMemory/internal/channel"
)

// ChannelID 与 Name 是注册表用的身份。
const (
	ChannelID = "continue"
	Name      = "Continue"
)

const (
	// indexFileName 是同目录下的索引文件，不是会话。
	indexFileName = "sessions.json"
	// maxFileBytes 是单份会话的上限（AC-07）。整份读，所以先量大小。
	maxFileBytes = 64 << 20
)

// Adapter 实现 channel.Adapter。
type Adapter struct {
	// Root 是数据根目录；留空时取 <用户目录>/.continue/sessions。
	Root string
}

// Detect 发现全部会话文件。
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
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		if entry.Name() == indexFileName {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		files = append(files, channel.SourceFile{
			Path:    filepath.Join(root, entry.Name()),
			Size:    info.Size(),
			ModTime: info.ModTime(),
		})
	}

	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	return channel.DetectResult{Status: channel.StatusAvailable, Root: root, Files: files}
}

// Parse 把一份会话文件解析成统一会话模型。
//
// 整份是 JSON，所以「文件损坏」就是解析失败——整份跳过，不产出半份对话。
func (a Adapter) Parse(sf channel.SourceFile) (channel.Session, error) {
	if sf.Size > maxFileBytes {
		return channel.Session{}, fmt.Errorf("%s 有 %d 字节，超过上限 %d，跳过", sf.Path, sf.Size, maxFileBytes)
	}

	raw, err := os.ReadFile(sf.Path)
	if err != nil {
		return channel.Session{}, fmt.Errorf("读 %s: %w", sf.Path, err)
	}

	var f sessionFile
	if err := json.Unmarshal(raw, &f); err != nil {
		return channel.Session{}, fmt.Errorf("解析 %s: %w", sf.Path, err)
	}

	session := channel.Session{
		Channel:     ChannelID,
		ID:          f.SessionID,
		ProjectPath: decodeWorkspaceDir(f.WorkspaceDirectory),
		SourceFile:  sf.Path,
	}
	// 这个渠道的记录里**没有时间戳**。拿源文件的修改时间当会话时间的近似，
	// 好过零值——归档路径要用它，零值会让所有会话挤到同一天。
	session.StartedAt = sf.ModTime

	var candidates []string
	for _, item := range f.History {
		msg, ok := item.Message.toMessage()
		if !ok {
			continue
		}
		session.Messages = append(session.Messages, msg)
		if msg.Role == channel.RoleUser {
			candidates = append(candidates, msg.Text)
		}
	}
	if title := strings.TrimSpace(f.Title); title != "" {
		session.Title = channel.Shorten(title, channel.TitleLimit)
	}
	if session.Title == "" {
		session.Title = channel.TitleFrom(candidates, sf.Path, session.ID)
	}
	// 一条消息都没有 = 空会话（打开过没说话）：不是错，由编排层跳过、不计失败（I-039）。
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
	return filepath.Join(home, ".continue", "sessions"), nil
}

// sessionFile 是会话文件的顶层结构。
type sessionFile struct {
	SessionID          string        `json:"sessionId"`
	Title              string        `json:"title"`
	WorkspaceDirectory string        `json:"workspaceDirectory"`
	History            []historyItem `json:"history"`
}

// historyItem 是 history 数组里的一项。这一层还带着 contextItems、promptLogs 等，
// 都不是对话内容，不解析。
type historyItem struct {
	Message message `json:"message"`
}

// message 是一条消息。
type message struct {
	ID      string          `json:"id"`
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
}

// toMessage 把一条历史记录变成统一消息。没内容时返回 false。
func (m message) toMessage() (channel.Message, bool) {
	text, err := contentText(m.Content)
	if err != nil || strings.TrimSpace(text) == "" {
		return channel.Message{}, false
	}
	return channel.Message{Role: roleOf(m.Role), Text: text}, true
}

// roleOf 把渠道的角色名映到统一角色的四种之一。
func roleOf(role string) string {
	switch role {
	case "user":
		return channel.RoleUser
	case "system":
		return channel.RoleSystem
	case "tool":
		return channel.RoleTool
	default:
		// 这个渠道的助手角色就是 "assistant"；其余未见的角色归到助手，
		// 不因为一个没见过的名字把整条消息丢掉。
		return channel.RoleAssistant
	}
}

// contentText 把 content 拍平成文本。
//
// 这里有两种形态：字符串（助手），或块数组（用户，块类型为 text）。
func contentText(raw json.RawMessage) (string, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return "", nil
	}
	if trimmed[0] == '"' {
		var s string
		if err := json.Unmarshal(trimmed, &s); err != nil {
			return "", err
		}
		return s, nil
	}

	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal(trimmed, &blocks); err != nil {
		return "", err
	}
	var sb strings.Builder
	for _, b := range blocks {
		if b.Type != "text" || strings.TrimSpace(b.Text) == "" {
			continue
		}
		if sb.Len() > 0 {
			sb.WriteString("\n")
		}
		sb.WriteString(b.Text)
	}
	return sb.String(), nil
}

// decodeWorkspaceDir 把 workspaceDirectory 还原成可读路径。
//
// 形态是 file:///d%3A/Coding/ReactDeom —— 既是 URL，又做了百分号编码。
// 不是 file:// 形态的原样返回（有些版本直接写普通路径）。
func decodeWorkspaceDir(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}

	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme != "file" {
		return raw
	}
	path := parsed.Path
	if path == "" {
		path = parsed.Opaque
	}
	// /d:/Coding/x → d:/Coding/x
	// 只有「斜杠 + 盘符 + 冒号」这一种情况才去掉前导斜杠；
	// 别的绝对路径（如 /home/me/proj）那个斜杠是路径的一部分，不能动。
	if len(path) >= 3 && path[0] == '/' && path[2] == ':' {
		path = path[1:]
	}
	if path == "" {
		return ""
	}
	return filepath.FromSlash(path)
}
