package main

import (
	"context"
	"log/slog"
	"strings"
	"sync"

	"github.com/Always1023/AgentMemory/internal/app"
	wruntime "github.com/wailsapp/wails/v2/pkg/runtime"
)

// App 是暴露给窗口的那一层。
//
// 只有「界面需要、编排层不提供」的东西才放这儿：读一份状态快照、把界面动作转成
// 对 config 与 app 的调用、把进度与前端判到的主题记进日志。真正的业务动作一律调
// internal/app，这里不做第二套实现。
//
// 状态整理与动作见 ui.go；渠道列表见 ui.go；这里只管「这一层自己记着什么」。
type App struct {
	ctx    context.Context
	logger *slog.Logger

	// deps 是渠道与备份这两个口子；留空就用内置的。
	// 只有用例会往里塞东西——生产路径一律走内置，不给外部留后门。
	deps app.Deps

	// emit 把一条事件推给窗口。默认走 Wails 的事件出口；用例里换成记录器——
	// Wails 的事件出口要有运行环境（窗口起来之后才有），测试里没有。
	emit func(name string, data ...any)

	// schedule 是调系统定时任务的出口（schedule.go）。默认跑真的 schtasks；
	// 用例里换成假的，免得为了跑一条用例在这台机器上留下一个每天自动跑的任务。
	schedule scheduleRunner

	// gitRunner 是跑 git 命令的出口（gitguide.go）。默认跑真的 git；
	// 用例里换成假的，不去碰文件系统与远端。
	gitRunner gitRunner

	// mu 护着下面这几个字段：收集跑在自己的 goroutine 上，界面随时可能来读状态
	// （State 与 CollectNow 会同时被打到）。
	mu      sync.Mutex
	lastRun *UIRun
	// lastChannels 是每个渠道最近一轮跑出来的数字。
	// 与 lastRun 分开：重试只更新其中一行，别的行要保住上一轮的样子（I-015 验收标准 3）。
	lastChannels map[string]app.ChannelResult
	collecting   bool
	theme        string
	// archiveNote 是「归档目录换过了」那句提示。
	archiveNote string
	// archiveOld 是上一个归档目录（I-024：复制已有归档时要它）。
	archiveOld string
	// migrating 报告当前有没有在搬归档（与收集互斥）。
	migrating bool
}

// NewApp 建一个 App。
func NewApp(logger *slog.Logger) *App {
	if logger == nil {
		logger = slog.Default()
	}
	a := &App{logger: logger}
	a.emit = a.emitViaWails
	a.schedule = runSchtasks
	a.gitRunner = runGit
	return a
}

// startup 由 Wails 在窗口起来时调用。
func (a *App) startup(ctx context.Context) {
	a.ctx = ctx
	a.logger.Info("窗口已打开")
}

// emitViaWails 把一条事件推给窗口。
//
// ctx 为空（窗口还没起来，或用例里根本没建窗口）时直接丢掉：没有事件出口时，
// 收集本身照常跑完，只是看不到进度。
func (a *App) emitViaWails(name string, data ...any) {
	if a.ctx == nil {
		return
	}
	wruntime.EventsEmit(a.ctx, name, data...)
}

// beginCollect 占住「正在收集」这个位子；已经有人占着就返回 false。
func (a *App) beginCollect() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.collecting {
		return false
	}
	a.collecting = true
	return true
}

// finishCollect 放开「正在收集」这个位子。
func (a *App) finishCollect() {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.collecting = false
}

// lastRunSnapshot 返回上一次收集的结果副本（界面只读它）。
func (a *App) lastRunSnapshot() *UIRun {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.lastRun == nil {
		return nil
	}
	out := *a.lastRun
	return &out
}

// ReportTheme 收下前端判到的明暗套，记一笔。
//
// 为什么要这条日志：窗口「没跟着系统变」有两种可能——前端没判对，或窗口本身
// 没收到系统主题。有了这一行，一眼能分清是哪种；以后要加「手动指定主题」时，
// 这里也是现成的落点。
func (a *App) ReportTheme(theme string) string {
	theme = strings.TrimSpace(theme)
	if theme == "" {
		theme = "unknown"
	}
	a.mu.Lock()
	a.theme = theme
	a.mu.Unlock()
	a.logger.Info("窗口主题", "主题", theme)
	return theme
}

// ReportView 收下前端报告「我这次渲染了哪个视图、事件出口通不通」，记一笔。
//
// 为什么要这条日志：窗口里出问题时分两种——**前端压根没跑起来**（渲染日志就没有）
// 与**渲染了但内容不对**（有日志、视图名不对）。没有这一行，这两种在日志里看起来
// 一模一样，只能靠人盯着窗口猜。
//
// events 说的是「进度那个事件出口拿到了没有」：拿不到的话收集照收，只是进度永远
// 不动、界面安安静静——这类「安静地不工作」只有日志能兜住。
func (a *App) ReportView(view string, events bool) string {
	view = strings.TrimSpace(view)
	if view == "" {
		view = "unknown"
	}
	eventText := "没有"
	if events {
		eventText = "有"
	}
	a.logger.Info("界面渲染", "视图", view, "事件出口", eventText)
	return view
}

// ReportFrontendError 收下前端没接住的异常，记一行日志（I-052）。
//
// 前端出错时窗口可能只是「不动了」；没有这一行，日志里看不出是前端没跑对、
// 还是后端没回来。只记一行文本，不记消息正文与完整堆栈。
func (a *App) ReportFrontendError(message string) string {
	message = oneLineText(message)
	if message == "" {
		message = "unknown"
	}
	if len(message) > 500 {
		message = message[:500]
	}
	a.logger.Error("前端未捕获错误", "内容", message)
	return message
}
