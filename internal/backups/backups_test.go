package backups

import (
	"strings"
	"testing"

	"github.com/Always1023/AgentMemory/internal/backup"
)

func TestAllEntriesAreRegisterable(t *testing.T) {
	registry := backup.NewRegistry()
	if err := RegisterInto(registry); err != nil {
		t.Fatalf("内置备份方式注册失败: %v", err)
	}
	if len(registry.Entries()) != len(All()) {
		t.Errorf("注册了 %d 条，All() 给了 %d 条", len(registry.Entries()), len(All()))
	}
}

func TestAllHaveValidIdentity(t *testing.T) {
	seen := map[string]bool{}
	for _, entry := range All() {
		if entry.ID == "" || entry.Name == "" || entry.Method == nil {
			t.Errorf("备份方式条目缺东西: %+v", entry)
		}
		if entry.ID != strings.ToLower(entry.ID) || strings.ContainsAny(entry.ID, " _") {
			t.Errorf("ID %q 应当是小写、无空格", entry.ID)
		}
		if seen[entry.ID] {
			t.Errorf("ID %q 重复", entry.ID)
		}
		seen[entry.ID] = true
	}

	// 配置里认的三种取值，阶段 1 已经落地两种（sync 排在阶段 2）。
	for _, id := range []string{"git", "none"} {
		if !seen[id] {
			t.Errorf("内置方式里缺 %s", id)
		}
	}
}
