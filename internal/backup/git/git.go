// Package git 实现「备份到 git 仓库」（PRD F5 的默认方式）。
//
// 走系统上已经装好的 git 命令行：复用用户自己配好的凭据
// （SSH key、Windows 凭据管理器），本程序不碰任何令牌（docs/ARCHITECTURE.md 技术栈）。
//
// 归档目录**本身**就是仓库：`git init` 建在它里面，`.git` 与 Markdown 并排。
// 于是「版本历史」和「归档文件」是同一份东西，不用另一处目录来记。
package git

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/Always1023/AgentMemory/internal/backup"
)

// ID 与 Name 是注册表用的身份。ID 与配置里的 backup.mode 取值一致。
const (
	ID   = "git"
	Name = "Git 仓库"
)

const (
	// defaultBranch 是没指定分支时用的分支名。
	defaultBranch = "main"
	// defaultTimeout 是单次 git 命令的默认上限（AC-07）。
	defaultTimeout = 60 * time.Second
	// outputLimit 是错误信息里最多带多少字符的 git 输出。
	outputLimit = 2000
	// messageLayout 是默认提交信息里的时间形式。
	messageLayout = "2006-01-02 15:04:05"
)

// Method 实现 backup.Method。
type Method struct{}

// Back 把归档目录提交并推送到远程仓库。
//
// 两种「什么都不做」不算失败：
//   - 归档目录还不存在（还没收集过）；
//   - 没配远程仓库地址（AC-02：未配置时不发起网络请求）。
//
// 其余任何一步失败都返回带 git 原话的错误；归档目录里的文件不受影响
// （最坏情况是本地多了一个提交，推不出去）。
func (Method) Back(s backup.Settings) backup.Result {
	dir := strings.TrimSpace(s.Dir)
	if dir == "" {
		return backup.Result{Err: errors.New("没有指定归档目录")}
	}
	switch info, err := os.Stat(dir); {
	case errors.Is(err, os.ErrNotExist):
		return backup.Result{Detail: "归档目录还不存在，跳过备份"}
	case err != nil:
		return backup.Result{Err: fmt.Errorf("看归档目录 %s: %w", dir, err)}
	case !info.IsDir():
		return backup.Result{Err: fmt.Errorf("归档目录 %s 不是目录", dir)}
	}
	if strings.TrimSpace(s.Remote) == "" {
		return backup.Result{Detail: "没有配置远程仓库地址，跳过备份"}
	}

	timeout := s.Timeout
	if timeout <= 0 {
		timeout = defaultTimeout
	}
	// 每条 git 命令都挂在这个 ctx 上：超时由它兜（验收标准 5）。
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	branch := strings.TrimSpace(s.Branch)
	if branch == "" {
		branch = defaultBranch
	}
	message := strings.TrimSpace(s.Message)
	if message == "" {
		message = "收集于 " + time.Now().Format(messageLayout)
	}

	if err := ensureRepo(ctx, dir, branch); err != nil {
		return backup.Result{Err: err}
	}
	if _, err := run(ctx, dir, "add", "-A"); err != nil {
		return backup.Result{Err: fmt.Errorf("git add: %w", err)}
	}

	// 没有变化就不提交——空提交会把历史变成噪音（验收标准 3）。
	// 但「没有新文件」不等于「没有事要做」：上一轮推送失败留下的提交，
	// 这一轮要补推（I-052）。
	status, err := run(ctx, dir, "status", "--porcelain")
	if err != nil {
		return backup.Result{Err: fmt.Errorf("git status: %w", err)}
	}
	if strings.TrimSpace(status) == "" {
		if !hasUnpushed(ctx, dir, branch) {
			return backup.Result{Detail: "工作区没有变化，跳过提交"}
		}
		if err := ensureRemote(ctx, dir, strings.TrimSpace(s.Remote)); err != nil {
			return backup.Result{Err: err}
		}
		if _, err := run(ctx, dir, "push", "origin", "HEAD:"+branch); err != nil {
			return backup.Result{Err: fmt.Errorf("git push: %w", err)}
		}
		return backup.Result{Detail: "工作区没有变化，补推了未推送的提交"}
	}
	files := len(strings.Split(strings.TrimSpace(status), "\n"))

	if _, err := run(ctx, dir, "commit", "-m", message); err != nil {
		return backup.Result{Changed: true, Detail: summary(files, false), Err: fmt.Errorf("git commit: %w", err)}
	}
	if err := ensureRemote(ctx, dir, strings.TrimSpace(s.Remote)); err != nil {
		return backup.Result{Changed: true, Detail: summary(files, false), Err: err}
	}

	// 显式写 `HEAD:<分支>`：本地分支叫什么都行，推上去的名字由配置定。
	if _, err := run(ctx, dir, "push", "origin", "HEAD:"+branch); err != nil {
		return backup.Result{Changed: true, Detail: summary(files, false), Err: fmt.Errorf("git push: %w", err)}
	}

	return backup.Result{Changed: true, Detail: summary(files, true)}
}

// hasUnpushed 报告本地有没有还没推上去的提交。
//
// 刚 init、还没有任何提交时返回 false——没有东西可推。
// 远端分支还不存在（第一次推）但有提交时返回 true：本地这些都要推。
// 查询本身出错也按「有」处理：宁可多推一次，也不要漏掉没推上去的提交。
func hasUnpushed(ctx context.Context, dir, branch string) bool {
	if _, err := run(ctx, dir, "rev-parse", "--verify", "HEAD"); err != nil {
		return false
	}
	out, err := run(ctx, dir, "rev-list", "--count", "origin/"+branch+"..HEAD")
	if err != nil {
		return true
	}
	n, err := strconv.Atoi(strings.TrimSpace(out))
	if err != nil {
		return true
	}
	return n > 0
}

// ensureRepo 保证 dir 是个 git 仓库。
func ensureRepo(ctx context.Context, dir, branch string) error {
	if out, err := run(ctx, dir, "rev-parse", "--is-inside-work-tree"); err == nil && strings.Contains(out, "true") {
		return nil
	}

	if _, err := run(ctx, dir, "init", "-b", branch); err == nil {
		return nil
	}
	// 老版本 git 不认 `init -b`，退回「先建、再改名」。
	if _, err := run(ctx, dir, "init"); err != nil {
		return fmt.Errorf("初始化仓库: %w", err)
	}
	if _, err := run(ctx, dir, "branch", "-M", branch); err != nil {
		return fmt.Errorf("把分支改名为 %s: %w", branch, err)
	}
	return nil
}

// ensureRemote 让 origin 指向配置里的地址，没配过就加，配过但不是这个就改。
func ensureRemote(ctx context.Context, dir, remote string) error {
	current, err := run(ctx, dir, "remote", "get-url", "origin")
	if err != nil {
		if _, err := run(ctx, dir, "remote", "add", "origin", remote); err != nil {
			return fmt.Errorf("加远程地址: %w", err)
		}
		return nil
	}
	if strings.TrimSpace(current) == remote {
		return nil
	}
	if _, err := run(ctx, dir, "remote", "set-url", "origin", remote); err != nil {
		return fmt.Errorf("改远程地址: %w", err)
	}
	return nil
}

// run 跑一条 git 命令，返回它的全部输出（stdout 与 stderr 合在一起）。
//
// 顺带禁掉交互式输入：卡在「请输入用户名」上等超时，比直接失败还难查。
func run(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...)
	// 不弹控制台窗口：这个程序是图形程序，弹出来只会在屏幕上闪一下
	// （与 cmd/agentmemory/proc.go 里那两处是同一条理由）。
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "GIT_ASKPASS=", "SSH_ASKPASS=")

	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf

	err := cmd.Run()
	out := buf.String()
	if err == nil {
		return out, nil
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		return out, fmt.Errorf("git %s 超时: %w", strings.Join(args, " "), ctxErr)
	}
	return out, fmt.Errorf("git %s 失败: %w: %s", strings.Join(args, " "), err, summarize(out))
}

// summary 报告「改了几个文件、推没推出去」。
func summary(files int, pushed bool) string {
	if pushed {
		return fmt.Sprintf("%d 个文件，已提交并推送", files)
	}
	return fmt.Sprintf("%d 个文件，已提交但没推出去", files)
}

// summarize 把 git 的输出收拾成一段能塞进错误里的短话。
func summarize(out string) string {
	out = strings.TrimSpace(out)
	if out == "" {
		return "（git 没有输出）"
	}
	if len(out) > outputLimit {
		out = out[:outputLimit] + "…"
	}
	return out
}
