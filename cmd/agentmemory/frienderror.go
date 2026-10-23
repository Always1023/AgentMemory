package main

// 这个文件是「出错时怎么说话」（I-034）。
//
// 一条规矩：出错了先说清**哪儿不对**，再给一条**下一步点哪里**；
// 认不出类别就照原话说一句，再请人把原话发过来——编一个原因比不说更糟。
// 话术按**类别**给：前端不自己拼字符串（验收标准 2），同一类错在几处说的是同一套话。

import (
	"strings"
)

// connectKind 是「连不上」的类别。
//
// 试连（引导那一步）与备份失败共用这一套判据，免得同一句 git 报错在两处
// 被认成两种原因。
type connectKind string

const (
	connectAuth    connectKind = "auth"
	connectAddress connectKind = "address"
	connectNetwork connectKind = "network"
	connectOther   connectKind = ""
)

// classifyConnect 认一下这段 git 输出属于哪一类；认不出返回 connectOther。
func classifyConnect(output string) connectKind {
	text := strings.ToLower(output)
	switch {
	case strings.Contains(text, "authentication") ||
		strings.Contains(text, "permission denied") ||
		strings.Contains(text, "could not read username") ||
		strings.Contains(text, "terminal prompts disabled"):
		return connectAuth
	case strings.Contains(text, "does not appear to be a git repository") ||
		strings.Contains(text, "repository not found") ||
		strings.Contains(text, "not found"):
		return connectAddress
	case strings.Contains(text, "could not resolve host") ||
		strings.Contains(text, "unable to access") ||
		strings.Contains(text, "failed to connect") ||
		strings.Contains(text, "proxy"):
		return connectNetwork
	}
	return connectOther
}

// oneLineText 把一句原话压成一行（与 oneLine 同一条规矩，只是收的是字符串）。
func oneLineText(raw string) string {
	return strings.ReplaceAll(strings.TrimSpace(raw), "\n", " ")
}

// friendlyCollectError 把「这一轮没跑完」的原话翻成一句能照着做的事。
//
// 归档写不进去是最常见的一种：多半被别的程序占用，或没有写权限。
func friendlyCollectError(raw, archiveDir string) string {
	text := strings.ToLower(raw)
	switch {
	case strings.Contains(raw, "归档") ||
		strings.Contains(text, "denied") ||
		strings.Contains(text, "permission") ||
		strings.Contains(raw, "占用"):
		where := "归档目录"
		if strings.TrimSpace(archiveDir) != "" {
			where = "归档目录 " + archiveDir
		}
		return "没写进" + where + "（可能被别的程序占用，或没有写权限）。去「设置」换一个目录，" +
			"或关掉占用它的程序，再点一次「增量收集」。"
	case strings.Contains(raw, "定时任务") || strings.Contains(text, "schtasks"):
		return "系统的定时任务没建成（多半是权限）。可以先用界面上的「增量收集」手动收；" +
			"想开定时的话，以管理员身份运行一次这个程序，再来打开这个开关。"
	}
	return "没做成：" + oneLineText(raw) + "。这句话可以照原样发给我，我照着看。"
}

// friendlyBackupError 备份没推上去时说的话：先接住「东西没丢」，再说去哪儿看。
func friendlyBackupError(raw string) string {
	head := "备份没推上去，归档已经在本机了。"
	switch classifyConnect(raw) {
	case connectAuth:
		return head + "像是凭据的问题（对面要你登录，而这里没拿到可用的凭据）——" +
			"去「设置 → 备份方式」点「试连」看提示，或者在命令行手动推一次，让 git 记住你的账号。"
	case connectAddress:
		return head + "像是地址写法的问题（找不到这个仓库）——" +
			"去「设置 → 备份方式」核对仓库地址（形如 git@github.com:账号/仓库.git）。"
	case connectNetwork:
		return head + "像是网络的问题（连不上那个主机）——检查网络与代理，再点一次「增量收集」。"
	}
	return head + "原话：" + oneLineText(raw) + "。这句话可以照原样发给我，我照着看。"
}

// fillRunHints 给一轮结果补上那两句「为什么 + 怎么办」。
//
// 只在这一处拼这两句：前端不拼（验收标准 2）。
func fillRunHints(run *UIRun, archiveDir string) {
	if run == nil {
		return
	}
	if run.Err != "" && run.ErrHint == "" {
		run.ErrHint = friendlyCollectError(run.Err, archiveDir)
	}
	if run.BackupErr != "" && run.BackupHint == "" {
		run.BackupHint = friendlyBackupError(run.BackupErr)
	}
}
