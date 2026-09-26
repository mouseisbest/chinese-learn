package hanzi

import "testing"

func TestLookupPinyin_Basic(t *testing.T) {
	cases := []struct {
		ch   string
		want string
	}{
		{"天", "tiān"},
		{"地", "dì"},
		{"人", "rén"},
		{"水", "shuǐ"},
		{"耳", "ěr"},
		{"一", "yī"},
		{"我", "wǒ"},
	}
	for _, c := range cases {
		got := LookupPinyin(c.ch)
		if got.Primary != c.want {
			t.Errorf("LookupPinyin(%q).Primary = %q, want %q", c.ch, got.Primary, c.want)
		}
	}
}

// 声调符号必须正确——用默认风格会得到 zha3ng 这种中间插数字的格式。
func TestLookupPinyin_ToneMarks(t *testing.T) {
	got := LookupPinyin("天")
	if got.Primary != "tiān" {
		t.Errorf("「天」= %q, want tiān（声调符号应在韵母上）", got.Primary)
	}
	if got.Primary == "tia1n" || got.Primary == "tian1" {
		t.Error("使用了数字声调而不是符号声调")
	}
}

// 常用音要贴合小学教材，而不是拼音库的词频排序。
func TestLookupPinyin_PreferredPrimary(t *testing.T) {
	cases := []struct {
		ch   string
		want string
		why  string
	}{
		{"长", "cháng", "孩子先学「长短」，不是「长大」"},
		{"干", "gān", "先学「干净」"},
	}
	for _, c := range cases {
		got := LookupPinyin(c.ch)
		if got.Primary != c.want {
			t.Errorf("「%s」常用音 = %q, want %q（%s）", c.ch, got.Primary, c.want, c.why)
		}
	}
}

func TestLookupPinyin_Empty(t *testing.T) {
	for _, ch := range []string{"", "A", "1", "，"} {
		got := LookupPinyin(ch)
		if !got.IsEmpty() {
			t.Errorf("LookupPinyin(%q) 应为空, 得到 %+v", ch, got)
		}
	}
}

// 多音字：只标注小学阶段会遇到的读音，生僻音不能带出来。
func TestOtherReadings(t *testing.T) {
	cases := []struct {
		ch      string
		primary string
		all     string
		want    []string
		desc    string
	}{
		{"长", "cháng", "cháng zhǎng", []string{"zhǎng"}, "常见多音字"},
		{"行", "xíng", "xíng háng héng xìng hàng", []string{"háng"},
			"只保留 háng，丢掉 héng/xìng/hàng"},
		{"重", "zhòng", "zhòng chóng tóng", []string{"chóng"},
			"丢掉 tóng"},
		{"耳", "ěr", "ěr réng", nil, "非多音字，不标注"},
		{"火", "huǒ", "huǒ huō", nil, "非多音字，不标注"},
		{"七", "qī", "qī qí", nil, "非多音字，不标注"},
		{"天", "tiān", "tiān", nil, "单音字"},
		{"", "x", "x y", nil, "空字"},
	}
	for _, c := range cases {
		got := OtherReadings(c.ch, c.primary, c.all)
		if len(got) != len(c.want) {
			t.Errorf("%s: OtherReadings(%q) = %v, want %v（%s）",
				c.ch, c.ch, got, c.want, c.desc)
			continue
		}
		for i := range c.want {
			if got[i] != c.want[i] {
				t.Errorf("%s: OtherReadings(%q)[%d] = %q, want %q",
					c.ch, c.ch, i, got[i], c.want[i])
			}
		}
	}
}

// 重复读音要去掉：「〇」在库里会返回 ling ling。
func TestLookupPinyin_Dedupe(t *testing.T) {
	got := LookupPinyin("〇")
	if got.IsEmpty() {
		t.Skip("「〇」在这个库里没有拼音")
	}
	// All 里不应有重复
	seen := map[string]bool{}
	for _, r := range splitFields(got.All) {
		if seen[r] {
			t.Errorf("「〇」的读音有重复: %q", got.All)
		}
		seen[r] = true
	}
}

func splitFields(s string) []string {
	var out []string
	cur := ""
	for _, c := range s {
		if c == ' ' {
			if cur != "" {
				out = append(out, cur)
				cur = ""
			}
			continue
		}
		cur += string(c)
	}
	if cur != "" {
		out = append(out, cur)
	}
	return out
}
