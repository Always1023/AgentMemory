package channel

import (
	"path/filepath"
	"strings"
)

// TitleLimit 是会话标题的最大字符数。
const TitleLimit = 60

// TitleFrom 依次尝试：第一条候选文本 → 文件名 → 会话 ID。
//
// 候选文本由适配器给（通常是前几条用户发言）。全都不合用时也不留空标题——
// 归档文件名要用它，空标题会让所有会话挤成一个名字。
func TitleFrom(candidates []string, sourcePath, sessionID string) string {
	for _, c := range candidates {
		if t := Shorten(c, TitleLimit); t != "" {
			return t
		}
	}
	if sourcePath != "" {
		base := strings.TrimSuffix(filepath.Base(sourcePath), filepath.Ext(sourcePath))
		if base != "" && base != "." && base != string(filepath.Separator) {
			return base
		}
	}
	return sessionID
}

// Shorten 取第一行、去首尾空白、限长。
//
// 看起来像标记的（以 < 开头，如 <environment_context>、<local-command-caveat>）
// 不算标题：那类文本是渠道自己塞的元信息，不是用户说的话。
func Shorten(text string, limit int) string {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" || strings.HasPrefix(trimmed, "<") {
		return ""
	}
	if idx := strings.IndexByte(trimmed, '\n'); idx >= 0 {
		trimmed = strings.TrimSpace(trimmed[:idx])
	}
	runes := []rune(trimmed)
	if len(runes) > limit {
		return strings.TrimSpace(string(runes[:limit])) + "…"
	}
	return trimmed
}
