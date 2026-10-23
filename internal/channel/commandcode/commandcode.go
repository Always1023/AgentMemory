// Package commandcode 实现对 CommandCode 会话记录的适配器。
//
// 数据来源：<用户目录>/.commandcode/projects/<项目>/<会话>.jsonl，一个文件一次会话。
// 行格式与 Claude Code 同构；同目录的 .checkpoints.jsonl 是断点快照，不是对话。
package commandcode

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/Always1023/AgentMemory/internal/channel"
	"github.com/Always1023/AgentMemory/internal/channel/claudejsonl"
)

// ChannelID 与 Name 是注册表用的身份。
const (
	ChannelID = "commandcode"
	Name      = "CommandCode"
)

// maxMetaBytes 是标题元数据的大小上限（AC-07）。
const maxMetaBytes = 1 << 20

// Adapter 实现 channel.Adapter。
type Adapter struct {
	// Root 是 projects 目录；留空时取 <用户目录>/.commandcode/projects。
	Root string
}

// Detect 发现全部主会话文件。
func (a Adapter) Detect() channel.DetectResult {
	root, err := a.root()
	if err != nil {
		return channel.DetectResult{Status: channel.StatusFailed, Err: err}
	}
	return claudejsonl.DetectProjectDir(root, isCheckpoint)
}

// Parse 解析一份会话。
func (a Adapter) Parse(sf channel.SourceFile) (channel.Session, error) {
	seed := claudejsonl.Seed{
		ChannelID: ChannelID,
		ID:        strings.TrimSuffix(filepath.Base(sf.Path), filepath.Ext(sf.Path)),
		Title:     metaTitle(sf.Path),
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
	return filepath.Join(home, ".commandcode", "projects"), nil
}

func isCheckpoint(name string) bool {
	return strings.HasSuffix(strings.ToLower(name), ".checkpoints.jsonl")
}

// metaTitle 读取同名的 .meta.json 里的标题；读不到就用空，让解析器从用户发言兜底。
func metaTitle(path string) string {
	metaPath := strings.TrimSuffix(path, filepath.Ext(path)) + ".meta.json"
	info, err := os.Stat(metaPath)
	if err != nil || info.Size() > maxMetaBytes {
		return ""
	}
	raw, err := os.ReadFile(metaPath)
	if err != nil {
		return ""
	}
	var meta struct {
		Title string `json:"title"`
	}
	if err := json.Unmarshal(raw, &meta); err != nil {
		return ""
	}
	return strings.TrimSpace(meta.Title)
}
