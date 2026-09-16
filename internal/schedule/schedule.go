// Package schedule 是认字程序的核心调度算法。
//
// # 设计要点
//
// 三类复习触发（常规阶梯 / 第 1·2·6 天硬锚点 / 未掌握字的 7 天上界）
// 不是三个队列，而是同一个 due_on 的三个「上界」：
//
//	due_on = min( ladder_due, anchors..., sweep_due )
//
// 取最早的那个。因此队列查询永远只有一句 WHERE due_on <= today，
// 不需要 sweep 批次表、不需要去重逻辑，同一个字同一天被排两次
// 在结构上就不可能发生——每个字只有一行、只有一个 due_on。
//
// 本包是纯函数：无 IO、无 time.Now()、无全局状态，可被穷举测试。
package schedule

import (
	"fmt"

	"chinese-learn/internal/day"
)

// Params 是调度参数，可由设置在运行时覆盖。
type Params struct {
	// Ladder 是等级 → 间隔天数。下标即等级，Ladder[0] 是占位（等级 0 表示未学）。
	Ladder []int
	// MaxLevel 是等级上限，会自动夹到 len(Ladder)-1。
	MaxLevel int
	// Backstep 是答错时回退的级数（低等级会夹到 1）。
	Backstep int
	// ResetAtWrong 是连续答错达到几次就重置等级。
	ResetAtWrong int
	// SweepGapDays 是「全滚」上界：未掌握的字最多隔这么多天必须再出现。
	SweepGapDays int
	// MasteryLevel 是「已掌握」的等级门槛，达到即豁免全滚上界。
	MasteryLevel int
	// AnchorDays 是硬锚点相对首次学习日的偏移天数。
	// 默认 {1, 2, 6}，即第 2 天、第 3 天、第 7 天（把学习当天算作第 1 天）。
	AnchorDays []int
}

// DefaultParams 返回默认参数。
//
// MasteryLevel 定在 7（间隔 30 天）而不是更低，是刻意的：
// 掌握意味着「退出 7 天全滚」，而小学生连续认对 5 次未必是真记住了。
// 要求等级达到 7 才豁免，相当于要多复习两轮，且间隔已拉长到 30 天——
// 到那时候确实是记住了。
func DefaultParams() Params {
	return Params{
		Ladder:       []int{0, 1, 2, 4, 7, 15, 30, 60, 90},
		MaxLevel:     8,
		Backstep:     2,
		ResetAtWrong: 3,
		SweepGapDays: 7,
		MasteryLevel: 7,
		AnchorDays:   []int{1, 2, 6},
	}
}

// normalized 修正非法或越界的参数，保证 Evaluate 不会 panic。
func (p Params) normalized() Params {
	if len(p.Ladder) < 2 {
		p.Ladder = DefaultParams().Ladder
	}
	if len(p.AnchorDays) == 0 {
		p.AnchorDays = DefaultParams().AnchorDays
	}
	if max := len(p.Ladder) - 1; p.MaxLevel > max || p.MaxLevel < 1 {
		p.MaxLevel = max
	}
	if p.Backstep < 1 {
		p.Backstep = 1
	}
	if p.ResetAtWrong < 1 {
		p.ResetAtWrong = 3
	}
	if p.SweepGapDays < 1 {
		p.SweepGapDays = 7
	}
	if p.MasteryLevel < 1 {
		p.MasteryLevel = p.MaxLevel + 1 // 不豁免任何字
	}
	return p
}

// interval 返回某等级对应的间隔天数，越界时夹到两端。
func (p Params) interval(level int) int {
	switch {
	case level < 0:
		return p.Ladder[0]
	case level >= len(p.Ladder):
		return p.Ladder[len(p.Ladder)-1]
	default:
		return p.Ladder[level]
	}
}

// Result 是孩子的一次判定。
type Result uint8

const (
	// Known 表示孩子点了「认识」。
	Known Result = iota
	// Unknown 表示孩子点了「不认识」。
	Unknown
)

// String 实现 fmt.Stringer，同时用于写库。
func (r Result) String() string {
	if r == Known {
		return "known"
	}
	return "unknown"
}

// State 是决策所需的全部输入，与 review_state 表的一行对应。
type State struct {
	Level         int
	StreakCorrect int
	StreakWrong   int
	// FirstLearned 是首次学习的日子，锚点基准。未学过时为零值。
	FirstLearned day.Day
	// LastReviewOn 是上一次复习日。未学过时为零值。
	LastReviewOn day.Day
	// HasHistory 表示这个字是否曾被复习过。
	HasHistory bool
}

// Next 是一次复习之后的调度结果。
type Next struct {
	// Level 是复习后的新等级。
	Level int
	// Interval 是新等级在阶梯上的标称间隔天数。
	// 注意：实际间隔可能更短（被锚点或全滚上界提前），以 DueOn 为准。
	Interval int
	// DueOn 是下次复习日。永远晚于 today。
	DueOn day.Day
	// Bound 记录是哪个约束决定了 DueOn：
	// "ladder" | "anchor+N" | "sweep"。写库后在管理页展示，便于排查。
	Bound string
	// Lapsed 表示本次答错触发了连续错误重置。
	Lapsed bool
}

// BeginFirst 为首次学习的字构造状态：把首次学习日设为 today。
//
// 调用方（store）在首次复习一个从未学过的字时应当用这个函数，
// 于是 Evaluate 会据此生成第 1/2/6 天锚点。
func BeginFirst(today day.Day) State {
	return State{HasHistory: true, FirstLearned: today, Level: 0}
}

// Anchors 返回首次学习日对应的硬锚点日期。
// firstLearned 为零值时返回三个零值，调用方应跳过。
func (p Params) Anchors(firstLearned day.Day) []day.Day {
	if firstLearned.IsZero() {
		return nil
	}
	out := make([]day.Day, 0, len(p.AnchorDays))
	for _, off := range p.AnchorDays {
		out = append(out, firstLearned.Add(off))
	}
	return out
}

// Evaluate 计算一次复习之后的新等级与下次复习日。
//
// 这是全程序唯一的调度决策点，纯函数，可穷举测试。
// 注意 DueOn 永远晚于 today——同一个字不会在同一天被出两次。
func Evaluate(p Params, s State, r Result, today day.Day) Next {
	p = p.normalized()

	// ---- 1. 等级与连续计数 ----
	var lvl int
	var lapsed bool

	if r == Known {
		lvl = s.Level + 1
		if lvl > p.MaxLevel {
			lvl = p.MaxLevel
		}
		if lvl < 1 {
			// 防脏数据：等级为负时不得跌破 1。
			lvl = 1
		}
	} else {
		if s.StreakWrong+1 >= p.ResetAtWrong {
			// 连着错够次数，说明确实忘了，退回重学。
			// 重置到 1（明天再看）而不是 0（今天再看一遍）：
			// 当天的重复记忆价值低，且会让孩子同一天反复见到同一个字。
			lvl = 1
			lapsed = true
		} else {
			lvl = s.Level - p.Backstep
			if lvl < 1 {
				lvl = 1
			}
			if lvl > p.MaxLevel {
				// 防脏数据：等级上限被调小时，旧数据可能超出。
				lvl = p.MaxLevel
			}
		}
	}

	// ---- 2. 阶梯给出的自然到期日 ----
	iv := p.interval(lvl)
	due := today.Add(iv)
	bound := "ladder"

	// ---- 3. 硬锚点：第 1/2/6 天必看 ----
	// 只考虑仍在未来的锚点——今天已经在复习了，今天及更早的锚点视为已满足。
	for i, a := range p.Anchors(s.FirstLearned) {
		if a.IsZero() || !a.After(today) {
			continue
		}
		if due.After(a) {
			due = a
			bound = fmt.Sprintf("anchor+%d", p.AnchorDays[i])
		}
	}

	// ---- 4. 全滚上界：未掌握的字最多隔 SweepGapDays 天 ----
	// 基准是 today（本次复习），不是上一次复习日——
	// 否则逾期复习的字会被算出一个已经过去的到期日。
	if lvl < p.MasteryLevel {
		hard := today.Add(p.SweepGapDays)
		if due.After(hard) {
			due = hard
			bound = "sweep"
		}
	}

	return Next{Level: lvl, Interval: iv, DueOn: due, Bound: bound, Lapsed: lapsed}
}

// IsMastered 判断某等级是否算「已掌握」（豁免全滚上界）。
func (p Params) IsMastered(level int) bool {
	return level >= p.normalized().MasteryLevel
}
