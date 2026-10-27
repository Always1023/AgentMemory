package channels_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"github.com/Always1023/AgentMemory/internal/channel"
	"github.com/Always1023/AgentMemory/internal/channels"
)

// 这个文件是「本地发现不得写死开发机」的守卫（I-059）。
//
// 判据读源码文本与字符串字面量，不读运行结果：写死路径这类问题在开发机上
// 永远跑得通，只有换一台电脑、换一个 Windows 用户才暴露——而那时已经发出去了。

// repoRoot 从本测试文件往上找到 go.mod 所在目录。
func repoRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("取不到本文件路径")
	}
	dir := filepath.Dir(file)
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("往上找不到 go.mod：测试不在仓库里？")
		}
		dir = parent
	}
}

// hardcodedPath 是生产代码里不该出现的固定路径。
//
// 只看字符串字面量（注释、导入路径都不算）：导入路径里有账号名是正常的，
// 而真正的风险是一句 filepath.Join 里写死了谁的家目录。
var hardcodedPath = []struct {
	name string
	re   *regexp.Regexp
}{
	// 任意盘符开头的绝对路径。前面用 \b 避开 URL 里的 "https://"（s 前是 p，不是边界）。
	{"盘符绝对路径", regexp.MustCompile(`\b[A-Za-z]:[\\/]`)},
	{"macOS 用户目录", regexp.MustCompile(`/Users/`)},
	{"Linux 用户目录", regexp.MustCompile(`/home/`)},
	{"开发机的家目录", regexp.MustCompile(`(?i)users[\\/]always`)},
}

// 一、生产代码里不许出现固定家目录 / 盘符路径。
//
// 配置目录走 %APPDATA% 或 AGENTMEMORY_HOME，渠道根目录走 os.UserHomeDir /
// os.UserConfigDir，归档目录从配置读（AC-09）。三者都不需要写死路径。
func TestProductionCodeHasNoHardcodedHomePaths(t *testing.T) {
	root := repoRoot(t)
	fset := token.NewFileSet()

	var bad []string
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			switch entry.Name() {
			case ".git", "build", "node_modules", "tmp_worldmap", ".gotmp":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		file, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			return err
		}
		ast.Inspect(file, func(node ast.Node) bool {
			lit, ok := node.(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				return true
			}
			value, err := strconv.Unquote(lit.Value)
			if err != nil {
				return true
			}
			for _, rule := range hardcodedPath {
				if rule.re.MatchString(value) {
					pos := fset.Position(lit.Pos())
					rel, _ := filepath.Rel(root, pos.Filename)
					bad = append(bad, rel+":"+strconv.Itoa(pos.Line)+" 出现"+rule.name+"："+lit.Value)
				}
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatalf("扫描生产代码: %v", err)
	}
	if len(bad) > 0 {
		t.Errorf("生产代码里不许写死本机路径（换台电脑就找不到数据了）：\n%s", strings.Join(bad, "\n"))
	}
}

// rootSource 是每个渠道适配器该从哪儿推默认根目录。
//
// 表里的每一项都要真的对得上：换了 API（比如 Cursor 改用 UserHomeDir）也会红，
// 因为那意味着路径判断的依据变了，得有人看一眼。
var rootSource = map[string]struct {
	id  string
	api string
}{
	"claudecode":   {"claude-code", "os.UserHomeDir()"},
	"codex":        {"codex", "os.UserHomeDir()"},
	"continuedev":  {"continue", "os.UserHomeDir()"},
	"copilotchat":  {"copilot-chat", "os.UserConfigDir()"},
	"cursor":       {"cursor", "os.UserConfigDir()"},
	"opencode":     {"opencode", "os.UserHomeDir()"},
	"workbuddy":    {"workbuddy", "os.UserHomeDir()"},
	"zcode":        {"zcode", "os.UserHomeDir()"},
	"gemini":       {"gemini", "os.UserHomeDir()"},
	"grokbuild":    {"grok-build", "os.UserHomeDir()"},
	"hermes":       {"hermes", "os.UserHomeDir()"},
	"minimax":      {"minimax", "os.UserHomeDir()"},
	"openclaw":     {"openclaw", "os.UserHomeDir()"},
	"pi":           {"pi", "os.UserHomeDir()"},
	"commandcode":  {"commandcode", "os.UserHomeDir()"},
	"factory":      {"factory", "os.UserHomeDir()"},
	"qoder":        {"qoder", "os.UserHomeDir()"},
	"cherrystudio": {"cherry-studio", "os.UserConfigDir()"},
	// 网页版归档的根在归档目录（配置注入），兜底走默认归档位置（家目录）。
	"webarchive": {"web-archive", "os.UserHomeDir()"},
}

// helperDirs 是 channel 下的共享实现，不是渠道适配器，不参与根目录守卫。
var helperDirs = map[string]bool{
	"claudejsonl": true,
}

// 二、每个适配器：根目录从系统 API 推，并且能被 Root 覆盖。
//
// 新增一个渠道时必须在这张表里声明来源——漏了会红，逼着作者想一遍
// 「换台电脑这个路径还对不对」。
func TestEveryAdapterResolvesRootFromOSAndCanBeOverridden(t *testing.T) {
	root := repoRoot(t)
	base := filepath.Join(root, "internal", "channel")

	dirs, err := os.ReadDir(base)
	if err != nil {
		t.Fatal(err)
	}

	for _, dir := range dirs {
		if !dir.IsDir() {
			continue
		}
		name := dir.Name()
		if helperDirs[name] {
			continue
		}
		want, ok := rootSource[name]
		if !ok {
			t.Errorf("渠道 %q 没在 rootSource 表里声明根目录来源：新增渠道要说清默认根目录从哪儿来", name)
			continue
		}
		source := readPackage(t, filepath.Join(base, name))
		if !strings.Contains(source, want.api) {
			t.Errorf("%s：默认根目录要用 %s 推，不能写死", name, want.api)
		}
		if !strings.Contains(source, `a.Root != ""`) {
			t.Errorf("%s：适配器要留 Root 覆盖（换目录 / 陌生电脑时由调用方指定）", name)
		}
	}

	// 注册表里的每个渠道都得有对应的适配器目录，反过来也一样——
	// 只加注册表不加目录、或加了目录忘了注册，都不该悄悄发生。
	registered := map[string]bool{}
	for _, entry := range channels.All() {
		registered[entry.ID] = true
	}
	for name, want := range rootSource {
		if !registered[want.id] {
			t.Errorf("适配器目录 %q 对应的渠道 %q 没注册进 channels.All()", name, want.id)
		}
	}
	for id := range registered {
		found := false
		for _, want := range rootSource {
			if want.id == id {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("注册表里的渠道 %q 没有对应的适配器目录（或没在 rootSource 表里）", id)
		}
	}
}

// readPackage 把一个渠道包里非测试的 .go 文件拼起来读。
func readPackage(t *testing.T, dir string) string {
	t.Helper()
	files, err := filepath.Glob(filepath.Join(dir, "*.go"))
	if err != nil {
		t.Fatal(err)
	}
	var out strings.Builder
	for _, file := range files {
		if strings.HasSuffix(file, "_test.go") {
			continue
		}
		raw, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		out.Write(raw)
	}
	if out.Len() == 0 {
		t.Fatalf("%s 里没有生产代码", dir)
	}
	return out.String()
}

// 三、换一个家目录，检测结果全部落在那个家目录里。
//
// 这条是「不读开发机路径」的运行时佐证：把 USERPROFILE / APPDATA 指到临时目录，
// 每个渠道的根目录要么为空（未安装），要么在这个临时目录下面。
func TestDetectionUnderForeignHomeStaysInsideIt(t *testing.T) {
	home := t.TempDir()
	t.Setenv("USERPROFILE", home)
	t.Setenv("HOME", home)
	t.Setenv("APPDATA", filepath.Join(home, "AppData", "Roaming"))

	registry := channel.NewRegistry()
	if err := channels.RegisterInto(registry); err != nil {
		t.Fatalf("注册渠道: %v", err)
	}

	for _, report := range registry.DetectAll() {
		if report.Root == "" {
			continue // 未安装：临时家目录里当然没有这些产品
		}
		if !strings.HasPrefix(report.Root, home) {
			t.Errorf("%s 的根目录 %q 跑到临时家目录 %q 外面了：说明它读的不是系统给的家目录",
				report.ID, report.Root, home)
		}
	}
}
