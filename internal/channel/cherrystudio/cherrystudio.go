// Package cherrystudio 实现 Cherry Studio v2 的 SQLite 适配器。
//
// 数据来源：<系统配置目录>/CherryStudio/Data/cherrystudio.sqlite（v2）。
// v1 的对话在 Electron IndexedDB（LevelDB）里，本适配器不读；v1 数据升级到 v2
// 时会由 Cherry Studio 自己迁进这个库（I-049）。
//
// 表结构：
//
//   - `topic`：一次会话一行——`name` 是标题、`active_node_id` 是当前分支的最后一条消息；
//   - `message`：树状（`parent_id` + `siblings_group_id`），`data.parts` 里是正文、
//     reasoning、工具调用与附件。
//
// 从 `active_node_id` 沿 `parent_id` 回溯，只收用户当前看到的那条分支：多模型
// 兄弟回复不重复归档。一个 topic 是一条虚拟源文件（`<库>#<topic ID>`）。
package cherrystudio

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Always1023/AgentMemory/internal/channel"
	"github.com/Always1023/AgentMemory/internal/sqlite"
)

// ChannelID 与 Name 是注册表用的身份。
const (
	ChannelID = "cherry-studio"
	Name      = "Cherry Studio"
)

const (
	// dbRelPath 是库在系统配置目录下的相对路径（Windows 即 %APPDATA%）。
	dbRelPath = "CherryStudio/Data/cherrystudio.sqlite"
	// maxTopics 是一次最多认多少条会话（AC-07）。
	maxTopics = 5000
	// maxMessages 是一个话题最多读多少条消息（AC-07）。
	maxMessages = 20000
)

// Adapter 实现 channel.Adapter。
type Adapter struct {
	// Root 是库文件路径；留空时取系统配置目录下的默认位置。
	Root string
}

// Detect 发现库里的每一个话题。
func (a Adapter) Detect() channel.DetectResult {
	path, err := a.dbPath()
	if err != nil {
		return channel.DetectResult{Status: channel.StatusFailed, Err: err}
	}
	if _, err := os.Stat(path); errors.Is(err, fs.ErrNotExist) {
		return channel.DetectResult{Status: channel.StatusNotInstalled}
	}

	db, err := sqlite.Open(path)
	if err != nil {
		return channel.DetectResult{Status: channel.StatusFailed, Root: path, Err: fmt.Errorf("打开 %s: %w", path, err)}
	}
	defer db.Close()

	ok, err := usable(db)
	if err != nil {
		return channel.DetectResult{Status: channel.StatusFailed, Root: path, Err: err}
	}
	if !ok {
		// 库在、但没有认得的表：不是失败，只是这个版本没会话。
		return channel.DetectResult{Status: channel.StatusAvailable, Root: path}
	}

	rows, err := db.All(`select t.id as id, count(m.id) as messages, t.updated_at as updated
		from topic t left join message m
			on m.topic_id = t.id and (m.deleted_at is null or m.deleted_at = 0)
		where t.deleted_at is null or t.deleted_at = 0
		group by t.id order by t.updated_at desc limit ?`, maxTopics+1)
	if err != nil {
		return channel.DetectResult{Status: channel.StatusFailed, Root: path, Err: err}
	}
	if len(rows) > maxTopics {
		return channel.DetectResult{
			Status: channel.StatusFailed, Root: path,
			Err: fmt.Errorf("%s 里的话题超过 %d 条上限，先别一次全读", path, maxTopics),
		}
	}

	files := make([]channel.SourceFile, 0, len(rows))
	for _, row := range rows {
		id := row.Text("id")
		if id == "" {
			continue
		}
		files = append(files, channel.SourceFile{
			Path: sqlite.VirtualPath(path, id),
			// 库型渠道没有「一份会话多少字节」；用消息条数当大小，够增量判断用。
			Size:    asInt64(row["messages"]),
			ModTime: millis(asInt64(row["updated"])),
		})
	}

	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	return channel.DetectResult{Status: channel.StatusAvailable, Root: path, Files: files}
}

// Parse 把一个话题解析成统一会话模型。
func (a Adapter) Parse(sf channel.SourceFile) (channel.Session, error) {
	path, id, ok := sqlite.SplitVirtualPath(sf.Path)
	if !ok {
		return channel.Session{}, fmt.Errorf("%s 不是「库#话题」这种路径", sf.Path)
	}

	db, err := sqlite.Open(path)
	if err != nil {
		return channel.Session{}, fmt.Errorf("打开 %s: %w", path, err)
	}
	defer db.Close()

	rows, err := db.All(`select id, name, active_node_id, created_at from topic
		where id = ? and (deleted_at is null or deleted_at = 0)`, id)
	if err != nil {
		return channel.Session{}, err
	}
	if len(rows) == 0 {
		return channel.Session{}, fmt.Errorf("%s 里找不到话题 %s", path, id)
	}
	row := rows[0]

	session := channel.Session{
		Channel:    ChannelID,
		ID:         id,
		SourceFile: sf.Path,
		StartedAt:  millis(asInt64(row["created_at"])),
	}
	if session.StartedAt.IsZero() {
		session.StartedAt = sf.ModTime
	}

	messages, err := readMessages(db, id, row.Text("active_node_id"))
	if err != nil {
		return channel.Session{}, err
	}
	session.Messages = messages

	var candidates []string
	for _, msg := range messages {
		if msg.Role == channel.RoleUser && strings.TrimSpace(msg.Text) != "" {
			candidates = append(candidates, msg.Text)
		}
	}
	if title := strings.TrimSpace(row.Text("name")); title != "" {
		session.Title = channel.Shorten(title, channel.TitleLimit)
	}
	if session.Title == "" {
		session.Title = channel.TitleFrom(candidates, "", session.ID)
	}
	return session, nil
}

// Probe 只判断这个话题有没有会话消息（I-056）。
//
// `searchable_text` 是 v2 为正文建的检索列；工具调用没有正文时再看一眼 data。
func (a Adapter) Probe(sf channel.SourceFile) (bool, error) {
	path, id, ok := sqlite.SplitVirtualPath(sf.Path)
	if !ok {
		return false, fmt.Errorf("%s 不是「库#话题」这种路径", sf.Path)
	}

	db, err := sqlite.Open(path)
	if err != nil {
		return false, fmt.Errorf("打开 %s: %w", path, err)
	}
	defer db.Close()

	rows, err := db.All(`select 1 as ok from message
		where topic_id = ? and (deleted_at is null or deleted_at = 0)
			and role in ('user', 'assistant')
			and (trim(searchable_text) <> '' or data like '%"dynamic-tool"%')
		limit 1`, id)
	if err != nil {
		return false, err
	}
	return len(rows) > 0, nil
}

// dbPath 返回库文件的位置。
func (a Adapter) dbPath() (string, error) {
	if a.Root != "" {
		return a.Root, nil
	}
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("找不到配置目录: %w", err)
	}
	return filepath.Join(dir, filepath.FromSlash(dbRelPath)), nil
}

// usable 判断这套表与列在库里全不全。
func usable(db *sqlite.DB) (bool, error) {
	topic, err := db.HasColumns("topic", "id", "name", "active_node_id", "created_at", "updated_at", "deleted_at")
	if err != nil {
		return false, err
	}
	message, err := db.HasColumns("message", "id", "parent_id", "topic_id", "role", "data", "searchable_text", "created_at", "deleted_at")
	if err != nil {
		return false, err
	}
	return topic && message, nil
}

// storedMessage 是 message 表里我们关心的几列。
type storedMessage struct {
	id        string
	parentID  string
	role      string
	data      string
	createdAt int64
}

// readMessages 读一个话题的消息，并按当前分支拼成对话。
func readMessages(db *sqlite.DB, topicID, activeNodeID string) ([]channel.Message, error) {
	rows, err := db.All(`select id, parent_id, role, data, created_at from message
		where topic_id = ? and (deleted_at is null or deleted_at = 0)
		order by created_at limit ?`, topicID, maxMessages)
	if err != nil {
		return nil, err
	}

	ordered := make([]storedMessage, 0, len(rows))
	byID := make(map[string]storedMessage, len(rows))
	for _, row := range rows {
		msg := storedMessage{
			id:        row.Text("id"),
			parentID:  row.Text("parent_id"),
			role:      row.Text("role"),
			data:      row.Text("data"),
			createdAt: asInt64(row["created_at"]),
		}
		if msg.id == "" {
			continue
		}
		ordered = append(ordered, msg)
		byID[msg.id] = msg
	}

	branch := followBranch(byID, activeNodeID)
	if len(branch) == 0 {
		// active_node_id 缺失或断了：退回按时间顺序，宁可多收也不漏。
		branch = ordered
	}

	out := make([]channel.Message, 0, len(branch))
	for _, msg := range branch {
		parsed, err := toMessages(msg)
		if err != nil {
			return nil, err
		}
		out = append(out, parsed...)
	}
	return out, nil
}

// followBranch 从当前节点沿 parent_id 回溯到根，再翻回时间顺序。
func followBranch(byID map[string]storedMessage, activeID string) []storedMessage {
	if activeID == "" {
		return nil
	}
	var chain []storedMessage
	seen := make(map[string]bool, len(byID))
	for id := activeID; id != ""; {
		msg, ok := byID[id]
		if !ok || seen[id] {
			break
		}
		seen[id] = true
		chain = append(chain, msg)
		if len(chain) >= maxMessages {
			break
		}
		id = msg.parentID
	}
	for i, j := 0, len(chain)-1; i < j; i, j = i+1, j-1 {
		chain[i], chain[j] = chain[j], chain[i]
	}
	return chain
}

// part 是 data.parts 里的一块。
type part struct {
	Type      string          `json:"type"`
	Text      string          `json:"text"`
	ToolName  string          `json:"toolName"`
	Input     json.RawMessage `json:"input"`
	Output    json.RawMessage `json:"output"`
	ErrorText string          `json:"errorText"`
	MediaType string          `json:"mediaType"`
	Filename  string          `json:"filename"`
	Data      *struct {
		Name    string `json:"name"`
		Message string `json:"message"`
	} `json:"data"`
}

// toMessages 把一条库记录变成 0..n 条统一消息。
func toMessages(row storedMessage) ([]channel.Message, error) {
	if row.role == "root" {
		// 树根是 v2 的内部节点，不是用户说的话。
		return nil, nil
	}

	var payload struct {
		Parts []part `json:"parts"`
	}
	if err := json.Unmarshal([]byte(row.data), &payload); err != nil {
		return nil, fmt.Errorf("消息 %s 的 data 解析失败: %w", row.id, err)
	}

	role := channel.RoleAssistant
	switch row.role {
	case channel.RoleUser:
		role = channel.RoleUser
	case channel.RoleAssistant:
		role = channel.RoleAssistant
	default:
		role = channel.RoleSystem
	}

	at := millis(row.createdAt)
	var text strings.Builder
	var tools []channel.ToolCall
	var toolResults []channel.Message
	var errors []string

	for _, p := range payload.Parts {
		switch p.Type {
		case "text":
			writeText(&text, p.Text)
		case "reasoning":
			// thinking 不进正文。
		case "dynamic-tool":
			tools = append(tools, channel.ToolCall{Name: p.ToolName, Input: normalizeJSON(p.Input)})
			if out := toolOutput(p); strings.TrimSpace(out) != "" {
				toolResults = append(toolResults, channel.Message{Role: channel.RoleTool, Time: at, Text: out})
			}
		case "file":
			writeText(&text, filePlaceholder(p))
		case "data-error":
			if p.Data != nil && strings.TrimSpace(p.Data.Message) != "" && p.Data.Message != "pause_placeholder" {
				errors = append(errors, p.Data.Message)
			}
		}
	}

	body := strings.TrimSpace(text.String())
	if body == "" && len(tools) == 0 && len(errors) > 0 {
		body = "（出错了：" + strings.Join(errors, "；") + "）"
	}
	if body == "" && len(tools) == 0 {
		return nil, nil
	}

	out := []channel.Message{{Role: role, Time: at, Text: body, Tools: tools}}
	return append(out, toolResults...), nil
}

// toolOutput 把工具结果拍成一段可读文本：错误原文 > content 里的 text > 原始 JSON。
func toolOutput(p part) string {
	if strings.TrimSpace(p.ErrorText) != "" {
		return p.ErrorText
	}
	raw := []byte(p.Output)
	if len(raw) == 0 {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	var out struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := json.Unmarshal(raw, &out); err == nil && len(out.Content) > 0 {
		var sb strings.Builder
		for _, c := range out.Content {
			if c.Type == "text" && c.Text != "" {
				if sb.Len() > 0 {
					sb.WriteString("\n")
				}
				sb.WriteString(c.Text)
			}
		}
		if sb.Len() > 0 {
			return sb.String()
		}
	}
	return string(raw)
}

// filePlaceholder 把附件块写成一句可读的话（统一模型没有独立的附件字段）。
func filePlaceholder(p part) string {
	label := "附件"
	if strings.HasPrefix(strings.ToLower(p.MediaType), "image/") {
		label = "图片"
	}
	if name := strings.TrimSpace(p.Filename); name != "" {
		return "（" + label + "：" + name + "）"
	}
	return "（" + label + "）"
}

// normalizeJSON 把可选的 JSON 入参转成字符串；null / 空给空串。
func normalizeJSON(raw json.RawMessage) string {
	s := strings.TrimSpace(string(raw))
	if s == "" || s == "null" {
		return ""
	}
	return s
}

// writeText 把一段正文并进 builder，段间留一个换行。
func writeText(b *strings.Builder, text string) {
	t := strings.TrimSpace(text)
	if t == "" {
		return
	}
	if b.Len() > 0 {
		b.WriteString("\n")
	}
	b.WriteString(t)
}

// millis 把 epoch 毫秒转成时间；0 给零值。
func millis(v int64) time.Time {
	if v <= 0 {
		return time.Time{}
	}
	return time.UnixMilli(v)
}

// asInt64 把驱动给的数值列收成 int64。
func asInt64(value any) int64 {
	switch v := value.(type) {
	case int64:
		return v
	case int:
		return int64(v)
	case float64:
		return int64(v)
	case string:
		n, _ := strconv.ParseInt(v, 10, 64)
		return n
	default:
		return 0
	}
}
