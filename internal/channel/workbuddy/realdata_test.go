package workbuddy

import (
	"os"
	"testing"

	"github.com/Always1023/AgentMemory/internal/channel"
)

// TestAgainstRealData 拿本机真实的 WorkBuddy 数据跑一遍。
//
// 默认跳过，设 AGENTMEMORY_REAL_DATA=1 才跑：
// 别人的机器上不一定装了 WorkBuddy，测试也不该依赖真实用户数据。
func TestAgainstRealData(t *testing.T) {
	if os.Getenv("AGENTMEMORY_REAL_DATA") != "1" {
		t.Skip("设 AGENTMEMORY_REAL_DATA=1 才跑（需要本机装了 WorkBuddy）")
	}

	adapter := Adapter{}
	detected := adapter.Detect()
	if detected.Status != channel.StatusAvailable {
		t.Fatalf("本机应当装了 WorkBuddy: status=%q root=%q err=%v",
			detected.Status, detected.Root, detected.Err)
	}
	t.Logf("认出 %d 个会话文件，数据根目录 %s", len(detected.Files), detected.Root)

	parsed, empty, failed := 0, 0, 0
	for _, f := range detected.Files {
		session, err := adapter.Parse(f)
		if err != nil {
			failed++
			t.Logf("跳过 %s: %v", f.Path, err)
			continue
		}
		if session.Title == "" || session.ID == "" {
			t.Errorf("%s 解析后缺标题或缺 ID", f.Path)
		}
		if session.MessageCount() == 0 {
			empty++
			continue
		}
		t.Logf("样例：标题=%q 消息=%d 条 项目=%q 开始=%s",
			session.Title, session.MessageCount(), session.ProjectPath,
			session.StartedAt.Format("2006-01-02 15:04"))
		parsed++
	}
	t.Logf("合计：有内容 %d 个，空会话 %d 个，失败 %d 个", parsed, empty, failed)
	if parsed == 0 {
		t.Fatal("一条有内容的会话都没解析出来")
	}
}
