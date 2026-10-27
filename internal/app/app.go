// Package app 是收集这条链路的唯一用例入口。
//
// 六步：① 检测 → ② 增量筛 → ③ 解析 → ④ 渲染 → ⑤ 归档 → ⑥ 备份。
// 界面与命令行都调这里，不各自把六步再串一遍。
//
// 错误分两类（AGENTS.md §11 项目代码规范）：
//   - **数据错**：某个源文件坏了。跳过这一条，记进结果，整轮继续。
//   - **系统错**：配置不可用、游标开不了、归档写不进去。整轮停下，
//     返回一句人能读懂的错误。
//
// 备份失败是第三类：它发生在一切都归档好之后，**不中断、不丢东西**，
// 只让整轮结论降级为「部分成功」（I-010 验收标准 4）。
//
// 依赖方向（docs/ARCHITECTURE.md 模块表）：`app` 允许依赖全部环节模块与 `config`，
// 但不直接读渠道文件、不直接调 git——那些都各自归 环节模块。
package app

import (
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"time"

	"github.com/Always1023/AgentMemory/internal/archive"
	"github.com/Always1023/AgentMemory/internal/backup"
	"github.com/Always1023/AgentMemory/internal/backups"
	"github.com/Always1023/AgentMemory/internal/channel"
	"github.com/Always1023/AgentMemory/internal/channels"
	"github.com/Always1023/AgentMemory/internal/config"
	"github.com/Always1023/AgentMemory/internal/derive"
	"github.com/Always1023/AgentMemory/internal/render"
	"github.com/Always1023/AgentMemory/internal/store"
)

// Outcome 是一轮收集的结论。
type Outcome string

const (
	// OutcomeSuccess 一切正常（包括「本来就没有新东西」）。
	OutcomeSuccess Outcome = "success"
	// OutcomePartial 有产出，但也有失败。
	OutcomePartial Outcome = "partial"
	// OutcomeFailed 什么都没产出，且有失败。
	OutcomeFailed Outcome = "failed"
)

// Failure 是一条失败记录。
type Failure struct {
	// Path 是出问题的源文件路径。
	Path string
	// Err 是原因。
	Err error
}

// ChannelResult 是一个渠道这一轮干了什么。
type ChannelResult struct {
	// ID 与 Name 是渠道身份。
	ID   string
	Name string
	// Status 是检测结果（可用 / 未安装 / 失败）。
	Status channel.Status
	// Enabled 为 false 表示配置里停用了这个渠道，这一轮没碰它。
	Enabled bool
	// Found 是检测到的源文件数。
	Found int
	// New 是本轮需要处理的数量（游标里没有，或文件变了）。
	New int
	// Archived 是成功归档的份数。
	Archived int
	// Failed 是失败条数。
	Failed int
	// Empty 是「空会话」条数：解析出来一条消息都没有的源文件（打开过没说话）。
	// 既不算失败、也不归档，但要在界面上跟失败分开（I-039）。
	Empty int
	// Failures 逐条失败，供界面与日志点名。
	Failures []Failure
	// Err 是**渠道级**的失败（检测失败），不是单条文件的。
	Err error
}

// BackupResult 是备份那一步的结果。
type BackupResult struct {
	// Method 是用的备份方式。
	Method string
	// Changed 报告备份有没有真的动东西。
	Changed bool
	// Detail 是给人看的一句话。
	Detail string
	// Err 非空表示备份这一步没成。
	Err error
}

// Result 是一轮收集的运行记录。
type Result struct {
	// StartedAt 与 Duration 是这一轮什么时候开始、跑了多久。
	StartedAt time.Time
	Duration  time.Duration
	// Channels 逐渠道的处理情况，按渠道 ID 排序。
	Channels []ChannelResult
	// Backup 是备份那一步的结果。
	Backup BackupResult
}

// Stage 是收集走到哪一步（六步里的哪一段）。
type Stage string

const (
	// StageDetect 正在检测各渠道的数据目录。
	StageDetect Stage = "detect"
	// StageChannel 正在处理某个渠道（增量筛 → 解析 → 渲染 → 归档）。
	StageChannel Stage = "channel"
	// StageBackup 正在备份归档目录。
	StageBackup Stage = "backup"
	// StageDone 这一轮跑完了。
	StageDone Stage = "done"
)

// Progress 是一条进度报告。
//
// 界面「立即收集」之后就地显示进度用（I-014）；命令行不装这个回调。
// 这里只说**走到哪一步了**，不写给人看的话——那是界面层的事（文案一处定义）。
type Progress struct {
	Stage Stage
	// ChannelID 与 ChannelName 只在 Stage 为 StageChannel 时有值。
	ChannelID   string
	ChannelName string
	// Index 与 Total 是「第几个渠道 / 共几个」，只在 StageChannel 时有值。
	Index int
	Total int
	// FileDone 与 FileTotal 是这个渠道里「处理到第几个文件 / 一共几个」，
	// 让界面那一行能显示 0% 到 100%（人提的：不然看着像卡住了）。
	FileDone  int
	FileTotal int
	// ChannelDone 为真表示这个渠道这一步走完了（界面先把它那一行画成勾）。
	ChannelDone bool
}

// report 报一条进度；没装回调就什么也不做。
func (d Deps) report(p Progress) {
	if d.Progress != nil {
		d.Progress(p)
	}
}

// Deps 是这一轮可以注入的依赖，留空就用内置的。
//
// 留这几个口子是为了测试能造出「文件坏了」「备份失败」「渠道未安装」
// 这些真实机器上不好复现的场景。
type Deps struct {
	// Channels 是渠道注册表；留空用内置渠道。
	Channels *channel.Registry
	// Backups 是备份注册表；留空用内置备份方式。
	Backups *backup.Registry
	// BaseDir 是配置与增量游标所在的目录；留空用 config.BaseDir()。
	BaseDir string
	// ArchiveDir 是归档目录：导入型渠道（channel.ArchiveRootSetter）的数据根
	// 在这里头。Collect 从配置填；Census 这类只读路径由调用方带上。
	ArchiveDir string
	// Only 非空时只处理这些渠道（界面上的「重试某个渠道」走这条）；留空就是全部。
	Only []string
	// Force 为真时不管游标怎么说，都把 Only 那些渠道的文件重跑一遍。
	//
	// 「重试」要的就是这个：失败的文件达到上限后不再自动重试，
	// 手动「重试」要能绕过游标立刻再试一次。
	Force bool
	// Progress 非空时，收集过程中一次次回调它，报「走到哪一步了」。
	//
	// 界面靠它显示进度（I-014）；命令行留空即可。检测与备份那几步各报一条，
	// 每个渠道开工前报一条（带「第几个 / 共几个」）。
	Progress func(Progress)
}

// Collect 跑一轮收集。
//
// 返回的错误只在**系统错**时非空；数据错与备份失败都记在 Result 里，
// 由调用方按 Outcome 决定退出码与提示（I-011）。
func Collect(cfg config.Config, logger *slog.Logger, deps Deps) (Result, error) {
	start := time.Now()
	result := Result{StartedAt: start}

	if logger == nil {
		logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}

	// 系统错之一：配置本身不可用——这一步不过，后面全白搭。
	if err := cfg.Validate(); err != nil {
		return result, fmt.Errorf("配置不可用：%w", err)
	}

	baseDir, err := resolveBaseDir(deps)
	if err != nil {
		return result, err
	}
	db, err := store.OpenDB(baseDir)
	if err != nil {
		return result, err
	}
	defer db.Close()
	cursor, err := db.OpenCursor()
	if err != nil {
		return result, fmt.Errorf("打开增量水位：%w", err)
	}
	if cursor.Rebuilt() {
		logger.Info("水位账是空的；本轮会重读一遍已有的会话文件")
	}
	if cursor.Forced() {
		logger.Info("换代回填：水位是旧版搬来的，这一轮不看它，全部源文件重读入库")
	}

	registry, adapters, err := resolveChannels(deps)
	if err != nil {
		return result, err
	}
	applyArchiveRoot(adapters, cfg.ArchiveDir)

	deps.report(Progress{Stage: StageDetect})
	// 只收指定的渠道（界面上的「重试」走这条）；留空就是全部。
	// 检测照旧做全量——界面要知道其他渠道现在什么样。
	reports := pickReports(registry.DetectAll(), deps.Only)

	for i, report := range reports {
		notify := func(done int, finished bool) {
			deps.report(Progress{
				Stage:       StageChannel,
				ChannelID:   report.ID,
				ChannelName: report.Name,
				Index:       i + 1,
				Total:       len(reports),
				FileDone:    done,
				FileTotal:   len(report.Files),
				ChannelDone: finished,
			})
		}
		notify(0, false)
		cr, err := collectOne(cfg, logger, db, cursor, report, adapters[report.ID], deps.Force, notify)
		result.Channels = append(result.Channels, cr)
		if err != nil {
			// 系统错：中断整轮，把可读的错误交出去。
			return result, err
		}
	}

	if err := cursor.Save(); err != nil {
		return result, fmt.Errorf("保存增量水位：%w", err)
	}

	// 换代回填到此为止：这一轮源文件都进过 L1 了，之后照常按水位增量走。
	// 标记写不成不挡收集——下一轮无非再全读一遍。
	if cursor.Forced() {
		if err := db.MarkL1Backfilled(); err != nil {
			logger.Warn("换代回填标记没写成，下一轮还会全读一遍", "原因", err)
		} else {
			logger.Info("换代回填完成：L1 已按源文件填齐")
		}
	}

	// 维度派生（L2a）：每轮收集后重建一遍。失败不挡收集——维度是派生视图，
	// 随时可从 L1 重来（AC-15）；账没重建成功只影响查询的新鲜度。
	if dimRegistry, derr := derive.Default(); derr == nil {
		if derr := derive.Rebuild(db, dimRegistry); derr != nil {
			logger.Warn("维度重建失败，下轮收集会再试", "原因", derr)
		}
	}

	deps.report(Progress{Stage: StageBackup})
	result.Backup = runBackup(cfg, logger, deps)
	result.Duration = time.Since(start)
	deps.report(Progress{Stage: StageDone})

	logger.Info("本轮收集结束",
		"发现", result.Found(), "新增", result.New(), "归档", result.Archived(),
		"空会话", result.Empty(), "失败", result.Failures(),
		"备份", backupSummary(result.Backup), "结论", string(result.Outcome()))
	return result, nil
}

// collectOne 处理一个渠道：增量筛 → 解析 → 渲染 → 归档。
//
// 单条文件解析失败只跳过它自己——一个渠道坏了不影响别的渠道（验收标准 1）。
// 返回的错误只在**系统错**时非空（归档写不进去），那时整轮要中断。
//
// force 为真时不看游标，这批文件全跑一遍（界面上的「重试」）。
func collectOne(cfg config.Config, logger *slog.Logger, db *store.DB, cursor *store.Cursor, report channel.Report, adapter channel.Adapter, force bool, notify func(done int, finished bool)) (ChannelResult, error) {
	cr := ChannelResult{
		ID:      report.ID,
		Name:    report.Name,
		Status:  report.Status,
		Enabled: Enabled(cfg.Channels, report.ID),
	}

	switch {
	case !cr.Enabled:
		logger.Info("渠道已停用，跳过", "渠道", report.ID)
		return cr, nil
	case report.Status == channel.StatusNotInstalled:
		logger.Info("渠道未安装，跳过", "渠道", report.ID)
		return cr, nil
	case report.Status == channel.StatusFailed:
		// 检测失败是渠道级失败：记下来，整轮继续。
		cr.Err = report.Err
		logger.Warn("渠道检测失败，跳过这个渠道", "渠道", report.ID, "原因", report.Err)
		return cr, nil
	case adapter == nil:
		cr.Err = errors.New("渠道报了可用但没有实现")
		logger.Warn("渠道没有适配器，跳过", "渠道", report.ID)
		return cr, nil
	}

	cr.Found = len(report.Files)
	for i, sf := range report.Files {
		if notify != nil {
			notify(i, false)
		}
		if !force && classifyPending(cursor, report.Name, adapter, sf) == store.ChangeNone {
			continue
		}
		cr.New++

		session, err := adapter.Parse(sf)
		if err != nil {
			// 数据错：跳过这一条，其余照常。
			cr.Failed++
			cr.Failures = append(cr.Failures, Failure{Path: sf.Path, Err: err})
			logger.Warn("解析失败，跳过这个文件", "渠道", report.ID, "文件", sf.Path, "原因", err)

			// 记一次失败：下一轮自动重试；连续失败达到上限后停手，
			// 直到文件大小或内容变化（I-048）。
			cursor.MarkFailed(sf)
			continue
		}

		// 防御性补齐：个别适配器没填渠道 ID（会话归属以注册表为准）；
		// 会话标识缺失时退回源文件路径——入库的身份键必须有值。
		if session.Channel == "" {
			session.Channel = report.ID
		}
		if session.ID == "" {
			session.ID = sf.Path
		}

		if session.MessageCount() == 0 {
			// 空会话（打开过没说话）：不算失败、不入库、不导出。也要记进水位，
			// 否则每一轮都把它当「没见过的文件」重来一遍（I-039）。
			cr.Empty++
			cursor.MarkProcessed(sf, "")
			logger.Info("空会话，跳过", "渠道", report.ID, "文件", sf.Path)
			continue
		}

		// 先入 L1（唯一真源），再导出 Markdown（L2 派生）。导出用的是同一个
		// 内存会话模型；「经库回组后渲染一字不差」由等价用例钉住（I-085）。
		if err := db.WriteSession(session, time.Now()); err != nil {
			// 系统错：主库写不进去。停下，别装作继续。
			cr.Failed++
			cr.Failures = append(cr.Failures, Failure{Path: sf.Path, Err: err})
			logger.Error("入库失败，整轮中断", "渠道", report.ID, "文件", sf.Path, "原因", err)
			return cr, fmt.Errorf("入库 %s：%w", sf.Path, err)
		}

		path, err := archive.Write(cfg.ArchiveDir, session, report.Name, render.Render(session, report.Name))
		if err != nil {
			// 系统错：磁盘满了、没权限之类。停下，别装作继续。
			cr.Failed++
			cr.Failures = append(cr.Failures, Failure{Path: sf.Path, Err: err})
			logger.Error("导出失败，整轮中断", "渠道", report.ID, "文件", sf.Path, "原因", err)
			return cr, fmt.Errorf("导出 %s：%w", sf.Path, err)
		}

		cursor.MarkProcessed(sf, path)
		cr.Archived++
	}

	if notify != nil {
		notify(len(report.Files), true)
	}

	logger.Info("渠道处理完成",
		"渠道", report.ID, "发现", cr.Found, "新增", cr.New, "归档", cr.Archived,
		"空会话", cr.Empty, "失败", cr.Failed)
	return cr, nil
}

// runBackup 跑备份那一步。失败不中断——归档已经好了（验收标准 4）。
func runBackup(cfg config.Config, logger *slog.Logger, deps Deps) BackupResult {
	method := cfg.Backup.Mode
	out := BackupResult{Method: method}

	registry := deps.Backups
	if registry == nil {
		registry = backup.NewRegistry()
		if err := backups.RegisterInto(registry); err != nil {
			out.Err = fmt.Errorf("注册内置备份方式：%w", err)
			logger.Warn("备份没做成", "方式", method, "原因", out.Err)
			return out
		}
	}

	result, err := registry.Run(method, backup.Settings{
		Dir:     cfg.ArchiveDir,
		Remote:  cfg.Backup.Git.Remote,
		Branch:  cfg.Backup.Git.Branch,
		Timeout: time.Duration(cfg.Backup.Git.TimeoutSeconds) * time.Second,
	})
	if err != nil {
		// 配置认得出这个取值，但还没有实现（如阶段 2 的 sync）。
		// 归档已经好了，只算这一步没成。
		out.Err = err
		logger.Warn("备份方式还没实现，跳过备份", "方式", method, "原因", err)
		return out
	}

	out.Changed = result.Changed
	out.Detail = result.Detail
	out.Err = result.Err
	if out.Err != nil {
		logger.Warn("备份失败", "方式", method, "原因", out.Err)
	} else {
		logger.Info("备份完成", "方式", method, "说明", out.Detail)
	}
	return out
}

// applyArchiveRoot 把归档目录告诉导入型渠道（channel.ArchiveRootSetter）；
// 没有这类渠道时什么也不做。
func applyArchiveRoot(adapters map[string]channel.Adapter, archiveDir string) {
	if strings.TrimSpace(archiveDir) == "" {
		return
	}
	for _, adapter := range adapters {
		if setter, ok := adapter.(channel.ArchiveRootSetter); ok {
			setter.SetArchiveRoot(archiveDir)
		}
	}
}

// resolveBaseDir 定出游标放哪儿。
func resolveBaseDir(deps Deps) (string, error) {
	if strings.TrimSpace(deps.BaseDir) != "" {
		return deps.BaseDir, nil
	}
	dir, err := config.BaseDir()
	if err != nil {
		return "", fmt.Errorf("定位配置目录：%w", err)
	}
	return dir, nil
}

// resolveChannels 定出这一轮有哪些渠道可用。
func resolveChannels(deps Deps) (*channel.Registry, map[string]channel.Adapter, error) {
	registry := deps.Channels
	if registry == nil {
		registry = channel.NewRegistry()
		if err := channels.RegisterInto(registry); err != nil {
			return nil, nil, fmt.Errorf("注册内置渠道：%w", err)
		}
	}

	adapters := map[string]channel.Adapter{}
	for _, entry := range registry.Entries() {
		adapters[entry.ID] = entry.Adapter
	}
	return registry, adapters, nil
}

// pickReports 按 only 挑出要处理的渠道；only 为空就全都留下。
//
// 保持原有顺序（注册表的顺序是稳定的），这样进度里的「第几个 / 共几个」不会跳。
func pickReports(reports []channel.Report, only []string) []channel.Report {
	if len(only) == 0 {
		return reports
	}
	wanted := make(map[string]bool, len(only))
	for _, id := range only {
		wanted[id] = true
	}

	out := make([]channel.Report, 0, len(only))
	for _, report := range reports {
		if wanted[report.ID] {
			out = append(out, report)
		}
	}
	return out
}

// Enabled 判断一个渠道是否启用。
//
// 配置里没写这个渠道 = 启用；明确写了 false 才是停用。
// 这样「刚装好、还没进过设置界面」的人第一次跑就能收到东西，
// 而不是收到一份空的运行记录去猜为什么。
func Enabled(configured map[string]bool, id string) bool {
	if v, ok := configured[id]; ok {
		return v
	}
	return true
}

// Empty 汇总「空会话」条数：解析成功、但一条消息都没有的源文件。
//
// 这些既不算失败也不算收下——界面上要说的是「总会话多少、有效会话多少」（I-039）。
func (r Result) Empty() int {
	n := 0
	for _, c := range r.Channels {
		n += c.Empty
	}
	return n
}

// Found 汇总检测到的源文件数。
func (r Result) Found() int {
	n := 0
	for _, c := range r.Channels {
		n += c.Found
	}
	return n
}

// New 汇总本轮需要处理的文件数。
func (r Result) New() int {
	n := 0
	for _, c := range r.Channels {
		n += c.New
	}
	return n
}

// Archived 汇总成功归档的份数。
func (r Result) Archived() int {
	n := 0
	for _, c := range r.Channels {
		n += c.Archived
	}
	return n
}

// Failures 汇总失败处数：检测失败的渠道、解析或归档失败的文件、失败的备份。
//
// 「未安装」不算失败——那不是出错，是那台机器上没装。
func (r Result) Failures() int {
	n := 0
	for _, c := range r.Channels {
		if c.Status == channel.StatusFailed || c.Err != nil {
			n++
		}
		n += c.Failed
	}
	if r.Backup.Err != nil {
		n++
	}
	return n
}

// Outcome 给出整轮结论。
func (r Result) Outcome() Outcome {
	switch failures := r.Failures(); {
	case failures == 0:
		return OutcomeSuccess
	case r.Archived() == 0:
		return OutcomeFailed
	default:
		return OutcomePartial
	}
}

// backupSummary 把备份结果压成日志里的一段。
func backupSummary(b BackupResult) string {
	switch {
	case b.Method == "":
		return "未配置"
	case b.Err != nil:
		return b.Method + "（失败）"
	case b.Detail == "":
		return b.Method
	default:
		return b.Method + "（" + b.Detail + "）"
	}
}
