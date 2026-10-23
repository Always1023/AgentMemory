// Package backups 汇总本项目内置的备份方式。
//
// 新增一种备份方式 = 写一个实现包 + 在 All() 里加一行。
// 这是 docs/ARCHITECTURE.md 承诺的「新增一种备份方式不改动既有代码」的落点：
// 加方式只动这里，app / channel / store / render / archive 一行都不用改。
package backups

import (
	"github.com/Always1023/AgentMemory/internal/backup"
	"github.com/Always1023/AgentMemory/internal/backup/git"
	"github.com/Always1023/AgentMemory/internal/backup/none"
)

// All 返回内置的全部备份方式。
//
// 阶段 2 会在这里加 sync（镜像到同步目录，交给网盘客户端）。
func All() []backup.Entry {
	return []backup.Entry{
		{ID: git.ID, Name: git.Name, Method: git.Method{}},
		{ID: none.ID, Name: none.Name, Method: none.Method{}},
	}
}

// RegisterInto 把内置方式注册进注册表。
func RegisterInto(r *backup.Registry) error {
	for _, e := range All() {
		if err := r.Register(e); err != nil {
			return err
		}
	}
	return nil
}
