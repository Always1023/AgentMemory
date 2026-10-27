package dsh

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Always1023/AgentMemory/internal/channel"
	"github.com/klauspost/compress/zstd"
)

// writeSession 按 dsh 的目录形态造一份会话：<root>/<工作区>/<会话>/session.v4.jsonl.zstd。
func writeSession(t *testing.T, root, workspace, session string, lines []string) string {
	t.Helper()
	dir := filepath.Join(root, workspace, session)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "session.v4.jsonl.zstd")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	enc, err := zstd.NewWriter(f)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := enc.Write([]byte(strings.Join(lines, "\n") + "\n")); err != nil {
		t.Fatal(err)
	}
	if err := enc.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}

func fixtureLines() []string {
	return []string{
		`{"type":"session","id":"session-abc","cwd":"D:\\work\\demo","version":4,"createdAt":1792993466667}`,
		`{"seq":0,"time":1792993466673,"type":"permission/preset","data":{"preset":"standard"}}`,
		`{"seq":1,"time":1792993466800,"type":"agent/inbox/spliced","data":{"start":0,"target":"next-turn","inserted":[{"id":"m1","role":"user","content":[{"type":"text","text":"帮我装一下插件"}]},{"id":"m2","role":"user","content":[{"type":"text","text":"另外看一下版本"}]}]}}`,
		`{"seq":2,"time":1792993467000,"type":"session/title","data":{"title":"安装插件并确认版本","messageSeqs":[1]}}`,
		`{"seq":3,"time":1792993468000,"type":"assistant/message","data":{"message":{"id":"a1","role":"assistant","content":[{"type":"reasoning","text":"先想想"},{"type":"text","text":"好，我先看版本"},{"type":"tool-call","id":"call-1","name":"bash","arguments":"{\"cmd\":\"dsh --version\"}"}]},"step":0,"turn":0}}`,
		`{"seq":4,"time":1792993469000,"type":"tool/result","data":{"id":"r1","role":"tool","toolCallId":"call-1","isError":false,"content":[{"type":"text","text":"0.2.1"}]}}`,
		`{"seq":5,"time":1792993470000,"type":"system/message","data":{"id":"s1","role":"system","content":[{"type":"text","text":"系统提示词"}]}}`,
	}
}

func TestDetectFindsSessions(t *testing.T) {
	root := t.TempDir()
	writeSession(t, root, "--D-work-demo--", "session-abc", fixtureLines())

	adapter := Adapter{Root: root}
	got := adapter.Detect()
	if got.Status != channel.StatusAvailable {
		t.Fatalf("状态 = %s，要可用", got.Status)
	}
	if len(got.Files) != 1 {
		t.Fatalf("应当发现 1 个会话，实际 %d", len(got.Files))
	}
	if !strings.HasSuffix(got.Files[0].Path, "session.v4.jsonl.zstd") {
		t.Fatalf("文件不对: %s", got.Files[0].Path)
	}
}

func TestDetectStatesWhenNoData(t *testing.T) {
	root := t.TempDir()
	if got := (Adapter{Root: root}).Detect(); got.Status != channel.StatusAvailable || len(got.Files) != 0 {
		t.Fatalf("空目录应当是「可用但没有会话」: %+v", got)
	}
	missing := filepath.Join(root, "nope")
	if got := (Adapter{Root: missing}).Detect(); got.Status != channel.StatusNotInstalled {
		t.Fatalf("目录不存在应当是未安装: %s", got.Status)
	}
}

func TestParseSession(t *testing.T) {
	root := t.TempDir()
	path := writeSession(t, root, "--D-work-demo--", "session-abc", fixtureLines())

	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	session, err := (Adapter{Root: root}).Parse(channel.SourceFile{
		Path: path, Size: info.Size(), ModTime: info.ModTime(),
	})
	if err != nil {
		t.Fatalf("解析: %v", err)
	}

	if session.Channel != ChannelID || session.ID != "session-abc" {
		t.Fatalf("身份不对: %+v", session)
	}
	if session.Title != "安装插件并确认版本" {
		t.Fatalf("标题不对: %q", session.Title)
	}
	if session.ProjectPath != `D:\work\demo` {
		t.Fatalf("项目路径不对: %q", session.ProjectPath)
	}
	if !session.StartedAt.Equal(time.UnixMilli(1792993466667)) {
		t.Fatalf("开始时间不对: %v", session.StartedAt)
	}

	// 两条用户消息 + 一条助手消息；系统消息与思考不算。
	if len(session.Messages) != 3 {
		t.Fatalf("消息数 = %d，要 3: %+v", len(session.Messages), session.Messages)
	}
	if session.Messages[0].Role != channel.RoleUser || session.Messages[0].Text != "帮我装一下插件" {
		t.Fatalf("第一条用户消息不对: %+v", session.Messages[0])
	}
	assistant := session.Messages[2]
	if assistant.Role != channel.RoleAssistant || assistant.Text != "好，我先看版本" {
		t.Fatalf("助手消息不对: %+v", assistant)
	}
	if strings.Contains(assistant.Text, "先想想") {
		t.Fatalf("思考过程不该进正文: %q", assistant.Text)
	}
	if len(assistant.Tools) != 1 || assistant.Tools[0].Name != "bash" || !strings.Contains(assistant.Tools[0].Input, "dsh --version") {
		t.Fatalf("工具调用不对: %+v", assistant.Tools)
	}
	if assistant.Tools[0].Output != "0.2.1" {
		t.Fatalf("工具结果没回填: %+v", assistant.Tools[0])
	}
}

func TestParseRejectsOversizeFile(t *testing.T) {
	adapter := Adapter{Root: t.TempDir()}
	_, err := adapter.Parse(channel.SourceFile{Path: "whatever.zstd", Size: maxCompressed + 1})
	if err == nil || !strings.Contains(err.Error(), "太大") {
		t.Fatalf("超大文件应当拒绝: %v", err)
	}
}

func TestParseReportsCorruptFile(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "ws", "session-bad")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "session.v4.jsonl.zstd")
	if err := os.WriteFile(path, []byte("这不是 zstd"), 0o644); err != nil {
		t.Fatal(err)
	}
	info, _ := os.Stat(path)
	if _, err := (Adapter{Root: root}).Parse(channel.SourceFile{Path: path, Size: info.Size(), ModTime: info.ModTime()}); err == nil {
		t.Fatal("坏文件应当报错（按数据错记账，不静默）")
	}
}
