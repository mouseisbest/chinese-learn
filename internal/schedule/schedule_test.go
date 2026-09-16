package schedule

import (
	"math/rand"
	"testing"

	"chinese-learn/internal/day"
)

func D(s string) day.Day { return day.Day(s) }

// 简化测试的构造器。
func st(level int, firstLearned day.Day) State {
	return State{Level: level, HasHistory: true, FirstLearned: firstLearned}
}

func TestEvaluate_Timeline(t *testing.T) {
	// 这是方案 §4.4 里手工推演的时间线，逐日验证。
	// D0 = 2026-09-16 首次学习，锚点为 D0+1 / D0+2 / D0+6。
	p := DefaultParams()
	d0 := D("2026-09-16")

	t.Run("D0 首次认识 → 明天（第2天必看）", func(t *testing.T) {
		got := Evaluate(p, BeginFirst(d0), Known, d0)
		if got.Level != 1 {
			t.Errorf("等级 = %d, want 1", got.Level)
		}
		if got.DueOn != d0.Add(1) {
			t.Errorf("到期日 = %s, want %s", got.DueOn, d0.Add(1))
		}
	})

	t.Run("D1 认识 → 锚点把 D3 拉回 D2", func(t *testing.T) {
		s := st(1, d0)
		s.LastReviewOn = d0.Add(1)
		got := Evaluate(p, s, Known, d0.Add(1))

		if got.Level != 2 {
			t.Errorf("等级 = %d, want 2", got.Level)
		}
		if got.Interval != 2 {
			t.Errorf("标称间隔 = %d, want 2", got.Interval)
		}
		// 阶梯给出 D1+2=D3，但锚点 D0+2=D2 更早。
		if want := d0.Add(2); got.DueOn != want {
			t.Errorf("到期日 = %s, want %s（锚点应把阶梯拉前）", got.DueOn, want)
		}
		if got.Bound != "anchor+2" {
			t.Errorf("约束 = %q, want anchor+2", got.Bound)
		}
	})

	t.Run("D2 认识 → 阶梯 D6 比锚点 D7 更近", func(t *testing.T) {
		s := st(2, d0)
		s.LastReviewOn = d0.Add(2)
		got := Evaluate(p, s, Known, d0.Add(2))

		if got.Level != 3 {
			t.Errorf("等级 = %d, want 3", got.Level)
		}
		if want := d0.Add(6); got.DueOn != want {
			t.Errorf("到期日 = %s, want %s（阶梯更近，锚点不生效）", got.DueOn, want)
		}
		if got.Bound != "ladder" {
			t.Errorf("约束 = %q, want ladder", got.Bound)
		}
	})

	t.Run("D6 不认识 → 退回等级1，明天再看", func(t *testing.T) {
		s := st(3, d0)
		s.LastReviewOn = d0.Add(6)
		got := Evaluate(p, s, Unknown, d0.Add(6))

		if got.Level != 1 {
			t.Errorf("等级 = %d, want 1", got.Level)
		}
		if want := d0.Add(7); got.DueOn != want {
			t.Errorf("到期日 = %s, want %s（明天必看）", got.DueOn, want)
		}
	})

	t.Run("D7 认识 → 锚点用尽，释放给阶梯", func(t *testing.T) {
		s := st(1, d0)
		s.LastReviewOn = d0.Add(7)
		got := Evaluate(p, s, Known, d0.Add(7))

		if got.Level != 2 {
			t.Errorf("等级 = %d, want 2", got.Level)
		}
		if want := d0.Add(9); got.DueOn != want {
			t.Errorf("到期日 = %s, want %s（阶梯 2 天）", got.DueOn, want)
		}
		if got.Bound != "ladder" {
			t.Errorf("约束 = %q, want ladder（锚点应已释放）", got.Bound)
		}
	})
}

func TestEvaluate_Unknown(t *testing.T) {
	p := DefaultParams()
	today := D("2026-10-01")
	learned := D("2026-09-01") // 锚点早已过期

	t.Run("全新字不认识 → 明天", func(t *testing.T) {
		got := Evaluate(p, BeginFirst(today), Unknown, today)
		if got.Level != 1 {
			t.Errorf("等级 = %d, want 1", got.Level)
		}
		if want := today.Add(1); got.DueOn != want {
			t.Errorf("到期日 = %s, want %s", got.DueOn, want)
		}
	})

	t.Run("高等级答错回退 2 级", func(t *testing.T) {
		s := st(6, learned)
		s.LastReviewOn = today
		got := Evaluate(p, s, Unknown, today)

		if got.Level != 4 {
			t.Errorf("等级 = %d, want 4（6-2）", got.Level)
		}
		if got.Lapsed {
			t.Error("只错一次不应触发重置")
		}
	})

	t.Run("等级1答错仍为1，不跌破", func(t *testing.T) {
		s := st(1, learned)
		s.LastReviewOn = today
		got := Evaluate(p, s, Unknown, today)

		if got.Level != 1 {
			t.Errorf("等级 = %d, want 1（不得跌破等级 1）", got.Level)
		}
	})

	t.Run("等级0答错不产生负等级", func(t *testing.T) {
		s := State{Level: 0, HasHistory: true, FirstLearned: today}
		got := Evaluate(p, s, Unknown, today)

		if got.Level < 1 {
			t.Errorf("等级 = %d，不得为负或零", got.Level)
		}
	})

	t.Run("连续错阈值", func(t *testing.T) {
		cases := []struct {
			streakWrong int
			wantLevel   int
			wantLapsed  bool
		}{
			{0, 4, false}, // 第 1 次错：6-2
			{1, 4, false}, // 第 2 次错：6-2
			{2, 1, true},  // 第 3 次错：重置
			{5, 1, true},  // 继续错仍重置
		}
		for _, c := range cases {
			s := st(6, learned)
			s.LastReviewOn = today
			s.StreakWrong = c.streakWrong
			got := Evaluate(p, s, Unknown, today)

			if got.Level != c.wantLevel {
				t.Errorf("streakWrong=%d: 等级 = %d, want %d",
					c.streakWrong, got.Level, c.wantLevel)
			}
			if got.Lapsed != c.wantLapsed {
				t.Errorf("streakWrong=%d: Lapsed = %v, want %v",
					c.streakWrong, got.Lapsed, c.wantLapsed)
			}
		}
	})
}

func TestEvaluate_Mastery(t *testing.T) {
	p := DefaultParams()
	today := D("2026-10-01")
	learned := D("2026-09-01")

	// 阶梯：L1=1 L2=2 L3=4 L4=7 L5=15 L6=30 L7=60 L8=90 天
	// 掌握线是 L7 —— 达到后豁免 7 天上界，走长间隔。
	t.Run("已掌握的字走长间隔", func(t *testing.T) {
		s := st(7, learned)
		s.LastReviewOn = today
		got := Evaluate(p, s, Known, today)

		if got.Level != 8 {
			t.Fatalf("等级 = %d, want 8", got.Level)
		}
		if want := today.Add(90); got.DueOn != want {
			t.Errorf("到期日 = %s, want %s（豁免全滚，走 90 天）", got.DueOn, want)
		}
		if got.Bound != "ladder" {
			t.Errorf("约束 = %q, want ladder", got.Bound)
		}
	})

	// 关键：L4→L5 后标称间隔 15 天，但 L5 尚未达掌握线，
	// 必须被 7 天上界砍回来。这条保证小学生「连着认对几次」
	// 不会被过早判定为掌握而退出滚动复习。
	t.Run("未掌握的字被 7 天上界砍短", func(t *testing.T) {
		s := st(4, learned)
		s.LastReviewOn = today
		got := Evaluate(p, s, Known, today)

		if got.Level != 5 {
			t.Fatalf("等级 = %d, want 5", got.Level)
		}
		if got.Interval != 15 {
			t.Fatalf("标称间隔 = %d, want 15", got.Interval)
		}
		if want := today.Add(7); got.DueOn != want {
			t.Errorf("到期日 = %s, want %s（15 天应被砍到 7 天）", got.DueOn, want)
		}
		if got.Bound != "sweep" {
			t.Errorf("约束 = %q, want sweep", got.Bound)
		}
	})

	// 跨过掌握线的那一次复习：升级后即豁免，走完整长间隔。
	t.Run("升级到掌握线即豁免", func(t *testing.T) {
		s := st(6, learned)
		s.LastReviewOn = today
		got := Evaluate(p, s, Known, today)

		if got.Level != 7 {
			t.Fatalf("等级 = %d, want 7", got.Level)
		}
		if want := today.Add(60); got.DueOn != want {
			t.Errorf("到期日 = %s, want %s（L7 已达掌握线，走 60 天）", got.DueOn, want)
		}
		if got.Bound != "ladder" {
			t.Errorf("约束 = %q, want ladder", got.Bound)
		}
	})
}

// 全滚上界必须以「今天」为基准，而不是「上次复习日」。
// 否则逾期很久的字会被算出一个已经过去的到期日，导致第二天又出现。
func TestEvaluate_SweepBaseIsToday(t *testing.T) {
	p := DefaultParams()
	p.MasteryLevel = 9 // 只有满级才算掌握（9 高于 MaxLevel，等同于不豁免）

	lastReview := D("2026-09-01")
	today := D("2026-11-01") // 隔了两个月才复习

	s := st(6, lastReview)
	s.LastReviewOn = lastReview
	got := Evaluate(p, s, Known, today)

	if !got.DueOn.After(today) {
		t.Errorf("到期日 = %s，必须晚于今天 %s", got.DueOn, today)
	}
	if want := today.Add(7); got.DueOn != want {
		t.Errorf("到期日 = %s, want %s（应以今天为基准，而非上次复习日）", got.DueOn, want)
	}
}

// 无论输入多荒谬，DueOn 都必须晚于 today——这是「同一个字同一天不出两次」的保证。
func TestEvaluate_DueOnAlwaysFuture(t *testing.T) {
	p := DefaultParams()
	today := D("2026-10-01")

	for _, r := range []Result{Known, Unknown} {
		for lvl := 0; lvl <= 12; lvl++ {
			for _, firstLearned := range []day.Day{"", today, today.Add(-1), today.Add(-3), today.Add(-6)} {
				for _, lastReview := range []day.Day{"", today, today.Add(-100)} {
					s := State{
						Level:        lvl,
						HasHistory:   true,
						FirstLearned: firstLearned,
						LastReviewOn: lastReview,
					}
					got := Evaluate(p, s, r, today)
					if !got.DueOn.After(today) {
						t.Errorf("r=%v lvl=%d first=%s last=%s: 到期日 %s 未晚于今天 %s",
							r, lvl, firstLearned, lastReview, got.DueOn, today)
					}
				}
			}
		}
	}
}

// 性质测试：随机输入下等级始终在合法范围内，且不 panic。
func TestEvaluate_Properties(t *testing.T) {
	p := DefaultParams()
	rng := rand.New(rand.NewSource(42))
	base := D("2026-01-01")

	for i := 0; i < 10000; i++ {
		today := base.Add(rng.Intn(365))
		s := State{
			Level:         rng.Intn(15) - 2, // 故意包含越界值
			StreakCorrect: rng.Intn(10),
			StreakWrong:   rng.Intn(10),
			FirstLearned:  base.Add(rng.Intn(400)),
			LastReviewOn:  base.Add(rng.Intn(400)),
			HasHistory:    rng.Intn(2) == 0,
		}
		r := Result(rng.Intn(2))

		got := Evaluate(p, s, r, today)

		if got.Level < 1 || got.Level > p.MaxLevel {
			t.Fatalf("等级 %d 越界 [1,%d]（输入 level=%d r=%v）",
				got.Level, p.MaxLevel, s.Level, r)
		}
		if !got.DueOn.After(today) {
			t.Fatalf("到期日 %s 未晚于今天 %s（输入 %+v r=%v）",
				got.DueOn, today, s, r)
		}
		if got.Interval < 0 {
			t.Fatalf("间隔 %d 为负", got.Interval)
		}
	}
}

// 参数被改坏时不能 panic，也不能产生非法结果。
func TestParams_Normalization(t *testing.T) {
	today := D("2026-10-01")
	bad := []Params{
		{},                                 // 全零值
		{Ladder: []int{0}},                 // 阶梯太短
		{MaxLevel: -5},                     // 负等级上限
		{MaxLevel: 999},                    // 等级上限超出阶梯
		{Backstep: -1, ResetAtWrong: 0},    // 负回退、零阈值
		{SweepGapDays: 0, MasteryLevel: 0}, // 零全滚间隔
		{Ladder: []int{0, 1}, AnchorDays: []int{}}, // 锚点为空
	}
	for i, p := range bad {
		s := State{Level: 3, HasHistory: true, FirstLearned: today.Add(-10)}
		got := Evaluate(p, s, Known, today)

		if got.Level < 1 {
			t.Errorf("params[%d]: 等级 = %d, 不得小于 1", i, got.Level)
		}
		if !got.DueOn.After(today) {
			t.Errorf("params[%d]: 到期日 %s 未晚于今天", i, got.DueOn)
		}
	}
}

func TestAnchors(t *testing.T) {
	p := DefaultParams()
	d0 := D("2026-09-16")

	got := p.Anchors(d0)
	want := []day.Day{d0.Add(1), d0.Add(2), d0.Add(6)}
	if len(got) != len(want) {
		t.Fatalf("锚点数量 = %d, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("锚点[%d] = %s, want %s", i, got[i], want[i])
		}
	}

	// 从未学过的字没有锚点。
	if got := p.Anchors(""); got != nil {
		t.Errorf("零值首次学习日应返回 nil，得到 %v", got)
	}
}

func TestIsMastered(t *testing.T) {
	p := DefaultParams()

	// 掌握线是 L7：低于它的一律仍受 7 天全滚保护。
	for _, lvl := range []int{0, 1, 4, 5, 6} {
		if p.IsMastered(lvl) {
			t.Errorf("等级 %d 不应算已掌握（掌握线 %d）", lvl, p.MasteryLevel)
		}
	}
	if !p.IsMastered(7) {
		t.Error("等级 7 应算已掌握")
	}
	if !p.IsMastered(8) {
		t.Error("等级 8 应算已掌握")
	}
}
