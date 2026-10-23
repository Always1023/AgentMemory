package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// ── 系统定时任务（I-016）──────────────────────────────────────────────────
//
// 这一组用例全用**假的命令执行器**：真的去建系统任务，会给跑测试的人留下一份
// 每天自动跑的定时任务。真机验证另有一条（见 TestScheduleAgainstTheRealSystem）。

// fakeSchtasks 是一台「假系统」：按调用参数回答，并记下被要求跑了什么。
//
// 它像真系统一样有记忆：建过、改过之后，再查回来的是新的时刻；删过之后就查不到。
// 用例因此能验「读回的是系统里的值」，而不是「把我们传进去的值又吐回来」。
type fakeSchtasks struct {
	// calls 是收到的每次调用（参数原样）。
	calls [][]string
	// installed 是「系统里现在有没有这个任务」。
	installed bool
	// at 是系统里存着的每天触发时刻。
	at string
	// missingErr 是查不到任务时返回的错（退出码 1，真 schtasks 就是这个）。
	missingErr error
	// createOut / createErr 是建任务失败时的返回。
	createOut string
	createErr error
	// deleteOut / deleteErr 是删任务失败时的返回。
	deleteOut string
	deleteErr error
}

func (f *fakeSchtasks) run(args ...string) (string, error) {
	f.calls = append(f.calls, args)

	switch args[0] {
	case "/Query":
		if !f.installed {
			return f.output(""), f.missingErr
		}
		return f.taskXML(), nil

	case "/Create":
		if f.createErr != nil {
			return f.createOut, f.createErr
		}
		f.installed = true
		f.at = valueOf(args, "/ST")
		return f.createOut, nil

	case "/Delete":
		if f.deleteErr != nil {
			return f.deleteOut, f.deleteErr
		}
		if !f.installed {
			// 真 schtasks 删一个不存在的任务也会退出 1。
			return f.output(""), f.missingErr
		}
		f.installed = false
		return f.deleteOut, nil
	}
	return "", nil
}

// output 统一一下「查不到任务时」那点输出（真 schtasks 会给一句本地化的错误）。
func (f *fakeSchtasks) output(_ string) string {
	if f.missingErr == nil {
		return ""
	}
	return "ERROR: The system cannot find the file specified."
}

// taskXML 按系统里存着的时刻现造一份任务定义。
func (f *fakeSchtasks) taskXML() string {
	at := f.at
	if at == "" {
		at = "09:00"
	}
	return strings.Replace(sampleTaskXML, "09:00:00", at+":00", 1)
}

// valueOf 取一个开关后面的值（如 /ST 07:30 里的 07:30）。
func valueOf(args []string, flag string) string {
	for i, arg := range args {
		if arg == flag && i+1 < len(args) {
			return args[i+1]
		}
	}
	return ""
}

// calledWith 报告有没有一次调用同时含这些片段。
func (f *fakeSchtasks) calledWith(parts ...string) bool {
	for _, call := range f.calls {
		joined := strings.Join(call, " ")
		all := true
		for _, part := range parts {
			if !strings.Contains(joined, part) {
				all = false
				break
			}
		}
		if all {
			return true
		}
	}
	return false
}

// exitError 造一个「退出码是 code」的错误，模拟 schtasks 失败。
func exitError(t *testing.T, code int) error {
	t.Helper()
	// 真跑一条必然失败的命令来拿到 ExitError：比手搓一个 *exec.ExitError 可靠。
	err := exec.Command("cmd", "/c", "exit", strconv.Itoa(code)).Run()
	if err == nil {
		t.Fatalf("拿不到退出码 %d", code)
	}
	return err
}

// 一份真实形态的任务定义（schtasks /Query /XML 的输出）。
const sampleTaskXML = `<?xml version="1.0" encoding="UTF-16"?>
<Task version="1.2" xmlns="http://schemas.microsoft.com/windows/2004/02/mit/task">
  <RegistrationInfo>
    <Author>DESKTOP\Always</Author>
    <URI>\AgentMemory\Collect</URI>
  </RegistrationInfo>
  <Triggers>
    <CalendarTrigger>
      <StartBoundary>2026-10-16T09:00:00</StartBoundary>
      <Enabled>true</Enabled>
      <ScheduleByDay>
        <DaysInterval>1</DaysInterval>
      </ScheduleByDay>
    </CalendarTrigger>
  </Triggers>
  <Actions Context="Author">
    <Exec>
      <Command>"D:\work\AgentMemory\cmd\agentmemory\build\bin\AgentMemory.exe"</Command>
      <Arguments>--collect</Arguments>
    </Exec>
  </Actions>
</Task>
`

// 验收标准 1：建出来的任务，动作指向本程序的 --collect。
func TestWriteScheduleCreatesDailyTaskRunningCollect(t *testing.T) {
	fake := &fakeSchtasks{}

	if err := writeSchedule(fake.run, "09:00"); err != nil {
		t.Fatalf("建任务: %v", err)
	}

	if !fake.calledWith("/Create", "/TN", taskName, "/SC", "DAILY", "/ST", "09:00") {
		t.Errorf("建任务的参数不对：%v", fake.calls)
	}
	// 动作里要有 --collect，也要有本程序的文件名。
	// 只认文件名而不是整个路径：任务的命令行来自系统，路径部分可能带非中文编码，
	// 但文件名与参数这一段是稳定的。
	if !fake.calledWith("/TR", collectFlag, "AgentMemory") {
		t.Errorf("任务的动作应当指向本程序的 %s，实际 %v", collectFlag, fake.calls)
	}
	if !fake.calledWith("/F") {
		t.Error("重建任务要带 /F：不带的话系统里会留下第二个任务")
	}
}

// 验收标准 3：改了时间，读回来的是新时间；而下次运行时间是从系统里存着的时刻算的。
func TestWriteThenReadGivesTheNewTime(t *testing.T) {
	fake := &fakeSchtasks{installed: true, at: "07:30"}

	if err := writeSchedule(fake.run, "07:30"); err != nil {
		t.Fatalf("建任务: %v", err)
	}
	status, err := readSchedule(fake.run)
	if err != nil {
		t.Fatalf("读任务: %v", err)
	}

	if !status.Installed {
		t.Fatal("读到任务了，Installed 应当为真")
	}
	if status.DailyAt != "07:30" {
		t.Errorf("读回来的时刻是 %q，想要 07:30", status.DailyAt)
	}
	if status.Action != `"D:\work\AgentMemory\cmd\agentmemory\build\bin\AgentMemory.exe" --collect` {
		t.Errorf("读回来的动作不对：%q", status.Action)
	}

	// 下次运行时间：今天 07:30 还没到就是今天，过了就是明天。
	next, err := time.ParseInLocation("2006-01-02 15:04", status.NextRun, time.Local)
	if err != nil {
		t.Fatalf("下次运行时间 %q 不是可解析的格式：%v", status.NextRun, err)
	}
	now := time.Now()
	if !next.After(now) {
		t.Errorf("下次运行时间 %v 应当在当前时间 %v 之后", next, now)
	}
	if next.Sub(now) > 24*time.Hour {
		t.Errorf("下次运行时间 %v 离现在 %v 超过一天了", next, now)
	}
	if next.Hour() != 7 || next.Minute() != 30 {
		t.Errorf("下次运行时间 %v 应当是 07:30", next)
	}
}

// 验收标准 1：「系统里没有它」要如实报，而不是当成出错。
func TestReadScheduleReportsNotInstalled(t *testing.T) {
	fake := &fakeSchtasks{missingErr: exitError(t, 1)}

	status, err := readSchedule(fake.run)
	if err != nil {
		t.Fatalf("查没有的任务不该报错：%v", err)
	}
	if status.Installed {
		t.Errorf("系统里没有这个任务，Installed 应当为假，实际 %+v", status)
	}
}

// 验收标准 2：删掉之后系统里不留残留；本来就没有也算关成功。
func TestRemoveScheduleLeavesNothingBehind(t *testing.T) {
	fake := &fakeSchtasks{}
	if err := removeSchedule(fake.run); err != nil {
		t.Fatalf("删任务: %v", err)
	}
	if !fake.calledWith("/Delete", "/F", "/TN", taskName) {
		t.Errorf("删任务的参数不对：%v", fake.calls)
	}

	// 本来就没有：删的时候系统报「找不到」，这不算失败。
	missing := &fakeSchtasks{missingErr: exitError(t, 1)}
	if err := removeSchedule(missing.run); err != nil {
		t.Errorf("删一个本来就不存在的任务不该报错：%v", err)
	}
}

// 验收标准 4：建失败（权限不足之类）要返回可读错误，不许静默成功。
func TestWriteScheduleFailureIsReadable(t *testing.T) {
	fake := &fakeSchtasks{
		createErr: exitError(t, 1),
		createOut: "ERROR: Access is denied.\n",
	}

	err := writeSchedule(fake.run, "09:00")
	if err == nil {
		t.Fatal("建失败必须报错——静默成功会让人以为定时开好了")
	}
	message := err.Error()
	for _, part := range []string{"建定时任务失败", "Access is denied", "权限"} {
		if !strings.Contains(message, part) {
			t.Errorf("错误里应当包含 %q，实际 %q", part, message)
		}
	}
	// 退出码 1 在建这一步是真的错了，不许被当成「本来就没有这个任务」——
	// 那会把「开定时失败」变成静默失败。上面 err == nil 那一句就是这条的判据。
}

// 系统上跑着中文时，schtasks 的输出是 GBK 字节：别把乱码塞进日志与界面。
func TestFailureTextDropsMojibake(t *testing.T) {
	err := exitError(t, 1)
	// 这是「错误: 系统找不到指定的文件。」的 GBK 字节。
	gbk := string([]byte{0xB4, 0xED, 0xCE, 0xF3, 0xA3, 0xBA, 0xCF, 0xB5, 0xCD, 0xB3, 0xD5, 0xD2, 0xB2, 0xBB, 0xB5, 0xBD})

	text := failureText(err, gbk)
	if strings.Contains(text, string([]byte{0xB4, 0xED})) {
		t.Errorf("GBK 字节不该原样出现在给日志与界面看的文字里，实际 %q", text)
	}
	if !strings.Contains(text, "exit status") {
		t.Errorf("输出读不出来时至少要有退出码，实际 %q", text)
	}
}

// 时间格式不对时，在建任务之前就拒绝——别拿一个非法时刻去动系统。
func TestWriteScheduleRejectsABadTime(t *testing.T) {
	fake := &fakeSchtasks{}

	for _, bad := range []string{"", "25:00", "9", "09:70", "上午九点"} {
		if err := writeSchedule(fake.run, bad); err == nil {
			t.Errorf("时刻 %q 不该被接受", bad)
		}
	}
	if len(fake.calls) != 0 {
		t.Errorf("时刻不合法时不该去动系统，实际调用了 %v", fake.calls)
	}
}

// 下次运行时间的算法：今天没到算今天，过了算明天。
func TestNextRunAtRollsToTomorrow(t *testing.T) {
	now := time.Now()

	// 两小时后的那个时刻：今天没到就是今天，跨过零点就是明天。
	// 期望值按「那个时刻本身」算，不写死「今天」——22:00 之后跑这条时，两小时后已经是明天了
	// （这条测试曾经就这么挂过，被当成「偶发失败」记了好几次）。
	soon := now.Add(2 * time.Hour)
	if got := nextRunAt(soon); got.Format("2006-01-02 15:04") != soon.Format("2006-01-02 15:04") {
		t.Errorf("下次应当是 %s，实际 %v", soon.Format("2006-01-02 15:04"), got)
	}

	past := now.Add(-2 * time.Hour)
	got := nextRunAt(past)
	if !got.After(now) {
		t.Errorf("已经过的时刻，下次应当在将来，实际 %v", got)
	}
	if got.Hour() != past.Hour() || got.Minute() != past.Minute() {
		t.Errorf("下次的时刻应当就是 %02d:%02d，实际 %v", past.Hour(), past.Minute(), got)
	}
	// 只在这一刻**今天**已经过去时才要求「不能还算今天」——
	// 凌晨跑这条时，now-2h 是昨天，那时今天这一刻还没到，算今天是对的
	//（原来这里不看日期就要求「不算今天」，过了零点必挂）。
	if past.Day() == now.Day() && got.Day() == now.Day() {
		t.Errorf("今天已经过了 %02d:%02d，下次就不该还算今天：%v", past.Hour(), past.Minute(), got)
	}
}

// 验收标准 5（评审核对）：程序不常驻。
//
// 这件事只能读代码：本文件不出现任何「等待 / 循环 / 常驻」结构，
// 只调用 schtasks 去建 / 删 / 查——触发由系统负责。
func TestScheduleWrapperDoesNotStayResident(t *testing.T) {
	raw, err := os.ReadFile("schedule.go")
	if err != nil {
		t.Fatal(err)
	}
	source := stripLineComments(string(raw))

	for _, forbidden := range []string{"for {", "time.Tick", "go func", "ListenAndServe", "select {"} {
		if strings.Contains(source, forbidden) {
			t.Errorf("schedule.go 里不该出现 %q——定时由系统触发，程序不常驻", forbidden)
		}
	}
	if !strings.Contains(source, "schedCommand(") {
		t.Error("跑 schtasks 要走 schedCommand，别自己拼 exec.Command")
	}
	if !strings.Contains(readSource(t, "proc.go"), "exec.CommandContext") {
		t.Error("外部调用应当带超时（exec.CommandContext）")
	}
}

// ── 真机验证（默认跳过）────────────────────────────────────────────────────
//
// 这一条会真的动这台机器的计划任务，所以默认不跑；自验时用
// AM_SCHEDULE_LIVE=1 手动跑一次，它自己会建、查、再删干净。
func TestScheduleAgainstTheRealSystem(t *testing.T) {
	if os.Getenv("AM_SCHEDULE_LIVE") != "1" {
		t.Skip("会动真机的计划任务；要跑就设 AM_SCHEDULE_LIVE=1")
	}

	// 收尾：无论成败都把任务删干净，别给这台机器留下一个每天自动跑的任务。
	t.Cleanup(func() {
		if err := removeSchedule(runSchtasks); err != nil {
			t.Errorf("收尾时没能删掉任务，请手工删掉 %s：%v", taskName, err)
			return
		}
		if status, err := readSchedule(runSchtasks); err != nil || status.Installed {
			t.Errorf("删完之后系统里还留着任务：%+v（%v）", status, err)
		}
	})

	if err := writeSchedule(runSchtasks, "07:30"); err != nil {
		t.Fatalf("建任务: %v", err)
	}

	status, err := readSchedule(runSchtasks)
	if err != nil {
		t.Fatalf("读任务: %v", err)
	}
	t.Logf("系统里的任务：动作=%q 每天=%s 下次=%s", status.Action, status.DailyAt, status.NextRun)

	if !status.Installed {
		t.Fatal("刚建完的任务，系统里应当能查到")
	}
	if status.DailyAt != "07:30" {
		t.Errorf("系统里存着的时刻是 %q，想要 07:30（读回的是系统里的值，不是我们传进去的）", status.DailyAt)
	}
	if !strings.Contains(status.Action, collectFlag) {
		t.Errorf("任务的动作里应当有 %s，实际 %q", collectFlag, status.Action)
	}
	if !strings.Contains(status.Action, filepath.Base(mustExecutable(t))) {
		t.Errorf("任务的动作应当跑本程序，实际 %q", status.Action)
	}
	if status.NextRun == "" {
		t.Error("应当能算出下次运行时间")
	}

	// 改时间：再建一次（/F 覆盖），读回来应当是新的。
	if err := writeSchedule(runSchtasks, "21:15"); err != nil {
		t.Fatalf("改时间: %v", err)
	}
	status, err = readSchedule(runSchtasks)
	if err != nil {
		t.Fatalf("再读一次: %v", err)
	}
	if status.DailyAt != "21:15" {
		t.Errorf("改完之后系统里的时刻是 %q，想要 21:15", status.DailyAt)
	}
}

func mustExecutable(t *testing.T) string {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	return exe
}
