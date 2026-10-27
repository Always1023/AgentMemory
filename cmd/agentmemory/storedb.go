package main

// 主库句柄的 cmd 侧入口。界面要读运行记录、记运行记录——这些是 store 的
// 只读查询与追加，允许 cmd 直调（ARCHITECTURE.md AC-10 的例外条款）；
// 这里是它拿主库的唯一入口，谁也不许自己拼 memory.db 的路径。

import (
	"github.com/Always1023/AgentMemory/internal/config"
	"github.com/Always1023/AgentMemory/internal/store"
)

// openStoreDB 打开主库（必要时创建并迁移）。调用方负责 Close。
func openStoreDB() (*store.DB, error) {
	dir, err := config.BaseDir()
	if err != nil {
		return nil, err
	}
	return store.OpenDB(dir)
}
