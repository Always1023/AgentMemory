package main

import (
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// 这个文件盯的是「我看不见窗口」这件事。
//
// 前端改错了元素 id、或前后端的事件名没对上，症状都是**界面安安静静什么都不发生**——
// 没有报错弹窗、日志里也看不出异常，只有人在那儿盯着窗口猜。这几条用例把这类
// 「安静地不工作」变成测试里的一声失败。
//
// 判据都读源码文本：它们要防的正是「读代码时对不上的地方」，跑起来才发现就晚了。

const (
	appJS      = "frontend/dist/app.js"
	indexHTML  = "frontend/dist/index.html"
	uiGoSource = "ui.go"
	styleCSS   = "frontend/dist/style.css"
)

func readSource(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

// app.js 里要用的元素，index.html 里都得有。
func TestFrontendElementIDsExist(t *testing.T) {
	front := readSource(t, appJS)
	page := readSource(t, indexHTML)

	declared := map[string]bool{}
	for _, m := range regexp.MustCompile(`id="([^"]+)"`).FindAllStringSubmatch(page, -1) {
		declared[m[1]] = true
	}
	if len(declared) == 0 {
		t.Fatal("index.html 里一个 id 都没有？读取或正则多半出问题了")
	}

	// $("x") 与 fill("x", …) 是界面上唯一两种按 id 取元素的方式。
	patterns := []*regexp.Regexp{
		regexp.MustCompile(`\$\("([^"]+)"\)`),
		regexp.MustCompile(`fill\("([^"]+)"`),
	}
	used := map[string][]string{}
	for _, re := range patterns {
		for _, m := range re.FindAllStringSubmatch(front, -1) {
			used[m[1]] = append(used[m[1]], re.String())
		}
	}
	if len(used) == 0 {
		t.Fatal("app.js 里一个按 id 取元素的地方都没找到？读取或正则多半出问题了")
	}

	for id := range used {
		if !declared[id] {
			t.Errorf("app.js 用了 #%s，但 index.html 里没有这个 id——界面上那一处会永远不动", id)
		}
	}
}

// 前后端的进度事件名必须一样，否则进度永远不显示，而且不报错。
func TestFrontendProgressEventMatchesBackend(t *testing.T) {
	front := readSource(t, appJS)
	backend := readSource(t, uiGoSource)

	frontName := firstGroup(t, front, `PROGRESS_EVENT\s*=\s*"([^"]+)"`)
	backendName := firstGroup(t, backend, `progressEvent\s*=\s*"([^"]+)"`)

	if frontName == "" || backendName == "" {
		t.Fatalf("事件名没读到：前端 %q，后端 %q", frontName, backendName)
	}
	if frontName != backendName {
		t.Errorf("前端订阅 %q，后端推的是 %q——进度会一条都收不到", frontName, backendName)
	}
}

// 读取过场的三个事件名也必须在前后端一致，否则进度条会一直停在 0（I-051）。
func TestFrontendReadingEventsMatchBackend(t *testing.T) {
	front := readSource(t, appJS)
	backend := readSource(t, uiGoSource)

	for _, pair := range [][2]string{
		{"READING_STARTED_EVENT", "readingStartedEvent"},
		{"READING_PROGRESS_EVENT", "readingProgressEvent"},
		{"READING_FINISHED_EVENT", "readingFinishedEvent"},
	} {
		frontName := firstGroup(t, front, pair[0]+`\s*=\s*"([^"]+)"`)
		backendName := firstGroup(t, backend, pair[1]+`\s*=\s*"([^"]+)"`)
		if frontName == "" || backendName == "" || frontName != backendName {
			t.Errorf("%s / %s 没对上：前端 %q，后端 %q", pair[0], pair[1], frontName, backendName)
		}
	}
}

// 打开主界面要先显示读取过场，完成后再渲染数字（I-051）。
func TestMainShowsReadingProgressBeforeNumbers(t *testing.T) {
	front := readSource(t, appJS)
	page := readSource(t, indexHTML)
	css := readSource(t, styleCSS)

	if !strings.Contains(page, `id="reading"`) || !strings.Contains(page, `id="reading-fill"`) {
		t.Error("主界面要有读取过场与进度条")
	}
	read := functionBody(t, front, "readState")
	if !strings.Contains(read, "setReading(true") || !strings.Contains(read, "bridge.StateWithProgress()") {
		t.Error("读状态要先显示过场，并调用带进度的 StateWithProgress")
	}
	main := functionBody(t, front, "renderMain")
	if !strings.Contains(main, "setReading(false)") {
		t.Error("数字渲染完成之后才收起读取过场")
	}
	if !strings.Contains(functionBody(t, front, "setReading"), "reading-fill") ||
		!strings.Contains(functionBody(t, front, "setReading"), "aria-valuenow") {
		t.Error("过场要按实际进度更新进度条与无障碍值")
	}
	if !strings.Contains(cssRule(t, css, ".progress__fill"), "width") {
		t.Error("进度条填充宽度要跟读取进度变化")
	}
}

// 引导那几步：index.html 里的 data-step 份数要与 ui.go 里的步数一致。
//
// 多了没用（走不到），少了会卡住（界面以为还有下一步）。
func TestFirstRunStepsMatchThePage(t *testing.T) {
	page := readSource(t, indexHTML)
	backend := readSource(t, uiGoSource)

	sections := regexp.MustCompile(`data-step="(\d+)"`).FindAllStringSubmatch(page, -1)
	want := firstRunSteps
	if len(sections) != want {
		t.Fatalf("index.html 里有 %d 段引导，ui.go 说有 %d 步", len(sections), want)
	}
	for i, m := range sections {
		if m[1] != strconv.Itoa(i) {
			t.Errorf("第 %d 段引导的 data-step 是 %s，想要 %d（顺序错了界面会跳着走）", i+1, m[1], i)
		}
	}
	if !strings.Contains(backend, "firstRunSteps = "+strconv.Itoa(want)) {
		t.Errorf("ui.go 里没读到 firstRunSteps = %d", want)
	}
}

// 抽屉相关的两条机制标准（I-017 标准 5、6）只能读源码来核对——
// 它们在浏览器里运行，而这里没有浏览器。
//
// 判据都落在「函数体里有没有那一句、孰先孰后」上：这类「安静地不工作」
// （内容没清干净、关的时候没问）在界面上看不出错，只能靠这些判据钉住。

// 验收标准 5：没进设置页时，界面上不渲染它的内容。
func TestSettingsPageRendersNothingUntilVisited(t *testing.T) {
	page := readSource(t, indexHTML)
	front := readSource(t, appJS)

	// 一、页面上那些容器本身是空的：内容全是进来时现建的。
	for _, id := range []string{"settings-body", "settings-foot"} {
		if body := elementHTML(t, page, id); strings.TrimSpace(body) != "" {
			t.Errorf("index.html 里 #%s 应当是空的（没进来就不渲染），实际装着：%q", id, body)
		}
	}

	// 二、返回的时候要把内容清掉。
	hide := functionBody(t, front, "hideSettings")
	if !strings.Contains(hide, "clearSettingsPage") {
		t.Error("hideSettings 应当把设置页清干净（clearSettingsPage）")
	}
	clear := functionBody(t, front, "clearSettingsPage")
	for _, want := range []string{`$("settings-body").replaceChildren()`, `$("settings-foot").replaceChildren()`} {
		if !strings.Contains(clear, want) {
			t.Errorf("clearSettingsPage 里应当有 %s——返回之后界面上不该留着表单", want)
		}
	}

	// 三、别在启动时就去问系统里的定时任务：那是进设置页才做的事。
	// （boot 里会接上「高级设置」那个按钮，所以判的是「有没有真的去读设置」。）
	boot := functionBody(t, front, "boot")
	if strings.Contains(boot, "bridge.Settings(") {
		t.Error("boot 里不该读设置：没进设置页的时候不去问系统里的定时任务")
	}
	if !strings.Contains(functionBody(t, front, "openSettings"), "bridge.Settings()") {
		t.Error("进设置页时才去读设置")
	}
}

// 验收标准 6：有没保存的改动时，返回之前会先问一句。
func TestSettingsPageAsksBeforeLeaving(t *testing.T) {
	front := readSource(t, appJS)

	// 那道闸门现在在 leaveSettings 里（日志页也要走它，I-030），
	// closeSettings 只负责「闸门放行了才真的回去」。
	guard := functionBody(t, front, "leaveSettings")
	dirtyAt := strings.Index(guard, "settingsDirty")
	if dirtyAt < 0 {
		t.Fatal("返回之前要看一眼有没有没保存的改动")
	}
	if !strings.Contains(guard, "renderSettingsFoot") {
		t.Error("有没保存的改动时要当场问一句（底部那行换成询问）")
	}

	closeBody := functionBody(t, front, "closeSettings")
	leaveAt := strings.Index(closeBody, "leaveSettings")
	hideAt := strings.Index(closeBody, "hideSettings")
	if leaveAt < 0 {
		t.Fatal("closeSettings 要先过那道闸门")
	}
	if hideAt < 0 {
		t.Fatal("closeSettings 最终要能真的回去（不然就回不去了）")
	}
	if leaveAt > hideAt {
		t.Error("应当先看有没有没保存的改动、再回去——顺序反了就等于没问")
	}

	// 打字的输入框要能让人「有改动」这件事被记下来。
	mark := functionBody(t, front, "markSettingsDirty")
	if !strings.Contains(mark, "settingsDirty = true") {
		t.Error("markSettingsDirty 应当把「有改动」记下来")
	}
	if n := strings.Count(front, `addEventListener("input", markSettingsDirty)`); n < 1 {
		t.Error("手打的输入框要在 input 时标记「有改动」，否则返回时不会问")
	}
}

// 验收标准 10：首次运行引导的归档目录那一步也有「选择文件夹」。
func TestWizardArchiveStepHasTheFolderPicker(t *testing.T) {
	page := readSource(t, indexHTML)
	front := readSource(t, appJS)

	// 引导第 1 步（data-step="0"）那一段里要有那个按钮。
	start := strings.Index(page, `data-step="0"`)
	end := strings.Index(page, `data-step="1"`)
	if start < 0 || end < 0 || end < start {
		t.Fatal("index.html 里读不到引导的第 1 步与第 2 步")
	}
	step := page[start:end]
	if !strings.Contains(step, `id="wizard-archive-browse"`) {
		t.Error("引导第 1 步（归档目录）里要有「选择文件夹」按钮——人第一眼看到的就是那一步")
	}
	if !strings.Contains(front, `$("wizard-archive-browse").addEventListener`) {
		t.Error("那个按钮要接上 pickDirectory，不然点了没反应")
	}
	// 引导里没有「保存」这一步，填进输入框就行，「下一步」会一起存。
	if !strings.Contains(front, `pickDirectory($("wizard-archive"))`) {
		t.Error("引导那一步应当把选中的目录填进 #wizard-archive")
	}
}

// 首次引导第 1 步：输入框留空，不拿默认目录占位；没选目录不能进下一步（I-075）。
func TestWizardArchiveStartsWithEmptyInput(t *testing.T) {
	page := readSource(t, indexHTML)
	front := readSource(t, appJS)

	start := strings.Index(page, `data-step="0"`)
	end := strings.Index(page, `data-step="1"`)
	if start < 0 || end < 0 || end < start {
		t.Fatal("index.html 里读不到引导的第 1 步与第 2 步")
	}
	if step := page[start:end]; strings.Contains(step, "默认目录") {
		t.Error("首次引导第 1 步不该说「默认目录」——新用户要先自己选一个")
	}

	render := functionBody(t, front, "renderWizard")
	if strings.Contains(render, "state.archiveDefault") {
		t.Error("引导的归档输入框不该拿默认目录当占位")
	}
	if !strings.Contains(render, "state.firstRunStep > 0") {
		t.Error("只有已经走过第 1 步 / 重走引导时，才回填归档目录")
	}
	if !strings.Contains(render, `archive.placeholder = ""`) {
		t.Error("首次引导的归档输入框不该有默认占位")
	}
	if !strings.Contains(front, "先选一个归档目录") {
		t.Error("没选目录点「下一步」要提示先选")
	}
	if !strings.Contains(functionBody(t, front, "wizardArchiveMissing"), "value.trim()") {
		t.Error("空值判断要针对输入框的值")
	}
}

// 重走引导第 1 步只留一个「返回设置」（I-075）。
func TestReplayWizardHasOneReturnButtonAtFirstStep(t *testing.T) {
	render := functionBody(t, readSource(t, appJS), "renderWizard")

	if strings.Contains(render, "step === 0 && !wizardReplay") {
		t.Error("第 1 步的返回键不再按「是不是重走」区分：两种情况都应隐藏左侧那个")
	}
	if !strings.Contains(render, "back.hidden = step === 0") {
		t.Error("第 1 步应当隐藏左侧的返回键")
	}
	if !strings.Contains(render, `skip.textContent = "返回设置"`) {
		t.Error("重走引导的「返回设置」要保留在右侧那个按钮上")
	}
}

// 首次引导第三步不再有「跳过」；重走的「返回设置」保留（I-075）。
func TestWizardHasNoSkipButtonOnFirstRun(t *testing.T) {
	render := functionBody(t, readSource(t, appJS), "renderWizard")

	if strings.Contains(render, `skip.textContent = "跳过"`) {
		t.Error("首次引导不该再显示「跳过」")
	}
	if strings.Contains(render, "skip.hidden = step !== last") {
		t.Error("首次引导的跳过按钮不该再按步骤显示")
	}
	if !strings.Contains(render, "skip.hidden = true") {
		t.Error("首次引导应当始终隐藏跳过按钮")
	}
	if !strings.Contains(render, `skip.textContent = "返回设置"`) {
		t.Error("重走引导的「返回设置」仍要保留")
	}
}

// 读取过场显示时，下面的「增量收集」也一起藏起来（I-076）。
func TestReadingHidesTheCollectActions(t *testing.T) {
	page := readSource(t, indexHTML)
	if !strings.Contains(page, `id="main-actions"`) {
		t.Error("主操作容器要有一个 id，读取时才能整块藏起来")
	}
	body := functionBody(t, readSource(t, appJS), "setReading")
	if !strings.Contains(body, `$("main-actions").hidden = active`) {
		t.Error("读取过场显示 / 收起时，主操作要跟着一起藏 / 显")
	}
}

// 引导页底部按钮组靠右下；重走引导共用同一组（I-075）。
func TestWizardActionsAreRightAligned(t *testing.T) {
	page := readSource(t, indexHTML)
	if !strings.Contains(page, `class="actions actions--end"`) {
		t.Error("引导页底部按钮要用靠右的 actions 修饰")
	}
	css := readSource(t, styleCSS)
	if !strings.Contains(css, ".actions--end") || !strings.Contains(css, "justify-content: flex-end") {
		t.Error("缺 .actions--end 的靠右规则")
	}
}

// 验收标准 9：切页有过渡，且系统说「减少动效」时过渡关掉。
//
// 动效只动颜色与透明度（DESIGN_SYSTEM §6），所以这里只认 opacity 动画，
// 出现位移 / 缩放就算越界。
func TestViewSwitchFadesAndRespectsReducedMotion(t *testing.T) {
	css := readSource(t, "frontend/dist/style.css")

	fade := cssRule(t, css, "@keyframes view-in")
	if !strings.Contains(fade, "opacity") {
		t.Errorf("切页的过渡应当只动透明度，实际 %q", fade)
	}
	for _, forbidden := range []string{"transform", "translate", "scale"} {
		if strings.Contains(fade, forbidden) {
			t.Errorf("切页过渡里不该出现 %s——动效只动颜色与透明度", forbidden)
		}
	}
	if !strings.Contains(css, "view-in var(--motion-base)") {
		t.Error("视图上要用 token 里的时长播这段过渡，别写字面值")
	}

	reduced := cssRule(t, css, "@media (prefers-reduced-motion: reduce)")
	if !strings.Contains(reduced, "animation-duration: 0ms !important") {
		t.Error("系统说「减少动效」时，过渡（动画）也要关掉")
	}
	if !strings.Contains(reduced, "transition-duration: 0ms !important") {
		t.Error("系统说「减少动效」时， 过渡也要关掉")
	}
}

// 验收标准 5 的另一半：hidden 属性必须真的把内容藏起来。
//
// 这一条来自一次真事故：`.shell__main` 与 `.drawer` 都写了 `display: flex`，
// 作者样式盖掉了浏览器对 [hidden] 的默认 display: none，于是「已经藏起来」的
// 引导与抽屉照旧露在外面，和主界面叠成一团——DOM 里查 hidden 是 true，
// 报告一切正常，只有人盯着窗口才看得出不对。
func TestHiddenAttributeActuallyHides(t *testing.T) {
	css := readSource(t, "frontend/dist/style.css")

	if !strings.Contains(css, "[hidden]") {
		t.Fatal("style.css 里要有 [hidden] 规则：本文件里给好几个容器写了 display: flex")
	}
	rule := cssRule(t, css, "[hidden]")
	if !strings.Contains(rule, "display: none") {
		t.Errorf("[hidden] 必须落到 display: none，实际 %q", rule)
	}
	if !strings.Contains(rule, "!important") {
		t.Error("[hidden] 的 display 要带 !important——不然压不住同一个元素上的 display: flex")
	}

	// 用到 hidden 切换的容器，自己都写了 display；哪天不写了，这条用例可以简化。
	// （三个视图都是 .shell__main；引导里的每一步是 .panel。）
	for _, selector := range []string{".shell__main", ".panel"} {
		if !strings.Contains(cssRule(t, css, selector), "display:") {
			t.Errorf("%s 不再写 display 了，这条用例可以简化", selector)
		}
	}
}

// cssRule 取一条 CSS 规则的内容（选择器之后到第一个右花括号）。
func cssRule(t *testing.T, css, selector string) string {
	t.Helper()
	at := strings.Index(css, selector+" {")
	if at < 0 {
		t.Fatalf("style.css 里没有 %s 的规则", selector)
	}
	rest := css[at+len(selector):]
	end := strings.Index(rest, "}")
	if end < 0 {
		t.Fatalf("%s 的规则没有收尾的右花括号", selector)
	}
	return rest[:end]
}

// elementHTML 取某个 id 元素在标签之间的内容（只认同一行内的空元素与空容器）。
func elementHTML(t *testing.T, page, id string) string {
	t.Helper()
	open := `id="` + id + `"`
	at := strings.Index(page, open)
	if at < 0 {
		t.Fatalf("index.html 里没有 id=%s", id)
	}
	// 往前后各找一下：前面的 `<div ...>` 与后面的第一个 `</div>`。
	start := strings.Index(page[at:], ">")
	if start < 0 {
		t.Fatalf("id=%s 的标签没有闭合的尖括号", id)
	}
	rest := page[at+start+1:]
	end := strings.Index(rest, "</")
	if end < 0 {
		t.Fatalf("id=%s 的容器没有结束标签", id)
	}
	return rest[:end]
}

// functionBody 取一个函数从声明到下一个顶格右花括号之间的内容。
// I-052：进度行与顶部状态句要真的按内容显隐。
//
// 以前 fill 只写文字、不碰 hidden：`#collect-progress` 带着 hidden 出生，
// 之后写多少字都看不见——界面上像是什么都没发生。
func TestProgressAndHeadlineToggleVisibility(t *testing.T) {
	front := readSource(t, appJS)

	if !strings.Contains(functionBody(t, front, "fill"), "hidden = false") {
		t.Error("fill 写完内容要把元素显出来，否则 #collect-progress 永远藏着")
	}
	if !strings.Contains(functionBody(t, front, "renderHeadline"), `fillOptional("headline"`) {
		t.Error("顶部状态句要同时管文字与显隐，空串不能写成「—」")
	}
}

// I-052：引导第 1 步的「选择文件夹」必须真的有这个函数。
//
// 以前只有调用、没有定义：点下去直接 ReferenceError，按钮看着像坏的。
func TestWizardFolderPickerIsDefined(t *testing.T) {
	front := readSource(t, appJS)

	if !strings.Contains(front, "function pickDirectory") {
		t.Fatal("pickDirectory 未定义：引导第 1 步点了会抛 ReferenceError")
	}
	if !strings.Contains(functionBody(t, front, "pickDirectory"), "BrowseDirectory") {
		t.Error("pickDirectory 要调后端的文件夹选择框")
	}
}

// I-052：读取过场在失败时要收起，不能永远停在「正在读取数据」。
func TestReadingOverlayRecoversFromFailure(t *testing.T) {
	front := readSource(t, appJS)

	read := functionBody(t, front, "readState")
	if !strings.Contains(read, "catch") || !strings.Contains(read, "setReading(false)") {
		t.Error("readState 失败要把过场收起来")
	}
	if strings.Contains(functionBody(t, front, "onReadingProgress"), "setReading(!progress.done") {
		t.Error("过场不能按 done 提前收：数字还没回来")
	}
}

// I-054：首页每次打开 / 切回都走带过场的读取；不再有本地快照缓存。
func TestHomeAlwaysReadsWithOverlay(t *testing.T) {
	front := readSource(t, appJS)
	boot := functionBody(t, front, "boot")

	if !strings.Contains(boot, "readState()") {
		t.Error("打开首页要走带过场的 readState()")
	}
	for _, gone := range []string{"loadCachedState", "saveCachedState", "HOME_CACHE_KEY"} {
		if strings.Contains(front, gone) {
			t.Errorf("首页快照缓存 %s 应当已经移除", gone)
		}
	}
	if strings.Contains(functionBody(t, front, "refreshMainView"), "silent = true") {
		t.Error("refreshMainView 不能默认静默：回首页要显示读取过场")
	}
	for _, name := range []string{"retryChannel", "collect"} {
		if strings.Contains(functionBody(t, front, name), "readState()") {
			t.Errorf("%s 失败后的重读不应再盖读取过场", name)
		}
	}
}

// I-054：从日志 / 设置回首页要补刷一次（走带过场的读取）。
func TestReturningHomeRefreshes(t *testing.T) {
	front := readSource(t, appJS)

	for _, name := range []string{"closeSettings", "hideSettings"} {
		if !strings.Contains(functionBody(t, front, name), "refreshMainView()") {
			t.Errorf("%s 回到首页时要刷新一次", name)
		}
	}
}

// I-054：设置页点「保存」成功后要说一声「已保存」；再编辑就收起来。
func TestSettingsSaveShowsSuccess(t *testing.T) {
	front := readSource(t, appJS)

	if !strings.Contains(functionBody(t, front, "saveSettingsEdits"), `"已保存"`) {
		t.Error("保存成功要提示「已保存」")
	}
	if !strings.Contains(functionBody(t, front, "markSettingsDirty"), `settingsMessage = ""`) {
		t.Error("再次编辑要清掉「已保存」")
	}
	foot := functionBody(t, front, "renderSettingsFoot")
	if !strings.Contains(foot, "settingsMessageKind") || !strings.Contains(foot, "foot__ok") {
		t.Error("底部提示要区分成功与失败")
	}
	// 报错的话不能沿用上一句「已保存」的绿色。
	for _, name := range []string{"saveSettingsEdits", "browseDirectory", "gitSay", "replayWizard"} {
		if !strings.Contains(functionBody(t, front, name), `settingsMessageKind = "error"`) {
			t.Errorf("%s 里的报错要标成 error，不能沿用绿色的「已保存」", name)
		}
	}
}

// I-052：设置页的自动保存不能清掉人正在做的选择，也不能把失败说成成功。
func TestSettingsKeepsUnsavedInputAndChoice(t *testing.T) {
	front := readSource(t, appJS)

	refresh := functionBody(t, front, "refreshSettings")
	if strings.Contains(refresh, "checked = false") || strings.Contains(refresh, "archiveSwapChoice = \"\"") {
		t.Error("refreshSettings 不能清掉「换目录怎么办」的选择：自动保存会把它打断")
	}

	saveEdits := functionBody(t, front, "saveSettingsEdits")
	for _, want := range []string{`archiveDir === ""`, "归档目录不能为空", "resetArchiveSwap", "if (!ok)"} {
		if !strings.Contains(saveEdits, want) {
			t.Errorf("saveSettingsEdits 少了 %q：空路径与保存失败都要留住人", want)
		}
	}

	if !strings.Contains(functionBody(t, front, "saveSettings"), "return ok") {
		t.Error("saveSettings 要告诉调用方成没成，失败时才不会清「有改动」")
	}
	if !strings.Contains(functionBody(t, front, "openSettings"), `$("view-settings").hidden`) {
		t.Error("再点「设置」时不能重建表单：会把正在敲的路径清掉")
	}
}

// I-052：引导连点不能跳步；放弃改动之后要去人原本点的那一页。
func TestWizardAndNavigationKeepIntent(t *testing.T) {
	front := readSource(t, appJS)

	if !strings.Contains(front, "setWizardBusy") {
		t.Error("引导缺少在途保护：连点两次会让响应乱序")
	}
	for _, name := range []string{"saveStep", "finishWizard"} {
		if !strings.Contains(functionBody(t, front, name), "wizardSaving") {
			t.Errorf("%s 没挡连点", name)
		}
	}
	if !strings.Contains(functionBody(t, front, "openLog"), "pendingView") {
		t.Error("被「有改动」拦住时，放弃改动要记得人原本要去日志页")
	}
	// 导航栏的「首页」在日志页也要能回主界面：不能在设置页以外直接 return。
	if !strings.Contains(functionBody(t, front, "closeSettings"), `$("view-log").hidden`) {
		t.Error("日志页点「首页」会没反应：closeSettings 要先判断在不在日志页")
	}
}

// I-052：前端未捕获的异常要有日志出口，前后端名字对得上。
func TestFrontendUncaughtErrorsReachTheLog(t *testing.T) {
	front := readSource(t, appJS)
	back := readSource(t, "app.go")

	if !strings.Contains(front, `addEventListener("error"`) ||
		!strings.Contains(front, `addEventListener("unhandledrejection"`) {
		t.Error("没有接住未捕获异常与未处理的 Promise 拒绝")
	}
	if !strings.Contains(front, "ReportFrontendError") {
		t.Error("前端没有往上报告的口子")
	}
	if !strings.Contains(back, "func (a *App) ReportFrontendError") {
		t.Error("后端没有 ReportFrontendError：前端报了也没地方落")
	}
}

// I-052：给人看的文案里不许再出现 Markdown 星号。
//
// （Git 引导里那段「请 AI 当向导」的 Prompt 是给模型读的 Markdown，不算。）
func TestNoMarkdownAsterisksInUserFacingText(t *testing.T) {
	cases := []struct {
		path string
		bad  []string
	}{
		{"frontend/dist/index.html", []string{"**默认只留在本机**", "**Git 仓库**"}},
		{appJS, []string{"要**空**仓库"}},
		{"frienderror.go", []string{"**凭据**", "**地址写法**", "**网络**"}},
		{"settings.go", []string{"**一份都没复制**", "**一份都没动**"}},
	}
	for _, c := range cases {
		src := readSource(t, c.path)
		for _, bad := range c.bad {
			if strings.Contains(src, bad) {
				t.Errorf("%s 里还有 Markdown 星号：%s", c.path, bad)
			}
		}
	}
}

// 设置页「定时」那段要讲清它包含备份，别让人以为自动备份是另一个功能（I-053）。
func TestScheduleHintSaysItIncludesBackup(t *testing.T) {
	front := readSource(t, appJS)
	if !strings.Contains(front, "先增量收集写到归档目录，再按上面的备份方式备份") {
		t.Error("定时的说明要说清「收集 + 按所选方式备份」")
	}
}

// 设置页有一块可扩的答疑区，首条直接回答「Cherry Studio 迁移失败怎么办」（I-074）。
func TestSettingsFAQExplainsCherryStudioMigrationFailure(t *testing.T) {
	front := readSource(t, appJS)

	body := functionBody(t, front, "renderSettings")
	if !strings.Contains(body, "renderFAQ") || !strings.Contains(body, `"答疑"`) {
		t.Error("设置页里应当挂上「答疑」面板")
	}

	faq := functionBody(t, front, "renderFAQ")
	if !strings.Contains(faq, "SETTINGS_FAQ") || !strings.Contains(faq, "forEach") {
		t.Error("答疑条目应当由 SETTINGS_FAQ 数组驱动：加一条只加数据")
	}

	for _, want := range []string{"Cherry Studio 迁移失败怎么办", "IndexedDB", "cherrystudio.sqlite", "1.9.13", "备份", "诊断包"} {
		if !strings.Contains(front, want) {
			t.Errorf("答疑首条应当说明 %q", want)
		}
	}
}

func functionBody(t *testing.T, source, name string) string {
	t.Helper()
	head := "function " + name + "("
	at := strings.Index(source, head)
	if at < 0 {
		t.Fatalf("源码里没有 function %s", name)
	}
	rest := source[at+len(head):]
	if end := strings.Index(rest, "\n}\n"); end >= 0 {
		return rest[:end]
	}
	return rest
}

// firstGroup 返回正则里第一个捕获组的内容；没匹配到返回空串。
func firstGroup(t *testing.T, source, pattern string) string {
	t.Helper()
	m := regexp.MustCompile(pattern).FindStringSubmatch(source)
	if m == nil {
		return ""
	}
	return m[1]
}

// 重启后的那一行要分三种：有历史 / 有归档但没记录 / 真没有（I-061）。
func TestRestartRunLineDistinguishesHistoryAndArchive(t *testing.T) {
	front := readSource(t, appJS)
	body := functionBody(t, front, "renderRun")

	for _, want := range []string{
		"state.archivedTotal",
		`本次启动还没收过，归档里已有`,
		"state.archivedWeb",
		`桌面端 ${desktop} · 网页版 ${web}`,
		"run.fromHistory",
		"上次收集没跑完",
		`: "还没收过。"`,
		"run.duration ?",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("renderRun 少了 %q：重启后的三种状态会被说成同一种", want)
		}
	}
}

// 「点按钮时窗口闪、卡」（人报的）——两条根因各钉一个判据。
//
// 两条都是「用得越久越明显、但不会报错」的那类毛病，只能靠源码判据钉住。
func TestClickingAroundDoesNotRebuildTheWorld(t *testing.T) {
	front := readSource(t, appJS)

	// 一、底部那一行不整个重建：建节点只在一处，其余只改字与显隐。
	draw := functionBody(t, front, "renderSettingsFoot")
	if strings.Contains(draw, "replaceChildren") {
		t.Error("renderSettingsFoot 不该重建底部那一行——敲一个字就把按钮删了重造，按钮会闪")
	}
	if !strings.Contains(draw, "hidden") {
		t.Error("底部那一行的三种状态（平常 / 询问 / 报错）靠改显隐来切")
	}
	if !strings.Contains(functionBody(t, front, "buildSettingsFoot"), "replaceChildren") {
		t.Error("建节点的那一份应当在 buildSettingsFoot 里，只跑一次")
	}

	// 二、存设置时不重新检测所有渠道：State() 留给返回主界面那一次。
	save := functionBody(t, front, "saveSettings")
	if strings.Contains(save, "refreshMainView") {
		t.Error("saveSettings 里不该刷主界面：State() 要重新检测所有渠道（几百次文件系统调用），人正停在设置页上看不见")
	}
	if !strings.Contains(functionBody(t, front, "hideSettings"), "refreshMainView") {
		t.Error("返回主界面时要把这次刷新补上")
	}
}

// 人问过「引导功能为什么找不到了」——它能走完就再也碰不到，界面里没有入口。
//
// 引导只在配置里没写过「走完了」时出现（那次也一样，得去删配置文件），
// 所以设置页里要留一个重走的入口，且不能被配置里那个标记挡住。
func TestTheWizardCanBeReplayedFromSettings(t *testing.T) {
	front := readSource(t, appJS)

	if n := strings.Count(front, `button("重走一遍引导"`); n != 1 {
		t.Errorf("设置页里应当有一个「重走一遍引导」按钮，实际找到 %d 个", n)
	}

	// 配置里早写着「走完了」，重走时视图得靠 wizardReplay 留在引导上，
	// 否则按一下「下一步」就会被踢回主界面（render 是按 firstRunDone 分岔的）。
	if !strings.Contains(functionBody(t, front, "render"), "wizardReplay") {
		t.Error("render() 要认 wizardReplay，不然重走时一按「下一步」就回主界面了")
	}
	replay := functionBody(t, front, "replayWizard")
	for _, want := range []string{"wizardReplay = true", "render()"} {
		if !strings.Contains(replay, want) {
			t.Errorf("replayWizard 里应当有 %s", want)
		}
	}
	if !strings.Contains(replay, "clearSettingsPage") {
		t.Error("重走之前要把设置页清干净，不然两页都留着内容")
	}

	// 不能把人关在引导里：第 1 步留一条回设置页的路，且真接上了。
	if !strings.Contains(functionBody(t, front, "renderWizard"), "返回设置") {
		t.Error("重走时第 1 步要留一条回设置页的路（「返回设置」）")
	}
	if !strings.Contains(functionBody(t, front, "boot"), "wizardReplay") {
		t.Error("那条路要接上（boot 里给「返回」按钮的处理器）")
	}
	if !strings.Contains(functionBody(t, front, "finishWizard"), "wizardReplay = false") {
		t.Error("走完之后要清掉重走状态，否则下次打开还是引导")
	}
}

// 顶部导航栏（人提的：首页 / 设置），主操作排在渠道下面并居中（人提的：按钮放最中间）。
func TestNavHasHomeAndSettingsAndTheActionIsCentered(t *testing.T) {
	page := readSource(t, indexHTML)
	css := readSource(t, styleCSS)
	front := readSource(t, appJS)

	// 一、导航栏两个入口都在，且排在页头之后（「在上面」）。
	for _, want := range []string{`id="nav"`, `id="nav-main"`, `id="nav-settings"`, ">首页<", ">设置<"} {
		if !strings.Contains(page, want) {
			t.Errorf("导航栏里应当有 %s", want)
		}
	}

	// 二、首页上不再有自己的「设置」按钮——入口在导航栏里。
	start := strings.Index(page, `id="view-main"`)
	if start < 0 {
		t.Fatal("index.html 里没有主界面")
	}
	end := strings.Index(page[start:], "</main>")
	main := page[start : start+end]
	if strings.Contains(main, `id="open-settings"`) {
		t.Error("首页上不该再有自己的「设置」按钮——入口在导航栏")
	}
	if !strings.Contains(main, `id="channel-stats"`) || !strings.Contains(main, `id="collect"`) {
		t.Error("首页上是渠道（含三层数字）与主操作")
	}
	if strings.Contains(main, `panel__title">收集`) || strings.Contains(main, `panel__title">统计`) || strings.Contains(main, `panel__title">配置`) {
		t.Error("首页上不该再有「收集 / 统计 / 配置」这几段")
	}

	// 三、主操作居中。
	at := strings.Index(css, ".actions--stack")
	if at < 0 {
		t.Fatal("主操作那一行要有自己的样式（居中）")
	}
	block := css[at:]
	if stop := strings.Index(block, "}"); stop > 0 {
		block = block[:stop]
	}
	if !strings.Contains(block, "align-items: center") {
		t.Error("主操作要居中（人提的：按钮放最中间）")
	}

	// 四、导航栏真的接上了；首页那条要走「有未保存改动先问」的老路。
	boot := functionBody(t, front, "boot")
	if !strings.Contains(boot, `$("nav-settings")`) || !strings.Contains(boot, "openSettings") {
		t.Error("导航栏的「设置」要接到 openSettings")
	}
	if !strings.Contains(boot, `$("nav-main")`) || !strings.Contains(boot, "closeSettings") {
		t.Error("导航栏的「首页」要接到 closeSettings（有未保存改动时先问）")
	}

	// 五、首次运行引导期间不给导航（引导是一条要走完的路）。
	if !strings.Contains(functionBody(t, front, "show"), `$("nav")`) {
		t.Error("show() 里要顺手管导航栏的显隐与高亮")
	}
}

// 人报「点备份方式和 Git 仓库那两个按钮时窗口有重影」。
//
// 没能在真机上复现成一张图（那种残影是瞬时的），但有一条是能确定、也该改的：
// 点一下造成**两轮重排**——点下去立刻重排一次（提示行、仓库地址框），约两百毫秒后
// git 状态回来又重排一次（那块内容从 0 长出来）。一轮能解决的事别做两轮。
func TestGitPanelDoesNotRedrawWhatItAlreadyShows(t *testing.T) {
	front := readSource(t, appJS)

	draw := functionBody(t, front, "renderGitPanel")
	if !strings.Contains(draw, "target.sig") {
		t.Error("状态没变就别重画：重画一次 = 这一块整块重排一次")
	}
	if !strings.Contains(draw, "childElementCount") {
		t.Error("签名相同时要确认那块内容真的还在——不然会把空面板当成画好了")
	}

	load := functionBody(t, front, "mountGitPanel")
	if !strings.Contains(load, "正在读 Git 状态") {
		t.Error("读 git 之前先把位置占住，别让这一块从 0 长出来")
	}

	if !strings.Contains(functionBody(t, front, "unmountGitPanel"), "gitTargets = gitTargets.filter") {
		t.Error("摘掉一个位置时要把位置连同签名一起摘掉，否则下次该画的时候会被签名挡住")
	}
}

// 引导是 1-2-3 三步：编号排开、第 2 步能一键复制、第 3 步自己试一次（I-029）。
func TestGitGuideIsThreeNumberedStepsWithCopyablePrompt(t *testing.T) {
	page := readSource(t, indexHTML)
	front := readSource(t, appJS)

	if !strings.Contains(page, `id="wizard-git"`) {
		t.Error("引导页也要有 Git 那一段——两端是同一块（人第二次澄清）")
	}
	if !strings.Contains(functionBody(t, front, "renderBackupChoices"), "syncWizardGit") {
		t.Error("引导页选了 Git 仓库时要把那一块挂上")
	}
	if !strings.Contains(functionBody(t, front, "syncWizardGit"), "mountGitPanel") {
		t.Error("引导页挂的要是同一份渲染，不是另写一遍")
	}

	draw := functionBody(t, front, "renderGitPanel")
	for _, want := range []string{"1. 建仓库", "2. 交给你的 AI", "3. 验一次"} {
		if !strings.Contains(draw, want) {
			t.Errorf("三步要编号排开，缺了 %q", want)
		}
	}
	// 编号只写在标题里：列表自己再编一次，人就看见 1.1 / 2.2 / 3.3（人报的）。
	if !strings.Contains(readSource(t, styleCSS), "list-style: none") {
		t.Error("三步的列表不该再自动编号（编号写在标题里）")
	}
	if !strings.Contains(draw, "git.prompt") {
		t.Error("第 2 步那段话由后端给（归档目录在它里面），前端不自己编")
	}
	if !strings.Contains(functionBody(t, front, "copyGitPrompt"), "bridge.CopyText") {
		t.Error("第 2 步要能一键复制（走 Wails 的剪贴板）")
	}

	// 第 3 步：认出远程就自己试一次，不等人点；只做一次。
	ensure := functionBody(t, front, "ensureGitGuide")
	if !strings.Contains(ensure, "detectedRemote") || !strings.Contains(ensure, "gitAutoTested") {
		t.Error("认出远程之后要自己试一次（且只做一次）")
	}
}

// I-058：Git 试连点下去要有即时反馈——按钮置灰、显示「正在测试…」，
// 测试中不能重复发起第二次请求；结束（成或败）后按钮恢复、结果照画。
func TestGitTestRemoteShowsProgressAndBlocksReentry(t *testing.T) {
	front := readSource(t, appJS)

	run := functionBody(t, front, "gitTestRemote")
	if !strings.Contains(run, "if (gitTesting)") {
		t.Error("测试中重复点击要直接返回，不能发起第二次请求")
	}
	if !strings.Contains(run, "gitTesting = true") {
		t.Error("开始试连要把 gitTesting 置真，让按钮进入「正在测试…」")
	}
	if !strings.Contains(run, "target.sig = \"\"") {
		t.Error("测试中要先把各位置的签名清掉再重画——否则签名防重画会挡住按钮变灰")
	}
	if !strings.Contains(run, "bridge.GitTestRemote") {
		t.Error("试连要调后端 GitTestRemote")
	}
	if !strings.Contains(run, "finally") || !strings.Contains(run, "gitTesting = false") {
		t.Error("结束（成功或失败）都要把 gitTesting 复位，按钮才能恢复")
	}

	draw := functionBody(t, front, "renderGitPanel")
	if !strings.Contains(draw, "gitTesting ? \"正在测试…\"") {
		t.Error("测试中按钮文字要换成「正在测试…」")
	}
	if !strings.Contains(draw, "testButton.disabled = gitTesting") {
		t.Error("测试中按钮要置灰，不能重复点")
	}
}

// 原生控件（单选圆点、勾选框）要跟着主题色，不能用浏览器默认的蓝（I-046，人提的）。
func TestNativeControlsUseTheThemeColor(t *testing.T) {
	css := readSource(t, styleCSS)

	idx := strings.Index(css, "accent-color")
	if idx < 0 {
		t.Fatal("style.css 里没设 accent-color：原生圆点会退回浏览器默认的蓝，等于第三个色系")
	}
	// 取这条声明所在的规则块，检查它用的确实是语义变量而不是裸色值。
	start := strings.LastIndex(css[:idx], "\n\n")
	if start < 0 {
		start = 0
	}
	end := strings.Index(css[idx:], "}")
	if end < 0 {
		end = len(css) - idx
	}
	block := css[start : idx+end]
	if !strings.Contains(block, `input[type="radio"]`) || !strings.Contains(block, `input[type="checkbox"]`) {
		t.Errorf("accent-color 要同时管单选钮与勾选框，实际的规则是：\n%s", strings.TrimSpace(block))
	}
	if !strings.Contains(block, "var(--") {
		t.Errorf("accent-color 要用语义变量，不写裸色值：\n%s", strings.TrimSpace(block))
	}
}

// 渠道那一段顶上的三层数字（I-023 / 人提的：不要再单独占一张卡片）。
func TestChannelSectionCarriesTheStats(t *testing.T) {
	page := readSource(t, indexHTML)
	front := readSource(t, appJS)

	if strings.Contains(page, "panel__title\">统计") {
		t.Error("统计不该再单独占一张卡片——并进渠道那一段")
	}
	if !strings.Contains(page, `id="channel-stats"`) {
		t.Error("渠道那一段顶要有一行放数字（本机会话 / 已收集 / 待收集）")
	}
	if !strings.Contains(front, `fill("channel-stats"`) {
		t.Error("主界面渲染时要把那行填上")
	}
	// 渠道那一行要把待收拆成「新会话 / 已收集会话有更新」，不做减法（I-062）。
	uiGo := readSource(t, uiGoSource)
	if !strings.Contains(uiGo, "个新会话") || !strings.Contains(uiGo, "个已收集会话有更新") {
		t.Error("渠道那一行要分别显示新会话与已收集会话的更新")
	}
}

// 首页渠道分组（I-066）：后端给出分组，首页按「已安装有会话 / 已安装无会话 /
// 未安装」三段展示，检测失败单列；引导页不分段，仍按原顺序平铺。
func TestHomeChannelsAreGroupedInThreeClasses(t *testing.T) {
	front := readSource(t, appJS)
	css := readSource(t, styleCSS)

	rows := functionBody(t, front, "renderChannelRows")
	if !strings.Contains(rows, "row.group") {
		t.Error("首页要按后端给的分组字段分段，不能自己猜")
	}
	for _, label := range []string{"已安装 · 有会话", "已安装 · 无会话", "未安装", "检测失败"} {
		if !strings.Contains(front, label) {
			t.Errorf("缺少分组标题 %q", label)
		}
	}
	if !strings.Contains(rows, "rows__group") {
		t.Error("每一段要有一行小字分开")
	}
	if !strings.Contains(rows, "withAction") {
		t.Error("分组只发生在首页；引导页要照旧平铺")
	}

	rule := cssRule(t, css, ".rows__group")
	if !strings.Contains(rule, "--space-") || !strings.Contains(rule, "--text-tertiary") {
		t.Error("分组小字要消费现有的间距与文字变量")
	}
	if strings.Contains(rule, "border") {
		t.Error("分组靠间距和小字，不画分隔线")
	}
}

// 引导页只管「看一眼」：渠道行不给重试按钮，备份设置要与设置页同步（人提的）。
func TestWizardChannelsAreReadOnlyAndInSync(t *testing.T) {
	front := readSource(t, appJS)

	if !strings.Contains(front, `renderChannelRows($("wizard-channels"), state.channels, false)`) {
		t.Error("引导页的渠道行不该带「重试」按钮（那儿不是干活的地方）")
	}
	if !strings.Contains(functionBody(t, front, "renderChannelRows"), "withAction") {
		t.Error("渠道行要能按调用方决定给不给动作按钮")
	}
	// 主界面那边照旧要按钮。
	if !strings.Contains(front, `renderChannelRows($("main-channels"), state.channels)`) {
		t.Error("主界面的渠道行要保留「重试」")
	}

	// 重走引导前先读回最新状态：设置页里刚改的备份方式要能反映进引导。
	replay := functionBody(t, front, "replayWizard")
	if !strings.Contains(replay, "bridge.State()") {
		t.Error("重走引导前要把状态读回最新的——不然引导显示的是旧设置")
	}
	// （它现在是 async 的——那句在函数名前面，函数体里找不到，所以判的是源码整体。）
}

// 设置页定时区块：「上次运行」排在「下次」前面，说明最后（I-073）。
func TestSettingsScheduleLineShowsLastRunBeforeNext(t *testing.T) {
	body := functionBody(t, readSource(t, appJS), "scheduleLine")

	if !strings.Contains(body, "scheduleLastRun") {
		t.Fatal("定时那一行要显示上次运行（数据来自运行记录）")
	}
	last := strings.Index(body, "上次运行")
	next := strings.Index(body, "下次")
	if last < 0 || next < 0 || last > next {
		t.Errorf("「上次运行」要排在「下次」前面，实际 last=%d next=%d", last, next)
	}
	note := strings.Index(body, "scheduleNote")
	if note < next {
		t.Error("那句说明要排在最后")
	}
}

// 设置页里那个「把旧目录里的 N 段复制过来」（I-024）。
func TestSettingsPageOffersTheMigrateButton(t *testing.T) {
	front := readSource(t, appJS)

	if !strings.Contains(functionBody(t, front, "renderSettings"), "form.migrateButton") {
		t.Error("设置页里要有那个复制按钮")
	}
	refresh := functionBody(t, front, "refreshSettings")
	if !strings.Contains(refresh, "archiveOld") || !strings.Contains(refresh, "段归档过来") {
		t.Error("按钮上的段数要来自后端，并按「换过目录没有」决定显不显示")
	}
	migrate := functionBody(t, front, "migrateArchive")
	for _, want := range []string{"bridge.MigrateArchive()", "refreshMainView", "settings = await bridge.Settings()"} {
		if !strings.Contains(migrate, want) {
			t.Errorf("搬完之后要 %s（搬完三个数得当场重算）", want)
		}
	}
}

// 首页只讲收集，不讲备份（I-053，推翻 I-037 的「行末备份秒数」）。
// 备份结果去日志页看；首页那一行再说一遍就是噪音，还会被误当成某个渠道的动作。
func TestRunNoteLeavesBackupToTheLog(t *testing.T) {
	front := readSource(t, appJS)

	draw := functionBody(t, front, "renderRun")
	for _, banned := range []string{"run.backupDetail", "run.backupErr", "run.backupHint"} {
		if strings.Contains(draw, banned) {
			t.Errorf("首页的收集结论不该出现 %s（备份去日志页看）", banned)
		}
	}
	if strings.Contains(draw, "个会话文件") {
		t.Error("单位统一成「段」，别再写「个会话文件」")
	}
}

// 日志页：导航里多一项、进来时才读、一天一行（I-030）。
func TestRunLogPageIsLazyAndOneRowPerDay(t *testing.T) {
	page := readSource(t, indexHTML)
	front := readSource(t, appJS)

	for _, want := range []string{`id="nav-log"`, `id="view-log"`, `id="log-rows"`} {
		if !strings.Contains(page, want) {
			t.Errorf("日志页缺 %s", want)
		}
	}
	if !strings.Contains(front, `$("nav-log").addEventListener("click", openLog)`) {
		t.Error("导航栏那一项要接到日志页上")
	}
	if !strings.Contains(functionBody(t, front, "openLog"), "renderLog") {
		t.Error("点进去才去读——这一页平时没人看")
	}
	read := functionBody(t, front, "renderLog")
	if !strings.Contains(read, "bridge.RunLog()") {
		t.Error("数据由后端按天合计给（前端不自己算账）")
	}
	detail := functionBody(t, front, "logDetail")
	for _, want := range []string{"没跑", "收下", "轮"} {
		if !strings.Contains(detail, want) {
			t.Errorf("那一行要说清那天的情况，缺 %q", want)
		}
	}
}

// 明暗小图标：用户可控（人提的「弄成明暗小图标，用户可控」）。
func TestThemeIsUserControllable(t *testing.T) {
	page := readSource(t, indexHTML)
	front := readSource(t, appJS)
	css := readSource(t, "frontend/dist/tokens.css")

	if !strings.Contains(page, `id="theme-toggle"`) {
		t.Error("页脚要有一个明暗小图标（按钮）")
	}
	if strings.Contains(page, `id="theme-dot"`) || strings.Contains(page, `id="theme-text"`) {
		t.Error("原来那行「主题：…」的诊断文字应当已被小图标取代")
	}
	// 三档循环，且选择记得住。
	for _, want := range []string{`THEME_ORDER = ["auto", "light", "dark"]`, "localStorage", "cycleTheme"} {
		if !strings.Contains(front, want) {
			t.Errorf("明暗切换里应当有 %s", want)
		}
	}
	if !strings.Contains(front, "cycleTheme") || !strings.Contains(functionBody(t, front, "boot"), "theme-toggle") {
		t.Error("小图标要真的接上")
	}
	// 手动选的那档要能盖过系统：tokens.css 里两套选择器都得在。
	for _, want := range []string{`@media (prefers-color-scheme: light)`, `:root:not([data-theme="dark"])`, `:root[data-theme="light"]`} {
		if !strings.Contains(css, want) {
			t.Errorf("tokens.css 里应当有 %s（不然手动选的那档盖不过系统）", want)
		}
	}
}

// 归档目录那道选择：选了才让点「选择文件夹」（人提的）。
func TestArchiveSwapChoiceGatesTheFolderPicker(t *testing.T) {
	front := readSource(t, appJS)

	build := functionBody(t, front, "renderSettings")
	if !strings.Contains(build, "form.archiveSwap") || !strings.Contains(build, "syncArchiveSwap") {
		t.Error("归档目录那一段要有「换目录时旧归档怎么办」的选项")
	}
	sync := functionBody(t, front, "syncArchiveSwap")
	if !strings.Contains(sync, "browse.disabled") {
		t.Error("没选之前「选择文件夹」应当是禁用的")
	}
	if !strings.Contains(functionBody(t, front, "saveSettingsEdits"), "copyOldArchives") {
		t.Error("选了「复制过去」要告诉后端（复制文件这件事不在前端做）")
	}
}

// Git 那一段：绿色引用边 + 按钮再小一档（人提的）。
func TestGitStepsLookSecondary(t *testing.T) {
	css := readSource(t, styleCSS)

	if rule := cssRule(t, css, ".git-steps"); !strings.Contains(rule, "--action-primary-bg") {
		t.Error("那三步要有一条绿色的引用边，一眼看得见")
	}
	if !strings.Contains(css, ".btn--small") {
		t.Error("要有一个更小一档的按钮样式")
	}
	if !strings.Contains(readSource(t, appJS), `"btn btn--quiet btn--small"`) {
		t.Error("引导里的按钮要比别处再小一档")
	}
}

// 日志那一行要能看出哪天备份成没成、最近一次说了什么（I-030 / I-053）。
func TestRunLogRowShowsTheBackup(t *testing.T) {
	detail := functionBody(t, readSource(t, appJS), "logDetail")
	for _, want := range []string{"备份成功", "backups", "backupFailures", "backupNote", "backupNoteFailed"} {
		if !strings.Contains(detail, want) {
			t.Errorf("日志那一行缺 %q（哪天备份成没成、为什么没成要看得出）", want)
		}
	}
}

// 引导页不给「旧数据怎么办」的选项：第一次用没有旧数据（人提的）。
func TestWizardArchiveStepHasNoSwapChoice(t *testing.T) {
	page := readSource(t, indexHTML)
	front := readSource(t, appJS)

	if strings.Contains(elementHTML(t, page, "view-wizard"), "archive-swap") {
		t.Error("引导页不该出现「换目录时旧归档怎么办」的选项")
	}
	if !strings.Contains(functionBody(t, front, "renderSettings"), "archive-swap") {
		t.Error("那道选项应当在设置页里")
	}
	if !strings.Contains(front, `$("wizard-archive-browse").addEventListener("click"`) {
		t.Error("引导页的「选择文件夹」照旧随手可点")
	}
	if isEmpty := false; isEmpty {
		t.Error("占位")
	}
	if sync := functionBody(t, front, "syncArchiveSwap"); strings.Contains(sync, "wizard") {
		t.Error("设置页那道闸门不该管到引导页")
	}
}

// 试连连上了就打一个绿色的勾（人提的：打勾最直观）。
func TestGitTestResultShowsAGreenCheck(t *testing.T) {
	front := readSource(t, appJS)
	css := readSource(t, styleCSS)

	if !strings.Contains(functionBody(t, front, "renderGitPanel"), "gitResult") {
		t.Error("连上没连上要用那一行结果来说（勾 / 叉）")
	}
	result := functionBody(t, front, "gitResult")
	if !strings.Contains(result, `"✓"`) {
		t.Error("连上了要打一个勾")
	}
	if !strings.Contains(result, "git-result--ok") {
		t.Error("成功那档要有自己的样式（勾是绿的）")
	}
	rule := cssRule(t, css, ".git-result--ok .git-result__mark")
	if !strings.Contains(rule, "--success") {
		t.Error("那个勾要是绿色的")
	}
}

// 渠道行末尾那两种标记：能点的箭头 / 绿勾，不再写「重试」两个字（人提的）。
func TestChannelRowMarkIsIconNotText(t *testing.T) {
	front := readSource(t, appJS)
	css := readSource(t, styleCSS)

	rows := functionBody(t, front, "channelRow")
	if !strings.Contains(rows, "channelMark") {
		t.Error("末尾那个标记要用两种状态的图标")
	}
	if strings.Contains(rows, `"重试"`) {
		t.Error("别再写成「重试」两个字了")
	}
	mark := functionBody(t, front, "channelMark")
	if !strings.Contains(mark, `"✓"`) || !strings.Contains(mark, `"↻"`) {
		t.Error("没有新内容给绿勾、还能收的给箭头")
	}
	if !strings.Contains(mark, "row.pendingNew") || !strings.Contains(mark, "row.pendingUpdated") {
		t.Error("箭头要说清还有多少会话没收（新会话 + 已收集有更新）")
	}
	if rule := cssRule(t, css, ".mark--done"); !strings.Contains(rule, "--success") {
		t.Error("那个勾要是绿的")
	}
}

// 按钮的开关挂在「调用还没回来」上，不挂在进度事件上
// （人报的：点完某个渠道，增量收集就点不动了）。
func TestCollectButtonStateFollowsTheCallNotTheEvents(t *testing.T) {
	front := readSource(t, appJS)

	if strings.Contains(functionBody(t, front, "onProgress"), "setCollecting") {
		t.Error("进度事件不该去开关按钮：事件晚一步到，按钮就被一直按在禁用上")
	}
	if !strings.Contains(functionBody(t, front, "collect"), "beginCollectCall(false)") {
		t.Error("「增量收集」是整轮，开始时要按上")
	}
	if !strings.Contains(functionBody(t, front, "retryChannel"), "beginCollectCall(true)") {
		t.Error("单点某一行要告诉它「这是单点」——单点时不把主按钮按灰（人提的）")
	}
	for _, name := range []string{"collect", "retryChannel"} {
		if !strings.Contains(functionBody(t, front, name), "endCollectCall()") {
			t.Errorf("%s 回来时要放开", name)
		}
	}
	set := functionBody(t, front, "setCollecting")
	if !strings.Contains(set, "collectingScoped") {
		t.Error("单点时主按钮不该灰（人提的：一个一个点、或一次全收，两件事随时都能做）")
	}
	if !strings.Contains(set, ".mark--collect") {
		t.Error("行内那些箭头也要跟着一起开关")
	}
}

// 按行显示 0%-100%，跑完那一行刷成勾（人提的：不然以为卡顿了）。
func TestChannelRowShowsPercentWhileRunning(t *testing.T) {
	front := readSource(t, appJS)

	progress := functionBody(t, front, "onProgress")
	if !strings.Contains(progress, "percent") || !strings.Contains(progress, `+ "%"`) {
		t.Error("那一行末尾要走 0% 到 100%")
	}
	if !strings.Contains(progress, "rowMark") || !strings.Contains(functionBody(t, front, "rowMark"), "data-id") {
		t.Error("进度要落到对应的那一行上（按渠道 id 找）")
	}
	if !strings.Contains(progress, "progress.finished") || !strings.Contains(progress, "refreshMainView") {
		t.Error("某个渠道跑完要重读状态，把那一行画成准的")
	}
	// 备份是整轮收尾，首页不显示（I-053）：事件一到就返回，连文字都不换。
	backupAt := strings.Index(progress, `progress.stage === "backup"`)
	busyAt := strings.Index(progress, "busyText = progress.text")
	if backupAt < 0 || busyAt < 0 || backupAt > busyAt {
		t.Error("备份阶段要在首页进度里摘掉：先判断 backup，再动那行字")
	}
	if strings.Contains(functionBody(t, front, "startBusyTicker"), "备份") {
		t.Error("首页的秒表不该再往渠道行上写「备份 Ns」")
	}
	if !strings.Contains(functionBody(t, front, "stopBusyTicker"), "clearInterval") {
		t.Error("收完要把那个秒表停掉")
	}
	rows := functionBody(t, front, "channelRow")
	if !strings.Contains(rows, "dataset.id") {
		t.Error("每一行要挂上 id，进度事件才找得到它")
	}
	// 勾后面跟用时（单点那一轮），那句话在这儿，不在顶部。
	if !strings.Contains(functionBody(t, front, "channelMark"), "row.lastDuration") {
		t.Error("「✓ 11.024s」该跟在那一行的勾后面")
	}
}

// 「检测到 N 段 · 备份：…」那一行只属于「增量收集」；单点一个渠道不显示（人提的）。
func TestRunHintOnlyForTheFullCollect(t *testing.T) {
	draw := functionBody(t, readSource(t, appJS), "renderRun")
	if !strings.Contains(draw, "run.scope") {
		t.Error("单点渠道（有 scope）时不该再显示那一行备份提示")
	}
}

// 窗口固定，内容区自己滚（I-067）。
//
// 判据落在布局上：整页不滚、Shell 占满窗口、头部与导航不被挤小、
// 视图本身是滚动框；滚动条颜色只用语义变量。
func TestContentScrollsInsideItsOwnFrame(t *testing.T) {
	css := readSource(t, styleCSS)
	page := readSource(t, indexHTML)

	// 直接认「body 这一条规则里写着 overflow: hidden」，避开上面的 `html, body`。
	if !strings.Contains(css, "body {\n  overflow: hidden;") {
		t.Error("窗口本身不该整页滚：body 要 overflow: hidden")
	}

	shell := cssRule(t, css, ".shell")
	for _, want := range []string{"height: 100%", "min-height: 0"} {
		if !strings.Contains(shell, want) {
			t.Errorf(".shell 要固定成窗口高度（缺 %q），内容才不会把整页撑高", want)
		}
	}

	// 主区域只排版：滚的是内容框，主操作留在框外。
	main := cssRule(t, css, ".shell__main")
	if strings.Contains(main, "overflow-y: auto") {
		t.Error(".shell__main 不该自己滚：一滚就会把「增量收集」和提示带出视野")
	}
	for _, want := range []string{"flex: 1", "min-height: 0"} {
		if !strings.Contains(main, want) {
			t.Errorf("主区域要占满剩下的高度（缺 %q）", want)
		}
	}

	frame := cssRule(t, css, ".scroll-area")
	for _, want := range []string{
		"flex: 1",
		"min-height: 0",
		"overflow-x: hidden",
		"overflow-y: auto",
	} {
		if !strings.Contains(frame, want) {
			t.Errorf("内容框要自己滚（缺 %q）", want)
		}
	}
	if !strings.Contains(cssRule(t, css, ".scroll-area > *"), "flex: none") {
		t.Error("内容框里的子项不该被挤扁：放不下时就该滚")
	}

	// 首页渠道、日志、搜索、引导三步都是同一个框；设置页是表单那一块（I-084 起多一页）。
	if n := strings.Count(page, `class="panel scroll-area"`); n != 6 {
		t.Errorf("首页、日志、搜索与引导三步都该用 panel scroll-area，实际 %d 处", n)
	}
	if !strings.Contains(page, `class="settings__body scroll-area"`) {
		t.Error("设置页的表单区要用同一个滚动框，底部的保存行留在框外")
	}

	// 滚动条自己画，颜色与宽度都从 tokens 取。
	bar := cssRule(t, css, ".scroll-area::-webkit-scrollbar")
	if !strings.Contains(bar, "--space-2") {
		t.Error("滚动条宽度要从间距变量取")
	}
	thumb := cssRule(t, css, ".scroll-area::-webkit-scrollbar-thumb")
	if !strings.Contains(thumb, "--border-tertiary") {
		t.Error("滚动条颜色要跟随主题的语义变量（暗色下不能是系统浅色）")
	}

	// 框外的东西都不许被内容挤走。
	for _, sel := range []string{".shell__head", ".nav", ".actions", ".settings__foot"} {
		if !strings.Contains(cssRule(t, css, sel), "flex: none") {
			t.Errorf("%s 固定在框外，不该被内容区挤小", sel)
		}
	}
}

// 搜索页（I-084 / F13）：四个元素在、接线在、视图切换认识它。
func TestSearchViewIsWired(t *testing.T) {
	page := readSource(t, indexHTML)
	front := readSource(t, appJS)

	for _, want := range []string{
		`id="nav-search"`, `id="view-search"`, `id="search-input"`,
		`id="search-go"`, `id="search-note"`, `id="search-rows"`, ">搜索<",
	} {
		if !strings.Contains(page, want) {
			t.Errorf("搜索页缺 %s", want)
		}
	}
	if !strings.Contains(front, `$("nav-search").addEventListener("click", openSearch)`) {
		t.Error("搜索入口没接上导航栏")
	}
	if !strings.Contains(front, `window.go.main.App.SearchMemory`) {
		t.Error("搜索没走后端绑定")
	}
	// 切片导出（F14）：按钮在、接线在、绑定在。
	for _, want := range []string{`id="search-export"`, ">导出切片<"} {
		if !strings.Contains(page, want) {
			t.Errorf("切片导出缺 %s", want)
		}
	}
	if !strings.Contains(front, `window.go.main.App.ExportSearchNDJSON`) {
		t.Error("切片导出没走后端绑定")
	}
	// 过滤（I-084 A2）：三个控件在，SearchOptions 与带参检索的接线在。
	for _, want := range []string{`id="search-channel"`, `id="search-days"`, `id="search-project"`, `id="search-project-options"`} {
		if !strings.Contains(page, want) {
			t.Errorf("搜索过滤缺 %s", want)
		}
	}
	if !strings.Contains(front, `window.go.main.App.SearchOptions`) {
		t.Error("过滤下拉没走后端绑定")
	}
	if !strings.Contains(front, `App.SearchMemory(query, channelID, project, days)`) {
		t.Error("搜索没把过滤条件带给后端")
	}
	show := functionBody(t, front, "show")
	for _, want := range []string{`$("view-search")`, `$("nav-search")`} {
		if !strings.Contains(show, want) {
			t.Errorf("show() 没有管 %s", want)
		}
	}
}

// I-084 验收反馈：搜索页「全部渠道」这个默认项必须带空 value。
// 不写 value 时浏览器拿显示文字当值，后端按渠道精确过滤就一条都查不到。
func TestSearchDefaultChannelOptionHasEmptyValue(t *testing.T) {
	front := readSource(t, appJS)
	if !strings.Contains(front, `allChannels.value = ""`) {
		t.Error("「全部渠道」没有显式的空 value：默认过滤会按渠道名精确匹配，结果为空")
	}
}

// I-084 验收反馈：搜索命中用「标题一行 + 片段两行」的块布局。
// 复用渠道行的「名称 + 说明」并排布局时，长标题会把片段挤成窄条，行高一大一小。
func TestSearchRowsUseBlockLayout(t *testing.T) {
	front := readSource(t, appJS)
	body := functionBody(t, front, "searchRow")
	if !strings.Contains(body, `el("li", "search-hit")`) {
		t.Error("搜索命中应当用 search-hit 块布局")
	}
	if strings.Contains(body, "row__name") {
		t.Error("搜索命中不该复用渠道行的并排布局：长标题会把片段挤窄")
	}
}

// I-091：设置页要有「对外接口」开关，切换后立刻保存。
func TestSettingsHasMCPSwitch(t *testing.T) {
	front := readSource(t, appJS)
	for _, want := range []string{`mcpOn.id = "set-mcp-on"`, "mcpEnabled: mcpOn.checked", `"对外接口"`} {
		if !strings.Contains(front, want) {
			t.Errorf("设置页缺 %q", want)
		}
	}
}
