package codex

import (
	"bufio"
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/Always1023/AgentMemory/internal/channel"
)

func TestDetectNotInstalledWhenRootMissing(t *testing.T) {
	got := Adapter{Root: filepath.Join(t.TempDir(), "not-there")}.Detect()
	if got.Status != channel.StatusNotInstalled {
		t.Fatalf("状态 = %q，想要 %q", got.Status, channel.StatusNotInstalled)
	}
	if got.Err != nil {
		t.Errorf("未安装不是错误: %v", got.Err)
	}
}

func TestDetectWalksNestedDateDirectories(t *testing.T) {
	root := t.TempDir()
	// 这个渠道按 年/月/日 分目录存放。
	for _, rel := range []string{
		filepath.Join("2026", "01", "05", "rollout-a.jsonl"),
		filepath.Join("2026", "01", "06", "rollout-b.jsonl"),
		filepath.Join("2025", "12", "31", "rollout-c.jsonl"),
	} {
		full := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte("{}\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	// 不是 jsonl 的不要。
	if err := os.WriteFile(filepath.Join(root, "2026", "01", "05", "notes.txt"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	got := Adapter{Root: root}.Detect()
	if got.Status != channel.StatusAvailable {
		t.Fatalf("状态 = %q，想要 %q（err=%v）", got.Status, channel.StatusAvailable, got.Err)
	}
	if len(got.Files) != 3 {
		t.Fatalf("发现 %d 个文件，想要 3 个: %+v", len(got.Files), got.Files)
	}
	if !strings.HasSuffix(got.Files[0].Path, filepath.Join("2025", "12", "31", "rollout-c.jsonl")) {
		t.Errorf("应当按路径排序，第一个是 %s", got.Files[0].Path)
	}
}

func parseFixture(t *testing.T, name string) channel.Session {
	t.Helper()
	path := filepath.Join("testdata", name)
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	session, err := Adapter{}.Parse(channel.SourceFile{Path: path, Size: info.Size(), ModTime: info.ModTime()})
	if err != nil {
		t.Fatalf("Parse(%s): %v", name, err)
	}
	return session
}

func TestParseProducesUnifiedSession(t *testing.T) {
	session := parseFixture(t, "session.jsonl")

	if session.Channel != ChannelID {
		t.Errorf("渠道 = %q，想要 %q", session.Channel, ChannelID)
	}
	if session.ID != "019b0000-1111-7222-8333-444455556666" {
		t.Errorf("会话 ID = %q，应当来自 session_meta", session.ID)
	}
	if session.ProjectPath != `D:\work\demo` {
		t.Errorf("项目路径 = %q，应当来自 session_meta 的 cwd", session.ProjectPath)
	}
	if session.Title != "帮我把这个项目的 README 补一下" {
		t.Errorf("标题 = %q，<environment_context> 那条不该当标题", session.Title)
	}
	want := time.Date(2026, 1, 5, 10, 0, 0, 0, time.UTC)
	if !session.StartedAt.Equal(want) {
		t.Errorf("开始时间 = %v，想要 %v", session.StartedAt, want)
	}

	// 2 条 user + 1 条带工具的 assistant + 1 条工具回执 + 1 条 assistant = 5 条。
	if len(session.Messages) != 5 {
		for i, m := range session.Messages {
			t.Logf("  消息 %d: role=%s text=%.30q tools=%d", i, m.Role, m.Text, len(m.Tools))
		}
		t.Fatalf("消息数 = %d，想要 5", len(session.Messages))
	}

	if m := session.Messages[0]; m.Role != channel.RoleUser || !strings.Contains(m.Text, "<environment_context>") {
		t.Errorf("第 1 条 = %+v", m)
	}
	if m := session.Messages[1]; m.Role != channel.RoleUser || m.Text != "帮我把这个项目的 README 补一下" {
		t.Errorf("第 2 条 = %+v", m)
	}
	m := session.Messages[2]
	if m.Role != channel.RoleAssistant || len(m.Tools) != 1 {
		t.Fatalf("第 3 条应当是带工具的 assistant: %+v", m)
	}
	if m.Tools[0].Name != "shell_command" || !strings.Contains(m.Tools[0].Input, "ls") {
		t.Errorf("工具调用 = %+v", m.Tools[0])
	}
	if m := session.Messages[3]; m.Role != channel.RoleTool || !strings.Contains(m.Text, "README.md") {
		t.Errorf("第 4 条应当是工具回执: %+v", m)
	}
	if m := session.Messages[4]; m.Role != channel.RoleAssistant || !strings.Contains(m.Text, "补了三段") {
		t.Errorf("第 5 条 = %+v", m)
	}
}

func TestParseSkipsReasoning(t *testing.T) {
	session := parseFixture(t, "session.jsonl")
	for _, m := range session.Messages {
		if strings.Contains(m.Text, "先看看目录里有什么") {
			t.Error("reasoning 内容不该进正文")
		}
	}
}

func TestParseRejectsHalfTruncatedFile(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("testdata", "session.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "half.jsonl")
	if err := os.WriteFile(path, raw[:len(raw)/2], 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := (Adapter{}).Parse(channel.SourceFile{Path: path}); err == nil {
		t.Fatal("截断一半的文件应当整份跳过")
	}
}

func TestParseRejectsBrokenLineAndSaysWhich(t *testing.T) {
	_, err := (Adapter{}).Parse(channel.SourceFile{Path: filepath.Join("testdata", "broken.jsonl")})
	if err == nil {
		t.Fatal("写坏的文件应当整份跳过")
	}
	if !strings.Contains(err.Error(), "第 2 行") {
		t.Errorf("错误应当指出坏在第几行: %v", err)
	}
}

// 空文件（一条消息都没有）是「空会话」：不当错误，交给编排层跳过（I-039）。
func TestParseMissingAndEmptyFiles(t *testing.T) {
	if _, err := (Adapter{}).Parse(channel.SourceFile{Path: filepath.Join(t.TempDir(), "nope.jsonl")}); err == nil {
		t.Error("文件不存在应当报错")
	}

	empty := filepath.Join(t.TempDir(), "empty.jsonl")
	if err := os.WriteFile(empty, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	session, err := (Adapter{}).Parse(channel.SourceFile{Path: empty})
	if err != nil {
		t.Errorf("空会话不该报错: %v", err)
	}
	if session.MessageCount() != 0 {
		t.Errorf("空会话不该有消息，实际 %d 条", session.MessageCount())
	}
	if session.Title == "" {
		t.Error("空会话也要有兜底标题")
	}
}

// 验收标准 3：只读打开，不写渠道目录。
func TestParseDoesNotTouchSourceFile(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("testdata", "session.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "s.jsonl")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := (Adapter{}).Parse(channel.SourceFile{Path: path}); err != nil {
		t.Fatalf("Parse: %v", err)
	}

	after, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if after.Size() != before.Size() || !after.ModTime().Equal(before.ModTime()) {
		t.Error("解析动了源文件")
	}
	now, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(now, raw) {
		t.Error("解析改了源文件内容")
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Errorf("渠道目录里多出了东西: %d 项", len(entries))
	}
}

// 验收标准 2：单个文件大于 10 MB 时流式读取，内存占用不随文件大小线性增长。
//
// 做法：造一个大于 10 MB 的文件，其中绝大多数是**会被跳过**的 reasoning 行。
// 若实现是一次性把文件读进内存（os.ReadFile 之类），堆占用会随文件大小线性上涨；
// 逐行流式读则几乎不涨。这个用例量的是后者。
func TestParseStreamsLargeFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "big.jsonl")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	w := bufio.NewWriter(f)

	write := func(v any) {
		raw, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write(append(raw, '\n')); err != nil {
			t.Fatal(err)
		}
	}

	write(map[string]any{
		"timestamp": "2026-01-05T10:00:00.000Z", "type": "session_meta",
		"payload": map[string]any{"session_id": "sid-big", "cwd": `D:\work\big`},
	})
	write(map[string]any{
		"timestamp": "2026-01-05T10:00:01.000Z", "type": "response_item",
		"payload": map[string]any{"type": "message", "role": "user",
			"content": []any{map[string]any{"type": "input_text", "text": "大文件的标题"}}},
	})

	// 每行约 1 KB，一万行 ≈ 10 MB，全部是会被跳过的 reasoning。
	noise := strings.Repeat("x", 1000)
	for i := 0; i < 11000; i++ {
		write(map[string]any{
			"timestamp": "2026-01-05T10:00:02.000Z", "type": "response_item",
			"payload": map[string]any{"type": "reasoning", "summary": []any{
				map[string]any{"type": "summary_text", "text": noise}}},
		})
	}
	if err := w.Flush(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() < 10<<20 {
		t.Fatalf("生成的文件只有 %.1f MB，不足以验证", float64(info.Size())/(1<<20))
	}

	runtime.GC()
	var before runtime.MemStats
	runtime.ReadMemStats(&before)

	session, err := Adapter{}.Parse(channel.SourceFile{Path: path, Size: info.Size()})
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	runtime.GC()
	var after runtime.MemStats
	runtime.ReadMemStats(&after)

	growth := int64(after.HeapAlloc) - int64(before.HeapAlloc)
	t.Logf("文件 %.1f MB → 解析后堆增长 %.2f MB，消息 %d 条",
		float64(info.Size())/(1<<20), float64(growth)/(1<<20), session.MessageCount())

	if growth > 4<<20 {
		t.Errorf("堆增长 %.1f MB：像是一次性把文件读进来了", float64(growth)/(1<<20))
	}
	if session.MessageCount() != 1 {
		t.Errorf("消息数 = %d，被跳过的行不该变成消息", session.MessageCount())
	}
}

// Probe 与 Parse 对「有没有消息」必须给出同一个答案（I-056）。
func TestProbeMatchesParse(t *testing.T) {
	path := filepath.Join("testdata", "session.jsonl")
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	sf := channel.SourceFile{Path: path, Size: info.Size(), ModTime: info.ModTime()}
	session, err := Adapter{}.Parse(sf)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	has, err := Adapter{}.Probe(sf)
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}
	if has != (session.MessageCount() > 0) {
		t.Errorf("Probe=%v 与 Parse 的 %d 条消息不一致", has, session.MessageCount())
	}

	empty := filepath.Join(t.TempDir(), "empty.jsonl")
	if err := os.WriteFile(empty, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	has, err = Adapter{}.Probe(channel.SourceFile{Path: empty})
	if err != nil || has {
		t.Errorf("空文件应当探测为没有消息，实际 has=%v err=%v", has, err)
	}
}
