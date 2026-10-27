// Package derive 是 L2a 维度提取器层：从 L1 事件流提取多维信息。
//
// 规矩（ARCHITECTURE §2 通信契约）：
//
//   - 一个维度 = 一个提取器 + 注册表一行；新增维度不改真源、不改编排。
//   - 提取器只读 store 的查询接口，算好的条目经 store.ReplaceDimension 落账
//     （主库只有 store 写）；维度表随时可以清掉重建（AC-15）。
//   - 第一批全是机械维度（渠道 / 项目 / 时间 / 规模 / 工具）；话题、决策这类
//     要 LLM 提炼的维度是后置想法，不在这里。
package derive

import (
	"fmt"

	"github.com/Always1023/AgentMemory/internal/store"
)

// Extractor 是一个维度的提取器：从 L1 读数据，算出一个维度的全部条目。
type Extractor interface {
	// Name 是维度的唯一标识（dimensions 表里的 name）。
	Name() string
	// Extract 算出这个维度的全部条目；返回前不清旧账（落账由 Rebuild 统一做）。
	Extract(db *store.DB) ([]store.DimensionRow, error)
}

// Registry 是提取器注册表。
type Registry struct {
	byName map[string]Extractor
	order  []string
}

// NewRegistry 造一个空注册表。
func NewRegistry() *Registry {
	return &Registry{byName: map[string]Extractor{}}
}

// Register 登记一个提取器；重名拒绝（新增维度是纯增量，不该覆盖谁）。
func (r *Registry) Register(e Extractor) error {
	if e == nil || e.Name() == "" {
		return fmt.Errorf("提取器或名字为空")
	}
	if _, dup := r.byName[e.Name()]; dup {
		return fmt.Errorf("维度 %s 已登记", e.Name())
	}
	r.byName[e.Name()] = e
	r.order = append(r.order, e.Name())
	return nil
}

// Names 按登记顺序返回维度名。
func (r *Registry) Names() []string {
	return append([]string(nil), r.order...)
}

// Default 把第一批机械维度装进注册表。
func Default() (*Registry, error) {
	r := NewRegistry()
	for _, e := range []Extractor{
		byChannel{},
		byProject{},
		byDay{},
		sessionStats{},
		toolUsage{},
		// 视角（I-092）：同一张表、同一个注册表，只是口径不同。
		weeklyActivity{},
		weekdayHour{},
		sizeBuckets{},
	} {
		if err := r.Register(e); err != nil {
			return nil, err
		}
	}
	return r, nil
}

// Rebuild 重建指定维度（names 为空 = 全部）：每个提取器算一遍，整段替换落账。
//
// 可重入：跑多少遍结果一致；单独一个维度坏了就整体报错，不留半截账
// （那一维度在库里的旧账会被下次成功的重建替换掉）。
func Rebuild(db *store.DB, registry *Registry, names ...string) error {
	want := names
	if len(want) == 0 {
		want = registry.Names()
	}
	for _, name := range want {
		e, ok := registry.byName[name]
		if !ok {
			return fmt.Errorf("没有登记过的维度 %s", name)
		}
		rows, err := e.Extract(db)
		if err != nil {
			return fmt.Errorf("维度 %s 提取失败: %w", name, err)
		}
		if err := db.ReplaceDimension(name, rows); err != nil {
			return fmt.Errorf("维度 %s 落账失败: %w", name, err)
		}
	}
	return nil
}
