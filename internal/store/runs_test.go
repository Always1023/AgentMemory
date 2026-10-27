package store

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// marshalRunLine 把一条运行记录写成旧版 jsonl 的一行（只给导入测试用）。
func marshalRunLine(r Run) string {
	b, err := json.Marshal(r)
	if err != nil {
		panic(err)
	}
	return string(b) + "\n"
}

// openTestRuns 开一个临时主库给运行记录用。
func openTestRuns(t *testing.T) *DB {
	return openTestDB(t)
}

// 加一行就多一行，已有行一个字都不动（I-030 验收标准 4）。
func TestAppendRunOnlyAdds(t *testing.T) {
	db := openTestRuns(t)
	now := time.Date(2026, 10, 16, 9, 0, 0, 0, time.Local)

	first := Run{At: now, Source: "手动", Archived: 2}
	if err := db.AppendRun(first, 90, now); err != nil {
		t.Fatal(err)
	}
	afterFirst, err := db.ReadRuns(90, now)
	if err != nil {
		t.Fatal(err)
	}

	second := Run{At: now.Add(time.Hour), Source: "定时", Archived: 5}
	if err := db.AppendRun(second, 90, now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	runs, err := db.ReadRuns(90, now.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 2 {
		t.Fatalf("应当读回 2 行，实际 %d", len(runs))
	}
	// 已有那行一个字都没动。
	if runs[0].Source != afterFirst[0].Source || runs[0].Archived != afterFirst[0].Archived {
		t.Errorf("已有那行被改过了：%+v => %+v", afterFirst[0], runs[0])
	}
	if runs[0].Source != "手动" || runs[1].Source != "定时" {
		t.Errorf("顺序该是从早到晚，实际 %+v", runs)
	}
	if runs[1].Archived != 5 {
		t.Errorf("数字要如实带回来，实际 %+v", runs[1])
	}
}

// 旧 runs.jsonl 导入：坏行跳过；导入后再按口径清旧账（验收标准 5）。
func TestLegacyRunsImportSkipsBadLinesAndPrunes(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 10, 16, 9, 0, 0, 0, time.Local)

	old := Run{At: now.AddDate(0, 0, -100), Source: "定时", Archived: 9}
	fresh := Run{At: now, Source: "手动", Archived: 1}
	body := marshalRunLine(old) + "{这不是一行记录\n" + marshalRunLine(fresh)
	if err := os.WriteFile(filepath.Join(dir, RunsFileName), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	db, err := OpenDB(dir)
	if err != nil {
		t.Fatalf("打开主库: %v", err)
	}
	defer db.Close()

	// 导入后只有 fresh 一行能活下来：old 超过 90 天，坏行被跳过。
	runs, err := db.ReadRuns(90, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 1 || runs[0].Archived != 1 {
		t.Fatalf("只该剩下今天那行，实际 %+v", runs)
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
