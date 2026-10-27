package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Always1023/AgentMemory/internal/channel"
	"github.com/Always1023/AgentMemory/internal/derive"
	"github.com/Always1023/AgentMemory/internal/store"
)

// nameOnlyAdapter 只为了占注册表的一个名额：检索绑定不解析源文件。
type nameOnlyAdapter struct{}

func (nameOnlyAdapter) Detect() channel.DetectResult { return channel.DetectResult{} }

func (nameOnlyAdapter) Parse(channel.SourceFile) (channel.Session, error) {
	return channel.Session{}, nil
}

// F13：搜索绑定按关键词命中，渠道显示名要翻成人话（A1）。
func TestSearchMemoryReturnsNamedHits(t *testing.T) {
	home := useTempHome(t)
	readyConfig(t, t.TempDir())

	runDB, err := store.OpenDB(home)
	if err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 10, 27, 9, 30, 0, 0, time.UTC)
	if err := runDB.WriteSession(channel.Session{
		Channel:    "claude-code",
		ID:         "s1",
		Title:      "归档撞名",
		SourceFile: "C:\\fake\\s1.jsonl",
		Messages:   []channel.Message{{Role: channel.RoleUser, Time: at, Text: "归档为什么撞名了？"}},
	}, time.Now()); err != nil {
		t.Fatal(err)
	}
	runDB.Close()

	ui := newTestApp(t, channel.Entry{ID: "claude-code", Name: "Claude Code", Adapter: nameOnlyAdapter{}})
	hits, err := ui.SearchMemory("撞名", "", "", 0)
	if err != nil {
		t.Fatalf("SearchMemory: %v", err)
	}
	if len(hits) != 1 {
		t.Fatalf("命中 %d 条，要 1 条", len(hits))
	}
	hit := hits[0]
	if hit.Channel != "Claude Code" {
		t.Errorf("渠道要显示成人话，实际 %q", hit.Channel)
	}
	if hit.Session != "归档撞名" {
		t.Errorf("会话标签取标题，实际 %q", hit.Session)
	}
	if hit.Kind != "消息" || !strings.Contains(hit.Snippet, "撞名") {
		t.Errorf("种类与片段不对: %+v", hit)
	}
}

// 空查询直接给空结果，不报错（界面会显示「输入一个关键词再搜」）。
func TestSearchMemoryEmptyQueryYieldsNothing(t *testing.T) {
	useTempHome(t)
	readyConfig(t, t.TempDir())
	ui := newTestApp(t)
	hits, err := ui.SearchMemory("   ", "", "", 0)
	if err != nil {
		t.Fatalf("空查询不该报错: %v", err)
	}
	if len(hits) != 0 {
		t.Fatalf("空查询应当没有命中，实际 %d", len(hits))
	}
}

// F14：切片导出落到归档目录的「切片」子目录，行行是合法 JSON（A1 / A2 的绑定侧）。
func TestExportSearchNDJSONWritesFile(t *testing.T) {
	home := useTempHome(t)
	archive := filepath.Join(t.TempDir(), "归档")
	readyConfig(t, archive)

	runDB, err := store.OpenDB(home)
	if err != nil {
		t.Fatal(err)
	}
	if err := runDB.WriteSession(channel.Session{
		Channel:    "claude-code",
		ID:         "s1",
		Title:      "归档撞名",
		SourceFile: filepath.Join("C:", "fake", "s1.jsonl"),
		Messages:   []channel.Message{{Role: channel.RoleUser, Time: time.Now().UTC(), Text: "归档为什么撞名了？"}},
	}, time.Now()); err != nil {
		t.Fatal(err)
	}
	runDB.Close()

	ui := newTestApp(t, channel.Entry{ID: "claude-code", Name: "Claude Code", Adapter: nameOnlyAdapter{}})
	out, err := ui.ExportSearchNDJSON("撞名")
	if err != nil {
		t.Fatalf("导出: %v", err)
	}
	if out.Count != 1 {
		t.Fatalf("导出 %d 行，要 1 行", out.Count)
	}
	raw, err := os.ReadFile(out.Path)
	if err != nil {
		t.Fatalf("读切片: %v", err)
	}
	var line map[string]any
	if err := json.Unmarshal(bytes.Split(raw, []byte("\n"))[0], &line); err != nil {
		t.Fatalf("第一行不是合法 JSON: %v", err)
	}
	if line["session"] != "s1" {
		t.Fatalf("切片定位不对: %v", line)
	}
	if !strings.Contains(out.Path, "切片") {
		t.Fatalf("落点应在归档目录的切片子目录: %q", out.Path)
	}

	// 空搜索词：给一句人话，不写文件。
	if _, err := ui.ExportSearchNDJSON("   "); err == nil {
		t.Fatal("空搜索词应当被拒")
	}
}

// A2：SearchOptions 用维度表填渠道与项目候选；SearchMemory 的渠道过滤只回选中渠道。
func TestSearchOptionsAndChannelFilter(t *testing.T) {
	home := useTempHome(t)
	readyConfig(t, t.TempDir())

	runDB, err := store.OpenDB(home)
	if err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 10, 27, 9, 0, 0, 0, time.UTC)
	for _, session := range []channel.Session{
		{Channel: "claude-code", ID: "a", Title: "甲", ProjectPath: filepath.Join("D:", "work"),
			StartedAt: at, SourceFile: "C:" + string(filepath.Separator) + "a.jsonl",
			Messages: []channel.Message{{Role: channel.RoleUser, Time: at, Text: "归档撞名一次"}}},
		{Channel: "codex", ID: "b", Title: "乙", ProjectPath: filepath.Join("D:", "other"),
			StartedAt: at, SourceFile: "C:" + string(filepath.Separator) + "b.jsonl",
			Messages: []channel.Message{{Role: channel.RoleUser, Time: at, Text: "归档撞名两次"}}},
	} {
		if err := runDB.WriteSession(session, time.Now()); err != nil {
			t.Fatal(err)
		}
	}
	// 维度表由每轮收集后的提取器重建；这里直接入库，补一次同样的重建。
	if registry, err := derive.Default(); err != nil {
		t.Fatal(err)
	} else if err := derive.Rebuild(runDB, registry); err != nil {
		t.Fatal(err)
	}
	runDB.Close()

	ui := newTestApp(t,
		channel.Entry{ID: "claude-code", Name: "Claude Code", Adapter: nameOnlyAdapter{}},
		channel.Entry{ID: "codex", Name: "Codex CLI", Adapter: nameOnlyAdapter{}})

	opts := ui.SearchOptions()
	if len(opts.Channels) != 2 {
		t.Fatalf("渠道候选 %d 个，要 2 个", len(opts.Channels))
	}
	if opts.Channels[0].Label != "Claude Code" || opts.Channels[0].Value != "claude-code" {
		t.Fatalf("渠道候选要带显示名: %+v", opts.Channels[0])
	}
	if len(opts.Projects) != 2 {
		t.Fatalf("项目候选 %d 个，要 2 个", len(opts.Projects))
	}

	// 渠道过滤：只回选中的那一家。
	hits, err := ui.SearchMemory("撞名", "codex", "", 0)
	if err != nil || len(hits) != 1 || hits[0].Channel != "Codex CLI" {
		t.Fatalf("渠道过滤命中: %d 条, %v", len(hits), err)
	}
}
