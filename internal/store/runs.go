package store

// 这个文件是「运行记录」（I-030 / PRD F11）：一行一次收集，只追加。
//
// 与日志文件分工不同：日志是给人读的、不保证好解析；这一份是结构化的，
// 界面按天读回去，回答「定时任务那天跑没跑、收了多少」。
// 存储在主库 runs 表；旧 runs.jsonl 只在首次导入时读一次。
//
// 依赖方向与水位一致：只依赖标准库，主库由调用方传入（不 import config）。

import (
	"bufio"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// RunsFileName 是旧运行记录的文件名；升级后只读导入一次。
const RunsFileName = "runs.jsonl"

// dayLayout 是记录里「一天」的写法。
const dayLayout = "2006-01-02"

// Run 是一次收集的结果，一行一条。
type Run struct {
	// At 是这一轮开始的时间（本地时区）。
	At time.Time `json:"at"`
	// Source 是这一轮是谁发起的：定时 / 手动 / 重试。
	Source string `json:"source"`
	// Scope 是重试时的渠道名；收全部时为空。
	Scope string `json:"scope,omitempty"`
	// Found / New 是这一轮看到的与新增的会话文件数；Archived 是**收下**的份数
	//（与界面「已收集」同口径）。
	Found    int `json:"found"`
	New      int `json:"new"`
	Archived int `json:"archived"`
	// Failures 是跳过的条数；Failed 为真表示这一轮整轮没跑完。
	Failures int  `json:"failures"`
	Failed   bool `json:"failed"`
	// Empty 是这一轮跳过的「空会话」数（打开过没说话的那些）：既不是失败，
	// 也没有收下——界面上单独说一句「跳过 N 个空会话」（I-039）。
	Empty int `json:"empty,omitempty"`
	// Backup 是备份那一步的一句话（形如「3 个文件，已提交并推送」）；
	// BackupFailed 为真表示这一步没成（东西还在本机）。人要看的就是这个：
	// 定时那一路到底有没有把改动推上去（I-030）。
	Backup       string `json:"backup,omitempty"`
	BackupFailed bool   `json:"backupFailed,omitempty"`
}

// AppendRun 添一行运行记录，顺手把太老的行清掉。
//
// 只追加：已有行不会被改（I-030 验收标准 4）。返回错误由调用方决定怎么办——
// 记录是给人看的账，不该因为它没写成而让收集本身失败。
func (d *DB) AppendRun(run Run, keepDays int, now time.Time) error {
	if keepDays < 1 {
		keepDays = 1
	}
	tx, err := d.handle.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.Exec(`INSERT INTO runs
		(at, source, scope, found, new, archived, failures, failed, empty, backup, backup_failed)
		VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		fmtTime(run.At), run.Source, run.Scope, run.Found, run.New, run.Archived,
		run.Failures, boolToInt(run.Failed), run.Empty, run.Backup, boolToInt(run.BackupFailed)); err != nil {
		return err
	}
	cut := cutoff(keepDays, now)
	if _, err := tx.Exec(`DELETE FROM runs WHERE at < ?`, fmtTime(cut)); err != nil {
		return err
	}
	return tx.Commit()
}

// ReadRuns 读回 keepDays 天以内的行，按时间从早到晚排好。
func (d *DB) ReadRuns(keepDays int, now time.Time) ([]Run, error) {
	cut := cutoff(keepDays, now)
	rows, err := d.handle.Query(`SELECT at, source, scope, found, new, archived, failures, failed, empty, backup, backup_failed
		FROM runs WHERE at >= ? ORDER BY at`, fmtTime(cut))
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]Run, 0, 64)
	for rows.Next() {
		var run Run
		var at string
		var failed, backupFailed int
		if err := rows.Scan(&at, &run.Source, &run.Scope, &run.Found, &run.New, &run.Archived,
			&run.Failures, &failed, &run.Empty, &run.Backup, &backupFailed); err != nil {
			return nil, err
		}
		run.At = parseTime(at)
		run.Failed = failed != 0
		run.BackupFailed = backupFailed != 0
		out = append(out, run)
	}
	return out, rows.Err()
}

// DailyRun 是某一天的合计，界面一天一行。
type DailyRun struct {
	// Day 是这一天，写成 2006-01-02。
	Day string
	// Archived 是那天收下的份数；Runs 是那天跑了几轮；Failures 是那天失败了几处
	//（整轮没跑完也算一处）。
	Archived int
	Runs     int
	Failures int
	// Sources 是那天出现过的来源，按先后排（定时 / 手动 / 重试）。
	Sources []string
	// Backups 是那天备份成功的次数，BackupFailures 是没成的次数。
	Backups        int
	BackupFailures int
	// BackupNote 是那天最近一次备份的说明；BackupNoteFailed 为真时它是失败原因。
	// 一天里只要有过失败，就保留最近一次失败原因，不被后面的成功盖掉（I-053）。
	BackupNote       string
	BackupNoteFailed bool
}

// DailyRuns 把记录按天合计，recentDays 天里**一天一行**、最近的在前。
//
// 没跑的那天也占一行（数字是零）：定时的意义就是「那天到底跑没跑」，
// 缺行和零行在界面上要被区分开——所以缺的天照补。
func DailyRuns(runs []Run, now time.Time, recentDays int) []DailyRun {
	if recentDays < 1 {
		recentDays = 1
	}

	type day = Total
	byDay := map[string]*day{}
	for _, run := range runs {
		key := run.At.Format(dayLayout)
		one := byDay[key]
		if one == nil {
			one = &day{}
			byDay[key] = one
		}
		one.Archived += run.Archived
		one.Runs++
		one.Failures += run.Failures
		if run.Failed {
			one.Failures++
		}
		if src := strings.TrimSpace(run.Source); src != "" && !hasString(one.Sources, src) {
			one.Sources = append(one.Sources, src)
		}
		if strings.TrimSpace(run.Backup) != "" {
			if run.BackupFailed {
				one.BackupFailures++
				one.BackupNote = run.Backup
				one.BackupNoteFailed = true
			} else {
				one.Backups++
				if !one.BackupNoteFailed {
					one.BackupNote = run.Backup
				}
			}
		}
	}

	start := dayStart(now)
	out := make([]DailyRun, 0, recentDays)
	for i := 0; i < recentDays; i++ {
		at := start.AddDate(0, 0, -i)
		row := DailyRun{Day: at.Format(dayLayout)}
		if one := byDay[row.Day]; one != nil {
			row.Archived = one.Archived
			row.Runs = one.Runs
			row.Failures = one.Failures
			row.Sources = one.Sources
			row.Backups = one.Backups
			row.BackupFailures = one.BackupFailures
			row.BackupNote = one.BackupNote
			row.BackupNoteFailed = one.BackupNoteFailed
		}
		out = append(out, row)
	}
	return out
}

// Total 是某一天的合计（与 DailyRun 同形，只是不带日期标签）。
type Total struct {
	Archived         int
	Runs             int
	Failures         int
	Sources          []string
	Backups          int
	BackupFailures   int
	BackupNote       string
	BackupNoteFailed bool
}

// cutoff 是「还留着」的最早一刻：keepDays 天里的第一天零点。
func cutoff(keepDays int, now time.Time) time.Time {
	if keepDays < 1 {
		keepDays = 1
	}
	return dayStart(now).AddDate(0, 0, -(keepDays - 1))
}

// dayStart 取那天的零点（本地时区）。
func dayStart(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, t.Location())
}

// hasString 报告列表里有没有这一项。
func hasString(list []string, want string) bool {
	for _, one := range list {
		if one == want {
			return true
		}
	}
	return false
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// readLegacyRuns 读旧 runs.jsonl。文件不在返回 ok = false；
// 一条读不懂的行跳过就好——导入是搬账，不是校对。
func readLegacyRuns(dir string) (runs []Run, ok bool, err error) {
	f, readErr := os.Open(filepath.Join(dir, RunsFileName))
	if readErr != nil {
		if errors.Is(readErr, os.ErrNotExist) {
			return nil, false, nil
		}
		return nil, false, readErr
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var run Run
		if json.Unmarshal([]byte(line), &run) != nil {
			continue
		}
		runs = append(runs, run)
	}
	return runs, true, scanner.Err()
}

// importLegacyRuns 把旧运行记录搬进主库（只在 runs 表为空时做一次）。
func (d *DB) importLegacyRuns(runs []Run) error {
	tx, err := d.handle.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	for _, run := range runs {
		if _, err := tx.Exec(`INSERT INTO runs
			(at, source, scope, found, new, archived, failures, failed, empty, backup, backup_failed)
			VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			fmtTime(run.At), run.Source, run.Scope, run.Found, run.New, run.Archived,
			run.Failures, boolToInt(run.Failed), run.Empty, run.Backup, boolToInt(run.BackupFailed)); err != nil {
			return err
		}
	}
	// 搬完顺手按 90 天口径清一次旧账，与日常 AppendRun 的行为一致。
	_, err = tx.Exec(`DELETE FROM runs WHERE at < ?`, fmtTime(cutoff(90, time.Now())))
	if err != nil {
		return err
	}
	return tx.Commit()
}

// sortRuns 按时间从早到晚排（导入侧保持与 ReadRuns 同样的顺序约定）。
func sortRuns(runs []Run) {
	sort.SliceStable(runs, func(i, j int) bool { return runs[i].At.Before(runs[j].At) })
}
