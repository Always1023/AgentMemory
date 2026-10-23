package zcode

import (
	"os"
	"sort"
	"testing"

	"github.com/Always1023/AgentMemory/internal/channel"
)

// TestAgainstRealData 拿本机真实的 ZCode 数据跑一遍。
//
// 默认跳过，设 AGENTMEMORY_REAL_DATA=1 才跑：
// 别人的机器上不一定装了 ZCode，测试也不该依赖真实用户数据。
//
// 这个库本机有 386 条会话、两万多条消息，所以只挑消息最多的几条细验，不逐条全跑。
func TestAgainstRealData(t *testing.T) {
	if os.Getenv("AGENTMEMORY_REAL_DATA") != "1" {
		t.Skip("设 AGENTMEMORY_REAL_DATA=1 才跑（需要本机装了 ZCode）")
	}

	adapter := Adapter{}
	detected := adapter.Detect()
	if detected.Status != channel.StatusAvailable {
		t.Fatalf("本机应当装了 ZCode: status=%q root=%q err=%v",
			detected.Status, detected.Root, detected.Err)
	}
	t.Logf("库里认出 %d 条会话，库文件 %s", len(detected.Files), detected.Root)

	// 大小是消息条数，降序就是「最重的几条」。
	files := append([]channel.SourceFile(nil), detected.Files...)
	sort.Slice(files, func(i, j int) bool { return files[i].Size > files[j].Size })

	const sample = 3
	parsed, empty, failed := 0, 0, 0
	for i, f := range files {
		if i >= sample {
			break
		}
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
	t.Logf("最重的 %d 条：成功 %d 条，空会话 %d 条，失败 %d 条", sample, parsed, empty, failed)
	if parsed == 0 {
		t.Fatal("一条有内容的会话都没解析出来")
	}
}
