// Package opencode 实现对 OpenCode 会话记录的适配器。
//
// 数据来源：`<用户目录>/.local/share/opencode/opencode.db`（Windows 与 Linux 都在这条相对路径下）。
// 这个库前后有两套表，本机**两套都有数据**，所以两套都认，一次把两边的会话都收上：
//
//   - 新格式：`session_v2` + `session_message`——一条消息一行，`type` 是 user / assistant，
//     `data` 是 JSON：用户消息里是 `text`，助手消息里是 `content[]`（每段的 `type` 为
//     text / tool / reasoning / step-start / step-finish / compaction 等）；
//   - 旧格式：`session` + `message` + `part`——消息与片段分两张表，片段在 `data` 里同样是
//     text / tool / reasoning 那几种。
//
// 会话的 `directory` 就是项目路径，`title` 是标题，时间戳是毫秒。
//
// 与文件型渠道的差别同 Cursor：**一个库装很多次会话**，所以一个「源文件」指的是一条会话行
// （路径是 `<库>#<表名>:<会话 ID>` 这种虚拟路径，表名让 Parse 知道该去哪张表找）。
// 增量判断用「消息条数 + 会话的更新时间」——两者都随会话活动变，不必拿整库的 mtime。
package opencode

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
	ChannelID = "opencode"
	Name      = "OpenCode"
)

const (
	// dbRelPath 是库在用户目录下的相对路径。
	dbRelPath = ".local/share/opencode/opencode.db"
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

// source 说明一套表长什么样。
type source struct {
	// table 是会话表名；它也会写进虚拟路径的键里，Parse 靠它认路。
	table    string
	messages string // 消息表
	pieces   string // 片段表；新格式为空（片段就在消息里）
}

// knownSources 是认得的表结构，新格式在前。
func knownSources() []source {
	return []source{
		{table: "session_v2", messages: "session_message"},
		{table: "session", messages: "message", pieces: "part"},
	}
}

// Detect 发现库里的每一条会话（两套表都扫）。
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

	var files []channel.SourceFile
	for _, src := range knownSources() {
		usable, err := usable(db, src)
		if err != nil {
			return channel.DetectResult{Status: channel.StatusFailed, Root: path, Err: err}
		}
		if !usable {
			continue
		}

		rows, err := db.All(fmt.Sprintf(`select s.id as id, s.time_updated as updated,
				count(m.id) as messages
			from %s s left join %s m on m.session_id = s.id
			group by s.id order by s.time_updated desc limit ?`, src.table, src.messages), maxSessions+1)
		if err != nil {
			return channel.DetectResult{Status: channel.StatusFailed, Root: path, Err: err}
		}
		if len(rows) > maxSessions {
			return channel.DetectResult{
				Status: channel.StatusFailed, Root: path,
				Err: fmt.Errorf("%s 的 %s 超过 %d 条会话上限，先别一次全读", path, src.table, maxSessions),
			}
		}

		for _, row := range rows {
			id := row.Text("id")
			if id == "" {
				continue
			}
			files = append(files, channel.SourceFile{
				Path: sqlite.VirtualPath(path, src.table+":"+id),
				// 这个渠道拿不到「这个会话有多少字节」；用消息条数当大小——
				// 它同样随会话活动变，够增量判断用。
				Size:    asInt64(row["messages"]),
				ModTime: millis(asInt64(row["updated"])),
			})
		}
	}

	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	return channel.DetectResult{Status: channel.StatusAvailable, Root: path, Files: files}
}

// Parse 把一条会话解析成统一会话模型。
func (a Adapter) Parse(sf channel.SourceFile) (channel.Session, error) {
	path, key, ok := sqlite.SplitVirtualPath(sf.Path)
	if !ok {
		return channel.Session{}, fmt.Errorf("%s 不是「库#会话」这种路径", sf.Path)
	}
	table, id, ok := strings.Cut(key, ":")
	if !ok || table == "" || id == "" {
		return channel.Session{}, fmt.Errorf("%s 的键 %q 里没有表名或会话 ID", sf.Path, key)
	}

	db, err := sqlite.Open(path)
	if err != nil {
		return channel.Session{}, fmt.Errorf("打开 %s: %w", path, err)
	}
	defer db.Close()

	src, err := sourceFor(db, table)
	if err != nil {
		return channel.Session{}, err
	}
	if src == nil {
		return channel.Session{}, fmt.Errorf("%s 里没有 %q 这张会话表", path, table)
	}

	rows, err := db.All(fmt.Sprintf(
		`select id, title, directory, time_created from %s where id = ?`, src.table), id)
	if err != nil {
		return channel.Session{}, err
	}
	if len(rows) == 0 {
		return channel.Session{}, fmt.Errorf("%s 的 %s 里找不到会话 %s", path, src.table, id)
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

	messages, err := readMessages(db, *src, id)
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

// usable 判断这套表在这库里能不能用：会话表与消息表都在才算。
func usable(db *sqlite.DB, src source) (bool, error) {
	for _, table := range []string{src.table, src.messages} {
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

// sourceFor 按表名找出这套表的完整形状；认不得时返回 nil。
func sourceFor(db *sqlite.DB, table string) (*source, error) {
	for _, src := range knownSources() {
		if src.table != table {
			continue
		}
		ok, err := usable(db, src)
		if err != nil {
			return nil, err
		}
		if !ok {
			return nil, nil
		}
		picked := src
		return &picked, nil
	}
	return nil, nil
}

// readMessages 读一个会话的全部消息。
//
// 新格式一条消息一行（正文与片段都在 data 里）；旧格式消息与片段分两张表，
// 得按 message_id 把片段捞回来。两种最后都走 applyPart 同一套拼装。
// Probe 只判断这条会话有没有会话消息（I-056）。
//
// 与 readMessages 用同一套「哪条算消息」的判据（有正文或有工具调用），
// 但读到第一条就返回，不把整条会话读完。
func (a Adapter) Probe(sf channel.SourceFile) (bool, error) {
	path, key, ok := sqlite.SplitVirtualPath(sf.Path)
	if !ok {
		return false, fmt.Errorf("%s 不是「库#会话」这种路径", sf.Path)
	}
	table, id, ok := strings.Cut(key, ":")
	if !ok || table == "" || id == "" {
		return false, fmt.Errorf("%s 的键 %q 里没有表名或会话 ID", sf.Path, key)
	}

	db, err := sqlite.Open(path)
	if err != nil {
		return false, fmt.Errorf("打开 %s: %w", path, err)
	}
	defer db.Close()

	src, err := sourceFor(db, table)
	if err != nil {
		return false, err
	}
	if src == nil {
		return false, fmt.Errorf("%s 里没有 %q 这张会话表", path, table)
	}
	return hasAnyMessage(db, *src, id)
}

func hasAnyMessage(db *sqlite.DB, src source, sessionID string) (bool, error) {
	if src.pieces == "" {
		rows, err := db.All(fmt.Sprintf(
			`select type, data from %s where session_id = ? order by seq limit ?`,
			src.messages), sessionID, maxMessages)
		if err != nil {
			return false, err
		}
		for _, row := range rows {
			var payload struct {
				Text    string            `json:"text"`
				Content []json.RawMessage `json:"content"`
			}
			_ = json.Unmarshal([]byte(row.Text("data")), &payload)

			role := channel.RoleAssistant
			if row.Text("type") == "user" {
				role = channel.RoleUser
			}
			var msg channel.Message
			var text strings.Builder
			if role == channel.RoleUser {
				writeText(&text, payload.Text)
			}
			for _, raw := range payload.Content {
				applyPart(&msg, &text, raw)
			}
			if strings.TrimSpace(text.String()) != "" || len(msg.Tools) > 0 {
				return true, nil
			}
		}
		return false, nil
	}

	rows, err := db.All(fmt.Sprintf(
		`select id, data from %s where session_id = ? order by rowid limit ?`,
		src.messages), sessionID, maxMessages)
	if err != nil {
		return false, err
	}
	for _, row := range rows {
		pieces, err := db.All(fmt.Sprintf(
			`select data from %s where message_id = ? order by rowid limit ?`,
			src.pieces), row.Text("id"), maxMessages)
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

func readMessages(db *sqlite.DB, src source, sessionID string) ([]channel.Message, error) {
	if src.pieces == "" {
		rows, err := db.All(fmt.Sprintf(
			`select type, data, time_created from %s where session_id = ? order by seq limit ?`,
			src.messages), sessionID, maxMessages)
		if err != nil {
			return nil, err
		}

		out := make([]channel.Message, 0, len(rows))
		for _, row := range rows {
			var payload struct {
				Text    string            `json:"text"`
				Content []json.RawMessage `json:"content"`
			}
			_ = json.Unmarshal([]byte(row.Text("data")), &payload)

			role := channel.RoleAssistant
			if row.Text("type") == "user" {
				role = channel.RoleUser
			}
			msg := channel.Message{Role: role, Time: millis(asInt64(row["time_created"]))}

			var text strings.Builder
			if role == channel.RoleUser {
				writeText(&text, payload.Text)
			}
			for _, raw := range payload.Content {
				applyPart(&msg, &text, raw)
			}
			msg.Text = text.String()
			if msg.Text == "" && len(msg.Tools) == 0 {
				continue
			}
			out = append(out, msg)
		}
		return out, nil
	}

	// 旧格式：先拿消息，再把每个消息的片段捞回来。
	rows, err := db.All(fmt.Sprintf(
		`select id, data, time_created from %s where session_id = ? order by rowid limit ?`,
		src.messages), sessionID, maxMessages)
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

		pieces, err := db.All(fmt.Sprintf(
			`select data from %s where message_id = ? order by rowid limit ?`,
			src.pieces), row.Text("id"), maxMessages)
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
// 其余（reasoning / step-start / step-finish / compaction 等内部块）跳过。
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
		// 内部块不算对话内容。
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
