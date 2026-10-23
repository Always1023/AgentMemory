package app

import (
	"bytes"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Always1023/AgentMemory/internal/backup"
	"github.com/Always1023/AgentMemory/internal/channel"
	"github.com/Always1023/AgentMemory/internal/config"
	"github.com/Always1023/AgentMemory/internal/logging"
)

// scriptedAdapter 按脚本回答：哪些文件解析必定失败，Parse 被调了几次。
type scriptedAdapter struct {
	id        string
	status    channel.Status
	sources   []channel.SourceFile
	bad       map[string]bool
	parses    *int
	detectErr error
}

func (a scriptedAdapter) Detect() channel.DetectResult {
	return channel.DetectResult{Status: a.status, Root: "/根", Files: a.sources, Err: a.detectErr}
}

func (a scriptedAdapter) Parse(sf channel.SourceFile) (channel.Session, error) {
	if a.parses != nil {
		*a.parses++
	}
	if a.bad[sf.Path] {
		return channel.Session{}, errors.New("这个文件写坏了")
	}
	return channel.Session{
		Channel:     a.id,
		ID:          filepath.Base(sf.Path),
		Title:       "标题 " + filepath.Base(sf.Path),
		ProjectPath: `D:\work\demo`,
		SourceFile:  sf.Path,
		Messages:    []channel.Message{{Role: channel.RoleUser, Text: "正文"}},
	}, nil
}

// fakeBackup 是测试用的备份方式：固定返回一个结果。
type fakeBackup struct{ result backup.Result }

func (f fakeBackup) Back(backup.Settings) backup.Result { return f.result }

// sourceFiles 在 dir 下造出真文件，返回它们的源文件描述。
func sourceFiles(t *testing.T, dir string, names ...string) []channel.SourceFile {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	var out []channel.SourceFile
	for _, name := range names {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte("（假装这是 "+name+"）\n"), 0o600); err != nil {
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

// testDeps 拼一份可以完全掌控的依赖。
func testDeps(t *testing.T, baseDir string, backupResult backup.Result, entries ...channel.Entry) Deps {
	t.Helper()
	channels := channel.NewRegistry()
	for _, e := range entries {
		if err := channels.Register(e); err != nil {
			t.Fatal(err)
		}
	}

	backups := backup.NewRegistry()
	for _, id := range []string{config.BackupNone, config.BackupGit, config.BackupSync} {
		if err := backups.Register(backup.Entry{ID: id, Name: id, Method: fakeBackup{result: backupResult}}); err != nil {
			t.Fatal(err)
		}
	}
	return Deps{Channels: channels, Backups: backups, BaseDir: baseDir}
}

func testConfig(archiveDir string) config.Config {
	cfg := config.Default()
	cfg.ArchiveDir = archiveDir
	cfg.Backup.Mode = config.BackupNone
	return cfg
}

func testLogger() (*slog.Logger, *bytes.Buffer) {
	var buf bytes.Buffer
	return slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})), &buf
}

func byID(t *testing.T, result Result) map[string]ChannelResult {
	t.Helper()
	out := map[string]ChannelResult{}
	for _, c := range result.Channels {
		out[c.ID] = c
	}
	return out
}

// archivedFiles 列出归档目录里的 .md。
func archivedFiles(t *testing.T, root string) []string {
	t.Helper()
	var out []string
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		out = append(out, path)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// 验收标准 1：一个渠道的样本文件损坏时，其余渠道照常产出，结果里逐条标出失败项。
func TestBrokenFileDoesNotStopOtherFilesOrChannels(t *testing.T) {
	root := t.TempDir()
	filesA := sourceFiles(t, filepath.Join(root, "源", "甲"), "好.jsonl", "坏.jsonl")
	filesB := sourceFiles(t, filepath.Join(root, "源", "乙"), "也好.jsonl")

	calls := 0
	deps := testDeps(t, filepath.Join(root, "配置"), backup.Result{},
		channel.Entry{ID: "jia", Name: "甲渠道", Adapter: scriptedAdapter{id: "jia", status: channel.StatusAvailable, sources: filesA, bad: map[string]bool{filesA[1].Path: true}, parses: &calls}},
		channel.Entry{ID: "yi", Name: "乙渠道", Adapter: scriptedAdapter{id: "yi", status: channel.StatusAvailable, sources: filesB, parses: &calls}},
	)
	cfg := testConfig(filepath.Join(root, "归档"))
	logger, logs := testLogger()

	result, err := Collect(cfg, logger, deps)
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}

	got := byID(t, result)
	if a := got["jia"]; a.Found != 2 || a.New != 2 || a.Archived != 1 || a.Failed != 1 {
		t.Errorf("甲渠道 = %+v，想要 发现 2 / 新增 2 / 归档 1 / 失败 1", a)
	} else if len(a.Failures) != 1 || !strings.HasSuffix(a.Failures[0].Path, "坏.jsonl") {
		t.Errorf("失败项没点名是哪个文件: %+v", a.Failures)
	}
	if b := got["yi"]; b.Found != 1 || b.Archived != 1 || b.Failed != 0 {
		t.Errorf("乙渠道被甲的坏文件带坏了: %+v", b)
	}

	if result.Archived() != 2 {
		t.Errorf("共归档 %d 份，想要 2 份", result.Archived())
	}
	if result.Outcome() != OutcomePartial {
		t.Errorf("结论 = %q，想要 %q", result.Outcome(), OutcomePartial)
	}
	if files := archivedFiles(t, cfg.ArchiveDir); len(files) != 2 {
		t.Errorf("归档目录里应当有 2 份文件，实际 %v", files)
	}
	if !strings.Contains(logs.String(), "坏.jsonl") {
		t.Error("日志里应当点名那个坏文件")
	}

	// 第二轮：上一轮失败的文件会自动重试（I-048）；仍然失败，照实记一笔。
	second, err := Collect(cfg, logger, deps)
	if err != nil {
		t.Fatalf("第二轮: %v", err)
	}
	if second.New() != 1 || second.Archived() != 0 || second.Failures() != 1 {
		t.Errorf("第二轮 新增 %d / 归档 %d / 失败 %d，想要 1 / 0 / 1",
			second.New(), second.Archived(), second.Failures())
	}
	if second.Outcome() != OutcomeFailed {
		t.Errorf("第二轮结论 = %q，想要 %q", second.Outcome(), OutcomeFailed)
	}

	// 第三轮再试一次（第 3 次失败）；第四轮起停手，不再当成新增。
	third, err := Collect(cfg, logger, deps)
	if err != nil {
		t.Fatalf("第三轮: %v", err)
	}
	if third.New() != 1 || third.Failures() != 1 {
		t.Errorf("第三轮 新增 %d / 失败 %d，想要 1 / 1", third.New(), third.Failures())
	}
	fourth, err := Collect(cfg, logger, deps)
	if err != nil {
		t.Fatalf("第四轮: %v", err)
	}
	if fourth.New() != 0 || fourth.Archived() != 0 || fourth.Failures() != 0 {
		t.Errorf("连续失败 3 次后 新增 %d / 归档 %d / 失败 %d，想要全 0",
			fourth.New(), fourth.Archived(), fourth.Failures())
	}
	if fourth.Outcome() != OutcomeSuccess {
		t.Errorf("第四轮结论 = %q，想要 %q", fourth.Outcome(), OutcomeSuccess)
	}
}

// 验收标准 2：系统错（如配置损坏）时整轮停止并返回可读错误。
func TestSystemErrorStopsTheRun(t *testing.T) {
	t.Run("配置不可用", func(t *testing.T) {
		cfg := testConfig("") // 归档目录空 → Validate 不过
		logger, _ := testLogger()
		if _, err := Collect(cfg, logger, testDeps(t, t.TempDir(), backup.Result{})); err == nil {
			t.Fatal("配置不可用应当报错")
		} else if !strings.Contains(err.Error(), "配置不可用") {
			t.Errorf("错误应当是人能读懂的: %v", err)
		}
	})

	t.Run("归档目录写不进去", func(t *testing.T) {
		root := t.TempDir()
		notADir := filepath.Join(root, "其实是个文件")
		if err := os.WriteFile(notADir, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		files := sourceFiles(t, filepath.Join(root, "源"), "a.jsonl")
		deps := testDeps(t, filepath.Join(root, "配置"), backup.Result{},
			channel.Entry{ID: "c", Name: "渠道", Adapter: scriptedAdapter{id: "c", status: channel.StatusAvailable, sources: files}})
		logger, _ := testLogger()

		_, err := Collect(testConfig(notADir), logger, deps)
		if err == nil {
			t.Fatal("归档写不进去应当整轮中断")
		}
		if !strings.Contains(err.Error(), "归档") {
			t.Errorf("错误应当说明卡在哪一步: %v", err)
		}
	})

	t.Run("游标目录不可用", func(t *testing.T) {
		root := t.TempDir()
		notADir := filepath.Join(root, "其实是个文件")
		if err := os.WriteFile(notADir, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		logger, _ := testLogger()
		_, err := Collect(testConfig(filepath.Join(root, "归档")), logger,
			testDeps(t, notADir, backup.Result{}))
		if err == nil || !strings.Contains(err.Error(), "增量游标") {
			t.Fatalf("游标开不了应当报错: %v", err)
		}
	})
}

// 验收标准 3：一次收集的运行记录含每个渠道的处理数与失败数。
func TestRunRecordHasPerChannelCounts(t *testing.T) {
	root := t.TempDir()
	filesA := sourceFiles(t, filepath.Join(root, "源", "甲"), "1.jsonl", "2.jsonl", "坏.jsonl")
	filesB := sourceFiles(t, filepath.Join(root, "源", "乙"), "3.jsonl")

	deps := testDeps(t, filepath.Join(root, "配置"), backup.Result{},
		channel.Entry{ID: "jia", Name: "甲渠道", Adapter: scriptedAdapter{id: "jia", status: channel.StatusAvailable, sources: filesA, bad: map[string]bool{filesA[2].Path: true}}},
		channel.Entry{ID: "bing", Name: "丙渠道", Adapter: scriptedAdapter{id: "bing", status: channel.StatusNotInstalled}},
		channel.Entry{ID: "yi", Name: "乙渠道", Adapter: scriptedAdapter{id: "yi", status: channel.StatusAvailable, sources: filesB}},
	)
	logger, logs := testLogger()

	result, err := Collect(testConfig(filepath.Join(root, "归档")), logger, deps)
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}

	if len(result.Channels) != 3 {
		t.Fatalf("运行记录里应当有三个渠道，实际 %d 个", len(result.Channels))
	}
	// 按渠道 ID 排序（注册表保证），不受注册顺序影响。
	if result.Channels[0].ID != "bing" || result.Channels[1].ID != "jia" || result.Channels[2].ID != "yi" {
		t.Errorf("渠道顺序 = %v", []string{result.Channels[0].ID, result.Channels[1].ID, result.Channels[2].ID})
	}

	got := byID(t, result)
	if a := got["jia"]; a.Found != 3 || a.New != 3 || a.Archived != 2 || a.Failed != 1 {
		t.Errorf("甲渠道 = %+v", a)
	}
	if b := got["yi"]; b.Found != 1 || b.Archived != 1 || b.Failed != 0 {
		t.Errorf("乙渠道 = %+v", b)
	}

	if result.Found() != 4 || result.New() != 4 || result.Archived() != 3 || result.Failures() != 1 {
		t.Errorf("汇总 = 发现 %d / 新增 %d / 归档 %d / 失败 %d，想要 4 / 4 / 3 / 1",
			result.Found(), result.New(), result.Archived(), result.Failures())
	}
	if result.StartedAt.IsZero() || result.Duration <= 0 {
		t.Errorf("运行记录没记时间: %+v", result)
	}

	// 日志里也要有每个渠道的处理数与失败数。
	for _, want := range []string{"渠道=jia", "渠道=yi", "发现=3", "归档=2", "失败=1"} {
		if !strings.Contains(logs.String(), want) {
			t.Errorf("日志里找不到 %q\n--- 日志 ---\n%s", want, logs.String())
		}
	}
}

// 验收标准 4：备份失败不影响已归档的内容，且结果里体现为部分成功。
func TestBackupFailureKeepsArchivesAndIsPartial(t *testing.T) {
	root := t.TempDir()
	files := sourceFiles(t, filepath.Join(root, "源"), "a.jsonl", "b.jsonl")

	boom := errors.New("远程仓库推不上去")
	deps := testDeps(t, filepath.Join(root, "配置"), backup.Result{Err: boom},
		channel.Entry{ID: "c", Name: "渠道", Adapter: scriptedAdapter{id: "c", status: channel.StatusAvailable, sources: files}})
	cfg := testConfig(filepath.Join(root, "归档"))
	logger, logs := testLogger()

	result, err := Collect(cfg, logger, deps)
	if err != nil {
		t.Fatalf("备份失败不该中断整轮: %v", err)
	}
	if !errors.Is(result.Backup.Err, boom) {
		t.Errorf("备份失败应当记进结果: %+v", result.Backup)
	}
	if result.Archived() != 2 {
		t.Errorf("归档份数 = %d，想要 2", result.Archived())
	}
	if files := archivedFiles(t, cfg.ArchiveDir); len(files) != 2 {
		t.Errorf("备份失败不该动归档文件，实际 %v", files)
	}
	if result.Outcome() != OutcomePartial {
		t.Errorf("结论 = %q，想要 %q", result.Outcome(), OutcomePartial)
	}
	if !strings.Contains(logs.String(), "推不上去") {
		t.Error("日志里应当带上失败原因")
	}
}

// 这是 I-011 验收标准 2 的机制侧：紧接着再跑一次，没有新增文件。
func TestSecondRunFindsNothingNew(t *testing.T) {
	root := t.TempDir()
	files := sourceFiles(t, filepath.Join(root, "源"), "a.jsonl", "b.jsonl")

	calls := 0
	deps := testDeps(t, filepath.Join(root, "配置"), backup.Result{},
		channel.Entry{ID: "c", Name: "渠道", Adapter: scriptedAdapter{id: "c", status: channel.StatusAvailable, sources: files, parses: &calls}})
	cfg := testConfig(filepath.Join(root, "归档"))
	logger, _ := testLogger()

	first, err := Collect(cfg, logger, deps)
	if err != nil {
		t.Fatalf("第一轮: %v", err)
	}
	if first.New() != 2 || first.Archived() != 2 {
		t.Fatalf("第一轮应当新增并归档 2 份，实际 新增 %d / 归档 %d", first.New(), first.Archived())
	}
	callsAfterFirst := calls

	second, err := Collect(cfg, logger, deps)
	if err != nil {
		t.Fatalf("第二轮: %v", err)
	}
	if second.New() != 0 || second.Archived() != 0 {
		t.Errorf("第二轮应当没有新增，实际 新增 %d / 归档 %d", second.New(), second.Archived())
	}
	if second.Found() != 2 {
		t.Errorf("第二轮仍然应当「发现」2 个文件，实际 %d", second.Found())
	}
	if calls != callsAfterFirst {
		t.Errorf("第二轮不该再解析文件：解析次数从 %d 变成了 %d", callsAfterFirst, calls)
	}
	if second.Outcome() != OutcomeSuccess {
		t.Errorf("没有新增也没出错，结论应当是 %q，实际 %q", OutcomeSuccess, second.Outcome())
	}
	if files := archivedFiles(t, cfg.ArchiveDir); len(files) != 2 {
		t.Errorf("归档目录里应当还是 2 份，实际 %v", files)
	}
}

// 这是 I-015 验收标准 3 的机制侧：「重试」只跑指定的那个渠道，且**不看游标**。
//
// 为什么必须不看游标：上一轮失败的文件已经记进游标（归档路径留空），
// 不强制重跑的话，点「重试」永远什么都不做——那是最像坏掉的「没反应」。
func TestOnlyAndForceRetryOneChannel(t *testing.T) {
	root := t.TempDir()
	filesA := sourceFiles(t, filepath.Join(root, "源A"), "a1.jsonl", "a2.jsonl")
	filesB := sourceFiles(t, filepath.Join(root, "源B"), "b1.jsonl")

	broken := map[string]bool{filesA[0].Path: true}
	callsA, callsB := 0, 0
	deps := testDeps(t, filepath.Join(root, "配置"), backup.Result{},
		channel.Entry{ID: "a", Name: "渠道甲", Adapter: scriptedAdapter{id: "a", status: channel.StatusAvailable, sources: filesA, bad: broken, parses: &callsA}},
		channel.Entry{ID: "b", Name: "渠道乙", Adapter: scriptedAdapter{id: "b", status: channel.StatusAvailable, sources: filesB, parses: &callsB}},
	)
	cfg := testConfig(filepath.Join(root, "归档"))
	logger, _ := testLogger()

	first, err := Collect(cfg, logger, deps)
	if err != nil {
		t.Fatalf("第一轮: %v", err)
	}
	if got := byID(t, first)["a"]; got.New != 2 || got.Archived != 1 || got.Failed != 1 {
		t.Fatalf("第一轮渠道甲应当 新增 2 / 归档 1 / 失败 1，实际 %+v", got)
	}
	callsAAfterFirst := callsA

	// 重试渠道甲：只跑它，而且那个坏文件要再被解析一次（不然「重试」没做事）。
	retry := deps
	retry.Only = []string{"a"}
	retry.Force = true

	second, err := Collect(cfg, logger, retry)
	if err != nil {
		t.Fatalf("重试: %v", err)
	}
	if len(second.Channels) != 1 || second.Channels[0].ID != "a" {
		t.Fatalf("重试那一轮里应当只有渠道甲，实际 %+v", second.Channels)
	}
	if callsA == callsAAfterFirst {
		t.Error("重试应当把渠道甲的文件重跑一遍，实际一次都没解析")
	}
	if callsB != 1 {
		t.Errorf("重试不该碰渠道乙，它的解析次数从 1 变成了 %d", callsB)
	}
	if got := second.Channels[0]; got.Failed != 1 {
		t.Errorf("文件还是坏的，重试应当仍然报失败，实际 %+v", got)
	}
}

// 重试一个不在注册表里的渠道名，等于什么都没跑——这种事要能被看出来。
func TestOnlyWithUnknownChannelCollectsNothing(t *testing.T) {
	root := t.TempDir()
	files := sourceFiles(t, filepath.Join(root, "源"), "a.jsonl")

	deps := testDeps(t, filepath.Join(root, "配置"), backup.Result{},
		channel.Entry{ID: "c", Name: "渠道", Adapter: scriptedAdapter{id: "c", status: channel.StatusAvailable, sources: files}})
	cfg := testConfig(filepath.Join(root, "归档"))
	logger, _ := testLogger()

	deps.Only = []string{"并不存在"}
	result, err := Collect(cfg, logger, deps)
	if err != nil {
		t.Fatalf("收一轮: %v", err)
	}
	if len(result.Channels) != 0 || result.New() != 0 {
		t.Errorf("指定了一个不存在的渠道，应当什么都没收，实际 %+v", result.Channels)
	}
}

func TestDisabledChannelIsSkipped(t *testing.T) {
	root := t.TempDir()
	files := sourceFiles(t, filepath.Join(root, "源"), "a.jsonl")

	calls := 0
	deps := testDeps(t, filepath.Join(root, "配置"), backup.Result{},
		channel.Entry{ID: "c", Name: "渠道", Adapter: scriptedAdapter{id: "c", status: channel.StatusAvailable, sources: files, parses: &calls}})
	cfg := testConfig(filepath.Join(root, "归档"))
	cfg.Channels = map[string]bool{"c": false}
	logger, _ := testLogger()

	result, err := Collect(cfg, logger, deps)
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if result.Archived() != 0 || calls != 0 {
		t.Errorf("停用的渠道不该被碰: 归档 %d，解析 %d 次", result.Archived(), calls)
	}
	if c := byID(t, result)["c"]; c.Enabled {
		t.Errorf("运行记录里应当标出这个渠道被停用了: %+v", c)
	}
	if result.Outcome() != OutcomeSuccess {
		t.Errorf("主动停用渠道不是失败，结论应当是 %q", OutcomeSuccess)
	}
}

func TestNotInstalledIsNotAFailure(t *testing.T) {
	root := t.TempDir()
	deps := testDeps(t, filepath.Join(root, "配置"), backup.Result{},
		channel.Entry{ID: "c", Name: "渠道", Adapter: scriptedAdapter{id: "c", status: channel.StatusNotInstalled}})
	logger, _ := testLogger()

	result, err := Collect(testConfig(filepath.Join(root, "归档")), logger, deps)
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if result.Failures() != 0 {
		t.Errorf("未安装不算失败，实际失败数 %d", result.Failures())
	}
	if result.Outcome() != OutcomeSuccess {
		t.Errorf("结论 = %q，想要 %q", result.Outcome(), OutcomeSuccess)
	}
}

func TestDetectFailureIsChannelLevelAndPartial(t *testing.T) {
	root := t.TempDir()
	files := sourceFiles(t, filepath.Join(root, "源"), "a.jsonl")

	deps := testDeps(t, filepath.Join(root, "配置"), backup.Result{},
		channel.Entry{ID: "bad", Name: "坏渠道", Adapter: scriptedAdapter{id: "bad", status: channel.StatusFailed, detectErr: errors.New("读不了")}},
		channel.Entry{ID: "ok", Name: "好渠道", Adapter: scriptedAdapter{id: "ok", status: channel.StatusAvailable, sources: files}},
	)
	logger, _ := testLogger()

	result, err := Collect(testConfig(filepath.Join(root, "归档")), logger, deps)
	if err != nil {
		t.Fatalf("一个渠道检测失败不该中断整轮: %v", err)
	}
	got := byID(t, result)
	if got["bad"].Err == nil {
		t.Error("检测失败应当记在渠道级 Err 上")
	}
	if got["ok"].Archived != 1 || result.Archived() != 1 {
		t.Errorf("好渠道应当照常产出: %+v", result)
	}
	if result.Failures() != 1 {
		t.Errorf("失败数 = %d，想要 1", result.Failures())
	}
	if result.Outcome() != OutcomePartial {
		t.Errorf("结论 = %q，想要 %q", result.Outcome(), OutcomePartial)
	}
}

// 配置认得出 sync、但阶段 1 还没实现：归档要保住，只算这一步没成。
func TestUnimplementedBackupModeIsPartialNotFatal(t *testing.T) {
	root := t.TempDir()
	files := sourceFiles(t, filepath.Join(root, "源"), "a.jsonl")

	deps := testDeps(t, filepath.Join(root, "配置"), backup.Result{},
		channel.Entry{ID: "c", Name: "渠道", Adapter: scriptedAdapter{id: "c", status: channel.StatusAvailable, sources: files}})
	// 把内置方式全撤掉，只剩一个空注册表 —— 模拟「sync 还没实现」。
	deps.Backups = backup.NewRegistry()
	cfg := testConfig(filepath.Join(root, "归档"))
	cfg.Backup.Mode = config.BackupSync
	logger, _ := testLogger()

	result, err := Collect(cfg, logger, deps)
	if err != nil {
		t.Fatalf("备份方式没实现不该中断整轮: %v", err)
	}
	if result.Backup.Err == nil {
		t.Error("应当把「备份没做成」记进结果")
	}
	if result.Archived() != 1 {
		t.Errorf("归档份数 = %d，想要 1", result.Archived())
	}
	if result.Outcome() != OutcomePartial {
		t.Errorf("结论 = %q，想要 %q", result.Outcome(), OutcomePartial)
	}
}

func TestNothingProducedAndSomethingFailedIsFailed(t *testing.T) {
	root := t.TempDir()
	deps := testDeps(t, filepath.Join(root, "配置"), backup.Result{},
		channel.Entry{ID: "bad", Name: "坏渠道", Adapter: scriptedAdapter{id: "bad", status: channel.StatusFailed, detectErr: errors.New("读不了")}})
	logger, _ := testLogger()

	result, err := Collect(testConfig(filepath.Join(root, "归档")), logger, deps)
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if result.Outcome() != OutcomeFailed {
		t.Errorf("什么都没产出又有失败，结论应当是 %q，实际 %q", OutcomeFailed, result.Outcome())
	}
}

// 验收标准 3（I-013）：界面列出的渠道与收集时检测到的渠道是同一份结果。
//
// 「一致」不靠两处各自实现得一样，靠的是同一段代码——这条用例把两者摆在一起比。
func TestDetectMatchesWhatCollectSees(t *testing.T) {
	root := t.TempDir()
	files := sourceFiles(t, filepath.Join(root, "源"), "a.jsonl")

	deps := testDeps(t, filepath.Join(root, "配置"), backup.Result{},
		channel.Entry{ID: "ok", Name: "好渠道", Adapter: scriptedAdapter{id: "ok", status: channel.StatusAvailable, sources: files}},
		channel.Entry{ID: "gone", Name: "没装的", Adapter: scriptedAdapter{id: "gone", status: channel.StatusNotInstalled}},
		channel.Entry{ID: "bad", Name: "坏渠道", Adapter: scriptedAdapter{id: "bad", status: channel.StatusFailed, detectErr: errors.New("读不了")}},
	)

	reports, err := Detect(deps)
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}

	logger, _ := testLogger()
	result, err := Collect(testConfig(filepath.Join(root, "归档")), logger, deps)
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}

	type row struct {
		id      string
		status  channel.Status
		found   int
		enabled bool
	}
	fromDetect := map[string]row{}
	for _, r := range reports {
		fromDetect[r.ID] = row{id: r.ID, status: r.Status, found: len(r.Files), enabled: true}
	}
	fromCollect := map[string]row{}
	for _, c := range result.Channels {
		fromCollect[c.ID] = row{id: c.ID, status: c.Status, found: c.Found, enabled: c.Enabled}
	}

	if len(fromDetect) != len(fromCollect) {
		t.Fatalf("检测到 %d 个渠道，收集看到 %d 个", len(fromDetect), len(fromCollect))
	}
	for id, want := range fromDetect {
		if got := fromCollect[id]; got != want {
			t.Errorf("渠道 %s：检测说 %+v，收集说 %+v", id, want, got)
		}
	}
	t.Logf("三个渠道（可用 / 未安装 / 失败）在检测与收集两侧逐项一致")
}

func TestDetectRejectsNoDeps(t *testing.T) {
	// 不留 Deps 时走内置渠道注册表；注册表本身是可用的，所以不报错。
	if _, err := Detect(Deps{Channels: channel.NewRegistry()}); err != nil {
		t.Errorf("空注册表也应当有个结论: %v", err)
	}
}

func TestEnabledDefaultsToOn(t *testing.T) {
	if !Enabled(nil, "任何") {
		t.Error("配置里没写这个渠道时应当算启用")
	}
	if !Enabled(map[string]bool{}, "任何") {
		t.Error("空表时应当算启用")
	}
	if Enabled(map[string]bool{"c": false}, "c") {
		t.Error("明确写了 false 才算停用")
	}
	if !Enabled(map[string]bool{"c": true}, "c") {
		t.Error("明确写了 true 就是启用")
	}
}

// I-011 验收标准 5：运行记录写进日志文件，含每个渠道的处理数与失败数。
//
// 这条写在 app 里验，因为「运行记录」是这一层产出的；入口那层只管把它转述给人。
func TestRunRecordLandsInTheLogFile(t *testing.T) {
	home := t.TempDir()
	t.Setenv("AGENTMEMORY_HOME", home)

	root := t.TempDir()
	files := sourceFiles(t, filepath.Join(root, "源"), "好.jsonl", "坏.jsonl")
	deps := testDeps(t, filepath.Join(home, "游标"), backup.Result{},
		channel.Entry{ID: "jia", Name: "甲渠道", Adapter: scriptedAdapter{
			id: "jia", status: channel.StatusAvailable, sources: files,
			bad: map[string]bool{files[1].Path: true},
		}})

	logger, closer, err := logging.Setup(io.Discard, slog.LevelInfo)
	if err != nil {
		t.Fatalf("建日志: %v", err)
	}
	if _, err := Collect(testConfig(filepath.Join(root, "归档")), logger, deps); err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if err := closer.Close(); err != nil {
		t.Fatalf("关日志: %v", err)
	}

	raw, err := os.ReadFile(filepath.Join(home, "logs", logging.FileName))
	if err != nil {
		t.Fatalf("日志文件没写出来: %v", err)
	}
	log := string(raw)
	for _, want := range []string{"渠道=jia", "发现=2", "归档=1", "失败=1", "坏.jsonl", "本轮收集结束"} {
		if !strings.Contains(log, want) {
			t.Errorf("日志文件里找不到 %q\n--- 日志 ---\n%s", want, log)
		}
	}
}

// 一个渠道里按文件报进度：界面那一行靠它走 0% 到 100%（人提的）。
func TestCollectReportsPerFileProgress(t *testing.T) {
	root := t.TempDir()
	deps := testDeps(t, filepath.Join(root, "配置"), backup.Result{},
		channel.Entry{ID: "fake", Name: "假渠道", Adapter: scriptedAdapter{
			id:      "fake",
			status:  channel.StatusAvailable,
			sources: sourceFiles(t, filepath.Join(root, "源"), "一.jsonl", "二.jsonl", "三.jsonl"),
		}})
	var got []Progress
	deps.Progress = func(p Progress) { got = append(got, p) }

	if _, err := Collect(testConfig(filepath.Join(root, "归档")), nil, deps); err != nil {
		t.Fatalf("Collect: %v", err)
	}

	var perFile []Progress
	for _, p := range got {
		if p.Stage == StageChannel && p.ChannelID != "" {
			perFile = append(perFile, p)
		}
	}
	if len(perFile) < 4 {
		t.Fatalf("一个渠道 3 个文件应当至少报 4 次进度（含收尾），实际 %d 次：%+v", len(perFile), perFile)
	}
	last := perFile[len(perFile)-1]
	if !last.ChannelDone || last.FileDone != 3 || last.FileTotal != 3 {
		t.Errorf("最后一次应当是「这个渠道 3/3 走完」，实际 %+v", last)
	}
	if first := perFile[0]; first.FileDone != 0 || first.FileTotal != 3 {
		t.Errorf("第一次应当是 0/3（0%% 起步），实际 %+v", first)
	}
}
