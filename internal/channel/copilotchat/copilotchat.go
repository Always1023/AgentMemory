// Package copilotchat 实现对 VS Code Copilot Chat 会话记录的适配器。
//
// 数据来源：<用户配置目录>/Code/User/workspaceStorage/<工作区哈希>/chatSessions/。
// 一个工作区一个目录，一个文件一次会话；同一个工作区同时可能出现两种文件：
//
//   - `.json`  —— 整份会话对象，直接读；
//   - `.jsonl` —— 编辑记录。第一行 `kind:0` 给底稿，之后每行一条编辑：
//     `kind:1` 把 `k` 指到的位置设成 `v`，`kind:2` 把 `v` 追加进 `k` 指到的数组。
//
// 项目目录记在同级的 `../workspace.json` 的 `folder` 字段（`file://` URI）。
package copilotchat

import (
	"bufio"
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
	"time"

	"github.com/Always1023/AgentMemory/internal/channel"
)

// ChannelID 与 Name 是注册表用的身份。
const (
	ChannelID = "copilot-chat"
	Name      = "VS Code Copilot Chat"
)

const (
	// sessionsDirName 是工作区目录下装会话的目录名。
	sessionsDirName = "chatSessions"
	// workspaceFileName 是工作区目录下记项目路径的文件名。
	workspaceFileName = "workspace.json"
	// maxFileBytes 是单份会话的上限（AC-07）。整份读，所以先量大小。
	maxFileBytes = 64 << 20
	// maxLineBytes 是编辑记录单行的上限（AC-07）：一条记录可能带整段正文。
	maxLineBytes = 8 << 20
	// editKindSet 是编辑记录里「设值」的 kind。
	editKindSet = 1
	// editKindPush 是编辑记录里「往数组追加」的 kind。
	editKindPush = 2
)

// Adapter 实现 channel.Adapter。
type Adapter struct {
	// Root 是 workspaceStorage 目录；留空时取用户配置目录下的 Code/User/workspaceStorage。
	Root string
}

// Detect 发现全部会话文件。
func (a Adapter) Detect() channel.DetectResult {
	root, err := a.root()
	if err != nil {
		return channel.DetectResult{Status: channel.StatusFailed, Err: err}
	}

	workspaces, err := os.ReadDir(root)
	if errors.Is(err, fs.ErrNotExist) {
		return channel.DetectResult{Status: channel.StatusNotInstalled}
	}
	if err != nil {
		return channel.DetectResult{Status: channel.StatusFailed, Root: root, Err: fmt.Errorf("读 %s: %w", root, err)}
	}

	var files []channel.SourceFile
	for _, workspace := range workspaces {
		if !workspace.IsDir() {
			continue
		}
		dir := filepath.Join(root, workspace.Name(), sessionsDirName)
		items, err := os.ReadDir(dir)
		if err != nil {
			// 单个工作区读不了，不影响其余工作区。
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

	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	return channel.DetectResult{Status: channel.StatusAvailable, Root: root, Files: files}
}

// Parse 把一份会话文件解析成统一会话模型。
func (a Adapter) Parse(sf channel.SourceFile) (channel.Session, error) {
	if sf.Size > maxFileBytes {
		return channel.Session{}, fmt.Errorf("%s 有 %d 字节，超过上限 %d，跳过", sf.Path, sf.Size, maxFileBytes)
	}

	f, err := readSession(sf.Path)
	if err != nil {
		return channel.Session{}, err
	}

	session := channel.Session{
		Channel:     ChannelID,
		ID:          f.SessionID,
		ProjectPath: projectPath(sf.Path),
		SourceFile:  sf.Path,
		StartedAt:   millis(f.CreationDate),
	}
	if session.ID == "" {
		session.ID = strings.TrimSuffix(filepath.Base(sf.Path), filepath.Ext(sf.Path))
	}
	if session.StartedAt.IsZero() {
		// 渠道没给开始时间时，拿源文件的修改时间当近似，好过零值——
		// 归档路径要用它，零值会让所有会话挤到同一天。
		session.StartedAt = sf.ModTime
	}

	var candidates []string
	for _, req := range f.Requests {
		if msg, ok := req.userMessage(); ok {
			session.Messages = append(session.Messages, msg)
			candidates = append(candidates, msg.Text)
		}
		if msg, ok := req.assistantMessage(); ok {
			session.Messages = append(session.Messages, msg)
		}
	}
	if title := strings.TrimSpace(f.CustomTitle); title != "" {
		session.Title = channel.Shorten(title, channel.TitleLimit)
	}
	if session.Title == "" {
		session.Title = channel.TitleFrom(candidates, sf.Path, session.ID)
	}
	// 一条消息都没有 = 空会话（打开过没说话）：不是错，由编排层跳过、不计失败（I-039）。
	return session, nil
}

// root 返回 workspaceStorage 目录。
//
// 留空时按用户配置目录推：Windows 是 %APPDATA%、macOS 是 Application Support、
// Linux 是 ~/.config——VS Code 在这三处都放在 <配置目录>/Code/User 下。
func (a Adapter) root() (string, error) {
	if a.Root != "" {
		return a.Root, nil
	}
	base, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("找不到用户配置目录: %w", err)
	}
	return filepath.Join(base, "Code", "User", "workspaceStorage"), nil
}

// isSessionFile 判断是不是会话文件；别的文件（索引、临时文件）不碰。
func isSessionFile(name string) bool {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".json", ".jsonl":
		return true
	}
	return false
}

// ── 读取：两种文件 ─────────────────────────────────────────────────────────

// sessionFile 是会话文件里我们关心的那部分；其余字段（输入框状态、附件、
// 模型元信息等）不是对话内容，不读。
type sessionFile struct {
	SessionID    string    `json:"sessionId"`
	CustomTitle  string    `json:"customTitle"`
	CreationDate int64     `json:"creationDate"`
	Requests     []request `json:"requests"`
}

// request 是一次「提问 + 回应」。
type request struct {
	Timestamp int64          `json:"timestamp"`
	Message   *userMessage   `json:"message"`
	Response  []responsePart `json:"response"`
}

// userMessage 是用户这一侧的输入。
type userMessage struct {
	Text string `json:"text"`
}

// responsePart 是回应里的一段。VS Code 用 kind 区分段落类型：
// 没有 kind 的是正文（值就是 Markdown），其余是思考过程、工具调用、进度提示等。
type responsePart struct {
	Kind              string          `json:"kind"`
	Value             json.RawMessage `json:"value"`
	InvocationMessage json.RawMessage `json:"invocationMessage"`
	ToolID            string          `json:"toolId"`
	ToolName          string          `json:"toolName"`
	Specific          *toolSpecific   `json:"toolSpecificData"`
	URI               *fileURI        `json:"uri"`
}

// toolSpecific 是工具调用自己的数据，只有终端命令我们用得上。
type toolSpecific struct {
	Kind        string       `json:"kind"`
	CommandLine *commandLine `json:"commandLine"`
}

type commandLine struct {
	Original   string `json:"original"`
	ToolEdited string `json:"toolEdited"`
}

// fileURI 是 VS Code 序列化的 URI，取本机路径那一段。
type fileURI struct {
	FsPath string `json:"fsPath"`
	Path   string `json:"path"`
}

// readSession 按扩展名分两条路读。
func readSession(path string) (sessionFile, error) {
	if strings.EqualFold(filepath.Ext(path), ".jsonl") {
		return readEditLog(path)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		return sessionFile{}, fmt.Errorf("读 %s: %w", path, err)
	}
	var f sessionFile
	if err := json.Unmarshal(raw, &f); err != nil {
		return sessionFile{}, fmt.Errorf("解析 %s: %w", path, err)
	}
	return f, nil
}

// editRecord 是一条编辑记录。
type editRecord struct {
	Kind int             `json:"kind"`
	K    []any           `json:"k"`
	V    json.RawMessage `json:"v"`
}

// readEditLog 读编辑记录：先拿底稿，再逐行把编辑应用上去。
func readEditLog(path string) (sessionFile, error) {
	file, err := os.Open(path)
	if err != nil {
		return sessionFile{}, fmt.Errorf("读 %s: %w", path, err)
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 64<<10), maxLineBytes)

	var doc map[string]any
	first, line := true, 0
	for scanner.Scan() {
		line++
		raw := bytes.TrimSpace(scanner.Bytes())
		if len(raw) == 0 {
			continue
		}
		var rec editRecord
		if err := json.Unmarshal(raw, &rec); err != nil {
			return sessionFile{}, fmt.Errorf("解析 %s 第 %d 行: %w", path, line, err)
		}
		if first {
			first = false
			if rec.Kind != 0 {
				return sessionFile{}, fmt.Errorf("%s 第 %d 行不是底稿（kind:%d）", path, line, rec.Kind)
			}
			if err := json.Unmarshal(rec.V, &doc); err != nil {
				return sessionFile{}, fmt.Errorf("解析 %s 的底稿: %w", path, err)
			}
			continue
		}
		if err := applyEdit(doc, rec); err != nil {
			return sessionFile{}, fmt.Errorf("应用 %s 第 %d 行的编辑记录: %w", path, line, err)
		}
	}
	if err := scanner.Err(); err != nil {
		return sessionFile{}, fmt.Errorf("读 %s: %w", path, err)
	}
	if doc == nil {
		return sessionFile{}, fmt.Errorf("%s 不是编辑记录：第一行不是 kind:0 的底稿", path)
	}

	// 底稿是通用结构（编辑要按路径改），这一步转成结构化字段。
	raw, err := json.Marshal(doc)
	if err != nil {
		return sessionFile{}, fmt.Errorf("还原 %s: %w", path, err)
	}
	var f sessionFile
	if err := json.Unmarshal(raw, &f); err != nil {
		return sessionFile{}, fmt.Errorf("还原 %s: %w", path, err)
	}
	return f, nil
}

// applyEdit 把一条编辑记录应用到通用 JSON 结构上。
//
// 不认识的 kind 直接跳过：VS Code 以后加新记录类型时，不该整份会话都读不出来。
func applyEdit(doc map[string]any, rec editRecord) error {
	if len(rec.K) == 0 || (rec.Kind != editKindSet && rec.Kind != editKindPush) {
		return nil
	}

	node := any(doc)
	for i, seg := range rec.K[:len(rec.K)-1] {
		next, ok := child(node, seg)
		if !ok {
			return fmt.Errorf("第 %d 段 %v 找不到", i+1, seg)
		}
		node = next
	}

	last := rec.K[len(rec.K)-1]
	if rec.Kind == editKindPush {
		var extra []any
		if err := json.Unmarshal(rec.V, &extra); err != nil {
			return fmt.Errorf("要追加的值不是数组: %w", err)
		}
		existing, _ := child(node, last)
		items, _ := existing.([]any)
		return setChild(node, last, append(items, extra...))
	}

	var value any
	if err := json.Unmarshal(rec.V, &value); err != nil {
		return fmt.Errorf("要设的值读不出来: %w", err)
	}
	return setChild(node, last, value)
}

// child 取一层子节点；路径段是对象键（字符串）或数组下标（数字）。
func child(node any, seg any) (any, bool) {
	switch key := seg.(type) {
	case string:
		obj, ok := node.(map[string]any)
		if !ok {
			return nil, false
		}
		value, ok := obj[key]
		return value, ok
	case float64:
		arr, ok := node.([]any)
		idx := int(key)
		if !ok || idx < 0 || idx >= len(arr) {
			return nil, false
		}
		return arr[idx], true
	}
	return nil, false
}

// setChild 写一层子节点。
func setChild(node any, seg any, value any) error {
	switch key := seg.(type) {
	case string:
		obj, ok := node.(map[string]any)
		if !ok {
			return fmt.Errorf("要对 %T 写键 %q", node, key)
		}
		obj[key] = value
		return nil
	case float64:
		arr, ok := node.([]any)
		idx := int(key)
		if !ok || idx < 0 || idx >= len(arr) {
			return fmt.Errorf("下标 %d 越界", idx)
		}
		arr[idx] = value
		return nil
	}
	return fmt.Errorf("不认识的路径段 %v", seg)
}

// ── 转换：段落 → 消息 ─────────────────────────────────────────────────────

// userMessage 把一次提问转成消息；没有正文时返回 false。
func (r request) userMessage() (channel.Message, bool) {
	if r.Message == nil {
		return channel.Message{}, false
	}
	text := strings.TrimSpace(r.Message.Text)
	if text == "" {
		return channel.Message{}, false
	}
	return channel.Message{Role: channel.RoleUser, Time: millis(r.Timestamp), Text: text}, true
}

// assistantMessage 把一次回应转成消息：正文合起来，工具调用各留一条。
func (r request) assistantMessage() (channel.Message, bool) {
	msg := channel.Message{Role: channel.RoleAssistant, Time: millis(r.Timestamp)}

	var text strings.Builder
	for _, part := range r.Response {
		switch part.Kind {
		case "":
			// 正文块：Copilot 的 Markdown 段落没有 kind，值就是正文。
			if body := strings.TrimSpace(textValue(part.Value)); body != "" {
				if text.Len() > 0 {
					text.WriteString("\n\n")
				}
				text.WriteString(body)
			}
		case "toolInvocationSerialized":
			msg.Tools = append(msg.Tools, part.toolCall())
		case "textEditGroup":
			msg.Tools = append(msg.Tools, part.editCall())
		default:
			// thinking、inlineReference、mcpServersStarting、prepareToolInvocation、
			// progressTaskSerialized 等内部块不属于对话内容，跳过。
		}
	}

	msg.Text = text.String()
	if msg.Text == "" && len(msg.Tools) == 0 {
		return channel.Message{}, false
	}
	return msg, true
}

// toolCall 把一次工具调用转成统一形态。
func (p responsePart) toolCall() channel.ToolCall {
	name := p.ToolID
	if name == "" {
		name = p.ToolName
	}

	input := ""
	if p.Specific != nil && p.Specific.Kind == "terminal" && p.Specific.CommandLine != nil {
		input = p.Specific.CommandLine.ToolEdited
		if input == "" {
			input = p.Specific.CommandLine.Original
		}
	}
	return channel.ToolCall{Name: name, Input: input}
}

// editCall 把一次文件改动转成工具调用。
func (p responsePart) editCall() channel.ToolCall {
	path := ""
	if p.URI != nil {
		path = p.URI.FsPath
		if path == "" {
			path = p.URI.Path
		}
	}
	return channel.ToolCall{Name: "编辑文件", Input: path}
}

// textValue 取「能当正文用」的值：只有字符串算，数组（thinking 的值那种）不算。
func textValue(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return ""
	}
	return s
}

// millis 把毫秒时间戳转成时间；非正数（渠道没给）返回零值。
func millis(ms int64) time.Time {
	if ms <= 0 {
		return time.Time{}
	}
	return time.UnixMilli(ms)
}

// ── 项目路径 ──────────────────────────────────────────────────────────────

// projectPath 从同级 ../workspace.json 里读项目目录；读不到就留空。
func projectPath(sessionFile string) string {
	raw, err := os.ReadFile(filepath.Join(filepath.Dir(filepath.Dir(sessionFile)), workspaceFileName))
	if err != nil {
		return ""
	}
	var w struct {
		Folder string `json:"folder"`
	}
	if err := json.Unmarshal(raw, &w); err != nil {
		return ""
	}
	return decodeFileURI(w.Folder)
}

// decodeFileURI 把 file:///d%3A/Coding/x 这种 URI 还原成本机路径。
func decodeFileURI(raw string) string {
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
