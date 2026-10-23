package codex

import (
	"os"
	"testing"

	"github.com/Always1023/AgentMemory/internal/channel"
)

// TestAgainstRealData 拿本机真实的 Codex CLI 数据跑一遍。
//
// 默认跳过，设 AGENTMEMORY_REAL_DATA=1 才跑：
// 别人的机器上不一定装了 Codex CLI，测试也不该依赖真实用户数据。
func TestAgainstRealData(t *testing.T) {
	if os.Getenv("AGENTMEMORY_REAL_DATA") != "1" {
		t.Skip("设 AGENTMEMORY_REAL_DATA=1 才跑（需要本机装了 Codex CLI）")
	}

	adapter := Adapter{}
	detected := adapter.Detect()
	if detected.Status != channel.StatusAvailable {
		t.Fatalf("本机应当装了 Codex CLI: status=%q root=%q err=%v", detected.Status, detected.Root, detected.Err)
	}

	var total int64
	for _, f := range detected.Files {
		total += f.Size
	}
	t.Logf("发现 %d 个会话文件，合计 %.1f MB，根目录 %s",
		len(detected.Files), float64(total)/(1<<20), detected.Root)

	// 取最大的几个文件——它们最考验流式读取。
	const sample = 5
	parsed, failed := 0, 0
	for i, f := range detected.Files {
		if i >= sample {
			break
		}
		session, err := adapter.Parse(f)
		if err != nil {
			failed++
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
			t.Logf("样例：%.1f MB 标题=%q 消息=%d 条 项目=%q 开始=%s",
				float64(f.Size)/(1<<20), session.Title, session.MessageCount(),
				session.ProjectPath, session.StartedAt.Format("2006-01-02 15:04"))
		}
		parsed++
	}
	t.Logf("前 %d 个文件：成功 %d 个，跳过 %d 个", sample, parsed, failed)
	if parsed == 0 {
		t.Fatal("一个都没解析成功")
	}
}
