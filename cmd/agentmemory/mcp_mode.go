package main

// MCP 模式（F15 / I-091）：stdio 上的只读接口。
//
// 单独一个文件，是因为入口的「静默模式不许读标准输入」那条规矩还留着：
// --collect 里不能有 os.Stdin，而 --mcp 恰恰要靠标准输入说话，两者分开看。

import (
	"fmt"
	"log/slog"
	"os"

	"github.com/Always1023/AgentMemory/internal/config"
	"github.com/Always1023/AgentMemory/internal/mcp"
)

// runMCP 以 MCP 服务（stdio）跑起来，客户端断开就退出。
//
// 只读（AC-17）：打开主库只为查询；切片导出落在归档目录的「切片」子目录。
// 开关关着就拒绝启动——「关掉 = 不可用」是验收标准的一部分。
func runMCP(cfg config.Config, logger *slog.Logger) int {
	if !cfg.MCPEnabled {
		logger.Error("MCP 没打开")
		fmt.Fprintf(os.Stderr, "AgentMemory: 对外接口没打开——去设置页「对外接口」打开后，再让客户端调用 agentmemory -mcp\n")
		return exitUsage
	}
	db, err := openStoreDB()
	if err != nil {
		logger.Error("主库打不开", "原因", err)
		fmt.Fprintf(os.Stderr, "AgentMemory: 主库打不开: %v\n", err)
		return exitUsage
	}
	defer db.Close()

	srv := &mcp.Server{DB: db, ArchiveDir: cfg.ArchiveDir, Logger: logger, Version: "b17"}
	logger.Info("MCP 服务启动", "归档目录", cfg.ArchiveDir)
	if err := mcp.Serve(os.Stdin, os.Stdout, srv); err != nil {
		logger.Error("MCP 服务异常退出", "原因", err)
		return exitFailed
	}
	logger.Info("MCP 服务结束")
	return exitOK
}
