package main

import (
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/Always1023/AgentMemory/internal/config"
)

// ── Git 分步引导（I-022）──────────────────────────────────────────────────
//
// 全用假的 git 出口：真去 init 会在跑测试的人的磁盘上建仓库，真去 ls-remote
// 会连外网。真机那条另立（见 TestGitGuideAgainstTheRealSystem）。

// fakeGit 是一台「假 git」：按命令回答，并记下被要求跑了什么。
type fakeGit struct {
	calls [][]string
	// available 为假时，`--version` 报「找不到命令」。
	available bool
	version   string
	// repoReady 为真时，`rev-parse` 说「在仓库里」。
	repoReady bool
	// remote / remoteErr：`remote get-url origin` 的回答。
	remote    string
	remoteErr error
	// initOut / initErr：`git init` 的回答。
	initOut string
	initErr error
	// lsRemoteOut / lsRemoteErr：`ls-remote` 的回答。
	lsRemoteOut string
	lsRemoteErr error
}

func newFakeGit() *fakeGit {
	return &fakeGit{available: true, version: "git version 2.44.0.windows.1"}
}

func (f *fakeGit) run(dir string, args ...string) (string, error) {
	f.calls = append(f.calls, append([]string{dir}, args...))

	switch args[0] {
	case "--version":
		if !f.available {
			return "", exec.ErrNotFound
		}
		return f.version + "\n", nil
	case "rev-parse":
		if f.repoReady {
			return "true\n", nil
		}
		return "false\n", nil
	case "remote":
		return f.remote, f.remoteErr
	case "init":
		if f.initErr != nil {
			return f.initOut, f.initErr
		}
		f.repoReady = true
		return f.initOut, nil
	case "ls-remote":
		return f.lsRemoteOut, f.lsRemoteErr
	}
	return "", nil
}

// calledWith 报告有没有一次调用同时含这些片段。
func (f *fakeGit) calledWith(parts ...string) bool {
	for _, call := range f.calls {
		joined := strings.Join(call, " ")
		all := true
		for _, part := range parts {
			if !strings.Contains(joined, part) {
				all = false
				break
			}
		}
		if all {
			return true
		}
	}
	return false
}

func gitApp(t *testing.T, fake *fakeGit) *App {
	t.Helper()
	ui := NewApp(quietLogger())
	ui.gitRunner = fake.run
	return ui
}

// useArchiveDir 把配置里的归档目录指到临时目录，并返回它。
func useArchiveDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	cfg := config.Default()
	cfg.FirstRunDone = true
	cfg.ArchiveDir = dir
	if err := config.Save(cfg); err != nil {
		t.Fatal(err)
	}
	return dir
}

// 验收标准 1：git 没装时，第 1 步就说明白，不往下走。
func TestGitGuideStopsWhenGitIsMissing(t *testing.T) {
	useTempHome(t)
	useArchiveDir(t)

	fake := newFakeGit()
	fake.available = false
	ui := gitApp(t, fake)

	guide, err := ui.GitGuide()
	if err != nil {
		t.Fatalf("GitGuide: %v", err)
	}
	if guide.GitAvailable {
		t.Error("没有 git，不该说装了")
	}
	if guide.Step != gitStepInstall {
		t.Errorf("应当停在「先装 git」这一步，实际 %q", guide.Step)
	}
	if !strings.Contains(guide.Note, "git-scm.com") {
		t.Errorf("要告诉人去哪装，实际 %q", guide.Note)
	}

	// 不往下走：不该去查仓库、更不该去 init。
	if fake.calledWith("init") {
		t.Errorf("git 都没有就别去 init，实际调用 %v", fake.calls)
	}
	if fake.calledWith("rev-parse") {
		t.Errorf("git 都没有，别查仓库状态，实际调用 %v", fake.calls)
	}

	// 这时候让它 init 要拒绝，不能假装成功。
	if _, err := ui.GitInit(); err == nil {
		t.Error("git 都没装，init 应当报错而不是假装建好了")
	}
}

// 验收标准 2：归档目录还不是仓库时能一键 init，且只作用在归档目录上。
func TestGitInitOnlyTouchesTheArchiveDir(t *testing.T) {
	useTempHome(t)
	archive := useArchiveDir(t)

	fake := newFakeGit()
	fake.repoReady = false
	ui := gitApp(t, fake)

	before, err := ui.GitGuide()
	if err != nil {
		t.Fatalf("GitGuide: %v", err)
	}
	if before.Step != gitStepInit || before.RepoReady {
		t.Errorf("还不是仓库时应当是「就地建仓库」这一步，实际 %+v", before)
	}

	after, err := ui.GitInit()
	if err != nil {
		t.Fatalf("GitInit: %v", err)
	}
	if !after.RepoReady {
		t.Error("建完之后应当说「已经是仓库」")
	}
	if !fake.calledWith(archive, "init") {
		t.Errorf("git init 应当在归档目录 %s 里跑，实际调用 %v", archive, fake.calls)
	}
}

// 验收标准 3：本地仓库已经配好远程时，把那个地址读出来填上。
func TestGitGuideDetectsTheConfiguredRemote(t *testing.T) {
	useTempHome(t)
	useArchiveDir(t)

	fake := newFakeGit()
	fake.repoReady = true
	fake.remote = "git@github.com:Always1023/Memory.git"
	ui := gitApp(t, fake)

	guide, err := ui.GitGuide()
	if err != nil {
		t.Fatalf("GitGuide: %v", err)
	}
	if guide.DetectedRemote != "git@github.com:Always1023/Memory.git" {
		t.Errorf("应当把本地仓库配好的远程读出来，实际 %q", guide.DetectedRemote)
	}
	// 配置里没填地址、而仓库里有：停在「试连」这一步，等界面把地址填上。
	if guide.Step != gitStepTest {
		t.Errorf("读出远程之后应当停在「试连」，实际 %q（%s）", guide.Step, guide.Note)
	}
}

// 验收标准 4：试连连得上/连不上都要说清；失败按三类给话术。
func TestGitTestRemoteExplainsWhatWentWrong(t *testing.T) {
	useTempHome(t)
	useArchiveDir(t)

	cases := []struct {
		name      string
		out       string
		err       error
		wantIn    string
		wantOK    bool
		wantStep  string
		wantExtra string
	}{
		{name: "连上了", wantOK: true, wantStep: gitStepDone, wantIn: "连上了"},
		{
			name: "凭据问题", err: exec.ErrNotFound,
			out:    "fatal: Authentication failed for 'https://github.com/a/b.git'",
			wantIn: "凭据",
		},
		{
			name: "地址写法", err: exec.ErrNotFound,
			out:    "remote: Repository not found.\nfatal: repository 'https://github.com/a/nope.git/' not found",
			wantIn: "地址写法",
		},
		{
			name: "网络问题", err: exec.ErrNotFound,
			out:    "fatal: unable to access 'https://github.com/a/b.git/': Could not resolve host: github.com",
			wantIn: "网络",
		},
		{
			name: "认不出来就照原文", err: exec.ErrNotFound,
			out:    "fatal: something we have never seen",
			wantIn: "something we have never seen",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fake := newFakeGit()
			fake.repoReady = true
			fake.lsRemoteOut, fake.lsRemoteErr = tc.out, tc.err
			ui := gitApp(t, fake)

			guide, err := ui.GitTestRemote("git@example.com:a/b.git")
			if err != nil {
				t.Fatalf("试连不该把失败当出错返回：%v", err)
			}
			if !guide.Tested {
				t.Error("试过之后要标上「试过了」")
			}
			if guide.OK != tc.wantOK {
				t.Errorf("结果应当是 %v，实际 %v（%s）", tc.wantOK, guide.OK, guide.Detail)
			}
			if !strings.Contains(guide.Detail, tc.wantIn) {
				t.Errorf("说明里应当包含 %q，实际 %q", tc.wantIn, guide.Detail)
			}
			if tc.wantStep != "" && guide.Step != tc.wantStep {
				t.Errorf("连上之后应当到「都准备好了」这一步，实际 %q", guide.Step)
			}
		})
	}
}

// 试连要用只读命令，且带上填的地址。
func TestGitTestRemoteUsesReadOnlyCommand(t *testing.T) {
	useTempHome(t)
	useArchiveDir(t)

	fake := newFakeGit()
	fake.repoReady = true
	ui := gitApp(t, fake)

	if _, err := ui.GitTestRemote("git@example.com:a/b.git"); err != nil {
		t.Fatalf("GitTestRemote: %v", err)
	}
	if !fake.calledWith("ls-remote", "git@example.com:a/b.git") {
		t.Errorf("试连应当是 git ls-remote <地址>，实际调用 %v", fake.calls)
	}
	// 不许出现任何会动远端或改本地仓库的命令。
	for _, forbidden := range []string{"push", "fetch", "clone", "add", "commit"} {
		if fake.calledWith(forbidden) {
			t.Errorf("引导里不该出现 %s，实际调用 %v", forbidden, fake.calls)
		}
	}
}

// 地址空着时试连：要明说，而不是让 git 去猜。
func TestGitTestRemoteRefusesAnEmptyAddress(t *testing.T) {
	useTempHome(t)
	useArchiveDir(t)

	ui := gitApp(t, newFakeGit())
	if _, err := ui.GitTestRemote("   "); err == nil {
		t.Error("没填地址应当报错")
	}
}

// 验收标准 5、6、7（评审核对落在用例上）：只有只读命令加一条 git init，
// 且外部调用都带超时。
func TestGitGuideStaysReadOnlyAndTimed(t *testing.T) {
	raw, err := os.ReadFile("gitguide.go")
	if err != nil {
		t.Fatal(err)
	}
	source := stripLineComments(string(raw))

	for _, forbidden := range []string{"push", "fetch", "clone", "reset", "checkout"} {
		// 「只读」这件事另有行为用例盯着（假 git 会把每次调用记下来），
		// 这里只看源码里有没有留下写命令的痕迹——注释与给人看的话不算。
		if strings.Contains(stripStringLiterals(source), forbidden) {
			t.Errorf("gitguide.go 里不该出现 %s——引导只读，推送是备份那一步的事", forbidden)
		}
	}
	// 命令统一由 proc.go 里的构造器造（那里设超时，也设「不弹控制台窗口」）。
	if !strings.Contains(source, "gitCommand(") {
		t.Error("跑 git 要走 gitCommand，别自己拼 exec.Command")
	}
	if !strings.Contains(readSource(t, "proc.go"), "exec.CommandContext") {
		t.Error("外部调用要带超时（exec.CommandContext）")
	}
	if !strings.Contains(source, "GIT_TERMINAL_PROMPT") {
		t.Error("要挡住 git 停下来问凭据——窗口里没有终端能回答它")
	}
	// 写操作只有 init，且目录来自配置。
	if n := strings.Count(source, `"init"`); n != 1 {
		t.Errorf("写操作应当只有 git init 一处，实际出现 %d 次", n)
	}
}

// stripStringLiterals 去掉 Go 源码里双引号与反引号字符串的内容。
//
// 用在「源码里不该出现某个词」这类判据上：给人看的那句话里出现 `git push`
// （教人去命令行推一次）不算写命令，注释也不算。
func stripStringLiterals(source string) string {
	var out strings.Builder
	for i := 0; i < len(source); i++ {
		switch source[i] {
		case '"', '`':
			quote := source[i]
			i++
			for i < len(source) && source[i] != quote {
				if source[i] == '\\' && quote == '"' {
					i++
				}
				i++
			}
		default:
			out.WriteByte(source[i])
		}
	}
	return out.String()
}

// ── 真机验证（默认跳过）────────────────────────────────────────────────────
//
// 真的调这台机器上的 git：先确认装了、再在一个临时目录里 init、读回状态。
// 不碰任何远程（不试连），免得依赖网络与凭据。
func TestGitGuideAgainstTheRealSystem(t *testing.T) {
	if testing.Short() {
		t.Skip("会真的跑 git init；-short 时跳过")
	}

	useTempHome(t)
	archive := useArchiveDir(t)

	ui := NewApp(quietLogger())
	guide, err := ui.GitGuide()
	if err != nil {
		t.Fatalf("GitGuide: %v", err)
	}
	t.Logf("真机：git=%v 版本=%q 已经是仓库=%v 步骤=%s", guide.GitAvailable, guide.GitVersion, guide.RepoReady, guide.Step)
	if !guide.GitAvailable {
		t.Skip("这台机器上没装 git，跳过后面两步")
	}
	if guide.RepoReady {
		t.Fatal("临时目录不该已经是 git 仓库")
	}

	after, err := ui.GitInit()
	if err != nil {
		t.Fatalf("真机 git init: %v", err)
	}
	if !after.RepoReady {
		t.Error("init 之后应当认出这是仓库")
	}
	t.Logf("真机：init 之后 %s 是仓库了；归档目录没被别的命令碰过", archive)
}

// 第 2 步那段话要能直接交给 AI：说清做什么、建**空**仓库、配哪个远程、别动数据（I-029）。
func TestGitPromptTellsTheAssistantWhatToDo(t *testing.T) {
	useTempHome(t)
	dir := useArchiveDir(t)

	ui := gitApp(t, newFakeGit())
	guide, err := ui.GitGuide()
	if err != nil {
		t.Fatal(err)
	}

	for _, want := range []string{"空仓库", "README", "密钥", "git init", "git remote add origin", "不要提交、不要推送"} {
		if !strings.Contains(guide.Prompt, want) {
			t.Errorf("那段 Prompt 里缺 %q：\n%s", want, guide.Prompt)
		}
	}
	if strings.Contains(guide.Prompt, dir) {
		t.Errorf("那段 Prompt 是给 AI 看的，不该嵌具体文件夹（%s）：\n%s", dir, guide.Prompt)
	}
}

// 复制这件事：窗口没起来、或压根没内容时如实报错，不假装成功（I-029）。
func TestCopyTextRefusesNothingAndNoWindow(t *testing.T) {
	useTempHome(t)
	useArchiveDir(t)

	ui := gitApp(t, newFakeGit())
	if err := ui.CopyText("一段话"); err == nil {
		t.Error("窗口还没起来时该如实报错")
	}
	if err := ui.CopyText("   "); err == nil {
		t.Error("空内容该报错")
	}
}

// 界面不再问地址：保存时把认出来的远程记进配置（I-029）。
func TestSaveSettingsTakesTheDetectedRemote(t *testing.T) {
	useTempHome(t)
	useArchiveDir(t)

	fake := newFakeGit()
	fake.repoReady = true
	fake.remote = "git@github.com:Always1023/Memory.git"
	ui := gitApp(t, fake)

	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Backup.Git.Remote != "" {
		t.Fatalf("开始时配置里不该有地址，实际 %q", cfg.Backup.Git.Remote)
	}

	if _, err := ui.SaveSettings(SettingsInput{BackupMode: config.BackupGit}); err != nil {
		t.Fatalf("保存: %v", err)
	}
	cfg, err = config.Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Backup.Git.Remote != fake.remote {
		t.Errorf("保存时应当把认出来的远程记下来，实际 %q", cfg.Backup.Git.Remote)
	}
}

// 认不出来时，别把配置里原有的地址抹掉（界面已经不传地址了，这里也得兜住）。
func TestSaveSettingsKeepsTheRemoteWhenNothingIsDetected(t *testing.T) {
	useTempHome(t)
	dir := useArchiveDir(t)

	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	cfg.ArchiveDir = dir
	cfg.Backup.Mode = config.BackupNone
	cfg.Backup.Git.Remote = "git@example.com:keep/me.git"
	if err := config.Save(cfg); err != nil {
		t.Fatal(err)
	}

	// 假 git 认不出远程（remote 为空、repoReady 为假）。
	ui := gitApp(t, newFakeGit())
	if _, err := ui.SaveSettings(SettingsInput{BackupMode: config.BackupGit}); err != nil {
		t.Fatalf("保存: %v", err)
	}

	cfg, err = config.Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Backup.Git.Remote != "git@example.com:keep/me.git" {
		t.Errorf("认不出来就该原样留着，实际 %q", cfg.Backup.Git.Remote)
	}
}

// 还没连上远程时点「试连」：说清「请先连上远程仓库」（人提的）。
func TestGitTestRemoteSaysConnectTheRemoteFirst(t *testing.T) {
	useTempHome(t)
	useArchiveDir(t)

	ui := gitApp(t, newFakeGit())
	_, err := ui.GitTestRemote("")
	if err == nil {
		t.Fatal("没配远程时该报错")
	}
	if !strings.Contains(err.Error(), "远程仓库") {
		t.Errorf("那句话要说清「请先连上远程仓库」，实际 %q", err.Error())
	}
}
