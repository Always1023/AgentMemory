// Package archive 把渲染好的 Markdown 落到归档目录里。
//
// 路径形态：`<归档目录>/<渠道>/<项目>/<日期>-<标题>.md`
//
// 两条硬规矩：
//  1. 路径是**算出来的**，同一会话永远落在同一个路径上——所以重复收集是「更新」而不是「新增」；
//  2. 只往归档目录里写，不读渠道数据、不碰 git（docs/ARCHITECTURE.md 模块表）。
//
// 依赖方向：只依赖共享的会话模型类型。
package archive

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/Always1023/AgentMemory/internal/channel"
)

// markdownExt 是归档文件的扩展名。
const markdownExt = ".md"

const (
	// dateLayout 是文件名里的日期形式。
	dateLayout = "2006-01-02"
	// titleLimit 是文件名里标题段的字符数上限。
	// 与 channel.TitleLimit 无关：即便调用方给了超长标题，这里也要兜住。
	titleLimit = 60
	// dirLimit 是目录名段的字符数上限。
	dirLimit = 80

	// noDate 是会话没有开始时间时的日期占位。
	// 用全零而不是今天：它排在前面、一眼看得出是缺的，也不会因为「今天」而天天改名。
	noDate = "0000-00-00"
	// noProject 是会话没有项目路径时的目录占位。
	noProject = "未标注项目"
	// noChannel 是渠道标识与显示名都空时的目录占位。
	noChannel = "未标注渠道"
	// untitled 是标题清理后为空时的占位。
	untitled = "未命名会话"
	// identityLength 是文件名里会话标识哈希取几位。
	// 10 位十六进制＝40 位：万级会话下撞哈希的概率可以忽略。
	identityLength = 10

	// tempSuffix 是写入过程中的临时文件后缀。
	tempSuffix = ".tmp"

	// webDirName 是网页版捕获插件在归档根下写的那一层目录名（I-078）。
	// 数份数时靠它区分来源，见 ARCHITECTURE-WEB §8。
	webDirName = "网页版"
)

// illegalInName 是文件名里一律不能出现的字符：Windows 那九个，
// 外加路径分隔符（在 POSIX 上只有 `/` 非法，但两份数据是同一份代码处理的）。
const illegalInName = `\/:*?"<>|`

// Plan 算出这个会话的归档路径。
//
// 路径形态：`<归档目录>/<渠道>/<项目>/<日期>-<标题>-<会话标识>.md`。
//
// 纯函数：同一会话永远得到同一个路径。这是「同一会话重复归档不产生第二份文件」的依据——
// 不靠记住上次写到哪，而是靠路径可复现。
//
// 末尾那截身份是必需的：光靠「日期 + 标题」定路径，不同会话会撞车。真实数据上
// 出现过——同一天、标题都叫「阅读当前看板」的两个会话，后者把前者的档案整个覆盖，
// 281 份归档只落下 165 个文件（见 I-018）。
//
// channelName 只在渠道标识为空时当兜底。
func Plan(root string, s channel.Session, channelName string) string {
	channelSeg := channelSegment(s.Channel, channelName)

	date := noDate
	if !s.StartedAt.IsZero() {
		date = s.StartedAt.Format(dateLayout)
	}

	fileName := date + "-" + titleSegment(s.Title) + "-" + identity(s) + ".md"
	return filepath.Join(root, channelSeg, projectDir(s.ProjectPath), fileName)
}

// channelSegment 算归档目录下渠道那一层的目录名。
//
// Plan 与 ChannelDir 共用它：渠道目录的取名规则只写一处，数出来的目录和写下去的
// 目录才不会错位（I-023 就是按渠道目录数的）。
func channelSegment(channelID, channelName string) string {
	seg := segment(channelID, dirLimit, false)
	if seg == "" {
		seg = segment(channelName, dirLimit, false)
	}
	if seg == "" {
		seg = noChannel
	}
	return seg
}

// ChannelDir 给出某个渠道在归档目录里的那一层目录。
//
// 数数的人（界面层）用它，不用自己去猜目录名怎么拼——归档目录的布局是这里定的
// （`ARCHITECTURE.md` 模块表：别人不直接读它的存储）。
func ChannelDir(root, channelID, channelName string) string {
	return filepath.Join(root, channelSegment(channelID, channelName))
}

// Stats 是数出来的归档数字（I-023、I-078）。
type Stats struct {
	// Total 是这里一共有多少份 Markdown，等于 Desktop + Web。
	Total int
	// Today 是落盘日期落在「今天」的份数。
	Today int
	// Desktop 是其中桌面端收进来的份数：相对归档根的第一段不是「网页版」。
	Desktop int
	// Web 是其中网页版捕获插件写进来的份数：相对归档根的第一段是「网页版」。
	Web int
}

// Count 数一个归档目录里的 Markdown：共几份、今天落了几份、其中多少份来自桌面端、多少份来自网页版。
//
// 「今天」按文件的落盘日期（修改时间）算，按本地时区取自然日——归档文件名里的
// 日期段是**会话当天**，跟「今天收进来的」不是一回事，不能拿它当今日数。
//
// 来源按目录分层判断，不读文件内容：桌面端写 `<归档目录>/<渠道>/<项目>/…`，
// 网页版插件写 `<归档目录>/网页版/<站点>/…`，两条产品线只共享归档根。
//
// 只读：不写、不删、不动游标。目录不存在时返回零值、不报错——还没收过就是这样。
func Count(root string, today time.Time) (Stats, error) {
	var stats Stats
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(d.Name(), markdownExt) {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		stats.Total++
		if sameDay(info.ModTime(), today) {
			stats.Today++
		}
		if fromWebCapture(root, path) {
			stats.Web++
		} else {
			stats.Desktop++
		}
		return nil
	})
	if errors.Is(err, fs.ErrNotExist) {
		return Stats{}, nil
	}
	if err != nil {
		return Stats{}, err
	}
	return stats, nil
}

// fromWebCapture 报这份归档是不是网页版捕获插件写进来的。
//
// 判据只有一条：相对归档根的第一段目录名是不是「网页版」。这也意味着，
// 人若把网页版那层目录改名或搬家，这些文件会重新算成桌面端——可接受：
// 数数是为了让人看明白来源，不是给文件发身份证。
func fromWebCapture(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return false
	}
	first, _, _ := strings.Cut(rel, string(filepath.Separator))
	return first == webDirName
}

// sameDay 报两个时刻在本地时区下是不是同一个自然日。
func sameDay(a, b time.Time) bool {
	ay, am, ad := a.In(time.Local).Date()
	by, bm, bd := b.In(time.Local).Date()
	return ay == by && am == bm && ad == bd
}

// Write 把渲染好的 Markdown 落到归档路径上，返回实际写入的路径。
//
// 覆盖写，不是追加：同一会话重复收集时整份更新。
// 先写同目录的临时文件再改名替换——中途崩了只会留下一个临时文件，
// 不会把一份好档案改成半截。
func Write(root string, s channel.Session, channelName, markdown string) (string, error) {
	path := Plan(root, s, channelName)

	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("建归档目录 %s: %w", dir, err)
	}

	tmp := path + tempSuffix
	if err := os.WriteFile(tmp, []byte(markdown), 0o600); err != nil {
		return "", fmt.Errorf("写归档临时文件 %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return "", fmt.Errorf("替换归档文件 %s: %w", path, err)
	}
	return path, nil
}

// projectDir 把项目路径变成一个目录名。
//
// 项目路径是完整路径（`D:\work\demo`），直接当目录名不行：里面全是分隔符与盘符冒号。
// 把它们统一换成 `-`，保留可读性：`D:\work\demo` → `D--work-demo`。
//
// 已知局限：盘符冒号与路径分隔符都换成 `-`，而字面连接号原样保留，所以
// `C:\a-b` 与 `C:\a\b` 会映射到同一个目录名。要互不撞名得给字面连接号转义
// （`-` → `--`），代价是所有带连接号的项目名都变丑（`000-vibeCoding规范` →
// `000--vibeCoding规范`）。这两种路径同时出现在一台机器上的概率极低，
// 且归档只是给人看的镜像（「哪个源文件对应哪份档案」以游标为准），
// 所以先按好看优先；真遇上了再给连接号转义。
//
// 连续的分隔符**不合并**，`D:\a\b` 不会跟 `D:\a\b` 以外的东西混在一起。
func projectDir(projectPath string) string {
	if strings.TrimSpace(projectPath) == "" {
		return noProject
	}
	if dir := segment(projectPath, dirLimit, false); dir != "" {
		return dir
	}
	return noProject
}

// titleSegment 把标题变成文件名里的那一段。
//
// 与目录名相反，标题里连续的非法字符合并成一个 `-`——标题是自由文本，
// 好看比精确重要，而且它前面已经顶着日期，不会跟别的会话重名。
func titleSegment(title string) string {
	if name := segment(title, titleLimit, true); name != "" {
		return name
	}
	return untitled
}

// identity 取会话身份在文件名里的那一截：`来源文件路径 + 会话标识` 的短哈希。
//
// 为什么哈希，而不是截会话标识前几位：标识的格式不由我们定。真实数据上踩过两次——
//
//	· Codex 的标识是 UUIDv7，**前若干位是毫秒时间戳**，同一分钟左右开的会话前缀完全一样；
//	  按前缀取，它们照旧撞在一起（实测 283 份归档仍有 24 处撞名）。
//	· Codex 的「续写」会把**同一个会话 ID 写进多个 rollout 文件**（实测一个会话横跨
//	  五个文件，每个文件是会话的一段）。只按会话 ID 定名，后一段会把前一段整个盖掉。
//
// 为什么把来源文件路径也掺进去：`ARCHITECTURE.md` 的数据模型写的是
// **一条游标 ↔ 一个源文件 ↔ 一个归档路径**。游标就是以源文件为单位的，
// 身份跟它对齐，才能保证「每个源文件都有自己的档案、一份都不少」。
// 代价：换机器（家目录变了）会算出不同的名字；归档本来就是本机镜像，能接受。
//
// 两者都取不到时（解析器没填来源文件），退回渠道 + 开始时间，仍然保证同一会话稳定。
//
// 完整会话 ID 不丢：它写在档案头部的「会话」一行里，也可以从增量游标反查。
func identity(s channel.Session) string {
	seed := s.SourceFile + "\x00" + s.ID
	if strings.TrimSpace(s.SourceFile) == "" && strings.TrimSpace(s.ID) == "" {
		seed = s.Channel + "\x00" + s.StartedAt.UTC().Format(time.RFC3339Nano)
	}
	sum := sha256.Sum256([]byte(seed))
	return hex.EncodeToString(sum[:])[:identityLength]
}

// segment 把一段文本变成合法的文件名 / 目录名段。
//
// collapse 为真时，连续的 `-` 合并成一个。
func segment(s string, limit int, collapse bool) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r < 0x20 || r == 0x7f || r == '\uFFFD':
			// 控制字符与替换符：换成连接号，别留在文件名里。
			b.WriteRune('-')
		case strings.ContainsRune(illegalInName, r):
			b.WriteRune('-')
		default:
			b.WriteRune(r)
		}
	}

	out := b.String()
	if collapse {
		out = tidy(out)
	}
	out = trimFileName(out)
	if out == "" {
		return ""
	}

	if runes := []rune(out); len(runes) > limit {
		// 按字符数截断，不是按字节——中文一个字是三个字节。
		out = trimFileName(string(runes[:limit]))
	}
	if out == "" {
		return ""
	}
	if isReserved(out) {
		// Windows 的保留名（CON、NUL、COM1…）不能当文件名，前面垫一个下划线。
		out = "_" + out
	}
	return out
}

// trimFileName 去掉文件名段首尾不能留的东西：空格、点、连接号。
//
// Windows 会把尾部的空格与点悄悄吃掉，于是「写的时候叫 A 」。读的时候叫 A」——
// 前后不一致。提前剪掉，名字就一直是它自己。
func trimFileName(s string) string {
	return strings.Trim(s, " .-")
}

// tidy 收拾文件名段里的空白与连接号。
//
// 连续空白压成一个空格、连续连接号压成一个，紧挨着的连接号与空格只留连接号：
// `问题?? 求解答` 先被换成 `问题-- 求解答`，收拾完是 `问题-求解答`。
// 普通标题（`Next.js Dashboard Structure`）原样不动。
func tidy(s string) string {
	for prev := ""; s != prev; {
		prev = s
		s = strings.Join(strings.Fields(s), " ")
		for strings.Contains(s, "--") {
			s = strings.ReplaceAll(s, "--", "-")
		}
		s = strings.ReplaceAll(s, " -", "-")
		s = strings.ReplaceAll(s, "- ", "-")
	}
	return s
}

// isReserved 判断 Windows 的保留设备名。
func isReserved(name string) bool {
	upper := strings.ToUpper(strings.TrimSuffix(name, filepath.Ext(name)))

	switch upper {
	case "CON", "PRN", "AUX", "NUL":
		return true
	}
	for _, prefix := range []string{"COM", "LPT"} {
		if len(upper) == len(prefix)+1 && strings.HasPrefix(upper, prefix) &&
			upper[len(prefix)] >= '1' && upper[len(prefix)] <= '9' {
			return true
		}
	}
	return false
}

// MigrateReport 是「把已有归档复制到新目录」的结果。
type MigrateReport struct {
	// OldTotal 是旧目录里符合归档形态的份数。
	OldTotal int
	// Copied 是新复制过去的份数。
	Copied int
	// Skipped 是目标目录里已有同样内容、跳过去的份数。
	Skipped int
	// Conflicts 是目标目录里已有同名、但内容不一样的（相对路径）。
	// 有冲突时**整趟不复制**，把这些列出来等人定。
	Conflicts []string
}

// archiveFileName 认「我们写下的归档文件」：<日期>-<标题>-<身份>.md。
//
// 只认这个形态（F10 第六条）：别的文件——`.tmp` 半截文件、`.txt` 笔记、
// 人自己放进去的东西——一个都不碰。
var archiveFileName = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}-.+-[0-9a-f]{` +
	strconv.Itoa(identityLength) + `}\.md$`)

// IsArchiveFile 报这个名字是不是我们写下的归档文件。
func IsArchiveFile(name string) bool {
	return archiveFileName.MatchString(name)
}

// Migrate 把旧归档目录里的归档**复制**到新目录。
//
// 规矩（F10，人定的最保守那一套）：
//   - **只复制，不搬**：旧目录一份不动、一份不删；
//   - 只认 `IsArchiveFile` 那个形态，别的文件一个不碰；
//   - 目标已有同名：内容一样就跳过；**不一样就整趟不复制**，列出来等人定（绝不覆盖）；
//   - 幂等：跑第二遍全部走「跳过」；
//   - 复制时**保留落盘时间**——不然「今日保存」会把历史归档算成今天收的。
//
// 旧目录不存在（或为空）时返回零值，不是错误。
func Migrate(oldRoot, newRoot string) (MigrateReport, error) {
	var report MigrateReport
	if oldRoot == "" || newRoot == "" || oldRoot == newRoot {
		return report, nil
	}

	var rels []string
	err := filepath.WalkDir(oldRoot, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !IsArchiveFile(d.Name()) {
			return nil
		}
		rel, err := filepath.Rel(oldRoot, path)
		if err != nil {
			return err
		}
		rels = append(rels, rel)
		return nil
	})
	if errors.Is(err, fs.ErrNotExist) {
		return report, nil
	}
	if err != nil {
		return report, err
	}
	report.OldTotal = len(rels)
	if len(rels) == 0 {
		return report, nil
	}

	// 先看一遍目标目录：只要有同名但内容不同的，整趟不动。
	for _, rel := range rels {
		same, exists, err := sameContent(filepath.Join(oldRoot, rel), filepath.Join(newRoot, rel))
		if err != nil {
			return report, err
		}
		if exists && !same {
			report.Conflicts = append(report.Conflicts, rel)
		}
	}
	if len(report.Conflicts) > 0 {
		return report, nil
	}

	for _, rel := range rels {
		src := filepath.Join(oldRoot, rel)
		dst := filepath.Join(newRoot, rel)
		_, exists, err := sameContent(src, dst)
		if err != nil {
			return report, err
		}
		if exists {
			report.Skipped++
			continue
		}
		if err := copyKeepingTime(src, dst); err != nil {
			return report, err
		}
		report.Copied++
	}
	return report, nil
}

// sameContent 报目标文件在不在、内容与源是不是一样。
func sameContent(src, dst string) (same, exists bool, err error) {
	sinfo, err := os.Stat(src)
	if err != nil {
		return false, false, err
	}
	dinfo, err := os.Stat(dst)
	if errors.Is(err, fs.ErrNotExist) {
		return false, false, nil
	}
	if err != nil {
		return false, false, err
	}
	if sinfo.Size() != dinfo.Size() {
		return false, true, nil
	}
	a, err := os.ReadFile(src)
	if err != nil {
		return false, true, err
	}
	b, err := os.ReadFile(dst)
	if err != nil {
		return false, true, err
	}
	return bytes.Equal(a, b), true, nil
}

// copyKeepingTime 复制一份文件，并把落盘时间照旧带过去。
func copyKeepingTime(src, dst string) error {
	info, err := os.Stat(src)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	raw, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	if err := os.WriteFile(dst, raw, 0o644); err != nil {
		return err
	}
	return os.Chtimes(dst, info.ModTime(), info.ModTime())
}
