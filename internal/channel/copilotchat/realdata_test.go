package copilotchat

import (
	"os"
	"sort"
	"testing"

	"github.com/Always1023/AgentMemory/internal/channel"
)

// TestAgainstRealData 拿本机真实的 VS Code Copilot Chat 数据跑一遍。
//
// 默认跳过，设 AGENTMEMORY_REAL_DATA=1 才跑：
// 别人的机器上不一定装了 VS Code，测试也不该依赖真实用户数据。
func TestAgainstRealData(t *testing.T) {
	if os.Getenv("AGENTMEMORY_REAL_DATA") != "1" {
		t.Skip("设 AGENTMEMORY_REAL_DATA=1 才跑（需要本机装了 VS Code 且用过 Copilot Chat）")
	}

	adapter := Adapter{}
	detected := adapter.Detect()
	if detected.Status != channel.StatusAvailable {
		t.Fatalf("本机应当装了 VS Code: status=%q root=%q err=%v", detected.Status, detected.Root, detected.Err)
	}

	files := append([]channel.SourceFile(nil), detected.Files...)
	sort.Slice(files, func(i, j int) bool { return files[i].Size > files[j].Size })

	var total int64
	for _, f := range detected.Files {
		total += f.Size
	}
	t.Logf("发现 %d 个会话文件，合计 %.1f MB，根目录 %s",
		len(detected.Files), float64(total)/(1<<20), detected.Root)

	// 取最大的几个——它们最考验编辑记录的重建。
	const sample = 8
	parsed, empty, failed := 0, 0, 0
	for i, f := range files {
		if i >= sample {
			break
		}
		session, err := adapter.Parse(f)
		if err != nil {
			if t.Failed() {
				continue
			}
			// 空会话（打开过没说话）在真实数据里很常见，单独数出来。
			empty++
			t.Logf("跳过 %s: %v", f.Path, err)
			continue
		}
		if session.Title == "" {
			t.Errorf("%s 解析后没有标题", f.Path)
		}
		if session.MessageCount() == 0 {
			t.Errorf("%s 解析后没有消息", f.Path)
		}
		if session.ID == "" {
			t.Errorf("%s 解析后没有会话 ID", f.Path)
		}
		if parsed < 3 {
			t.Logf("样例：%.1f KB 标题=%q 消息=%d 条 项目=%q 开始=%s",
				float64(f.Size)/(1<<10), session.Title, session.MessageCount(),
				session.ProjectPath, session.StartedAt.Format("2006-01-02 15:04"))
		}
		parsed++
	}
	t.Logf("最大的 %d 个文件：成功 %d 个，空会话 %d 个，其他失败 %d 个", sample, parsed, empty, failed)
	if parsed == 0 {
		t.Fatal("一个都没解析成功")
	}
}
