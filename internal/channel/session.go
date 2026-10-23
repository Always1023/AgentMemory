// Package channel 定义统一会话模型与渠道适配器的契约。
//
// 两件事：把各家格式各异的聊天记录，收敛成同一个「会话 + 消息」形态；
// 并给渠道一个只有「发现」与「解析」两步的契约。
//
// 依赖方向（docs/ARCHITECTURE.md AC-10）：本包不依赖任何其他业务包。
// 它不知道游标、归档目录与 Markdown——那些是别处的职责。
package channel

import "time"

// 消息角色。
const (
	RoleUser      = "user"
	RoleAssistant = "assistant"
	RoleSystem    = "system"
	RoleTool      = "tool"
)

// ToolCall 是一次工具调用。
type ToolCall struct {
	// Name 是工具名。
	Name string
	// Input 是调用入参原文。
	Input string
	// Output 是调用结果原文。
	Output string
}

// Message 是一条消息。
type Message struct {
	// Role 取 RoleUser / RoleAssistant / RoleSystem / RoleTool。
	Role string
	// Time 是消息时间；渠道没有给时间时为零值。
	Time time.Time
	// Text 是正文。
	Text string
	// Tools 是这条消息里的工具调用，没有则为空。
	Tools []ToolCall
}

// Session 是一次会话，是所有渠道解析后的统一形态。
type Session struct {
	// Channel 是渠道 ID，如 "claude-code"。
	Channel string
	// ID 是渠道内的会话标识。
	ID string
	// Title 是会话标题；渠道没有标题时由解析器给一个兜底值，不留空。
	Title string
	// ProjectPath 是会话所属项目目录；渠道没有这个概念时为空。
	ProjectPath string
	// StartedAt 是会话开始时间。
	StartedAt time.Time
	// SourceFile 是这份会话来自哪个源文件（绝对路径）。
	// 归档与游标都要用它，所以解析器必须填。
	SourceFile string
	// Messages 按时间先后排列。
	Messages []Message
}

// MessageCount 返回消息条数。
func (s Session) MessageCount() int { return len(s.Messages) }
