package channel

import (
	"strings"
	"testing"
)

func TestShortenRejectsMarkup(t *testing.T) {
	for _, markup := range []string{
		"<environment_context>\n  <cwd>D:\\x</cwd>",
		"<local-command-caveat>xx",
		"   ",
		"",
	} {
		if got := Shorten(markup, TitleLimit); got != "" {
			t.Errorf("Shorten(%q) = %q，标记类文本不该当标题", markup, got)
		}
	}
}

func TestShortenTakesFirstLineAndLimitsLength(t *testing.T) {
	if got := Shorten("  第一行\n第二行  ", TitleLimit); got != "第一行" {
		t.Errorf("应当取第一行: %q", got)
	}

	long := strings.Repeat("字", TitleLimit+10)
	got := Shorten(long, TitleLimit)
	if n := len([]rune(got)); n > TitleLimit+1 {
		t.Errorf("没截断，%d 个字符", n)
	}
	if !strings.HasSuffix(got, "…") {
		t.Errorf("截断后应当有省略号: %q", got)
	}
}

func TestTitleFromFallsBack(t *testing.T) {
	path := "/x/y/rollout-2025-12-10-abc.jsonl"

	if got := TitleFrom([]string{"<meta>", "真的第一句"}, path, "sid"); got != "真的第一句" {
		t.Errorf("应当跳过标记取第二句: %q", got)
	}
	if got := TitleFrom(nil, path, "sid"); got != "rollout-2025-12-10-abc" {
		t.Errorf("没有候选时应当退回文件名: %q", got)
	}
	if got := TitleFrom(nil, "", "sid"); got != "sid" {
		t.Errorf("文件名也空时应当退回会话 ID: %q", got)
	}
}
