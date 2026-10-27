// Package storetest 给别的包的测试开一个临时主库。
//
// 单独一个小包是为了不让 store 的生产代码 import testing。
package storetest

import (
	"testing"

	"github.com/Always1023/AgentMemory/internal/store"
)

// Open 开一个临时目录下的主库，测试结束自动关闭。
func Open(t *testing.T) *store.DB {
	t.Helper()
	db, err := store.OpenDB(t.TempDir())
	if err != nil {
		t.Fatalf("打开主库: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}
