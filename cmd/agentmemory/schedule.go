package main

import (
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Always1023/AgentMemory/internal/config"
)

// 这个文件封装系统定时任务（Windows 计划任务）的增 / 删 / 改 / 查。
//
// 用 `schtasks.exe`，不引库（docs/ARCHITECTURE.md 技术栈表：「定时：schtasks.exe，
// 界面可一键开关定时，不引库」）。几次调用都设超时（AC-07）。
//
// 只做「系统里有没有它、它现在长什么样、建它、删它」这四件事；「该不该开定时、
// 几点跑」是配置的事，不在这儿判断。界面上那个开关与时间选择在 I-017。

const (
	// taskName 是系统里那个任务的路径。
	//
	// 放在 \AgentMemory\ 下面：人在「任务计划程序」里一眼能找到，排查时也不会
	// 和机器上原有的任务混在一起。
	taskName = `\AgentMemory\Collect`

	// scheduleTimeout 是单次外部调用的上限（AC-07）。
	scheduleTimeout = 30 * time.Second

	// collectFlag 是任务要跑的那个参数，与 main.go 里注册的 `-collect` 同一个。
	collectFlag = "--collect"
)

// ScheduleStatus 是系统里那个任务现在的样子。
type ScheduleStatus struct {
	// Installed 报告系统里有没有这个任务。
	Installed bool
	// Action 是任务的动作（命令行）；没有任务时为空。
	Action string
	// DailyAt 是从**系统里读回来的**每天触发时刻（"09:00"）；读不出来为空。
	//
	// 注意它不等于配置里的值：系统里被改过、或上一次没建成，这里就会不一样。
	// 界面显示它、而不是显示配置，人才能看出「说了要开定时，其实没建成」。
	DailyAt string
	// NextRun 是据此算出来的下次运行时间（"2026-10-16 09:00"）。
	NextRun string
}

// scheduleRunner 跑一条外部命令，返回合并后的输出与错误。
//
// 留成可替换的：用例不真去动系统上的计划任务（那会给跑测试的人留下一个每天
// 自动跑的任务），而是注入一个假的，记下被要求跑了什么。
type scheduleRunner func(args ...string) (string, error)

// runSchtasks 跑一次 schtasks.exe。
func runSchtasks(args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), scheduleTimeout)
	defer cancel()

	out, err := schedCommand(ctx, args...).CombinedOutput()
	text := string(out)
	if ctx.Err() != nil {
		return text, fmt.Errorf("schtasks 超时（%s）", scheduleTimeout)
	}
	return text, err
}

// readSchedule 读系统里那个任务现在的样子。
//
// 存在与否看退出码：schtasks 查不到任务时退出 1，这条与系统语言无关。
// 动作与触发时刻从 `/XML` 读——XML 里的元素名是 Task Scheduler 模式里固定的英文，
// 不像 `/FO LIST` 那样随系统语言变（中文系统上是「下次运行时间」这类标签）。
func readSchedule(run scheduleRunner) (ScheduleStatus, error) {
	out, err := run("/Query", "/TN", taskName, "/XML")
	if err != nil {
		if isMissingTask(err) {
			return ScheduleStatus{}, nil
		}
		return ScheduleStatus{}, fmt.Errorf("查定时任务失败：%s", failureText(err, out))
	}
	return parseTaskXML(out)
}

// writeSchedule 建（或按新的时间重建）那个任务：每天 at 跑一次本程序的 --collect。
//
// 重建用 /F：改时间就是「按新时间覆盖」。不覆盖的话系统里会留下第二个任务，
// 那正是「时间改了却还是老点跑」的来源。
func writeSchedule(run scheduleRunner, at string) error {
	hour, minute, err := config.ParseDailyAt(at)
	if err != nil {
		return err
	}
	when := fmt.Sprintf("%02d:%02d", hour, minute)

	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("找不到本程序的位置：%w", err)
	}

	out, err := run("/Create", "/F", "/TN", taskName, "/TR", taskCommand(exe), "/SC", "DAILY", "/ST", when)
	if err != nil {
		return fmt.Errorf("建定时任务失败：%s（常见原因是权限不足，或被安全软件拦下）。先用界面上的「增量收集」手动收；要以管理员身份运行一次这个程序，再来打开这个开关",
			failureText(err, out))
	}
	return nil
}

// removeSchedule 删掉那个任务。
//
// 本来就没有也算成功：「关掉定时」要的结果是「系统里没有它」，不是「删的动作成功过」。
func removeSchedule(run scheduleRunner) error {
	out, err := run("/Delete", "/F", "/TN", taskName)
	if err != nil {
		if isMissingTask(err) {
			return nil
		}
		return fmt.Errorf("删定时任务失败：%s", failureText(err, out))
	}
	return nil
}

// taskCommand 拼出任务要跑的命令行。
//
// 程序路径带空格时要自己再加一层引号，schtasks 才认得出「引号里是一整条命令」。
func taskCommand(exe string) string {
	return `"` + exe + `" ` + collectFlag
}

// isMissingTask 判断这次失败是不是「系统里没有这个任务」。
//
// schtasks 在查不到 / 删不到时退出 1。**只在读和删的时候这么判**：
// `/Create` 也用退出 1 表示别的错（权限不足），把建失败当成「本来就没有」
// 会变成静默失败（验收标准 4 要的正是「不许静默」）。
func isMissingTask(err error) bool {
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		return exit.ExitCode() == 1
	}
	return false
}

// failureText 把「命令失败 + 它的输出」压成一句给人看的话。
//
// 中文系统上 schtasks 的中文输出是 GBK 字节，直接当 UTF-8 用就是乱码；那种情况
// 只留错误本身（里面有退出码），别把乱码塞进日志与界面。
func failureText(err error, out string) string {
	message := "schtasks 没有说明"
	if err != nil {
		message = err.Error()
	}

	text := strings.TrimSpace(out)
	if text == "" || !utf8.ValidString(text) {
		return message
	}
	return message + "：" + shortText(strings.Join(strings.Fields(text), " "), 200)
}

// shortText 截断过长的输出，免得一句错误把日志与界面撑爆。
func shortText(s string, limit int) string {
	if len(s) <= limit {
		return s
	}
	return s[:limit] + "…"
}

// taskDef 是任务定义 XML 里我们用得着的那几段。
type taskDef struct {
	Triggers struct {
		Calendar []struct {
			StartBoundary string `xml:"StartBoundary"`
			ScheduleByDay *struct {
				DaysInterval string `xml:"DaysInterval"`
			} `xml:"ScheduleByDay"`
		} `xml:"CalendarTrigger"`
	} `xml:"Triggers"`
	Actions struct {
		Exec []struct {
			Command   string `xml:"Command"`
			Arguments string `xml:"Arguments"`
		} `xml:"Exec"`
	} `xml:"Actions"`
}

// parseTaskXML 从任务定义里读出「动作是什么、每天几点」。
func parseTaskXML(raw string) (ScheduleStatus, error) {
	var def taskDef
	decoder := xml.NewDecoder(strings.NewReader(raw))
	// schtasks 在 XML 头上写着 UTF-16，实际给的是当前代码页的字节。
	// 按字节原样读，别让解码器去找我们没装的字符集转换器。
	decoder.CharsetReader = func(_ string, input io.Reader) (io.Reader, error) { return input, nil }
	if err := decoder.Decode(&def); err != nil {
		return ScheduleStatus{}, fmt.Errorf("读任务定义失败：%w", err)
	}

	status := ScheduleStatus{Installed: true}
	if len(def.Actions.Exec) > 0 {
		status.Action = strings.TrimSpace(def.Actions.Exec[0].Command + " " + def.Actions.Exec[0].Arguments)
	}
	for _, trigger := range def.Triggers.Calendar {
		if trigger.ScheduleByDay == nil {
			continue
		}
		at, ok := dailyAtFromBoundary(trigger.StartBoundary)
		if !ok {
			continue
		}
		status.DailyAt = fmt.Sprintf("%02d:%02d", at.Hour(), at.Minute())
		status.NextRun = nextRunAt(at).Format("2006-01-02 15:04")
		break
	}
	return status, nil
}

// dailyAtFromBoundary 解析任务定义里的 StartBoundary。
//
// 系统写出来的是本地时间，形如 `2026-10-16T09:00:00`；带时区偏移的写法也认。
func dailyAtFromBoundary(boundary string) (time.Time, bool) {
	boundary = strings.TrimSpace(boundary)
	for _, layout := range []string{time.RFC3339, "2006-01-02T15:04:05"} {
		if at, err := time.ParseInLocation(layout, boundary, time.Local); err == nil {
			return at, true
		}
	}
	return time.Time{}, false
}

// nextRunAt 算「下一次几点跑」。
//
// 系统里存的是「每天 HH:MM」；运行时的下次时间由它推出来：今天这个点还没到就是
// 今天，已经过了（或正好到点）就是明天。这么算不依赖系统语言，比去解析 schtasks
// 那句本地化的「下次运行时间」稳。
func nextRunAt(at time.Time) time.Time {
	now := time.Now()
	next := time.Date(now.Year(), now.Month(), now.Day(), at.Hour(), at.Minute(), 0, 0, now.Location())
	if !next.After(now) {
		next = next.AddDate(0, 0, 1)
	}
	return next
}
