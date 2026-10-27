package store

// FTS 索引的文本处理。FTS5 内置的 unicode61 分词器把一串连续汉字当成一个词，
// 搜「归档」就命中不了「归档目录」。本项目的解法：入库与查询前都过一遍
// ftsSegment——每个 CJK 字符自成一个词（与 unicode61 的单字切词等价），
// CJK 与拉丁字母之间补一个空格，避免「AI归档」被并成一个词。
// 更聪明的分词（jieba 一类）将来可以只换这一处。

import (
	"strings"
	"unicode"
)

// ftsSegment 把文本整理成 FTS5 友好的形态：CJK 单字分词。
// 建索引与构造查询必须走同一个函数，两边才对得上。
func ftsSegment(s string) string {
	var b strings.Builder
	b.Grow(len(s) + 16)
	for _, r := range s {
		if isCJK(r) {
			b.WriteByte(' ')
			b.WriteRune(r)
			b.WriteByte(' ')
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

// isCJK 报告一个字符是不是表意文字（汉字 / 假名 / 谚文）。
// 这些文字在 unicode61 下不做分词，需要我们手动切开。
func isCJK(r rune) bool {
	return unicode.Is(unicode.Han, r) ||
		unicode.Is(unicode.Hiragana, r) ||
		unicode.Is(unicode.Katakana, r) ||
		unicode.Is(unicode.Hangul, r)
}
