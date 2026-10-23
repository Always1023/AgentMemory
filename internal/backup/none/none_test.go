package none

import (
	"testing"

	"github.com/Always1023/AgentMemory/internal/backup"
)

// 验收标准 2 的机制侧：选「仅本地」时不报错、不动任何东西。
// （行为侧「断网也能跑完」由人验收，见 issue 文件「怎么验收」。）
func TestBackDoesNothingAndReportsWhy(t *testing.T) {
	result := Method{}.Back(backup.Settings{
		Dir:    "D:/归档",
		Remote: "git@example.com:x.git", // 就算配了地址，也不该去碰它
	})

	if result.Err != nil {
		t.Errorf("仅本地不该报错: %v", result.Err)
	}
	if result.Changed {
		t.Error("仅本地不该报告有变化")
	}
	if result.Detail != Detail {
		t.Errorf("说明 = %q，想要 %q", result.Detail, Detail)
	}
}

func TestIdentityMatchesConfig(t *testing.T) {
	// ID 必须与配置里的 backup.mode 取值一致，否则选「仅本地」会找不到实现。
	if ID != "none" {
		t.Errorf("ID = %q，想要 %q", ID, "none")
	}
	if Name == "" {
		t.Error("缺少显示名")
	}
}
