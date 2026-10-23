// Package none 实现「仅本地」备份：什么都不做。
//
// 它的存在是有意义的：让「不备份」也是一个明确的选择，
// 而不是「把配置留空、然后碰运气」。选了它，整轮收集不发起任何网络访问。
package none

import "github.com/Always1023/AgentMemory/internal/backup"

// ID 与 Name 是注册表用的身份。ID 与配置里的 backup.mode 取值一致。
const (
	ID   = "none"
	Name = "仅本地"
)

// Detail 是跑完之后报告的一句话。
const Detail = "仅本地：归档只留在本机，没有做任何备份"

// Method 实现 backup.Method。
type Method struct{}

// Back 什么都不做，也不算失败。
func (Method) Back(backup.Settings) backup.Result {
	return backup.Result{Detail: Detail}
}
