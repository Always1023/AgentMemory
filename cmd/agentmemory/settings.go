package main

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/Always1023/AgentMemory/internal/archive"
	"github.com/Always1023/AgentMemory/internal/config"
	"github.com/Always1023/AgentMemory/internal/llm"
	wruntime "github.com/wailsapp/wails/v2/pkg/runtime"
)

// 这个文件是「高级设置抽屉」那一层（I-017）：把配置里的几项整理成抽屉要的形状、
// 把抽屉里改的东西写回配置，并顺带把系统定时任务调成一致。
//
// 不做第二套实现：配置读写走 internal/config，定时任务走 schedule.go 那几个函数。

// UISettings 是抽屉里要显示与编辑的东西。
type UISettings struct {
	ArchiveDir     string `json:"archiveDir"`
	ArchiveDefault string `json:"archiveDefault"`
	// ArchiveNote 是「归档目录换过了」那句提示（含旧目录与还剩多少份）；
	// 没换过、或旧目录里没东西时是空串。只提示，不搬（I-024）。
	ArchiveNote string `json:"archiveNote"`
	// ArchiveOld 是上一个归档目录（没换过就是空串）；ArchiveOldCount 是它里面
	// 还剩几段。界面据此显示「把旧目录里的 N 段复制过来」（I-024）。
	ArchiveOld      string     `json:"archiveOld"`
	ArchiveOldCount int        `json:"archiveOldCount"`
	BackupMode      string     `json:"backupMode"`
	BackupRemote    string     `json:"backupRemote"`
	BackupModes     []UIOption `json:"backupModes"`

	ScheduleEnabled bool   `json:"scheduleEnabled"`
	ScheduleDailyAt string `json:"scheduleDailyAt"`
	// ScheduleInstalled 是**从系统里读回来的**：系统里到底有没有那个任务。
	//
	// 它可能与配置里的开关不一致——那正是要让人看见的（「说了要开定时，其实没建成」）。
	ScheduleInstalled bool `json:"scheduleInstalled"`
	// ScheduleNextRun 是系统里那个任务的下次运行时间。
	ScheduleNextRun string `json:"scheduleNextRun"`
	// ScheduleLastRun 是「上次运行」那一句的值：运行记录里最近一次**定时**运行的
	// 时间（"2026-10-22 03:24"）；没有记录时是「还没有记录」，读不到时是
	// 「读不到运行记录」。手动收集与重试不算（I-073）。
	ScheduleLastRun string `json:"scheduleLastRun"`
	// MCPEnabled 是「对外接口」开关（F15）。MCPCommand / MCPArgs 是给客户端抄的两项：
	// 命令只填 exe 路径，-mcp 放参数里（ccs 这类管理器两个框分开填）。
	MCPEnabled bool   `json:"mcpEnabled"`
	MCPCommand string `json:"mcpCommand"`
	MCPArgs    string `json:"mcpArgs"`
	// LLMEnabled 是「视角（LLM）」开关；LLMConfigured / LLMHint 告诉人环境变量齐不齐。
	LLMEnabled bool   `json:"llmEnabled"`
	LLMHint    string `json:"llmHint"`
	// ScheduleNote 是定时那一段的一句说明（读系统失败、或与配置对不上时用它）。
	ScheduleNote string `json:"scheduleNote"`

	ConfigPath string `json:"configPath"`
}

// SettingsInput 是抽屉里改动的项。
//
// 空值表示「这一项没动」——与 SaveWizard 同一个路子：只写传进来的字段，
// 免得「只点了备份方式」把半截的归档目录也一起存下去。
type SettingsInput struct {
	ArchiveDir   string `json:"archiveDir"`
	BackupMode   string `json:"backupMode"`
	BackupRemote string `json:"backupRemote"`
	// SetRemote 为真时，即使 BackupRemote 为空也要写进去（人主动清空）。
	SetRemote bool `json:"setRemote"`
	// CopyOldArchives 为真表示：这次换目录把旧目录里的归档**复制**过来
	//（人在「归档目录」那一段选过；人提的「直接给选项让用户决定要不要迁移」）。
	CopyOldArchives bool `json:"copyOldArchives"`
	// ScheduleEnabled 为 nil 表示这一项没动；否则按它开关定时。
	ScheduleEnabled *bool `json:"scheduleEnabled"`
	// ScheduleDailyAt 为空表示这一项没动。
	ScheduleDailyAt string `json:"scheduleDailyAt"`
	// MCPEnabled 为 nil 表示这一项没动；否则按它开关「对外接口」。
	MCPEnabled *bool `json:"mcpEnabled"`
	// LLMEnabled 为 nil 表示这一项没动；否则按它开关「视角（LLM）」。
	LLMEnabled *bool `json:"llmEnabled"`
}

// Settings 读一遍抽屉要显示的东西。
//
// 这一步会去问系统里那个定时任务（schtasks），所以**只在抽屉打开时调**：
// 抽屉折叠着的时候不去打扰系统（《前端设计规范》3.5「折叠时不渲染内容」）。
func (a *App) Settings() (UISettings, error) {
	cfg, err := config.Load()
	if err != nil {
		return UISettings{}, err
	}
	path, err := config.Path()
	if err != nil {
		return UISettings{}, err
	}

	out := UISettings{
		ArchiveDir:      cfg.ArchiveDir,
		ArchiveNote:     a.archiveNoteSnapshot(),
		ArchiveOld:      a.archiveOldSnapshot(),
		ArchiveDefault:  config.Default().ArchiveDir,
		BackupMode:      cfg.Backup.Mode,
		BackupRemote:    cfg.Backup.Git.Remote,
		BackupModes:     a.backupModes(),
		ScheduleEnabled: cfg.Schedule.Enabled,
		ScheduleDailyAt: cfg.Schedule.DailyAt,
		ScheduleLastRun: a.scheduleLastRun(),
		MCPEnabled:      cfg.MCPEnabled,
		MCPCommand:      mcpExecutable(),
		MCPArgs:         mcpArgs,
		LLMEnabled:      cfg.LLMEnabled,
		LLMHint:         llmConfiguredHint(),
		ConfigPath:      path,
	}

	// 「旧目录里还剩几段」要现数（换过目录才有值）：界面那个按钮上写的就是它。
	if out.ArchiveOld != "" {
		if stats, err := archive.Count(out.ArchiveOld, time.Now()); err == nil {
			out.ArchiveOldCount = stats.Total
		}
	}

	status, err := readSchedule(a.scheduler())
	if err != nil {
		// 读不出系统里的任务，不代表抽屉打不开：如实写一句，别的照常显示。
		out.ScheduleNote = "读系统里的定时任务失败：" + oneLine(err)
		return out, nil
	}
	out.ScheduleInstalled = status.Installed
	out.ScheduleNextRun = status.NextRun
	out.ScheduleNote = scheduleNote(cfg, status)
	return out, nil
}

// scheduleNote 写定时那一段的一句说明；没事要说时为空。
//
// 文案只在这一个函数里（与 I-015 的 runStateText 同一条规矩）。
func scheduleNote(cfg config.Config, status ScheduleStatus) string {
	switch {
	case !cfg.Schedule.Enabled && !status.Installed:
		return "没开定时；开了之后每天这个点自动收一次。"
	case !cfg.Schedule.Enabled && status.Installed:
		return "配置里已经关掉了，但系统里还留着这个任务——保存一次就会清掉。"
	case cfg.Schedule.Enabled && !status.Installed:
		return "配置里开着，但系统里没有这个任务——保存一次就会建它。"
	case status.DailyAt != "" && status.DailyAt != cfg.Schedule.DailyAt:
		return fmt.Sprintf("系统里的任务是每天 %s，与这里的 %s 对不上——保存一次会按这里的重建。",
			status.DailyAt, cfg.Schedule.DailyAt)
	case status.NextRun == "":
		return "系统里有这个任务，但读不出下次运行时间。"
	default:
		return ""
	}
}

// 设置页「上次运行」没有时间可显示时的两种状态。
const (
	scheduleLastRunNone       = "还没有记录"
	scheduleLastRunUnreadable = "读不到运行记录"
)

// scheduleLastRun 取运行记录里最近一次「定时」运行的时间，给设置页的定时区块用（I-073）。
//
// 只看来源为定时的记录：手动收集与重试回答不了「定时任务有没有在跑」。
// 没有记录与读不到是两回事，分别给一句不同的话——不能把读失败说成「还没跑过」。
func (a *App) scheduleLastRun() string {
	db, err := openStoreDB()
	if err != nil {
		a.logger.Warn("读运行记录失败", "原因", err)
		return scheduleLastRunUnreadable
	}
	defer db.Close()
	runs, err := db.ReadRuns(runLogKeepDays, time.Now())
	if err != nil {
		a.logger.Warn("读运行记录失败", "原因", err)
		return scheduleLastRunUnreadable
	}
	for i := len(runs) - 1; i >= 0; i-- {
		if runs[i].Source == runSourceSchedule {
			return runs[i].At.In(time.Local).Format("2006-01-02 15:04")
		}
	}
	return scheduleLastRunNone
}

// SaveSettings 把抽屉里改的东西写回配置，并把系统里的定时任务调成一致。
//
// 「立即生效」的意思是：配置与定时任务这一刻就改好了，不用重启——归档目录与备份
// 方式是每次收集现读配置的，定时任务直接调 schtasks 建 / 删。
func (a *App) SaveSettings(input SettingsInput) (UISettings, error) {
	cfg, err := config.Load()
	if err != nil {
		return UISettings{}, err
	}

	oldArchive := cfg.ArchiveDir

	if dir := strings.TrimSpace(input.ArchiveDir); dir != "" {
		cfg.ArchiveDir = dir
	}
	if mode := strings.TrimSpace(input.BackupMode); mode != "" {
		cfg.Backup.Mode = mode
	}
	if input.SetRemote {
		cfg.Backup.Git.Remote = strings.TrimSpace(input.BackupRemote)
	}
	if input.ScheduleEnabled != nil {
		cfg.Schedule.Enabled = *input.ScheduleEnabled
	}
	if at := strings.TrimSpace(input.ScheduleDailyAt); at != "" {
		cfg.Schedule.DailyAt = at
	}
	if input.MCPEnabled != nil {
		cfg.MCPEnabled = *input.MCPEnabled
	}
	if input.LLMEnabled != nil {
		cfg.LLMEnabled = *input.LLMEnabled
	}

	// 界面不再问仓库地址（人提的：有「试连」就够了）——选着 Git 仓库、而配置里
	// 还没有地址时，现认一次本地仓库里配好的远程，认到就记下来（I-029）。
	if cfg.Backup.Mode == config.BackupGit && strings.TrimSpace(cfg.Backup.Git.Remote) == "" {
		if remote := a.detectedRemote(); remote != "" {
			cfg.Backup.Git.Remote = remote
		}
	}

	if strings.TrimSpace(cfg.ArchiveDir) == "" {
		return UISettings{}, errors.New("归档目录不能为空")
	}
	if err := cfg.Validate(); err != nil {
		return UISettings{}, err
	}
	if err := config.Save(cfg); err != nil {
		return UISettings{}, err
	}

	// 配置已经记下了人的意图，接下来把它落到系统里。这一步失败要如实报，
	// 但**不回滚配置**：配置里留着「我要开定时」，下次保存会再试，
	// 而界面同时会显示「系统里其实没有这个任务」——人看得见，不会被蒙在鼓里。
	//
	// 只在这次改动碰上定时那两处时才去碰系统：备份方式、归档目录这些跟定时无关
	// 的改动每次保存都建/删一次任务，等于每点一下按钮就 spawn 一个进程，人感觉到
	// 的就是「点一下卡一下」。「保存」那条路一定会带上定时的时刻，所以界面里
	// 「保存一次就会清掉」那句提示照样成立。
	scheduleTouched := input.ScheduleEnabled != nil || strings.TrimSpace(input.ScheduleDailyAt) != ""
	if scheduleTouched {
		if err := a.syncSchedule(cfg); err != nil {
			a.logger.Warn("定时任务没能与配置对齐", "原因", err)
			settings, _ := a.Settings()
			return settings, err
		}
	}

	// 归档目录换了：按人这次的选择处理（复制过来 / 留在原地），并把结果说清楚。
	a.afterArchiveChange(oldArchive, cfg.ArchiveDir, input.CopyOldArchives)

	a.logger.Info("高级设置已保存",
		"归档目录", cfg.ArchiveDir,
		"备份方式", cfg.Backup.Mode,
		"定时", cfg.Schedule.Enabled,
		"时刻", cfg.Schedule.DailyAt)
	return a.Settings()
}

// afterArchiveChange 处理「归档目录换了」这件事（I-024）。
//
// 人选了把旧目录里的归档复制过来，就当场复制；没选就只给一句提示。
// 收集期间**不复制**：两边都会写归档目录，撞一起谁都说不清。
func (a *App) afterArchiveChange(oldDir, newDir string, wantCopy bool) {
	if !wantCopy || oldDir == "" || oldDir == newDir {
		a.setArchiveNote(oldDir, newDir, archiveSwapNote(oldDir, newDir))
		return
	}
	if a.collectingNow() {
		a.setArchiveNote(oldDir, newDir,
			"换好了。不过这一轮还在收集，先不复制文件——等收完点下面的「复制旧目录里的归档」。")
		return
	}

	a.mu.Lock()
	a.migrating = true
	a.mu.Unlock()
	report, err := archive.Migrate(oldDir, newDir)
	a.mu.Lock()
	a.migrating = false
	a.mu.Unlock()

	if err != nil {
		a.logger.Warn("换目录时复制归档没能完成", "原因", err)
		a.setArchiveNote(oldDir, newDir, fmt.Sprintf(
			"换好了，但复制旧目录里的东西没成功：%s。旧目录里的还在，稍后可以点下面的「复制旧目录里的归档」再试。",
			oneLine(err)))
		return
	}
	a.setArchiveNote(oldDir, newDir, migrateSwapNote(oldDir, newDir, report))
	a.logger.Info("换目录时复制了旧归档", "旧目录", oldDir, "新目录", newDir,
		"复制", report.Copied, "跳过", report.Skipped, "撞名", len(report.Conflicts))
}

// migrateSwapNote 是「换目录 + 复制过去」之后那句话；没得复制就是空串。
func migrateSwapNote(oldDir, newDir string, report archive.MigrateReport) string {
	switch {
	case len(report.Conflicts) > 0:
		return fmt.Sprintf("归档目录已改为 %s，但旧目录里有 %d 段在新目录里已经是同名、内容却不一样——"+
			"一份都没复制。先看看那几份要留哪个，再点下面的「复制旧目录里的归档」。",
			newDir, len(report.Conflicts))
	case report.OldTotal == 0:
		return ""
	default:
		return fmt.Sprintf("归档目录已改为 %s，并把旧目录 %s 里的 %d 段复制过来了（内容相同的 %d 段跳过）。"+
			"旧目录里的东西一份没动，你确认之后可以自己清。", newDir, oldDir, report.Copied, report.Skipped)
	}
}

// syncSchedule 让系统里的定时任务与配置一致：开着就建（按新时间覆盖），
// 关着就删。
func (a *App) syncSchedule(cfg config.Config) error {
	runner := a.scheduler()
	if cfg.Schedule.Enabled {
		return writeSchedule(runner, cfg.Schedule.DailyAt)
	}
	return removeSchedule(runner)
}

// BrowseDirectory 打开系统的「选择文件夹」对话框，返回人选中的目录。
//
// 取消时返回空串、错误为空——「什么都没选」是正常结果，不是出错
// （I-017 验收标准 8）。调用方（app.js）据此决定不动原来的值。
func (a *App) BrowseDirectory(current string) (string, error) {
	if a.ctx == nil {
		// 窗口没起来就没有对话框可开。返回可读错误，别让调用方以为「人选了空」。
		return "", errors.New("窗口还没起来，打不开文件夹选择框")
	}

	options := wruntime.OpenDialogOptions{Title: "选择目录"}
	// 默认定位到现在的那个目录；路径不存在或不合法时别传过去，
	// 传了对话框会打不开（人正想换个目录，反倒卡在这一步）。
	if dir := strings.TrimSpace(current); dir != "" {
		if info, err := os.Stat(dir); err == nil && info.IsDir() {
			options.DefaultDirectory = dir
		}
	}

	picked, err := wruntime.OpenDirectoryDialog(a.ctx, options)
	if err != nil {
		return "", fmt.Errorf("打开文件夹选择框失败：%w", err)
	}
	return picked, nil
}

// scheduler 拿到跑系统定时任务的那个出口。
//
// App 是从 NewApp 建出来的，这里本来可以直接读字段；留这一层是为了用例把出口
// 换成假的之后，别的代码一行都不用改。
func (a *App) scheduler() scheduleRunner {
	if a.schedule == nil {
		return runSchtasks
	}
	return a.schedule
}

// archiveSwapNote 拼「归档目录换了」那句提示；没换或旧目录里没东西就说空串。
//
// 数一下旧目录里还剩多少份，报实数——「有些东西留在那儿」和「283 份留在那儿」
// 对人是两回事。数不动也不慌：提示照给，只是不带数字。
func archiveSwapNote(oldDir, newDir string) string {
	if oldDir == "" || newDir == "" || oldDir == newDir {
		return ""
	}
	stats, err := archive.Count(oldDir, time.Now())
	if err != nil {
		return fmt.Sprintf("归档目录已改为 %s。旧目录 %s 里的归档不会自己跟过来——"+
			"想让它们也在这儿，点下面的「复制旧目录里的归档」（只复制，不删旧目录里的）。", newDir, oldDir)
	}
	if stats.Total == 0 {
		return ""
	}
	return fmt.Sprintf("归档目录已改为 %s。旧目录 %s 里还留着 %d 段归档，它们不会自己跟过来；"+
		"想让它们也在这儿，点下面的「复制旧目录里的归档」（只复制，不删旧目录里的）。",
		newDir, oldDir, stats.Total)
}

// setArchiveNote 记下这句提示（界面读设置时显示在「归档目录」那一段下面），
// 同时记住旧目录——界面上那个「把旧目录里的 N 份复制过来」要它（I-024）。
func (a *App) setArchiveNote(oldDir, newDir, note string) {
	a.mu.Lock()
	a.archiveNote = note
	if oldDir != "" && oldDir != newDir {
		a.archiveOld = oldDir
	} else {
		a.archiveOld = ""
	}
	a.mu.Unlock()
}

// archiveOldSnapshot 取「上一个归档目录」（没换过就是空串）。
func (a *App) archiveOldSnapshot() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.archiveOld
}

// archiveNoteSnapshot 取那句提示（界面读设置时带上）。
func (a *App) archiveNoteSnapshot() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.archiveNote
}

// MigrateArchive 把旧归档目录里的归档**复制**到当前目录（I-024 / F10）。
//
// 规矩都在 archive.Migrate 里（只复制不搬、撞名内容不同就整趟不动、幂等）；
// 这里只管两件事：收集期间不许搬（同一把锁，免得两件事一起写归档目录），
// 以及把结果整理成一句人看得懂的话。
func (a *App) MigrateArchive() (UIMigrate, error) {
	oldDir := a.archiveOldSnapshot()
	if oldDir == "" {
		return UIMigrate{}, errors.New("还没换过归档目录，没有要搬的东西")
	}
	cfg, err := config.Load()
	if err != nil {
		return UIMigrate{}, err
	}
	if cfg.ArchiveDir == oldDir {
		return UIMigrate{}, errors.New("当前归档目录就是它，没有要搬的东西")
	}

	a.mu.Lock()
	if a.collecting {
		a.mu.Unlock()
		return UIMigrate{}, errors.New("正在收集，等这一轮收完再搬（两边都会写归档目录）")
	}
	a.migrating = true
	a.mu.Unlock()
	defer func() {
		a.mu.Lock()
		a.migrating = false
		a.mu.Unlock()
	}()

	report, err := archive.Migrate(oldDir, cfg.ArchiveDir)
	if err != nil {
		a.logger.Warn("搬归档没能完成", "原因", err)
		return UIMigrate{}, err
	}

	out := UIMigrate{
		Copied:    report.Copied,
		Skipped:   report.Skipped,
		Conflicts: len(report.Conflicts),
	}
	switch {
	case out.Conflicts > 0:
		out.Message = fmt.Sprintf("有 %d 份在新目录里已经是同名但内容不一样的文件，一份都没动——先看看那几份要留哪个，再来一次。", out.Conflicts)
	case report.OldTotal == 0:
		out.Message = "旧目录里没有归档文件，不用搬。"
	default:
		out.Message = fmt.Sprintf("搬完了：新复制 %d 份，已有相同跳过 %d 份。旧目录里的东西一份没动，你确认之后可以自己清。",
			out.Copied, out.Skipped)
	}
	a.logger.Info("搬归档", "旧目录", oldDir, "新目录", cfg.ArchiveDir,
		"复制", out.Copied, "跳过", out.Skipped, "撞名", out.Conflicts)
	return out, nil
}

// mcpArgs 是启动 MCP 服务的那一个参数。
const mcpArgs = "-mcp"

// mcpExecutable 给出当前 exe 的完整路径（给外部客户端抄；路径有空格时客户端要自己加引号）。
func mcpExecutable() string {
	exe, err := os.Executable()
	if err != nil || strings.TrimSpace(exe) == "" {
		return "AgentMemory.exe"
	}
	return exe
}

// llmConfiguredHint 说明模型环境变量齐不齐（不显示密钥本身）。
func llmConfiguredHint() string {
	client := llm.FromEnv()
	if err := client.Configured(); err != nil {
		return err.Error()
	}
	return "模型：" + client.ModelTag() + " · 端点：" + client.BaseURL
}
