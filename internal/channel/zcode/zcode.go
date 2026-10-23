// Package zcode 实现对 ZCode 会话记录的适配器。
//
// 数据来源：`<用户目录>/.zcode/cli/db/db.sqlite`（本机 363 MB）。
// 注意别认错地方：`%APPDATA%\ZCode` 那 462 MB 全是 Electron / Chromium 的界面缓存
// （Cache、Code Cache、DIPS 那一套），不是会话；真数据在用户目录下的 `cli/db`。
//
// 表结构与 OpenCode 的旧格式是同一路子：
//
//   - `session`：一次会话一行——`directory` 是项目路径、`title` 是标题、时间是毫秒；
//   - `message`：一条消息一行——`data` 里是 `role`；
//   - `part`：消息的片段一行一行——`data.type` 为 text / tool / reasoning / step-start /
//     step-finish / file 等，正文块在 `text`，工具调用在 `tool` + `state.input` / `state.output`。
//
// 与文件型渠道的差别同其余库型渠道：**一个库装很多次会话**，所以一个「源文件」指的是一条会话行
// （路径是 `<库>#<会话 ID>` 这种虚拟路径）。增量判断用「消息条数 + 会话的 `time_updated`」。
//
// 拼装片段那几行（applyPart / writeText）与 opencode 那边是同源写法；等第三个同形状的渠道
// 落地（WorkBuddy）再抽出去共用——两个就先各留一份，免得为了共用绕出一层壳。
package zcode

import (
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
	"github.com/Always1023/AgentMemory/internal/sqlite"
)

// ChannelID 与 Name 是注册表用的身份。
const (
	ChannelID = "zcode"
	Name      = "ZCode"
)

const (
	// dbRelPath 是库在用户目录下的相对路径。
	dbRelPath = ".zcode/cli/db/db.sqlite"
	// maxSessions 是一次最多认多少条会话（AC-07）。
	maxSessions = 5000
	// maxMessages 是一个会话最多读多少条消息（AC-07）。
	maxMessages = 20000
)

// Adapter 实现 channel.Adapter。
type Adapter struct {
	// Root 是库文件路径；留空时取用户目录下的默认位置。
	Root string
}

// Detect 发现库里的每一条会话。
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

	rows, err := db.All(`select s.id as id, s.time_updated as updated, count(m.id) as messages
		from session s left join message m on m.session_id = s.id
		group by s.id order by s.time_updated desc limit ?`, maxSessions+1)
	if err != nil {
		return channel.DetectResult{Status: channel.StatusFailed, Root: path, Err: err}
	}
	if len(rows) > maxSessions {
		return channel.DetectResult{
			Status: channel.StatusFailed, Root: path,
			Err: fmt.Errorf("%s 里的会话超过 %d 条上限，先别一次全读", path, maxSessions),
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
			// 这个渠道拿不到「这个会话有多少字节」；用消息条数当大小——
			// 它同样随会话活动变，够增量判断用。
			Size:    asInt64(row["messages"]),
			ModTime: millis(asInt64(row["updated"])),
		})
	}

	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	return channel.DetectResult{Status: channel.StatusAvailable, Root: path, Files: files}
}

// Parse 把一条会话解析成统一会话模型。
func (a Adapter) Parse(sf channel.SourceFile) (channel.Session, error) {
	path, id, ok := sqlite.SplitVirtualPath(sf.Path)
	if !ok {
		return channel.Session{}, fmt.Errorf("%s 不是「库#会话」这种路径", sf.Path)
	}

	db, err := sqlite.Open(path)
	if err != nil {
		return channel.Session{}, fmt.Errorf("打开 %s: %w", path, err)
	}
	defer db.Close()

	rows, err := db.All(`select id, title, directory, time_created from session where id = ?`, id)
	if err != nil {
		return channel.Session{}, err
	}
	if len(rows) == 0 {
		return channel.Session{}, fmt.Errorf("%s 里找不到会话 %s", path, id)
	}
	row := rows[0]

	session := channel.Session{
		Channel:     ChannelID,
		ID:          id,
		ProjectPath: row.Text("directory"),
		SourceFile:  sf.Path,
		StartedAt:   millis(asInt64(row["time_created"])),
	}
	if session.StartedAt.IsZero() {
		session.StartedAt = sf.ModTime
	}

	messages, err := readMessages(db, id)
	if err != nil {
		return channel.Session{}, err
	}
	session.Messages = messages

	// 空会话（点开过没说话）不是错：返回 0 条消息，由编排层跳过（I-039）。
	var candidates []string
	for _, msg := range messages {
		if msg.Role == channel.RoleUser {
			candidates = append(candidates, msg.Text)
		}
	}
	if title := strings.TrimSpace(row.Text("title")); title != "" {
		session.Title = channel.Shorten(title, channel.TitleLimit)
	}
	if session.Title == "" {
		session.Title = channel.TitleFrom(candidates, "", session.ID)
	}
	return session, nil
}

// Probe 只判断这条会话有没有会话消息（I-056）。
//
// 不走完整 readMessages：只要有一条带正文或工具调用的消息就返回，
// 空会话才把消息与片段读完。
func (a Adapter) Probe(sf channel.SourceFile) (bool, error) {
	path, id, ok := sqlite.SplitVirtualPath(sf.Path)
	if !ok {
		return false, fmt.Errorf("%s 不是「库#会话」这种路径", sf.Path)
	}

	db, err := sqlite.Open(path)
	if err != nil {
		return false, fmt.Errorf("打开 %s: %w", path, err)
	}
	defer db.Close()

	rows, err := db.All(
		`select id from message where session_id = ? order by sequence limit ?`, id, maxMessages)
	if err != nil {
		return false, err
	}
	for _, row := range rows {
		pieces, err := db.All(
			`select data from part where message_id = ? order by sequence limit ?`,
			row.Text("id"), maxMessages)
		if err != nil {
			return false, err
		}
		var msg channel.Message
		var text strings.Builder
		for _, piece := range pieces {
			applyPart(&msg, &text, []byte(piece.Text("data")))
		}
		if strings.TrimSpace(text.String()) != "" || len(msg.Tools) > 0 {
			return true, nil
		}
	}
	return false, nil
}

// dbPath 返回库文件的位置。
func (a Adapter) dbPath() (string, error) {
	if a.Root != "" {
		return a.Root, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("找不到用户目录: %w", err)
	}
	return filepath.Join(home, filepath.FromSlash(dbRelPath)), nil
}

// usable 判断这套表在库里全不全。
func usable(db *sqlite.DB) (bool, error) {
	for _, table := range []string{"session", "message", "part"} {
		ok, err := db.HasTable(table)
		if err != nil {
			return false, err
		}
		if !ok {
			return false, nil
		}
	}
	return true, nil
}

// readMessages 读一个会话的全部消息：消息与片段分两张表，按 message_id 把片段捞回来。
func readMessages(db *sqlite.DB, sessionID string) ([]channel.Message, error) {
	rows, err := db.All(`select id, data, time_created from message
		where session_id = ? order by sequence limit ?`, sessionID, maxMessages)
	if err != nil {
		return nil, err
	}

	out := make([]channel.Message, 0, len(rows))
	for _, row := range rows {
		var payload struct {
			Role string `json:"role"`
		}
		_ = json.Unmarshal([]byte(row.Text("data")), &payload)

		role := channel.RoleAssistant
		if payload.Role == channel.RoleUser {
			role = channel.RoleUser
		}
		msg := channel.Message{Role: role, Time: millis(asInt64(row["time_created"]))}

		pieces, err := db.All(`select data from part where message_id = ? order by sequence limit ?`,
			row.Text("id"), maxMessages)
		if err != nil {
			return nil, err
		}
		var text strings.Builder
		for _, piece := range pieces {
			applyPart(&msg, &text, []byte(piece.Text("data")))
		}
		msg.Text = text.String()
		if msg.Text == "" && len(msg.Tools) == 0 {
			continue
		}
		out = append(out, msg)
	}
	return out, nil
}

// applyPart 把一段并进消息：正文块合起来，工具调用各留一条，
// 其余（reasoning / step-start / step-finish / file 等）跳过。
func applyPart(msg *channel.Message, text *strings.Builder, raw json.RawMessage) {
	var part struct {
		Type  string `json:"type"`
		Text  string `json:"text"`
		Tool  string `json:"tool"`
		State *struct {
			Input  json.RawMessage `json:"input"`
			Output string          `json:"output"`
		} `json:"state"`
	}
	if err := json.Unmarshal(raw, &part); err != nil {
		return
	}

	switch part.Type {
	case "text":
		writeText(text, part.Text)
	case "tool":
		call := channel.ToolCall{Name: part.Tool}
		if part.State != nil {
			if len(part.State.Input) > 0 {
				call.Input = string(part.State.Input)
			}
			call.Output = part.State.Output
		}
		msg.Tools = append(msg.Tools, call)
	default:
		// 内部块（reasoning / step-* / file 等）不算对话内容。
	}
}

// writeText 把一段正文并进去，段与段之间空一行。
func writeText(text *strings.Builder, body string) {
	body = strings.TrimSpace(body)
	if body == "" {
		return
	}
	if text.Len() > 0 {
		text.WriteString("\n\n")
	}
	text.WriteString(body)
}

// asInt64 把驱动给的值当整数用（列是 INTEGER 时给的就是 int64；文本形态也顺手认一下）。
func asInt64(value any) int64 {
	switch v := value.(type) {
	case int64:
		return v
	case int:
		return int64(v)
	case float64:
		return int64(v)
	case string:
		var n int64
		if _, err := fmt.Sscanf(v, "%d", &n); err == nil {
			return n
		}
	}
	return 0
}

// millis 把毫秒时间戳转成时间；非正数返回零值。
func millis(ms int64) time.Time {
	if ms <= 0 {
		return time.Time{}
	}
	return time.UnixMilli(ms)
}
