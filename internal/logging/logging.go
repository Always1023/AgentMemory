// Package logging 负责本项目唯一的日志出口。
//
// 约定（AGENTS.md 第 11 节「项目自己的代码规范」）：
// 日志同时写到控制台与文件；不记令牌，不记消息正文全文。
//
// 两个出口**各写各的**（见 fanout.go）：控制台不可写时（双击启动就是这样），
// 文件那一份照样拿到每一行——不然最需要日志的时候偏偏没有日志（I-020）。
package logging

import (
	"io"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/Always1023/AgentMemory/internal/config"
)

// FileName 是日志文件名。
const FileName = "agentmemory.log"

// Dir 返回日志目录，与配置同根。
func Dir() (string, error) {
	base, err := config.BaseDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(base, "logs"), nil
}

// Setup 建好日志：一条写 console，一条追加写 <日志目录>/agentmemory.log。
//
// 两个出口各写各的：console 写不进去（双击启动没有控制台）也不影响文件那一份。
// 返回的 io.Closer 负责关闭文件，调用方 defer 它。
func Setup(console io.Writer, level slog.Level) (*slog.Logger, io.Closer, error) {
	dir, err := Dir()
	if err != nil {
		return nil, nil, err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, nil, err
	}

	f, err := os.OpenFile(filepath.Join(dir, FileName), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil, nil, err
	}

	handler := slog.NewTextHandler(newFanOut(console, f), &slog.HandlerOptions{Level: level})
	return slog.New(handler), f, nil
}
