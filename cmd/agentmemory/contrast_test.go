package main

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// 这一组用例把 docs/DESIGN_SYSTEM.md 里「自动跑、不靠肉眼」的要求落成代码：
// 对比度门槛（3.6）、只许两个色系、组件不许写裸值。
//
// 判据全部从 cmd/agentmemory/frontend/dist/tokens.css 读出来——那是数值的唯一真源，
// 用例写死期望值就等于又抄了一份，改了变量反而没人拦。

const tokensPath = "frontend/dist/tokens.css"

// tokensOf 把 tokens.css 解析成「暗色 / 亮色」两套变量。
func tokensOf(t *testing.T) (dark, light map[string]string) {
	t.Helper()
	raw, err := os.ReadFile(tokensPath)
	if err != nil {
		t.Fatalf("读 %s: %v", tokensPath, err)
	}
	return parseTokens(string(raw))
}

// parseTokens 逐行解析；亮色那套认在 `prefers-color-scheme: light` 块里。
func parseTokens(css string) (dark, light map[string]string) {
	dark, light = map[string]string{}, map[string]string{}
	depth, lightDepth := 0, -1

	for _, line := range strings.Split(css, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.Contains(trimmed, "prefers-color-scheme: light") {
			lightDepth = depth
		}
		depth += strings.Count(line, "{")

		if strings.HasPrefix(trimmed, "--") {
			if i := strings.Index(trimmed, ":"); i > 0 {
				name := strings.TrimSpace(trimmed[:i])
				value := strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(trimmed[i+1:]), ";"))
				target := dark
				if lightDepth >= 0 {
					target = light
				}
				target[name] = value
			}
		}

		depth -= strings.Count(line, "}")
		if lightDepth >= 0 && depth <= lightDepth {
			lightDepth = -1
		}
	}
	return dark, light
}

// channel 取出 `R G B` 里的一个通道，0–255。
func channelValue(t *testing.T, value string, index int) float64 {
	t.Helper()
	parts := strings.Fields(value)
	if len(parts) < 3 {
		t.Fatalf("值 %q 不是 R G B 三元组", value)
	}
	n, err := strconv.Atoi(parts[index])
	if err != nil || n < 0 || n > 255 {
		t.Fatalf("值 %q 的第 %d 个通道不是 0–255 的整数", value, index+1)
	}
	return float64(n) / 255
}

// relativeLuminance 是 WCAG 2.x 的相对亮度。
func relativeLuminance(t *testing.T, value string) float64 {
	t.Helper()
	linear := func(c float64) float64 {
		if c <= 0.03928 {
			return c / 12.92
		}
		return math.Pow((c+0.055)/1.055, 2.4)
	}
	return 0.2126*linear(channelValue(t, value, 0)) +
		0.7152*linear(channelValue(t, value, 1)) +
		0.0722*linear(channelValue(t, value, 2))
}

// contrast 算两个颜色之间的对比度。
func contrast(t *testing.T, a, b string) float64 {
	t.Helper()
	la, lb := relativeLuminance(t, a), relativeLuminance(t, b)
	if la < lb {
		la, lb = lb, la
	}
	return (la + 0.05) / (lb + 0.05)
}

// pair 是一处要检查的配色关系。
type pair struct {
	why    string // 这处是什么
	fg, bg string // 变量名
	floor  float64
}

// pairs 列出要跑的检查。门槛照 3.6 的原话：
// 正文与链接 ≥ 4.5:1；按钮与大字 ≥ 3:1。
func pairs() []pair {
	return []pair{
		{"正文 · 主文字在页面底", "--text-primary", "--bg-0", 4.5},
		{"正文 · 次级文字在页面底", "--text-secondary", "--bg-0", 4.5},
		{"正文 · 主文字在面板", "--text-primary", "--bg-1", 4.5},
		{"正文 · 次级文字在面板", "--text-secondary", "--bg-1", 4.5},
		{"正文 · 三级文字在页面底", "--text-tertiary", "--bg-0", 4.5},
		{"链接 · 在页面底", "--text-link", "--bg-0", 4.5},
		{"链接 · 在面板", "--text-link", "--bg-1", 4.5},
		{"按钮 · 底与页面底的区分", "--action-primary-bg", "--bg-0", 3},
		{"按钮 · 字在按钮底上", "--action-primary-fg", "--action-primary-bg", 3},
		{"大字 · 主文字在页面底", "--text-primary", "--bg-0", 3},
		{"焦点环 · 与页面底", "--focus-ring", "--bg-0", 3},
		{"状态 · 成功标记与页面底", "--success", "--bg-0", 3},
		{"状态 · 危险标记与页面底", "--danger", "--bg-0", 3},
		{"状态 · 提醒标记与页面底", "--warn", "--bg-0", 3},
	}
}

// 验收标准 6：正文与链接 ≥ 4.5:1、按钮与大字 ≥ 3:1，明暗两套自动跑通。
func TestContrastMeetsThresholds(t *testing.T) {
	dark, light := tokensOf(t)
	if len(dark) == 0 || len(light) == 0 {
		t.Fatalf("没解析出两套变量：暗色 %d 项、亮色 %d 项", len(dark), len(light))
	}

	for _, mode := range []struct {
		name  string
		value map[string]string
	}{{"暗色", dark}, {"亮色", light}} {
		for _, p := range pairs() {
			fg, ok := mode.value[p.fg]
			if !ok {
				t.Errorf("%s 缺变量 %s", mode.name, p.fg)
				continue
			}
			bg, ok := mode.value[p.bg]
			if !ok {
				t.Errorf("%s 缺变量 %s", mode.name, p.bg)
				continue
			}
			got := contrast(t, fg, bg)
			if got < p.floor {
				t.Errorf("%s · %s：%s 对 %s = %.2f:1，低于门槛 %.1f:1",
					mode.name, p.why, p.fg, p.bg, got, p.floor)
				continue
			}
			t.Logf("%.2f:1  %s · %s", got, mode.name, p.why)
		}
	}
}

// 最弱文字档只能当占位 / 禁用：它够得着「非文本」的门槛，但够不着正文门槛。
// 这条用例把这个事实固定下来——哪天有人把它拿去当正文，这里的注释就是依据。
func TestWeakestTextIsOnlyForPlaceholders(t *testing.T) {
	dark, light := tokensOf(t)

	for _, mode := range []struct {
		name  string
		value map[string]string
	}{{"暗色", dark}, {"亮色", light}} {
		got := contrast(t, mode.value["--text-quaternary"], mode.value["--bg-0"])
		t.Logf("%.2f:1  %s · 最弱文字对页面底（3.6 写明：只作占位 / 禁用，不作正文）", got, mode.name)
		if got < 3 {
			t.Errorf("%s · 最弱文字对页面底 = %.2f:1，连占位都看不清（想要 ≥ 3:1）", mode.name, got)
		}
	}
}

// 验收标准 3：语义变量含 DESIGN_SYSTEM 列出的全部项，明暗两套齐全。
//
// 「两套齐全」只对**跟着明暗走的那批**（颜色、半透明）成立；字号 / 间距 / 圆角 /
// 字族 / 动效这些与明暗无关，只在 :root 里写一份才是对的——在两套里各抄一份，
// 改的时候必然漏一处。
func TestBothModesCarryTheSameVariables(t *testing.T) {
	dark, light := tokensOf(t)

	isColour := regexp.MustCompile(`^\d{1,3} \d{1,3} \d{1,3}( / \d+(\.\d+)?%)?$`)
	paired := 0
	for name, value := range dark {
		if !isColour.MatchString(value) {
			continue
		}
		paired++
		if _, ok := light[name]; !ok {
			t.Errorf("亮色缺 %s（颜色档必须两套成对）", name)
		}
	}
	for name, value := range light {
		if !isColour.MatchString(value) {
			continue
		}
		if _, ok := dark[name]; !ok {
			t.Errorf("暗色缺 %s（颜色档必须两套成对）", name)
		}
	}
	if paired == 0 {
		t.Fatal("一个颜色档都没检查到——解析大概坏了")
	}
	t.Logf("%d 个颜色档在明暗两套里成对出现", paired)

	// DESIGN_SYSTEM §3 逐条点名的项，一个都不能少。
	required := []string{
		"--text-primary", "--text-secondary", "--text-tertiary", "--text-quaternary",
		"--bg-0", "--bg-1", "--bg-2", "--bg-3",
		"--bg-secondary", "--bg-tertiary", "--bg-quaternary",
		"--border-primary", "--border-secondary", "--border-tertiary",
		"--bg-translucent", "--border-translucent", "--border-translucent-strong", "--overlay-primary",
		"--action-primary-bg", "--action-primary-fg",
		"--success", "--danger", "--note",
		"--warn", "--focus-ring", "--text-link",
	}
	for _, name := range required {
		if _, ok := dark[name]; !ok {
			t.Errorf("缺语义变量 %s", name)
		}
	}
}

// palette 是 3.6 给的两支色系（明暗各 10 档）＋ 参考灰阶（明暗各 14 档）＋ 半透明基色。
// 台账式列出，不推导：抄错一个数，第三色系那条用例就会红。
var palette = strings.Fields(`
232:246:237 214:238:222 178:222:195 134:199:160 84:176:124 40:150:96 5:122:72 2:106:62 2:74:44 1:56:34
13:30:22 19:42:31 30:66:48 78:207:146 60:182:122 40:152:96 96:214:156 134:226:176 178:238:203 216:246:227
253:244:235 250:232:214 243:209:173 235:184:135 229:168:107 224:154:95 197:125:64 158:93:38 122:70:28 94:53:21
41:29:19 56:40:26 86:61:39 224:154:95 206:137:79 187:118:62 214:147:86 229:168:107 240:198:156 249:226:203
8:9:10 15:16:17 20:21:22 25:26:27 28:28:31 35:35:38 40:40:44 247:248:248 208:214:224 138:143:152 98:102:109 35:37:42 52:52:58 62:62:68
255:255:255 248:248:248 244:244:244 240:240:240 249:248:249 244:242:244 238:237:239 40:42:48 60:65:73 111:110:119 134:132:141 233:232:234 228:226:228 220:219:221
0:0:0
`)

// 验收标准 4：全站不出现第三个色系。
//
// 做法是台账比对：tokens.css 里每一个 `R G B` 都必须能在 3.6 的两支色系或参考灰阶里
// 找到。自己「顺手调一个更舒服的蓝」在这里过不去。
func TestOnlyTwoColourFamilies(t *testing.T) {
	allowed := map[string]bool{}
	for _, triple := range palette {
		allowed[strings.ReplaceAll(triple, ":", " ")] = true
	}

	dark, light := tokensOf(t)
	triple := regexp.MustCompile(`^\d{1,3} \d{1,3} \d{1,3}$`)

	seen := 0
	for _, mode := range []struct {
		name  string
		value map[string]string
	}{{"暗色", dark}, {"亮色", light}} {
		for name, value := range mode.value {
			// 半透明写成 `R G B / x%`，取斜杠前那截。
			head := strings.TrimSpace(strings.SplitN(value, "/", 2)[0])
			if !triple.MatchString(head) {
				continue
			}
			seen++
			if !allowed[head] {
				t.Errorf("%s · %s = %s 不在 3.6 的两支色系与参考灰阶里（第三个色系？）", mode.name, name, head)
			}
		}
	}
	if seen == 0 {
		t.Fatal("一个色值都没检查到——解析大概坏了")
	}
	t.Logf("核对了 %d 个色值，全部来自 3.6 的两支色系与参考灰阶", seen)
}

// 验收标准 5：组件文件里无裸色值、无裸尺寸字面量。
//
// tokens.css 是唯一允许出现裸值的地方；别的文件只能消费变量。
func TestComponentsUseTokensOnly(t *testing.T) {
	files := []string{
		"frontend/dist/style.css",
		"frontend/dist/index.html",
		"frontend/dist/app.js",
	}
	patterns := []struct {
		re   *regexp.Regexp
		what string
	}{
		{regexp.MustCompile(`#[0-9a-fA-F]{3,8}\b`), "裸色值（#rrggbb）"},
		{regexp.MustCompile(`\d+px`), "裸尺寸（px 字面量）"},
		{regexp.MustCompile(`\d+rem`), "裸尺寸（rem 字面量）"},
	}
	// rgb()/rgba() 只有在里面不是 var(...) 时才算裸值：
	// `rgb(var(--text-primary))` 是约定的取用写法（--x: R G B 配 rgb(var(--x))）。
	colourCall := regexp.MustCompile(`rgba?\(([^()]*(\([^()]*\))?[^()]*)`)

	for _, name := range files {
		raw, err := os.ReadFile(filepath.FromSlash(name))
		if err != nil {
			t.Fatalf("读 %s: %v", name, err)
		}
		for i, line := range strings.Split(string(raw), "\n") {
			// 注释行不免除——注释里放裸值一样会被人抄走。
			for _, p := range patterns {
				if m := p.re.FindString(line); m != "" {
					t.Errorf("%s 第 %d 行出现%s：%q\n  %s", name, i+1, p.what, m, strings.TrimSpace(line))
				}
			}
			for _, call := range colourCall.FindAllString(line, -1) {
				if !strings.Contains(call, "var(") {
					t.Errorf("%s 第 %d 行出现裸色值：%q\n  %s", name, i+1, call, strings.TrimSpace(line))
				}
			}
		}
	}
}

// 顺带守住 tokens.css 自己的写法：半透明那组必须带百分比，
// 免得被人写成实心色后「看不出哪里不对」。
func TestTranslucentTokensKeepTheirAlpha(t *testing.T) {
	dark, light := tokensOf(t)
	for _, name := range []string{"--bg-translucent", "--border-translucent", "--border-translucent-strong", "--overlay-primary"} {
		for _, mode := range []struct {
			label string
			value map[string]string
		}{{"暗色", dark}, {"亮色", light}} {
			v := mode.value[name]
			if !strings.Contains(v, "/") || !strings.Contains(v, "%") {
				t.Errorf("%s · %s = %q，半透明档丢了透明度", mode.label, name, v)
			}
		}
	}
	_ = fmt.Sprint
}
