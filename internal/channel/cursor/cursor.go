// Package cursor 实现对 Cursor 会话记录的适配器。
//
// 数据来源：`<用户配置目录>/Cursor/User/globalStorage/state.vscdb` 里的 `cursorDiskKV` 表——
// 一个键一次会话（`composerData:<会话标识>`），值是整份会话 JSON，正文在它的 `conversation` 数组里
// （`type` 为 1 是用户、2 是助手，`text` 是正文）。
//
// 与文件型渠道的根本差别：**一个库装着很多次会话**。所以这里「一个源文件」指的是一个会话行：
// Detect 报的是每一行，路径写成 `<库路径>#<键>`（虚拟路径，只在本渠道内部用），
// 大小取这一行的字节数、时间取它自己的 `lastUpdatedAt`——这样游标的增量判断落在
// 「这一次会话有没有变」上，而不是「整个库有没有变」（整库一变就把所有会话重收一遍）。
//
// 库一律只读打开（AC-01），不写渠道的任何东西。
package cursor

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
	ChannelID = "cursor"
	Name      = "Cursor"
)

const (
	// dbRelPath 是库在用户配置目录下的相对路径。
	dbRelPath = "Cursor/User/globalStorage/state.vscdb"
	// composerPrefix 是会话行的键前缀；别的键（如 inlineDiffsData）不是会话。
	composerPrefix = "composerData:"
	// maxSessions 是一次最多认多少条会话（AC-07）。
	maxSessions = 5000
	// virtualSep 把库路径与会话键隔开，凑出一个虚拟的「源文件路径」。
	virtualSep = "#"
)

// 会话里 `type` 的取值：1 是用户，2 是助手。
const (
	typeUser      = 1
	typeAssistant = 2
)

// Adapter 实现 channel.Adapter。
type Adapter struct {
	// Root 是库文件路径；留空时取用户配置目录下的默认位置。
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

	handle, err := sqlite.Open(path)
	if err != nil {
		return channel.DetectResult{Status: channel.StatusFailed, Root: path, Err: fmt.Errorf("打开 %s: %w", path, err)}
	}
	defer handle.Close()

	if ok, err := handle.HasTable("cursorDiskKV"); err != nil {
		return channel.DetectResult{Status: channel.StatusFailed, Root: path, Err: err}
	} else if !ok {
		// 库在、但没有这张表：不是失败，只是这个版本没会话。
		return channel.DetectResult{Status: channel.StatusAvailable, Root: path}
	}

	rows, err := handle.All(
		`select key, value from cursorDiskKV where key like ? order by key limit ?`,
		composerPrefix+"%", maxSessions+1)
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
		key := row.Text("key")
		body := row.Text("value")
		if key == "" {
			continue
		}
		files = append(files, channel.SourceFile{
			Path:    path + virtualSep + key,
			Size:    int64(len(body)),
			ModTime: updatedAt(body, path),
		})
	}

	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	return channel.DetectResult{Status: channel.StatusAvailable, Root: path, Files: files}
}

// Parse 把一条会话行解析成统一会话模型。
func (a Adapter) Parse(sf channel.SourceFile) (channel.Session, error) {
	path, key, ok := splitVirtualPath(sf.Path)
	if !ok {
		return channel.Session{}, fmt.Errorf("%s 不是「库#会话键」这种路径", sf.Path)
	}
	if !strings.HasPrefix(key, composerPrefix) {
		return channel.Session{}, fmt.Errorf("%s 不是会话（键 %q）", sf.Path, key)
	}

	handle, err := sqlite.Open(path)
	if err != nil {
		return channel.Session{}, fmt.Errorf("打开 %s: %w", path, err)
	}
	defer handle.Close()

	rows, err := handle.All(`select value from cursorDiskKV where key = ?`, key)
	if err != nil {
		return channel.Session{}, err
	}
	if len(rows) == 0 {
		return channel.Session{}, fmt.Errorf("%s 里找不到会话 %s", path, key)
	}

	var doc composer
	if err := json.Unmarshal([]byte(rows[0].Text("value")), &doc); err != nil {
		return channel.Session{}, fmt.Errorf("解析会话 %s: %w", key, err)
	}

	session := channel.Session{
		Channel:    ChannelID,
		ID:         doc.ComposerID,
		SourceFile: sf.Path,
		StartedAt:  millis(doc.CreatedAt),
	}
	if session.ID == "" {
		session.ID = strings.TrimPrefix(key, composerPrefix)
	}
	if session.StartedAt.IsZero() {
		session.StartedAt = sf.ModTime
	}

	var candidates []string
	for _, item := range doc.Conversation {
		text := strings.TrimSpace(item.Text)
		if text == "" {
			continue
		}
		role := channel.RoleAssistant
		if item.Type == typeUser {
			role = channel.RoleUser
		}
		session.Messages = append(session.Messages, channel.Message{Role: role, Text: text})
		if role == channel.RoleUser {
			candidates = append(candidates, text)
		}
	}
	// 空会话（点开过没说话）不是错：返回 0 条消息，由编排层跳过（I-039）。
	// 标题还是给一个——它照样要进游标。
	if title := strings.TrimSpace(doc.Name); title != "" {
		session.Title = channel.Shorten(title, channel.TitleLimit)
	}
	if session.Title == "" {
		session.Title = channel.TitleFrom(candidates, "", session.ID)
	}
	return session, nil
}

// dbPath 返回库文件的位置。
//
// Cursor 把会话放在**全局**库（`globalStorage`）里，工作区那层的库里只有界面状态。
func (a Adapter) dbPath() (string, error) {
	if a.Root != "" {
		return a.Root, nil
	}
	base, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("找不到用户配置目录: %w", err)
	}
	return filepath.Join(base, filepath.FromSlash(dbRelPath)), nil
}

// composer 是会话行里我们关心的那部分。
type composer struct {
	ComposerID   string   `json:"composerId"`
	Name         string   `json:"name"`
	CreatedAt    int64    `json:"createdAt"`
	LastUpdated  int64    `json:"lastUpdatedAt"`
	Conversation []bubble `json:"conversation"`
}

// bubble 是会话里的一条发言。
type bubble struct {
	Type int    `json:"type"`
	Text string `json:"text"`
}

// updatedAt 取这条会话自己的更新时间；读不出来时退回库文件的修改时间。
func updatedAt(body, dbPath string) time.Time {
	var probe struct {
		LastUpdated int64 `json:"lastUpdatedAt"`
	}
	if err := json.Unmarshal([]byte(body), &probe); err == nil {
		if at := millis(probe.LastUpdated); !at.IsZero() {
			return at
		}
	}
	if info, err := os.Stat(dbPath); err == nil {
		return info.ModTime()
	}
	return time.Time{}
}

// splitVirtualPath 把 `<库路径>#<会话键>` 拆开。
//
// 用最后一个 `#`：Windows 路径里不会有它，而键里可能出现（例如某些工具的自定义键）。
func splitVirtualPath(virtual string) (dbPath, key string, ok bool) {
	idx := strings.LastIndex(virtual, virtualSep)
	if idx <= 0 || idx == len(virtual)-1 {
		return "", "", false
	}
	return virtual[:idx], virtual[idx+1:], true
}

// millis 把毫秒时间戳转成时间；非正数返回零值。
func millis(ms int64) time.Time {
	if ms <= 0 {
		return time.Time{}
	}
	return time.UnixMilli(ms)
}
