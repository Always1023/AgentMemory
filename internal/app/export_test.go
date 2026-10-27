package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Always1023/AgentMemory/internal/backup"
	"github.com/Always1023/AgentMemory/internal/channel"
	"github.com/Always1023/AgentMemory/internal/render"
	"github.com/Always1023/AgentMemory/internal/store"
)

// fixtureForExport 造一份覆盖面全的会话：中文、工具调用、无项目路径。
func fixtureForExport() channel.Session {
	at := time.Date(2026, 10, 27, 8, 0, 0, 0, time.UTC)
	return channel.Session{
		Channel:    "claude-code",
		ID:         "exp-1",
		Title:      "导出等价检查",
		StartedAt:  at,
		SourceFile: "C:\\fake\\exp-1.jsonl",
		Messages: []channel.Message{
			{Role: channel.RoleUser, Time: at, Text: "归档目录怎么换？"},
			{Role: channel.RoleAssistant, Time: at.Add(time.Minute), Text: "换目录时可以复制旧归档。",
				Tools: []channel.ToolCall{{Name: "read_file", Input: "config.go", Output: "ArchiveDir string"}}},
			{Role: channel.RoleUser, Time: at.Add(2 * time.Minute), Text: "网页版的归档也会动吗？"},
		},
	}
}

// A1：经「入库 → 回组」后的渲染与直连渲染逐字节一致。
func TestRenderFromL1MatchesDirectRender(t *testing.T) {
	db, err := store.OpenDB(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	session := fixtureForExport()
	if err := db.WriteSession(session, time.Now()); err != nil {
		t.Fatal(err)
	}
	got, ok, err := db.ReadSession(session.Channel, session.SourceFile, session.ID)
	if err != nil || !ok {
		t.Fatalf("回组: ok=%v, err=%v", ok, err)
	}
	direct := render.Render(session, "Claude Code")
	viaDB := render.Render(got, "Claude Code")
	if direct != viaDB {
		t.Fatalf("渲染不等价：\n直接 %q\n经库 %q", direct, viaDB)
	}
}

// A2：全量导出可重跑——跑两遍文件内容一致、数量一致（幂等覆盖）。
func TestRebuildExportsIsIdempotent(t *testing.T) {
	root := t.TempDir()
	archiveDir := filepath.Join(root, "归档")
	baseDir := filepath.Join(root, "数据")

	db, err := store.OpenDB(baseDir)
	if err != nil {
		t.Fatal(err)
	}
	session := fixtureForExport()
	if err := db.WriteSession(session, time.Now()); err != nil {
		t.Fatal(err)
	}
	db.Close()

	logger, _ := testLogger()
	cfg := testConfig(archiveDir)
	deps := testDeps(t, baseDir, backup.Result{})

	first, err := RebuildExports(cfg, logger, deps)
	if err != nil {
		t.Fatalf("第一次导出: %v", err)
	}
	if first != 1 {
		t.Fatalf("第一次导出 %d 个会话，要 1", first)
	}
	before := snapshotArchive(t, archiveDir)

	second, err := RebuildExports(cfg, logger, deps)
	if err != nil {
		t.Fatalf("第二次导出: %v", err)
	}
	if second != first {
		t.Fatalf("第二次导出 %d 个，与第一次 %d 不一致", second, first)
	}
	after := snapshotArchive(t, archiveDir)
	if len(before) != len(after) {
		t.Fatalf("重跑前后文件数变了: %d → %d", len(before), len(after))
	}
	for name, body := range before {
		if after[name] != body {
			t.Fatalf("重跑改动了 %s 的内容", name)
		}
	}
}

// snapshotArchive 把归档目录里的文件名与内容拍下来。
func snapshotArchive(t *testing.T, dir string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".md") {
			return nil
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		out[filepath.Base(path)] = string(raw)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}
