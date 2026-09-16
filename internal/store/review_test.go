package store

import (
	"errors"
	"testing"

	"chinese-learn/internal/day"
	"chinese-learn/internal/schedule"
)

// seed 导入一批字并返回孩子、学期 ID。
func seed(t *testing.T, s *Store, text string) (int64, int64) {
	t.Helper()
	child, err := s.ListChildren()
	if err != nil {
		t.Fatal(err)
	}
	cid := child[0].ID
	sem, err := s.CurrentSemester(cid)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CommitImport(ImportRequest{
		ChildID: cid, SemesterID: sem.ID, SourceName: "test", Text: text,
	}); err != nil {
		t.Fatal(err)
	}
	return cid, sem.ID
}

// firstHanziID 取某个字的 ID。
func hanziID(t *testing.T, s *Store, cid int64, ch string) int64 {
	t.Helper()
	var id int64
	if err := s.DB().QueryRow(
		`SELECT id FROM hanzi WHERE child_id = ? AND ch = ?`, cid, ch).Scan(&id); err != nil {
		t.Fatalf("找不到字 %q: %v", ch, err)
	}
	return id
}

func dayStr(s string) day.Day { return day.Day(s) }

func TestSubmitReview_UnknownDueTomorrow(t *testing.T) {
	s := newTestStore(t)
	cid, _ := seed(t, s, "天地人")
	hid := hanziID(t, s, cid, "天")

	p := schedule.DefaultParams()
	today := dayStr("2026-09-16")

	out, err := s.SubmitReview(p, SubmitRequest{
		ChildID: cid, HanziID: hid, Result: schedule.Unknown, Now: today,
	})
	if err != nil {
		t.Fatal(err)
	}

	// 用户明确要求：不认识的字第二天必须再看。
	if out.DueOn != today.Add(1) {
		t.Errorf("不认识后到期日 = %s, want %s", out.DueOn, today.Add(1))
	}
	if !out.FirstTime {
		t.Error("首次学习应标记 FirstTime")
	}

	var level, streakWrong int
	var dueOn string
	if err := s.DB().QueryRow(
		`SELECT level, streak_wrong, due_on FROM review_state WHERE hanzi_id = ?`, hid).
		Scan(&level, &streakWrong, &dueOn); err != nil {
		t.Fatal(err)
	}
	if dueOn != string(today.Add(1)) {
		t.Errorf("库中 due_on = %s, want %s", dueOn, today.Add(1))
	}
	if streakWrong != 1 {
		t.Errorf("streak_wrong = %d, want 1", streakWrong)
	}
}

func TestSubmitReview_FirstSessionAnchors(t *testing.T) {
	s := newTestStore(t)
	cid, _ := seed(t, s, "天地人")
	hid := hanziID(t, s, cid, "天")

	p := schedule.DefaultParams()
	d0 := dayStr("2026-09-16")

	// D0 认识 → 等级 1，明天再看（第 2 天锚点）。
	out, err := s.SubmitReview(p, SubmitRequest{
		ChildID: cid, HanziID: hid, Result: schedule.Known, Now: d0,
	})
	if err != nil {
		t.Fatal(err)
	}
	if out.Level != 1 {
		t.Errorf("等级 = %d, want 1", out.Level)
	}
	if out.DueOn != d0.Add(1) {
		t.Errorf("D0 认识后 = %s, want %s", out.DueOn, d0.Add(1))
	}

	// D1 认识 → 等级 2，锚点把它拉回 D2。
	out2, err := s.SubmitReview(p, SubmitRequest{
		ChildID: cid, HanziID: hid, Result: schedule.Known, Now: d0.Add(1),
	})
	if err != nil {
		t.Fatal(err)
	}
	if out2.Level != 2 {
		t.Errorf("等级 = %d, want 2", out2.Level)
	}
	if out2.DueOn != d0.Add(2) {
		t.Errorf("D1 认识后 = %s, want %s（第 3 天锚点）", out2.DueOn, d0.Add(2))
	}
}

func TestSubmitReview_DuplicateBlocked(t *testing.T) {
	s := newTestStore(t)
	cid, _ := seed(t, s, "天地人")
	hid := hanziID(t, s, cid, "天")

	p := schedule.DefaultParams()
	today := dayStr("2026-09-16")
	req := SubmitRequest{
		ChildID: cid, HanziID: hid, Result: schedule.Known,
		SessionID: "session-1", Now: today,
	}

	if _, err := s.SubmitReview(p, req); err != nil {
		t.Fatal(err)
	}
	// 连点第二次应被挡住，而不是把进度推进两次。
	if _, err := s.SubmitReview(p, req); !errors.Is(err, ErrDuplicate) {
		t.Errorf("重复提交应返回 ErrDuplicate，得到 %v", err)
	}

	var n int
	s.DB().QueryRow(`SELECT COUNT(*) FROM review_log WHERE child_id = ?`, cid).Scan(&n)
	if n != 1 {
		t.Errorf("流水条数 = %d, want 1", n)
	}
}

func TestSubmitReview_LapseResets(t *testing.T) {
	s := newTestStore(t)
	cid, _ := seed(t, s, "天地人")
	hid := hanziID(t, s, cid, "天")

	p := schedule.DefaultParams()
	today := dayStr("2026-09-16")

	// 先把等级推高。
	if _, err := s.DB().Exec(
		`UPDATE review_state SET level = 6, first_learned_on = '2026-08-01',
		   last_review_on = '2026-09-14', due_on = '2026-09-16' WHERE hanzi_id = ?`,
		hid); err != nil {
		t.Fatal(err)
	}

	// 连错 3 次（每次换 session 以绕过去重）→ 应重置为等级 1。
	for i := 0; i < 3; i++ {
		if _, err := s.SubmitReview(p, SubmitRequest{
			ChildID: cid, HanziID: hid, Result: schedule.Unknown,
			SessionID: "s" + string(rune('a'+i)), Now: today,
		}); err != nil {
			t.Fatal(err)
		}
	}

	var level, lapses int
	if err := s.DB().QueryRow(
		`SELECT level, lapses FROM review_state WHERE hanzi_id = ?`, hid).
		Scan(&level, &lapses); err != nil {
		t.Fatal(err)
	}
	if level != 1 {
		t.Errorf("连续 3 次答错后等级 = %d, want 1", level)
	}
	if lapses != 1 {
		t.Errorf("lapses = %d, want 1", lapses)
	}
}

func TestUndoLast(t *testing.T) {
	s := newTestStore(t)
	cid, _ := seed(t, s, "天地人")
	hid := hanziID(t, s, cid, "天")

	p := schedule.DefaultParams()
	today := dayStr("2026-09-16")

	if _, err := s.SubmitReview(p, SubmitRequest{
		ChildID: cid, HanziID: hid, Result: schedule.Unknown, Now: today,
	}); err != nil {
		t.Fatal(err)
	}

	gotID, gotResult, err := s.UndoLast(cid)
	if err != nil {
		t.Fatal(err)
	}
	if gotID != hid {
		t.Errorf("撤销的字 = %d, want %d", gotID, hid)
	}
	if gotResult != "unknown" {
		t.Errorf("撤销的判定 = %q, want unknown", gotResult)
	}

	// 状态应回到「从未学过」。
	var level int
	var dueOn, firstLearned *string
	if err := s.DB().QueryRow(
		`SELECT level, due_on, first_learned_on FROM review_state WHERE hanzi_id = ?`, hid).
		Scan(&level, &dueOn, &firstLearned); err != nil {
		t.Fatal(err)
	}
	if level != 0 {
		t.Errorf("撤销后等级 = %d, want 0", level)
	}
	if dueOn != nil {
		t.Errorf("撤销后 due_on = %v, want NULL", *dueOn)
	}

	var logs int
	s.DB().QueryRow(`SELECT COUNT(*) FROM review_log WHERE child_id = ?`, cid).Scan(&logs)
	if logs != 0 {
		t.Errorf("撤销后流水条数 = %d, want 0", logs)
	}

	// 配额也应回滚。
	st, _ := s.GetDayStats(cid, today)
	if st.Answered != 0 {
		t.Errorf("撤销后 answered = %d, want 0", st.Answered)
	}
}

// 核心行为：切换学期后，新字只从新学期放，但老字仍在复习队列。
func TestPlan_SemesterSwitch(t *testing.T) {
	s := newTestStore(t)
	cid, sem1 := seed(t, s, "天地人")

	p := schedule.DefaultParams()
	today := dayStr("2026-09-16")

	// 让「天」变成到期状态（模拟上学期学过但还没掌握）。
	if _, err := s.DB().Exec(
		`UPDATE review_state SET level = 2, first_learned_on = '2026-09-01',
		   last_review_on = '2026-09-14', due_on = '2026-09-15'
		 WHERE hanzi_id = ?`, hanziID(t, s, cid, "天")); err != nil {
		t.Fatal(err)
	}

	// 新建下学期并切为当前。
	sem2, err := s.CreateSemester(cid, "一年级下册", true)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CommitImport(ImportRequest{
		ChildID: cid, SemesterID: sem2, SourceName: "下册", Text: "春夏秋冬",
	}); err != nil {
		t.Fatal(err)
	}

	plan, err := s.BuildPlan(cid, p, today)
	if err != nil {
		t.Fatal(err)
	}

	var sawOld, sawNewSemester bool
	for _, it := range plan.Items {
		if it.Ch == "天" {
			sawOld = true
			if it.SemesterID != sem1 {
				t.Errorf("「天」应属于上学期 %d, 得到 %d", sem1, it.SemesterID)
			}
		}
		if it.SemesterID == sem2 {
			sawNewSemester = true
		}
	}

	if !sawOld {
		t.Error("★ 上学期未掌握的字「天」仍应出现在复习队列里")
	}
	if !sawNewSemester {
		t.Error("新字应从当前学期（下册）放出")
	}

	// 「地」「人」是上学期的未学新字，不应再被放出。
	for _, it := range plan.Items {
		if (it.Ch == "地" || it.Ch == "人") && it.IsNew {
			t.Errorf("上学期的未学新字 %q 不应作为新字放出", it.Ch)
		}
	}
}

func TestPlan_RespectsCaps(t *testing.T) {
	s := newTestStore(t)
	cid, _ := seed(t, s, "一二三四五六七八九十")

	// 每天只放 3 个新字。
	st := DefaultSettings()
	st.DailyNewCap = 3
	if err := s.SaveSettings(cid, st); err != nil {
		t.Fatal(err)
	}

	p := schedule.DefaultParams()
	today := dayStr("2026-09-16")

	plan, err := s.BuildPlan(cid, p, today)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Items) != 3 {
		t.Errorf("新字数 = %d, want 3（受 DailyNewCap 限制）", len(plan.Items))
	}
	if plan.NewTotal != 10 {
		t.Errorf("新字总数 = %d, want 10", plan.NewTotal)
	}

	// 全部答完后，今天的配额用尽，不应再放新字。
	for _, it := range plan.Items {
		if _, err := s.SubmitReview(p, SubmitRequest{
			ChildID: cid, HanziID: it.HanziID, Result: schedule.Known, Now: today,
		}); err != nil {
			t.Fatal(err)
		}
	}

	plan2, err := s.BuildPlan(cid, p, today)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan2.Items) != 0 {
		t.Errorf("配额用尽后不应再放新字，得到 %d 个", len(plan2.Items))
	}
}

// 到期的字优先于新字出现，且逾期最久的排最前。
func TestPlan_DueBeforeNew(t *testing.T) {
	s := newTestStore(t)
	cid, _ := seed(t, s, "天地人日月")

	today := dayStr("2026-09-16")

	// 让「天」逾期 3 天，「地」逾期 1 天。
	for ch, due := range map[string]string{"天": "2026-09-13", "地": "2026-09-15"} {
		if _, err := s.DB().Exec(
			`UPDATE review_state SET level = 3, first_learned_on = '2026-09-01',
			   last_review_on = '2026-09-10', due_on = ? WHERE hanzi_id = ?`,
			due, hanziID(t, s, cid, ch)); err != nil {
			t.Fatal(err)
		}
	}

	p := schedule.DefaultParams()
	plan, err := s.BuildPlan(cid, p, today)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Items) < 2 {
		t.Fatalf("应有至少 2 个到期字，得到 %d", len(plan.Items))
	}

	// 逾期最久的排最前。
	if plan.Items[0].Ch != "天" {
		t.Errorf("队首 = %q, want 天（逾期最久优先）", plan.Items[0].Ch)
	}
	if plan.Items[1].Ch != "地" {
		t.Errorf("第二 = %q, want 地", plan.Items[1].Ch)
	}

	if plan.Backlog != 2 {
		t.Errorf("逾期数 = %d, want 2", plan.Backlog)
	}
}

// 到期复习已经很多时，今天不应再引入新字。
//
// 这条规则曾经因为「在 dueTotal 赋值之前就判断」而完全失效，
// 且没有任何测试覆盖到——所以单独测一次。
func TestPlan_HeavyBacklogSuppressesNew(t *testing.T) {
	s := newTestStore(t)
	cid, _ := seed(t, s, "一二三四五六七八九十甲乙丙丁戊己庚辛壬癸")

	// 阈值设为 5：到期字达到 5 个就不放新字。
	cfg := DefaultSettings()
	cfg.HeaviestThresh = 5
	cfg.DailyNewCap = 10
	if err := s.SaveSettings(cid, cfg); err != nil {
		t.Fatal(err)
	}

	p := schedule.DefaultParams()
	today := dayStr("2026-09-16")

	// 造 5 个逾期字。
	var ids []int64
	rows, err := s.DB().Query(
		`SELECT id FROM hanzi WHERE child_id = ? ORDER BY seq LIMIT 5`, cid)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var id int64
		rows.Scan(&id)
		ids = append(ids, id)
	}
	rows.Close()

	for _, id := range ids {
		if _, err := s.DB().Exec(
			`UPDATE review_state SET level = 2, first_learned_on = '2026-09-01',
			   last_review_on = '2026-09-10', due_on = '2026-09-15'
			 WHERE hanzi_id = ?`, id); err != nil {
			t.Fatal(err)
		}
	}

	plan, err := s.BuildPlan(cid, p, today)
	if err != nil {
		t.Fatal(err)
	}

	if plan.DueTotal < 5 {
		t.Fatalf("到期数 = %d, 应至少 5", plan.DueTotal)
	}
	for _, it := range plan.Items {
		if it.IsNew {
			t.Errorf("到期字已达阈值，不应再放出新字 %q", it.Ch)
		}
	}
	if plan.NewRemaining != 0 {
		t.Errorf("剩余新字配额 = %d, want 0", plan.NewRemaining)
	}
}

func TestPlan_EmptyDatabase(t *testing.T) {
	s := newTestStore(t)
	child, _ := s.ListChildren()
	cid := child[0].ID

	p := schedule.DefaultParams()
	plan, err := s.BuildPlan(cid, p, dayStr("2026-09-16"))
	if err != nil {
		t.Fatalf("空库不应报错: %v", err)
	}
	if len(plan.Items) != 0 {
		t.Errorf("空库应有 0 个待复习，得到 %d", len(plan.Items))
	}
}

func TestSubmitReview_SuspendedExcluded(t *testing.T) {
	s := newTestStore(t)
	cid, _ := seed(t, s, "天地人")
	hid := hanziID(t, s, cid, "天")

	if _, err := s.DB().Exec(
		`UPDATE hanzi SET status = 'suspended' WHERE id = ?`, hid); err != nil {
		t.Fatal(err)
	}

	p := schedule.DefaultParams()
	today := dayStr("2026-09-16")

	// 暂停的字不在队列里。
	plan, err := s.BuildPlan(cid, p, today)
	if err != nil {
		t.Fatal(err)
	}
	for _, it := range plan.Items {
		if it.Ch == "天" {
			t.Error("暂停的字不应出现在队列里")
		}
	}

	// 也不接受提交。
	if _, err := s.SubmitReview(p, SubmitRequest{
		ChildID: cid, HanziID: hid, Result: schedule.Known, Now: today,
	}); err == nil {
		t.Error("暂停的字不应接受复习提交")
	}
}

func TestRecentMistakes(t *testing.T) {
	s := newTestStore(t)
	cid, _ := seed(t, s, "天地人")

	p := schedule.DefaultParams()
	today := dayStr("2026-09-16")

	if _, err := s.SubmitReview(p, SubmitRequest{
		ChildID: cid, HanziID: hanziID(t, s, cid, "天"),
		Result: schedule.Unknown, Now: today,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SubmitReview(p, SubmitRequest{
		ChildID: cid, HanziID: hanziID(t, s, cid, "地"),
		Result: schedule.Known, Now: today,
	}); err != nil {
		t.Fatal(err)
	}

	got, err := s.RecentMistakes(cid, today)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0] != "天" {
		t.Errorf("错字 = %v, want [天]", got)
	}
}
