package main

import (
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Always1023/AgentMemory/internal/app"
	"github.com/Always1023/AgentMemory/internal/backup/none"
	"github.com/Always1023/AgentMemory/internal/store"
)

// 收一轮就落一行账：今天的行在最上面，数字与那一轮对得上（I-030 验收标准 1、3）。
func TestRunLogAddsUpToday(t *testing.T) {
	home := useTempHome(t)
	archive := filepath.Join(home, "归档")
	if err := os.MkdirAll(archive, 0o755); err != nil {
		t.Fatal(err)
	}
	readyConfig(t, archive)

	ui := newTestApp(t, fakeChannel("fake", "假渠道", fakeSources(t, 3)))
	state, err := ui.CollectNow()
	if err != nil {
		t.Fatalf("CollectNow: %v", err)
	}

	rows, err := ui.RunLog()
	if err != nil {
		t.Fatalf("RunLog: %v", err)
	}
	if len(rows) != runLogListDays {
		t.Fatalf("日志页列最近 %d 天、一天一行，实际 %d 行", runLogListDays, len(rows))
	}
	today := rows[0]
	if today.Label != "今天" {
		t.Errorf("最近的在前、今天在最上面，实际 %q", today.Label)
	}
	if today.Source != runSourceManual {
		t.Errorf("手动收的那轮要标成「手动」，实际 %q", today.Source)
	}
	if today.Runs != 1 {
		t.Errorf("今天跑了一轮，实际 %d", today.Runs)
	}
	// 「今天收下多少」与渠道那一段的数同口径：直接对那一轮的数字。
	if state.LastRun == nil {
		t.Fatal("收完应当留下这一轮的结果")
	}
	if today.Archived != state.LastRun.Archived {
		t.Errorf("「今天收下 %d 段」与这一轮收下的 %d 段对不上", today.Archived, state.LastRun.Archived)
	}
	if rows[1].Runs != 0 || rows[1].Archived != 0 {
		t.Errorf("昨天没跑，该是零行，实际 %+v", rows[1])
	}
}

// 定时那一路（命令行）也落账，来源标成「定时」（I-030 验收标准 2）。
func TestScheduledRoundIsRecordedAsScheduled(t *testing.T) {
	home := useTempHome(t)
	if err := os.MkdirAll(filepath.Join(home, "归档"), 0o755); err != nil {
		t.Fatal(err)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	recordRun(logger, runFrom(sampleResult()), runSourceSchedule)

	rows, err := newTestApp(t).RunLog()
	if err != nil {
		t.Fatal(err)
	}
	if rows[0].Source != runSourceSchedule || rows[0].Archived != 2 {
		t.Errorf("今天那行该是「定时 / 收下 2 段」，实际 %+v", rows[0])
	}
}

// 账写不成也不影响收集本身：只记一条日志，不 panic、不删不毁（验收标准 4）。
func TestRecordRunSurvivesAWriteFailure(t *testing.T) {
	home := useTempHome(t)
	// 把记录的位置占成一个目录：写进去必定失败。
	if err := os.MkdirAll(filepath.Join(home, store.RunsFileName), 0o755); err != nil {
		t.Fatal(err)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	recordRun(logger, UIRun{Archived: 1}, runSourceManual)

	if info, err := os.Stat(filepath.Join(home, store.RunsFileName)); err != nil || !info.IsDir() {
		t.Fatalf("那一步不该动这个位置：info=%v err=%v", info, err)
	}
}

// 日志页那一行的写法：今天 / 昨天 / 10-16 周五（一天一行，看得出是哪天）。
func TestDayLabels(t *testing.T) {
	if got := dayLabel("2026-10-16"); got != "10-16 周五" {
		t.Errorf("dayLabel(2026-10-16) = %q，想要 10-16 周五", got)
	}
	if got := dayLabel("坏日子"); got != "坏日子" {
		t.Errorf("认不出就照原样，实际 %q", got)
	}
}

// 备份那一步的情况也进记录：定时那一路有没有把改动推上去，日志里看得见（I-030）。
func TestRunLogRecordsTheBackup(t *testing.T) {
	home := useTempHome(t)
	if err := os.MkdirAll(filepath.Join(home, "归档"), 0o755); err != nil {
		t.Fatal(err)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	// sampleResult 里那一步是「3 个文件，已提交并推送」。
	recordRun(logger, runFrom(sampleResult()), runSourceSchedule)

	rows, err := newTestApp(t).RunLog()
	if err != nil {
		t.Fatal(err)
	}
	if rows[0].Backups != 1 || rows[0].BackupFailures != 0 {
		t.Errorf("今天该记下 1 次推送，实际 %+v", rows[0])
	}
	if rows[0].BackupNote != "3 个文件，已提交并推送" || rows[0].BackupNoteFailed {
		t.Errorf("成功说明没记上，实际 %+v", rows[0])
	}

	// 推失败的那一次也要记上（东西还在本机），并且留下可读原因。
	recordRun(logger, UIRun{BackupDetail: "3 个文件", BackupErr: "推不上去"}, runSourceSchedule)

	rows, err = newTestApp(t).RunLog()
	if err != nil {
		t.Fatal(err)
	}
	if rows[0].Backups != 1 || rows[0].BackupFailures != 1 {
		t.Errorf("该是 1 次成功、1 次失败，实际 %+v", rows[0])
	}
	if !rows[0].BackupNoteFailed || !strings.Contains(rows[0].BackupNote, "推不上去") {
		t.Errorf("失败原因要进日志页，实际 %+v", rows[0])
	}
}

// 「仅本地」不是一次备份：日志里不记次数，也不写备份说明（I-053）。
func TestRunLogDoesNotCountLocalOnlyAsBackup(t *testing.T) {
	home := useTempHome(t)
	if err := os.MkdirAll(filepath.Join(home, "归档"), 0o755); err != nil {
		t.Fatal(err)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	result := sampleResult()
	result.Backup = app.BackupResult{Method: none.ID, Detail: none.Detail}

	recordRun(logger, runFrom(result), runSourceSchedule)

	rows, err := newTestApp(t).RunLog()
	if err != nil {
		t.Fatal(err)
	}
	if rows[0].Backups != 0 || rows[0].BackupFailures != 0 || rows[0].BackupNote != "" {
		t.Errorf("仅本地不该被记成一次备份，实际 %+v", rows[0])
	}
}
