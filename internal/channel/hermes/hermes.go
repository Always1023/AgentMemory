// Package hermes 实现 Hermes 会话记录的适配器。
//
// Hermes 有两套会话来源：`state.db` 里的 sessions / messages 表，以及
// `sessions/*.jsonl`（部分版本也写 `.json`）。同一个会话 ID 同时出现在两处时，
// SQLite 版本优先；这与参考实现 cc-switch 的合并规则一致。
//
// 数据根目录依次取 HERMES_HOME、Windows 的 %LOCALAPPDATA%/hermes、
// 其他平台的 ~/.hermes。库只读打开，绝不修改渠道数据。
package hermes

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
	"strconv"
	"strings"
	"time"

	"github.com/Always1023/AgentMemory/internal/channel"
	"github.com/Always1023/AgentMemory/internal/sqlite"
)

// ChannelID 与 Name 是注册表用的身份。
const (
	ChannelID = "hermes"
	Name      = "Hermes"
)

const (
	maxSessions  = 5000
	maxMessages  = 20000
	maxLineBytes = 8 << 20
)

// Adapter 实现 channel.Adapter。
type Adapter struct {
	// Root 是 Hermes 数据目录；留空时按 HERMES_HOME 与平台默认值查找。
	Root string
}

// Detect 合并 SQLite 与 JSONL 会话；同一 ID 以 SQLite 为准。
func (a Adapter) Detect() channel.DetectResult {
	root, err := a.root()
	if err != nil {
		return channel.DetectResult{Status: channel.StatusFailed, Err: err}
	}
	if _, err := os.Stat(root); errors.Is(err, fs.ErrNotExist) {
		return channel.DetectResult{Status: channel.StatusNotInstalled}
	} else if err != nil {
		return channel.DetectResult{Status: channel.StatusFailed, Root: root, Err: fmt.Errorf("读 %s: %w", root, err)}
	}

	sqliteFiles, err := detectSQLite(filepath.Join(root, "state.db"))
	if err != nil {
		return channel.DetectResult{Status: channel.StatusFailed, Root: root, Err: err}
	}
	jsonlFiles, err := detectJSONL(filepath.Join(root, "sessions"))
	if err != nil {
		return channel.DetectResult{Status: channel.StatusFailed, Root: root, Err: err}
	}

	seen := make(map[string]bool, len(sqliteFiles))
	files := make([]channel.SourceFile, 0, len(sqliteFiles)+len(jsonlFiles))
	for _, file := range sqliteFiles {
		seen[sessionIDFromPath(file.Path)] = true
		files = append(files, file)
	}
	for _, file := range jsonlFiles {
		if seen[jsonlSessionID(file.Path)] {
			continue
		}
		files = append(files, file)
	}

	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	return channel.DetectResult{Status: channel.StatusAvailable, Root: root, Files: files}
}

// Parse 解析 SQLite 虚拟行或 JSONL 会话文件。
func (a Adapter) Parse(sf channel.SourceFile) (channel.Session, error) {
	if dbPath, id, ok := sqlite.SplitVirtualPath(sf.Path); ok {
		return parseSQLiteSession(dbPath, id, sf)
	}
	return parseJSONLSession(sf)
}

func detectSQLite(path string) ([]channel.SourceFile, error) {
	if _, err := os.Stat(path); errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	} else if err != nil {
		return nil, fmt.Errorf("读 %s: %w", path, err)
	}

	db, err := sqlite.Open(path)
	if err != nil {
		return nil, fmt.Errorf("打开 %s: %w", path, err)
	}
	defer db.Close()

	ok, err := db.HasTable("sessions")
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, nil
	}

	rows, err := db.All(`select id, started_at, ended_at, last_activity_at,
		(select count(*) from messages where messages.session_id = sessions.id) as message_count
		from sessions order by rowid desc limit ?`, maxSessions+1)
	if err != nil {
		return nil, err
	}
	if len(rows) > maxSessions {
		return nil, fmt.Errorf("%s 里的会话超过 %d 条上限，先别一次全读", path, maxSessions)
	}

	files := make([]channel.SourceFile, 0, len(rows))
	for _, row := range rows {
		id := row.Text("id")
		if id == "" {
			continue
		}
		files = append(files, channel.SourceFile{
			Path:    sqlite.VirtualPath(path, id),
			Size:    asInt64(row["message_count"]),
			ModTime: firstTime(row, "last_activity_at", "ended_at", "started_at"),
		})
	}
	return files, nil
}

func parseSQLiteSession(dbPath, id string, sf channel.SourceFile) (channel.Session, error) {
	db, err := sqlite.Open(dbPath)
	if err != nil {
		return channel.Session{}, fmt.Errorf("打开 %s: %w", dbPath, err)
	}
	defer db.Close()

	columns, err := db.Columns("sessions")
	if err != nil {
		return channel.Session{}, err
	}
	column := func(name string) string {
		for _, have := range columns {
			if have == name {
				return name
			}
		}
		return "null"
	}
	rows, err := db.All(fmt.Sprintf(`select id, %s as title, %s as cwd, %s as directory,
		%s as started_at, %s as created_at from sessions where id = ?`,
		column("title"), column("cwd"), column("directory"), column("started_at"), column("created_at")), id)
	if err != nil {
		return channel.Session{}, err
	}
	if len(rows) == 0 {
		return channel.Session{}, fmt.Errorf("%s 里找不到会话 %s", dbPath, id)
	}
	row := rows[0]
	session := channel.Session{
		Channel:     ChannelID,
		ID:          id,
		ProjectPath: firstText(row, "cwd", "directory"),
		SourceFile:  sf.Path,
		StartedAt:   firstTime(row, "started_at", "created_at"),
	}
	if session.StartedAt.IsZero() {
		session.StartedAt = sf.ModTime
	}

	messages, err := readSQLiteMessages(db, id)
	if err != nil {
		return channel.Session{}, err
	}
	session.Messages = messages
	setTitle(&session, firstText(row, "title"))
	return session, nil
}

func readSQLiteMessages(db *sqlite.DB, sessionID string) ([]channel.Message, error) {
	hasTable, err := db.HasTable("messages")
	if err != nil {
		return nil, err
	}
	if !hasTable {
		return nil, nil
	}

	columns, err := db.Columns("messages")
	if err != nil {
		return nil, err
	}
	has := func(name string) bool { return contains(columns, name) }
	if !has("role") || !has("content") {
		return nil, nil
	}

	timestamp := "null"
	for _, name := range []string{"timestamp", "created_at"} {
		if has(name) {
			timestamp = name
			break
		}
	}
	column := func(name string) string {
		if has(name) {
			return name
		}
		return "null"
	}
	filter := ""
	if has("active") && has("compacted") {
		filter = " and (active = 1 or compacted = 1)"
	} else if has("active") {
		filter = " and active = 1"
	}

	query := fmt.Sprintf(`select id, role, content, %s as timestamp, %s as tool_call_id,
		%s as tool_calls, %s as tool_name, %s as reasoning from messages
		where session_id = ?%s order by id limit ?`,
		timestamp, column("tool_call_id"), column("tool_calls"), column("tool_name"),
		column("reasoning"), filter)
	rows, err := db.All(query, sessionID, maxMessages)
	if err != nil {
		return nil, err
	}

	messages := make([]channel.Message, 0, len(rows))
	for _, row := range rows {
		role := row.Text("role")
		text := decodeContent(row.Text("content"))
		if role == channel.RoleAssistant {
			if reasoning := strings.TrimSpace(row.Text("reasoning")); reasoning != "" {
				text = strings.TrimSpace(reasoning + "\n\n" + text)
			}
		}
		tools := parseToolCalls(row.Text("tool_calls"))
		if role == channel.RoleTool {
			if name := row.Text("tool_name"); name != "" && strings.TrimSpace(text) != "" {
				tools = append(tools, channel.ToolCall{Name: name, Output: text})
			}
		}
		if strings.TrimSpace(text) == "" && len(tools) == 0 {
			continue
		}
		messages = append(messages, channel.Message{
			Role:  normalizeRole(role),
			Time:  parseEpoch(row["timestamp"]),
			Text:  text,
			Tools: tools,
		})
	}
	return messages, nil
}

func detectJSONL(dir string) ([]channel.SourceFile, error) {
	entries, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("读 %s: %w", dir, err)
	}

	files := make([]channel.SourceFile, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		ext := strings.ToLower(filepath.Ext(entry.Name()))
		if ext != ".jsonl" && ext != ".json" {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		files = append(files, channel.SourceFile{
			Path:    filepath.Join(dir, entry.Name()),
			Size:    info.Size(),
			ModTime: info.ModTime(),
		})
	}
	return files, nil
}

func parseJSONLSession(sf channel.SourceFile) (channel.Session, error) {
	file, err := os.Open(sf.Path)
	if err != nil {
		return channel.Session{}, fmt.Errorf("读 %s: %w", sf.Path, err)
	}
	defer file.Close()

	session := channel.Session{Channel: ChannelID, SourceFile: sf.Path}
	var explicitTitle string
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 64<<10), maxLineBytes)
	for lineNo := 1; scanner.Scan(); lineNo++ {
		raw := bytes.TrimSpace(scanner.Bytes())
		if len(raw) == 0 {
			continue
		}
		var record map[string]json.RawMessage
		if err := json.Unmarshal(raw, &record); err != nil {
			return channel.Session{}, fmt.Errorf("解析 %s 第 %d 行: %w", sf.Path, lineNo, err)
		}
		if recordType(record) == "session" || recordType(record) == "init" {
			if session.ID == "" {
				session.ID = rawText(record, "id", "sessionId")
			}
			if explicitTitle == "" {
				explicitTitle = rawText(record, "title")
			}
			if session.ProjectPath == "" {
				session.ProjectPath = rawText(record, "cwd", "directory")
			}
			if session.StartedAt.IsZero() {
				session.StartedAt = parseRawTime(record["timestamp"])
			}
		}

		message, ok := decodeJSONLMessage(record)
		if !ok {
			continue
		}
		if session.StartedAt.IsZero() {
			session.StartedAt = message.Time
		}
		session.Messages = append(session.Messages, message)
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
	setTitle(&session, explicitTitle)
	return session, nil
}

func decodeJSONLMessage(record map[string]json.RawMessage) (channel.Message, bool) {
	nested := recordType(record) == "message"
	message := record
	if nested {
		decoded := make(map[string]json.RawMessage)
		if err := json.Unmarshal(record["message"], &decoded); err != nil || decoded == nil {
			return channel.Message{}, false
		}
		message = decoded
	}
	role := rawText(message, "role")
	if role == "" {
		return channel.Message{}, false
	}
	text := decodeContent(rawText(message, "content"))
	if role == channel.RoleAssistant {
		if reasoning := strings.TrimSpace(rawText(message, "reasoning")); reasoning != "" {
			text = strings.TrimSpace(reasoning + "\n\n" + text)
		}
	}
	tools := parseToolCalls(rawJSONText(message, "tool_calls"))
	if role == channel.RoleTool {
		if name := rawText(message, "tool_name"); name != "" && strings.TrimSpace(text) != "" {
			tools = append(tools, channel.ToolCall{Name: name, Output: text})
		}
	}
	if strings.TrimSpace(text) == "" && len(tools) == 0 {
		return channel.Message{}, false
	}
	when := parseRawTime(message["timestamp"])
	if when.IsZero() {
		when = parseRawTime(message["ts"])
	}
	if when.IsZero() && nested {
		when = parseRawTime(record["timestamp"])
	}
	if when.IsZero() && nested {
		when = parseRawTime(record["ts"])
	}
	return channel.Message{Role: normalizeRole(role), Time: when, Text: text, Tools: tools}, true
}

func parseToolCalls(raw string) []channel.ToolCall {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	var calls []struct {
		Function struct {
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
		} `json:"function"`
	}
	if err := json.Unmarshal([]byte(raw), &calls); err != nil {
		return nil
	}
	out := make([]channel.ToolCall, 0, len(calls))
	for _, call := range calls {
		input := strings.TrimSpace(string(call.Function.Arguments))
		var text string
		if err := json.Unmarshal(call.Function.Arguments, &text); err == nil {
			input = text
		}
		out = append(out, channel.ToolCall{Name: call.Function.Name, Input: input})
	}
	return out
}

func decodeContent(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	raw = strings.TrimPrefix(raw, "\x00json:")
	var value any
	if err := json.Unmarshal([]byte(raw), &value); err != nil {
		return raw
	}
	switch v := value.(type) {
	case string:
		return v
	case []any:
		var parts []string
		for _, item := range v {
			if block, ok := item.(map[string]any); ok {
				if text, ok := block["text"].(string); ok && strings.TrimSpace(text) != "" {
					parts = append(parts, text)
				}
			}
		}
		return strings.Join(parts, "\n")
	default:
		return raw
	}
}

func setTitle(session *channel.Session, explicit string) {
	var candidates []string
	for _, message := range session.Messages {
		if message.Role == channel.RoleUser {
			candidates = append(candidates, message.Text)
		}
	}
	if strings.TrimSpace(explicit) != "" {
		session.Title = channel.Shorten(explicit, channel.TitleLimit)
	}
	if session.Title == "" {
		session.Title = channel.TitleFrom(candidates, session.SourceFile, session.ID)
	}
}

func (a Adapter) root() (string, error) {
	if a.Root != "" {
		return a.Root, nil
	}
	if value := strings.TrimSpace(os.Getenv("HERMES_HOME")); value != "" {
		return value, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("取用户目录: %w", err)
	}
	if local := strings.TrimSpace(os.Getenv("LOCALAPPDATA")); local != "" {
		return filepath.Join(local, "hermes"), nil
	}
	return filepath.Join(home, ".hermes"), nil
}

func sessionIDFromPath(path string) string {
	_, id, ok := sqlite.SplitVirtualPath(path)
	if ok {
		return id
	}
	return strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
}

// jsonlSessionID 读文件头里的会话 ID；读不到才退回文件名。
// 去重必须用同一个逻辑 ID，否则同名会话会被收两遍。
func jsonlSessionID(path string) string {
	file, err := os.Open(path)
	if err != nil {
		return strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 64<<10), maxLineBytes)
	for line := 0; line < 30 && scanner.Scan(); line++ {
		var record map[string]json.RawMessage
		if json.Unmarshal(bytes.TrimSpace(scanner.Bytes()), &record) != nil {
			continue
		}
		if recordType(record) == "session" || recordType(record) == "init" {
			if id := rawText(record, "id", "sessionId"); id != "" {
				return id
			}
		}
	}
	return strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
}

func recordType(record map[string]json.RawMessage) string { return rawText(record, "type") }

func rawText(record map[string]json.RawMessage, names ...string) string {
	for _, name := range names {
		raw, ok := record[name]
		if !ok {
			continue
		}
		var value string
		if err := json.Unmarshal(raw, &value); err == nil {
			return value
		}
	}
	return ""
}

func rawJSONText(record map[string]json.RawMessage, names ...string) string {
	for _, name := range names {
		if raw, ok := record[name]; ok && len(raw) > 0 {
			return string(raw)
		}
	}
	return ""
}

func parseRawTime(raw json.RawMessage) time.Time {
	if len(raw) == 0 {
		return time.Time{}
	}
	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		if parsed, err := time.Parse(time.RFC3339Nano, text); err == nil {
			return parsed
		}
		if seconds, err := strconv.ParseFloat(strings.TrimSpace(text), 64); err == nil {
			return epoch(seconds)
		}
	}
	var number float64
	if err := json.Unmarshal(raw, &number); err == nil {
		return epoch(number)
	}
	return time.Time{}
}

func parseEpoch(value any) time.Time {
	switch v := value.(type) {
	case int64:
		return epoch(float64(v))
	case float64:
		return epoch(v)
	case string:
		seconds, err := strconv.ParseFloat(strings.TrimSpace(v), 64)
		if err == nil {
			return epoch(seconds)
		}
		if parsed, err := time.Parse(time.RFC3339Nano, v); err == nil {
			return parsed
		}
	}
	return time.Time{}
}

func epoch(value float64) time.Time {
	if value <= 0 {
		return time.Time{}
	}
	if value > 1e12 {
		return time.UnixMilli(int64(value))
	}
	return time.UnixMilli(int64(value * 1000))
}

func firstTime(row sqlite.Row, names ...string) time.Time {
	for _, name := range names {
		if when := parseEpoch(row[name]); !when.IsZero() {
			return when
		}
	}
	return time.Time{}
}

func firstText(row sqlite.Row, names ...string) string {
	for _, name := range names {
		if text := strings.TrimSpace(row.Text(name)); text != "" {
			return text
		}
	}
	return ""
}

func contains(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}

func normalizeRole(role string) string {
	switch role {
	case channel.RoleUser, channel.RoleAssistant, channel.RoleSystem, channel.RoleTool:
		return role
	case "toolResult", "tool_result":
		return channel.RoleTool
	default:
		return channel.RoleAssistant
	}
}

func asInt64(value any) int64 {
	switch v := value.(type) {
	case int64:
		return v
	case float64:
		return int64(v)
	case string:
		var n int64
		_, _ = fmt.Sscanf(v, "%d", &n)
		return n
	default:
		return 0
	}
}
