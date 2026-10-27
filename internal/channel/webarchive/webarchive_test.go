package webarchive

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Always1023/AgentMemory/internal/channel"
	"github.com/Always1023/AgentMemory/internal/store/storetest"
)

// fixtureMD 是一份插件产出的样本（与 extension/lib/render.js 的形态一致）。
const fixtureMD = "# 深聊归档方案\n" +
	"\n" +
	"- 渠道：DeepSeek\n" +
	"- 项目：—\n" +
	"- 会话：web-123\n" +
	"- 开始：2026-10-26 21:30:00\n" +
	"- 消息：3 条\n" +
	"- 来自：https://chat.example.com/s/web-123\n" +
	"- 保存：2026-10-27 09:00:00\n" +
	"\n" +
	"## 1 · 我 · 2026-10-26 21:30:00\n" +
	"\n" +
	"```plain\n" +
	"归档目录怎么换？\n" +
	"```\n" +
	"\n" +
	"## 2 · 助手 · 2026-10-26 21:31:00\n" +
	"\n" +
	"`````text\n" +
	"换目录时可以复制旧归档；正文里有 ``` 也不怕。\n" +
	"`````\n" +
	"\n" +
	"## 3 · 我\n" +
	"\n"

func writeFixture(t *testing.T, dir, name, body string) channel.SourceFile {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return channel.SourceFile{Path: path, Size: info.Size(), ModTime: info.ModTime()}
}

// Parse：标题、会话 ID、开始时间、三条消息（角色、时间、正文）都对得上；
// 正文里的长围栏不切断；空正文的消息保留为空。
func TestParseReadsExtensionMarkdown(t *testing.T) {
	dir := t.TempDir()
	sf := writeFixture(t, dir, "web-123.md", fixtureMD)

	session, err := Adapter{}.Parse(sf)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if session.Channel != ChannelID || session.ID != "web-123" || session.Title != "深聊归档方案" {
		t.Fatalf("会话头不对: %+v", session)
	}
	if session.StartedAt.Format("2006-01-02 15:04:05") != "2026-10-26 21:30:00" {
		t.Fatalf("开始时间不对: %v", session.StartedAt)
	}
	if len(session.Messages) != 3 {
		t.Fatalf("消息 %d 条，要 3 条", len(session.Messages))
	}
	if session.Messages[0].Role != channel.RoleUser || session.Messages[0].Text != "归档目录怎么换？" {
		t.Fatalf("第 1 条不对: %+v", session.Messages[0])
	}
	if session.Messages[1].Role != channel.RoleAssistant ||
		!strings.Contains(session.Messages[1].Text, "``` 也不怕") {
		t.Fatalf("第 2 条不对: %+v", session.Messages[1])
	}
	if session.Messages[2].Text != "" || session.Messages[2].Time != (time.Time{}) {
		t.Fatalf("空正文消息应保留为空、无时间: %+v", session.Messages[2])
	}
}

// 元信息缺失：开始时间退回文件修改时间，会话 ID 退回文件名（去掉 .md）。
func TestParseFallsBackWhenHeaderMissing(t *testing.T) {
	dir := t.TempDir()
	body := "# 没头没尾\n\n## 1 · 我\n\n```\n就一句\n```\n"
	sf := writeFixture(t, dir, "fallback-name.md", body)

	session, err := Adapter{}.Parse(sf)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if session.ID != "fallback-name" {
		t.Errorf("会话 ID 应退回文件名，实际 %q", session.ID)
	}
	if !session.StartedAt.Equal(sf.ModTime) {
		t.Errorf("开始时间应退回文件修改时间，实际 %v", session.StartedAt)
	}
	if session.Title != "没头没尾" || session.Messages[0].Text != "就一句" {
		t.Errorf("标题与正文不对: %+v", session)
	}
}

// Detect：目录在 → 可用并列出 md；目录不在 → 未安装；注入归档目录后能找到。
func TestDetectStatesFollowArchiveRoot(t *testing.T) {
	// 目录不在：未安装。
	if got := (Adapter{}).Detect(); got.Status != channel.StatusNotInstalled {
		t.Fatalf("空环境应报未安装，实际 %s", got.Status)
	}

	archive := t.TempDir()
	webDir := filepath.Join(archive, webDirName)
	if err := os.MkdirAll(filepath.Join(webDir, "deepseek"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFixture(t, filepath.Join(webDir, "deepseek"), "web-123.md", fixtureMD)

	// 注入归档目录（编排层的路子）。
	adapter := &Adapter{}
	adapter.SetArchiveRoot(archive)
	got := adapter.Detect()
	if got.Status != channel.StatusAvailable || len(got.Files) != 1 {
		t.Fatalf("注入后应可用且发现 1 份，实际 %s / %d 份", got.Status, len(got.Files))
	}

	// 目录在但为空：可用、0 份（界面说「已安装无会话」）。
	empty := t.TempDir()
	if err := os.MkdirAll(filepath.Join(empty, webDirName), 0o755); err != nil {
		t.Fatal(err)
	}
	emptyAdapter := &Adapter{}
	emptyAdapter.SetArchiveRoot(empty)
	if got := emptyAdapter.Detect(); got.Status != channel.StatusAvailable || len(got.Files) != 0 {
		t.Fatalf("空目录应可用 0 份，实际 %s / %d", got.Status, len(got.Files))
	}
}

// A1：同一份网页归档重复入库零重复（导入与桌面收集共用同一入库管线）。
func TestIngestIsIdempotent(t *testing.T) {
	dir := t.TempDir()
	sf := writeFixture(t, dir, "web-123.md", fixtureMD)
	session, err := Adapter{}.Parse(sf)
	if err != nil {
		t.Fatal(err)
	}

	db := storetest.Open(t)
	if err := db.WriteSession(session, time.Now()); err != nil {
		t.Fatalf("第一次入库: %v", err)
	}
	if err := db.WriteSession(session, time.Now()); err != nil {
		t.Fatalf("重复入库: %v", err)
	}

	// 只有一个会话条目（重复入库没有开出第二个 revision）。
	sessions, err := db.LatestSessions()
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 1 {
		t.Fatalf("入库后应有 1 个会话条目，实际 %d", len(sessions))
	}

	got, ok, err := db.ReadSession(session.Channel, session.SourceFile, session.ID)
	if err != nil || !ok {
		t.Fatalf("回组: ok=%v, err=%v", ok, err)
	}
	if len(got.Messages) != 3 {
		t.Fatalf("回组 %d 条消息，要 3 条", len(got.Messages))
	}
	if !reflect.DeepEqual(got, session) {
		t.Fatalf("回组与原模型不一致")
	}
}
