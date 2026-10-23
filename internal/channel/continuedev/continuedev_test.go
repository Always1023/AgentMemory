package continuedev

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
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

func TestDetectSkipsIndexFileAndNonJSON(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"a.json", "b.json", "sessions.json", "notes.txt"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte("{}"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	// 子目录不算。
	if err := os.MkdirAll(filepath.Join(root, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}

	got := Adapter{Root: root}.Detect()
	if got.Status != channel.StatusAvailable {
		t.Fatalf("状态 = %q，想要 %q（err=%v）", got.Status, channel.StatusAvailable, got.Err)
	}
	if len(got.Files) != 2 {
		names := []string{}
		for _, f := range got.Files {
			names = append(names, filepath.Base(f.Path))
		}
		t.Fatalf("发现 %v，想要只有 a.json 与 b.json（sessions.json 是索引）", names)
	}
	for _, f := range got.Files {
		if filepath.Base(f.Path) == indexFileName {
			t.Errorf("索引文件不该当成会话: %s", f.Path)
		}
		if f.Size == 0 {
			t.Errorf("%s 的大小没填", f.Path)
		}
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
	session := parseFixture(t, "session.json")

	if session.Channel != ChannelID {
		t.Errorf("渠道 = %q，想要 %q", session.Channel, ChannelID)
	}
	if session.ID != "7933988c-2fd8-4bbd-9dda-baec8b81872f" {
		t.Errorf("会话 ID = %q", session.ID)
	}
	if session.Title != "项目结构梳理" {
		t.Errorf("标题 = %q，应当优先用会话文件自带的 title", session.Title)
	}
	if want := filepath.FromSlash("d:/Coding/ReactDeom"); session.ProjectPath != want {
		t.Errorf("项目路径 = %q，想要 %q", session.ProjectPath, want)
	}
	if !strings.HasSuffix(session.SourceFile, "session.json") {
		t.Errorf("源文件 = %q", session.SourceFile)
	}

	// 4 条历史记录里，两条是空内容，要被丢掉。
	if len(session.Messages) != 2 {
		for i, m := range session.Messages {
			t.Logf("  消息 %d: role=%s text=%q", i, m.Role, m.Text)
		}
		t.Fatalf("消息数 = %d，想要 2（空内容的要丢掉）", len(session.Messages))
	}
	if m := session.Messages[0]; m.Role != channel.RoleUser || m.Text != "帮我分析一下这个项目的结构" {
		t.Errorf("第 1 条 = %+v", m)
	}
	if m := session.Messages[1]; m.Role != channel.RoleAssistant || !strings.Contains(m.Text, "Next.js") {
		t.Errorf("第 2 条 = %+v", m)
	}
}

// 验收标准 2：workspaceDirectory 是 URL 编码路径时，能还原成可读项目路径。
func TestDecodeWorkspaceDir(t *testing.T) {
	cases := map[string]string{
		"file:///d%3A/Coding/ReactDeom": filepath.FromSlash("d:/Coding/ReactDeom"),
		"file:///home/me/proj":          filepath.FromSlash("/home/me/proj"),
		"":                              "",
		"D:\\Coding\\ReactDeom":         "D:\\Coding\\ReactDeom",
		"/plain/path":                   "/plain/path",
	}
	for raw, want := range cases {
		if got := decodeWorkspaceDir(raw); got != want {
			t.Errorf("decodeWorkspaceDir(%q) = %q，想要 %q", raw, got, want)
		}
	}

	// 百分号编码必须被还原，不能留着 %3A。
	if got := decodeWorkspaceDir("file:///d%3A/Coding/ReactDeom"); strings.Contains(got, "%3A") {
		t.Errorf("百分号编码没还原: %q", got)
	}
}

func TestParseFallsBackToFirstUserMessageForTitle(t *testing.T) {
	path := filepath.Join(t.TempDir(), "no-title.json")
	raw, err := json.Marshal(map[string]any{
		"sessionId":          "sid-no-title",
		"workspaceDirectory": "file:///d%3A/Coding/ReactDeom",
		"history": []any{
			map[string]any{"message": map[string]any{
				"role": "user", "content": []any{map[string]any{"type": "text", "text": "标题得从这句里来"}}}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}

	session, err := (Adapter{}).Parse(channel.SourceFile{Path: path})
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if session.Title != "标题得从这句里来" {
		t.Errorf("标题 = %q，没有 title 字段时应当退到第一条用户发言", session.Title)
	}
}

// 这个渠道的记录里没有时间戳，用源文件的修改时间兜底。
func TestParseUsesFileModTimeAsStart(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("testdata", "session.json"))
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "s.json")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	when := time.Date(2025, 3, 9, 12, 0, 0, 0, time.Local)
	if err := os.Chtimes(path, when, when); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}

	session, err := (Adapter{}).Parse(channel.SourceFile{Path: path, Size: info.Size(), ModTime: info.ModTime()})
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if session.StartedAt.IsZero() {
		t.Fatal("开始时间是零值——归档路径会把它挤到同一天")
	}
	if !session.StartedAt.Equal(when) {
		t.Errorf("开始时间 = %v，想要 %v", session.StartedAt, when)
	}
}

// 验收标准 3：文件损坏时跳过该文件并留日志（适配器这一侧是「整份跳过并报错」）。
func TestParseRejectsBrokenFile(t *testing.T) {
	info, err := os.Stat(filepath.Join("testdata", "broken.json"))
	if err != nil {
		t.Fatal(err)
	}
	_, err = (Adapter{}).Parse(channel.SourceFile{
		Path: filepath.Join("testdata", "broken.json"), Size: info.Size(), ModTime: info.ModTime(),
	})
	if err == nil {
		t.Fatal("写坏的会话文件应当整份跳过")
	}
	if !strings.Contains(err.Error(), "broken.json") {
		t.Errorf("错误里应当指出是哪个文件: %v", err)
	}
}

// 空文件与「没有消息的会话」都是空会话：不当错误，交给编排层跳过（I-039）。
func TestParseMissingEmptyAndNoHistory(t *testing.T) {
	if _, err := (Adapter{}).Parse(channel.SourceFile{Path: filepath.Join(t.TempDir(), "nope.json")}); err == nil {
		t.Error("文件不存在应当报错")
	}

	dir := t.TempDir()
	// 0 字节的整份 JSON：这不是「空会话」，是文件本身不完整——照旧报错。
	empty := filepath.Join(dir, "empty.json")
	if err := os.WriteFile(empty, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := (Adapter{}).Parse(channel.SourceFile{Path: empty}); err == nil {
		t.Error("0 字节的文件应当报错")
	}

	noHistory := filepath.Join(dir, "nohistory.json")
	if err := os.WriteFile(noHistory, []byte(`{"sessionId":"x","history":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if session, err := (Adapter{}).Parse(channel.SourceFile{Path: noHistory}); err != nil {
		t.Errorf("没有消息的会话不该报错: %v", err)
	} else if session.MessageCount() != 0 {
		t.Errorf("没有消息的会话该是 0 条消息，实际 %d 条", session.MessageCount())
	}

	garbage := filepath.Join(dir, "garbage.json")
	if err := os.WriteFile(garbage, []byte("not json at all"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := (Adapter{}).Parse(channel.SourceFile{Path: garbage}); err == nil {
		t.Error("乱码应当报错而不是猜")
	}
}

// 验收标准 3（前半）：超过上限的文件整份跳过，不硬读进来。
func TestParseRejectsOversizedFile(t *testing.T) {
	_, err := (Adapter{}).Parse(channel.SourceFile{
		Path: filepath.Join("testdata", "session.json"), Size: maxFileBytes + 1,
	})
	if err == nil {
		t.Fatal("超过上限的文件应当整份跳过")
	}
	if !strings.Contains(err.Error(), "上限") {
		t.Errorf("错误应当说明是超上限: %v", err)
	}
}

// 验收标准 4：只读打开，不写渠道目录。
func TestParseDoesNotTouchSourceFile(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("testdata", "session.json"))
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "s.json")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := (Adapter{}).Parse(channel.SourceFile{Path: path, Size: before.Size(), ModTime: before.ModTime()}); err != nil {
		t.Fatalf("Parse: %v", err)
	}

	after, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if after.Size() != before.Size() || !after.ModTime().Equal(before.ModTime()) {
		t.Errorf("解析动了源文件：大小 %d→%d，修改时间 %v→%v",
			before.Size(), after.Size(), before.ModTime(), after.ModTime())
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

func TestContentTextShapes(t *testing.T) {
	if got, err := contentText(json.RawMessage(`"就是一段话"`)); err != nil || got != "就是一段话" {
		t.Errorf("字符串形态: %q, %v", got, err)
	}
	blocks := `[{"type":"text","text":"第一段"},{"type":"image"},{"type":"text","text":"第二段"}]`
	if got, err := contentText(json.RawMessage(blocks)); err != nil || got != "第一段\n第二段" {
		t.Errorf("块数组形态: %q, %v", got, err)
	}
	if got, err := contentText(nil); err != nil || got != "" {
		t.Errorf("空内容: %q, %v", got, err)
	}
}

func TestRoleOf(t *testing.T) {
	cases := map[string]string{
		"user":      channel.RoleUser,
		"assistant": channel.RoleAssistant,
		"system":    channel.RoleSystem,
		"tool":      channel.RoleTool,
		"没见过的":      channel.RoleAssistant,
	}
	for in, want := range cases {
		if got := roleOf(in); got != want {
			t.Errorf("roleOf(%q) = %q，想要 %q", in, got, want)
		}
	}
}

func TestPackageNameIsNotAKeyword(t *testing.T) {
	// 记一笔：这个包不能叫 continue（Go 关键字），所以目录名是 continuedev，
	// 而渠道 ID 仍是 "continue"——对外标识与内部包名是两回事。
	if ChannelID != "continue" {
		t.Errorf("渠道 ID = %q，想要 %q", ChannelID, "continue")
	}
}
