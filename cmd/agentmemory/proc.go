package main

import (
	"context"
	"os/exec"
	"syscall"
)

// 这一层跑外部命令（git、schtasks）的地方都从这两个构造器出去。
//
// 为什么要有它们：Windows 上从图形程序里起一个控制台程序，**默认会弹一个黑窗口**
// 再消失。人点一下「备份方式」或切到 Git，后端要连着跑好几条命令（`git --version`、
// `git remote get-url`、`schtasks`），屏幕上看到的就是「快速闪出几个窗口，又快速消失」
// （人报的「重影卡顿」就是这么来的，不是画面残影）。
//
// 统一设 HideWindow：不弹窗，命令照常跑。

// gitCommand 造一条 git 命令（不弹控制台窗口）。
func gitCommand(ctx context.Context, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, "git", args...)
	hideWindow(cmd)
	return cmd
}

// schedCommand 造一条 schtasks 命令（不弹控制台窗口）。
func schedCommand(ctx context.Context, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, "schtasks.exe", args...)
	hideWindow(cmd)
	return cmd
}

// hideWindow 让这次外部命令不弹控制台窗口。
func hideWindow(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
}
