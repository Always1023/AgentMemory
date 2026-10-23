package backup

import (
	"strings"
	"testing"
	"time"
)

// fakeMethod 是测试用的假方式：不动任何既有代码，就能加一种「备份方式」。
type fakeMethod struct {
	back func(Settings) Result
}

func (f fakeMethod) Back(s Settings) Result {
	if f.back == nil {
		return Result{Detail: "假方式什么都没做"}
	}
	return f.back(s)
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

func TestRegisterRejectsBadEntries(t *testing.T) {
	cases := map[string]Entry{
		"缺 ID": {Name: "x", Method: fakeMethod{}},
		"缺显示名": {ID: "x", Method: fakeMethod{}},
		"缺实现":  {ID: "x", Name: "x"},
	}
	for name, e := range cases {
		if err := NewRegistry().Register(e); err == nil {
			t.Errorf("%s：应当报错", name)
		}
	}

	r := NewRegistry()
	dup := Entry{ID: "dup", Name: "重复", Method: fakeMethod{}}
	if err := r.Register(dup); err != nil {
		t.Fatalf("第一次注册应当成功: %v", err)
	}
	if err := r.Register(dup); err == nil {
		t.Error("重复注册同一个 ID 应当报错")
	}
}

func TestRunDispatchesToTheRightMethod(t *testing.T) {
	var got Settings
	r := mustRegistry(t,
		Entry{ID: "a", Name: "甲", Method: fakeMethod{back: func(s Settings) Result {
			got = s
			return Result{Changed: true, Detail: "甲干了活"}
		}}},
		Entry{ID: "b", Name: "乙", Method: fakeMethod{back: func(Settings) Result {
			t.Error("不该调到乙")
			return Result{}
		}}},
	)

	settings := Settings{Dir: "D:/归档", Remote: "git@example.com:x.git", Branch: "main", Timeout: time.Minute}
	result, err := r.Run("a", settings)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !result.Changed || result.Detail != "甲干了活" {
		t.Errorf("结果 = %+v", result)
	}
	if got != settings {
		t.Errorf("传给实现的东西 = %+v，想要 %+v", got, settings)
	}
}

func TestRunRejectsUnknownID(t *testing.T) {
	r := mustRegistry(t, Entry{ID: "a", Name: "甲", Method: fakeMethod{}})

	if _, err := r.Run("没有这个方式", Settings{}); err == nil {
		t.Error("不认识的备份方式应当报错，而不是当成功混过去")
	} else if !strings.Contains(err.Error(), "没有这个方式") {
		t.Errorf("错误里应当带上那个 ID: %v", err)
	}
}

// 验收标准 6：新增一种备份方式不改动既有代码。
func TestAddingAMethodNeedsNoChangeToExistingCode(t *testing.T) {
	old := func() Result { return Result{Detail: "老方式"} }
	r := mustRegistry(t, Entry{ID: "old", Name: "老方式", Method: fakeMethod{back: func(Settings) Result { return old() }}})
	before, err := r.Run("old", Settings{})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	// 「新增一种备份方式」：本包与既有代码一行都不动。
	if err := r.Register(Entry{ID: "new", Name: "新方式", Method: fakeMethod{back: func(Settings) Result {
		return Result{Detail: "新方式"}
	}}}); err != nil {
		t.Fatalf("Register: %v", err)
	}

	after, err := r.Run("old", Settings{})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if after != before {
		t.Errorf("既有方式被改动了：%+v → %+v", before, after)
	}
	fresh, err := r.Run("new", Settings{})
	if err != nil {
		t.Fatalf("Run(new): %v", err)
	}
	if fresh.Detail != "新方式" {
		t.Errorf("新方式没进来: %+v", fresh)
	}
	if len(r.Entries()) != 2 {
		t.Errorf("方式数 = %d，想要 2", len(r.Entries()))
	}
}

func TestEntriesSortedByID(t *testing.T) {
	r := mustRegistry(t,
		Entry{ID: "c", Name: "c", Method: fakeMethod{}},
		Entry{ID: "a", Name: "a", Method: fakeMethod{}},
		Entry{ID: "b", Name: "b", Method: fakeMethod{}},
	)

	var ids []string
	for _, e := range r.Entries() {
		ids = append(ids, e.ID)
	}
	if strings.Join(ids, ",") != "a,b,c" {
		t.Errorf("顺序 = %v，想要按 ID 升序", ids)
	}

	if _, ok := r.Lookup("b"); !ok {
		t.Error("Lookup 应当找得到已注册的 ID")
	}
	if _, ok := r.Lookup("没有"); ok {
		t.Error("Lookup 不该找到没注册的 ID")
	}
}
