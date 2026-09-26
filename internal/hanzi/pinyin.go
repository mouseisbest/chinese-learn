package hanzi

import (
	"strings"

	"github.com/mozillazg/go-pinyin"
)

// Pinyin 是一个字的读音信息。
type Pinyin struct {
	// Primary 是最常用的读音，带声调符号（如「长」→ zhǎng）。
	// 汉字没有拼音（生僻字、非汉字）时为空串。
	Primary string
	// All 是全部读音，按库给的顺序，用空格分隔（如「长」→ "zhǎng cháng"）。
	// 只有一个读音时与 Primary 相同。
	All string
}

// IsEmpty 判断是否没有拼音信息。
func (p Pinyin) IsEmpty() bool { return p.Primary == "" && p.All == "" }

// preferredPrimary 覆盖拼音库给的默认读音顺序。
//
// 拼音库按词频排读音，不完全符合小学教材的先后。
// 例如「长」库给 zhǎng 在前，但孩子先学的是「长短」的 cháng。
// 这里列出的字会把指定读音提到第一位。
var preferredPrimary = map[string]string{
	"长": "cháng",
	"干": "gān",
	"处": "chǔ",
	"只": "zhǐ",
	"种": "zhǒng",
	"发": "fā",
	"为": "wéi",
	"结": "jié",
	"兴": "xìng",
	"应": "yīng",
	"落": "luò",
	"着": "zhe",
	"朝": "cháo",
	"曲": "qū",
	"圈": "quān",
	"铺": "pū",
	"挑": "tiāo",
	"折": "zhé",
	"散": "sàn",
	"舍": "shě",
}

// LookupPinyin 查一个字的读音。
//
// 用 Tone 风格而不是默认风格：默认风格会把声调数字插在音节中间
// （「长」变成 zha3ng），那是给程序做匹配用的，不能给孩子看。
func LookupPinyin(ch string) Pinyin {
	if ch == "" {
		return Pinyin{}
	}

	toneArgs := pinyin.NewArgs()
	toneArgs.Style = pinyin.Tone
	// Heteronym 必须显式打开，否则只返回一个读音：
	// 「长」会只剩 zhǎng，丢掉 cháng。
	toneArgs.Heteronym = true

	all := pinyin.Pinyin(ch, toneArgs)
	if len(all) == 0 || len(all[0]) == 0 {
		return Pinyin{}
	}

	readings := dedupe(all[0])
	readings = promotePreferred(ch, readings)

	return Pinyin{
		Primary: readings[0],
		All:     strings.Join(readings, " "),
	}
}

// promotePreferred 把偏好的读音挪到第一位。
func promotePreferred(ch string, readings []string) []string {
	want, ok := preferredPrimary[ch]
	if !ok {
		return readings
	}
	idx := -1
	for i, r := range readings {
		if r == want {
			idx = i
			break
		}
	}
	if idx <= 0 {
		return readings // 没找到或已经在首位
	}
	out := make([]string, 0, len(readings))
	out = append(out, readings[idx])
	out = append(out, readings[:idx]...)
	out = append(out, readings[idx+1:]...)
	return out
}

// OtherReadings 返回一个字除常用音之外的其他读音，用于「小字标注」。
//
// 两层过滤，缺一不可：
//  1. 只对 commonHeteronyms 白名单里的字标注。拼音库把很多生僻音
//     也列出来了（「耳」带 réng、「火」带 huō、「七」带 qí），
//     那些不是孩子需要知道的，标出来只会造成困惑。
//  2. 即使在白名单里，也只保留 learningReadings 里列出的常用读音。
//     例如「行」库里有 xíng/háng/héng/xìng/hàng，只该标注 háng。
func OtherReadings(ch, primary, all string) []string {
	if ch == "" || all == "" || !commonHeteronyms[ch] {
		return nil
	}

	allowed := learningReadings[ch]
	if len(allowed) == 0 {
		return nil
	}

	// 按 all 的原始顺序输出，只保留白名单里的读音。
	var out []string
	for _, r := range strings.Fields(all) {
		if r == primary {
			continue
		}
		for _, ok := range allowed {
			if r == ok {
				out = append(out, r)
				break
			}
		}
	}
	return out
}

// learningReadings 列出每个多音字在小学阶段可能遇到的读音。
//
// 拼音库对多音字会给出全部历史读音，其中很多是生僻用法。
// 这张表只保留孩子课本上会碰到的那些，避免把「行」的
// héng/xìng/hàng 也标给孩子看。
//
// 值里包含常用音本身，OtherReadings 会跳过它只输出其余的。
var learningReadings = map[string][]string{
	"长": {"zhǎng", "cháng"},
	"行": {"xíng", "háng"},
	"重": {"zhòng", "chóng"},
	"乐": {"lè", "yuè"},
	"还": {"hái", "huán"},
	"都": {"dōu", "dū"},
	"为": {"wèi", "wéi"},
	"觉": {"jué", "jiào"},
	"教": {"jiāo", "jiào"},
	"数": {"shù", "shǔ"},
	"种": {"zhǒng", "zhòng"},
	"发": {"fā", "fà"},
	"干": {"gàn", "gān"},
	"当": {"dāng", "dàng"},
	"只": {"zhǐ", "zhī"},
	"处": {"chù", "chǔ"},
	"空": {"kōng", "kòng"},
	"间": {"jiān", "jiàn"},
	"看": {"kàn", "kān"},
	"没": {"méi", "mò"},
	"会": {"huì", "kuài"},
	"好": {"hǎo", "hào"},
	"少": {"shǎo", "shào"},
	"分": {"fēn", "fèn"},
	"中": {"zhōng", "zhòng"},
	"过": {"guò", "guo"},
	"结": {"jié", "jiē"},
	"角": {"jiǎo", "jué"},
	"背": {"bèi", "bēi"},
	"倒": {"dào", "dǎo"},
	"调": {"diào", "tiáo"},
	"斗": {"dòu", "dǒu"},
	"假": {"jiǎ", "jià"},
	"尽": {"jǐn", "jìn"},
	"卷": {"juǎn", "juàn"},
	"量": {"liàng", "liáng"},
	"曲": {"qǔ", "qū"},
	"圈": {"quān", "juàn"},
	"散": {"sàn", "sǎn"},
	"舍": {"shě", "shè"},
	"盛": {"shèng", "chéng"},
	"提": {"tí", "dī"},
	"挑": {"tiāo", "tiǎo"},
	"兴": {"xìng", "xīng"},
	"应": {"yīng", "yìng"},
	"朝": {"zhāo", "cháo"},
	"折": {"zhé", "shé"},
	"转": {"zhuǎn", "zhuàn"},
	"铺": {"pù", "pū"},
	"落": {"luò", "là"},
	"传": {"chuán", "zhuàn"},
	"着": {"zhe", "zháo", "zhuó"},
}

// commonHeteronyms 判断这个字是否属于要标注的多音字。
var commonHeteronyms = func() map[string]bool {
	m := make(map[string]bool, len(learningReadings))
	for ch := range learningReadings {
		m[ch] = true
	}
	return m
}()

// dedupe 去掉重复读音并保持顺序。
//
// 库里偶尔会出现同一个读音重复（如「〇」返回 ling ling），
// 直接展示给孩子会造成困惑。
func dedupe(in []string) []string {
	seen := make(map[string]struct{}, len(in))
	out := make([]string, 0, len(in))
	for _, s := range in {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		if _, dup := seen[s]; dup {
			continue
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	if len(out) == 0 {
		// 全部为空时退回原始值，避免调用方拿到空切片后越界。
		return in
	}
	return out
}
