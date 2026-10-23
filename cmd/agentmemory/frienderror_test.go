package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 三类错各自都能看见「哪儿不对」和「下一步点哪里」（I-034 验收标准 2）。
func TestFriendlyErrorsSayWhyAndWhereToGo(t *testing.T) {
	archive := friendlyCollectError("归档 C:\\x\\a.md: 写归档临时文件: Access is denied.", "D:\\归档")
	for _, want := range []string{"归档目录", "D:\\归档", "设置", "增量收集"} {
		if !strings.Contains(archive, want) {
			t.Errorf("归档写不进去那句话里缺 %q：%s", want, archive)
		}
	}

	cases := []struct {
		name string
		raw  string
		want []string
	}{
		{"凭据", "fatal: could not read Username for 'https://github.com': terminal prompts disabled", []string{"凭据", "试连"}},
		{"地址", "remote: Repository not found.", []string{"地址写法", "仓库地址"}},
		{"网络", "fatal: unable to access 'https://github.com/x/y.git/': Could not resolve host: github.com", []string{"网络", "增量收集"}},
	}
	for _, c := range cases {
		got := friendlyBackupError(c.raw)
		if !strings.Contains(got, "归档已经在本机") {
			t.Errorf("%s：先接住「东西没丢」，实际 %s", c.name, got)
		}
		for _, want := range c.want {
			if !strings.Contains(got, want) {
				t.Errorf("%s 那句话里缺 %q：%s", c.name, want, got)
			}
		}
	}
}

// 认不出来就照原话说一句，再请人把原话发过来——不编原因。
func TestFriendlyErrorsFallBackToTheRawWords(t *testing.T) {
	raw := "某个没见过的问题"

	collect := friendlyCollectError(raw, "")
	if !strings.Contains(collect, raw) || !strings.Contains(collect, "发给我") {
		t.Errorf("认不出来时该照原话说，实际 %s", collect)
	}

	backup := friendlyBackupError(raw)
	if !strings.Contains(backup, raw) || !strings.Contains(backup, "发给我") {
		t.Errorf("认不出来时该照原话说，实际 %s", backup)
	}
	if strings.Contains(backup, "凭据") || strings.Contains(backup, "网络") || strings.Contains(backup, "地址写法") {
		t.Errorf("认不出来就别猜原因，实际 %s", backup)
	}
}

// 顶部那句话用的是翻好的那句，不是原话；界面拿到的也带着它（I-034）。
func TestHeadlineCarriesTheFriendlyError(t *testing.T) {
	home := useTempHome(t)
	dir := filepath.Join(home, "归档")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	readyConfig(t, dir)

	ui := newTestApp(t)
	ui.lastRun = &UIRun{
		At:       "10:00",
		Err:      "归档 C:\\x\\a.md: 写归档临时文件: Access is denied.",
		Duration: "1ms",
	}

	state, err := ui.State()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(state.Headline, dir) || !strings.Contains(state.Headline, "增量收集") {
		t.Errorf("顶部该说清哪儿不对、下一步点哪里，实际 %q", state.Headline)
	}
	if state.LastRun == nil || state.LastRun.ErrHint == "" {
		t.Fatal("界面拿到的也该带着那句提示（面板里要显示）")
	}
	if !strings.Contains(state.LastRun.ErrHint, "归档目录") {
		t.Errorf("提示里要说清是归档目录写不进去，实际 %q", state.LastRun.ErrHint)
	}
}

// 备份失败那句也走同一套：先在 State 里补上，界面直接取。
func TestStateAddsTheBackupHint(t *testing.T) {
	home := useTempHome(t)
	dir := filepath.Join(home, "归档")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	readyConfig(t, dir)

	ui := newTestApp(t)
	ui.lastRun = &UIRun{
		At:        "10:00",
		Duration:  "1ms",
		BackupErr: "fatal: unable to access 'https://github.com/x/y.git/': Could not resolve host: github.com",
	}

	state, err := ui.State()
	if err != nil {
		t.Fatal(err)
	}
	if state.LastRun == nil || !strings.Contains(state.LastRun.BackupHint, "网络") {
		t.Errorf("备份失败要按类别给话，实际 %+v", state.LastRun)
	}
}
