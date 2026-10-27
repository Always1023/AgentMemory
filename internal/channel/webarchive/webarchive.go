// Package webarchive 实现网页版归档的导入渠道（I-086）。
//
// 数据来源：<归档目录>/网页版/**\/*.md——浏览器插件（独立产品线，见 docs/PRD-WEB.md）
// 批量保存的产物。这些文件是爬虫唯一副本（AC-16）：只读不写，解析成统一会话模型
// 进 L1，之后与桌面会话同库同权（可检索、可切片）。
//
// 根目录的来源与别家不同：归档目录在用户配置里，不在家目录——所以默认根取
// 「用户目录/Documents/AgentMemory/网页版」（PRD 的默认归档位置），真正的目录
// 由编排层经 SetArchiveRoot 注入（channel 包不认识配置，AC-10）。
package webarchive

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/Always1023/AgentMemory/internal/channel"
)

// ChannelID 与 Name 是注册表用的身份。
const (
	ChannelID = "web-archive"
	Name      = "网页版归档"
)

// webDirName 是归档目录里插件产物的固定子目录（与插件侧约定一致）。
const webDirName = "网页版"

// Adapter 实现 channel.Adapter。
type Adapter struct {
	// Root 是「网页版」目录的完整路径；留空时取默认归档位置下的 网页版。
	Root string
}

// SetArchiveRoot 由编排层在每轮收集前注入归档目录（channel.ArchiveRootSetter）。
func (a *Adapter) SetArchiveRoot(archiveDir string) {
	if strings.TrimSpace(archiveDir) != "" {
		a.Root = filepath.Join(archiveDir, webDirName)
	}
}

// Detect 发现「网页版」目录下的全部 Markdown。
func (a Adapter) Detect() channel.DetectResult {
	root := a.root()
	if info, err := os.Stat(root); err != nil || !info.IsDir() {
		// 目录不在 = 没用过插件，这不是错误。
		return channel.DetectResult{Status: channel.StatusNotInstalled}
	}

	var files []channel.SourceFile
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil // 读不动的子目录跳过，别让整轮检测失败
		}
		if d.IsDir() || !strings.EqualFold(filepath.Ext(path), ".md") {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		files = append(files, channel.SourceFile{Path: path, Size: info.Size(), ModTime: info.ModTime()})
		return nil
	})
	if err != nil {
		return channel.DetectResult{Status: channel.StatusFailed, Err: err}
	}
	return channel.DetectResult{Status: channel.StatusAvailable, Root: root, Files: files}
}

// Parse 把一份插件产出的 Markdown 解析成统一会话模型。
//
// 文件形态（extension/lib/render.js 渲染）：首行「# 标题」，元信息是一组
// 「- 键：值」，消息是「## 序号 · 角色 · 时间」跟一段自适应围栏的正文。
func (a Adapter) Parse(sf channel.SourceFile) (channel.Session, error) {
	f, err := os.Open(sf.Path)
	if err != nil {
		return channel.Session{}, fmt.Errorf("打开 %s: %w", sf.Path, err)
	}
	defer f.Close()

	session := channel.Session{
		Channel:    ChannelID,
		SourceFile: sf.Path,
		ID:         strings.TrimSuffix(filepath.Base(sf.Path), ".md"),
		StartedAt:  sf.ModTime, // 没给开始时间时用文件修改时间兜底（I-048 的先例）
	}

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	inFence := false
	fenceLen := 0
	var current *channel.Message

	flush := func() {
		if current == nil {
			return
		}
		// 逐行拼接多出的那个换行去掉，与插件捕获时 trim 过的正文保持一致。
		current.Text = strings.TrimSuffix(current.Text, "\n")
		session.Messages = append(session.Messages, *current)
		current = nil
	}
	for scanner.Scan() {
		line := scanner.Text()
		switch {
		case inFence:
			if fenceClose(line, fenceLen) {
				inFence = false // 闭合围栏；这一行不进正文
				continue
			}
			current.Text += line + "\n"
		case strings.HasPrefix(line, "# ") && session.Title == "":
			session.Title = strings.TrimSpace(strings.TrimPrefix(line, "# "))
		case headingRe.MatchString(line):
			flush()
			current = parseHeading(line)
		case strings.HasPrefix(line, "- 会话："):
			if id := headerValue(line, "- 会话："); id != "" && id != "—" {
				session.ID = id
			}
		case strings.HasPrefix(line, "- 开始："):
			if at, ok := parseHeaderTime(headerValue(line, "- 开始：")); ok {
				session.StartedAt = at
			}
		case backtickRun(line) >= 3:
			// 开一段围栏（允许带语言标签）；长度取这一行的反引号数。
			inFence = true
			fenceLen = backtickRun(line)
		}
	}
	if err := scanner.Err(); err != nil {
		return channel.Session{}, fmt.Errorf("读 %s: %w", sf.Path, err)
	}
	flush()

	if session.Title == "" {
		session.Title = "未命名会话"
	}
	return session, nil
}

// root 定「网页版」目录：注入的优先，否则默认归档位置。
func (a Adapter) root() string {
	if a.Root != "" {
		return a.Root
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return webDirName // 极端情况下退相对路径：检测会报 NotInstalled，不至于崩
	}
	return filepath.Join(home, "Documents", "AgentMemory", webDirName)
}

// headingRe 匹配消息标题行：「## 3 · 助手 · 2026-10-27 12:00:00」。
var headingRe = regexp.MustCompile(`^## \d+ · ([^·]+?)(?: · (\d{4}-\d{2}-\d{2} \d{2}:\d{2}:\d{2}))?\s*$`)

// parseHeading 从标题行取角色与时间。
func parseHeading(line string) *channel.Message {
	m := headingRe.FindStringSubmatch(line)
	if m == nil {
		return &channel.Message{}
	}
	msg := &channel.Message{Role: mapRole(strings.TrimSpace(m[1]))}
	if m[2] != "" {
		if at, err := time.ParseInLocation("2006-01-02 15:04:05", m[2], time.Local); err == nil {
			msg.Time = at
		}
	}
	return msg
}

// mapRole 把插件的角色标签翻回统一模型的英文角色。
// 「未知」是插件对空角色的叫法，多数是用户气泡，按 user 收。
func mapRole(label string) string {
	switch label {
	case "我", "user":
		return channel.RoleUser
	case "助手", "assistant":
		return channel.RoleAssistant
	case "系统", "system":
		return channel.RoleSystem
	case "工具结果", "tool":
		return channel.RoleTool
	}
	return channel.RoleUser
}

// headerValue 取「- 键：值」里的值（去空格；占位符原样交回，由调用方判断）。
func headerValue(line, prefix string) string {
	return strings.TrimSpace(strings.TrimPrefix(line, prefix))
}

// parseHeaderTime 解析元信息里的时间（本地时区）。
func parseHeaderTime(v string) (time.Time, bool) {
	if v == "" || v == "—" {
		return time.Time{}, false
	}
	at, err := time.ParseInLocation("2006-01-02 15:04:05", v, time.Local)
	if err != nil {
		return time.Time{}, false
	}
	return at, true
}

// backtickRun 数一行开头的反引号数；开栏行允许带语言标签（```text）。
func backtickRun(line string) int {
	n := 0
	for _, r := range line {
		if r != '`' {
			break
		}
		n++
	}
	return n
}

// fenceClose 判断一行是不是围栏的收口：整行只有与开栏等长的反引号。
func fenceClose(line string, want int) bool {
	return want >= 3 && strings.TrimSpace(line) == strings.Repeat("`", want)
}
