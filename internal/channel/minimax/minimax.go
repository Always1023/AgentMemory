// Package minimax 实现 MiniMax Code（cc-switch 中的 mcode）会话适配器。
//
// 数据来源：<数据目录>/v2/sqlite/runtime-state.sqlite。数据目录依次取
// MINIMAX_DATA_DIR、MAVIS_DATA_DIR、~/.minimax。
//
// 会话列表只取可见的根会话；消息优先读 local_runtime_message_rows，
// 未迁移的旧库则从 local_runtime_messages.display_messages_json 读取。
package minimax

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
	ChannelID = "minimax"
	Name      = "MiniMax Code"
)

const maxSessions = 5000

// Adapter 实现 channel.Adapter。
type Adapter struct {
	// Root 是 runtime-state.sqlite 的路径，也接受数据目录。
	Root string
}

// Detect 发现可见的根会话。
func (a Adapter) Detect() channel.DetectResult {
	path, err := a.dbPath()
	if err != nil {
		return channel.DetectResult{Status: channel.StatusFailed, Err: err}
	}
	if _, err := os.Stat(path); errors.Is(err, fs.ErrNotExist) {
		return channel.DetectResult{Status: channel.StatusNotInstalled}
	} else if err != nil {
		return channel.DetectResult{Status: channel.StatusFailed, Err: fmt.Errorf("读 %s: %w", path, err)}
	}

	db, err := sqlite.Open(path)
	if err != nil {
		return channel.DetectResult{Status: channel.StatusFailed, Root: path, Err: fmt.Errorf("打开 %s: %w", path, err)}
	}
	defer db.Close()

	ok, err := db.HasTable("local_runtime_sessions")
	if err != nil {
		return channel.DetectResult{Status: channel.StatusFailed, Root: path, Err: err}
	}
	if !ok {
		return channel.DetectResult{Status: channel.StatusAvailable, Root: path}
	}

	hasMigrations, err := db.HasTable("local_runtime_message_row_migrations")
	if err != nil {
		return channel.DetectResult{Status: channel.StatusFailed, Root: path, Err: err}
	}
	hasRows, err := db.HasTable("local_runtime_message_rows")
	if err != nil {
		return channel.DetectResult{Status: channel.StatusFailed, Root: path, Err: err}
	}
	countExpr := `(select count(*) from local_runtime_messages l where l.session_id = s.session_id)`
	if hasMigrations && hasRows {
		countExpr = `case when exists (select 1 from local_runtime_message_row_migrations m where m.session_id = s.session_id)
		  then (select count(*) from local_runtime_message_rows r where r.session_id = s.session_id)
		  else ` + countExpr + `
		end`
	}
	query := fmt.Sprintf(`select s.session_id as id, s.updated_at_ms as updated,
		%s as message_count
		from local_runtime_sessions s
		where s.visibility <> 'hidden' and s.archived = 0
		  and s.parent_session_id is null
		  and s.session_kind not in ('peek', 'channel', 'cron')
		order by s.updated_at_ms desc limit ?`, countExpr)
	rows, err := db.All(query, maxSessions+1)
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
			Path:    sqlite.VirtualPath(path, id),
			Size:    asInt64(row["message_count"]),
			ModTime: millis(asInt64(row["updated"])),
		})
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	return channel.DetectResult{Status: channel.StatusAvailable, Root: path, Files: files}
}

// Parse 解析一条 MiniMax Code 会话。
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

	rows, err := db.All(`select session_id, title, workspace_dir, created_at_ms, updated_at_ms
		from local_runtime_sessions where session_id = ?`, id)
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
		Title:       channel.Shorten(row.Text("title"), channel.TitleLimit),
		ProjectPath: row.Text("workspace_dir"),
		SourceFile:  sf.Path,
		StartedAt:   millis(asInt64(row["created_at_ms"])),
	}
	if session.StartedAt.IsZero() {
		session.StartedAt = sf.ModTime
	}

	messages, err := readMessages(db, id)
	if err != nil {
		return channel.Session{}, err
	}
	session.Messages = messages
	if session.Title == "" {
		var candidates []string
		for _, message := range messages {
			if message.Role == channel.RoleUser {
				candidates = append(candidates, message.Text)
			}
		}
		session.Title = channel.TitleFrom(candidates, "", session.ID)
	}
	return session, nil
}

func readMessages(db *sqlite.DB, id string) ([]channel.Message, error) {
	migrated, err := db.HasTable("local_runtime_message_row_migrations")
	if err != nil {
		return nil, err
	}
	if migrated {
		rows, err := db.All(`select 1 from local_runtime_message_row_migrations where session_id = ?`, id)
		if err != nil {
			return nil, err
		}
		migrated = len(rows) > 0
	}

	if migrated {
		return readMigrated(db, id)
	}
	return readLegacy(db, id)
}

func readMigrated(db *sqlite.DB, id string) ([]channel.Message, error) {
	rows, err := db.All(`select role, data_json, created_at_ms from local_runtime_message_rows
		where session_id = ? and role in ('user', 'assistant') order by id`, id)
	if err != nil {
		return nil, err
	}
	messages := make([]channel.Message, 0, len(rows))
	for _, row := range rows {
		var payload map[string]json.RawMessage
		_ = json.Unmarshal([]byte(row.Text("data_json")), &payload)
		text := extractContent(payload["msg_content"])
		if strings.TrimSpace(text) == "" {
			continue
		}
		when := millis(asInt64(row["created_at_ms"]))
		if when.IsZero() {
			when = firstTime(payload, "timestamp", "created_at")
		}
		messages = append(messages, channel.Message{Role: normalizeRole(row.Text("role")), Time: when, Text: text})
	}
	return messages, nil
}

func readLegacy(db *sqlite.DB, id string) ([]channel.Message, error) {
	rows, err := db.All(`select display_messages_json from local_runtime_messages where session_id = ?`, id)
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, nil
	}
	var items []map[string]json.RawMessage
	if err := json.Unmarshal([]byte(rows[0].Text("display_messages_json")), &items); err != nil {
		return nil, fmt.Errorf("解析会话 %s 的 display_messages_json: %w", id, err)
	}
	messages := make([]channel.Message, 0, len(items))
	for _, item := range items {
		role := rawText(item, "role")
		if role != channel.RoleUser && role != channel.RoleAssistant {
			continue
		}
		text := extractContent(item["msg_content"])
		if strings.TrimSpace(text) == "" {
			continue
		}
		messages = append(messages, channel.Message{
			Role: normalizeRole(role), Time: firstTime(item, "timestamp", "created_at"), Text: text,
		})
	}
	return messages, nil
}

func extractContent(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		return text
	}
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal(raw, &blocks); err == nil {
		var parts []string
		for _, block := range blocks {
			if strings.TrimSpace(block.Text) != "" {
				parts = append(parts, block.Text)
			}
		}
		return strings.Join(parts, "\n")
	}
	var value any
	if err := json.Unmarshal(raw, &value); err == nil {
		if object, ok := value.(map[string]any); ok {
			if text, ok := object["text"].(string); ok {
				return text
			}
		}
	}
	return string(raw)
}

func (a Adapter) dbPath() (string, error) {
	if a.Root != "" {
		if info, err := os.Stat(a.Root); err == nil && info.IsDir() {
			return filepath.Join(a.Root, "v2", "sqlite", "runtime-state.sqlite"), nil
		}
		return a.Root, nil
	}
	dataDir := strings.TrimSpace(os.Getenv("MINIMAX_DATA_DIR"))
	if dataDir == "" {
		dataDir = strings.TrimSpace(os.Getenv("MAVIS_DATA_DIR"))
	}
	if dataDir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("取用户目录: %w", err)
		}
		dataDir = filepath.Join(home, ".minimax")
	}
	return filepath.Join(dataDir, "v2", "sqlite", "runtime-state.sqlite"), nil
}

func rawText(record map[string]json.RawMessage, names ...string) string {
	for _, name := range names {
		var text string
		if err := json.Unmarshal(record[name], &text); err == nil {
			return text
		}
	}
	return ""
}

func firstTime(record map[string]json.RawMessage, names ...string) time.Time {
	for _, name := range names {
		raw := record[name]
		if len(raw) == 0 {
			continue
		}
		var text string
		if err := json.Unmarshal(raw, &text); err == nil {
			if value, err := strconv.ParseFloat(text, 64); err == nil {
				return epoch(value)
			}
			continue
		}
		var value float64
		if err := json.Unmarshal(raw, &value); err == nil {
			return epoch(value)
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

func millis(value int64) time.Time {
	if value <= 0 {
		return time.Time{}
	}
	return time.UnixMilli(value)
}

func normalizeRole(role string) string {
	if role == channel.RoleUser {
		return channel.RoleUser
	}
	return channel.RoleAssistant
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
