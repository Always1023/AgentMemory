package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Always1023/AgentMemory/internal/store"
)

// 重启后内存里没有本轮结果：首页从运行记录读回上一轮（I-061 验收标准 1）。
func TestStateRecoversLastRunFromLog(t *testing.T) {
	home := useTempHome(t)
	archiveDir := filepath.Join(home, "归档")
	if err := os.MkdirAll(archiveDir, 0o755); err != nil {
		t.Fatal(err)
	}
	readyConfig(t, archiveDir)

	now := time.Now()
	if err := store.AppendRun(home, store.Run{At: now.Add(-2 * time.Hour), Archived: 1, Found: 10}, runLogKeepDays, now); err != nil {
		t.Fatal(err)
	}
	if err := store.AppendRun(home, store.Run{At: now, Archived: 8, Found: 1191, Failures: 1}, runLogKeepDays, now); err != nil {
		t.Fatal(err)
	}

	state, err := newTestApp(t).State()
	if err != nil {
		t.Fatalf("State: %v", err)
	}
	run := state.LastRun
	if run == nil {
		t.Fatal("运行记录里有上一轮，不该是 nil")
	}
	if !run.FromHistory {
		t.Error("重启后读回的上一轮要标成历史，不能冒充刚跑完")
	}
	if run.Archived != 8 || run.Found != 1191 || run.Failures != 1 {
		t.Errorf("读回的数字不对：收下 %d / 检测到 %d / 失败 %d，想要 8 / 1191 / 1",
			run.Archived, run.Found, run.Failures)
	}
	if strings.TrimSpace(run.At) == "" {
		t.Error("历史行要有上一轮的时间")
	}
	if state.Headline != "" {
		t.Errorf("历史行不该占顶部（旧结果不是刚发生），实际 %q", state.Headline)
	}
}

// 没有运行记录但归档里有文件：不能再说「还没收过」，要给归档份数（验收标准 2）。
func TestStateFallsBackToArchiveTotalWithoutRunLog(t *testing.T) {
	home := useTempHome(t)
	archiveDir := filepath.Join(home, "归档")
	readyConfig(t, archiveDir)

	dir := filepath.Join(archiveDir, "fake", "proj")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "2026-10-22-会话-aaaaaaaaaa.md"), []byte("# 会话"), 0o644); err != nil {
		t.Fatal(err)
	}

	state, err := newTestApp(t).State()
	if err != nil {
		t.Fatalf("State: %v", err)
	}
	if state.LastRun != nil {
		t.Errorf("没有运行记录时不该编出上一轮，实际 %+v", state.LastRun)
	}
	if state.ArchivedTotal != 1 {
		t.Errorf("归档里有一份时 archivedTotal 应为 1，实际 %d", state.ArchivedTotal)
	}
}

// 运行记录坏了只当没有：界面照常打开，不报错、不编上一轮（验收标准 5）。
func TestStateSurvivesBrokenRunLog(t *testing.T) {
	home := useTempHome(t)
	archiveDir := filepath.Join(home, "归档")
	readyConfig(t, archiveDir)

	if err := os.WriteFile(filepath.Join(home, store.RunsFileName), []byte("这不是 JSON\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	state, err := newTestApp(t).State()
	if err != nil {
		t.Fatalf("坏记录不该让 State 失败：%v", err)
	}
	if state.LastRun != nil {
		t.Errorf("坏行应当当作没有记录，实际 %+v", state.LastRun)
	}
}
