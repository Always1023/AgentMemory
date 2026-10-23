package main

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/Always1023/AgentMemory/internal/config"
	wruntime "github.com/wailsapp/wails/v2/pkg/runtime"
)

// 这个文件是「Git 备份」那一段的引导（I-022）。
//
// 三步（I-029）：建仓库 → 把一段可复制的 Prompt 交给自己的 AI 去配远程 → 验连接。
// 中间还多做一件人交代的事：本地仓库**已经配好远程**时，把那个地址读出来自动填上——
// 远程那一头常常是别的 AI 建好配上的，不该让人手抄一遍。
//
// 只调只读命令，加一条 `git init`：
//   - `--version` / `rev-parse` / `remote get-url` / `ls-remote` 都是只读的；
//   - 唯一的写操作是 `git init`，且只作用在配置里的归档目录。
//
// 不动渠道原始目录（AC-01），也不推任何东西——推送是备份那一步的事。
// 每次调用都设超时（AC-07）。

const (
	// gitTimeout 是单次 git 调用的上限。
	gitTimeout = 30 * time.Second

	// repoPageURL 是新建空仓库的入口。
	repoPageURL = "https://github.com/new"

	// 引导停在哪一步。
	gitStepInstall = "installGit"
	gitStepInit    = "initRepo"
	gitStepRemote  = "createRemote"
	gitStepTest    = "testConnection"
	gitStepDone    = "done"
)

// gitRunner 跑一条 git 命令（dir 是工作目录），返回合并后的输出与错误。
//
// 留成可替换的：用例不真去碰文件系统与远端，注入一个假的就行。
type gitRunner func(dir string, args ...string) (string, error)

// UIGit 是 Git 那一段现在的样子。
type UIGit struct {
	// GitAvailable 报告这台机器上找不找得到 git；GitVersion 是版本号。
	GitAvailable bool   `json:"gitAvailable"`
	GitVersion   string `json:"gitVersion"`
	// RepoReady 报告归档目录已经是一个 git 仓库。
	RepoReady bool `json:"repoReady"`
	// DetectedRemote 是从本地仓库里读出来的远程地址；没配就是空的。
	DetectedRemote string `json:"detectedRemote"`
	// Step 是下一步该做什么；Note 是这一步的一句话。
	Step string `json:"step"`
	Note string `json:"note"`
	// RepoURL 是新建空仓库的直链。
	RepoURL string `json:"repoURL"`
	// Prompt 是第 2 步那段可复制的话：交给自己的 AI，让它去建仓库、配远程（I-029）。
	Prompt string `json:"prompt"`
	// Tested 表示刚试连过；OK 是结果，Detail 是说明。
	Tested bool   `json:"tested"`
	OK     bool   `json:"ok"`
	Detail string `json:"detail"`
}

// GitGuide 看一眼 Git 那一段现在到哪一步了。
func (a *App) GitGuide() (UIGit, error) {
	return a.gitGuide("")
}

// GitInit 在归档目录里建一个 git 仓库。
//
// 只作用在归档目录：目录从配置里来，且写之前先确认它不是空的（AC-09：路径不硬编码）。
func (a *App) GitInit() (UIGit, error) {
	cfg, err := config.Load()
	if err != nil {
		return UIGit{}, err
	}
	if strings.TrimSpace(cfg.ArchiveDir) == "" {
		return UIGit{}, errors.New("归档目录还没设，没法建仓库")
	}

	// 先确认 git 在：不在的话，让它去跑 init 也只会拿回一句含糊的错，
	// 不如在这儿说清「先去装 git」。
	guide, err := a.gitGuide("")
	if err != nil {
		return UIGit{}, err
	}
	if !guide.GitAvailable {
		return UIGit{}, errors.New("这台机器上没找到 git，先装它再来建仓库")
	}

	out, err := a.git()(cfg.ArchiveDir, "init")
	if err != nil {
		return UIGit{}, fmt.Errorf("在归档目录里建仓库失败：%s", failureText(err, out))
	}
	a.logger.Info("在归档目录里建了 git 仓库", "目录", cfg.ArchiveDir)
	return a.gitGuide("")
}

// GitTestRemote 试连一次：只读的 `git ls-remote`。
//
// 连不上不算「出错」——那正是要给人看的结果；只有读配置这类系统错才返回错误。
func (a *App) GitTestRemote(remote string) (UIGit, error) {
	cfg, err := config.Load()
	if err != nil {
		return UIGit{}, err
	}
	remote = strings.TrimSpace(remote)
	guide, err := a.gitGuide(remote)
	if err != nil {
		return UIGit{}, err
	}
	if remote == "" {
		// 界面不再问地址（人提的：有「试连」就够了）：先认本地仓库里配好的那个，
		// 认不到就看配置里记着的。
		remote = strings.TrimSpace(guide.DetectedRemote)
	}
	if remote == "" {
		remote = strings.TrimSpace(cfg.Backup.Git.Remote)
	}
	if remote == "" {
		return UIGit{}, errors.New("还没连上远程仓库。照上面第 1、2 步把仓库建好、远程配好，再来点「试连」")
	}

	out, err := a.git()(cfg.ArchiveDir, "ls-remote", remote)
	guide.Tested = true
	if err != nil {
		guide.OK = false
		guide.Detail = gitConnectHint(err, out)
		return guide, nil
	}

	guide.OK = true
	guide.Step = gitStepDone
	guide.Detail = "连上了。点「保存」之后，每天收完就会自动提交推送。"
	guide.Note = "都准备好了，点「保存」生效。"
	return guide, nil
}

// OpenRepoPage 在系统浏览器里打开「新建仓库」那一页。
//
// 地址固定在这里、不从界面传进来：界面能传的话，就等于给了它一个能打开任意网址的口子。
func (a *App) OpenRepoPage() error {
	if a.ctx == nil {
		return errors.New("窗口还没起来，打不开浏览器")
	}
	wruntime.BrowserOpenURL(a.ctx, repoPageURL)
	return nil
}

// CopyText 把一段文字放进系统剪贴板（I-029 第 2 步的「复制」）。
//
// 走 Wails 的 runtime，不靠浏览器剪贴板权限——窗口里没有那种弹窗可点。
// 只做这一件事：内容白名单由界面决定，后端不替它挑。
func (a *App) CopyText(text string) error {
	if a.ctx == nil {
		return errors.New("窗口还没起来，复制不了")
	}
	if strings.TrimSpace(text) == "" {
		return errors.New("没有可复制的内容")
	}
	return wruntime.ClipboardSetText(a.ctx, text)
}

// gitGuide 看一眼现状。remote 非空时把它当成「现在填在框里的地址」参与判断。
func (a *App) gitGuide(remote string) (UIGit, error) {
	cfg, err := config.Load()
	if err != nil {
		return UIGit{}, err
	}
	if strings.TrimSpace(remote) == "" {
		remote = cfg.Backup.Git.Remote
	}

	out := UIGit{RepoURL: repoPageURL, Prompt: gitPrompt()}
	run := a.git()

	version, versionErr := run("", "--version")
	if versionErr != nil {
		out.Step = gitStepInstall
		if errors.Is(versionErr, exec.ErrNotFound) {
			out.Note = "这台机器上没找到 git。先装一个：https://git-scm.com/download/win（装完点「重新检查」）。"
		} else {
			out.Note = "git 跑不起来：" + failureText(versionErr, version) + "。修好之后点「重新检查」。"
		}
		return out, nil
	}
	out.GitAvailable = true
	out.GitVersion = strings.TrimPrefix(strings.TrimSpace(version), "git version ")

	inside, _ := run(cfg.ArchiveDir, "rev-parse", "--is-inside-work-tree")
	out.RepoReady = strings.TrimSpace(inside) == "true"
	if !out.RepoReady {
		out.Step = gitStepInit
		out.Note = "归档目录还不是 git 仓库，点「就地建仓库」给它建一个。"
		return out, nil
	}

	// 已经是仓库：看看它有没有配好远程——这一步常常是别的 AI 做好的。
	if url, err := run(cfg.ArchiveDir, "remote", "get-url", "origin"); err == nil {
		out.DetectedRemote = strings.TrimSpace(url)
		if strings.TrimSpace(remote) == "" {
			// 框里还空着、仓库里却已经配好了：那就是现成的地址，按它来判断到哪一步。
			remote = out.DetectedRemote
		}
	}

	out.Step = gitStepRemote
	out.Note = "还没认出远程：把第 2 步那段交给你的 AI，等它带你建好仓库、配好远程，回到这里会自动认出来。"
	if strings.TrimSpace(remote) != "" {
		out.Step = gitStepTest
		out.Note = "已经认出远程了，这里会自己试一次；连不上就照下面的提示改。"
	}
	return out, nil
}

// gitPrompt 是第 2 步那段可复制的话：它读给**对方的 AI** 听（I-029）。
//
// 人交代过：AI 没那么聪明，不能指望它自己把事办完——所以这段话是「请它当向导，
// 一步一步带我做」，把「新建 GitHub 空仓库」「配密钥」这些明确写成它要说给我的步骤。
// 它只提「AgentMemory 的归档目录」这个概念，不嵌本机路径：这段话是给对面的 AI 看的（I-070）。
func gitPrompt() string {
	return "请你当我的向导，带我把 AgentMemory 的归档目录连到一个 GitHub 仓库。请**一步一步来**：" +
		"每一步说清「我要做什么、在哪里点或敲哪条命令、做完怎么告诉你」，" +
		"我说「这步好了」你再给下一步；别一次把三步都倒给我，也别替我做主。\n" +
		"\n" +
		"第 1 步（建仓库）：让我在 GitHub 上新建一个**空仓库**——不要勾 Add a README file、" +
		".gitignore、Choose a license；建好把仓库地址（形如 git@github.com:账号/仓库.git）给我。\n" +
		"第 2 步（配密钥）：让这台电脑能免密访问它——先看看有没有现成的 SSH 密钥，" +
		"没有就带我生成一对，再把**公钥**加到 GitHub（仓库的 Deploy key，或者我账号的 SSH keys），" +
		"最后用 `ssh -T git@github.com` 试一下通不通。\n" +
		"第 3 步（配远程）：在 AgentMemory 的归档目录里执行 `git init`（如果它还不是 git 仓库），" +
		"再执行 `git remote add origin <仓库地址>`；如果已经配了别的远程，" +
		"就用 `git remote set-url origin <仓库地址>` 改成这个。\n" +
		"\n" +
		"这三步做完就行：AgentMemory 会自己认出仓库地址并试一次连接。" +
		"**不要提交、不要推送**，也不要改动这个目录里已有的文件。"
}

// detectedRemote 认一下本地仓库里配好的远程；认不到就给空串。
//
// 界面不再让人手打地址，所以保存时要现认一次：远程是用户跟着自己的 AI 建的，
// 地址本来就该从本地仓库里读出来（I-029）。
func (a *App) detectedRemote() string {
	guide, err := a.gitGuide("")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(guide.DetectedRemote)
}

// gitConnectHint 把试连的失败翻成一句人能照着做的事（I-022）。
//
// 只给三类固定话术（凭据 / 地址写法 / 网络）；认不出来就照原文说一句——
// 编一个原因比不说更糟。判据与备份失败共用（见 frienderror.go 的 classifyConnect），
// 同一句报错不该在两处被认成两种原因。
func gitConnectHint(err error, out string) string {
	switch classifyConnect(out) {
	case connectAuth:
		return "连不上：像是凭据的问题（仓库要你登录，而这里没拿到可用的凭据）。" +
			"照第 2 步把密钥配好（或者在这台机器的命令行里手动推一次 `git push`），再回来试。"
	case connectAddress:
		return "连不上：像是地址写法的问题（找不到这个仓库）。检查账号名与仓库名；" +
			"GitHub 页面上复制下来的是 `https://github.com/账号/仓库.git` 这种。"
	case connectNetwork:
		return "连不上：像是网络的问题（连不上那个主机）。检查网络与代理设置。"
	}
	return "连不上：" + failureText(err, out)
}

// runGit 跑一次 git。dir 为空时在当前目录跑。
func runGit(dir string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), gitTimeout)
	defer cancel()

	cmd := gitCommand(ctx, args...)
	if strings.TrimSpace(dir) != "" {
		cmd.Dir = dir
	}
	// 别让 git 停下来问凭据：窗口里没有终端能回答它，问了就是卡住。
	cmd.Env = append(cmd.Environ(), "GIT_TERMINAL_PROMPT=0", "GIT_ASKPASS=")

	out, err := cmd.CombinedOutput()
	text := string(out)
	if ctx.Err() != nil {
		return text, fmt.Errorf("git 超时（%s）", gitTimeout)
	}
	return text, err
}

// git 拿到跑 git 命令的那个出口。
func (a *App) git() gitRunner {
	if a.gitRunner == nil {
		return runGit
	}
	return a.gitRunner
}
