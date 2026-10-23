// Package backup 定义备份方式的契约与注册表。
//
// 备份是把归档目录交出去（默认 git 仓库），本包不知道 git 怎么调、
// 也不读归档目录里的内容（docs/ARCHITECTURE.md 模块表：不读渠道数据）。
//
// 依赖方向：**不 import 任何其他业务模块**，包括 `config`——
// 备份要用的东西（目录、仓库地址、超时）由调用方取好放进 `Settings`。
// 这与 `config` 同类：一个只被依赖、不依赖别人的支撑/环节模块。
package backup

import (
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"
)

// Settings 是一次备份要用到的输入，由调用方从配置里取好。
type Settings struct {
	// Dir 是要备份出去的归档目录。
	Dir string
	// Remote 是远程仓库地址；为空表示没配。
	Remote string
	// Branch 是要推的分支；留空按实现自己的默认值。
	Branch string
	// Timeout 是单次外部命令的超时（AC-07）；留空按实现自己的默认值。
	Timeout time.Duration
	// Message 是提交信息；留空按实现自己的默认值。
	Message string
}

// Result 是一次备份的结果。
type Result struct {
	// Changed 报告这次是否真的动了东西（git 是「有没有产生提交」）。
	Changed bool
	// Detail 是给人看的一句话，成功也留着（如「4 个文件，已提交并推送」）。
	Detail string
	// Err 非空表示这次备份失败。**归档目录本身不受影响**——
	// 备份是往外送，失败不该反过来损坏要送的东西（验收标准 4）。
	Err error
}

// Method 是一种备份方式的契约。
//
// 只有「备份一次」一件事：不暴露任何 git / 网盘细节。
// 与渠道适配器同一路数——身份（ID、显示名）记在 `Entry` 上，实现只管干活。
type Method interface {
	// Back 把 Settings.Dir 备份出去。
	//
	// 「没什么可备份」不是错误（目录还没有、没配仓库地址），
	// 那种情况返回 Detail 说明、Err 为空。
	Back(Settings) Result
}

// Entry 是注册表里的一种备份方式：身份 + 实现。
type Entry struct {
	// ID 是方式标识，与配置里的 backup.mode 取值一致（git / none / sync）。
	ID string
	// Name 是给人看的名字。
	Name string
	// Method 负责真正干活。
	Method Method
}

// Registry 是备份方式注册表。
//
// 新增一种备份方式 = 写一个实现 + Register 一条，本包与其他既有代码一行都不用改。
type Registry struct {
	mu      sync.RWMutex
	entries map[string]Entry
}

// NewRegistry 建一个空注册表。
func NewRegistry() *Registry {
	return &Registry{entries: map[string]Entry{}}
}

// Register 注册一种备份方式。必填项缺失或 ID 重复时返回错误。
func (r *Registry) Register(e Entry) error {
	switch {
	case e.ID == "":
		return errors.New("备份方式 ID 不能为空")
	case e.Name == "":
		return fmt.Errorf("备份方式 %s 缺少显示名", e.ID)
	case e.Method == nil:
		return fmt.Errorf("备份方式 %s 没有实现", e.ID)
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	if _, dup := r.entries[e.ID]; dup {
		return fmt.Errorf("备份方式 %s 已经注册过", e.ID)
	}
	r.entries[e.ID] = e
	return nil
}

// Entries 返回已注册的方式，按 ID 排序。
func (r *Registry) Entries() []Entry {
	r.mu.RLock()
	defer r.mu.RUnlock()

	out := make([]Entry, 0, len(r.entries))
	for _, e := range r.entries {
		out = append(out, e)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// Lookup 按 ID 取一种方式。
func (r *Registry) Lookup(id string) (Entry, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	e, ok := r.entries[id]
	return e, ok
}

// Run 按 ID 跑一次备份。
//
// ID 不认识时返回错误——那是配置错了，不该被当成「备份成功」混过去。
func (r *Registry) Run(id string, s Settings) (Result, error) {
	e, ok := r.Lookup(id)
	if !ok {
		return Result{}, fmt.Errorf("不认识的备份方式 %q", id)
	}
	return e.Method.Back(s), nil
}
