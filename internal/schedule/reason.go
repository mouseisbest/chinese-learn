package schedule

import (
	"fmt"

	"chinese-learn/internal/day"
)

// Reason 解释某个字今天为什么出现在队列里，仅用于展示。
//
// 返回值与 review_log.reason 的取值一致：
// new / anchor1 / anchor2 / anchor6 / sweep / due
//
// 之所以用「重算」而不是把约束存进数据库：约束是状态的纯函数，
// 存一份冗余副本会在参数被修改后变成谎话。
func Reason(p Params, s State, today day.Day) string {
	p = p.normalized()

	if !s.HasHistory {
		return "new"
	}

	// 今天恰好是某个锚点日——这是最能解释「为什么今天见到它」的答案。
	for i, a := range p.Anchors(s.FirstLearned) {
		if a.IsZero() {
			continue
		}
		if today == a {
			return fmt.Sprintf("anchor%d", p.AnchorDays[i])
		}
	}

	// 未掌握的字，距上次复习已达全滚间隔。
	if s.Level < p.MasteryLevel && !s.LastReviewOn.IsZero() {
		if !today.Before(s.LastReviewOn.Add(p.SweepGapDays)) {
			return "sweep"
		}
	}

	return "due"
}

// ReasonLabel 把 reason 代码转成给孩子/家长看的中文短语。
func ReasonLabel(reason string) string {
	switch reason {
	case "new":
		return "新字"
	case "anchor1":
		return "第 2 天复习"
	case "anchor2":
		return "第 3 天复习"
	case "anchor6":
		return "第 7 天复习"
	case "sweep":
		return "滚动复习"
	case "manual":
		return "手动加入"
	default:
		return "该复习了"
	}
}
