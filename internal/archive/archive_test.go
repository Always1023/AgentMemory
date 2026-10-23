package archive

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/Always1023/AgentMemory/internal/channel"
)

const sampleMarkdown = "# 项目结构梳理\n\n正文\n"

func sampleSession() channel.Session {
	return channel.Session{
		Channel:     "claude-code",
		ID:          "11111111-2222-3333-4444-555555555555",
		Title:       "项目结构梳理",
		ProjectPath: `D:\work\demo`,
		StartedAt:   time.Date(2026, 10, 9, 13, 50, 56, 0, time.UTC),
		Messages: []channel.Message{
			{Role: channel.RoleUser, Text: "帮我看一下这个项目的结构"},
			{Role: channel.RoleAssistant, Text: "这是一个 Go 项目。"},
		},
	}
}

// markdownFiles 列出 root 下所有 .md 与 .tmp，用来数「归档出了几份文件」。
func markdownFiles(t *testing.T, root string) []string {
	t.Helper()
	var out []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		out = append(out, filepath.ToSlash(path))
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// 验收标准 1（I-008）＋ 修 I-018 之后的形态：路径符合
// <归档目录>/<渠道>/<项目>/<日期>-<标题>-<会话标识哈希>.md。
func TestPlanMatchesDocumentedLayout(t *testing.T) {
	root := filepath.Join("D:", "归档")
	session := sampleSession()

	got := Plan(root, session, "Claude Code")
	want := filepath.Join(root, "claude-code", "D--work-demo",
		"2026-10-09-项目结构梳理-"+identity(session)+".md")
	if got != want {
		t.Errorf("路径 = %q\n想要 %q", got, want)
	}
	if !regexp.MustCompile(`^2026-10-09-项目结构梳理-[0-9a-f]{10}\.md$`).MatchString(filepath.Base(got)) {
		t.Errorf("文件名 = %q，末尾应当是 %d 位十六进制", filepath.Base(got), identityLength)
	}
}

func TestPlanFallsBackForMissingPieces(t *testing.T) {
	root := filepath.Join("D:", "归档")

	empty := channel.Session{}
	got := Plan(root, empty, "")
	if !strings.HasPrefix(filepath.Base(got), noDate+"-"+untitled+"-") {
		t.Errorf("全空的会话路径 = %q，日期与标题段应当还是占位", got)
	}
	if !strings.Contains(got, noChannel) || !strings.Contains(got, noProject) {
		t.Errorf("全空的会话路径 = %q，缺少渠道/项目占位", got)
	}
	if !strings.HasSuffix(got, ".md") {
		t.Errorf("路径 = %q", got)
	}

	// 渠道标识为空时用显示名兜底。
	named := channel.Session{}
	if got := Plan(root, named, "Codex CLI"); !strings.Contains(got, "Codex CLI") {
		t.Errorf("渠道标识为空时应当退回显示名: %q", got)
	}
}

// 验收标准 1 的机制侧（I-018）：不同会话绝不共用同一个文件。
//
// 这条用例还原的就是真实数据上的那次事故：同渠道、同项目、同一天、同标题，
// 只有会话标识不同——修之前两者算出同一个路径，后者把前者覆盖。
func TestDifferentSessionsNeverShareAFile(t *testing.T) {
	root := t.TempDir()

	first := sampleSession()
	first.ID = "3ea41e56-804d-4a42-a909-0e98fae9c331"
	first.Messages = []channel.Message{{Role: channel.RoleUser, Text: "阅读当前看板"}}

	second := first
	second.ID = "a0dcad0c-011b-4355-8bc5-1f82eb19ac2b"
	// 除标识以外全同：同渠道、同项目、同一天、同标题。
	second.Title = first.Title
	second.StartedAt = first.StartedAt

	if Plan(root, first, "Claude Code") == Plan(root, second, "Claude Code") {
		t.Fatal("两个不同会话算出了同一个路径")
	}

	writeArchive := func(s channel.Session, markdown string) string {
		t.Helper()
		path, err := Write(root, s, "Claude Code", markdown)
		if err != nil {
			t.Fatalf("Write: %v", err)
		}
		return path
	}
	pathFirst := writeArchive(first, "# 第一个会话\n")
	pathSecond := writeArchive(second, "# 第二个会话\n")

	// 两份都在，内容各自完整——后者没有把前者盖掉。
	raw, err := os.ReadFile(pathFirst)
	if err != nil {
		t.Fatalf("第一个会话的档案没了: %v", err)
	}
	if string(raw) != "# 第一个会话\n" {
		t.Errorf("第一个会话的内容被改成了 %q", string(raw))
	}
	raw, err = os.ReadFile(pathSecond)
	if err != nil {
		t.Fatalf("第二个会话的档案没了: %v", err)
	}
	if string(raw) != "# 第二个会话\n" {
		t.Errorf("第二个会话的内容 = %q", string(raw))
	}

	if files := markdownFiles(t, root); len(files) != 2 {
		t.Errorf("目录里应当是 2 份档案，实际 %d 份: %v", len(files), files)
	}
}

// 真凭实据的第二处：Codex 的「续写」把**同一个会话 ID** 写进多个 rollout 文件。
// 只按会话 ID 定名，后一段会把前一段整个盖掉——每个源文件都得有自己的档案。
func TestSameSessionAcrossResumedFilesKeepsItsOwnArchive(t *testing.T) {
	root := t.TempDir()

	// 同一个会话 ID，五个源文件（真实数据里的形状：…-<会话ID>_<新ID>.jsonl）。
	const sessionID = "01a0d25b-1074-7df0-8687-e7380d58d755"
	base := sampleSession()
	base.Channel = "codex"
	base.ID = sessionID
	base.Title = "帮我把站点桩的时序图刷进文档"

	files := []string{
		`C:\Users\test\.codex\sessions\2026\09\24\rollout-2026-09-24T15-39-38-` + sessionID + `.jsonl`,
		`C:\Users\test\.codex\sessions\2026\09\24\rollout-2026-09-24T15-41-43-` + sessionID + `_01a0d25c-f8a1-7b21-8289-774ac6e104c6.jsonl`,
		`C:\Users\test\.codex\sessions\2026\09\24\rollout-2026-09-24T15-43-46-` + sessionID + `_01a0d25e-d93e-7350-be26-76c6f5b7b2b7.jsonl`,
	}

	paths := map[string]bool{}
	for i, source := range files {
		session := base
		session.SourceFile = source
		path, err := Write(root, session, "Codex CLI", fmt.Sprintf("# 第 %d 段\n", i+1))
		if err != nil {
			t.Fatalf("Write: %v", err)
		}
		if paths[path] {
			t.Fatalf("第 %d 段和历史某段落到了同一个文件：%s", i+1, path)
		}
		paths[path] = true
	}

	if got := len(markdownFiles(t, root)); got != len(files) {
		t.Errorf("三个源文件应当产出三份档案，实际 %d 份", got)
	}

	// 每一段的内容都还在（没有互相覆盖）。
	for i, path := range sortedKeys(paths) {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("第 %d 段的档案没了: %v", i+1, err)
		}
		if !strings.Contains(string(raw), "第 ") {
			t.Errorf("第 %d 段的内容不对：%q", i+1, string(raw))
		}
	}
}

// sortedKeys 让「逐份比对」的顺序稳定，报错时才看得出是第几段。
func sortedKeys(set map[string]bool) []string {
	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// 验收标准 3：会话标识为空时文件名仍有区分度。
func TestIdentityFallsBackWhenSessionIDIsEmpty(t *testing.T) {
	root := filepath.Join("D:", "归档")

	// 标识为空的两个会话，来源文件不同 → 路径必须不同。
	a := sampleSession()
	a.ID = ""
	a.SourceFile = `C:\Users\test\.claude\projects\X\a.jsonl`
	b := sampleSession()
	b.ID = ""
	b.SourceFile = `C:\Users\test\.claude\projects\X\b.jsonl`
	if Plan(root, a, "Claude Code") == Plan(root, b, "Claude Code") {
		t.Error("标识为空、来源文件不同，两个会话仍然撞了名")
	}

	// 同一个会话算两次必须一致（不然「重复收集只更新一份」就崩了）。
	if identity(a) != identity(a) {
		t.Error("同一会话的标识不稳定")
	}
	if Plan(root, a, "Claude Code") != Plan(root, a, "Claude Code") {
		t.Error("同一会话的路径不稳定")
	}

	// 开始时间也参与兜底：来源文件都空时，靠时间拉开区分度。
	c, d := sampleSession(), sampleSession()
	c.ID, c.SourceFile = "", ""
	d.ID, d.SourceFile = "", ""
	d.StartedAt = c.StartedAt.Add(time.Minute)
	if identity(c) == identity(d) {
		t.Error("来源文件与标识都空时，开始时间应当提供区分度")
	}
}

// 会话标识的形状不由我们定，所以文件名里放的是整串标识的哈希：
// 同一标识永远同一个哈希，不同标识（几乎）不会撞。
func TestIdentityIsStableShortHash(t *testing.T) {
	hexOnly := regexp.MustCompile(`^[0-9a-f]+$`)

	seen := map[string]string{}
	for _, id := range []string{
		"019b0000-1111-7222-8333-444455556666", // Codex（UUIDv7）
		"7933988c-2fd8-4bbd-9dda-baec8b81872f", // Continue
		"11111111-2222-3333-4444-555555555555", // Claude Code
		"abc",
		"带中文的标识",
	} {
		s := sampleSession()
		s.ID = id

		got := identity(s)
		if len(got) != identityLength || !hexOnly.MatchString(got) {
			t.Errorf("identity(%q) = %q，应当是 %d 位小写十六进制", id, got, identityLength)
		}
		if identity(s) != got {
			t.Errorf("identity(%q) 不稳定", id)
		}
		if other, dup := seen[got]; dup {
			t.Errorf("identity(%q) 与 identity(%q) 撞了：%q", id, other, got)
		}
		seen[got] = id
	}
}

// 真实数据上就是这么漏的：Codex 的标识是 UUIDv7，**前 8 位是毫秒时间戳**，
// 同一分钟左右开的会话前缀完全一样。按前缀取会撞名，按整串哈希不会。
func TestUUIDv7StylePrefixesDoNotCollide(t *testing.T) {
	first := sampleSession()
	first.ID = "019b064b-d476-7fa1-a9bc-9dc95f88451d"
	second := sampleSession()
	second.ID = "019b064b-d476-7fa1-a9bc-9dc95f88451e" // 前缀全同，只有尾号差一位

	if strings.HasPrefix(first.ID, second.ID[:8]) != true {
		t.Fatal("这条用例的前提变了：两个标识应当在头 8 位上相同")
	}
	if identity(first) == identity(second) {
		t.Errorf("前缀相同的两个 UUIDv7 撞了名：%q", identity(first))
	}
	if Plan("D:\\归档", first, "Codex CLI") == Plan("D:\\归档", second, "Codex CLI") {
		t.Error("前缀相同的两个 UUIDv7 算出了同一个归档路径")
	}
}

func TestProjectPathVariants(t *testing.T) {
	cases := map[string]string{
		`D:\work\demo`:  "D--work-demo",
		`D:\work\demo\`: "D--work-demo",
		"/home/me/proj": "home-me-proj",
		`C:\a-b`:        "C--a-b",
		"":              noProject,
		"   ":           noProject,
		"CON":           "_CON",
		`D:\工作\我的项目`:    "D--工作-我的项目",
	}
	for projectPath, want := range cases {
		if got := projectDir(projectPath); got != want {
			t.Errorf("projectDir(%q) = %q，想要 %q", projectPath, got, want)
		}
	}

	// 已知局限（代码里也写着）：盘符冒号与路径分隔符都换成 `-`，
	// 所以这两个路径会映射到同一个目录名。要互不撞名得给字面连接号转义，
	// 代价是所有带连接号的项目名都变丑。这条断言反过来盯着这件事：
	// 哪天实现改成能区分了，它会红，提醒把注释一起改掉。
	if projectDir(`C:\a-b`) != projectDir(`C:\a\b`) {
		t.Errorf("预期中的已知局限不再成立（%q / %q），代码里那段注释该删了",
			projectDir(`C:\a-b`), projectDir(`C:\a\b`))
	}
}

// 验收标准 2：标题含 \ / : * ? " < > | 或超长时，文件名仍合法。
//
// 判据是**真往磁盘上写**——文件名合不合法，操作系统说了算。
func TestNastyTitlesProduceLegalFiles(t *testing.T) {
	nasty := map[string]string{
		"反斜杠":    `标题\里带反斜杠`,
		"正斜杠":    "标题/里带正斜杠",
		"冒号":     "标题:里带冒号",
		"星号":     "标题*里带星号",
		"问号":     "标题?里带问号",
		"双引号":    `标题"里带双引号`,
		"尖括号":    "标题<里带尖括号>",
		"竖线":     "标题|里带竖线",
		"控制字符":   "标题\x01\x02里带控制字符",
		"结尾是点":   "标题结尾是点...",
		"结尾是空格":  "标题结尾是空格   ",
		"全是非法字符": `\/:*?"<>|`,
		"空标题":    "",
		"只有空白":   "   ",
		"保留名":    "CON",
		"保留名带扩展": "nul.txt",
		"COM 口":  "COM1",
		"超长":     strings.Repeat("长", 300),
		"超长且带非法": strings.Repeat(`a\b:`, 200),
		"换行":     "标题里有\n换行",
		"只有连接号":  "---",
	}

	for name, title := range nasty {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			session := sampleSession()
			session.Title = title

			path, err := Write(root, session, "Claude Code", sampleMarkdown)
			if err != nil {
				t.Fatalf("写不出去: %v", err)
			}
			info, err := os.Stat(path)
			if err != nil || info.IsDir() {
				t.Fatalf("文件不在: %v", err)
			}

			base := filepath.Base(path)
			for _, r := range illegalInName {
				if strings.ContainsRune(base, r) {
					t.Errorf("文件名里还留着 %q：%s", r, base)
				}
			}
			if strings.HasSuffix(base, " ") || strings.HasSuffix(base, ".") {
				t.Errorf("文件名不该以空格或点结尾：%q", base)
			}
			if got := len([]rune(base)); got > 10+1+titleLimit+1+identityLength+len(".md") {
				t.Errorf("文件名 %d 个字符，超出上限：%s", got, base)
			}
			if strings.TrimSuffix(base, ".md") == "" {
				t.Errorf("文件名只剩扩展名：%q", base)
			}

			// 再写一次要落到同一个文件（标题不同但同一会话的重复收集也稳）。
			again, err := Write(root, session, "Claude Code", sampleMarkdown)
			if err != nil {
				t.Fatalf("第二次写不出去: %v", err)
			}
			if again != path {
				t.Errorf("同一会话两次写出不同路径：%q / %q", path, again)
			}
			if files := markdownFiles(t, root); len(files) != 1 {
				t.Errorf("目录里应当只有 1 份文件，实际 %d 份: %v", len(files), files)
			}
		})
	}
}

func TestTitleSegmentCollapsesAndFallsBack(t *testing.T) {
	cases := map[string]string{
		`问题?? 求解答`:  "问题-求解答",
		`a\\b:::c`:  "a-b-c",
		"":          untitled,
		"   ...   ": untitled,
		"CON":       "_CON",
		"com9":      "_com9",
		"CON1":      "CON1",
	}
	for title, want := range cases {
		if got := titleSegment(title); got != want {
			t.Errorf("titleSegment(%q) = %q，想要 %q", title, got, want)
		}
	}
}

// 验收标准 3：同一会话重复归档不产生第二份文件，且内容被更新。
func TestRepeatedArchiveUpdatesInsteadOfAdding(t *testing.T) {
	root := t.TempDir()
	session := sampleSession()

	first := "# 第一版\n"
	path1, err := Write(root, session, "Claude Code", first)
	if err != nil {
		t.Fatalf("第一次写: %v", err)
	}

	// 会话长长了（消息更多），标题与开始时间没变——同一个会话。
	grown := session
	grown.Messages = append(grown.Messages, channel.Message{Role: channel.RoleUser, Text: "又加了一句"})
	second := "# 第二版\n"
	path2, err := Write(root, grown, "Claude Code", second)
	if err != nil {
		t.Fatalf("第二次写: %v", err)
	}

	if path1 != path2 {
		t.Errorf("同一会话应当落在同一个路径：%q / %q", path1, path2)
	}
	files := markdownFiles(t, root)
	if len(files) != 1 {
		t.Fatalf("应当只有 1 份文件，实际 %d 份: %v", len(files), files)
	}
	raw, err := os.ReadFile(path1)
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != second {
		t.Errorf("内容没被更新，实际 %q", string(raw))
	}
}

// 验收标准 4：归档目录不存在时自动创建。
func TestCreatesMissingDirectories(t *testing.T) {
	root := filepath.Join(t.TempDir(), "还没有", "这些", "目录")

	path, err := Write(root, sampleSession(), "Claude Code", sampleMarkdown)
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	if info, err := os.Stat(filepath.Dir(path)); err != nil || !info.IsDir() {
		t.Fatalf("目录没建出来: %v", err)
	}
	if raw, err := os.ReadFile(path); err != nil || string(raw) != sampleMarkdown {
		t.Fatalf("文件内容不对: %q, %v", string(raw), err)
	}
}

func TestWriteLeavesNoTempFile(t *testing.T) {
	root := t.TempDir()

	path, err := Write(root, sampleSession(), "Claude Code", sampleMarkdown)
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	files := markdownFiles(t, root)
	if len(files) != 1 || !strings.HasSuffix(files[0], ".md") {
		t.Errorf("归档后目录里应当只有那份 .md，实际 %v", files)
	}

	// 再写一次（覆盖已存在的文件）：临时文件也不能留下。
	if _, err := Write(root, sampleSession(), "Claude Code", sampleMarkdown); err != nil {
		t.Fatalf("第二次 Write: %v", err)
	}
	if files := markdownFiles(t, root); len(files) != 1 {
		t.Errorf("覆盖写之后应当还是只有 1 份文件，实际 %v", files)
	}
	if _, err := os.Stat(path + tempSuffix); err == nil {
		t.Error("临时文件没清掉")
	}
}

func TestPlanIsPureAndStable(t *testing.T) {
	root := filepath.Join("D:", "归档")
	session := sampleSession()

	first := Plan(root, session, "Claude Code")
	for i := 0; i < 20; i++ {
		if got := Plan(root, session, "Claude Code"); got != first {
			t.Fatalf("第 %d 次结果不同：%q", i+2, got)
		}
	}

	// 参数改了就应当改路径：渠道不同、项目不同、日期不同。
	variants := []channel.Session{session, session, session}
	variants[0].Channel = "codex"
	variants[1].ProjectPath = `D:\work\other`
	variants[2].StartedAt = session.StartedAt.AddDate(0, 0, 1)
	for i, v := range variants {
		if Plan(root, v, "Claude Code") == first {
			t.Errorf("第 %d 个变体应当改路径", i+1)
		}
	}
}

// ── 数归档目录（I-023）────────────────────────────────────────────────────
//
// 界面要显示「已收集多少份 / 今天收了几份」，这两个数都由这里出：
// 归档目录的布局是 archive 定的，别人不该自己去读它的目录。

// 造一份归档文件，并把落盘时间设成指定时刻。
func putArchive(t *testing.T, root, channel, project, name string, at time.Time) string {
	t.Helper()
	dir := filepath.Join(root, channel, project)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte("# 会话\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, at, at); err != nil {
		t.Fatal(err)
	}
	return path
}

// 验收标准 5：数出来的份数与目录里实际的文件数一致；按渠道数的也一样。
func TestCountSaysHowManyAndHowManyToday(t *testing.T) {
	root := t.TempDir()
	today := time.Date(2026, 10, 16, 15, 0, 0, 0, time.Local)
	yesterday := today.AddDate(0, 0, -1)

	putArchive(t, root, "Codex CLI", "proj", "2026-10-16-甲-aaaaaaaaaa.md", today)
	putArchive(t, root, "Codex CLI", "proj", "2026-10-15-乙-bbbbbbbbbb.md", yesterday)
	putArchive(t, root, "Claude Code", "未标注项目", "2026-10-16-丙-cccccccccc.md", today)
	// 不是归档文件：数数的时候要跳过。
	putArchive(t, root, "Claude Code", "未标注项目", "半截.md.tmp", today)
	putArchive(t, root, "Claude Code", "未标注项目", "笔记.txt", today)

	all, err := Count(root, today)
	if err != nil {
		t.Fatal(err)
	}
	if all.Total != 3 || all.Today != 2 {
		t.Errorf("整个归档目录应当是 3 份、今天 2 份，实际 共 %d / 今天 %d", all.Total, all.Today)
	}

	perChannel, err := Count(ChannelDir(root, "Codex CLI", ""), today)
	if err != nil {
		t.Fatal(err)
	}
	if perChannel.Total != 2 || perChannel.Today != 1 {
		t.Errorf("Codex 那一层应当是 2 份、今天 1 份，实际 共 %d / 今天 %d", perChannel.Total, perChannel.Today)
	}

	// 没建过的渠道目录：0 份，且不是错——还没收过就是这样。
	empty, err := Count(ChannelDir(root, "Cursor", "Cursor"), today)
	if err != nil {
		t.Fatalf("渠道目录不存在不该报错：%v", err)
	}
	if empty.Total != 0 || empty.Today != 0 {
		t.Errorf("没建过的渠道目录应当是 0 / 0，实际 %+v", empty)
	}
}

// 归档根下混了网页版插件写的那一层时，份数要按来源拆开（I-078）。
func TestCountSplitsDesktopAndWeb(t *testing.T) {
	root := t.TempDir()
	now := time.Now()

	putArchive(t, root, "Codex CLI", "proj", "2026-10-16-甲-aaaaaaaaaa.md", now)
	putArchive(t, root, "Claude Code", "proj", "2026-10-16-乙-bbbbbbbbbb.md", now)
	// 网页版插件写的是 <归档目录>/网页版/<站点>/<文件>.md，中间没有项目层。
	putArchive(t, root, "网页版", "DeepSeek", "2026-10-16-丙-cccccccccc.md", now)

	got, err := Count(root, now)
	if err != nil {
		t.Fatal(err)
	}
	if got.Total != 3 || got.Desktop != 2 || got.Web != 1 {
		t.Errorf("应当共 3 份、桌面端 2 份、网页版 1 份，实际 %+v", got)
	}
	if got.Desktop+got.Web != got.Total {
		t.Errorf("总数必须是两者之和，实际 %+v", got)
	}

	// 把扫描根指到网页版那一层时，里面都是「本地目录」，不算网页版来源——
	// 判据看的是相对扫描根的第一段，不是认得「DeepSeek」这种站点名。
	inner, err := Count(filepath.Join(root, "网页版"), now)
	if err != nil {
		t.Fatal(err)
	}
	if inner.Total != 1 || inner.Web != 0 || inner.Desktop != 1 {
		t.Errorf("扫描根落在 网页版/ 里面时不该再算成网页版来源，实际 %+v", inner)
	}
}

// 验收标准 7：今日数只看落盘日期，不看文件名里那截会话日期。
func TestCountTodayIgnoresTheDateInTheName(t *testing.T) {
	root := t.TempDir()
	now := time.Now()
	putArchive(t, root, "Codex CLI", "proj", "2026-10-16-甲-aaaaaaaaaa.md", now.AddDate(0, 0, -1))

	got, err := Count(root, now)
	if err != nil {
		t.Fatal(err)
	}
	if got.Total != 1 || got.Today != 0 {
		t.Errorf("文件名里那截是会话当天，不能当今日数：实际 共 %d / 今天 %d", got.Total, got.Today)
	}
}

// 验收标准 8：数数只读——不改归档目录里的任何东西。
func TestCountLeavesTheArchiveAlone(t *testing.T) {
	root := t.TempDir()
	now := time.Now()
	putArchive(t, root, "Codex CLI", "proj", "2026-10-16-甲-aaaaaaaaaa.md", now)
	putArchive(t, root, "Claude Code", "未标注项目", "2026-10-15-乙-bbbbbbbbbb.md", now.AddDate(0, 0, -1))

	list := func() string {
		var lines []string
		err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			info, err := d.Info()
			if err != nil {
				return err
			}
			if d.IsDir() {
				// 目录只记路径：目录自己的时间戳由系统懒着写（NTFS），
				// 拿它比「有没有被改动」会时灵时不灵（I-030 里逮到的）。
				lines = append(lines, path+" 目录")
				return nil
			}
			lines = append(lines, fmt.Sprintf("%s %d %d %s", path, info.Size(), info.Mode(), info.ModTime().Format(time.RFC3339Nano)))
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		sort.Strings(lines)
		return strings.Join(lines, "\n")
	}

	before := list()
	if _, err := Count(root, now); err != nil {
		t.Fatal(err)
	}
	if after := list(); after != before {
		t.Errorf("数数只读：归档目录里不该有任何东西被改动\n改动前：\n%s\n改动后：\n%s", before, after)
	}
}

// ── 复制已有归档（I-024 / F10）───────────────────────────────────────────
//
// 规矩：只复制不搬、只认归档形态的 .md、内容一样跳过、撞名内容不同整趟不动、
// 幂等、旧目录一份不动。

func TestMigrateCopiesOnlyOurArchiveFiles(t *testing.T) {
	oldRoot := t.TempDir()
	newRoot := t.TempDir()
	at := time.Now()

	putArchive(t, oldRoot, "Codex CLI", "proj", "2026-10-16-甲-aaaaaaaaaa.md", at)
	putArchive(t, oldRoot, "Codex CLI", "proj", "半截.md.tmp", at)
	putArchive(t, oldRoot, "Codex CLI", "proj", "笔记.txt", at)
	putArchive(t, oldRoot, "Codex CLI", "proj", "随手记.md", at) // 不是我们的命名形态

	report, err := Migrate(oldRoot, newRoot)
	if err != nil {
		t.Fatal(err)
	}
	if report.OldTotal != 1 || report.Copied != 1 || report.Skipped != 0 || len(report.Conflicts) != 0 {
		t.Errorf("只该复制那 1 份归档，实际 %+v", report)
	}
	if _, err := os.Stat(filepath.Join(newRoot, "Codex CLI", "proj", "2026-10-16-甲-aaaaaaaaaa.md")); err != nil {
		t.Errorf("那份归档没复制过去：%v", err)
	}
	for _, name := range []string{"半截.md.tmp", "笔记.txt", "随手记.md"} {
		if _, err := os.Stat(filepath.Join(newRoot, "Codex CLI", "proj", name)); err == nil {
			t.Errorf("%s 不是我们写的归档，不该被复制", name)
		}
	}
}

// 幂等 + 内容一样就跳过；复制要保留落盘时间（不然「今天保存」会被算成今天收的）。
func TestMigrateIsIdempotentAndKeepsModTime(t *testing.T) {
	oldRoot := t.TempDir()
	newRoot := t.TempDir()
	at := time.Now().Add(-48 * time.Hour)
	src := putArchive(t, oldRoot, "Codex CLI", "proj", "2026-10-14-甲-aaaaaaaaaa.md", at)

	first, err := Migrate(oldRoot, newRoot)
	if err != nil {
		t.Fatal(err)
	}
	if first.Copied != 1 {
		t.Fatalf("第一遍应当复制 1 份，实际 %+v", first)
	}
	dst := filepath.Join(newRoot, "Codex CLI", "proj", "2026-10-14-甲-aaaaaaaaaa.md")
	info, err := os.Stat(dst)
	if err != nil {
		t.Fatal(err)
	}
	if !info.ModTime().Equal(mustStat(t, src).ModTime()) {
		t.Errorf("复制过去的文件应当保留落盘时间：%v vs %v", info.ModTime(), mustStat(t, src).ModTime())
	}

	second, err := Migrate(oldRoot, newRoot)
	if err != nil {
		t.Fatal(err)
	}
	if second.Copied != 0 || second.Skipped != 1 {
		t.Errorf("第二遍应当全部跳过，实际 %+v", second)
	}
}

// 撞名但内容不同：整趟不复制，列出来等人定（绝不覆盖）。
func TestMigrateStopsOnConflictingContent(t *testing.T) {
	oldRoot := t.TempDir()
	newRoot := t.TempDir()
	at := time.Now()
	putArchive(t, oldRoot, "Codex CLI", "proj", "2026-10-16-甲-aaaaaaaaaa.md", at)
	putArchive(t, oldRoot, "Codex CLI", "proj", "2026-10-16-乙-bbbbbbbbbb.md", at)

	// 目标里放一份同名但内容不同的。
	dir := filepath.Join(newRoot, "Codex CLI", "proj")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	clash := filepath.Join(dir, "2026-10-16-甲-aaaaaaaaaa.md")
	if err := os.WriteFile(clash, []byte("# 别的内容"), 0o644); err != nil {
		t.Fatal(err)
	}

	report, err := Migrate(oldRoot, newRoot)
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Conflicts) != 1 || report.Copied != 0 {
		t.Errorf("有撞名时应当整趟不复制、只列出冲突，实际 %+v", report)
	}
	// 另一份（不冲突的）也不该被复制进来——「整趟不动」。
	if _, err := os.Stat(filepath.Join(dir, "2026-10-16-乙-bbbbbbbbbb.md")); err == nil {
		t.Error("有冲突时整趟都该不动，不该偷偷复制别的")
	}
	// 目标里那份原样保留（没被覆盖）。
	raw, err := os.ReadFile(clash)
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != "# 别的内容" {
		t.Error("目标里已有的那份不该被动过")
	}
}

// 旧目录只读：跑前跑后整棵列一遍比对。
func TestMigrateLeavesTheOldRootAlone(t *testing.T) {
	oldRoot := t.TempDir()
	newRoot := t.TempDir()
	putArchive(t, oldRoot, "Codex CLI", "proj", "2026-10-16-甲-aaaaaaaaaa.md", time.Now())

	list := func(root string) string {
		var lines []string
		err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			info, err := d.Info()
			if err != nil {
				return err
			}
			if d.IsDir() {
				// 同上：目录自己的时间戳不比。
				lines = append(lines, path+" 目录")
				return nil
			}
			lines = append(lines, fmt.Sprintf("%s %d %s", path, info.Size(), info.ModTime().Format(time.RFC3339Nano)))
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		sort.Strings(lines)
		return strings.Join(lines, "\n")
	}

	before := list(oldRoot)
	if _, err := Migrate(oldRoot, newRoot); err != nil {
		t.Fatal(err)
	}
	if after := list(oldRoot); after != before {
		t.Error("复制只读旧目录：一份都不该变")
	}
}

func mustStat(t *testing.T, path string) os.FileInfo {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return info
}
