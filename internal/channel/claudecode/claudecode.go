// Package claudecode 实现对 Claude Code 会话记录的适配器。
//
// 数据来源：<用户目录>/.claude/projects/<项目>/<会话>.jsonl，一个文件一次会话。
// 行格式与 Qoder 同构（type=user / assistant），解析复用 claudejsonl（I-063）。
package claudecode

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/Always1023/AgentMemory/internal/channel"
	"github.com/Always1023/AgentMemory/internal/channel/claudejsonl"
)

// ChannelID 与 Name 是注册表用的身份。
const (
	ChannelID = "claude-code"
	Name      = "Claude Code"
)

// Adapter 实现 channel.Adapter。
type Adapter struct {
	// Root 是数据根目录；留空时取 <用户目录>/.claude/projects。
	// 给测试与自定义安装位置留的口子。
	Root string
}

// Detect 发现全部会话文件。
func (a Adapter) Detect() channel.DetectResult {
	root, err := a.root()
	if err != nil {
		return channel.DetectResult{Status: channel.StatusFailed, Err: err}
	}

	projects, err := os.ReadDir(root)
	if errors.Is(err, fs.ErrNotExist) {
		return channel.DetectResult{Status: channel.StatusNotInstalled}
	}
	if err != nil {
		return channel.DetectResult{Status: channel.StatusFailed, Root: root, Err: fmt.Errorf("读 %s: %w", root, err)}
	}

	var files []channel.SourceFile
	for _, project := range projects {
		if !project.IsDir() {
			continue
		}
		dir := filepath.Join(root, project.Name())
		items, err := os.ReadDir(dir)
		if err != nil {
			// 单个项目目录读不了，不影响其余项目。
			continue
		}
		for _, item := range items {
			if item.IsDir() || !strings.HasSuffix(item.Name(), ".jsonl") {
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

// Parse 把一个会话文件解析成统一会话模型；解析复用 claudejsonl。
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
	return filepath.Join(home, ".claude", "projects"), nil
}
