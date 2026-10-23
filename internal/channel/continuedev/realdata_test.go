package continuedev

import (
	"os"
	"testing"

	"github.com/Always1023/AgentMemory/internal/channel"
)

// TestAgainstRealData 拿本机真实的 Continue 数据跑一遍。
//
// 默认跳过，设 AGENTMEMORY_REAL_DATA=1 才跑：别人的机器上不一定装了 Continue。
func TestAgainstRealData(t *testing.T) {
	if os.Getenv("AGENTMEMORY_REAL_DATA") != "1" {
		t.Skip("设 AGENTMEMORY_REAL_DATA=1 才跑（需要本机装了 Continue）")
	}

	adapter := Adapter{}
	detected := adapter.Detect()
	if detected.Status != channel.StatusAvailable {
		t.Fatalf("本机应当装了 Continue: status=%q root=%q err=%v", detected.Status, detected.Root, detected.Err)
	}
	t.Logf("发现 %d 个会话文件，根目录 %s", len(detected.Files), detected.Root)

	parsed, failed := 0, 0
	for _, f := range detected.Files {
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
		if session.StartedAt.IsZero() {
			t.Errorf("%s 解析后没有开始时间", f.Path)
		}
		t.Logf("样例：标题=%q 消息=%d 条 项目=%q 开始=%s",
			session.Title, session.MessageCount(), session.ProjectPath, session.StartedAt.Format("2006-01-02 15:04"))
		parsed++
	}
	t.Logf("成功 %d 个，跳过 %d 个", parsed, failed)
	if parsed == 0 {
		t.Fatal("一个都没解析成功")
	}
}
