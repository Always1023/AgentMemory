package cursor

import (
	"os"
	"testing"

	"github.com/Always1023/AgentMemory/internal/channel"
)

// TestAgainstRealData 拿本机真实的 Cursor 数据跑一遍。
//
// 默认跳过，设 AGENTMEMORY_REAL_DATA=1 才跑：
// 别人的机器上不一定装了 Cursor，测试也不该依赖真实用户数据。
func TestAgainstRealData(t *testing.T) {
	if os.Getenv("AGENTMEMORY_REAL_DATA") != "1" {
		t.Skip("设 AGENTMEMORY_REAL_DATA=1 才跑（需要本机装了 Cursor）")
	}

	adapter := Adapter{}
	detected := adapter.Detect()
	if detected.Status != channel.StatusAvailable {
		t.Fatalf("本机应当装了 Cursor: status=%q root=%q err=%v",
			detected.Status, detected.Root, detected.Err)
	}
	t.Logf("库里认出 %d 条会话，库文件 %s", len(detected.Files), detected.Root)

	withMessages, empty, failed := 0, 0, 0
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
		if withMessages < 3 {
			t.Logf("样例：标题=%q 消息=%d 条 开始=%s",
				session.Title, session.MessageCount(), session.StartedAt.Format("2006-01-02 15:04"))
		}
		withMessages++
	}
	t.Logf("有会话 %d 条，空会话 %d 条，解析失败 %d 条", withMessages, empty, failed)
	if withMessages == 0 {
		t.Fatal("一条有内容的会话都没解析出来")
	}
}
