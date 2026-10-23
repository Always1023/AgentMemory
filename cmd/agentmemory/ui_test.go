package main

import (
	"bytes"
	"errors"
	"io"
	"log/slog"
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

// quietLogger 让界面用例不往测试输出里喷日志。
func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelError}))
}

// recordingAdapter 只是把源文件列出来；它一个字节都不写。
type recordingAdapter struct {
	id      string
	sources []channel.SourceFile
}

func (a recordingAdapter) Detect() channel.DetectResult {
	return channel.DetectResult{Status: channel.StatusAvailable, Root: "/根", Files: a.sources}
}

func (a recordingAdapter) Parse(sf channel.SourceFile) (channel.Session, error) {
	return channel.Session{
		Channel:    a.id,
		SourceFile: sf.Path,
		Messages:   []channel.Message{{Role: channel.RoleUser, Text: "假装说了一句"}},
	}, nil
}

// emptyAdapter 每个源文件都解析成**空会话**（打开过没说话那种）。
type emptyAdapter struct {
	id      string
	sources []channel.SourceFile
}

func (a emptyAdapter) Detect() channel.DetectResult {
	return channel.DetectResult{Status: channel.StatusAvailable, Root: "/根", Files: a.sources}
}

func (a emptyAdapter) Parse(sf channel.SourceFile) (channel.Session, error) {
	return channel.Session{Channel: a.id, SourceFile: sf.Path, Title: "空会话"}, nil
}

// contentAdapter 把文件正文当消息，让「内容变没变」影响归档；检测时重新读大小与修改时间。
type contentAdapter struct {
	id      string
	sources []channel.SourceFile
}

func (a contentAdapter) Detect() channel.DetectResult {
	files := make([]channel.SourceFile, 0, len(a.sources))
	for _, sf := range a.sources {
		info, err := os.Stat(sf.Path)
		if err != nil {
			return channel.DetectResult{Status: channel.StatusFailed, Root: "/根", Err: err}
		}
		files = append(files, channel.SourceFile{Path: sf.Path, Size: info.Size(), ModTime: info.ModTime()})
	}
	return channel.DetectResult{Status: channel.StatusAvailable, Root: "/根", Files: files}
}

func (a contentAdapter) Parse(sf channel.SourceFile) (channel.Session, error) {
	raw, err := os.ReadFile(sf.Path)
	if err != nil {
		return channel.Session{}, err
	}
	return channel.Session{
		Channel:    a.id,
		ID:         filepath.Base(sf.Path),
		Title:      "标题",
		SourceFile: sf.Path,
		Messages:   []channel.Message{{Role: channel.RoleUser, Text: strings.TrimSpace(string(raw))}},
	}, nil
}

// onlyArchived 返回归档目录里唯一那份 Markdown。
func onlyArchived(t *testing.T, root string) string {
	t.Helper()
	var found string
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(d.Name(), ".md") {
			return nil
		}
		if found != "" {
			t.Fatalf("归档不止一份：%s 与 %s", found, path)
		}
		found = path
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if found == "" {
		t.Fatal("归档目录里没有 Markdown")
	}
	return found
}

// newTestApp 造一个 App，渠道换成给的那些（不碰真实用户目录）。
func newTestApp(t *testing.T, entries ...channel.Entry) *App {
	t.Helper()
	registry := channel.NewRegistry()
	for _, entry := range entries {
		if err := registry.Register(entry); err != nil {
			t.Fatal(err)
		}
	}
	ui := NewApp(quietLogger())
	ui.deps = app.Deps{Channels: registry}
	return ui
}

// useTempHome 把配置目录指到临时目录，并返回它。
func useTempHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("AGENTMEMORY_HOME", home)
	return home
}

// 验收标准 4：引导中途关闭，下次打开能接着走。
func TestWizardResumesWhereItStopped(t *testing.T) {
	home := useTempHome(t)
	archive := filepath.Join(home, "归档")

	// 界面状态会解析每个已安装渠道；这条只验引导的步号与目录，别把真实用户目录拖进来
	// （尤其 ZCode 的 SQLite 可能正被产品占着，见 I-051）。
	ui := newTestApp(t)

	state, err := ui.State()
	if err != nil {
		t.Fatalf("State: %v", err)
	}
	if state.FirstRunDone || state.FirstRunStep != 0 {
		t.Fatalf("还没走过引导，应当是「没走完、停在第 1 步」，实际 %+v", state)
	}
	if state.FirstRunSteps != firstRunSteps {
		t.Errorf("总步数 = %d，想要 %d", state.FirstRunSteps, firstRunSteps)
	}
	if state.ArchiveDefault == "" {
		t.Error("应当给出归档目录的默认值，引导里要显示它")
	}

	// 走一步：第 1 步填归档目录，保存后停在第 2 步。
	state, err = ui.SaveWizard(WizardInput{Step: 1, ArchiveDir: archive})
	if err != nil {
		t.Fatalf("SaveWizard: %v", err)
	}
	if state.FirstRunStep != 1 || state.FirstRunDone {
		t.Errorf("第 1 步保存后应当停在第 2 步，实际 %+v", state)
	}
	if state.ArchiveDir != archive {
		t.Errorf("归档目录 = %q，想要 %q", state.ArchiveDir, archive)
	}

	// 「关掉窗口、下次打开」：换一个 App 读同一份配置。
	fresh := newTestApp(t)
	state, err = fresh.State()
	if err != nil {
		t.Fatalf("重开时 State: %v", err)
	}
	if state.FirstRunStep != 1 {
		t.Errorf("重开后应当停在第 2 步，实际第 %d 步", state.FirstRunStep)
	}
	if state.FirstRunDone {
		t.Error("还没走完，不该标成走完")
	}
	if state.ArchiveDir != archive {
		t.Errorf("重开后归档目录 = %q，想要 %q", state.ArchiveDir, archive)
	}
	if state.BackupModes == nil {
		t.Error("重开后应当给出备份方式的可选值")
	}

	// 走完。
	state, err = fresh.FinishWizard()
	if err != nil {
		t.Fatalf("FinishWizard: %v", err)
	}
	if !state.FirstRunDone {
		t.Error("走完之后应当标成走完")
	}

	// 再开一次：不再出现引导。
	again := newTestApp(t)
	state, err = again.State()
	if err != nil {
		t.Fatalf("State: %v", err)
	}
	if !state.FirstRunDone {
		t.Error("走完之后的再一次打开，不该再出现引导")
	}
}

// 配置是人也能手改的文本文件，改坏了不该把界面弄崩。
func TestWizardStateSurvivesHandEditedConfig(t *testing.T) {
	home := useTempHome(t)
	if err := os.WriteFile(filepath.Join(home, "config.json"),
		[]byte(`{"version":1,"archive_dir":"D:/x","first_run_step":99}`), 0o600); err != nil {
		t.Fatal(err)
	}

	state, err := newTestApp(t).State()
	if err != nil {
		t.Fatalf("State: %v", err)
	}
	if state.FirstRunStep < 0 || state.FirstRunStep >= firstRunSteps {
		t.Errorf("步号 = %d，应当被收进 0 到 %d 之间", state.FirstRunStep, firstRunSteps-1)
	}
}

func TestSaveWizardRejectsEmptyArchiveDir(t *testing.T) {
	useTempHome(t)
	ui := NewApp(quietLogger())

	// 先把归档目录清空，再存一次（模拟人手删掉了它）。
	cfg := config.Default()
	cfg.ArchiveDir = ""
	cfg.FirstRunDone = true
	if err := config.Save(cfg); err != nil {
		t.Fatal(err)
	}

	if _, err := ui.SaveWizard(WizardInput{Step: 1, ArchiveDir: "  "}); err == nil {
		t.Error("归档目录为空应当报错——收上来的东西得有地方放")
	}
}

// 验收标准 5：引导不改变渠道原始目录。
//
// 走一遍完整的引导，逐个比对渠道源文件的大小、修改时间与内容——一个字节都不该动。
func TestWizardNeverTouchesChannelSources(t *testing.T) {
	useTempHome(t)

	// 造两个「渠道」目录与它们的源文件。
	sourceRoot := t.TempDir()
	var sources []channel.SourceFile
	names := []string{"a.jsonl", "b.jsonl"}
	for _, name := range names {
		path := filepath.Join(sourceRoot, name)
		if err := os.WriteFile(path, []byte("（假装这是聊天记录 "+name+"）"), 0o600); err != nil {
			t.Fatal(err)
		}
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		sources = append(sources, channel.SourceFile{Path: path, Size: info.Size(), ModTime: info.ModTime()})
	}

	before := map[string][]byte{}
	for _, sf := range sources {
		raw, err := os.ReadFile(sf.Path)
		if err != nil {
			t.Fatal(err)
		}
		before[sf.Path] = raw
	}

	ui := newTestApp(t, channel.Entry{
		ID: "fake", Name: "假渠道", Adapter: recordingAdapter{id: "fake", sources: sources},
	})

	// 走一遍引导：读状态 → 存第 1 步 → 看第 2 步（渠道）→ 走完。
	if _, err := ui.State(); err != nil {
		t.Fatalf("State: %v", err)
	}
	if _, err := ui.SaveWizard(WizardInput{Step: 1, ArchiveDir: filepath.Join(t.TempDir(), "归档")}); err != nil {
		t.Fatalf("SaveWizard: %v", err)
	}
	state, err := ui.State()
	if err != nil {
		t.Fatalf("State: %v", err)
	}
	if len(state.Channels) != 1 || state.Channels[0].Found != len(sources) {
		t.Errorf("引导里应当列出这个渠道与它的 %d 个源文件，实际 %+v", len(sources), state.Channels)
	}
	if _, err := ui.FinishWizard(); err != nil {
		t.Fatalf("FinishWizard: %v", err)
	}

	// 逐个比对：大小、修改时间、内容都没变，目录里也没多出东西。
	entries, err := os.ReadDir(sourceRoot)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != len(names) {
		names := []string{}
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Errorf("渠道目录里多出了东西: %v", names)
	}
	for _, sf := range sources {
		info, err := os.Stat(sf.Path)
		if err != nil {
			t.Fatalf("源文件没了: %v", err)
		}
		if info.Size() != sf.Size || !info.ModTime().Equal(sf.ModTime) {
			t.Errorf("%s 被动过：大小 %d→%d，修改时间 %v→%v",
				sf.Path, sf.Size, info.Size(), sf.ModTime, info.ModTime())
		}
		raw, err := os.ReadFile(sf.Path)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(raw, before[sf.Path]) {
			t.Errorf("%s 的内容变了", sf.Path)
		}
	}
}

func TestChannelStatesChooseOneOfThreeDotStates(t *testing.T) {
	storetHome := useTempHome(t)
	_ = storetHome

	// 失败渠道：状态点应当是 bad，且给「重试」。
	ui := newTestApp(t,
		channel.Entry{ID: "bad", Name: "坏渠道", Adapter: failingAdapter{}},
		channel.Entry{ID: "gone", Name: "没装的", Adapter: notInstalledAdapter{}},
		channel.Entry{ID: "off", Name: "停用的", Adapter: recordingAdapter{id: "off"}},
	)

	cfg := config.Default()
	cfg.ArchiveDir = filepath.Join(t.TempDir(), "归档")
	cfg.FirstRunDone = true
	cfg.Channels = map[string]bool{"off": false}
	if err := config.Save(cfg); err != nil {
		t.Fatal(err)
	}

	state, err := ui.State()
	if err != nil {
		t.Fatalf("State: %v", err)
	}

	got := map[string]UIChannel{}
	for _, row := range state.Channels {
		got[row.ID] = row
	}
	if row := got["bad"]; row.State != "bad" || row.Action != channelActionCollect || row.Group != channelGroupFailed {
		t.Errorf("失败渠道 = %+v，想要 state=bad、给那个能点的箭头、单独一组", row)
	}
	if row := got["gone"]; row.State != "none" || row.Action != "" || row.Group != channelGroupNotInstalled {
		t.Errorf("未安装渠道 = %+v，想要 state=none、不给标记、归「未安装」", row)
	}
	if row := got["off"]; row.State != "none" || row.Enabled || row.Group != channelGroupInstalledNoSessions {
		t.Errorf("停用渠道 = %+v，想要 state=none、Enabled=false、归「已安装无会话」", row)
	}
}

// 首页渠道分组（I-066）：已安装有会话 / 已安装无会话 / 未安装三类；
// 检测失败单列一组；停用但仍有会话的留在「有会话」。后端不重排，引导页才能照旧平铺。
func TestChannelRowsCarryTheirHomeGroup(t *testing.T) {
	home := useTempHome(t)
	readyConfig(t, filepath.Join(home, "归档"))

	ui := newTestApp(t,
		fakeChannel("a", "有会话", fakeSources(t, 1)),
		channel.Entry{ID: "b", Name: "没装的", Adapter: notInstalledAdapter{}},
		channel.Entry{ID: "c", Name: "没会话", Adapter: recordingAdapter{id: "c"}},
		channel.Entry{ID: "d", Name: "坏了", Adapter: failingAdapter{}},
		fakeChannel("e", "停用但有会话", fakeSources(t, 1)),
	)

	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	cfg.Channels = map[string]bool{"e": false}
	if err := config.Save(cfg); err != nil {
		t.Fatal(err)
	}

	state, err := ui.State()
	if err != nil {
		t.Fatal(err)
	}

	want := map[string]string{
		"a": channelGroupHasSessions,
		"b": channelGroupNotInstalled,
		"c": channelGroupInstalledNoSessions,
		"d": channelGroupFailed,
		"e": channelGroupHasSessions,
	}
	for id, wantGroup := range want {
		row := findRow(t, state, id)
		if row.Group != wantGroup {
			t.Errorf("渠道 %s 的分组 = %q，想要 %q（行 %+v）", id, row.Group, wantGroup, row)
		}
	}
	if row := findRow(t, state, "c"); row.Action != "" {
		t.Errorf("已安装但没有会话的渠道不该给绿勾，行末标记 = %q", row.Action)
	}

	// 顺序是引导页要用的：首页自己分组，后端不把列表重排。
	var order []string
	for _, row := range state.Channels {
		order = append(order, row.ID)
	}
	if got := strings.Join(order, ","); got != "a,b,c,d,e" {
		t.Errorf("后端顺序要保持注册表顺序（引导页照旧平铺），实际 %s", got)
	}
	if !strings.Contains(state.ChannelStats, "总会话 2") {
		t.Errorf("顶部统计仍应包含全部渠道，实际 %q", state.ChannelStats)
	}
}

type failingAdapter struct{}

func (failingAdapter) Detect() channel.DetectResult {
	return channel.DetectResult{Status: channel.StatusFailed, Err: errors.New("读不了")}
}
func (failingAdapter) Parse(channel.SourceFile) (channel.Session, error) {
	return channel.Session{}, errors.New("不该被调用")
}

type notInstalledAdapter struct{}

func (notInstalledAdapter) Detect() channel.DetectResult {
	return channel.DetectResult{Status: channel.StatusNotInstalled}
}
func (notInstalledAdapter) Parse(channel.SourceFile) (channel.Session, error) {
	return channel.Session{}, errors.New("不该被调用")
}

// 界面文案只有一处定义：渠道那几句状态话都在 channelStates 里，
// 换说法时不会漏掉某处（I-015 验收标准 5）。
func TestChannelDetailTextHasOneDefinition(t *testing.T) {
	raw, err := os.ReadFile("ui.go")
	if err != nil {
		t.Fatal(err)
	}
	// 注释不算「定义」——文档里出现同一个词不等于多了一处文案。
	source := stripLineComments(string(raw))

	for _, phrase := range []string{`"未检测到"`, `"已停用"`, `"会话 %d · 有效 %d"`, `解析失败 %d 个`} {
		if n := strings.Count(source, phrase); n != 1 {
			t.Errorf("%s 在 ui.go 里出现了 %d 次（注释不算），状态文案应当只有一处定义", phrase, n)
		}
	}
}

// stripLineComments 去掉 // 起的行内注释与整行注释。
//
// 这个文件里没有 URL 之类的「字符串里带 //」，所以按行切足够了。
func stripLineComments(source string) string {
	var out strings.Builder
	for _, line := range strings.Split(source, "\n") {
		if i := strings.Index(line, "//"); i >= 0 {
			line = line[:i]
		}
		out.WriteString(line)
		out.WriteString("\n")
	}
	return out.String()
}

// 统计（I-023）：两层各说各的；数与目录一致；「今天」按落盘日期算。
func TestStatsKeepTheTwoLayersApart(t *testing.T) {
	home := useTempHome(t)
	archiveDir := filepath.Join(home, "归档")
	readyConfig(t, archiveDir)

	// 本机层：两份源文件，把其中一份改成昨天落的盘。
	sources := fakeSources(t, 2)
	yesterday := time.Now().AddDate(0, 0, -1)
	if err := os.Chtimes(sources[1].Path, yesterday, yesterday); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(sources[1].Path)
	if err != nil {
		t.Fatal(err)
	}
	sources[1].ModTime = info.ModTime()

	ui := newTestApp(t, fakeChannel("fake", "假渠道", sources))

	// 已收集层：这个渠道（目录名取渠道标识）里两份，其中一份是今天落的。
	dir := filepath.Join(archiveDir, "fake", "proj")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	put := func(name string, at time.Time) {
		t.Helper()
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte("# 会话"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(path, at, at); err != nil {
			t.Fatal(err)
		}
	}
	put("2026-10-16-甲-aaaaaaaaaa.md", time.Now())
	put("2026-10-15-乙-bbbbbbbbbb.md", yesterday)

	state, err := ui.State()
	if err != nil {
		t.Fatal(err)
	}

	if !strings.Contains(state.ChannelStats, "总会话 2") || !strings.Contains(state.ChannelStats, "有效会话 2") ||
		!strings.Contains(state.ChannelStats, "已收集 2 段") {
		t.Errorf("渠道那段顶上那句话不对：%q", state.ChannelStats)
	}

	row := findRow(t, state, "fake")
	if row.Found != 2 || row.HostToday != 1 {
		t.Errorf("那一行的本机层应当是 发现 2 / 今日新增 1，实际 发现 %d / 今日 %d", row.Found, row.HostToday)
	}
	if row.Collected != 2 || row.CollectedToday != 1 {
		t.Errorf("那一行的已收集层应当是 2 份 / 今日保存 1 份，实际 %d / %d", row.Collected, row.CollectedToday)
	}
	if row.Valid != 2 {
		t.Errorf("有效会话应当来自本机解析（2），实际 %d", row.Valid)
	}
	if !strings.Contains(row.Detail, "有效 2") {
		t.Errorf("渠道那一行要能看出有效会话多少，实际 %q", row.Detail)
	}
	// 待收数的是「还没归档过的」，不是「本机减归档」的差额（I-039）。
	// 这个用例没跑过收集，所以两个文件都还没进游标 → 两个都算新会话。
	if row.PendingNew != 2 || row.PendingUpdated != 0 || !strings.Contains(row.Detail, "待收 2 个新会话") {
		t.Errorf("没跑过收集时两个文件都该算新会话，实际 新 %d / 更新 %d / %q",
			row.PendingNew, row.PendingUpdated, row.Detail)
	}

	// 收过一轮：都进了游标 → 待收归零；再冒出一个新文件 → 新会话 1。
	first := newTestApp(t, fakeChannel("fake", "假渠道", sources))
	collected, err := first.CollectNow()
	if err != nil {
		t.Fatal(err)
	}
	if row := findRow(t, collected, "fake"); row.PendingNew != 0 || row.PendingUpdated != 0 {
		t.Errorf("刚收完应当是待收 0，实际 新 %d / 更新 %d（说明 %q）",
			row.PendingNew, row.PendingUpdated, row.Detail)
	}

	// 另造一个 App（假适配器拿的是构造时那一份，改切片对它没用）。
	threeSources := append(append([]channel.SourceFile{}, sources...),
		channel.SourceFile{Path: "额外.jsonl", Size: 1, ModTime: time.Now()})
	another := newTestApp(t, fakeChannel("fake", "假渠道", threeSources))
	again, err := another.State()
	if err != nil {
		t.Fatal(err)
	}
	more := findRow(t, again, "fake")
	if more.Found != 3 || more.PendingNew != 1 || more.PendingUpdated != 0 ||
		!strings.Contains(more.Detail, "待收 1 个新会话") {
		t.Errorf("新冒出来的那个文件应当是 1 个新会话，实际 发现 %d / 新 %d / 更新 %d",
			more.Found, more.PendingNew, more.PendingUpdated)
	}

	// 换一个干净的归档目录：已收集回到 0，但有效会话仍然来自本机解析，不能跟着变成 0。
	if err := os.RemoveAll(archiveDir); err != nil {
		t.Fatal(err)
	}
	empty, err := ui.State()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(empty.ChannelStats, "有效会话 2") || !strings.Contains(empty.ChannelStats, "已收集 0 段") {
		t.Errorf("归档目录空了之后有效会话仍应是 2、已收集是 0 段，实际 %q", empty.ChannelStats)
	}
	if findRow(t, empty, "fake").Collected != 0 {
		t.Error("归档目录空了之后，那一行的已收集应当是 0")
	}
}

// 渠道行末尾那两种状态（人提的）：还有没转移的给能点的箭头，没有新内容给绿勾。
func TestChannelMarkHasTwoStates(t *testing.T) {
	home := useTempHome(t)
	archiveDir := filepath.Join(home, "归档")
	readyConfig(t, archiveDir)

	ui := newTestApp(t, fakeChannel("fake", "假渠道", fakeSources(t, 2)))

	// 归档里一份都没有：还有两个没收 → 能点的箭头。
	state, err := ui.State()
	if err != nil {
		t.Fatal(err)
	}
	if row := findRow(t, state, "fake"); row.Action != channelActionCollect || row.PendingNew != 2 || row.PendingUpdated != 0 {
		t.Errorf("还有没收的时候该给能点的箭头，实际 %+v", row)
	}

	// 把两份都收进归档：没有新内容 → 绿勾。
	if _, err := ui.CollectNow(); err != nil {
		t.Fatalf("CollectNow: %v", err)
	}
	state, err = ui.State()
	if err != nil {
		t.Fatal(err)
	}
	if row := findRow(t, state, "fake"); row.Action != channelActionDone || row.PendingNew != 0 || row.PendingUpdated != 0 {
		t.Errorf("没有新内容的时候该给绿勾，实际 %+v", row)
	}
}

// 只有修改时间被动过、内容没变：不算待收，点收集也不重写归档（I-062）。
func TestModTimeOnlyChangeIsNotPending(t *testing.T) {
	home := useTempHome(t)
	archiveDir := filepath.Join(home, "归档")
	readyConfig(t, archiveDir)

	sources := fakeSources(t, 1)
	ui := newTestApp(t, channel.Entry{ID: "fake", Name: "假渠道", Adapter: contentAdapter{id: "fake", sources: sources}})
	if _, err := ui.CollectNow(); err != nil {
		t.Fatalf("CollectNow: %v", err)
	}
	archivePath := onlyArchived(t, archiveDir)
	before, err := os.Stat(archivePath)
	if err != nil {
		t.Fatal(err)
	}

	// 只动修改时间，字节一个不动。
	touched := time.Now().Add(2 * time.Hour)
	if err := os.Chtimes(sources[0].Path, touched, touched); err != nil {
		t.Fatal(err)
	}

	state, err := ui.State()
	if err != nil {
		t.Fatal(err)
	}
	row := findRow(t, state, "fake")
	if row.PendingNew != 0 || row.PendingUpdated != 0 {
		t.Errorf("只有修改时间变化时不该算待收，实际 新 %d / 更新 %d（说明 %q）",
			row.PendingNew, row.PendingUpdated, row.Detail)
	}
	if row.Action != channelActionDone {
		t.Errorf("没有活的时候该给绿勾，实际 %q", row.Action)
	}
	if strings.Contains(row.Detail, "待收") {
		t.Errorf("没有待收时不该挂一句「待收」：%q", row.Detail)
	}

	again, err := ui.CollectNow()
	if err != nil {
		t.Fatalf("第二次 CollectNow: %v", err)
	}
	if again.LastRun == nil || again.LastRun.New != 0 || again.LastRun.Archived != 0 {
		t.Errorf("内容没变时不该重新收，实际 %+v", again.LastRun)
	}
	after, err := os.Stat(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	if !after.ModTime().Equal(before.ModTime()) {
		t.Error("内容没变时不该重写归档")
	}
}

// 待收拆成「新会话」与「已收集有更新」，渠道行和顶部汇总都说清（I-062）。
func TestPendingShowsNewAndUpdatedSeparately(t *testing.T) {
	home := useTempHome(t)
	archiveDir := filepath.Join(home, "归档")
	readyConfig(t, archiveDir)

	sources := fakeSources(t, 3)

	// 先只让前两个被发现并收下。
	first := newTestApp(t, channel.Entry{ID: "fake", Name: "假渠道", Adapter: contentAdapter{id: "fake", sources: sources[:2]}})
	if _, err := first.CollectNow(); err != nil {
		t.Fatalf("CollectNow: %v", err)
	}

	// 第一个追加内容（已收集会话有更新），第三个是新冒出来的。
	if err := os.WriteFile(sources[0].Path, []byte("（假装有内容）又补了一句\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	next := newTestApp(t, channel.Entry{ID: "fake", Name: "假渠道", Adapter: contentAdapter{id: "fake", sources: sources}})
	state, err := next.State()
	if err != nil {
		t.Fatal(err)
	}
	row := findRow(t, state, "fake")
	if row.PendingNew != 1 || row.PendingUpdated != 1 || row.Action != channelActionCollect {
		t.Errorf("应当是 1 个新会话 + 1 个有更新、箭头可点，实际 %+v", row)
	}
	const want = "待收 1 个新会话 + 1 个已收集会话有更新"
	if !strings.Contains(row.Detail, want) {
		t.Errorf("渠道行要说清两类待收，实际 %q", row.Detail)
	}
	if !strings.Contains(state.ChannelStats, want) {
		t.Errorf("顶部汇总要用同一套口径，实际 %q", state.ChannelStats)
	}

	after, err := next.CollectNow()
	if err != nil {
		t.Fatalf("CollectNow: %v", err)
	}
	if after.LastRun == nil || after.LastRun.New != 2 || after.LastRun.Archived != 2 {
		t.Errorf("两件活都该收下，实际 %+v", after.LastRun)
	}
	row = findRow(t, after, "fake")
	if row.PendingNew != 0 || row.PendingUpdated != 0 || row.Action != channelActionDone {
		t.Errorf("收完之后待收应当归零、给绿勾，实际 %+v", row)
	}
}

// 空会话（打开过没说话）：不算失败、不归档，界面上跟「有效会话」分开说（I-039）。
func TestEmptySessionsAreSkippedNotFailed(t *testing.T) {
	home := useTempHome(t)
	archiveDir := filepath.Join(home, "归档")
	readyConfig(t, archiveDir)

	ui := newTestApp(t, channel.Entry{ID: "fake", Name: "假渠道", Adapter: emptyAdapter{id: "fake", sources: fakeSources(t, 3)}})

	state, err := ui.CollectNow()
	if err != nil {
		t.Fatalf("CollectNow: %v", err)
	}
	if state.LastRun == nil {
		t.Fatal("收完应当留下运行记录")
	}
	if state.LastRun.Failures != 0 || state.LastRun.Empty != 3 {
		t.Errorf("空会话既不算失败也不算收下：失败 %d / 空会话 %d，想要 0 / 3",
			state.LastRun.Failures, state.LastRun.Empty)
	}
	if strings.Contains(state.Headline, "失败") {
		t.Errorf("空会话不该说成失败，实际 %q", state.Headline)
	}
	if !strings.Contains(state.Headline, "跳过 3 个空会话") {
		t.Errorf("顶部要说明跳过了几个空会话，实际 %q", state.Headline)
	}
	if _, err := os.Stat(archiveDir); err == nil {
		if n := countArchived(t, archiveDir); n != 0 {
			t.Errorf("空会话不该产生归档文件，实际落了 %d 个", n)
		}
	}
	if _, err := os.Stat(filepath.Join(archiveDir, "fake")); err == nil {
		t.Error("空会话那一轮不该在归档目录里建出这个渠道的目录")
	}

	// 再次收集：空会话记在游标里了，不该再被当成「新增」重来一遍。
	again, err := ui.CollectNow()
	if err != nil {
		t.Fatalf("第二次 CollectNow: %v", err)
	}
	if again.LastRun.New != 0 || again.LastRun.Empty != 0 {
		t.Errorf("第二次不该再有新增或空会话，实际 新增 %d / 空会话 %d", again.LastRun.New, again.LastRun.Empty)
	}
	row := findRow(t, again, "fake")
	if row.PendingNew != 0 || row.PendingUpdated != 0 {
		t.Errorf("空会话处理过之后不该还算待收，实际 新 %d / 更新 %d（说明 %q）",
			row.PendingNew, row.PendingUpdated, row.Detail)
	}
	if !strings.Contains(row.Detail, "会话 3") || !strings.Contains(row.Detail, "有效 0") {
		t.Errorf("那一行要给「会话 / 有效」两个数，实际 %q", row.Detail)
	}
	if !strings.Contains(row.Detail, "空 3") {
		t.Errorf("空会话要单独计数，实际 %q", row.Detail)
	}
}

// 有效会话由本机解析结果决定，不从归档目录倒推；坏文件也不能把其余有效数抹掉（I-051）。
func TestValidSessionsComeFromParsingNotArchive(t *testing.T) {
	home := useTempHome(t)
	readyConfig(t, filepath.Join(home, "归档"))

	files := fakeSources(t, 3)
	adapter := brokenAdapter{id: "fake", sources: files, bad: map[string]bool{files[2].Path: true}}
	ui := newTestApp(t, channel.Entry{ID: "fake", Name: "假渠道", Adapter: adapter})

	state, err := ui.State()
	if err != nil {
		t.Fatalf("State: %v", err)
	}
	row := findRow(t, state, "fake")
	if row.Valid != 2 || row.ParseFailed != 1 || row.Collected != 0 {
		t.Errorf("有效 / 读不动 / 已收集 = %d / %d / %d，想要 2 / 1 / 0（还没收集）",
			row.Valid, row.ParseFailed, row.Collected)
	}
	if !strings.Contains(state.ChannelStats, "有效会话 2") || !strings.Contains(state.ChannelStats, "已收集 0 段") {
		t.Errorf("顶部也要区分有效与已收集，实际 %q", state.ChannelStats)
	}
	if !strings.Contains(row.Detail, "有效 2") || !strings.Contains(row.Detail, "读不动 1 段") {
		t.Errorf("这一行要同时保留有效数与读不动的数，实际 %q", row.Detail)
	}
}

// 空会话与坏文件混在同一渠道时，各数各的：有效只数非空会话（I-051）。
func TestCensusSeparatesEmptyAndBrokenSessions(t *testing.T) {
	useTempHome(t)
	readyConfig(t, t.TempDir())

	files := fakeSources(t, 3)
	ui := newTestApp(t, channel.Entry{
		ID:   "fake",
		Name: "假渠道",
		Adapter: mixedAdapter{
			id:      "fake",
			sources: files,
			empty:   map[string]bool{files[1].Path: true},
			bad:     map[string]bool{files[2].Path: true},
		},
	})

	state, err := ui.State()
	if err != nil {
		t.Fatalf("State: %v", err)
	}
	row := findRow(t, state, "fake")
	if row.Valid != 1 || row.Empty != 1 || row.ParseFailed != 1 {
		t.Errorf("有效 / 空 / 失败 = %d / %d / %d，想要 1 / 1 / 1", row.Valid, row.Empty, row.ParseFailed)
	}
	if !strings.Contains(state.ChannelStats, "有效会话 1") ||
		!strings.Contains(state.ChannelStats, "空会话 1") ||
		!strings.Contains(state.ChannelStats, "读不动 1 段") {
		t.Errorf("顶部要分别列出有效、空会话与读不动，实际 %q", state.ChannelStats)
	}
}

type mixedAdapter struct {
	id      string
	sources []channel.SourceFile
	empty   map[string]bool
	bad     map[string]bool
}

func (a mixedAdapter) Detect() channel.DetectResult {
	return channel.DetectResult{Status: channel.StatusAvailable, Root: "/根", Files: a.sources}
}

func (a mixedAdapter) Parse(sf channel.SourceFile) (channel.Session, error) {
	if a.bad[sf.Path] {
		return channel.Session{}, errTestBrokenFile
	}
	if a.empty[sf.Path] {
		return channel.Session{Channel: a.id, SourceFile: sf.Path, Title: "空会话"}, nil
	}
	return channel.Session{
		Channel:    a.id,
		SourceFile: sf.Path,
		Messages:   []channel.Message{{Role: channel.RoleUser, Text: "有效"}},
	}, nil
}

// 首次引导没走完时，主界面不该再挂一句提示（I-075）。
func TestHeadlineDoesNotNagAboutUnfinishedWizard(t *testing.T) {
	cfg := config.Default()
	cfg.FirstRunDone = false
	cfg.ArchiveDir = t.TempDir()

	if got := headlineFor(cfg, nil); got != "" {
		t.Errorf("首次引导未完成时主界面不该再挂提示，实际 %q", got)
	}
}

// 读取统计会报渠道进度并以完成事件收尾；读取不是收集，不改游标（I-051）。
// 重试失败的顶部句：「重试 X 没能完成」中间要有空格（I-052）。
func TestRetryFailureHeadlineKeepsSpace(t *testing.T) {
	got := headlineFor(config.Config{ArchiveDir: `D:\archives`}, &UIRun{Scope: "Claude Code", Err: "归档目录写不进去"})
	if !strings.Contains(got, "重试 Claude Code 没能完成") {
		t.Errorf("headline = %q，想要「重试 Claude Code 没能完成」", got)
	}
}

func TestStateWithProgressReportsReadingProgress(t *testing.T) {
	useTempHome(t)
	readyConfig(t, t.TempDir())

	ui := newTestApp(t,
		fakeChannel("a", "渠道甲", fakeSources(t, 2)),
		fakeChannel("b", "渠道乙", fakeSources(t, 1)),
	)

	var mu sync.Mutex
	var events []string
	ui.emit = func(name string, data ...any) {
		mu.Lock()
		defer mu.Unlock()
		if name == readingStartedEvent || name == readingProgressEvent || name == readingFinishedEvent {
			events = append(events, name)
		}
	}

	state, err := ui.StateWithProgress()
	if err != nil {
		t.Fatalf("StateWithProgress: %v", err)
	}
	if !state.Reading {
		t.Error("读取路径交回的状态要标成 Reading")
	}
	mu.Lock()
	defer mu.Unlock()
	if len(events) < 4 || events[0] != readingStartedEvent || events[len(events)-1] != readingFinishedEvent {
		t.Errorf("读取进度应当从开始事件走到完成事件，实际 %v", events)
	}
}

// 进度映射：那一行要看得到 0% 到 100%（人提的：不然以为卡住了）。
func TestProgressCarriesRowPercent(t *testing.T) {
	base := app.Progress{Stage: app.StageChannel, ChannelID: "claude-code", FileTotal: 4}

	got := progressToUI(app.Progress{Stage: base.Stage, ChannelID: base.ChannelID, FileTotal: 4, FileDone: 0})
	if got.Percent != 0 || got.Finished {
		t.Errorf("刚开始应当是 0%%、还没完，实际 %+v", got)
	}
	got = progressToUI(app.Progress{Stage: base.Stage, ChannelID: base.ChannelID, FileTotal: 4, FileDone: 3})
	if got.Percent != 75 {
		t.Errorf("处理完 3/4 应当是 75%%，实际 %+v", got)
	}
	got = progressToUI(app.Progress{Stage: base.Stage, ChannelID: base.ChannelID, FileTotal: 4, FileDone: 4, ChannelDone: true})
	if got.Percent != 100 || !got.Finished {
		t.Errorf("走完了应当是 100%% 且 Finished，实际 %+v", got)
	}

	// 检测 / 备份那几步跟某一行无关：percent 给 -1，界面别去动任何一行。
	for _, stage := range []app.Stage{app.StageDetect, app.StageBackup, app.StageDone} {
		if got := progressToUI(app.Progress{Stage: stage}); got.Percent != -1 {
			t.Errorf("%s 这一步不该带那一行的数字，实际 %+v", stage, got)
		}
	}
}

// Census 的解析错误属于单条数据错，不该让整个统计读取失败（AC-05、I-051）。
func TestCensusKeepsGoingAfterOneBrokenFile(t *testing.T) {
	useTempHome(t)
	readyConfig(t, t.TempDir())

	files := fakeSources(t, 2)
	registry := channel.NewRegistry()
	if err := registry.Register(channel.Entry{
		ID:      "fake",
		Name:    "假渠道",
		Adapter: brokenAdapter{id: "fake", sources: files, bad: map[string]bool{files[0].Path: true}},
	}); err != nil {
		t.Fatal(err)
	}

	got, err := app.Census(app.Deps{Channels: registry}, nil)
	if err != nil {
		t.Fatalf("一个坏文件不该让整个统计失败: %v", err)
	}
	if got.Valid["fake"] != 1 || got.Failed["fake"] != 1 || got.Empty["fake"] != 0 {
		t.Errorf("有效 / 失败 / 空 = %d / %d / %d，想要 1 / 1 / 0",
			got.Valid["fake"], got.Failed["fake"], got.Empty["fake"])
	}
}

// 读取过场的百分比要在同一个渠道内按文件走，不能整段停在 0（I-056）。
func TestReadingProgressMovesWithinAChannel(t *testing.T) {
	useTempHome(t)
	readyConfig(t, t.TempDir())

	ui := newTestApp(t, fakeChannel("a", "渠道甲", fakeSources(t, 4)))
	var percents []int
	ui.emit = func(name string, data ...any) {
		if name != readingProgressEvent || len(data) == 0 {
			return
		}
		if p, ok := data[0].(UIReadingProgress); ok {
			percents = append(percents, p.Percent)
		}
	}

	if _, err := ui.StateWithProgress(); err != nil {
		t.Fatalf("StateWithProgress: %v", err)
	}

	sawMiddle := false
	for _, p := range percents {
		if p > 0 && p < 100 {
			sawMiddle = true
		}
	}
	if !sawMiddle {
		t.Errorf("读取过场应当在 0 与 100 之间推进，实际百分比 %v", percents)
	}
	if len(percents) == 0 || percents[len(percents)-1] != 100 {
		t.Errorf("最后一个渠道读完应当是 100，实际 %v", percents)
	}
}
