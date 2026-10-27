package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Always1023/AgentMemory/internal/app"
	"github.com/Always1023/AgentMemory/internal/archive"
	"github.com/Always1023/AgentMemory/internal/channel"
	"github.com/Always1023/AgentMemory/internal/config"
	"github.com/Always1023/AgentMemory/internal/store"
)

// ── 高级设置抽屉（I-017）──────────────────────────────────────────────────
//
// 抽屉是界面上的东西（人验收），这里盯的是它背后的机制：
// 改完真的写进配置、真的落到系统里，以及系统里到底是什么样——如实报，不靠配置猜。

// settingsApp 造一个 App：系统的定时任务换成假的，别的照常。
func settingsApp(t *testing.T, fake *fakeSchtasks) *App {
	t.Helper()
	ui := NewApp(quietLogger())
	ui.schedule = fake.run
	return ui
}

// depsFor 造一份只含这些渠道的依赖（不碰真实用户目录）。
func depsFor(t *testing.T, entries ...channel.Entry) app.Deps {
	t.Helper()
	registry := channel.NewRegistry()
	for _, entry := range entries {
		if err := registry.Register(entry); err != nil {
			t.Fatal(err)
		}
	}
	return app.Deps{Channels: registry}
}

// 验收标准 1（机制侧）：抽屉里改了归档目录，下一次收集就落到新目录。
func TestSaveSettingsArchiveDirTakesEffectOnNextCollect(t *testing.T) {
	useTempHome(t)
	oldDir := t.TempDir()
	readyConfig(t, oldDir)

	ui := settingsApp(t, &fakeSchtasks{missingErr: exitError(t, 1)})
	sources := fakeSources(t, 1)
	ui.deps = depsFor(t, fakeChannel("fake", "假渠道", sources))

	// 先收一轮，落到老目录。
	if _, err := ui.CollectNow(); err != nil {
		t.Fatalf("第一轮: %v", err)
	}
	if n := countArchived(t, oldDir); n != 1 {
		t.Fatalf("老目录里应当有 1 份，实际 %d", n)
	}

	// 让源文件变一下（人又聊了两句）：下一轮才会真的再写一次归档。
	//
	// 改了内容还要把新的「大小 + 修改时间」报到适配器里：真实适配器每次 Detect
	// 都会重新看一眼文件，这里的假适配器照模板回答，得手动跟上。
	if err := os.WriteFile(sources[0].Path, []byte("（假装又聊了两句，比刚才长一点）"), 0o600); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(sources[0].Path)
	if err != nil {
		t.Fatal(err)
	}
	sources[0].Size, sources[0].ModTime = info.Size(), info.ModTime()

	newDir := t.TempDir()
	settings, err := ui.SaveSettings(SettingsInput{ArchiveDir: newDir})
	if err != nil {
		t.Fatalf("保存: %v", err)
	}
	if settings.ArchiveDir != newDir {
		t.Errorf("保存后读回来的是 %q，想要 %q", settings.ArchiveDir, newDir)
	}

	// 再收一轮：新目录里出现，老目录不动。
	if _, err := ui.CollectNow(); err != nil {
		t.Fatalf("第二轮: %v", err)
	}
	if n := countArchived(t, newDir); n != 1 {
		t.Errorf("新目录里应当有 1 份（改完就生效，不用重启），实际 %d", n)
	}
	if n := countArchived(t, oldDir); n != 1 {
		t.Errorf("老目录里应当还是 1 份，实际 %d", n)
	}
}

// 验收标准 2（机制侧）：备份方式切开之后立即生效——下一次收集走的就是新方式。
func TestSaveSettingsBackupModeTakesEffectOnNextCollect(t *testing.T) {
	useTempHome(t)
	readyConfig(t, t.TempDir())

	ui := settingsApp(t, &fakeSchtasks{missingErr: exitError(t, 1)})
	ui.deps = depsFor(t, fakeChannel("fake", "假渠道", fakeSources(t, 1)))

	// 一开始配置里是「仅本地」（readyConfig 就是这么写的）。
	state, err := ui.CollectNow()
	if err != nil {
		t.Fatalf("第一轮: %v", err)
	}
	if state.LastRun.BackupDetail == "" {
		t.Fatal("收完应当报一句备份结果")
	}

	if _, err := ui.SaveSettings(SettingsInput{BackupMode: config.BackupNone}); err != nil {
		t.Fatalf("切到仅本地: %v", err)
	}
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Backup.Mode != config.BackupNone {
		t.Errorf("配置里的备份方式是 %q，想要 %q", cfg.Backup.Mode, config.BackupNone)
	}

	state, err = ui.CollectNow()
	if err != nil {
		t.Fatalf("第二轮: %v", err)
	}
	if state.LastRun.BackupDetail == "" {
		t.Error("收完应当报一句备份结果——「立即生效」就是这一轮真的走了新方式")
	}

	// 不认识的备份方式要当场拒绝，不能写进配置。
	if _, err := ui.SaveSettings(SettingsInput{BackupMode: "并不存在的方式"}); err == nil {
		t.Error("不认识的备份方式应当被拒")
	}
}

// 验收标准 3（机制侧）：开关定时会把系统里的任务建起来 / 删掉，
// 而抽屉里显示的下次运行时间是从**系统里**读回来的。
func TestSaveSettingsScheduleAlignsTheSystemTask(t *testing.T) {
	useTempHome(t)
	readyConfig(t, t.TempDir())

	fake := &fakeSchtasks{missingErr: exitError(t, 1)}
	ui := settingsApp(t, fake)

	// 一开始系统里没有任务。
	before, err := ui.Settings()
	if err != nil {
		t.Fatalf("读设置: %v", err)
	}
	if before.ScheduleInstalled {
		t.Error("系统里本来没有任务，不该说装好了")
	}
	if !strings.Contains(before.ScheduleNote, "没开定时") {
		t.Errorf("没开定时时那句说明不对：%q", before.ScheduleNote)
	}

	// 打开：按 07:30 建。
	on := true
	after, err := ui.SaveSettings(SettingsInput{ScheduleEnabled: &on, ScheduleDailyAt: "07:30"})
	if err != nil {
		t.Fatalf("开定时: %v", err)
	}
	if !fake.calledWith("/Create", "/SC", "DAILY", "/ST", "07:30") {
		t.Errorf("应当按 07:30 建任务，实际调用 %v", fake.calls)
	}
	if !after.ScheduleInstalled {
		t.Error("建完之后读回来应当说系统里有这个任务")
	}
	if after.ScheduleNextRun == "" {
		t.Error("应当报出下次运行时间")
	}
	next, parseErr := time.ParseInLocation("2006-01-02 15:04", after.ScheduleNextRun, time.Local)
	if parseErr != nil {
		t.Fatalf("下次运行时间 %q 读不出来：%v", after.ScheduleNextRun, parseErr)
	}
	if !next.After(time.Now()) || next.Sub(time.Now()) > 24*time.Hour {
		t.Errorf("下次运行时间 %v 应当是将来 24 小时以内", next)
	}
	if next.Hour() != 7 || next.Minute() != 30 {
		t.Errorf("下次运行时间 %v 应当是 07:30", next)
	}
	if after.ScheduleNote != "" {
		t.Errorf("一切正常时不该有那句提醒，实际 %q", after.ScheduleNote)
	}

	// 明明配置里开着、系统里却没有：要如实说出来，不能拿配置糊弄人。
	fake.installed = false
	drifted, err := ui.Settings()
	if err != nil {
		t.Fatalf("再读设置: %v", err)
	}
	if drifted.ScheduleInstalled {
		t.Error("系统里已经没有这个任务了，不该还说有")
	}
	if !strings.Contains(drifted.ScheduleNote, "系统里没有这个任务") {
		t.Errorf("配置与系统对不上时要说出来，实际 %q", drifted.ScheduleNote)
	}

	// 关掉：系统里的任务要删掉。
	off := false
	fake.installed = true
	closed, err := ui.SaveSettings(SettingsInput{ScheduleEnabled: &off})
	if err != nil {
		t.Fatalf("关定时: %v", err)
	}
	if !fake.calledWith("/Delete", "/TN", taskName) {
		t.Errorf("关定时应当删掉任务，实际调用 %v", fake.calls)
	}
	if closed.ScheduleInstalled {
		t.Error("删完之后不该还说系统里有这个任务")
	}
	if !strings.Contains(closed.ScheduleNote, "没开定时") {
		t.Errorf("关定时之后那句说明不对：%q", closed.ScheduleNote)
	}
}

// 时刻不合法：当场拒绝，别拿它去动系统。
func TestSaveSettingsRejectsABadTime(t *testing.T) {
	useTempHome(t)
	readyConfig(t, t.TempDir())

	fake := &fakeSchtasks{missingErr: exitError(t, 1)}
	ui := settingsApp(t, fake)

	if _, err := ui.SaveSettings(SettingsInput{ScheduleDailyAt: "25:00"}); err == nil {
		t.Error("25:00 不该被接受")
	}
	if fake.calledWith("/Create") {
		t.Error("时刻不合法时不该去动系统")
	}
}

// 建任务失败：要把原因交出来，同时**不假装开好了**——配置留着意图，界面显示系统里没有。
func TestSaveSettingsScheduleFailureIsReportedButIntentKept(t *testing.T) {
	useTempHome(t)
	readyConfig(t, t.TempDir())

	fake := &fakeSchtasks{missingErr: exitError(t, 1), createErr: exitError(t, 1), createOut: "ERROR: Access is denied."}
	ui := settingsApp(t, fake)

	on := true
	settings, err := ui.SaveSettings(SettingsInput{ScheduleEnabled: &on, ScheduleDailyAt: "06:00"})
	if err == nil {
		t.Fatal("建任务失败必须报错（静默失败会让人以为定时开好了）")
	}
	if !strings.Contains(err.Error(), "建定时任务失败") {
		t.Errorf("错误要说清是哪一步失败，实际 %v", err)
	}
	if settings.ScheduleEnabled != true {
		t.Error("配置里应当留着「人想开定时」这个意图，下次保存再试")
	}
	if settings.ScheduleInstalled {
		t.Error("系统里其实没建成，不该说装好了")
	}
	if !strings.Contains(settings.ScheduleNote, "系统里没有这个任务") {
		t.Errorf("要向人显示系统里的真实情况，实际 %q", settings.ScheduleNote)
	}
}

// 设置页的定时区块：上次运行只认定时那一条，手动收集不改变它（I-073）。
func TestSettingsShowsTheLastScheduledRun(t *testing.T) {
	home := useTempHome(t)
	readyConfig(t, t.TempDir())

	now := time.Now()
	runDB, err := store.OpenDB(home)
	if err != nil {
		t.Fatal(err)
	}
	defer runDB.Close()
	scheduled := now.Add(-3 * time.Hour).Truncate(time.Minute)
	manual := scheduled.Add(time.Hour)
	for _, run := range []store.Run{
		{At: scheduled, Source: runSourceSchedule},
		{At: manual, Source: runSourceManual},
	} {
		if err := runDB.AppendRun(run, runLogKeepDays, now); err != nil {
			t.Fatalf("写运行记录: %v", err)
		}
	}

	settings, err := settingsApp(t, &fakeSchtasks{missingErr: exitError(t, 1)}).Settings()
	if err != nil {
		t.Fatalf("读设置: %v", err)
	}
	want := scheduled.In(time.Local).Format("2006-01-02 15:04")
	if settings.ScheduleLastRun != want {
		t.Errorf("上次运行应当是最近一次定时记录 %q，实际 %q", want, settings.ScheduleLastRun)
	}
}

// 没有定时记录时要说「还没有记录」，不能借用最近一次手动收集（I-073）。
func TestSettingsSaysWhenThereIsNoScheduledRun(t *testing.T) {
	home := useTempHome(t)
	readyConfig(t, t.TempDir())

	now := time.Now()
	runDB, err := store.OpenDB(home)
	if err != nil {
		t.Fatal(err)
	}
	defer runDB.Close()
	if err := runDB.AppendRun(store.Run{At: now, Source: runSourceManual}, runLogKeepDays, now); err != nil {
		t.Fatal(err)
	}

	settings, err := settingsApp(t, &fakeSchtasks{missingErr: exitError(t, 1)}).Settings()
	if err != nil {
		t.Fatal(err)
	}
	if settings.ScheduleLastRun != scheduleLastRunNone {
		t.Errorf("只有手动记录时该说 %q，实际 %q", scheduleLastRunNone, settings.ScheduleLastRun)
	}
}

// 运行记录读不动：如实说读不到，设置页别的项照常打开（I-073）。
func TestSettingsSurvivesABrokenRunLog(t *testing.T) {
	home := useTempHome(t)
	readyConfig(t, t.TempDir())
	// 把主库占成一个坏文件：读它必定失败。
	if err := os.WriteFile(filepath.Join(home, store.DBFileName), []byte("这不是一个库"), 0o600); err != nil {
		t.Fatal(err)
	}

	settings, err := settingsApp(t, &fakeSchtasks{missingErr: exitError(t, 1)}).Settings()
	if err != nil {
		t.Fatalf("运行记录读不动，设置页也该照常打开：%v", err)
	}
	if settings.ScheduleLastRun != scheduleLastRunUnreadable {
		t.Errorf("该说 %q，实际 %q", scheduleLastRunUnreadable, settings.ScheduleLastRun)
	}
	if settings.ArchiveDir == "" {
		t.Error("运行记录读不动，别的项照样要显示")
	}
}

// 读系统里的任务失败（不是「没有」，是「读不了」）：抽屉还开得起来，但那句话要说清。
func TestSettingsSurvivesASchedulerReadFailure(t *testing.T) {
	useTempHome(t)
	readyConfig(t, t.TempDir())

	ui := settingsApp(t, &fakeSchtasks{})
	// 让查询报一个「不是查不到」的错：退出码 2。
	ui.schedule = func(...string) (string, error) { return "", exitError(t, 2) }

	settings, err := ui.Settings()
	if err != nil {
		t.Fatalf("读不了系统里的任务，抽屉也该照常打开：%v", err)
	}
	if !strings.Contains(settings.ScheduleNote, "读系统里的定时任务失败") {
		t.Errorf("应当如实说读不出来，实际 %q", settings.ScheduleNote)
	}
	if settings.ArchiveDir == "" {
		t.Error("读不了定时任务，别的项照样要显示")
	}
}

// 归档目录清空（人手改坏了配置文件）：拒绝，且不写下去。
func TestSaveSettingsRejectsEmptyArchiveDir(t *testing.T) {
	useTempHome(t)
	readyConfig(t, t.TempDir())

	// 把配置里的归档目录清空，模拟人手改坏了文件。
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	cfg.ArchiveDir = ""
	if err := config.Save(cfg); err != nil {
		t.Fatal(err)
	}

	ui := settingsApp(t, &fakeSchtasks{missingErr: exitError(t, 1)})
	if _, err := ui.SaveSettings(SettingsInput{BackupMode: config.BackupNone}); err == nil {
		t.Error("归档目录为空时应当报错——收上来的东西得有地方放")
	}
}

// 验收标准 11：「仅本地」排在「Git 仓库」前面，且每种方式带自己那句话。
func TestBackupModesPutLocalFirst(t *testing.T) {
	modes := NewApp(quietLogger()).backupModes()
	if len(modes) < 2 {
		t.Fatalf("备份方式至少应当有两种，实际 %+v", modes)
	}

	if modes[0].Value != config.BackupNone {
		t.Errorf("第一项应当是「仅本地」（%s），实际 %+v", config.BackupNone, modes)
	}
	if modes[len(modes)-1].Value != config.BackupGit {
		t.Errorf("最后一项应当是 Git 仓库（%s），实际 %+v", config.BackupGit, modes)
	}

	// 「仅本地」那句要提到网盘：归档目录放进网盘同步目录，是这条路子最实用的做法。
	if !strings.Contains(modes[0].Hint, "网盘") {
		t.Errorf("「仅本地」那句应当提到可以放进网盘同步目录，实际 %q", modes[0].Hint)
	}
	for _, mode := range modes {
		if mode.Value == config.BackupGit && !mode.NeedsRemote {
			t.Error("Git 仓库这一项要一并问仓库地址")
		}
	}
}

// 窗口还没起来时，文件夹选择框返回可读错误，而不是崩掉。
func TestBrowseDirectoryWithoutAWindowIsAReadableError(t *testing.T) {
	ui := NewApp(quietLogger())
	// 用例里 never startup，ctx 就是空的。

	if _, err := ui.BrowseDirectory("D:/随便"); err == nil {
		t.Fatal("没有窗口时应当返回错误，而不是崩在这里")
	} else if !strings.Contains(err.Error(), "窗口还没起来") {
		t.Errorf("错误要说人话，实际 %v", err)
	}
}

// 抽屉里的每一项都只在被传上来时才动：只改备份方式，别把半截的归档目录也存下去。
func TestSaveSettingsOnlyWritesWhatItIsGiven(t *testing.T) {
	useTempHome(t)
	archive := t.TempDir()
	readyConfig(t, archive)

	fake := &fakeSchtasks{missingErr: exitError(t, 1)}
	ui := settingsApp(t, fake)

	if _, err := ui.SaveSettings(SettingsInput{BackupRemote: "git@example.com:a/b.git", SetRemote: true}); err != nil {
		t.Fatalf("存仓库地址: %v", err)
	}
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ArchiveDir != archive {
		t.Errorf("没动归档目录，它却变成了 %q", cfg.ArchiveDir)
	}
	if cfg.Backup.Git.Remote != "git@example.com:a/b.git" {
		t.Errorf("仓库地址没存上：%q", cfg.Backup.Git.Remote)
	}

	// 不设 SetRemote 时，仓库地址不该被空串清掉。
	if _, err := ui.SaveSettings(SettingsInput{BackupMode: config.BackupGit}); err != nil {
		t.Fatalf("切备份方式: %v", err)
	}
	cfg, err = config.Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Backup.Git.Remote != "git@example.com:a/b.git" {
		t.Errorf("只是切了备份方式，仓库地址不该被动：%q", cfg.Backup.Git.Remote)
	}
}

// 跟定时无关的改动（备份方式、归档目录）不该去动系统里的任务。
//
// 这是人报的「点按钮时窗口卡」的一半：每次保存都 spawn 一次 schtasks，人每点
// 一下备份方式就卡一下，而这一步本来什么都不用做。
func TestSaveSettingsLeavesTheSystemTaskAloneWhenScheduleUntouched(t *testing.T) {
	useTempHome(t)
	readyConfig(t, t.TempDir())

	fake := &fakeSchtasks{missingErr: exitError(t, 1)}
	ui := settingsApp(t, fake)

	if _, err := ui.SaveSettings(SettingsInput{BackupMode: config.BackupNone}); err != nil {
		t.Fatalf("切备份方式: %v", err)
	}
	if fake.calledWith("/Create", "/TN", taskName) || fake.calledWith("/Delete", "/TN", taskName) {
		t.Errorf("没动定时，却去建 / 删了系统里的任务：%v", fake.calls)
	}

	// 而「保存」那条路会带上定时的时刻，所以照样去对齐系统任务——
	// 关着定时就是删一次（系统里本来没有也照删，那是正常结果）。
	if _, err := ui.SaveSettings(SettingsInput{ArchiveDir: t.TempDir(), ScheduleDailyAt: "07:30"}); err != nil {
		t.Fatalf("带上定时的时刻时不该出错：%v", err)
	}
	if !fake.calledWith("/Delete", "/TN", taskName) {
		t.Errorf("带上定时的时刻时，应当去对齐系统里的任务，实际调用 %v", fake.calls)
	}
}

// 人问过「我改变文件夹，对应的内容不也要跟着转移吗」——搬没做（I-024 还没定），
// 但必须**当场说清楚**：旧目录里还留着多少份、这次没搬。
func TestSaveSettingsSaysTheOldArchivesStayBehind(t *testing.T) {
	home := useTempHome(t)
	oldDir := filepath.Join(home, "旧归档")
	newDir := filepath.Join(home, "新归档")
	readyConfig(t, oldDir)

	// 旧目录里有两份归档。
	for _, name := range []string{"甲.md", "乙.md"} {
		if err := os.MkdirAll(oldDir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(oldDir, name), []byte("# 会话"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(newDir, 0o755); err != nil {
		t.Fatal(err)
	}

	ui := settingsApp(t, &fakeSchtasks{missingErr: exitError(t, 1)})

	got, err := ui.SaveSettings(SettingsInput{ArchiveDir: newDir})
	if err != nil {
		t.Fatalf("改归档目录: %v", err)
	}
	if !strings.Contains(got.ArchiveNote, oldDir) || !strings.Contains(got.ArchiveNote, "2 段") {
		t.Errorf("换了目录就要说清旧目录里还剩多少段，实际 %q", got.ArchiveNote)
	}
	// 那句话要把「复制过来」这个出路写出来（人提的：功能得写进文案里）。
	if !strings.Contains(got.ArchiveNote, "复制旧目录里的归档") {
		t.Errorf("要说清怎么把它们弄过来（点那个按钮），实际 %q", got.ArchiveNote)
	}
	if !strings.Contains(got.ArchiveNote, "不会自己跟过来") {
		t.Errorf("要说清这次没搬，实际 %q", got.ArchiveNote)
	}

	// 再存一次（目录没变）：这句提示要收回去，不能一直挂在那儿。
	again, err := ui.SaveSettings(SettingsInput{BackupMode: config.BackupNone})
	if err != nil {
		t.Fatal(err)
	}
	if again.ArchiveNote != "" {
		t.Errorf("没换目录时不该还挂着那句提示，实际 %q", again.ArchiveNote)
	}
}

// 搬归档（I-024）：换过目录才有得搬；收集期间拒绝；只复制不搬；界面拿得到段数。
func TestMigrateArchiveCopiesAndRefusesWhileCollecting(t *testing.T) {
	home := useTempHome(t)
	oldDir := filepath.Join(home, "旧归档")
	newDir := filepath.Join(home, "新归档")
	readyConfig(t, oldDir)

	dir := filepath.Join(oldDir, "Codex CLI", "proj")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"2026-10-16-甲-aaaaaaaaaa.md", "2026-10-16-乙-bbbbbbbbbb.md"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("# 会话"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(newDir, 0o755); err != nil {
		t.Fatal(err)
	}

	ui := settingsApp(t, &fakeSchtasks{missingErr: exitError(t, 1)})

	// 还没换过目录：没得搬。
	if _, err := ui.MigrateArchive(); err == nil {
		t.Error("没换过归档目录时不该允许搬")
	}

	if _, err := ui.SaveSettings(SettingsInput{ArchiveDir: newDir}); err != nil {
		t.Fatalf("换目录: %v", err)
	}
	settings, err := ui.Settings()
	if err != nil {
		t.Fatal(err)
	}
	if settings.ArchiveOld != oldDir || settings.ArchiveOldCount != 2 {
		t.Fatalf("界面要知道旧目录与里面有几段，实际 %q / %d", settings.ArchiveOld, settings.ArchiveOldCount)
	}

	// 收集期间拒绝（两边都写归档目录，不能并着来）。
	ui.mu.Lock()
	ui.collecting = true
	ui.mu.Unlock()
	if _, err := ui.MigrateArchive(); err == nil {
		t.Error("收集期间不该允许搬")
	}
	ui.mu.Lock()
	ui.collecting = false
	ui.mu.Unlock()

	got, err := ui.MigrateArchive()
	if err != nil {
		t.Fatalf("搬: %v", err)
	}
	if got.Copied != 2 || got.Conflicts != 0 || got.Skipped != 0 {
		t.Errorf("应当复制 2 段，实际 %+v", got)
	}
	if !strings.Contains(got.Message, "2") || !strings.Contains(got.Message, "一份没动") {
		t.Errorf("要说清搬了几段、旧目录没动，实际 %q", got.Message)
	}

	// 旧目录一份不少（复制不是搬）。
	old, err := archive.Count(oldDir, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if old.Total != 2 {
		t.Errorf("旧目录应当还是 2 段，实际 %d", old.Total)
	}
	now, err := archive.Count(newDir, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if now.Total != 2 {
		t.Errorf("新目录应当也有 2 段，实际 %d", now.Total)
	}
}

// 换目录时人选了「复制过去」：保存就把旧目录里的归档复制过来（人提的选项，I-024）。
func TestSaveSettingsCopiesOldArchivesWhenChosen(t *testing.T) {
	home := useTempHome(t)
	oldDir := filepath.Join(home, "旧归档")
	newDir := filepath.Join(home, "新归档")
	readyConfig(t, oldDir)

	dir := filepath.Join(oldDir, "Codex CLI", "proj")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"2026-10-16-甲-aaaaaaaaaa.md", "2026-10-15-乙-bbbbbbbbbb.md"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("# 会话"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(newDir, 0o755); err != nil {
		t.Fatal(err)
	}

	ui := settingsApp(t, &fakeSchtasks{missingErr: exitError(t, 1)})
	got, err := ui.SaveSettings(SettingsInput{ArchiveDir: newDir, CopyOldArchives: true})
	if err != nil {
		t.Fatalf("换目录并复制: %v", err)
	}

	// 新目录里当场就有那两段。
	now, err := archive.Count(newDir, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if now.Total != 2 {
		t.Errorf("选了「复制过去」，新目录里应当有 2 段，实际 %d", now.Total)
	}
	// 旧目录里的不动（复制不是搬）。
	old, err := archive.Count(oldDir, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if old.Total != 2 {
		t.Errorf("旧目录里应当还是 2 段，实际 %d", old.Total)
	}
	// 那句话要说清搬了几段、旧目录没动。
	if !strings.Contains(got.ArchiveNote, "复制过来了") || !strings.Contains(got.ArchiveNote, "一份没动") {
		t.Errorf("要说清复制了过来、旧目录没动，实际 %q", got.ArchiveNote)
	}
}

// 没选「复制过去」时就只提示，不动旧目录里的东西。
func TestSaveSettingsDoesNotCopyWhenNotChosen(t *testing.T) {
	home := useTempHome(t)
	oldDir := filepath.Join(home, "旧归档")
	newDir := filepath.Join(home, "新归档")
	readyConfig(t, oldDir)

	dir := filepath.Join(oldDir, "Codex CLI", "proj")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "2026-10-16-甲-aaaaaaaaaa.md"), []byte("# 会话"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(newDir, 0o755); err != nil {
		t.Fatal(err)
	}

	ui := settingsApp(t, &fakeSchtasks{missingErr: exitError(t, 1)})
	got, err := ui.SaveSettings(SettingsInput{ArchiveDir: newDir})
	if err != nil {
		t.Fatal(err)
	}
	now, err := archive.Count(newDir, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if now.Total != 0 {
		t.Errorf("没选复制就不该动文件，实际新目录里有 %d 段", now.Total)
	}
	if !strings.Contains(got.ArchiveNote, "不会自己跟过来") {
		t.Errorf("要说清它们不会自己过来、以及怎么弄过来，实际 %q", got.ArchiveNote)
	}
}

// I-091：设置页「对外接口」开关落进配置，读回来能看到；命令示例带上 -mcp。
func TestMCPSwitchRoundTrips(t *testing.T) {
	useTempHome(t)
	ui := settingsApp(t, &fakeSchtasks{})

	before, err := ui.Settings()
	if err != nil {
		t.Fatalf("读设置: %v", err)
	}
	if before.MCPEnabled {
		t.Fatal("默认应当是关的")
	}

	on := true
	after, err := ui.SaveSettings(SettingsInput{MCPEnabled: &on})
	if err != nil {
		t.Fatalf("保存: %v", err)
	}
	if !after.MCPEnabled {
		t.Fatal("打开后读回来还是关的")
	}
	if strings.HasSuffix(after.MCPCommand, "-mcp") {
		t.Fatalf("命令里不该带 -mcp（要放参数里）: %q", after.MCPCommand)
	}
	if after.MCPArgs != "-mcp" {
		t.Fatalf("参数示例不对: %q", after.MCPArgs)
	}

	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.MCPEnabled {
		t.Fatal("配置里没记下来")
	}

	off := false
	after, err = ui.SaveSettings(SettingsInput{MCPEnabled: &off})
	if err != nil || after.MCPEnabled {
		t.Fatalf("关回去失败: %+v %v", after, err)
	}
}
