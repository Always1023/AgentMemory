package main

import (
	"bytes"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Always1023/AgentMemory/internal/app"
	"github.com/Always1023/AgentMemory/internal/channel"
)

func TestParseArgs(t *testing.T) {
	if opts, err := parseArgs([]string{"-collect"}, &bytes.Buffer{}); err != nil || !opts.collect {
		t.Errorf("-collect 应当被认出来: %+v, %v", opts, err)
	}
	if opts, err := parseArgs(nil, &bytes.Buffer{}); err != nil || opts.collect {
		t.Errorf("不带参数应当是图形界面模式: %+v, %v", opts, err)
	}

	var errOut bytes.Buffer
	if _, err := parseArgs([]string{"--没有这个参数"}, &errOut); err == nil {
		t.Error("不认识的参数应当报错")
	}
	if _, err := parseArgs([]string{"多余的东西"}, &errOut); err == nil {
		t.Error("多余的位置参数应当报错")
	}
}

// 验收标准 3：退出码区分成功 / 部分成功 / 失败。
func TestExitCodesAreDistinct(t *testing.T) {
	cases := map[app.Outcome]int{
		app.OutcomeSuccess: exitOK,
		app.OutcomePartial: exitPartial,
		app.OutcomeFailed:  exitFailed,
	}
	seen := map[int]app.Outcome{}
	for outcome, want := range cases {
		if got := exitCodeFor(outcome); got != want {
			t.Errorf("exitCodeFor(%q) = %d，想要 %d", outcome, got, want)
		}
		if other, dup := seen[want]; dup {
			t.Errorf("%q 与 %q 的退出码都是 %d，没区分开", outcome, other, want)
		}
		seen[want] = outcome
	}

	// 「没跑起来」的那两个码不能和「跑完了」的撞。
	for _, code := range []int{exitUsage, exitNotImpl} {
		if _, clash := seen[code]; clash {
			t.Errorf("退出码 %d 与收集结论撞了", code)
		}
	}
}

func sampleResult() app.Result {
	return app.Result{
		StartedAt: time.Now().Add(-300 * time.Millisecond),
		Duration:  300 * time.Millisecond,
		Channels: []app.ChannelResult{
			{ID: "claude-code", Name: "Claude Code", Status: channel.StatusAvailable, Enabled: true,
				Found: 25, New: 2, Archived: 2},
			{ID: "codex", Name: "Codex CLI", Status: channel.StatusAvailable, Enabled: true,
				Found: 256, New: 1, Archived: 0, Failed: 1,
				Failures: []app.Failure{{Path: `C:\x\坏.jsonl`, Err: errStub(`C:\x\坏.jsonl 第 3 行解析失败`)}}},
			{ID: "continue", Name: "Continue", Status: channel.StatusNotInstalled, Enabled: true},
			{ID: "cursor", Name: "Cursor", Status: channel.StatusAvailable, Enabled: false},
		},
		Backup: app.BackupResult{Method: "git", Changed: true, Detail: "3 个文件，已提交并推送"},
	}
}

type errStub string

func (e errStub) Error() string { return string(e) }

// 验收标准 2 的输出侧：紧接着再跑一次，输出要明说「没有新增文件」。
func TestSummarySaysNothingNew(t *testing.T) {
	var out bytes.Buffer
	result := sampleResult()
	for i := range result.Channels {
		result.Channels[i].New = 0
		result.Channels[i].Archived = 0
	}
	result.Channels[1].Failed = 0
	result.Channels[1].Failures = nil

	printSummary(&out, result)
	got := out.String()
	if !strings.Contains(got, "没有新增") {
		t.Errorf("输出应当明说没有新增文件：\n%s", got)
	}
	if strings.Contains(got, "新增 0 段，收下 0 段") {
		t.Errorf("不该把「没有新增」写成一行零：\n%s", got)
	}
}

func TestSummaryCarriesCountsFailuresAndBackup(t *testing.T) {
	var out bytes.Buffer
	printSummary(&out, sampleResult())
	got := out.String()

	for _, want := range []string{
		"新增 3 段，收下 2 段，失败 1 处",
		"Claude Code", "发现 25 段 · 新增 2 段 · 收下 2 段",
		"Codex CLI", "失败 1",
		"Continue", "未检测到",
		"Cursor", "已停用",
		"坏.jsonl", "第 3 行解析失败",
		"备份：git · 3 个文件，已提交并推送",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("输出里找不到 %q\n--- 输出 ---\n%s", want, got)
		}
	}
}

func TestSummaryReportsBackupFailure(t *testing.T) {
	var out bytes.Buffer
	result := sampleResult()
	result.Backup = app.BackupResult{Method: "git", Changed: true, Err: errStub("没有权限")}

	printSummary(&out, result)
	if got := out.String(); !strings.Contains(got, "git · 没做成：没有权限") {
		t.Errorf("备份失败要在输出里说清楚：\n%s", got)
	}
}

func TestUnknownArgumentExitsWithUsageCode(t *testing.T) {
	var out, errOut bytes.Buffer
	code := run([]string{"--没有这个参数"}, &out, &errOut)
	if code != exitUsage {
		t.Errorf("退出码 = %d，想要 %d", code, exitUsage)
	}
	if errOut.Len() == 0 {
		t.Error("应当往标准错误写点提示")
	}
}

func TestHelpExitsZero(t *testing.T) {
	var out, errOut bytes.Buffer
	code := run([]string{"-h"}, &out, &errOut)
	if code != exitOK {
		t.Errorf("看帮助的退出码 = %d，想要 %d", code, exitOK)
	}
	if !strings.Contains(errOut.String(), "用法") {
		t.Errorf("应当打出用法：\n%s", errOut.String())
	}
}

// 验收标准 4：全程不弹窗、不需要交互输入。
//
// 「不弹窗」由构建方式决定（控制台程序不起窗口）；这里守住能守住的一半：
// 入口不读标准输入——定时任务里没人能回答它。
func TestEntryNeverReadsStandardInput(t *testing.T) {
	source, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"os.Stdin", "bufio.NewReader", "fmt.Scan", "term.ReadPassword", "os.Stdin.Fd"} {
		if strings.Contains(string(source), forbidden) {
			t.Errorf("入口里出现了 %s——静默模式不许要人回答", forbidden)
		}
	}
}

// I-091：--mcp 是独立模式，和 --collect 一样能在命令行上认出来。
func TestParseArgsMCP(t *testing.T) {
	opts, err := parseArgs([]string{"-mcp"}, &bytes.Buffer{})
	if err != nil || !opts.mcp {
		t.Fatalf("应当认 -mcp：%+v %v", opts, err)
	}
	if opts.collect {
		t.Fatal("-mcp 不该顺带打开收集")
	}
}
