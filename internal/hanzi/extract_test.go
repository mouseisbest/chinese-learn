package hanzi

import (
	"strings"
	"testing"
)

func TestExtract_Basic(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string // 期望的有序去重字表
	}{
		{"空串", "", ""},
		{"纯 ASCII", "hello world 123", ""},
		{"纯标点", "，。！？、；：", ""},
		{"单个汉字", "我", "我"},
		{"一行一个字", "我\n们\n是", "我们是"},
		{"一句话", "我们是中国人", "我们是中国人"},
		{"去重保序", "我我我们们是", "我们是"},
		{"带拼音的字表", "我 wǒ\n们 men\n是 shì", "我们是"},
		{"带序号", "1. 我\n2. 们\n3. 是", "我们是"},
		{"带标点的句子", "我爱北京，天安门。", "我爱北京天安门"},
		{"中英混排", "我爱 Go 语言", "我爱语言"},
		// 按首次出现顺序：秋 天 到 了 气 凉 树 叶 黄 一 片 子 从 上 落 下 来
		{"整篇课文", "秋天到了，天气凉了。树叶黄了，一片片叶子从树上落下来。",
			"秋天到了气凉树叶黄一片子从上落下来"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := Extract(c.in)
			if s := got.String(); s != c.want {
				t.Errorf("Extract(%q) = %q, want %q", c.in, s, c.want)
			}
		})
	}
}

// 这是本包最重要的测试：unicode.Is(unicode.Han) 会误收这些字符，
// 必须靠手工码点区间把它们挡在外面。
func TestExtract_RejectsHanLookalikes(t *testing.T) {
	cases := []struct {
		name string
		in   string
		desc string
	}{
		{"〇", "一二〇三四", "U+3007 IDEOGRAPHIC NUMBER ZERO，印刷品里极常见"},
		{"々", "人々", "U+3005 叠字符号"},
		{"〆", "〆切", "U+3006"},
		{"康熙部首-一", "⼀", "U+2F00 KANGXI RADICAL ONE"},
		{"康熙部首-丨", "⼁", "U+2F01 KANGXI RADICAL LINE"},
		{"部首补充", "⺀", "U+2E80 CJK RADICAL REPEAT"},
		{"注音符号", "ㄅㄆㄇ", "注音 ㄅㄆㄇ"},
		{"兼容汉字-樂", "樂", "U+F914 与 U+6A02 樂 是同一个字的两个码点"},
		{"兼容汉字-一", "來", "U+F92D"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := Extract(c.in)
			for _, r := range got.Ordered {
				if r == '〇' || r == '々' || r == '〆' {
					t.Errorf("Extract(%q) 不应包含 %q（%s）", c.in, r, c.desc)
				}
				if r >= 0x2E80 && r <= 0x2FDF {
					t.Errorf("Extract(%q) 不应包含部首 U+%04X（%s）", c.in, r, c.desc)
				}
				if r >= 0xF900 && r <= 0xFAD9 {
					t.Errorf("Extract(%q) 不应包含兼容汉字 U+%04X（%s）", c.in, r, c.desc)
				}
			}
		})
	}

	// 具体确认：〇 被归入符号类，且不出现在结果里。
	got := Extract("一二〇三四")
	if want := "一二三四"; got.String() != want {
		t.Errorf("含〇的文本 = %q, want %q", got.String(), want)
	}
	if got.Skipped[SkipSymbol] == 0 {
		t.Error("〇 应被归类为符号")
	}
}

// 兼容区汉字不应与基本区正字重复。
func TestExtract_NoCompatibilityDuplicates(t *testing.T) {
	// U+6A02 樂（基本区）与 U+F914 樂（兼容区）看起来是同一个字
	in := "樂樂"
	got := Extract(in)

	if len(got.Ordered) != 1 {
		t.Errorf("兼容区汉字应被排除，得到 %d 个字: %q", len(got.Ordered), got.String())
	}
	if got.String() != "樂" {
		t.Errorf("应只保留基本区码点，得到 %q", got.String())
	}
}

func TestIsLearnable(t *testing.T) {
	cases := []struct {
		r    rune
		want bool
		desc string
	}{
		{'我', true, "基本区"},
		{'一', true, "基本区"},
		{'龥', true, "U+9FA5 基本区上界"},
		{'㐀', true, "扩展 A 下界"},
		{'䶿', true, "扩展 A 上界"},
		{'\U00020000', true, "扩展 B 下界"},
		{'\U0002A6DF', true, "扩展 B 上界"},
		{'〇', false, "U+3007"},
		{'々', false, "U+3005"},
		{'⼀', false, "康熙部首"},
		{'⺀', false, "部首补充"},
		{'樂', false, "兼容汉字"},
		{'A', false, "拉丁字母"},
		{'1', false, "数字"},
		{'，', false, "标点"},
		{' ', false, "空格"},
		{'\t', false, "制表符"},
		{'😀', false, "emoji"},
		{'鿿', true, "基本区上界"},
		{'　', false, "全角空格"},
	}
	for _, c := range cases {
		if got := IsLearnable(c.r); got != c.want {
			t.Errorf("IsLearnable(%q U+%04X) = %v, want %v（%s）",
				c.r, c.r, got, c.want, c.desc)
		}
	}
}

func TestExtract_Counts(t *testing.T) {
	got := Extract("我我我们们是")

	want := map[rune]int{'我': 3, '们': 2, '是': 1}
	for r, n := range want {
		if got.Counts[r] != n {
			t.Errorf("%q 出现次数 = %d, want %d", r, got.Counts[r], n)
		}
	}
	if got.TotalRunes != 6 {
		t.Errorf("总字符数 = %d, want 6", got.TotalRunes)
	}
}

func TestExtract_SkippedClassification(t *testing.T) {
	got := Extract("我，。A1〇！")

	if got.Skipped[SkipPunct] != 3 { // ，。！
		t.Errorf("标点数 = %d, want 3", got.Skipped[SkipPunct])
	}
	if got.Skipped[SkipLatin] != 1 {
		t.Errorf("拉丁字母数 = %d, want 1", got.Skipped[SkipLatin])
	}
	if got.Skipped[SkipDigit] != 1 {
		t.Errorf("数字数 = %d, want 1", got.Skipped[SkipDigit])
	}
	if got.Skipped[SkipSymbol] != 1 { // 〇
		t.Errorf("符号数 = %d, want 1", got.Skipped[SkipSymbol])
	}
}

func TestExtract_SortByFrequency(t *testing.T) {
	got := Extract("一一一二二三个") // 一:3 二:2 三:1 个:1
	want := []rune{'一', '二', '三', '个'}

	sorted := got.SortByFrequency()
	if string(sorted) != string(want) {
		t.Errorf("按频率排序 = %q, want %q", string(sorted), string(want))
	}

	// 原有序字表不应被修改（仍是首次出现顺序）。
	if got.String() != "一二三个" {
		t.Errorf("SortByFrequency 修改了原字表: %q", got.String())
	}
}

func TestExtract_Truncation(t *testing.T) {
	// 构造超过上限的各种汉字。用扩展 B 区码点，保证互不相同且都可学习。
	var sb strings.Builder
	n := MaxUniqueHanzi + 100
	for i := 0; i < n; i++ {
		sb.WriteRune(rune(0x20000 + i))
	}

	got := Extract(sb.String())

	if !got.Truncated {
		t.Error("超出上限时应设置 Truncated")
	}
	if len(got.Ordered) != MaxUniqueHanzi {
		t.Errorf("抽取数量 = %d, want %d", len(got.Ordered), MaxUniqueHanzi)
	}
}

func TestExtractReader(t *testing.T) {
	got := ExtractReader(strings.NewReader("我们是中国人"))
	if got.String() != "我们是中国人" {
		t.Errorf("ExtractReader = %q, want 我们是中国人", got.String())
	}
}

// 换行、制表符、全角空格等空白不应影响抽取。
func TestExtract_Whitespace(t *testing.T) {
	in := "我\t们\r\n是　中　国"
	got := Extract(in)
	if got.String() != "我们是中国" {
		t.Errorf("含各种空白 = %q, want 我们是中国", got.String())
	}
}
