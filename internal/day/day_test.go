package day

import (
	"testing"
	"time"
)

var shanghai = time.FixedZone("CST", 8*3600)

func TestFromTime_Cutoff(t *testing.T) {
	cases := []struct {
		name   string
		wall   string // 本地墙钟时间
		cutoff time.Duration
		want   Day
	}{
		{"下午 3 点算当天", "2026-09-16 15:00:00", DefaultCutoff, "2026-09-16"},
		{"晚上 23:30 仍算当天", "2026-09-16 23:30:00", DefaultCutoff, "2026-09-16"},
		{"23:59 仍算当天", "2026-09-16 23:59:59", DefaultCutoff, "2026-09-16"},
		{"凌晨 00:00 算前一天", "2026-09-17 00:00:00", DefaultCutoff, "2026-09-16"},
		{"凌晨 01:00 算前一天", "2026-09-17 01:00:00", DefaultCutoff, "2026-09-16"},
		{"03:59 算前一天", "2026-09-17 03:59:59", DefaultCutoff, "2026-09-16"},
		{"04:00 算当天", "2026-09-17 04:00:00", DefaultCutoff, "2026-09-17"},
		{"08:00 算当天", "2026-09-17 08:00:00", DefaultCutoff, "2026-09-17"},
		{"cutoff=0 时 00:00 即当天", "2026-09-17 00:00:00", 0, "2026-09-17"},
		{"cutoff=0 时 23:59 即当天", "2026-09-16 23:59:59", 0, "2026-09-16"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			wall, err := time.ParseInLocation("2006-01-02 15:04:05", c.wall, shanghai)
			if err != nil {
				t.Fatal(err)
			}
			if got := FromTime(wall, shanghai, c.cutoff); got != c.want {
				t.Errorf("FromTime(%s, cutoff=%v) = %s, want %s", c.wall, c.cutoff, got, c.want)
			}
		})
	}
}

// 时区换算必须正确：同一时刻在不同时区属于不同的学习日。
func TestFromTime_RespectsLocation(t *testing.T) {
	// 2026-09-16 22:00 UTC = 2026-09-17 06:00 上海
	wall := time.Date(2026, 9, 16, 22, 0, 0, 0, time.UTC)

	if got := FromTime(wall, shanghai, DefaultCutoff); got != "2026-09-17" {
		t.Errorf("上海时区 = %s, want 2026-09-17", got)
	}
	if got := FromTime(wall, time.UTC, DefaultCutoff); got != "2026-09-16" {
		t.Errorf("UTC 时区 = %s, want 2026-09-16", got)
	}
}

func TestAdd(t *testing.T) {
	cases := []struct {
		in   Day
		n    int
		want Day
	}{
		{"2026-09-16", 1, "2026-09-17"},
		{"2026-09-16", 7, "2026-09-23"},
		{"2026-09-16", 0, "2026-09-16"},
		{"2026-09-16", -1, "2026-09-15"},
		{"2026-12-31", 1, "2027-01-01"}, // 跨年
		{"2026-02-28", 1, "2026-03-01"}, // 平年
		{"2028-02-28", 1, "2028-02-29"}, // 闰年
		{"2026-09-30", 1, "2026-10-01"}, // 跨月
	}
	for _, c := range cases {
		if got := c.in.Add(c.n); got != c.want {
			t.Errorf("%s.Add(%d) = %s, want %s", c.in, c.n, got, c.want)
		}
	}
}

func TestComparison(t *testing.T) {
	a, b := Day("2026-09-16"), Day("2026-09-17")

	if !a.Before(b) {
		t.Error("2026-09-16 应早于 2026-09-17")
	}
	if b.Before(a) {
		t.Error("2026-09-17 不应早于 2026-09-16")
	}
	if !b.After(a) {
		t.Error("2026-09-17 应晚于 2026-09-16")
	}
	if a.After(a) {
		t.Error("同一天不应晚于自己")
	}
	if a.Before(a) {
		t.Error("同一天不应早于自己")
	}

	// 零值参与比较时一律返回 false，避免"没学过"被当成"过期"。
	z := Day("")
	if z.Before(a) || a.Before(z) || z.After(a) || a.After(z) {
		t.Error("零值与有效日期比较应返回 false")
	}
}

func TestSub(t *testing.T) {
	cases := []struct {
		d, other Day
		want     int
	}{
		{"2026-09-17", "2026-09-16", 1},
		{"2026-09-23", "2026-09-16", 7},
		{"2026-09-16", "2026-09-16", 0},
		{"2026-09-15", "2026-09-16", -1},
		{"2026-09-16", "", 0}, // 零值
		{"", "2026-09-16", 0},
	}
	for _, c := range cases {
		if got := c.d.Sub(c.other); got != c.want {
			t.Errorf("%s.Sub(%s) = %d, want %d", c.d, c.other, got, c.want)
		}
	}
}

func TestMin(t *testing.T) {
	a, b := Day("2026-09-16"), Day("2026-09-20")

	if got := Min(a, b); got != a {
		t.Errorf("Min = %s, want %s", got, a)
	}
	if got := Min(b, a); got != a {
		t.Errorf("Min 应交换律成立, got %s", got)
	}
	// 零值应被忽略——还没学过的字不该拉低到期日。
	if got := Min(Day(""), b); got != b {
		t.Errorf("Min(零值, %s) = %s, want %s", b, got, b)
	}
	if got := Min(a, Day("")); got != a {
		t.Errorf("Min(%s, 零值) = %s, want %s", a, got, a)
	}
}

func TestParse(t *testing.T) {
	if got, err := Parse("2026-09-16"); err != nil || got != "2026-09-16" {
		t.Errorf("Parse 合法输入失败: %v %q", err, got)
	}
	for _, bad := range []string{"", "2026/09/16", "20260916", "abc", "2026-13-01"} {
		if _, err := Parse(bad); err == nil {
			t.Errorf("Parse(%q) 应返回错误", bad)
		}
	}
}
