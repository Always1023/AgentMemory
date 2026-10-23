package logging

import (
	"bytes"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSetupWritesConsoleAndFile(t *testing.T) {
	home := t.TempDir()
	t.Setenv("AGENTMEMORY_HOME", home)

	var console bytes.Buffer
	logger, closer, err := Setup(&console, slog.LevelInfo)
	if err != nil {
		t.Fatalf("Setup: %v", err)
	}
	logger.Info("hello", "k", "v")
	logger.Debug("不该出现")
	if err := closer.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	if !strings.Contains(console.String(), "hello") {
		t.Errorf("控制台没收到日志: %q", console.String())
	}
	if strings.Contains(console.String(), "不该出现") {
		t.Error("低于级别的日志不该输出")
	}

	raw, err := os.ReadFile(filepath.Join(home, "logs", FileName))
	if err != nil {
		t.Fatalf("读日志文件: %v", err)
	}
	text := string(raw)
	if !strings.Contains(text, "hello") || !strings.Contains(text, "k=v") {
		t.Errorf("日志文件内容不对: %q", text)
	}
}

func TestSetupAppendsAcrossRuns(t *testing.T) {
	home := t.TempDir()
	t.Setenv("AGENTMEMORY_HOME", home)

	for _, msg := range []string{"第一次", "第二次"} {
		logger, closer, err := Setup(&bytes.Buffer{}, slog.LevelInfo)
		if err != nil {
			t.Fatalf("Setup: %v", err)
		}
		logger.Info(msg)
		if err := closer.Close(); err != nil {
			t.Fatalf("Close: %v", err)
		}
	}

	raw, err := os.ReadFile(filepath.Join(home, "logs", FileName))
	if err != nil {
		t.Fatalf("读日志文件: %v", err)
	}
	text := string(raw)
	if !strings.Contains(text, "第一次") || !strings.Contains(text, "第二次") {
		t.Errorf("第二轮应当追加而不是截断: %q", text)
	}
}

func TestDirFollowsBaseDir(t *testing.T) {
	home := t.TempDir()
	t.Setenv("AGENTMEMORY_HOME", home)

	dir, err := Dir()
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(home, "logs")
	if dir != want {
		t.Errorf("日志目录 = %q，想要 %q", dir, want)
	}
}

// 控制台写不进去时（双击启动就是这种情形），文件那一份要照样拿到整行（I-020）。
func TestSetupKeepsTheFileWhenTheConsoleIsUnwritable(t *testing.T) {
	home := t.TempDir()
	t.Setenv("AGENTMEMORY_HOME", home)

	logger, closer, err := Setup(brokenWriter{}, slog.LevelInfo)
	if err != nil {
		t.Fatalf("Setup: %v", err)
	}
	logger.Info("这一行必须落进文件")
	if err := closer.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	raw, err := os.ReadFile(filepath.Join(home, "logs", FileName))
	if err != nil {
		t.Fatalf("读日志文件: %v", err)
	}
	if !strings.Contains(string(raw), "这一行必须落进文件") {
		t.Errorf("控制台不可写时，文件那一份也空了：%q", string(raw))
	}
}

// brokenWriter 假装一个写不进去的出口（资源管理器双击启动时的 os.Stderr）。
type brokenWriter struct{}

func (brokenWriter) Write(p []byte) (int, error) { return 0, errors.New("控制台不可写") }
