// Package day 提供「学习日」的纯函数工具。
//
// 本程序里「第二天」「第 7 天」都是自然日概念，不是时长概念：
// 今天 23:50 认识的字，「第二天」指的是明天零点之后，而不是 86400 秒之后。
// 因此内部一律用 'YYYY-MM-DD' 字符串表示一天，字符串比较即日期比较。
//
// 日切点（cutoff）默认为凌晨 4 点：晚上 23:30 学习算「今天」，
// 凌晨 1:00 学习算「昨天」，于是孩子睡前学的东西不会被判成跨天。
package day

import "time"

// Day 是一个学习日，格式固定为 "2006-01-02"。
type Day string

// DefaultCutoff 是默认日切点：凌晨 4 点之前算作前一天。
const DefaultCutoff = 4 * time.Hour

const layout = "2006-01-02"

// FromTime 把墙钟时间换算成学习日。
// t 会先转换到 loc 时区，再减去 cutoff 偏移，最后取日期部分。
func FromTime(t time.Time, loc *time.Location, cutoff time.Duration) Day {
	if loc == nil {
		loc = time.Local
	}
	return Day(t.In(loc).Add(-cutoff).Format(layout))
}

// Today 是 FromTime 的便捷形式，用给定的 cutoff 换算「现在」。
func Today(loc *time.Location, cutoff time.Duration) Day {
	return FromTime(time.Now(), loc, cutoff)
}

// Parse 解析 'YYYY-MM-DD'，非法输入返回零值 Day 和错误。
func Parse(s string) (Day, error) {
	t, err := time.ParseInLocation(layout, s, time.UTC)
	if err != nil {
		return "", err
	}
	return Day(t.Format(layout)), nil
}

// IsZero 判断是否为零值（即"没有这个日期"）。
func (d Day) IsZero() bool { return d == "" }

// String 实现 fmt.Stringer。
func (d Day) String() string { return string(d) }

// Add 返回 n 天之后的日期。n 可以为负。
//
// 用日历加法（AddDate）而不是 +24h，这样跨夏令时不会出现差一天的错误。
func (d Day) Add(n int) Day {
	t, err := time.ParseInLocation(layout, string(d), time.UTC)
	if err != nil {
		return ""
	}
	return Day(t.AddDate(0, 0, n).Format(layout))
}

// Before 判断 d 是否早于 other。
func (d Day) Before(other Day) bool {
	if d.IsZero() || other.IsZero() {
		return false
	}
	return string(d) < string(other)
}

// After 判断 d 是否晚于 other。
func (d Day) After(other Day) bool {
	if d.IsZero() || other.IsZero() {
		return false
	}
	return string(d) > string(other)
}

// Sub 返回 d 比 other 晚多少天。d 晚于 other 时为正，早于时为负。
// 任一为零值时返回 0。
func (d Day) Sub(other Day) int {
	if d.IsZero() || other.IsZero() {
		return 0
	}
	return int(d.Time().Sub(other.Time()) / (24 * time.Hour)) //nolint:durationcheck // 明确要天数
}

// Time 返回该学习日对应的 UTC 零点。
// 只用于日期算术，不要用于生成时间戳。
func (d Day) Time() time.Time {
	t, err := time.ParseInLocation(layout, string(d), time.UTC)
	if err != nil {
		return time.Time{}
	}
	return t
}

// Min 返回两个日期中较早的一个，零值被忽略。
func Min(a, b Day) Day {
	switch {
	case a.IsZero():
		return b
	case b.IsZero():
		return a
	case a.Before(b):
		return a
	default:
		return b
	}
}
