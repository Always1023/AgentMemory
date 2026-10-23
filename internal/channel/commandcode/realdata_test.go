package commandcode

import (
	"os"
	"testing"

	"github.com/Always1023/AgentMemory/internal/channel"
)

// TestAgainstRealData 拿本机真实的 CommandCode 数据跑一遍。
//
// 默认跳过，设 AGENTMEMORY_REAL_DATA=1 才跑：
// 测试不依赖真实用户目录与外部环境。
func TestAgainstRealData(t *testing.T) {
	if os.Getenv("AGENTMEMORY_REAL_DATA") != "1" {
		t.Skip("设 AGENTMEMORY_REAL_DATA=1 才跑（需要本机装了 CommandCode）")
	}

	adapter := Adapter{}
	detected := adapter.Detect()
	if detected.Status != channel.StatusAvailable {
		t.Fatalf("本机应当装了 CommandCode: status=%q root=%q err=%v", detected.Status, detected.Root, detected.Err)
	}
	t.Logf("发现 %d 个会话文件，根目录 %s", len(detected.Files), detected.Root)

	const sample = 30
	parsed, failed, empty := 0, 0, 0
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
		if session.MessageCount() == 0 {
			empty++
			continue
		}
		if session.Title == "" || session.ID == "" {
			t.Errorf("%s 解析后缺少标题或 ID", f.Path)
		}
		if parsed < 3 {
			t.Logf("样例：标题=%q 消息=%d 条 项目=%q 开始=%s",
				session.Title, session.MessageCount(), session.ProjectPath, session.StartedAt.Format("2006-01-02 15:04"))
		}
		parsed++
	}
	t.Logf("前 %d 个文件：成功 %d 个，空会话 %d 个，跳过 %d 个", sample, parsed, empty, failed)
	if parsed == 0 {
		t.Fatal("一个都没解析成功")
	}
}
