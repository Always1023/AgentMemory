package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Always1023/AgentMemory/internal/app"
	"github.com/Always1023/AgentMemory/internal/channel"
	"github.com/Always1023/AgentMemory/internal/config"
)

// ── 造场景 ────────────────────────────────────────────────────────────────

// fakeSources 造 n 个「会话文件」，返回带大小与修改时间的源文件列表。
func fakeSources(t *testing.T, n int) []channel.SourceFile {
	t.Helper()
	root := t.TempDir()

	out := make([]channel.SourceFile, 0, n)
	for i := 0; i < n; i++ {
		path := filepath.Join(root, fmt.Sprintf("会话-%d.jsonl", i))
		if err := os.WriteFile(path, []byte("（假装有内容）"), 0o600); err != nil {
			t.Fatal(err)
		}
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, channel.SourceFile{Path: path, Size: info.Size(), ModTime: info.ModTime()})
	}
	return out
}

// fakeChannel 把一批源文件包成一个渠道（解析永远成功）。
func fakeChannel(id, name string, sources []channel.SourceFile) channel.Entry {
	return channel.Entry{ID: id, Name: name, Adapter: recordingAdapter{id: id, sources: sources}}
}

// readyConfig 写一份「引导走完了、备份只留本地」的配置。
//
// 备份选 none 是为了不让用例去碰 git——收集本身的事跟备份无关。
func readyConfig(t *testing.T, archiveDir string) {
	t.Helper()
	cfg := config.Default()
	cfg.FirstRunDone = true
	cfg.ArchiveDir = archiveDir
	cfg.Backup.Mode = config.BackupNone
	if err := config.Save(cfg); err != nil {
		t.Fatal(err)
	}
}

// countArchived 数归档目录里落下了几个 Markdown。
func countArchived(t *testing.T, root string) int {
	t.Helper()
	n := 0
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() && strings.HasSuffix(d.Name(), ".md") {
			n++
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return n
}

// ── 一轮收集 ──────────────────────────────────────────────────────────────

// 验收标准 1（机制侧）：点一次「立即收集」就是收一轮；收完顶部说出本次新增数。
func TestCollectNowRunsOneRoundAndShowsNewFiles(t *testing.T) {
	useTempHome(t)
	archive := t.TempDir()
	readyConfig(t, archive)

	ui := newTestApp(t, fakeChannel("fake", "假渠道", fakeSources(t, 3)))

	state, err := ui.CollectNow()
	if err != nil {
		t.Fatalf("CollectNow: %v", err)
	}
	if state.LastRun == nil {
		t.Fatal("收完应当留下这一轮的运行记录")
	}
	if state.LastRun.New != 3 || state.LastRun.Archived != 3 {
		t.Errorf("新增 %d / 收下 %d，想要 3 / 3", state.LastRun.New, state.LastRun.Archived)
	}
	if state.LastRun.Duration == "" {
		t.Error("运行记录里应当有用时，界面要显示它")
	}
	if !strings.Contains(state.Headline, "新增 3 段") {
		t.Errorf("顶部应当报出本次新增数，实际 %q", state.Headline)
	}
	if state.Collecting {
		t.Error("已经收完了，不该还说在收")
	}
	if n := countArchived(t, archive); n != 3 {
		t.Errorf("归档目录里落了 %d 个 Markdown，想要 3 个", n)
	}

	// 再点一次：同一批文件不产生第二份归档——「重复收集」是更新，不是新增。
	state, err = ui.CollectNow()
	if err != nil {
		t.Fatalf("再收一轮: %v", err)
	}
	if state.LastRun.New != 0 {
		t.Errorf("第二轮新增 %d，想要 0（游标应当认得出这批文件）", state.LastRun.New)
	}
	if !strings.Contains(state.Headline, "没有新增") {
		t.Errorf("第二轮顶部应当说没有新增，实际 %q", state.Headline)
	}
	if n := countArchived(t, archive); n != 3 {
		t.Errorf("第二轮之后归档目录里是 %d 个 Markdown，想要还是 3 个", n)
	}
}

// 验收标准 3（机制侧）：界面收一轮，数出来的与命令行收一轮是同一套数字。
//
// 两边调的都是 app.Collect（界面不是另写一遍收集）。这里用两份等价的环境——
// 同一批源文件、各自全新的游标——各跑一遍，比对四个数字。
func TestCollectNowMatchesTheCommandLineNumbers(t *testing.T) {
	type numbers struct{ Found, New, Archived, Failures int }

	sources := fakeSources(t, 2)
	entry := fakeChannel("fake", "假渠道", sources)

	runWith := func(viaUI bool) numbers {
		t.Helper()
		t.Setenv("AGENTMEMORY_HOME", t.TempDir())
		readyConfig(t, filepath.Join(t.TempDir(), "归档"))

		registry := channel.NewRegistry()
		if err := registry.Register(entry); err != nil {
			t.Fatal(err)
		}
		deps := app.Deps{Channels: registry}

		if viaUI {
			ui := NewApp(quietLogger())
			ui.deps = deps
			if _, err := ui.CollectNow(); err != nil {
				t.Fatalf("CollectNow: %v", err)
			}
			state, err := ui.State()
			if err != nil {
				t.Fatal(err)
			}
			return numbers{state.LastRun.Found, state.LastRun.New, state.LastRun.Archived, state.LastRun.Failures}
		}

		cfg, err := config.Load()
		if err != nil {
			t.Fatal(err)
		}
		result, err := app.Collect(cfg, quietLogger(), deps)
		if err != nil {
			t.Fatalf("命令行那一轮: %v", err)
		}
		return numbers{result.Found(), result.New(), result.Archived(), result.Failures()}
	}

	if ui := runWith(true); ui != runWith(false) {
		t.Errorf("界面收到的是 %+v，命令行收到的是 %+v——两边不该不一样", ui, runWith(false))
	}
}

// slowAdapter 每解析一个文件就停一下，并在开始解析时报一声。
//
// 用例靠这一声卡在「正在收集」的那一刻，不必猜时间。
type slowAdapter struct {
	started chan struct{}
	delay   time.Duration
	files   []channel.SourceFile
}

func (a *slowAdapter) Detect() channel.DetectResult {
	return channel.DetectResult{Status: channel.StatusAvailable, Root: "/根", Files: a.files}
}

// Probe 是统计那一路的轻量探测（I-056）：快进快出。
// 没有它，State() 的统计与收集用同一个慢 Parse，两边耗时一样，
// 「收集中再点」的窗口就压线了——真实适配器正是靠 Probe 把统计变快的。
func (a *slowAdapter) Probe(sf channel.SourceFile) (bool, error) {
	time.Sleep(5 * time.Millisecond)
	return true, nil
}

func (a *slowAdapter) Parse(sf channel.SourceFile) (channel.Session, error) {
	select {
	case a.started <- struct{}{}:
	default:
	}
	time.Sleep(a.delay)
	return channel.Session{
		Channel:    "slow",
		SourceFile: sf.Path,
		Messages:   []channel.Message{{Role: channel.RoleUser, Text: "假装说了一句"}},
	}, nil
}

// 验收标准 2 与 4（机制侧）：收集中再点会被拒；收集中界面照样能读到状态。
//
// 「窗口能拖动、能关闭」这件事只能人看着窗口说；能在这儿立起来的是它的机制：
// 收集不占着界面那一路——收集中途另一个调用（State）能照常返回，且如实说「在收」
// （界面据此把按钮置灰）。状态读不出来或读出来是「没收」，按钮就会留给人再点一次。
func TestStateAnswersWhileCollectingAndSecondClickIsRejected(t *testing.T) {
	useTempHome(t)
	readyConfig(t, t.TempDir())

	adapter := &slowAdapter{started: make(chan struct{}, 1), delay: 150 * time.Millisecond}
	adapter.files = fakeSources(t, 3)
	ui := newTestApp(t, channel.Entry{ID: "slow", Name: "慢渠道", Adapter: adapter})

	type outcome struct {
		state UIState
		err   error
	}
	done := make(chan outcome, 1)
	go func() {
		state, err := ui.CollectNow()
		done <- outcome{state, err}
	}()

	select {
	case <-adapter.started:
	case <-time.After(10 * time.Second):
		t.Fatal("收集没跑起来")
	}

	// 第一个文件正在解析——这一轮肯定还没结束（结束了 done 就有值了）。
	select {
	case <-done:
		t.Fatal("这一轮已经结束了，这一刻不算「收集中」")
	default:
	}

	state, err := ui.State()
	if err != nil {
		t.Fatalf("收集中读状态: %v", err)
	}
	if !state.Collecting {
		t.Error("收集中读到的状态应当说「在收」，界面靠它把按钮置灰")
	}

	if _, err := ui.CollectNow(); err == nil {
		t.Error("收集中再点，应当被拒——不能叠第二轮")
	} else if !strings.Contains(err.Error(), "正在收集") {
		t.Errorf("拒的理由要说人话，实际 %v", err)
	}

	final := <-done
	if final.err != nil {
		t.Fatalf("这一轮: %v", final.err)
	}
	if final.state.LastRun.Archived != 3 {
		t.Errorf("收下 %d 段，想要 3 份——被拒的那次不该让这一轮少收或多收",
			final.state.LastRun.Archived)
	}
	if final.state.Collecting {
		t.Error("收完了不该还说在收")
	}
}

// 一轮收不完时，顶部要如实说没跑完，并且把「正在收集」放掉。
func TestCollectFailureShowsUpInTheHeadline(t *testing.T) {
	useTempHome(t)

	// 归档目录的位置上放一个文件：建目录必然失败，属于系统错。
	blocked := filepath.Join(t.TempDir(), "归档")
	if err := os.WriteFile(blocked, []byte("我是文件，不是目录"), 0o600); err != nil {
		t.Fatal(err)
	}
	readyConfig(t, blocked)

	ui := newTestApp(t, fakeChannel("fake", "假渠道", fakeSources(t, 1)))

	state, err := ui.CollectNow()
	if err == nil {
		t.Fatal("归档目录写不进去，这一轮不该报成功")
	}
	if !strings.Contains(state.Headline, "收集没能完成") {
		t.Errorf("顶部应当如实说没跑完，实际 %q", state.Headline)
	}
	if state.Collecting {
		t.Error("失败了也要把「正在收集」放掉，不然按钮一直置灰")
	}
	if state.LastRun == nil || state.LastRun.Err == "" {
		t.Error("运行记录里应当留着没跑完的原因")
	}
}

// ── 进度 ──────────────────────────────────────────────────────────────────

// 验收标准 1（机制侧）：进度按步报——检测 → 逐渠道 → 备份 → 收尾，顺序不能乱。
//
// 进度走事件（不是界面反复来问）：所以这里换掉事件出口，把推出去的内容记下来。
func TestProgressReportsEveryStepInOrder(t *testing.T) {
	useTempHome(t)
	readyConfig(t, t.TempDir())

	ui := newTestApp(t, fakeChannel("fake", "假渠道", fakeSources(t, 1)))

	var mu sync.Mutex
	var texts []string
	ui.emit = func(name string, data ...any) {
		if name != progressEvent {
			return
		}
		mu.Lock()
		defer mu.Unlock()
		for _, item := range data {
			if p, ok := item.(UIProgress); ok {
				texts = append(texts, p.Text)
			}
		}
	}

	if _, err := ui.CollectNow(); err != nil {
		t.Fatalf("CollectNow: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(texts) == 0 {
		t.Fatal("一条进度都没推出去，界面就没得显示")
	}

	want := []string{"正在检测渠道", "正在收集 假渠道（1/1）", "正在备份"}
	from := 0
	for _, phrase := range want {
		at := indexFrom(texts, phrase, from)
		if at < 0 {
			t.Fatalf("进度里少了 %q（且要在上一步之后），实际顺序 %v", phrase, texts)
		}
		from = at + 1
	}
	if last := texts[len(texts)-1]; last != "收集完成" {
		t.Errorf("最后一条进度是 %q，想要「收集完成」", last)
	}
}

// indexFrom 从 from 开始找含 phrase 的第一条，返回它的下标；找不到返回 -1。
func indexFrom(texts []string, phrase string, from int) int {
	for i := from; i < len(texts); i++ {
		if strings.Contains(texts[i], phrase) {
			return i
		}
	}
	return -1
}

// 还没收过的时候，顶部说的是配置状态，不是上一次的结果。
func TestHeadlineStaysQuietWhenNothingToSay(t *testing.T) {
	useTempHome(t)
	readyConfig(t, t.TempDir())

	// 界面状态会解析每个已安装渠道；这条只验顶部文案，不该把真实用户目录
	// 拖进测试（尤其 ZCode 的 SQLite 可能正被产品占着）。
	ui := newTestApp(t)
	state, err := ui.State()
	if err != nil {
		t.Fatal(err)
	}
	if state.LastRun != nil {
		t.Error("这个进程还没收过，不该有运行记录（重启之后顶部不该还挂着上次的数字）")
	}
	// 一切正常时顶部**不说话**（人提的：别拿「配置就绪」这种小字占地方）；
	// 这行留给「有结果」与「出问题」两种时候。
	if state.Headline != "" {
		t.Errorf("还没收过、配置也正常时，顶部应当是空的，实际 %q", state.Headline)
	}

	// 配置真的不能用时还是要说话，而且要给人出路。
	broken := config.Default()
	broken.ArchiveDir = ""
	if got := headlineFor(broken, nil); !strings.Contains(got, "配置还没配好") {
		t.Errorf("配置不合法时要说明原因，实际 %q", got)
	}
}

// 那一行的百分比真的会跳：一个渠道按文件报进度，界面才有 0%→100%（人提的）。
func TestProgressTicksPerFileWithinAChannel(t *testing.T) {
	useTempHome(t)
	readyConfig(t, t.TempDir())

	ui := newTestApp(t, fakeChannel("fake", "假渠道", fakeSources(t, 4)))

	var mu sync.Mutex
	var got []UIProgress
	ui.emit = func(name string, data ...any) {
		if name != progressEvent {
			return
		}
		mu.Lock()
		defer mu.Unlock()
		for _, item := range data {
			if p, ok := item.(UIProgress); ok {
				got = append(got, p)
			}
		}
	}

	if _, err := ui.CollectNow(); err != nil {
		t.Fatalf("CollectNow: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	var percents []int
	finished := false
	for _, p := range got {
		if p.Channel != "fake" {
			continue
		}
		percents = append(percents, p.Percent)
		if p.Finished {
			finished = true
		}
	}
	if len(percents) < 5 {
		t.Fatalf("4 个文件应当至少报 5 次（0 起步 + 每个文件 + 收尾），实际 %v", percents)
	}
	if percents[0] != 0 || !finished {
		t.Errorf("应当从 0%% 起步、最后报「这个渠道完了」，实际 %v（finished=%v）", percents, finished)
	}
	// 中间的百分比要单调不减，并且能走到 100。
	for i := 1; i < len(percents); i++ {
		if percents[i] < percents[i-1] {
			t.Errorf("百分比不该往回走：%v", percents)
			break
		}
	}
	if percents[len(percents)-1] != 100 {
		t.Errorf("最后应当是 100%%，实际 %v", percents)
	}
	// 真要有「中间值」——不然界面上就是 0 直接跳 100。
	mid := false
	for _, p := range percents {
		if p > 0 && p < 100 {
			mid = true
		}
	}
	if !mid {
		t.Errorf("界面要看到中间值（0 和 100 之间），实际 %v", percents)
	}
}
