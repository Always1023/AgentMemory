// Package channels 汇总本项目内置的渠道清单。
//
// 新增一个渠道 = 写一个适配器包 + 在 All() 里加一行。
// 这是 docs/ARCHITECTURE.md 承诺的「纯增量注册」的落点：
// 加渠道只动这里，app / store / render / archive 一行都不用改。
package channels

import (
	"github.com/Always1023/AgentMemory/internal/channel"
	"github.com/Always1023/AgentMemory/internal/channel/cherrystudio"
	"github.com/Always1023/AgentMemory/internal/channel/claudecode"
	"github.com/Always1023/AgentMemory/internal/channel/codex"
	"github.com/Always1023/AgentMemory/internal/channel/commandcode"
	"github.com/Always1023/AgentMemory/internal/channel/continuedev"
	"github.com/Always1023/AgentMemory/internal/channel/copilotchat"
	"github.com/Always1023/AgentMemory/internal/channel/cursor"
	"github.com/Always1023/AgentMemory/internal/channel/dsh"
	"github.com/Always1023/AgentMemory/internal/channel/factory"
	"github.com/Always1023/AgentMemory/internal/channel/gemini"
	"github.com/Always1023/AgentMemory/internal/channel/grokbuild"
	"github.com/Always1023/AgentMemory/internal/channel/hermes"
	"github.com/Always1023/AgentMemory/internal/channel/minimax"
	"github.com/Always1023/AgentMemory/internal/channel/openclaw"
	"github.com/Always1023/AgentMemory/internal/channel/opencode"
	"github.com/Always1023/AgentMemory/internal/channel/pi"
	"github.com/Always1023/AgentMemory/internal/channel/qoder"
	"github.com/Always1023/AgentMemory/internal/channel/webarchive"
	"github.com/Always1023/AgentMemory/internal/channel/workbuddy"
	"github.com/Always1023/AgentMemory/internal/channel/zcode"
)

// All 返回内置的全部渠道。
func All() []channel.Entry {
	return []channel.Entry{
		{ID: claudecode.ChannelID, Name: claudecode.Name, Adapter: claudecode.Adapter{}},
		{ID: codex.ChannelID, Name: codex.Name, Adapter: codex.Adapter{}},
		{ID: commandcode.ChannelID, Name: commandcode.Name, Adapter: commandcode.Adapter{}},
		{ID: continuedev.ChannelID, Name: continuedev.Name, Adapter: continuedev.Adapter{}},
		{ID: copilotchat.ChannelID, Name: copilotchat.Name, Adapter: copilotchat.Adapter{}},
		{ID: cursor.ChannelID, Name: cursor.Name, Adapter: cursor.Adapter{}},
		{ID: dsh.ChannelID, Name: dsh.Name, Adapter: dsh.Adapter{}},
		{ID: factory.ChannelID, Name: factory.Name, Adapter: factory.Adapter{}},
		{ID: opencode.ChannelID, Name: opencode.Name, Adapter: opencode.Adapter{}},
		{ID: workbuddy.ChannelID, Name: workbuddy.Name, Adapter: workbuddy.Adapter{}},
		{ID: zcode.ChannelID, Name: zcode.Name, Adapter: zcode.Adapter{}},
		{ID: gemini.ChannelID, Name: gemini.Name, Adapter: gemini.Adapter{}},
		{ID: grokbuild.ChannelID, Name: grokbuild.Name, Adapter: grokbuild.Adapter{}},
		{ID: hermes.ChannelID, Name: hermes.Name, Adapter: hermes.Adapter{}},
		{ID: minimax.ChannelID, Name: minimax.Name, Adapter: minimax.Adapter{}},
		{ID: openclaw.ChannelID, Name: openclaw.Name, Adapter: openclaw.Adapter{}},
		{ID: pi.ChannelID, Name: pi.Name, Adapter: pi.Adapter{}},
		{ID: qoder.ChannelID, Name: qoder.Name, Adapter: qoder.Adapter{}},
		{ID: cherrystudio.ChannelID, Name: cherrystudio.Name, Adapter: cherrystudio.Adapter{}},
		{ID: webarchive.ChannelID, Name: webarchive.Name, Adapter: &webarchive.Adapter{}},
	}
}

// RegisterInto 把内置渠道注册进注册表。
func RegisterInto(r *channel.Registry) error {
	for _, e := range All() {
		if err := r.Register(e); err != nil {
			return err
		}
	}
	return nil
}
