package git

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Always1023/AgentMemory/internal/backup"
)

// tempDir 造一个临时目录，并**确认它不在任何 git 仓库里**。
//
// 为什么较真：这些用例会真跑 git init / add / commit / push。要是临时目录落在某个
// 仓库内部（比如有人把 TMP 指到项目目录里），git 会一路向上找到外层仓库，
// 把**真实仓库**提交掉、甚至推上去——2026-10-17 就真发生过一次。
func tempDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()

	cmd := exec.Command("git", "-C", dir, "rev-parse", "--show-toplevel")
	if out, err := cmd.CombinedOutput(); err == nil {
		t.Fatalf("临时目录 %s 落在 git 仓库 %s 里——这些用例会真的提交，换个临时目录再跑",
			dir, strings.TrimSpace(string(out)))
	}
	return dir
}

// needGit 跳过没装 git 的机器。
func needGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("这台机器上没有 git，跳过这条用例")
	}
}

// hermetic 给测试一套自己的 git 身份，不依赖机器上的全局配置。
func hermetic(t *testing.T) {
	t.Helper()
	t.Setenv("GIT_AUTHOR_NAME", "AgentMemory 测试")
	t.Setenv("GIT_AUTHOR_EMAIL", "test@example.com")
	t.Setenv("GIT_COMMITTER_NAME", "AgentMemory 测试")
	t.Setenv("GIT_COMMITTER_EMAIL", "test@example.com")
}

// newRemote 造一个裸仓库当远程用——不碰网络，也不需要凭据。
func newRemote(t *testing.T) string {
	t.Helper()
	remote := filepath.Join(tempDir(t), "远程.git")
	cmd := exec.Command("git", "init", "--bare", "-b", "main", remote)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("建裸仓库失败: %v\n%s", err, out)
	}
	return remote
}

// writeArchive 在归档目录里铺一批文件。
func writeArchive(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	for name, content := range files {
		full := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

// remoteFiles 列出远程仓库某个分支上的文件。
func remoteFiles(t *testing.T, remote, branch string) []string {
	t.Helper()
	cmd := exec.Command("git", "-c", "core.quotepath=false", "-C", remote, "ls-tree", "-r", "--name-only", branch)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("读远程仓库失败: %v\n%s", err, out)
	}
	var files []string
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if line != "" {
			files = append(files, line)
		}
	}
	return files
}

// commitCount 数远程仓库某个分支上有几个提交。
func commitCount(t *testing.T, remote, branch string) int {
	t.Helper()
	cmd := exec.Command("git", "-C", remote, "rev-list", "--count", branch)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("数提交失败: %v\n%s", err, out)
	}
	n, err := strconv.Atoi(strings.TrimSpace(string(out)))
	if err != nil {
		t.Fatalf("提交数读不出来: %q", string(out))
	}
	return n
}

// 验收标准 1 的机制侧：归档目录里的文件能被推到远程仓库。
// （「GitHub 上出现归档文件」这句要人看，见 issue 文件的「怎么验收」。）
func TestBackPushesArchiveToRemote(t *testing.T) {
	needGit(t)
	hermetic(t)

	remote := newRemote(t)
	root := filepath.Join(tempDir(t), "归档")
	writeArchive(t, root, map[string]string{
		"claude-code/D--work-demo/2026-10-09-项目结构梳理.md": "# 项目结构梳理\n",
		"codex/C--a/2026-01-05-聊一聊.md":                  "# 聊一聊\n",
	})

	result := Method{}.Back(backup.Settings{Dir: root, Remote: remote, Branch: "main"})
	if result.Err != nil {
		t.Fatalf("Back: %v", result.Err)
	}
	if !result.Changed {
		t.Errorf("第一次应当报告有变化: %+v", result)
	}
	t.Logf("结果：%s", result.Detail)

	got := remoteFiles(t, remote, "main")
	want := []string{
		"claude-code/D--work-demo/2026-10-09-项目结构梳理.md",
		"codex/C--a/2026-01-05-聊一聊.md",
	}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("远程文件 = %v\n想要 %v", got, want)
	}
}

// 验收标准 3：工作区无变化时不产生空提交。
func TestNoChangeProducesNoCommit(t *testing.T) {
	needGit(t)
	hermetic(t)

	remote := newRemote(t)
	root := filepath.Join(tempDir(t), "归档")
	writeArchive(t, root, map[string]string{"a.md": "# 第一版\n"})
	settings := backup.Settings{Dir: root, Remote: remote, Branch: "main"}

	if result := (Method{}).Back(settings); result.Err != nil {
		t.Fatalf("第一次 Back: %v", result.Err)
	}
	first := commitCount(t, remote, "main")

	// 什么都不改，再跑一次。
	result := Method{}.Back(settings)
	if result.Err != nil {
		t.Fatalf("第二次 Back: %v", result.Err)
	}
	if result.Changed {
		t.Errorf("没有变化时不该报告有变化: %+v", result)
	}
	if !strings.Contains(result.Detail, "没有变化") {
		t.Errorf("说明应当讲清为什么没做事: %q", result.Detail)
	}
	if got := commitCount(t, remote, "main"); got != first {
		t.Errorf("提交数从 %d 变成了 %d——产生了空提交", first, got)
	}
	t.Logf("两次跑完，提交数仍是 %d：%s", first, result.Detail)
}

// 工作区没有变化，但本地有未推送的提交：这一轮要补推（I-052）。
//
// 场景就是「上一轮推送失败」：提交已经落在本地，远程没有；下一轮没有新文件，
// 不能因为「没有变化」就把那个提交永远留在本地。
func TestNoChangeStillPushesUnpushedCommit(t *testing.T) {
	needGit(t)
	hermetic(t)

	remote := newRemote(t)
	root := filepath.Join(tempDir(t), "归档")
	writeArchive(t, root, map[string]string{"a.md": "# 第一版\n"})
	settings := backup.Settings{Dir: root, Remote: remote, Branch: "main"}

	if result := (Method{}).Back(settings); result.Err != nil {
		t.Fatalf("第一次 Back: %v", result.Err)
	}
	first := commitCount(t, remote, "main")

	// 模拟上一轮推送失败：本地多一个提交，远程没有。
	cmd := exec.Command("git", "-C", root, "commit", "--allow-empty", "-m", "上一轮没推上去")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("造本地未推送提交失败: %v\n%s", err, out)
	}

	result := Method{}.Back(settings)
	if result.Err != nil {
		t.Fatalf("第二次 Back: %v", result.Err)
	}
	if !strings.Contains(result.Detail, "补推") {
		t.Errorf("说明要讲清这次补推了: %q", result.Detail)
	}
	if got := commitCount(t, remote, "main"); got != first+1 {
		t.Errorf("远程提交数 = %d，想要 %d——未推送的提交没补上", got, first+1)
	}
	t.Logf("结果：%s", result.Detail)
}

func TestChangeProducesNewCommit(t *testing.T) {
	needGit(t)
	hermetic(t)

	remote := newRemote(t)
	root := filepath.Join(tempDir(t), "归档")
	writeArchive(t, root, map[string]string{"a.md": "# 第一版\n"})
	settings := backup.Settings{Dir: root, Remote: remote, Branch: "main"}

	if result := (Method{}).Back(settings); result.Err != nil {
		t.Fatalf("第一次 Back: %v", result.Err)
	}
	first := commitCount(t, remote, "main")

	writeArchive(t, root, map[string]string{"a.md": "# 第二版\n"})
	result := Method{}.Back(settings)
	if result.Err != nil {
		t.Fatalf("第二次 Back: %v", result.Err)
	}
	if !result.Changed {
		t.Errorf("改了内容应当报告有变化: %+v", result)
	}
	if got := commitCount(t, remote, "main"); got != first+1 {
		t.Errorf("提交数 = %d，想要 %d", got, first+1)
	}
	t.Logf("结果：%s", result.Detail)
}

// 验收标准 4：推送失败时归档目录内容完整。
//
// 「日志含失败原因、退出码非 0」由 I-010 的编排与 I-011 的入口落地；
// 这一层要保证的是：错误带着 git 的原话往上走，且归档文件一个都不少。
func TestPushFailureKeepsArchiveIntact(t *testing.T) {
	needGit(t)
	hermetic(t)

	root := filepath.Join(tempDir(t), "归档")
	writeArchive(t, root, map[string]string{
		"a.md":     "# 第一版\n",
		"子目录/b.md": "# 另一个\n",
	})
	badRemote := filepath.Join(tempDir(t), "没有这个仓库.git")

	result := Method{}.Back(backup.Settings{Dir: root, Remote: badRemote, Branch: "main"})
	if result.Err == nil {
		t.Fatal("推到一个不存在的远程应当失败")
	}
	t.Logf("失败信息：%v", result.Err)
	if !strings.Contains(result.Err.Error(), "push") {
		t.Errorf("错误应当说明是 push 那一步: %v", result.Err)
	}
	if !strings.Contains(result.Err.Error(), badRemote) {
		t.Errorf("错误应当带上 git 的原话: %v", result.Err)
	}
	if !result.Changed {
		t.Error("本地提交已经做出来了，应当如实报告有变化")
	}

	// 归档文件一个都不少、内容没变。
	for name, want := range map[string]string{"a.md": "# 第一版\n", "子目录/b.md": "# 另一个\n"} {
		raw, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(name)))
		if err != nil {
			t.Errorf("归档文件没了: %s: %v", name, err)
			continue
		}
		if string(raw) != want {
			t.Errorf("归档文件被改了: %s = %q", name, string(raw))
		}
	}

	// 本地提交留住了——推不出去也不该把东西丢掉。
	cmd := exec.Command("git", "-C", root, "log", "--oneline")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("读本地历史失败: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "收集于") {
		t.Errorf("本地提交应当留住，实际历史：%s", string(out))
	}
}

// 验收标准 5：git 调用设了超时。
func TestCommandsRunUnderTimeout(t *testing.T) {
	needGit(t)
	hermetic(t)

	root := filepath.Join(tempDir(t), "归档")
	writeArchive(t, root, map[string]string{"a.md": "# x\n"})

	result := Method{}.Back(backup.Settings{
		Dir:     root,
		Remote:  newRemote(t),
		Branch:  "main",
		Timeout: time.Nanosecond, // 立刻过期：只要命令挂在 ctx 上，就一定跑不完
	})

	if result.Err == nil {
		t.Fatal("超时为零的命令应当失败")
	}
	t.Logf("失败信息：%v", result.Err)
	if !errors.Is(result.Err, context.DeadlineExceeded) {
		t.Errorf("错误应当能认出是超时: %v", result.Err)
	}
}

// AC-02：没配远程仓库地址时，跳过备份，不发起网络请求，也不偷偷建仓库。
func TestSkipsWhenRemoteNotConfigured(t *testing.T) {
	root := filepath.Join(tempDir(t), "归档")
	writeArchive(t, root, map[string]string{"a.md": "# x\n"})

	result := Method{}.Back(backup.Settings{Dir: root})
	if result.Err != nil {
		t.Errorf("没配远程不算失败: %v", result.Err)
	}
	if result.Changed {
		t.Error("没配远程时不该有变化")
	}
	if !strings.Contains(result.Detail, "没有配置远程仓库") {
		t.Errorf("说明 = %q", result.Detail)
	}
	if _, err := os.Stat(filepath.Join(root, ".git")); err == nil {
		t.Error("没配远程时不该在归档目录里建仓库")
	}
}

func TestSkipsWhenArchiveDirMissing(t *testing.T) {
	result := Method{}.Back(backup.Settings{
		Dir:    filepath.Join(tempDir(t), "还没有"),
		Remote: "git@example.com:x.git",
	})
	if result.Err != nil {
		t.Errorf("归档目录还没有不算失败: %v", result.Err)
	}
	if result.Changed {
		t.Error("什么都没做，不该报告有变化")
	}
	if !strings.Contains(result.Detail, "还不存在") {
		t.Errorf("说明 = %q", result.Detail)
	}
}

func TestRejectsBadSettings(t *testing.T) {
	file := filepath.Join(tempDir(t), "不是目录")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	cases := map[string]backup.Settings{
		"没给归档目录":  {Remote: "git@example.com:x.git"},
		"归档目录是文件": {Dir: file, Remote: "git@example.com:x.git"},
	}
	for name, settings := range cases {
		result := Method{}.Back(settings)
		if result.Err == nil {
			t.Errorf("%s：应当报错", name)
		}
	}
}

func TestRespectsConfiguredBranch(t *testing.T) {
	needGit(t)
	hermetic(t)

	remote := newRemote(t)
	root := filepath.Join(tempDir(t), "归档")
	writeArchive(t, root, map[string]string{"a.md": "# x\n"})

	result := Method{}.Back(backup.Settings{Dir: root, Remote: remote, Branch: "归档分支"})
	if result.Err != nil {
		t.Fatalf("Back: %v", result.Err)
	}
	if files := remoteFiles(t, remote, "归档分支"); len(files) != 1 || files[0] != "a.md" {
		t.Errorf("配的分支上应当有那个文件，实际 %v", files)
	}
}

func TestFollowsConfiguredRemoteWhenItChanges(t *testing.T) {
	needGit(t)
	hermetic(t)

	first, second := newRemote(t), newRemote(t)
	root := filepath.Join(tempDir(t), "归档")
	writeArchive(t, root, map[string]string{"a.md": "# 第一版\n"})

	if result := (Method{}).Back(backup.Settings{Dir: root, Remote: first, Branch: "main"}); result.Err != nil {
		t.Fatalf("第一次 Back: %v", result.Err)
	}

	writeArchive(t, root, map[string]string{"a.md": "# 第二版\n"})
	if result := (Method{}).Back(backup.Settings{Dir: root, Remote: second, Branch: "main"}); result.Err != nil {
		t.Fatalf("第二次 Back: %v", result.Err)
	}

	if files := remoteFiles(t, first, "main"); len(files) != 1 {
		t.Errorf("第一个远程应当还是 1 个文件，实际 %v", files)
	}
	if files := remoteFiles(t, second, "main"); len(files) != 1 || files[0] != "a.md" {
		t.Errorf("换了地址之后应当推到第二个远程，实际 %v", files)
	}
}

func TestIdentityMatchesConfig(t *testing.T) {
	// ID 必须与配置里的 backup.mode 取值一致，否则选「Git 仓库」会找不到实现。
	if ID != "git" {
		t.Errorf("ID = %q，想要 %q", ID, "git")
	}
	if Name == "" {
		t.Error("缺少显示名")
	}
}
