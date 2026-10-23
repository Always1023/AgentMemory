// Package factory 实现对 Factory Droid 会话记录的适配器。
//
// 数据来源：<用户目录>/.factory/sessions/<项目>/<会话>.jsonl，一个文件一次会话。
// 行格式与 Claude Code 同构；todo_state / compaction_state 不是对话。
package factory

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
	ChannelID = "factory"
	Name      = "Factory Droid"
)

// Adapter 实现 channel.Adapter。
type Adapter struct {
	// Root 是 sessions 目录；留空时取 <用户目录>/.factory/sessions。
	Root string
}

// Detect 发现全部主会话文件。
func (a Adapter) Detect() channel.DetectResult {
	root, err := a.root()
	if err != nil {
		return channel.DetectResult{Status: channel.StatusFailed, Err: err}
	}
	return claudejsonl.DetectProjectDir(root, isSidecar)
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
	return filepath.Join(home, ".factory", "sessions"), nil
}

func isSidecar(name string) bool {
	return strings.HasSuffix(strings.ToLower(name), ".checkpoints.jsonl")
}
