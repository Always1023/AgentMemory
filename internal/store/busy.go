package store

// 锁的重试：本进程内所有访问共用一条连接（见 OpenDB），正常不会撞锁。
// 但别的进程（定时任务、第二个窗口、杀软扫描）可能正占着写锁，而 WAL 下
// 「先读后写」的事务升级失败是立刻报 SQLITE_BUSY 的，busy_timeout 兜不住
// （I-081 验收反馈里的报错就是这一种）。
//
// 写动作因此再退让几次：等一小会儿重来，仍不行才把错误交给上层。
// 只包写路径——读路径撞锁时等人重试没有意义，直接把错误说出来。

import (
	"strings"
	"time"
)

const (
	busyRetries = 4
	busyBackoff = 250 * time.Millisecond
)

// retryBusy 反复调用 op，直到成功、遇到非锁错误、或次数用完。
func retryBusy(op func() error) error {
	var err error
	for i := 0; i < busyRetries; i++ {
		err = op()
		if err == nil || !isBusy(err) {
			return err
		}
		time.Sleep(time.Duration(i+1) * busyBackoff)
	}
	return err
}

// isBusy 认出「库正被别的连接占着」这一类错误。
func isBusy(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "sqlite_busy") || strings.Contains(msg, "database is locked")
}
