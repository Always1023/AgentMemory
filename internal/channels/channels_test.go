package channels

import (
	"os"
	"strings"
	"testing"

	"github.com/Always1023/AgentMemory/internal/channel"
)

func TestAllEntriesAreRegisterable(t *testing.T) {
	registry := channel.NewRegistry()
	if err := RegisterInto(registry); err != nil {
		t.Fatalf("内置渠道注册失败: %v", err)
	}
	if len(registry.Entries()) != len(All()) {
		t.Errorf("注册了 %d 条，All() 给了 %d 条", len(registry.Entries()), len(All()))
	}
}

func TestAllHaveValidIdentity(t *testing.T) {
	seen := map[string]bool{}
	for _, entry := range All() {
		if entry.ID == "" || entry.Name == "" || entry.Adapter == nil {
			t.Errorf("渠道条目缺东西: %+v", entry)
		}
		if entry.ID != strings.ToLower(entry.ID) || strings.ContainsAny(entry.ID, " _") {
			t.Errorf("渠道 ID %q 应当是小写、连字符分隔", entry.ID)
		}
		if seen[entry.ID] {
			t.Errorf("渠道 ID %q 重复", entry.ID)
		}
		seen[entry.ID] = true
	}
}

func TestMainstreamAgentChannelsAreRegistered(t *testing.T) {
	wanted := map[string]bool{
		"gemini": false, "grok-build": false, "openclaw": false,
		"hermes": false, "pi": false, "minimax": false,
	}
	for _, entry := range All() {
		if _, ok := wanted[entry.ID]; ok {
			wanted[entry.ID] = true
		}
	}
	for id, found := range wanted {
		if !found {
			t.Errorf("主流 Agent 渠道 %q 没有注册", id)
		}
	}
}

// TestAllDetectOnThisMachine 是本机实况核验：所有内置渠道都能完成检测，
// 单个渠道缺失或没有会话是正常结果，但不能有失败。
//
// 默认跳过，设 AGENTMEMORY_REAL_DATA=1 才跑。
func TestAllDetectOnThisMachine(t *testing.T) {
	if os.Getenv("AGENTMEMORY_REAL_DATA") != "1" {
		t.Skip("设 AGENTMEMORY_REAL_DATA=1 才跑（需要本机真实渠道目录）")
	}

	registry := channel.NewRegistry()
	if err := RegisterInto(registry); err != nil {
		t.Fatalf("内置渠道注册失败: %v", err)
	}

	reports := registry.DetectAll()
	if len(reports) != len(All()) {
		t.Fatalf("报告数 = %d，想要 %d", len(reports), len(All()))
	}

	for _, report := range reports {
		t.Logf("%s（%s）：%s，%d 个会话文件", report.Name, report.ID, report.Status, len(report.Files))
		if report.Status == channel.StatusFailed {
			t.Errorf("%s 检测失败: %v", report.ID, report.Err)
		}
	}
}
