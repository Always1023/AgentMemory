package channel

import (
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
)

// fakeAdapter 是测试用的假适配器：不动任何既有代码，就能加一个「渠道」。
type fakeAdapter struct {
	detect func() DetectResult
	parse  func(SourceFile) (Session, error)
}

func (f fakeAdapter) Detect() DetectResult { return f.detect() }

func (f fakeAdapter) Parse(sf SourceFile) (Session, error) {
	if f.parse == nil {
		return Session{}, errors.New("这个假适配器没有实现解析")
	}
	return f.parse(sf)
}

func mustRegistry(t *testing.T, entries ...Entry) *Registry {
	t.Helper()
	r := NewRegistry()
	for _, e := range entries {
		if err := r.Register(e); err != nil {
			t.Fatalf("Register(%s): %v", e.ID, err)
		}
	}
	return r
}

// 验收标准 4：适配器接口只有「检测」与「解析」两个方法，不暴露存储细节。
func TestAdapterInterfaceHasOnlyDetectAndParse(t *testing.T) {
	typ := reflect.TypeOf((*Adapter)(nil)).Elem()

	if got := typ.NumMethod(); got != 2 {
		t.Fatalf("Adapter 有 %d 个方法，验收标准要求 2 个", got)
	}
	want := map[string]bool{"Detect": true, "Parse": true}
	for i := 0; i < typ.NumMethod(); i++ {
		if name := typ.Method(i).Name; !want[name] {
			t.Errorf("Adapter 多出方法 %s", name)
		}
	}
}

// 验收标准 1：检测能区分「可用」与「未安装」。
func TestDetectAllReportsThreeStates(t *testing.T) {
	file := SourceFile{Path: "/x/a.jsonl", Size: 10, ModTime: time.Unix(0, 0)}

	r := mustRegistry(t,
		Entry{ID: "installed", Name: "装了的", Adapter: fakeAdapter{detect: func() DetectResult {
			return DetectResult{Status: StatusAvailable, Root: "/x", Files: []SourceFile{file}}
		}}},
		Entry{ID: "missing", Name: "没装的", Adapter: fakeAdapter{detect: func() DetectResult {
			return DetectResult{Status: StatusNotInstalled}
		}}},
		Entry{ID: "broken", Name: "坏了的", Adapter: fakeAdapter{detect: func() DetectResult {
			return DetectResult{Status: StatusFailed, Err: errors.New("读不了")}
		}}},
	)

	got := map[string]Report{}
	for _, rep := range r.DetectAll() {
		got[rep.ID] = rep
	}

	if rep := got["installed"]; rep.Status != StatusAvailable || len(rep.Files) != 1 {
		t.Errorf("可用渠道应当带着源文件: %+v", rep)
	}
	if rep := got["missing"]; rep.Status != StatusNotInstalled {
		t.Errorf("未安装渠道状态 = %q，想要 %q", rep.Status, StatusNotInstalled)
	}
	if rep := got["broken"]; rep.Status != StatusFailed || rep.Err == nil {
		t.Errorf("失败渠道应当有状态与原因: %+v", rep)
	}
}

// 验收标准 3：单个渠道检测抛错时，其余渠道结果不受影响。
func TestDetectIsolatesFailures(t *testing.T) {
	r := mustRegistry(t,
		Entry{ID: "a-broken", Name: "报错的", Adapter: fakeAdapter{detect: func() DetectResult {
			return DetectResult{Status: StatusFailed, Err: errors.New("读不了")}
		}}},
		Entry{ID: "b-panics", Name: "崩了的", Adapter: fakeAdapter{detect: func() DetectResult {
			panic("适配器作者写错了")
		}}},
		Entry{ID: "c-fine", Name: "好着的", Adapter: fakeAdapter{detect: func() DetectResult {
			return DetectResult{Status: StatusAvailable, Root: "/x"}
		}}},
	)

	reports := r.DetectAll()
	if len(reports) != 3 {
		t.Fatalf("应当有三个渠道的报告，得到 %d 个", len(reports))
	}

	byID := map[string]Report{}
	for _, rep := range reports {
		byID[rep.ID] = rep
	}

	if rep := byID["a-broken"]; rep.Status != StatusFailed {
		t.Errorf("报错渠道状态 = %q，想要 %q", rep.Status, StatusFailed)
	}
	if rep := byID["b-panics"]; rep.Status != StatusFailed || rep.Err == nil {
		t.Errorf("崩溃渠道应当被收敛成失败并带原因: %+v", rep)
	}
	if rep := byID["c-fine"]; rep.Status != StatusAvailable {
		t.Errorf("好的渠道被前两个带坏了: %+v", rep)
	}
}

// 验收标准 2：新增一个渠道不改动既有代码——只 Register 一条，别的渠道照旧。
func TestAddingAChannelNeedsNoChangeToExistingCode(t *testing.T) {
	existing := func() DetectResult { return DetectResult{Status: StatusAvailable, Root: "/old"} }

	r := mustRegistry(t, Entry{ID: "old", Name: "老渠道", Adapter: fakeAdapter{detect: existing}})
	before := r.DetectAll()

	// 「新增一个渠道」：本包与既有代码一行都不动。
	if err := r.Register(Entry{ID: "new", Name: "新渠道", Adapter: fakeAdapter{
		detect: func() DetectResult { return DetectResult{Status: StatusAvailable, Root: "/new"} },
	}}); err != nil {
		t.Fatalf("Register: %v", err)
	}

	after := r.DetectAll()
	if len(after) != len(before)+1 {
		t.Fatalf("注册后报告数 = %d，想要 %d", len(after), len(before)+1)
	}

	byID := map[string]Report{}
	for _, rep := range after {
		byID[rep.ID] = rep
	}
	if byID["old"].Root != "/old" {
		t.Errorf("既有渠道被改动了: %+v", byID["old"])
	}
	if byID["new"].Root != "/new" {
		t.Errorf("新渠道没进来: %+v", byID["new"])
	}
}

func TestRegisterRejectsBadEntries(t *testing.T) {
	cases := map[string]Entry{
		"缺 ID": {Name: "x", Adapter: fakeAdapter{}},
		"缺显示名": {ID: "x", Adapter: fakeAdapter{}},
		"缺适配器": {ID: "x", Name: "x"},
	}
	for name, e := range cases {
		r := NewRegistry()
		if err := r.Register(e); err == nil {
			t.Errorf("%s：应当报错", name)
		}
	}

	r := NewRegistry()
	e := Entry{ID: "dup", Name: "重复", Adapter: fakeAdapter{}}
	if err := r.Register(e); err != nil {
		t.Fatalf("第一次注册应当成功: %v", err)
	}
	if err := r.Register(e); err == nil {
		t.Error("重复注册同一个 ID 应当报错")
	}
}

func TestDetectNormalizesOddAdapterBehaviour(t *testing.T) {
	// 这几种都该收敛成「失败 + 有原因」。
	r := mustRegistry(t,
		Entry{ID: "no-status", Name: "没给状态", Adapter: fakeAdapter{detect: func() DetectResult {
			return DetectResult{Root: "/x"}
		}}},
		Entry{ID: "weird-status", Name: "怪状态", Adapter: fakeAdapter{detect: func() DetectResult {
			return DetectResult{Status: Status("whatever"), Files: []SourceFile{{Path: "/x/a"}}}
		}}},
		Entry{ID: "failed-no-reason", Name: "失败不给原因", Adapter: fakeAdapter{detect: func() DetectResult {
			return DetectResult{Status: StatusFailed}
		}}},
	)

	for _, rep := range r.DetectAll() {
		if rep.Status != StatusFailed {
			t.Errorf("%s 状态 = %q，不守规矩的适配器都应当收敛成 %q", rep.ID, rep.Status, StatusFailed)
		}
		if rep.Err == nil {
			t.Errorf("%s 收敛成失败后必须有原因", rep.ID)
		}
		if len(rep.Files) != 0 {
			t.Errorf("%s 非可用状态不该带源文件: %+v", rep.ID, rep.Files)
		}
	}

	// 报了「未安装」却给了文件：状态照它说的算，但文件必须清掉——
	// 未安装就不该有东西交给下游去解析。
	r2 := mustRegistry(t, Entry{ID: "contradictory", Name: "自相矛盾", Adapter: fakeAdapter{detect: func() DetectResult {
		return DetectResult{Status: StatusNotInstalled, Files: []SourceFile{{Path: "/x/a"}}}
	}}})
	rep := r2.DetectAll()[0]
	if rep.Status != StatusNotInstalled {
		t.Errorf("状态 = %q，想要 %q", rep.Status, StatusNotInstalled)
	}
	if rep.Err != nil {
		t.Errorf("未安装不是错误，不该带原因: %v", rep.Err)
	}
	if len(rep.Files) != 0 {
		t.Errorf("未安装还带着源文件: %+v", rep.Files)
	}
}

func TestEntriesSortedByID(t *testing.T) {
	r := mustRegistry(t,
		Entry{ID: "charlie", Name: "c", Adapter: fakeAdapter{}},
		Entry{ID: "alpha", Name: "a", Adapter: fakeAdapter{}},
		Entry{ID: "bravo", Name: "b", Adapter: fakeAdapter{}},
	)

	var ids []string
	for _, e := range r.Entries() {
		ids = append(ids, e.ID)
	}
	if got := strings.Join(ids, ","); got != "alpha,bravo,charlie" {
		t.Errorf("顺序 = %s，想要按 ID 升序", got)
	}
}
