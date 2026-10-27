// Command agentmemory 是 AgentMemory 的入口。
//
// 两种模式（见 docs/ARCHITECTURE.md 第 2 节）：
//   - 不带参数：打开图形界面
//   - --collect：静默收集一次后退出，供系统定时任务调用
//   - --mcp：以 MCP 服务（stdio）跑起来，供 dsh / Claude Code 等外部 Agent 调用
//
// 静默模式的三条自我约束：
//   - 不弹窗、不读标准输入（定时任务里没人能回答它）；
//   - 输出走标准输出，日志走标准错误与日志文件，两不打扰；
//   - 未实现的功能如实报告并返回非 0——不假装成功。
//
// Wails 工程与入口同层（wails.json / main.go / frontend/ 并排），
// 所以图形界面从本目录起：`wails dev` 与 `wails build` 都在这里跑。
package main

import (
	"embed"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/Always1023/AgentMemory/internal/app"
	"github.com/Always1023/AgentMemory/internal/channel"
	"github.com/Always1023/AgentMemory/internal/config"
	"github.com/Always1023/AgentMemory/internal/logging"
	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
)

//go:embed all:frontend/dist
var assets embed.FS

// 窗口的默认尺寸与底色。这些是窗口层的东西，CSS 管不到，所以留在这一层；
// 尺寸刻度本身（间距、圆角、字号）在 frontend/dist/tokens.css 里。
const (
	windowTitle     = "AgentMemory"
	windowWidth     = 960
	windowHeight    = 720
	windowMinWidth  = 560
	windowMinHeight = 420

	// 窗口底色兜底（前端第一帧画出来之前用它），取暗色页面底 = 8 9 10。
	windowBgR, windowBgG, windowBgB uint8 = 8, 9, 10
)

// 退出码。
//
// 前三个是「这一轮收集的结论」，定时任务靠它们判断要不要提醒人；
// 后两个是「这一轮根本没跑起来」。分开是因为处理方式不同：
// 前者去看归档目录，后者去看配置与环境。
const (
	exitOK      = 0 // 成功
	exitPartial = 1 // 部分成功：有产出，也有失败
	exitFailed  = 2 // 失败：什么都没产出，或有系统错
	exitUsage   = 3 // 参数、配置或环境不对
	exitNotImpl = 4 // 功能还没实现
)

// cliOptions 是命令行选项。
type cliOptions struct {
	collect bool
	mcp     bool
}

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

// run 跑一次入口，返回退出码。
//
// out 与 errOut 当参数传进来，是为了让测试能直接拿到输出，
// 不用去截进程的管道、也不用真起一个子进程。
func run(args []string, out, errOut io.Writer) int {
	opts, err := parseArgs(args, errOut)
	if errors.Is(err, flag.ErrHelp) {
		return exitOK // 人主动要看帮助，不是出错
	}
	if err != nil {
		fmt.Fprintf(errOut, "AgentMemory: %v\n", err)
		return exitUsage
	}

	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(errOut, "AgentMemory: %v\n", err)
		return exitUsage
	}

	logger, closer, err := logging.Setup(errOut, slog.LevelInfo)
	if err != nil {
		fmt.Fprintf(errOut, "AgentMemory: 初始化日志失败: %v\n", err)
		return exitUsage
	}
	defer func() { _ = closer.Close() }()

	logger.Info("AgentMemory 启动",
		"config_version", cfg.Version,
		"archive_dir", cfg.ArchiveDir,
		"backup_mode", cfg.Backup.Mode,
	)

	// 配置不合法是「这一轮根本没跑起来」，不是「收集失败」——两者要分开，
	// 人处理方式不同：一个去改配置，一个去看归档目录。
	if err := cfg.Validate(); err != nil {
		logger.Error("配置不合法", "原因", err)
		fmt.Fprintf(errOut, "AgentMemory: 配置不合法: %v\n", err)
		return exitUsage
	}

	if opts.mcp {
		return runMCP(cfg, logger)
	}
	if !opts.collect {
		return runGUI(logger)
	}

	result, err := app.Collect(cfg, logger, app.Deps{})
	if err != nil {
		// 系统错：整轮中断。归档目录可能还是好的，但这一轮没跑完。
		logger.Error("收集没能完成", "原因", err)
		// 也要记一笔：那天确实跑了，只是没跑完（I-030）。
		recordRun(logger, UIRun{Err: oneLine(err)}, runSourceSchedule)
		fmt.Fprintf(errOut, "AgentMemory: 收集没能完成: %v\n", err)
		return exitFailed
	}

	// 定时任务这一路的账：哪天跑了、收了多少（I-030）。
	recordRun(logger, runFrom(result), runSourceSchedule)
	printSummary(out, result)
	return exitCodeFor(result.Outcome())
}

// runGUI 打开窗口，窗口关掉才返回。
//
// 窗口自己是一个事件循环，所以这里是阻塞的；退出码只有「起没起来」两种。
func runGUI(logger *slog.Logger) int {
	bound := NewApp(logger)

	err := wails.Run(&options.App{
		Title:     windowTitle,
		Width:     windowWidth,
		Height:    windowHeight,
		MinWidth:  windowMinWidth,
		MinHeight: windowMinHeight,
		// 前端第一帧之前的底色：不设的话会先闪一下白，暗色主题下很扎眼。
		BackgroundColour: &options.RGBA{R: windowBgR, G: windowBgG, B: windowBgB, A: 1},
		AssetServer:      &assetserver.Options{Assets: assets},
		OnStartup:        bound.startup,
		Bind:             []any{bound},
	})
	if err != nil {
		logger.Error("窗口没能起来", "原因", err)
		fmt.Fprintf(os.Stderr, "AgentMemory: 窗口没能起来: %v\n", err)
		return exitFailed
	}
	return exitOK
}

// parseArgs 解析命令行。
func parseArgs(args []string, errOut io.Writer) (cliOptions, error) {
	fs := flag.NewFlagSet("agentmemory", flag.ContinueOnError)
	fs.SetOutput(errOut)
	fs.Usage = func() {
		fmt.Fprintf(errOut, "用法：agentmemory [-collect]\n\n")
		fmt.Fprintf(errOut, "  -collect  静默收集一次后退出；不弹窗、不需要输入\n")
		fmt.Fprintf(errOut, "  -mcp      以 MCP 服务（stdio）跑起来，供 dsh / Claude Code 等外部 Agent 调用\n")
		fmt.Fprintf(errOut, "  不带参数  打开图形界面\n")
	}

	var opts cliOptions
	fs.BoolVar(&opts.collect, "collect", false, "静默收集一次后退出，不弹窗、不需要输入")
	fs.BoolVar(&opts.mcp, "mcp", false, "以 MCP 服务（stdio）跑起来，供外部 Agent 调用（设置里要先打开「对外接口」）")
	if err := fs.Parse(args); err != nil {
		return cliOptions{}, err
	}
	if fs.NArg() > 0 {
		return cliOptions{}, fmt.Errorf("不认识的参数：%s", strings.Join(fs.Args(), " "))
	}
	return opts, nil
}

// exitCodeFor 把一轮收集的结论翻成退出码。
func exitCodeFor(outcome app.Outcome) int {
	switch outcome {
	case app.OutcomeSuccess:
		return exitOK
	case app.OutcomePartial:
		return exitPartial
	default:
		return exitFailed
	}
}

// printSummary 把运行记录写成几行人能读的话。
//
// 输出给人看，也给定时任务的日志看，所以「没有新增」要明说——
// 留一行空表让人猜「是没跑起来还是真没新东西」，是最糟的写法。
func printSummary(w io.Writer, r app.Result) {
	if r.New() == 0 {
		fmt.Fprintf(w, "AgentMemory 收集完成：没有新增（用时 %s）\n", shortDuration(r.Duration))
	} else {
		fmt.Fprintf(w, "AgentMemory 收集完成：新增 %d 段，收下 %d 段，失败 %d 处（用时 %s）\n",
			r.New(), r.Archived(), r.Failures(), shortDuration(r.Duration))
	}

	for _, c := range r.Channels {
		fmt.Fprintf(w, "  %s　%s\n", c.Name, channelLine(c))
	}

	for _, c := range r.Channels {
		for _, f := range c.Failures {
			// 只印原因：每一句失败原因里本来就带着源文件路径
			// （解析失败由适配器带，归档失败由编排那层带）。
			// 再拼一次路径会变成「路径：路径 里没有可用的消息」。
			fmt.Fprintf(w, "  失败：%v\n", f.Err)
		}
	}

	fmt.Fprintf(w, "备份：%s\n", backupLine(r.Backup))
}

// channelLine 写一个渠道这一行。
func channelLine(c app.ChannelResult) string {
	switch {
	case !c.Enabled:
		return "已停用"
	case c.Status == channel.StatusNotInstalled:
		return "未检测到"
	case c.Status == channel.StatusFailed:
		return "检测失败：" + errText(c.Err)
	case c.Err != nil:
		return "不能用：" + errText(c.Err)
	}

	line := fmt.Sprintf("发现 %d 段 · 新增 %d 段 · 收下 %d 段", c.Found, c.New, c.Archived)
	if c.Failed > 0 {
		line += fmt.Sprintf(" · 失败 %d", c.Failed)
	}
	return line
}

// backupLine 写备份那一行。
func backupLine(b app.BackupResult) string {
	switch {
	case b.Method == "":
		return "未配置"
	case b.Err != nil:
		return fmt.Sprintf("%s · 没做成：%v", b.Method, b.Err)
	case b.Detail == "":
		return b.Method
	default:
		return fmt.Sprintf("%s · %s", b.Method, b.Detail)
	}
}

func errText(err error) string {
	if err == nil {
		return "没有说明"
	}
	return strings.ReplaceAll(err.Error(), "\n", " ")
}

// shortDuration 把耗时写成毫秒级，避免输出里出现一长串纳秒。
func shortDuration(d time.Duration) string {
	return d.Round(time.Millisecond).String()
}
