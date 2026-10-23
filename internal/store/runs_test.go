package store

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// 加一行就多一行，已有行一个字都不动（I-030 验收标准 4）。
func TestAppendRunOnlyAdds(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 10, 16, 9, 0, 0, 0, time.Local)

	first := Run{At: now, Source: "手动", Archived: 2}
	if err := AppendRun(dir, first, 90, now); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(RunsPath(dir))
	if err != nil {
		t.Fatal(err)
	}

	second := Run{At: now.Add(time.Hour), Source: "定时", Archived: 5}
	if err := AppendRun(dir, second, 90, now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(RunsPath(dir))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(after), string(raw)) {
		t.Errorf("已有那行被改过了：\n%s\n=>\n%s", raw, after)
	}

	runs, err := ReadRuns(dir, 90, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 2 {
		t.Fatalf("应当读回 2 行，实际 %d", len(runs))
	}
	if runs[0].Source != "手动" || runs[1].Source != "定时" {
		t.Errorf("顺序该是从早到晚，实际 %+v", runs)
	}
	if runs[1].Archived != 5 {
		t.Errorf("数字要如实带回来，实际 %+v", runs[1])
	}
}

// 超过 90 天的行自动清掉，坏行跳过（验收标准 5）。
func TestRunsDropOldRowsAndBadLines(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 10, 16, 9, 0, 0, 0, time.Local)

	old := Run{At: now.AddDate(0, 0, -100), Source: "定时", Archived: 9}
	fresh := Run{At: now, Source: "手动", Archived: 1}
	if err := AppendRun(dir, old, 90, old.At); err != nil {
		t.Fatal(err)
	}
	// 手动塞一行坏的进去：坏行不该让整页打不开。
	f, err := os.OpenFile(RunsPath(dir), os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString("{这不是一行记录\n"); err != nil {
		t.Fatal(err)
	}
	_ = f.Close()
	if err := AppendRun(dir, fresh, 90, now); err != nil {
		t.Fatal(err)
	}

	runs, err := ReadRuns(dir, 90, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 1 || runs[0].Archived != 1 {
		t.Fatalf("只该剩下今天那行，实际 %+v", runs)
	}

	raw, err := os.ReadFile(RunsPath(dir))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(raw), "\n") != 1 {
		t.Errorf("文件里只该剩一行（老的 + 坏的都清掉），实际：\n%s", raw)
	}
}

// 按天合计：缺的天补零行、最近的在前（页面「一天一行」）。
func TestDailyRunsFillsGapsAndAddsUp(t *testing.T) {
	now := time.Date(2026, 10, 16, 21, 30, 0, 0, time.Local)
	runs := []Run{
		{At: now.Add(-3 * 24 * time.Hour), Source: "手动", Archived: 2},
		{At: now.Add(-2 * time.Hour), Source: "定时", Archived: 3, Backup: "3 个文件，已提交并推送"},
		{At: now.Add(-time.Hour), Source: "重试", Archived: 2, Failures: 1, Failed: true,
			Backup: "3 个文件", BackupFailed: true},
	}

	daily := DailyRuns(runs, now, 5)
	if len(daily) != 5 {
		t.Fatalf("要 5 行（一天一行），实际 %d", len(daily))
	}
	if daily[0].Day != "2026-10-16" || daily[4].Day != "2026-10-12" {
		t.Errorf("最近的在前，第一天是今天，实际 %s … %s", daily[0].Day, daily[4].Day)
	}
	if daily[0].Archived != 5 || daily[0].Runs != 2 || daily[0].Failures != 2 {
		t.Errorf("今天该是 收下 5 / 2 轮 / 失败 2 处（1 处跳过 + 1 轮没跑完），实际 %+v", daily[0])
	}
	if len(daily[0].Sources) != 2 || daily[0].Sources[0] != "定时" || daily[0].Sources[1] != "重试" {
		t.Errorf("今天两种来源都要记上，实际 %+v", daily[0].Sources)
	}
	if daily[0].Backups != 1 || daily[0].BackupFailures != 1 {
		t.Errorf("今天该是 1 次推送成功、1 次失败，实际 %+v", daily[0])
	}
	if daily[0].BackupNote != "3 个文件" || !daily[0].BackupNoteFailed {
		t.Errorf("当天有失败时，说明要留失败原因，实际 %+v", daily[0])
	}
	if daily[1].Runs != 0 || daily[1].Archived != 0 {
		t.Errorf("昨天没跑，该是零行，实际 %+v", daily[1])
	}
	if daily[3].Day != "2026-10-13" || daily[3].Archived != 2 {
		t.Errorf("三天前那天的数字没对上，实际 %+v", daily[3])
	}
}

// 一天里先失败、后成功：失败原因不能被后一次成功盖掉（I-053）。
func TestDailyRunsKeepsTheFailureNote(t *testing.T) {
	now := time.Date(2026, 10, 16, 21, 30, 0, 0, time.Local)
	runs := []Run{
		{At: now.Add(-2 * time.Hour), Backup: "推不上去", BackupFailed: true},
		{At: now.Add(-time.Hour), Backup: "3 个文件，已提交并推送"},
	}

	daily := DailyRuns(runs, now, 1)
	if daily[0].Backups != 1 || daily[0].BackupFailures != 1 {
		t.Errorf("该是 1 次成功、1 次失败，实际 %+v", daily[0])
	}
	if daily[0].BackupNote != "推不上去" || !daily[0].BackupNoteFailed {
		t.Errorf("失败原因要留着，实际 %+v", daily[0])
	}
}

// 落盘的目录给不出来时，如实报错而不是静默丢账。
func TestAppendRunRefusesAnEmptyDir(t *testing.T) {
	if err := AppendRun("  ", Run{}, 90, time.Now()); err == nil {
		t.Error("没有目录时该报错")
	}
	if err := AppendRun(filepath.Join(t.TempDir(), "没有的目录", "再一层"), Run{}, 90, time.Now()); err != nil {
		t.Errorf("目录不存在该自己建出来，实际 %v", err)
	}
}
