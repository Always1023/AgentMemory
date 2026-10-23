package store

// 这个文件是「运行记录」（I-030 / PRD F11）：一行一次收集，只追加。
//
// 与日志文件分工不同：日志是给人读的、不保证好解析；这一份是结构化的，
// 界面按天读回去，回答「定时任务那天跑没跑、收了多少」。
//
// 依赖方向与游标一致：只依赖标准库，目录由调用方传入（不 import config）。

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// RunsFileName 是运行记录的文件名；本包是它唯一的写入者。
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

// RunsPath 给出运行记录的文件路径。
func RunsPath(dir string) string { return filepath.Join(dir, RunsFileName) }

// AppendRun 添一行运行记录，顺手把太老的行清掉。
//
// 只追加：已有行不会被改（I-030 验收标准 4）。返回错误由调用方决定怎么办——
// 记录是给人看的账，不该因为它没写成而让收集本身失败。
func AppendRun(dir string, run Run, keepDays int, now time.Time) error {
	if strings.TrimSpace(dir) == "" {
		return errors.New("没有可写运行记录的目录")
	}
	if keepDays < 1 {
		keepDays = 1
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("建运行记录目录 %s: %w", dir, err)
	}

	line, err := json.Marshal(run)
	if err != nil {
		return fmt.Errorf("整理这一轮的记录: %w", err)
	}

	f, err := os.OpenFile(RunsPath(dir), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("打开运行记录 %s: %w", RunsPath(dir), err)
	}
	if _, err := f.Write(append(line, '\n')); err != nil {
		_ = f.Close()
		return fmt.Errorf("写运行记录 %s: %w", RunsPath(dir), err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("写运行记录 %s: %w", RunsPath(dir), err)
	}

	return pruneRuns(dir, keepDays, now)
}

// ReadRuns 读回 keepDays 天以内的行，按时间从早到晚排好。
//
// 一条读不懂的行跳过就好：记录是账，一行坏了不该让整页打不开。
func ReadRuns(dir string, keepDays int, now time.Time) ([]Run, error) {
	raw, err := os.ReadFile(RunsPath(dir))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("读运行记录 %s: %w", RunsPath(dir), err)
	}

	cut := cutoff(keepDays, now)
	out := make([]Run, 0, 64)
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var run Run
		if err := json.Unmarshal([]byte(line), &run); err != nil {
			continue
		}
		if run.At.Before(cut) {
			continue
		}
		out = append(out, run)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].At.Before(out[j].At) })
	return out, nil
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

// pruneRuns 丢掉太老的行（I-030 验收标准 5）：超过 keepDays 天的一律清掉。
//
// 没得清就一个字都不动——省一次重写，也不给「历史被改过」留口子。
func pruneRuns(dir string, keepDays int, now time.Time) error {
	raw, err := os.ReadFile(RunsPath(dir))
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("读运行记录 %s: %w", RunsPath(dir), err)
	}

	cut := cutoff(keepDays, now)
	kept := make([]string, 0, 64)
	total := 0
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		total++
		var run Run
		if err := json.Unmarshal([]byte(line), &run); err != nil {
			continue // 坏行丢掉
		}
		if run.At.Before(cut) {
			continue
		}
		kept = append(kept, line)
	}
	if len(kept) == total {
		return nil
	}

	// 先写临时文件再改名：中途出问题也不会留下一半的记录。
	tmp := RunsPath(dir) + ".tmp"
	body := ""
	if len(kept) > 0 {
		body = strings.Join(kept, "\n") + "\n"
	}
	if err := os.WriteFile(tmp, []byte(body), 0o644); err != nil {
		return fmt.Errorf("整理运行记录 %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, RunsPath(dir)); err != nil {
		return fmt.Errorf("替换运行记录 %s: %w", RunsPath(dir), err)
	}
	return nil
}
