// Package render 把统一会话模型渲染成 Markdown 文本。
//
// 纯函数：不碰磁盘、不读时钟，同一输入永远同一输出（验收标准 2）。
//
// 依赖方向（docs/ARCHITECTURE.md 模块表）：只依赖共享的会话模型类型。
package render

import (
	"fmt"
	"strings"
	"time"

	"github.com/Always1023/AgentMemory/internal/channel"
)

// 角色显示名。模型里的角色是英文标识，这里换成给人看的字。
var roleLabels = map[string]string{
	channel.RoleUser:      "我",
	channel.RoleAssistant: "助手",
	channel.RoleSystem:    "系统",
	channel.RoleTool:      "工具结果",
}

// emptyMark 是字段缺值时的占位。
const emptyMark = "—"

// timeLayout 是文档里时间的写法。
const timeLayout = "2006-01-02 15:04:05"

// Render 把一个会话渲染成 Markdown。
//
// channelName 是渠道的显示名，留空时退回渠道标识。
//
// 正文一律放进围栏（代码块）。这不是偷懒：聊天正文里什么都有
// （`#` 开头的一行、三连反引号、`---`、半个 HTML 标签），
// 直接铺在页面上会给文档造出我们没打算有的结构（验收标准 3）。
// 围栏长度按正文里最长的一串反引号算，正文关不掉我们的围栏。
func Render(s channel.Session, channelName string) string {
	var b strings.Builder

	title := oneLine(s.Title)
	if title == "" {
		title = "未命名会话"
	}
	fmt.Fprintf(&b, "# %s\n\n", title)

	name := oneLine(channelName)
	if name == "" {
		name = oneLine(s.Channel)
	}
	if name == "" {
		name = emptyMark
	} else if id := oneLine(s.Channel); id != "" && id != name {
		name = fmt.Sprintf("%s（%s）", name, id)
	}
	fmt.Fprintf(&b, "- 渠道：%s\n", name)
	fmt.Fprintf(&b, "- 项目：%s\n", valueOr(s.ProjectPath))
	fmt.Fprintf(&b, "- 会话：%s\n", valueOr(s.ID))
	fmt.Fprintf(&b, "- 开始：%s\n", timeOr(s.StartedAt))
	fmt.Fprintf(&b, "- 消息：%d 条\n", s.MessageCount())
	fmt.Fprintf(&b, "- 来自：%s\n", valueOr(s.SourceFile))

	for i, msg := range s.Messages {
		fmt.Fprintf(&b, "\n## %d · %s", i+1, roleLabel(msg.Role))
		if !msg.Time.IsZero() {
			fmt.Fprintf(&b, " · %s", msg.Time.Format(timeLayout))
		}
		b.WriteString("\n\n")

		if body := strings.TrimSpace(msg.Text); body != "" {
			writeFenced(&b, body)
		}
		for _, call := range msg.Tools {
			name := oneLine(call.Name)
			if name == "" {
				name = "工具"
			}
			fmt.Fprintf(&b, "**调用 · %s**\n\n", name)
			if input := strings.TrimSpace(call.Input); input != "" {
				writeFenced(&b, input)
			}
		}
	}

	return b.String()
}

// roleLabel 返回角色的显示名；没见过的角色原样显示，不把它丢掉。
func roleLabel(role string) string {
	if label, ok := roleLabels[role]; ok {
		return label
	}
	if role == "" {
		return "未知"
	}
	return oneLine(role)
}

// valueOr 空值给占位。
func valueOr(s string) string {
	if s = oneLine(s); s != "" {
		return s
	}
	return emptyMark
}

// timeOr 零值时间给占位——「零值」在模型里的含义就是「渠道没给时间」。
func timeOr(t time.Time) string {
	if t.IsZero() {
		return emptyMark
	}
	return t.Format(timeLayout)
}

// oneLine 把值压成一行：换行会把文档结构撑破（列表项、标题都会断在那儿）。
func oneLine(s string) string {
	return strings.TrimSpace(strings.NewReplacer("\r\n", " ", "\n", " ", "\r", " ").Replace(s))
}

// writeFenced 把一段正文写进围栏。
func writeFenced(b *strings.Builder, body string) {
	fence := fenceFor(body)
	body = strings.TrimRight(body, "\n")
	fmt.Fprintf(b, "%s\n%s\n%s\n\n", fence, body, fence)
}

// fenceFor 算出一个能安全裹住 body 的围栏。
//
// 围栏必须比正文里最长的一串反引号还长——否则正文能提前把围栏关掉，
// 后面的一切都会跑到围栏外面去。正文里没有反引号时用标准的三连。
func fenceFor(body string) string {
	longest, run := 0, 0
	for _, r := range body {
		if r == '`' {
			run++
			if run > longest {
				longest = run
			}
			continue
		}
		run = 0
	}
	n := longest + 1
	if n < 3 {
		n = 3
	}
	return strings.Repeat("`", n)
}
