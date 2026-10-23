package main

import (
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"

	"github.com/Always1023/AgentMemory/internal/app"
	"github.com/Always1023/AgentMemory/internal/archive"
	"github.com/Always1023/AgentMemory/internal/backup"
	"github.com/Always1023/AgentMemory/internal/backup/none"
	"github.com/Always1023/AgentMemory/internal/backups"
	"github.com/Always1023/AgentMemory/internal/channel"
	"github.com/Always1023/AgentMemory/internal/config"
	"github.com/Always1023/AgentMemory/internal/store"
)

// 这个文件是窗口与后端之间的那一层。
//
// 只放「界面要、编排层不提供」的东西：把状态整理成前端好用的形状、把界面的动作
// 转成对 config 与 app 的调用。业务逻辑一律在 internal/*，这里不做第二套实现。

// firstRunSteps 是首次运行引导有几步（归档目录 → 渠道 → 备份）。
const firstRunSteps = 3

// UIOption 是一个选择项。
type UIOption struct {
	Value string `json:"value"`
	Label string `json:"label"`
	// NeedsRemote 为真时，界面要一并问仓库地址。
	NeedsRemote bool `json:"needsRemote"`
	// Hint 是选中这一项时跟在后面的一句提示；为空则不显示。
	//
	// 放在这里、而不是让前端按取值去查：前端不认备份方式的标识（`none` / `git` …），
	// 认的话每加一种方式两边都要改。
	Hint string `json:"hint"`
}

// UIChannel 是界面上一个渠道的状态。
type UIChannel struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	// State 是给人看的一档：ok（可用）/ none（未检测到或已停用）/ bad（有失败）。
	// 界面按它挑颜色，不自己去解释 Status 的三种取值。
	State string `json:"state"`
	// Detail 是一句话说明（「发现 25 · 新增 25 · 归档 25」这类）。
	// 文案只在 runStateText 一处定义（I-015 验收标准 5）。
	Detail string `json:"detail"`
	// Found 是检测到的会话源文件数（「本机会话」那一层）。
	Found int `json:"found"`
	// Group 是首页分组：本机有会话 / 已安装无会话 / 未安装 / 检测失败（I-066）。
	// 界面只按组名分段，不自己解释检测状态。
	Group string `json:"group"`
	// HostToday 是其中落盘日期是今天的有多少个（本机层的「今日新增」）。
	HostToday int `json:"hostToday"`
	// Valid 是本机解析成功且至少有一条消息的会话数（I-051）。
	// 它不等于归档份数：未收集的渠道也应该在这里显示真实的有效会话。
	Valid int `json:"valid"`
	// Empty 是解析成功但没有任何消息的会话数（打开过没说话）。
	Empty int `json:"empty"`
	// ParseFailed 是本机统计时解析失败的源文件数。
	ParseFailed int `json:"parseFailed"`
	// Collected 是这个渠道已经收进归档目录的份数（「已收集」那一层）。
	Collected int `json:"collected"`
	// CollectedToday 是其中今天收进来的份数（已收集层的「今日保存」）。
	CollectedToday int `json:"collectedToday"`
	// PendingNew 是还没有归档过的新会话数（游标里没有，或上一轮是空 / 失败）。
	PendingNew int `json:"pendingNew"`
	// PendingUpdated 是已经归档过、内容又变了的会话数。
	// 两者分开显示，避免用一个「待收 N」让人和「已收集 N」做减法（I-062）。
	PendingUpdated int `json:"pendingUpdated"`
	// New / Archived / Failed 是最近一轮这个渠道的数字；还没跑过时都是零。
	New      int `json:"new"`
	Archived int `json:"archived"`
	Failed   int `json:"failed"`
	// Enabled 为 false 表示配置里停用了这个渠道。
	Enabled bool `json:"enabled"`
	// LastDuration 是「这一行刚被单点收过一次」的那一轮用时（形如 11.024s）；
	// 界面把它跟在勾后面（人提的：「✓ 11.024s」该在那一行，不在顶部）。
	LastDuration string `json:"lastDuration"`
	// Action 是这一行末尾那个标记该显示什么（人提的：两种状态，别再用「重试」两个字）：
	//   collect —— 还有没转移的内容（或上一轮失败了）：一个转圈箭头，点一下就收这个渠道；
	//   done    —— 没有新内容：一个绿勾，不用点；
	//   空       —— 这一行不适用（未检测到 / 已停用）。
	Action string `json:"action"`
}

// 渠道行末尾那个标记的两种状态。
const (
	channelActionCollect = "collect"
	channelActionDone    = "done"
)

// 首页渠道分组。检测失败单列一段：不知道本机有没有会话，又不能归进「未安装」。
const (
	channelGroupFailed              = "failed"
	channelGroupHasSessions         = "has_sessions"
	channelGroupInstalledNoSessions = "installed_no_sessions"
	channelGroupNotInstalled        = "not_installed"
)

// UIRun 是上次收集的结果，界面用它的几个数字。
type UIRun struct {
	At       string `json:"at"`
	Found    int    `json:"found"`
	New      int    `json:"new"`
	Archived int    `json:"archived"`
	Failures int    `json:"failures"`
	// Empty 是这一轮跳过的「空会话」数（打开过没说话的那些）：与失败是两回事（I-039）。
	Empty int `json:"empty"`
	// Scope 是这一轮的范围：空表示「全部渠道」，非空是只重试了某个渠道的名字。
	Scope string `json:"scope"`
	// Duration 是这一轮跑了多久（已收成毫秒级的一小段）。
	Duration string `json:"duration"`
	// Detail 是备份那一步的一句话。
	BackupDetail string `json:"backupDetail"`
	BackupErr    string `json:"backupErr"`
	// BackupMethod 是这一步实际用的方式（none / git / sync），只给运行记录判断
	// 「仅本地不算备份」用，不发给界面（I-053）。
	BackupMethod string `json:"-"`
	// Err 非空表示这一轮**没跑完**（系统错：归档写不进去这类）。
	// 与 Failures 不同：那些是「跳过几条、整轮照跑」，这个是「整轮停下」。
	Err string `json:"err"`
	// ErrHint 是上面那句原话翻成的「为什么 + 怎么办」（I-034）；认不出类别时
	// 就是「照原话说一句 + 请人把原话发过来」。界面直接显示它。
	ErrHint string `json:"errHint"`
	// BackupHint 是备份没推上去时的那句「为什么 + 怎么办」（I-034）。
	BackupHint string `json:"backupHint"`
	// FromHistory 为真表示这条是从运行记录读回的上一轮，不是本进程刚跑完的。
	// 界面照样显示时间和份数，但顶部不能把它说成刚发生（I-061）。
	FromHistory bool `json:"fromHistory,omitempty"`
}

// UIProgress 是一条进度，前端直接显示 Text。
type UIProgress struct {
	// Text 是这一条要说的话（「正在收集 Claude Code（2/4）」）。
	Text string `json:"text"`
	// Channel 是这条进度对应的渠道 ID；不是渠道的进度为空。
	Channel string `json:"channel"`
	// Done 为真表示这一轮跑完了。
	Done bool `json:"done"`
	// Percent 是这一行（这个渠道）的进度，0-100；-1 表示这一步跟某一行无关，
	// 界面不动那一行的数字（人提的：要能看到 0% 到 100%，不然以为卡住了）。
	Percent int `json:"percent"`
	// Finished 为真表示这个渠道跑完了，界面先把它那一行画成勾。
	Finished bool `json:"finished"`
	// Stage 是这一步是什么：detect（检测）/ channel（收某个渠道）/ backup（提交推送）/ done。
	// 界面靠它决定那个「在动的数字」挂在哪一行（备份那一步没有文件可数）。
	Stage string `json:"stage"`
}

// UIReadingProgress 是打开/刷新主界面时读取统计的进度。
//
// 与收集进度分开：读取只解析、不写归档，也不该让主按钮进入「收集中」。
type UIReadingProgress struct {
	Text    string `json:"text"`
	Percent int    `json:"percent"`
	Done    bool   `json:"done"`
}

// UIState 是界面要显示的全部东西。
type UIState struct {
	// Headline 是顶部那句状态话。
	Headline string `json:"headline"`
	// ConfigPath 是配置文件路径（引导里显示，方便人自己去找）。
	ConfigPath string `json:"configPath"`

	ArchiveDir string `json:"archiveDir"`
	// ArchiveDefault 是归档目录的默认值，引导里当占位提示用。
	ArchiveDefault string `json:"archiveDefault"`

	BackupMode   string     `json:"backupMode"`
	BackupRemote string     `json:"backupRemote"`
	BackupModes  []UIOption `json:"backupModes"`

	FirstRunDone  bool `json:"firstRunDone"`
	FirstRunStep  int  `json:"firstRunStep"`
	FirstRunSteps int  `json:"firstRunSteps"`

	// ChannelStats 是渠道那一段顶上那句话（I-023）：三层数字一句话说清
	// ——本机会话、本机有效会话、已收集与待收。
	ChannelStats string `json:"channelStats"`

	Channels []UIChannel `json:"channels"`

	// LastRun 是上次收集的结果；本进程还没收过时，是运行记录里读回的上一轮
	// （带 FromHistory 标记）。运行记录也为空时才是 nil（I-061）。
	LastRun *UIRun `json:"lastRun"`

	// ArchivedTotal 是归档目录里现有的 Markdown 份数。
	// 只在没有运行记录可显示时由前端兜底用（I-061），有上一轮时不重复数。
	ArchivedTotal int `json:"archivedTotal"`

	// ArchivedDesktop / ArchivedWeb 把上面那个总数按来源拆开（I-078）：
	// 桌面端收的份数，与网页版捕获插件写进来的份数。判据在 archive.Count。
	ArchivedDesktop int `json:"archivedDesktop"`
	ArchivedWeb     int `json:"archivedWeb"`

	// Collecting 报告当前有没有在收集（界面据此把主按钮置灰）。
	Collecting bool `json:"collecting"`

	// Reading 为真表示这次 State 调用正在读取渠道数据。它是给调用方看的状态；
	// 实际的渠道级进度通过 progressEvent 推送。
	Reading bool `json:"reading"`
}

// readingStartedEvent 是读取统计开始时的进度事件。
// readingProgressEvent 与 readingFinishedEvent 是同一段过场的后续与收尾。
const (
	readingStartedEvent  = "state:reading-started"
	readingProgressEvent = "state:reading-progress"
	readingFinishedEvent = "state:reading-finished"
)

// UIDailyRun 是日志页里的一行：一天一行（I-030）。
type UIDailyRun struct {
	// Day 是这一天（2006-01-02），Label 是给人看的写法（今天 / 昨天 / 10-16 周五）。
	Day   string `json:"day"`
	Label string `json:"label"`
	// Archived 是那天**收下**的份数（与渠道那段的「已收集」同口径）；
	// Runs 是那天跑了几轮；Failures 是那天失败了几处。
	Archived int `json:"archived"`
	Runs     int `json:"runs"`
	Failures int `json:"failures"`
	// Source 是那天是谁发起的：定时 / 手动 / 重试（都出现过就都列上）。
	Source string `json:"source"`
	// Backups 是那天备份成过的次数（提交并推上去），BackupFailures 是没成的次数。
	Backups        int `json:"backups"`
	BackupFailures int `json:"backupFailures"`
	// BackupNote 是最近一次备份的说明；BackupNoteFailed 为真时它是失败原因。
	// 首页不再显示备份（I-053），日志页靠这两项说清「成没成、为什么没成」。
	BackupNote       string `json:"backupNote"`
	BackupNoteFailed bool   `json:"backupNoteFailed"`
}

// UIMigrate 是「把旧目录里的归档复制过来」的结果（I-024）。
type UIMigrate struct {
	Copied    int    `json:"copied"`
	Skipped   int    `json:"skipped"`
	Conflicts int    `json:"conflicts"`
	Message   string `json:"message"`
}

// WizardInput 是引导里那几步填上来的东西。
type WizardInput struct {
	// Step 是保存之后应当停在第几步。
	Step int `json:"step"`
	// ArchiveDir 为空表示这一步没改。
	ArchiveDir string `json:"archiveDir"`
	// BackupMode 为空表示这一步没改。
	BackupMode string `json:"backupMode"`
	// BackupRemote 是仓库地址；允许为空（「跳过」）。
	BackupRemote string `json:"backupRemote"`
	// SetRemote 为真时，即使 BackupRemote 为空也要把它写进去（人主动清空）。
	SetRemote bool `json:"setRemote"`
}

// State 读一遍配置与渠道状态，返回界面要显示的东西。
func (a *App) State() (UIState, error) {
	return a.readState(false)
}

// StateWithProgress 读取界面状态，并在读取渠道时推送进度事件。
//
// 启动和主界面刷新走这一条：先把「正在读取数据」画出来，再让进度条动起来，
// 完成后一次性交回数字，避免界面先显示 0、随后突然跳变。
func (a *App) StateWithProgress() (UIState, error) {
	return a.readState(true)
}

func (a *App) readState(reportReading bool) (UIState, error) {
	cfg, err := config.Load()
	if err != nil {
		return UIState{}, err
	}
	path, err := config.Path()
	if err != nil {
		return UIState{}, err
	}

	lastRun := a.lastRunSnapshot()
	if lastRun == nil {
		// 重启后内存里没有本轮结果：从运行记录读回上一轮，别把已有归档说成
		// 「还没收过」（I-061）。读不动就照旧返回 nil，界面还有归档份数兜底。
		lastRun = a.lastRunFromLog()
	}

	out := UIState{
		ConfigPath:     path,
		ArchiveDir:     cfg.ArchiveDir,
		ArchiveDefault: config.Default().ArchiveDir,
		BackupMode:     cfg.Backup.Mode,
		BackupRemote:   cfg.Backup.Git.Remote,
		BackupModes:    a.backupModes(),
		FirstRunDone:   cfg.FirstRunDone,
		FirstRunStep:   clampStep(cfg.FirstRunStep),
		FirstRunSteps:  firstRunSteps,
		LastRun:        lastRun,
		Collecting:     a.collectingNow(),
		Reading:        reportReading,
	}
	if reportReading {
		// 先把「正在读取」交出去：Wails 的绑定调用本身要等结果，若只靠 State
		// 返回值，界面在读取期间仍是一片旧数字。事件先到，前端就能画过场。
		a.emit(readingStartedEvent, UIReadingProgress{Text: "正在读取数据…", Percent: 0})
	}

	// 读取进度的阶段与收集共用一套事件；前端按 stage 区分「读取」和「收集」。
	var progress func(app.Progress)
	if reportReading {
		var doneChannels, totalChannels int
		progress = func(p app.Progress) {
			switch p.Stage {
			case app.StageDetect:
				a.emit(readingProgressEvent, UIReadingProgress{Text: "正在检测渠道…", Percent: 0})
			case app.StageChannel:
				if p.Total > 0 {
					totalChannels = p.Total
				}
				percent := 0
				if totalChannels > 0 {
					percent = doneChannels * 100 / totalChannels
					// 当前渠道里再按文件走：一个渠道有 300 个文件时，
					// 不能整段停在同一个百分比上（I-056）。
					if p.FileTotal > 0 {
						filePercent := p.FileDone * 100 / p.FileTotal
						percent = (doneChannels*100 + filePercent) / totalChannels
					}
				}
				text := fmt.Sprintf("正在读取 %s…", p.ChannelName)
				if p.FileTotal > 0 {
					text = fmt.Sprintf("正在读取 %s…（%d/%d）", p.ChannelName, p.FileDone, p.FileTotal)
				}
				a.emit(readingProgressEvent, UIReadingProgress{Text: text, Percent: percent})
				if p.ChannelDone {
					doneChannels++
				}
			case app.StageDone:
				a.emit(readingFinishedEvent, UIReadingProgress{Text: "读取完成", Percent: 100, Done: true})
			}
		}
	}

	// 那一轮要是没跑完 / 没推上去，先把「为什么 + 怎么办」两句补上（I-034），
	// 顶部那句话用的就是它们。历史行没有原始错误，也没有 hint 可补（I-061）。
	if out.LastRun != nil && !out.LastRun.FromHistory {
		fillRunHints(out.LastRun, cfg.ArchiveDir)
	}

	out.Headline = headlineFor(cfg, out.LastRun)

	detectionFailed := false
	channels, err := a.channelStates(cfg, progress)
	if err != nil {
		// 检测出问题不代表界面不能用：如实显示成「检测失败」比整个界面报错好。
		a.logger.Warn("渠道检测失败", "原因", err)
		out.Headline = "渠道检测失败：" + err.Error()
		detectionFailed = true
	}
	out.Channels = channels
	if reportReading {
		// 读取事件只负责让进度条走完；具体数字要等 StateWithProgress 的返回值到达后
		// 再由 renderMain 露出，避免「事件先到、旧数字先闪一下」（I-051）。
		a.emit(readingFinishedEvent, UIReadingProgress{Text: "读取完成", Percent: 100, Done: true})
	}

	// 渠道那一段顶上的数字（I-023）。检测挂了就如实说读不到。
	if detectionFailed {
		out.ChannelStats = "读不到（渠道检测失败）"
	} else {
		out.ChannelStats = channelStatsText(channels)
	}

	// 没有运行记录可显示时，给前端一个归档份数：只要归档里还有东西，
	// 就不能说成「从来没收集过」（I-061）。有上一轮时前端不读这个数，不必多走一遍。
	if out.LastRun == nil && strings.TrimSpace(cfg.ArchiveDir) != "" {
		if stats, err := archive.Count(cfg.ArchiveDir, time.Now()); err == nil {
			out.ArchivedTotal = stats.Total
			out.ArchivedDesktop = stats.Desktop
			out.ArchivedWeb = stats.Web
		} else {
			a.logger.Warn("数归档目录失败", "原因", err)
		}
	}

	return out, nil
}

// collectingNow 报告当前有没有在收集。
func (a *App) collectingNow() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.collecting
}

// headlineFor 拼顶部那句状态话。
//
// 收集过之后，顶部先讲这一轮的结果（新增几份），再讲配置状态——人点完按钮
// 第一眼要看的就是「收了几个」，而不是「配置就绪」（I-014 验收标准 1）。
// 重试那一轮用 `重试 <渠道>` 开头，免得让人以为收的是全部渠道。
func headlineFor(cfg config.Config, run *UIRun) string {
	// 历史行不占顶部：它回答「上次是什么样」，不是刚跑完的结论（I-061）。
	if run != nil && !run.FromHistory {
		who := strings.TrimSpace(run.Scope)
		prefix := "收集完成："
		if who != "" {
			// 单点渠道的那一轮：走完上面两个特例之后，正文只有一个「勾 + 用时」，
			// 所以这里不用前缀（原来是「重试 <渠道名>：…」，人提的：去掉）。
			prefix = ""
		}
		if run.Err != "" {
			// 原话翻成「哪儿不对 + 下一步点哪里」（I-034）；这里没翻过就先翻一下。
			hint := run.ErrHint
			if hint == "" {
				hint = friendlyCollectError(run.Err, "")
			}
			if who != "" {
				// 「重试 X 没能完成」中间留一个空格，别粘成「X没能完成」（I-052）。
				return "重试 " + who + " 没能完成。" + hint
			}
			return "收集没能完成。" + hint
		}

		if who != "" {
			// 单点一个渠道：结果**不上顶部**——那段话该跟在那一行的勾后面
			//（人提的：「✓ 11.024s」别放在导航栏上面）。用时挂在渠道那一行上，见 channelStates。
			return ""
		}

		line := prefix + fmt.Sprintf("没有新增（用时 %s）", run.Duration)
		if run.New > 0 {
			line = prefix + fmt.Sprintf("新增 %d 段，收下 %d 段（用时 %s）",
				run.New, run.Archived, run.Duration)
		}
		if run.Empty > 0 {
			// 空会话（打开过没说话）不是失败，但要说一句：不然人会以为是漏收了（I-039）。
			line += fmt.Sprintf("，跳过 %d 个空会话", run.Empty)
		}
		if run.Failures > 0 {
			line += fmt.Sprintf("，失败 %d 处", run.Failures)
		}
		return line
	}

	if err := cfg.Validate(); err != nil {
		return "配置还没配好：" + err.Error()
	}
	// 一切正常就**不说**：这行留给「有结果」和「出问题」两种时候（人提的：别拿小字占地方）。
	return ""
}

// channelStates 把检测、解析与归档结果整理成界面要的那几个字段。
//
// 一行 = 检测结果 + 最近一轮这个渠道跑出来的数字。两层信息合成一句话，
// 文案只在 runStateText 里（I-015 验收标准 5）。
func (a *App) channelStates(cfg config.Config, progress func(app.Progress)) ([]UIChannel, error) {
	census, err := app.Census(a.deps, progress)
	if err != nil {
		return nil, err
	}
	reports := census.Reports

	// 待收一次数完所有渠道：打开一份游标就够了，旧游标补指纹也只会发生一次。
	pending, err := app.PendingByChannel(a.deps, reports)
	if err != nil {
		return nil, err
	}

	a.mu.Lock()
	runs := a.lastChannels
	lastRun := a.lastRun
	a.mu.Unlock()

	now := time.Now()
	out := make([]UIChannel, 0, len(reports))
	for _, report := range reports {
		row := UIChannel{
			ID:          report.ID,
			Name:        report.Name,
			Found:       len(report.Files),
			Valid:       census.Valid[report.ID],
			Empty:       census.Empty[report.ID],
			ParseFailed: census.Failed[report.ID],
			Enabled:     app.Enabled(cfg.Channels, report.ID),
		}
		switch report.Status {
		case channel.StatusFailed:
			row.Group = channelGroupFailed
		case channel.StatusNotInstalled:
			row.Group = channelGroupNotInstalled
		default:
			if row.Found > 0 {
				row.Group = channelGroupHasSessions
			} else {
				row.Group = channelGroupInstalledNoSessions
			}
		}
		run, ran := runs[report.ID]

		// 已收集：数这个渠道在归档目录里的那一层。布局归 archive 管，这里不自己拼目录名。
		stats, err := archive.Count(archive.ChannelDir(cfg.ArchiveDir, report.ID, report.Name), now)
		if err != nil {
			return nil, err
		}
		row.Collected = stats.Total
		row.CollectedToday = stats.Today
		// 待收：没归档过的新会话与已收集会话的新内容分开数。空会话处理过一次
		// 就躺在游标里了，不再算进来（I-039）；只有修改时间被动过、内容没变的
		// 文件也不该算（I-062）。
		row.PendingNew = pending[report.ID].New
		row.PendingUpdated = pending[report.ID].Updated

		// 今日新增（本机层）：源文件里落盘日期是今天的那些。用的是同一份检测结果。
		for _, file := range report.Files {
			if sameLocalDay(file.ModTime, now) {
				row.HostToday++
			}
		}

		switch {
		case !row.Enabled:
			row.State, row.Detail = "none", "已停用"
		case report.Status == channel.StatusNotInstalled:
			row.State, row.Detail = "none", "未检测到"
			// 没装的东西没有「重试」可言——重试也不会突然装上。
		case report.Status == channel.StatusFailed:
			row.State, row.Detail = "bad", "检测失败："+oneLine(report.Err)
			row.Action = channelActionCollect
		default:
			// 检测得到、也启用着：能单独收它。
			if ran {
				row.New, row.Archived, row.Failed = run.New, run.Archived, run.Failed
			}
			// 单点这一行跑的那一轮：用时挂在这一行上（顶部不说了，人提的）。
			if lastRun != nil && strings.TrimSpace(lastRun.Scope) != "" && lastRun.Scope == row.Name {
				row.LastDuration = lastRun.Duration
			}
			row.State, row.Detail = runStateText(row, ran)
			// 本机统计时读不出来的文件单独算失败；但其余有效会话仍然要显示，
			// 不能因为一个坏文件把整个渠道的「有效」抹成 0（I-051）。
			if row.ParseFailed > 0 {
				row.State = "bad"
			}
			// 绿勾只说「这个渠道的会话都收完了」。本机一个会话都没有时没什么可收，
			// 不给勾——那会像已经处理过（I-066）。有会话的才有箭头 / 绿勾。
			if row.Group == channelGroupHasSessions {
				row.Action = channelActionDone
				if row.PendingNew > 0 || row.PendingUpdated > 0 || row.Failed > 0 {
					row.Action = channelActionCollect
				}
			}
		}
		out = append(out, row)
	}
	return out, nil
}

// runStateText 写一行「发现多少 · 收了多少 · 有没有失败」。
//
// 状态文案只在这一个函数里——引导、主界面、渠道列表要显示同一件事都走它，
// 换说法时不会漏掉某处（I-015 验收标准 5）。
func runStateText(row UIChannel, ran bool) (state, detail string) {
	// 有效 = 本机可解析且至少有一条消息的会话；已收集 = 归档目录里的份数（I-027 定下的用词，
	// 与「已收集」那一层同一个说法）。两个数分开说，避免未收集的渠道被误读成
	// 「本机没有有效对话」（I-051）。
	census := fmt.Sprintf("会话 %d · 有效 %d", row.Found, row.Valid)
	if row.Empty > 0 {
		census += fmt.Sprintf(" · 空 %d", row.Empty)
	}
	if row.Collected > 0 {
		census += fmt.Sprintf(" · 已收集 %d 段", row.Collected)
	}
	if row.ParseFailed > 0 {
		// 这一行的「解析失败 %d 个」说的是**上一轮新增里**没读成的，这里说的是
		// **本机全部源文件里**没读成的；同一个说法挂两个数会让人对不上账，
		// 所以本机统计这一侧改说「读不动」（I-051）。
		census += fmt.Sprintf(" · 读不动 %d 段", row.ParseFailed)
	}
	if !ran {
		// 还没跑过这一轮：只说检测到多少，不说「0 新增」——那会让人以为跑了但没收到东西。
		return "ok", census + pendingSuffix(row)
	}
	if row.Failed > 0 {
		return "bad", fmt.Sprintf("解析失败 %d 个 · %s", row.Failed, census) + pendingSuffix(row)
	}
	if row.New == 0 {
		return "ok", "没有新增 · " + census + pendingSuffix(row)
	}
	// 「收下」只在与「新增」不一样时出现（有解析失败或空会话时才会不一样）——
	// 同一个数不写两遍，这是 I-027 定下的用词：界面上不再拿「归档」当东西说。
	line := fmt.Sprintf("新增 %d 段", row.New)
	if row.Archived != row.New {
		line += fmt.Sprintf(" · 收下 %d 段", row.Archived)
	}
	return "ok", line + " · " + census + pendingSuffix(row)
}

// pendingSuffix 说还有多少活没干；没有就不说——别拿没用的数字占地方。
//
// 两类分开说：新会话（还没归档过）与已收集会话的新内容。不合成一个数，
// 否则又会让人拿它和「已收集」做减法（I-062）。
func pendingSuffix(row UIChannel) string {
	if phrase := pendingPhrase(row.PendingNew, row.PendingUpdated); phrase != "" {
		return " · " + phrase
	}
	return ""
}

// pendingPhrase 把两个待收数字写成一句；只列非零的那类。
func pendingPhrase(newSessions, updated int) string {
	parts := make([]string, 0, 2)
	if newSessions > 0 {
		parts = append(parts, fmt.Sprintf("%d 个新会话", newSessions))
	}
	if updated > 0 {
		parts = append(parts, fmt.Sprintf("%d 个已收集会话有更新", updated))
	}
	if len(parts) == 0 {
		return ""
	}
	return "待收 " + strings.Join(parts, " + ")
}

// sameLocalDay 报两个时刻在本地时区下是不是同一个自然日。
//
// 与 internal/archive 里数归档份数用的规则是同一条：按落盘日期取本地自然日。
func sameLocalDay(a, b time.Time) bool {
	ay, am, ad := a.In(time.Local).Date()
	by, bm, bd := b.In(time.Local).Date()
	return ay == by && am == bm && ad == bd
}

// channelStatsText 拼渠道那一段顶上那句话：三层数字一次说清。
func channelStatsText(rows []UIChannel) string {
	var host, valid, empty, failed, collected, pendingNew, pendingUpdated int
	for _, row := range rows {
		host += row.Found
		valid += row.Valid
		empty += row.Empty
		failed += row.ParseFailed
		collected += row.Collected
		pendingNew += row.PendingNew
		pendingUpdated += row.PendingUpdated
	}
	line := fmt.Sprintf("总会话 %d · 有效会话 %d · 已收集 %d 段", host, valid, collected)
	if phrase := pendingPhrase(pendingNew, pendingUpdated); phrase != "" {
		line += " · " + phrase
	} else {
		line += " · 待收 0"
	}
	if empty > 0 {
		line += fmt.Sprintf(" · 空会话 %d", empty)
	}
	if failed > 0 {
		line += fmt.Sprintf(" · 读不动 %d 段", failed)
	}
	return line
}

// backupModes 列出配置里认得的备份方式，名字取内置注册表。
//
// 摆放次序是**界面的事**，不跟注册表的字母序：注册表按 ID 排序是为了稳定，
// 而界面上该把最省事、最不碰网络的那一档放前面——「仅本地」在前，「Git 仓库」
// 在后（它要人先准备好仓库）；阶段 2 的同步目录夹在中间。
func (a *App) backupModes() []UIOption {
	registry := backup.NewRegistry()
	if err := backups.RegisterInto(registry); err != nil {
		a.logger.Warn("内置备份方式注册失败", "原因", err)
	}

	entries := registry.Entries()
	out := make([]UIOption, 0, len(entries))
	for _, entry := range entries {
		out = append(out, UIOption{
			Value:       entry.ID,
			Label:       entry.Name,
			NeedsRemote: entry.ID == config.BackupGit,
			Hint:        backupModeHints[entry.ID],
		})
	}
	sort.SliceStable(out, func(i, j int) bool {
		return backupModeOrder(out[i].Value) < backupModeOrder(out[j].Value)
	})
	return out
}

// backupModeOrder 是备份方式在界面上的摆放次序（越小越前）。
//
// 不认识的取值（将来新加的）排到最后，不去抢现有几项的位置。
func backupModeOrder(id string) int {
	switch id {
	case config.BackupNone:
		return 0
	case config.BackupSync:
		return 1
	case config.BackupGit:
		return 2
	default:
		return 99
	}
}

// 运行记录的两条口径（I-030）：留多久、日志页列几天。
//
// 留 90 天是 PRD F11 定的；列 14 天是界面上一次看得完的量——两周够看出
// 「定时任务是不是每天在跑」，再长就得翻很久（滚动条那一段也够用）。
const (
	runLogKeepDays = 90
	runLogListDays = 14
)

// 运行记录里的来源。
const (
	runSourceManual   = "手动"
	runSourceSchedule = "定时"
	runSourceRetry    = "重试"
)

// RunLog 读回运行记录、按天合计，给日志页用（I-030）。
//
// 每次打开这一页现读一遍：记录是别人（定时任务）写进去的，缓存一份反而会显示旧账。
func (a *App) RunLog() ([]UIDailyRun, error) {
	dir, err := config.BaseDir()
	if err != nil {
		return nil, err
	}
	now := time.Now()
	runs, err := store.ReadRuns(dir, runLogKeepDays, now)
	if err != nil {
		return nil, err
	}
	return runLogRows(store.DailyRuns(runs, now, runLogListDays), now), nil
}

// lastRunFromLog 从运行记录里读回最后一轮，供重启后的首页显示（I-061）。
//
// 只读。没有记录、读不动都返回 nil，让界面退回「本次启动还没收过」那条；
// 历史行没有存用时与错误原文，所以只还原时间与份数。
func (a *App) lastRunFromLog() *UIRun {
	dir, err := config.BaseDir()
	if err != nil {
		a.logger.Warn("找不到运行记录的位置", "原因", err)
		return nil
	}
	now := time.Now()
	runs, err := store.ReadRuns(dir, runLogKeepDays, now)
	if err != nil {
		a.logger.Warn("读运行记录失败", "原因", err)
		return nil
	}
	if len(runs) == 0 {
		return nil
	}

	last := runs[len(runs)-1]
	run := &UIRun{
		At:          historyRunTime(last.At, now),
		Found:       last.Found,
		New:         last.New,
		Archived:    last.Archived,
		Failures:    last.Failures,
		Empty:       last.Empty,
		Scope:       last.Scope,
		FromHistory: true,
	}
	if last.Failed {
		// 记录里只有「整轮没跑完」这个结果，没有原因；界面只说结果，不编原因。
		run.Err = "上一轮没跑完"
	}
	return run
}

// historyRunTime 把运行记录里的时刻写成首页那一句用的形式。
// 今年的不带年份；跨年才补上，免得「01-02」看不出是哪一年。
func historyRunTime(at, now time.Time) string {
	at = at.In(time.Local)
	if at.Year() == now.In(time.Local).Year() {
		return at.Format("01-02 15:04")
	}
	return at.Format("2006-01-02 15:04")
}

// runLogRows 把合计翻成界面要的话。文案只在这一处。
func runLogRows(daily []store.DailyRun, now time.Time) []UIDailyRun {
	rows := make([]UIDailyRun, 0, len(daily))
	for i, one := range daily {
		row := UIDailyRun{
			Day:              one.Day,
			Archived:         one.Archived,
			Runs:             one.Runs,
			Failures:         one.Failures,
			Source:           strings.Join(one.Sources, "、"),
			Backups:          one.Backups,
			BackupFailures:   one.BackupFailures,
			BackupNote:       one.BackupNote,
			BackupNoteFailed: one.BackupNoteFailed,
		}
		switch i {
		case 0:
			row.Label = "今天"
		case 1:
			row.Label = "昨天"
		default:
			row.Label = dayLabel(one.Day)
		}
		rows = append(rows, row)
	}
	return rows
}

// dayLabel 把 2026-10-16 写成 10-16 周五。
func dayLabel(day string) string {
	at, err := time.ParseInLocation("2006-01-02", day, time.Local)
	if err != nil {
		return day
	}
	weekdays := [...]string{"周日", "周一", "周二", "周三", "周四", "周五", "周六"}
	if day == "" {
		return day
	}
	short := day
	if len(day) > 5 {
		short = day[5:]
	}
	return short + " " + weekdays[at.Weekday()]
}

// recordRun 记一笔运行记录（I-030）。写不成只记一条日志——
//
// 记录是给人看的账，不该因为它没写成而让收集本身失败（验收标准 4）。
// 命令行那一路（定时任务）与界面共用这一份。
func recordRun(logger *slog.Logger, run UIRun, source string) {
	dir, err := config.BaseDir()
	if err != nil {
		logger.Warn("运行记录没能写成", "原因", err)
		return
	}
	// 备份那一步的话也记上：定时那一路到底有没有把改动推上去，就看它（I-030）。
	// 失败时记可读原因，不记空串——首页不再显示备份，日志页要能说清（I-053）。
	// 「仅本地」不是一次备份：不记次数、不写说明，日志页才不会说「备份成功」。
	backup := run.BackupDetail
	if run.BackupMethod == none.ID {
		backup = ""
	}
	if run.BackupErr != "" {
		backup = friendlyBackupError(run.BackupErr)
	}
	rec := store.Run{
		At:           time.Now(),
		Source:       source,
		Scope:        run.Scope,
		Found:        run.Found,
		New:          run.New,
		Archived:     run.Archived,
		Failures:     run.Failures,
		Empty:        run.Empty,
		Failed:       run.Err != "",
		Backup:       backup,
		BackupFailed: run.BackupErr != "",
	}
	if err := store.AppendRun(dir, rec, runLogKeepDays, time.Now()); err != nil {
		logger.Warn("运行记录没能写成", "原因", err)
	}
}

// backupModeHints 是每种备份方式选中之后跟的一句话；没写的就不显示。
//
// 文案只在这一处（与 progressText、runStateText 同一条规矩）。
var backupModeHints = map[string]string{
	config.BackupNone: "归档只留在本机。想更保险：把归档目录放进网盘同步目录（OneDrive、百度网盘、夸克网盘等），网盘客户端会替你传上去。",
	config.BackupGit:  "推到你自己账号下的一个仓库里，地址填在下面。",
}

// SaveWizard 记下引导里一步的结果，返回刷新后的状态。
//
// 只写传进来的字段：ArchiveDir 为空就不动归档目录，BackupMode 为空就不动备份方式。
// 这样「中途关掉、下次接着走」只要记一个步号，不必把整份配置再抄一遍。
func (a *App) SaveWizard(input WizardInput) (UIState, error) {
	cfg, err := config.Load()
	if err != nil {
		return UIState{}, err
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

	// 引导里也不再问地址（人提的：有「试连」就够了）：现认一次本地仓库里配好的远程。
	if cfg.Backup.Mode == config.BackupGit && strings.TrimSpace(cfg.Backup.Git.Remote) == "" {
		if remote := a.detectedRemote(); remote != "" {
			cfg.Backup.Git.Remote = remote
		}
	}

	// 备份选了「Git 仓库」却没认出地址，不算错——PRD F5 允许先跳过；
	// 但归档目录必须能定下来，不然收上来的东西没地方放。
	if strings.TrimSpace(cfg.ArchiveDir) == "" {
		return UIState{}, errors.New("归档目录不能为空")
	}
	if err := cfg.Validate(); err != nil {
		return UIState{}, err
	}

	cfg.FirstRunStep = clampStep(input.Step)
	if err := config.Save(cfg); err != nil {
		return UIState{}, err
	}
	// 引导里**不给**「旧数据怎么办」的选项（第一次用本来就没有旧数据，人提的）。
	// 但要是重走引导时换了目录，得把「旧目录里还剩多少」记下来——之后在设置页里
	// 再决定要不要复制过来。
	a.afterArchiveChange(oldArchive, cfg.ArchiveDir, false)

	a.logger.Info("引导已保存", "步", cfg.FirstRunStep, "归档目录", cfg.ArchiveDir, "备份方式", cfg.Backup.Mode)
	return a.State()
}

// FinishWizard 收尾：标成走完了，下次打开不再出现引导。
func (a *App) FinishWizard() (UIState, error) {
	cfg, err := config.Load()
	if err != nil {
		return UIState{}, err
	}
	if err := cfg.Validate(); err != nil {
		return UIState{}, err
	}
	cfg.FirstRunDone = true
	cfg.FirstRunStep = 0
	if err := config.Save(cfg); err != nil {
		return UIState{}, err
	}
	a.logger.Info("首次运行引导走完了", "归档目录", cfg.ArchiveDir, "备份方式", cfg.Backup.Mode)
	return a.State()
}

// progressEvent 是进度事件的通道名；前端按同一个名字订阅（app.js 里有同名常量）。
const progressEvent = "collect:progress"

// collectRequest 是一轮收集的范围。
type collectRequest struct {
	// only 为空表示收全部渠道；非空表示只跑这些渠道（界面上的「重试」）。
	only []string
	// force 为真时不管游标，把 only 里那些渠道的会话文件重跑一遍。
	force bool
	// label 是这一轮在界面上怎么称呼（重试时是渠道名，收全部时为空）。
	label string
}

// CollectNow 收一轮（全部渠道），返回收完之后的状态。
//
// 这里**阻塞到收完为止**：Wails 把一次绑定调用放在它自己的 goroutine 上跑，
// 窗口该拖还是能拖、该关还是能关，界面靠事件推的进度在动（I-014 验收标准 4）。
// 进度走事件、不走轮询——轮询会把「一边收集一边不停问状态」的负担加回来。
//
// 正在收的时候再点，直接拒绝：不叠第二轮（I-014 验收标准 2）。界面那边也会把按钮
// 置灰，这里是兜底——按钮的置灰状态可能因为一次刷新慢半拍。
func (a *App) CollectNow() (UIState, error) {
	return a.collect(collectRequest{})
}

// RetryChannel 只重跑一个渠道（界面上的「重试」）。
//
// 「只重跑它」这件事在编排层做（`Deps.Only`），界面不自己把六步再串一遍。
// 重试**不看游标**：上一轮失败的那个文件已经记进游标了（归档路径留空），
// 不强制重跑的话，「重试」永远什么都不做（I-015 验收标准 3）。
func (a *App) RetryChannel(id string) (UIState, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return UIState{}, errors.New("没说要重试哪个渠道")
	}

	cfg, err := config.Load()
	if err != nil {
		return UIState{}, err
	}
	if err := cfg.Validate(); err != nil {
		return UIState{}, err
	}

	reports, err := app.Detect(a.deps)
	if err != nil {
		return UIState{}, err
	}
	name := ""
	for _, report := range reports {
		if report.ID == id {
			name = report.Name
			break
		}
	}
	if name == "" {
		return UIState{}, fmt.Errorf("不认识的渠道 %q", id)
	}
	if !app.Enabled(cfg.Channels, id) {
		return UIState{}, fmt.Errorf("渠道 %s 已停用，先启用再重试", name)
	}

	return a.collect(collectRequest{only: []string{id}, force: true, label: name})
}

// collect 收一轮，req 决定范围。
func (a *App) collect(req collectRequest) (UIState, error) {
	if !a.beginCollect() {
		a.logger.Warn("收集还没结束，忽略这次点击")
		state, err := a.State()
		if err != nil {
			return UIState{}, err
		}
		return state, errors.New("正在收集中，等这一轮结束再点")
	}

	cfg, err := config.Load()
	if err == nil {
		err = cfg.Validate()
	}
	if err != nil {
		// 配置都读不出来，这一轮根本没跑起来。先把「正在收集」的位子放掉——
		// 留着它，界面会把按钮一直置灰，而这一轮其实已经结束了。
		a.finishCollect()
		state, stateErr := a.State()
		if stateErr != nil {
			return UIState{}, err
		}
		return state, err
	}

	if req.label == "" {
		a.logger.Info("界面点了立即收集",
			"归档目录", cfg.ArchiveDir, "备份方式", cfg.Backup.Mode, "渠道数", len(cfg.Channels))
	} else {
		a.logger.Info("界面点了重试", "渠道", req.label, "归档目录", cfg.ArchiveDir)
	}

	deps := a.deps
	deps.Progress = a.reportProgress
	deps.Only = req.only
	deps.Force = req.force
	result, collectErr := app.Collect(cfg, a.logger, deps)

	// 先放掉「正在收集」，再整理状态：反过来的话，交出去的 State 会说
	// 「还在收集」，界面就会一直把按钮置灰，而这一轮其实已经结束了。
	a.mu.Lock()
	if a.lastChannels == nil {
		a.lastChannels = map[string]app.ChannelResult{}
	}
	for _, cr := range result.Channels {
		// 重试时只有被重试的那个渠道会出现在 result 里，别的行照旧——
		// 「其他行状态不变」就是这么来的（I-015 验收标准 3）。
		a.lastChannels[cr.ID] = cr
	}
	run := runFrom(result)
	run.Scope = req.label
	if collectErr != nil {
		run.Err = oneLine(collectErr)
	}
	a.lastRun = &run
	a.collecting = false
	a.mu.Unlock()

	// 记一笔运行记录（I-030）：手动点的、还是重试某一个渠道，行里要标出来。
	source := runSourceManual
	if req.label != "" {
		source = runSourceRetry
	}
	recordRun(a.logger, run, source)

	state, stateErr := a.State()
	if collectErr != nil {
		return state, collectErr
	}
	return state, stateErr
}

// reportProgress 把编排层报的一步推给界面。
func (a *App) reportProgress(p app.Progress) {
	a.emit(progressEvent, progressToUI(p))
}

// progressToUI 把编排层的进度翻成界面要的那几个字段（纯函数，好盯着）。
//
// 关键是 Percent：那一行要能看到 0% 走到 100%（人提的：不然以为卡住了）。
func progressToUI(p app.Progress) UIProgress {
	out := UIProgress{
		Text:    progressText(p),
		Channel: p.ChannelID,
		Done:    p.Stage == app.StageDone,
		Percent: -1,
		Stage:   string(p.Stage),
	}
	if p.Stage == app.StageChannel && p.ChannelID != "" {
		switch {
		case p.ChannelDone:
			out.Percent, out.Finished = 100, true
		case p.FileTotal > 0:
			out.Percent = p.FileDone * 100 / p.FileTotal
			if out.Percent > 100 {
				out.Percent = 100
			}
		default:
			// 一个文件都没有：从 0 起步，别让那一行看着像卡住。
			out.Percent = 0
		}
	}
	return out
}

// progressText 把编排层报的一步翻成一句给界面看的话。
//
// 进度文案只在这一个函数里——引导、主界面、将来的抽屉要显示进度都走它，
// 换说法时不会漏掉某处（I-015 验收标准 5）。
func progressText(p app.Progress) string {
	switch p.Stage {
	case app.StageDetect:
		return "正在检测渠道…"
	case app.StageChannel:
		if p.Total > 0 {
			return fmt.Sprintf("正在收集 %s（%d/%d）", p.ChannelName, p.Index, p.Total)
		}
		return "正在收集 " + p.ChannelName
	case app.StageBackup:
		return "正在备份…"
	case app.StageDone:
		return "收集完成"
	default:
		return "正在收集…"
	}
}

// runFrom 把一轮收集的运行记录整理成界面要的形状。
func runFrom(result app.Result) UIRun {
	run := UIRun{
		At:       result.StartedAt.Format("15:04:05"),
		Found:    result.Found(),
		New:      result.New(),
		Archived: result.Archived(),
		Failures: result.Failures(),
		Empty:    result.Empty(),
		Duration: shortDuration(result.Duration),
		// 详情留着：界面眼下不显示备份，但它仍是这一轮结果的记录（I-053）。
		BackupDetail: result.Backup.Detail,
		BackupMethod: result.Backup.Method,
	}
	if result.Backup.Err != nil {
		run.BackupErr = oneLine(result.Backup.Err)
	}
	return run
}

// clampStep 把步号收在合理范围内——配置是文本文件，人手改坏了也不该把界面弄崩。
func clampStep(step int) int {
	if step < 0 {
		return 0
	}
	if step > firstRunSteps-1 {
		return firstRunSteps - 1
	}
	return step
}

// oneLine 把多行错误压成一行，免得把布局撑破。
func oneLine(err error) string {
	if err == nil {
		return "没有说明"
	}
	return strings.ReplaceAll(err.Error(), "\n", " ")
}
