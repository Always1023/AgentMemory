package main

import (
	"strings"
	"testing"

	"github.com/Always1023/AgentMemory/internal/app"
	"github.com/Always1023/AgentMemory/internal/channel"
	"github.com/Always1023/AgentMemory/internal/config"
)

// ── 渠道列表（I-015）──────────────────────────────────────────────────────
//
// 这一组用例盯的是「哪一行出了什么事，一眼看得见」：
// 状态与计数要跟真跑出来的结果对上，坏掉的那行要给「重试」，而重试只动那一行。

// brokenAdapter 让指定的那些文件解析失败（其余照常）。
type brokenAdapter struct {
	id      string
	sources []channel.SourceFile
	bad     map[string]bool
}

func (a brokenAdapter) Detect() channel.DetectResult {
	return channel.DetectResult{Status: channel.StatusAvailable, Root: "/根", Files: a.sources}
}

func (a brokenAdapter) Parse(sf channel.SourceFile) (channel.Session, error) {
	if a.bad[sf.Path] {
		return channel.Session{}, errTestBrokenFile
	}
	return channel.Session{
		Channel:    a.id,
		SourceFile: sf.Path,
		Messages:   []channel.Message{{Role: channel.RoleUser, Text: "假装说了一句"}},
	}, nil
}

var errTestBrokenFile = &brokenFileError{}

type brokenFileError struct{}

func (*brokenFileError) Error() string { return "这个会话文件写坏了" }

// 验收标准 1：一行一个渠道，状态与计数跟真跑出来的一致。
func TestChannelRowsCarryTheLastRunCounts(t *testing.T) {
	useTempHome(t)
	readyConfig(t, t.TempDir())

	files := fakeSources(t, 3)
	adapter := brokenAdapter{id: "fake", sources: files, bad: map[string]bool{files[1].Path: true}}
	ui := newTestApp(t, channel.Entry{ID: "fake", Name: "假渠道", Adapter: adapter})

	state, err := ui.CollectNow()
	if err != nil {
		t.Fatalf("CollectNow: %v", err)
	}

	row := findRow(t, state, "fake")
	if row.Found != 3 || row.New != 3 || row.Archived != 2 || row.Failed != 1 {
		t.Errorf("这一行的数字是 %+v，想要 会话 3 / 新增 3 / 收下 2 / 失败 1", row)
	}
	if row.State != "bad" {
		t.Errorf("有文件解析失败，这一行应当是 bad（界面标红），实际 %q", row.State)
	}
	if !strings.Contains(row.Detail, "解析失败 1 个") {
		t.Errorf("这一行的说明里应当写清「解析失败 1 个」，实际 %q", row.Detail)
	}
	if row.Action != channelActionCollect {
		t.Error("还有没收的（这里是坏文件留下的）应当给那个能点的箭头（验收标准 2）")
	}

	// 收第二遍：坏文件会自动重试（I-048），仍然失败；好文件不重复处理。
	state, err = ui.CollectNow()
	if err != nil {
		t.Fatalf("第二轮: %v", err)
	}
	row = findRow(t, state, "fake")
	if row.New != 1 || row.Archived != 0 || row.Failed != 1 {
		t.Errorf("第二轮应当只重试那个坏文件（新增 1 / 失败 1），实际 %+v", row)
	}
}

// 验收标准 3：点「重试」只改那一行，其他行原样不动。
func TestRetryTouchesOnlyItsOwnRow(t *testing.T) {
	useTempHome(t)
	readyConfig(t, t.TempDir())

	broken := fakeSources(t, 2)
	healthy := fakeSources(t, 2)

	ui := newTestApp(t,
		channel.Entry{ID: "bad", Name: "渠道甲", Adapter: brokenAdapter{id: "bad", sources: broken, bad: map[string]bool{broken[0].Path: true}}},
		channel.Entry{ID: "ok", Name: "渠道乙", Adapter: recordingAdapter{id: "ok", sources: healthy}},
	)

	state, err := ui.CollectNow()
	if err != nil {
		t.Fatalf("CollectNow: %v", err)
	}
	beforeBad := findRow(t, state, "bad")
	beforeOK := findRow(t, state, "ok")
	if beforeBad.State != "bad" || beforeOK.State != "ok" {
		t.Fatalf("收完一轮后两行的状态不对：甲 %+v，乙 %+v", beforeBad, beforeOK)
	}

	// 重试健康的那一个：它自己会重跑一遍（重试就是「再跑一次这个渠道」），
	// 坏的那一行一个字都不该动。
	state, err = ui.RetryChannel("ok")
	if err != nil {
		t.Fatalf("RetryChannel: %v", err)
	}

	if after := findRow(t, state, "bad"); after != beforeBad {
		t.Errorf("重试渠道乙，渠道甲那一行不该变：\n重试前 %+v\n重试后 %+v", beforeBad, after)
	}
	// 单点一个渠道：结果**不上顶部**，「勾 + 用时」跟在那一行后面（人提的）。
	if state.Headline != "" {
		t.Errorf("单点渠道之后顶部应当是空的，实际 %q", state.Headline)
	}
	afterRow := findRow(t, state, "ok")
	if afterRow.LastDuration == "" {
		t.Errorf("用时该挂在那一行上（就是被单点的那个渠道），实际 %+v", afterRow)
	}

	// 再重试坏的那一个：文件还是坏的，应当仍然报失败——不许悄悄变成「没事了」。
	state, err = ui.RetryChannel("bad")
	if err != nil {
		t.Fatalf("RetryChannel(坏渠道): %v", err)
	}
	if after := findRow(t, state, "bad"); after.State != "bad" || after.Failed != 1 {
		t.Errorf("坏文件没修，重试之后仍应当报解析失败，实际 %+v", after)
	}
	// 仍要有失败：顶部照旧不说（那一行自己会说「解析失败 N 个」），但那一行要带上失败数。
	failedRow := findRow(t, state, "bad")
	if failedRow.Failed == 0 || failedRow.Action != channelActionCollect {
		t.Errorf("失败的那一行仍要留着能点的箭头与失败数，实际 %+v", failedRow)
	}
}

// 重试一个不存在、或已停用的渠道：要明说，不能默默什么都不做。
func TestRetryRejectsUnknownAndDisabledChannels(t *testing.T) {
	useTempHome(t)
	readyConfig(t, t.TempDir())

	ui := newTestApp(t, channel.Entry{ID: "fake", Name: "假渠道", Adapter: recordingAdapter{id: "fake"}})

	if _, err := ui.RetryChannel("并不存在"); err == nil {
		t.Error("重试一个不存在的渠道应当报错")
	} else if !strings.Contains(err.Error(), "不认识的渠道") {
		t.Errorf("报错要说清是不认识这个渠道，实际 %v", err)
	}
	if _, err := ui.RetryChannel("  "); err == nil {
		t.Error("没给渠道名也应当报错")
	}

	// 停用一个渠道之后重试它：说清楚要先启用。
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	cfg.Channels = map[string]bool{"fake": false}
	if err := config.Save(cfg); err != nil {
		t.Fatal(err)
	}
	if _, err := ui.RetryChannel("fake"); err == nil {
		t.Error("重试已停用的渠道应当报错")
	} else if !strings.Contains(err.Error(), "已停用") {
		t.Errorf("报错要说清是停用了，实际 %v", err)
	}
}

// 验收标准 4：未安装的渠道显示「未检测到」，而且不给「重试」——重试也不会突然装上。
func TestNotInstalledRowHasNoRetry(t *testing.T) {
	useTempHome(t)
	readyConfig(t, t.TempDir())

	ui := newTestApp(t,
		channel.Entry{ID: "gone", Name: "没装的", Adapter: notInstalledAdapter{}},
		channel.Entry{ID: "here", Name: "装了的", Adapter: recordingAdapter{id: "here", sources: fakeSources(t, 1)}},
	)

	state, err := ui.State()
	if err != nil {
		t.Fatal(err)
	}
	if row := findRow(t, state, "gone"); row.State != "none" || row.Action != "" || row.Detail != "未检测到" {
		t.Errorf("未安装的那一行应当是「未检测到」且不给标记，实际 %+v", row)
	}
	if row := findRow(t, state, "here"); row.Action == "" {
		t.Errorf("装了的渠道应当给一个标记（箭头或勾），实际 %+v", row)
	}
}

// 收完一轮之后，运行记录里也要有逐渠道的数字——界面那一列就是从这里来的。
func TestLastChannelsKeepThePreviousRunWhenRetrying(t *testing.T) {
	useTempHome(t)
	readyConfig(t, t.TempDir())

	sources := fakeSources(t, 2)
	ui := newTestApp(t,
		channel.Entry{ID: "a", Name: "渠道甲", Adapter: recordingAdapter{id: "a", sources: sources}},
		channel.Entry{ID: "b", Name: "渠道乙", Adapter: recordingAdapter{id: "b", sources: fakeSources(t, 1)}},
	)

	if _, err := ui.CollectNow(); err != nil {
		t.Fatalf("CollectNow: %v", err)
	}
	if _, err := ui.RetryChannel("a"); err != nil {
		t.Fatalf("RetryChannel: %v", err)
	}

	ui.mu.Lock()
	runs := ui.lastChannels
	ui.mu.Unlock()
	if len(runs) != 2 {
		t.Fatalf("两个渠道的数字都该留着，实际 %d 个", len(runs))
	}
	if runs["b"].Archived != 1 {
		t.Errorf("没被重试的渠道乙，上一轮的数字应当保住，实际 %+v", runs["b"])
	}
	if runs["a"].Archived != 2 {
		t.Errorf("被重试的渠道甲应当重跑一遍（重试不看游标），实际 %+v", runs["a"])
	}
}

// findRow 取出某个渠道那一行；没有就让人看见。
func findRow(t *testing.T, state UIState, id string) UIChannel {
	t.Helper()
	for _, row := range state.Channels {
		if row.ID == id {
			return row
		}
	}
	t.Fatalf("状态里没有渠道 %q，只有 %+v", id, state.Channels)
	return UIChannel{}
}

// 保证 app 包的那个类型还是界面列数据用的那个（改坏了编译期就发现）。
var _ = app.ChannelResult{}
