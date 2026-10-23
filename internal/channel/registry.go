package channel

import (
	"errors"
	"fmt"
	"sort"
	"sync"
)

// Entry 是注册表里的一条渠道：身份 + 适配器。
type Entry struct {
	// ID 是渠道标识，如 "claude-code"。全小写、连字符分隔，全项目唯一。
	ID string
	// Name 是给人看的渠道名，如 "Claude Code"。
	Name string
	// Adapter 负责发现与解析。
	Adapter Adapter
}

// Registry 是渠道注册表。
//
// 新增一个渠道 = 建一个 Entry 并 Register，本包与其他既有代码一行都不用改动。
type Registry struct {
	mu      sync.RWMutex
	entries map[string]Entry
}

// NewRegistry 建一个空注册表。
func NewRegistry() *Registry {
	return &Registry{entries: map[string]Entry{}}
}

// Register 把一个渠道加进注册表。必填项缺失或 ID 重复时返回错误。
func (r *Registry) Register(e Entry) error {
	switch {
	case e.ID == "":
		return errors.New("渠道 ID 不能为空")
	case e.Name == "":
		return fmt.Errorf("渠道 %s 缺少显示名", e.ID)
	case e.Adapter == nil:
		return fmt.Errorf("渠道 %s 没有适配器", e.ID)
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	if _, dup := r.entries[e.ID]; dup {
		return fmt.Errorf("渠道 %s 已经注册过", e.ID)
	}
	r.entries[e.ID] = e
	return nil
}

// Entries 返回已注册的渠道，按 ID 排序。
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

// DetectAll 逐个检测，返回全部渠道的报告。
//
// 一个渠道失败或崩溃都不影响其他渠道（AC-05）。
func (r *Registry) DetectAll() []Report {
	entries := r.Entries()
	reports := make([]Report, 0, len(entries))
	for _, e := range entries {
		reports = append(reports, Report{
			ID:           e.ID,
			Name:         e.Name,
			DetectResult: detectSafely(e.Adapter),
		})
	}
	return reports
}

// detectSafely 跑一次检测，把崩溃与不合格的返回值收敛成三态。
//
// 适配器是「别人写的东西」，不能假设它守规矩。
func detectSafely(a Adapter) (res DetectResult) {
	defer func() {
		if p := recover(); p != nil {
			res = DetectResult{Status: StatusFailed, Err: fmt.Errorf("检测时崩溃: %v", p)}
		}
	}()

	res = a.Detect()

	if res.Err != nil {
		res.Status = StatusFailed
	}

	switch res.Status {
	case StatusNotInstalled:
		res.Files = nil
	case StatusFailed:
		res.Files = nil
		if res.Err == nil {
			res.Err = errors.New("适配器报了失败但没给原因")
		}
	case StatusAvailable:
	default:
		res = DetectResult{
			Status: StatusFailed,
			Err:    fmt.Errorf("适配器返回了不认识的状态 %q", string(res.Status)),
		}
	}
	return res
}
