package app

// 全量导出（I-085 / AC-15）：从 L1 把全部会话重新导出成 Markdown。
//
// 「派生可弃」的导出这一支：全文索引有 RebuildFTS，维度有 derive.Rebuild，
// 导出文件在这里。只生成、不删除——文件名是纯函数，同会话覆盖写；
// 目录里多出来的旧文件不动（桌面旧归档的清理由人决定，PRD 已确认）。

import (
	"fmt"
	"io"
	"log/slog"

	"github.com/Always1023/AgentMemory/internal/archive"
	"github.com/Always1023/AgentMemory/internal/channel"
	"github.com/Always1023/AgentMemory/internal/channels"
	"github.com/Always1023/AgentMemory/internal/config"
	"github.com/Always1023/AgentMemory/internal/render"
	"github.com/Always1023/AgentMemory/internal/store"
)

// RebuildExports 从 L1 重建归档目录里的全部 Markdown，返回导出的会话数。
//
// 返回的错误只在系统错（配置不可用、主库打不开、导出写不进）时非空；
// 单个会话回组失败按数据错跳过并记日志，不挡其余。
func RebuildExports(cfg config.Config, logger *slog.Logger, deps Deps) (int, error) {
	if logger == nil {
		logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	if err := cfg.Validate(); err != nil {
		return 0, fmt.Errorf("配置不可用：%w", err)
	}
	baseDir, err := resolveBaseDir(deps)
	if err != nil {
		return 0, err
	}
	db, err := store.OpenDB(baseDir)
	if err != nil {
		return 0, err
	}
	defer db.Close()

	sessions, err := db.LatestSessions()
	if err != nil {
		return 0, fmt.Errorf("列会话：%w", err)
	}
	names := channelDisplayNames(deps)

	count := 0
	for _, ref := range sessions {
		session, ok, err := db.ReadSession(ref.Channel, ref.SourceRef, ref.SessionKey)
		if err != nil {
			logger.Warn("会话回组失败，跳过", "渠道", ref.Channel, "会话", ref.SessionKey, "原因", err)
			continue
		}
		if !ok {
			continue
		}
		name := names[ref.Channel]
		if name == "" {
			name = ref.Channel
		}
		if _, err := archive.Write(cfg.ArchiveDir, session, name, render.Render(session, name)); err != nil {
			return count, fmt.Errorf("导出 %s：%w", ref.SessionKey, err)
		}
		count++
	}
	logger.Info("全量导出完成", "会话", count)
	return count, nil
}

// channelDisplayNames 渠道 ID → 显示名（重试、日志、导出共用一份口径）。
func channelDisplayNames(deps Deps) map[string]string {
	registry := deps.Channels
	if registry == nil {
		registry = channel.NewRegistry()
		if err := channels.RegisterInto(registry); err != nil {
			return map[string]string{}
		}
	}
	names := map[string]string{}
	for _, entry := range registry.Entries() {
		names[entry.ID] = entry.Name
	}
	return names
}
