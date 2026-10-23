// Package qoder 实现对 Qoder 会话记录的适配器。
//
// 数据来源：<用户目录>/.qoder/projects/<项目>/<会话 ID>.jsonl，一个文件一次会话。
// 行格式与 Claude Code 同构（type=user / assistant），解析复用 claudejsonl；
// subagents/ 下的侧链文件不读。
package qoder

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/Always1023/AgentMemory/internal/channel"
	"github.com/Always1023/AgentMemory/internal/channel/claudejsonl"
)

// ChannelID 与 Name 是注册表用的身份。
const (
	ChannelID = "qoder"
	Name      = "Qoder"
)

// Adapter 实现 channel.Adapter。
type Adapter struct {
	// Root 是 projects 目录；留空时取 <用户目录>/.qoder/projects。
	Root string
}

// Detect 发现全部主会话文件。
func (a Adapter) Detect() channel.DetectResult {
	root, err := a.root()
	if err != nil {
		return channel.DetectResult{Status: channel.StatusFailed, Err: err}
	}
	return claudejsonl.DetectProjectDir(root, nil)
}

// Parse 解析一份会话。
func (a Adapter) Parse(sf channel.SourceFile) (channel.Session, error) {
	seed := claudejsonl.Seed{
		ChannelID: ChannelID,
		ID:        strings.TrimSuffix(filepath.Base(sf.Path), filepath.Ext(sf.Path)),
	}
	return claudejsonl.Parse(sf, seed)
}

// Probe 只判断这个文件里有没有会话消息（I-056）。
func (a Adapter) Probe(sf channel.SourceFile) (bool, error) {
	return claudejsonl.Probe(sf)
}

func (a Adapter) root() (string, error) {
	if a.Root != "" {
		return a.Root, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("取用户目录: %w", err)
	}
	return filepath.Join(home, ".qoder", "projects"), nil
}
