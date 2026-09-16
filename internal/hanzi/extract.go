// Package hanzi 从任意文本中抽取可学习的汉字。
//
// 数据源的格式无法预期——可能是一行一个字、一行一句话、整篇课文，
// 或者带标点、拼音、序号、英文的字表。因此这里不做格式假设，
// 只是扫描全文、挑出汉字、按首次出现顺序去重。
package hanzi

import (
	"bufio"
	"io"
	"sort"
	"strings"
	"unicode"
)

// ExtractResult 是一次抽取的结果。
type ExtractResult struct {
	// Ordered 是去重后的汉字，保持首次出现的顺序。
	Ordered []rune
	// Counts 是每个字的出现次数。
	Counts map[rune]int
	// TotalRunes 是输入的总字符数。
	TotalRunes int
	// Skipped 按类别统计被跳过的字符数。
	Skipped map[string]int
	// Truncated 表示是否因为超出上限而截断。
	Truncated bool
}

// MaxUniqueHanzi 是单次抽取的汉字数量上限，防止粘贴整本小说导致内存暴涨。
const MaxUniqueHanzi = 5000

// 跳过的字符类别名。
const (
	SkipPunct  = "punct"  // 标点、空格、换行
	SkipLatin  = "latin"  // 拉丁字母
	SkipDigit  = "digit"  // 数字
	SkipSymbol = "symbol" // 符号、部首、注音符号等
	SkipOther  = "other"  // 其他非常规汉字
)

// IsLearnable 判断一个码点是否是可学习的规范汉字。
//
// 这里刻意不使用 unicode.Is(unicode.Han, r)：Go 的 unicode.Han 表
// 除了汉字还包含
//
//   - 2E80–2EF3 部首补充、2F00–2FD5 康熙部首（⼀⼁⼂）——会被当单字导入
//   - 3005–3007 即 々 〆 〇——〇 在印刷品里极常见，会被当汉字导入
//   - 3021–3029 注音符号、3038–303B
//   - F900–FAD9 兼容汉字——樂 与 樂 是两个码点却是同一个字，会产生重复项
//
// 手工划定区间可以同时避免「误收符号」和「兼容区重复」两个问题。
func IsLearnable(r rune) bool {
	switch {
	case r >= 0x3400 && r <= 0x4DBF: // CJK 扩展 A
		return true
	case r >= 0x4E00 && r <= 0x9FFF: // CJK 基本区（URO）
		return true
	case r >= 0x20000 && r <= 0x2EBE0: // 扩展 B–F
		return true
	case r >= 0x30000 && r <= 0x323AF: // 扩展 G–H
		return true
	default:
		return false
	}
}

// Extract 从文本中抽取汉字。
//
// 返回的 Ordered 已按首次出现顺序去重；Counts 保留每个字的出现次数，
// 可用于「按出现频率排序」之类的后续处理。
func Extract(text string) ExtractResult {
	return ExtractReader(strings.NewReader(text))
}

// ExtractReader 是 Extract 的流式版本，适合处理大文件而不必一次性读进内存。
func ExtractReader(r io.Reader) ExtractResult {
	res := ExtractResult{
		Counts:  make(map[rune]int),
		Skipped: make(map[string]int),
	}
	seen := make(map[rune]struct{})

	br := bufio.NewReaderSize(r, 64*1024)
	for {
		ch, _, err := br.ReadRune()
		if err == io.EOF {
			break
		}
		if err != nil {
			break
		}

		res.TotalRunes++

		if !IsLearnable(ch) {
			res.Skipped[classify(ch)]++
			continue
		}

		res.Counts[ch]++
		if _, dup := seen[ch]; dup {
			continue
		}
		if len(res.Ordered) >= MaxUniqueHanzi {
			res.Truncated = true
			continue
		}
		seen[ch] = struct{}{}
		res.Ordered = append(res.Ordered, ch)
	}
	return res
}

// classify 把被跳过的字符归类，用于在预览页告诉用户「跳过了什么」。
func classify(r rune) string {
	switch {
	case unicode.IsSpace(r):
		return SkipPunct
	case unicode.Is(unicode.Pi, r), unicode.Is(unicode.Pf, r),
		unicode.Is(unicode.Ps, r), unicode.Is(unicode.Pe, r),
		unicode.Is(unicode.Po, r):
		return SkipPunct
	case unicode.Is(unicode.Latin, r):
		return SkipLatin
	case unicode.IsDigit(r):
		return SkipDigit
	case isHanLike(r):
		// 是「像汉字的符号」但不可学习：部首、〇、々、兼容汉字等。
		return SkipSymbol
	default:
		return SkipOther
	}
}

// isHanLike 判断是否是 CJK 相关但不可独立学习的码点，
// 这些单独归类为「符号」而非「其他」，让预览页的提示更有意义。
func isHanLike(r rune) bool {
	switch {
	case r >= 0x2E80 && r <= 0x2EF3: // 部首补充
		return true
	case r >= 0x2F00 && r <= 0x2FDF: // 康熙部首
		return true
	case r >= 0x3005 && r <= 0x3007: // 々 〆 〇
		return true
	case r >= 0x3021 && r <= 0x3029: // 注音符号
		return true
	case r >= 0x3038 && r <= 0x303B:
		return true
	case r >= 0xF900 && r <= 0xFAD9: // 兼容汉字
		return true
	default:
		return false
	}
}

// String 把抽取结果的有序字表还原成字符串，便于写库和预览。
func (e ExtractResult) String() string {
	return string(e.Ordered)
}

// SortByFrequency 返回按出现次数降序排列的字表，次数相同则保持原顺序。
// 用于将来做「先学高频字」的选项。
func (e ExtractResult) SortByFrequency() []rune {
	out := make([]rune, len(e.Ordered))
	copy(out, e.Ordered)

	// SliceStable 保证次数相同时维持首次出现的顺序。
	sort.SliceStable(out, func(i, j int) bool {
		return e.Counts[out[i]] > e.Counts[out[j]]
	})
	return out
}
