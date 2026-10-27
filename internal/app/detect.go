package app

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/Always1023/AgentMemory/internal/channel"
	"github.com/Always1023/AgentMemory/internal/store"
)

// censusParseTimeout 是单条会话统计解析的上限。库被产品占着、WAL 在恢复时，
// 不能让整个界面停在过场里；超时就把这一条算解析失败，其他渠道照常读（I-051）。
const censusParseTimeout = 1500 * time.Millisecond

// CensusResult 是一次界面统计读取的结果：检测与解析都来自同一批源文件。
//
// 与 Collect 不同，这里不写归档、不动游标；它只回答「本机现在有什么」。
// 界面刷新时用它，避免把「归档份数」冒充成「本机有效会话」。
type CensusResult struct {
	Reports []channel.Report
	// Names 与 Reports 一一对应，保留渠道显示名（Report 本身没有这个字段）。
	Names map[string]string
	// Valid / Empty / Failed 按渠道 ID 记录解析结果。
	Valid  map[string]int
	Empty  map[string]int
	Failed map[string]int
}

// Census 读取所有渠道的源文件并逐个判定，返回按渠道分组的统计。
//
// progress 在检测和每个渠道、每个文件前后报进度；它不参与业务判断。
//
// 首页只关心「有没有消息」，所以先查统计缓存；没命中时优先用适配器的
// 轻量探测（channel.Prober），只有没实现探测的渠道才退回完整 Parse（I-056）。
func Census(deps Deps, progress func(Progress)) (CensusResult, error) {
	out := CensusResult{
		Names:  map[string]string{},
		Valid:  map[string]int{},
		Empty:  map[string]int{},
		Failed: map[string]int{},
	}
	registry, adapters, err := resolveChannels(deps)
	if err != nil {
		return out, err
	}
	applyArchiveRoot(adapters, deps.ArchiveDir)

	if progress != nil {
		progress(Progress{Stage: StageDetect})
	}
	reports := registry.DetectAll()
	out.Reports = reports
	for _, entry := range registry.Entries() {
		out.Names[entry.ID] = entry.Name
	}

	cdb := openCensusDB(deps)
	if cdb != nil {
		defer cdb.Close()
	}
	var cache *store.CensusCache
	if cdb != nil {
		cache, err = cdb.OpenCensus()
		if err != nil {
			// 缓存打不开只影响速度，不影响这次统计结果。
			cache = nil
		}
	}
	seen := map[string]bool{}

	for i, report := range reports {
		adapter := adapters[report.ID]
		notify := func(done int, finished bool) {
			if progress == nil {
				return
			}
			progress(Progress{
				Stage:       StageChannel,
				ChannelID:   report.ID,
				ChannelName: out.Names[report.ID],
				Index:       i + 1,
				Total:       len(reports),
				FileDone:    done,
				FileTotal:   len(report.Files),
				ChannelDone: finished,
			})
		}
		notify(0, false)
		if report.Status != channel.StatusAvailable || adapter == nil {
			notify(len(report.Files), true)
			continue
		}
		for j, sf := range report.Files {
			notify(j, false)
			seen[sf.Path] = true

			if state, ok := cache.Lookup(sf); ok {
				countCensus(&out, report.ID, state)
				continue
			}

			state, err := censusFileState(adapter, sf)
			if err != nil {
				out.Failed[report.ID]++
				continue
			}
			cache.Mark(sf, state)
			countCensus(&out, report.ID, state)
		}
		notify(len(report.Files), true)
	}
	if progress != nil {
		progress(Progress{Stage: StageDone})
	}

	// 缓存只是加速：写不进去也不影响这次统计结果。
	cache.KeepOnly(seen)
	_ = cache.Save()

	return out, nil
}

// openCensusDB 打开统计缓存所在的 主库。
//
// 测试里注入了渠道、又没指定 BaseDir 时不开：否则测试会写进真实的
// 配置目录。生产用内置渠道（Channels 为空），照常走默认目录。
func openCensusDB(deps Deps) *store.DB {
	if deps.BaseDir == "" && deps.Channels != nil {
		return nil
	}
	dir, err := resolveBaseDir(deps)
	if err != nil {
		return nil
	}
	db, err := store.OpenDB(dir)
	if err != nil {
		return nil
	}
	return db
}

// censusFileState 判定一个源文件是「有效」还是「空」。
//
// 优先用适配器的轻量探测；没实现就退回完整 Parse。
func censusFileState(adapter channel.Adapter, sf channel.SourceFile) (string, error) {
	if prober, ok := adapter.(channel.Prober); ok {
		has, err := probeCensusFile(prober, sf)
		if err != nil {
			return "", err
		}
		if has {
			return store.CensusValid, nil
		}
		return store.CensusEmpty, nil
	}

	session, err := parseCensusFile(adapter, sf)
	if err != nil {
		return "", err
	}
	if session.MessageCount() == 0 {
		return store.CensusEmpty, nil
	}
	return store.CensusValid, nil
}

func countCensus(out *CensusResult, channelID, state string) {
	switch state {
	case store.CensusValid:
		out.Valid[channelID]++
	case store.CensusEmpty:
		out.Empty[channelID]++
	}
}

// probeCensusFile 给单条轻量探测加一道墙钟超时（与解析同一档）。
func probeCensusFile(prober channel.Prober, sf channel.SourceFile) (bool, error) {
	type probed struct {
		has bool
		err error
	}
	done := make(chan probed, 1)
	go func() {
		has, err := prober.Probe(sf)
		done <- probed{has: has, err: err}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), censusParseTimeout)
	defer cancel()
	select {
	case got := <-done:
		return got.has, got.err
	case <-ctx.Done():
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return false, fmt.Errorf("探测超过 %s，先跳过这一条", censusParseTimeout)
		}
		return false, ctx.Err()
	}
}

// parseCensusFile 给单条解析加一道墙钟超时。
//
// 不把 ctx 往下塞：适配器契约没有 context，这里只能让等待方先走；卡住的 goroutine
// 会随驱动调用自行结束。统计是只读的，提前返回不会留下半写状态。
func parseCensusFile(adapter channel.Adapter, sf channel.SourceFile) (channel.Session, error) {
	type parsed struct {
		session channel.Session
		err     error
	}
	done := make(chan parsed, 1)
	go func() {
		session, err := adapter.Parse(sf)
		done <- parsed{session: session, err: err}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), censusParseTimeout)
	defer cancel()
	select {
	case got := <-done:
		return got.session, got.err
	case <-ctx.Done():
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return channel.Session{}, fmt.Errorf("读取超过 %s，先跳过这一条", censusParseTimeout)
		}
		return channel.Session{}, ctx.Err()
	}
}

// Detect 只做检测，不做收集。
//
// 界面用它列出渠道状态（首次运行引导 I-013、渠道列表 I-015），命令行不走这条。
// 它与 `Collect` 内部用的是**同一段检测代码**（都落到 `resolveChannels` + `DetectAll`），
// 所以界面上看到的渠道状态与 `--collect` 打印的是同一份结果，而不是两套各自实现
// （I-013 验收标准 3 要的就是这个「一致」）。
func Detect(deps Deps) ([]channel.Report, error) {
	registry, _, err := resolveChannels(deps)
	if err != nil {
		return nil, err
	}
	return registry.DetectAll(), nil
}
