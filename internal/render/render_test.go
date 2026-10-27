package render

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Always1023/AgentMemory/internal/channel"
)

func sampleSession() channel.Session {
	start := time.Date(2026, 10, 9, 13, 50, 56, 0, time.UTC)
	return channel.Session{
		Channel:     "claude-code",
		ID:          "11111111-2222-3333-4444-555555555555",
		Title:       "项目结构梳理",
		ProjectPath: `D:\work\demo`,
		StartedAt:   start,
		SourceFile:  `C:\Users\Always\.claude\projects\D--work-demo\x.jsonl`,
		Messages: []channel.Message{
			{Role: channel.RoleUser, Time: start, Text: "帮我看一下这个项目的结构"},
			{Role: channel.RoleAssistant, Time: start.Add(4 * time.Second), Text: "这是一个 Go 项目。",
				Tools: []channel.ToolCall{{Name: "Bash", Input: `{"command":"ls -la"}`}}},
			{Role: channel.RoleTool, Time: start.Add(5 * time.Second), Text: "main.go\ndocs"},
			{Role: channel.RoleSystem, Time: start.Add(6 * time.Second), Text: "提醒一句"},
		},
	}
}

// fenceRun 数行首连续的反引号或波浪号个数——用来判断这行是不是围栏。
func fenceRun(line string) (char byte, n int) {
	line = strings.TrimSpace(line)
	if line == "" {
		return 0, 0
	}
	c := line[0]
	if c != '`' && c != '~' {
		return 0, 0
	}
	i := 0
	for i < len(line) && line[i] == c {
		i++
	}
	return c, i
}

// structuralHeadings 抽出文档里「真的算标题」的行——围栏内的不算。
// 这是验收标准 3 的判据：正文里的东西不该出现在这份清单里。
func structuralHeadings(md string) []string {
	var out []string
	var fenceChar byte
	var fenceLen int

	for _, line := range strings.Split(md, "\n") {
		c, n := fenceRun(line)
		if fenceChar == 0 {
			if n >= 3 {
				fenceChar, fenceLen = c, n
				continue
			}
			if trimmed := strings.TrimSpace(line); strings.HasPrefix(trimmed, "#") {
				out = append(out, trimmed)
			}
			continue
		}
		if c == fenceChar && n >= fenceLen {
			fenceChar = 0
		}
	}
	return out
}

// fencesBalanced 报告围栏是否成对闭合——没闭合说明正文把围栏撑破了。
func fencesBalanced(md string) bool {
	var fenceChar byte
	var fenceLen int
	for _, line := range strings.Split(md, "\n") {
		c, n := fenceRun(line)
		switch {
		case fenceChar == 0 && n >= 3:
			fenceChar, fenceLen = c, n
		case fenceChar != 0 && c == fenceChar && n >= fenceLen:
			fenceChar = 0
		}
	}
	return fenceChar == 0
}

// 验收标准 1：渲染结果含标题、渠道、项目路径、时间与全部消息正文。
func TestRenderCarriesAllFieldsAndBodies(t *testing.T) {
	md := Render(sampleSession(), "Claude Code")

	wants := []string{
		"# 项目结构梳理",
		"- 渠道：Claude Code（claude-code）",
		`- 项目：D:\work\demo`,
		"- 会话：11111111-2222-3333-4444-555555555555",
		"- 开始：2026-10-09 13:50:56",
		"- 消息：4 条",
		"- 来自：",
		"## 1 · 我 · 2026-10-09 13:50:56",
		"帮我看一下这个项目的结构",
		"## 2 · 助手 · 2026-10-09 13:51:00",
		"这是一个 Go 项目。",
		"**调用 · Bash**",
		`{"command":"ls -la"}`,
		"## 3 · 工具结果 · 2026-10-09 13:51:01",
		"main.go",
		"docs",
		"## 4 · 系统 · 2026-10-09 13:51:02",
		"提醒一句",
	}
	for _, want := range wants {
		if !strings.Contains(md, want) {
			t.Errorf("渲染结果里找不到 %q\n--- 全文 ---\n%s", want, md)
		}
	}
}

// 验收标准 2：渲染函数不碰磁盘，同一输入同一输出。
func TestRendererTouchesNoDiskAndUsesNoClock(t *testing.T) {
	source, err := os.ReadFile("render.go")
	if err != nil {
		t.Fatal(err)
	}
	src := string(source)

	// time 是允许的（格式化用），但时钟不许读：读了时钟，同一输入就未必同一输出。
	for _, forbidden := range []string{`"os"`, `"io"`, `"io/fs"`, `"bufio"`, `"path/filepath"`, "time.Now", "os.ReadFile", "os.WriteFile", "os.Stat"} {
		if strings.Contains(src, forbidden) {
			t.Errorf("render.go 里出现了 %s——渲染必须不碰磁盘、不读时钟", forbidden)
		}
	}
}

func TestRenderIsDeterministic(t *testing.T) {
	session := sampleSession()
	first := Render(session, "Claude Code")
	for i := 0; i < 20; i++ {
		if got := Render(session, "Claude Code"); got != first {
			t.Fatalf("第 %d 次渲染与第一次不一致", i+2)
		}
	}
}

// 验收标准 3：正文里的 Markdown 特殊字符不会破坏文档结构。
func TestBodyCannotBreakDocumentStructure(t *testing.T) {
	session := sampleSession()
	session.Messages[0].Text = strings.Join([]string{
		"# 我也是一个标题",
		"",
		"```",
		"我也带一个围栏",
		"```",
		"",
		"---",
		"",
		"***",
		"",
		"<div>",
		"没关的标签",
	}, "\n")

	md := Render(session, "Claude Code")

	wantPrefixes := []string{"# 项目结构梳理", "## 1 · 我", "## 2 · 助手", "## 3 · 工具结果", "## 4 · 系统"}
	got := structuralHeadings(md)
	if len(got) != len(wantPrefixes) {
		t.Fatalf("标题数 = %d，想要 %d\n实际：%v\n--- 全文 ---\n%s", len(got), len(wantPrefixes), got, md)
	}
	for i, prefix := range wantPrefixes {
		if !strings.HasPrefix(got[i], prefix) {
			t.Errorf("第 %d 个标题 = %q，应当以 %q 开头\n--- 全文 ---\n%s", i+1, got[i], prefix, md)
		}
	}
	if got[0] != "# 项目结构梳理" {
		t.Errorf("一级标题 = %q，正文里的那行不该顶掉它", got[0])
	}
	if !fencesBalanced(md) {
		t.Errorf("围栏没闭合\n--- 全文 ---\n%s", md)
	}
	if !strings.Contains(md, "# 我也是一个标题") {
		t.Error("正文里的那行标题应当原样留在围栏里，只是不算标题")
	}
}

func TestFenceGrowsToBeatTheBody(t *testing.T) {
	session := sampleSession()
	session.Messages[0].Text = "这里有五个反引号：````` 结束"

	md := Render(session, "Claude Code")
	if !fencesBalanced(md) {
		t.Fatalf("围栏没闭合\n%s", md)
	}
	if strings.Contains(md, "\n`````\n") {
		t.Error("围栏用回了正文里的长度，正文能把围栏关掉")
	}
	if !strings.Contains(md, "\n``````\n") {
		t.Errorf("围栏应当长到六连反引号\n%s", md)
	}
}

func TestFieldsFallBackToPlaceholders(t *testing.T) {
	md := Render(channel.Session{Messages: []channel.Message{{Role: channel.RoleUser, Text: "就一句话"}}}, "")

	if !strings.Contains(md, "# 未命名会话") {
		t.Errorf("没有标题时应当有兜底标题\n%s", md)
	}
	if !strings.Contains(md, "- 开始：—") {
		t.Errorf("零值时间应当给占位，而不是 0001-01-01\n%s", md)
	}
	if !strings.Contains(md, "- 项目：—") {
		t.Errorf("空项目路径应当给占位\n%s", md)
	}
}

func TestMultilineValuesAreFlattened(t *testing.T) {
	md := Render(channel.Session{
		Channel:     "x",
		ID:          "sid",
		Title:       "标题\n第二行",
		ProjectPath: "D:\\a\nD:\\b",
		Messages:    []channel.Message{{Role: channel.RoleUser, Text: "正文"}},
	}, "渠道\n名字")

	for _, line := range []string{"# 标题 第二行", "- 渠道：渠道 名字（x）", `- 项目：D:\a D:\b`} {
		if !strings.Contains(md, line) {
			t.Errorf("多行值没被压成一行，找不到 %q\n%s", line, md)
		}
	}
}

func TestUnknownRoleIsKeptNotDropped(t *testing.T) {
	md := Render(channel.Session{
		Messages: []channel.Message{{Role: "narrator", Text: "旁白"}},
	}, "")
	if !strings.Contains(md, "## 1 · narrator") || !strings.Contains(md, "旁白") {
		t.Errorf("没见过的角色应当原样显示，而不是丢掉\n%s", md)
	}
}

func TestRenderEndsWithNewline(t *testing.T) {
	md := Render(sampleSession(), "Claude Code")
	if !strings.HasSuffix(md, "\n") {
		t.Error("渲染结果应当以换行结尾，拼进别的文件时才不会粘行")
	}
}
