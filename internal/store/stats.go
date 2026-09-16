package store

import (
	"database/sql"

	"chinese-learn/internal/day"
	"chinese-learn/internal/schedule"
)

// SemesterStats 是某个学期的学习进度。
type SemesterStats struct {
	Semester Semester
	Total    int // 该学期总的字数
	Mastered int // 已掌握
	Learning int // 学习中
	New      int // 还没学
	DueToday int // 今天要复习的
}

// OverallStats 是某个孩子的总体情况。
type OverallStats struct {
	Total    int
	Mastered int
	Learning int
	New      int
	DueToday int
	// DueTotal 是包含逾期的待复习总数。
	DueTotal int
	// Backlog 是逾期数量。
	Backlog int
}

// MasteryLevel 是判定「已掌握」的等级门槛。
//
// 直接取自调度参数的单一来源——两处各写一个常量迟早会漂移，
// 那会导致仪表盘说「已掌握」而调度仍按未掌握处理。
var MasteryLevel = schedule.DefaultParams().MasteryLevel

// childStats 汇总一个孩子的总体进度。
func (s *Store) ChildStats(childID int64, today day.Day) (OverallStats, error) {
	var st OverallStats

	err := s.db.QueryRow(
		`SELECT
		   COUNT(*),
		   COALESCE(SUM(CASE WHEN rs.level >= ? THEN 1 ELSE 0 END), 0),
		   COALESCE(SUM(CASE WHEN rs.level > 0 AND rs.level < ? THEN 1 ELSE 0 END), 0),
		   COALESCE(SUM(CASE WHEN rs.due_on IS NULL THEN 1 ELSE 0 END), 0)
		 FROM hanzi h
		 JOIN review_state rs ON rs.hanzi_id = h.id
		 WHERE h.child_id = ? AND h.status <> 'suspended'`,
		MasteryLevel, MasteryLevel, childID).
		Scan(&st.Total, &st.Mastered, &st.Learning, &st.New)
	if err != nil {
		return st, err
	}

	if err := s.db.QueryRow(
		`SELECT COALESCE(SUM(CASE WHEN rs.due_on <= ? THEN 1 ELSE 0 END), 0)
		 FROM hanzi h JOIN review_state rs ON rs.hanzi_id = h.id
		 WHERE h.child_id = ? AND h.status <> 'suspended' AND rs.due_on IS NOT NULL`,
		string(today), childID).Scan(&st.DueToday); err != nil {
		return st, err
	}

	if err := s.db.QueryRow(
		`SELECT COALESCE(SUM(CASE WHEN rs.due_on < ? THEN 1 ELSE 0 END), 0)
		 FROM hanzi h JOIN review_state rs ON rs.hanzi_id = h.id
		 WHERE h.child_id = ? AND h.status <> 'suspended' AND rs.due_on IS NOT NULL`,
		string(today), childID).Scan(&st.Backlog); err != nil {
		return st, err
	}
	st.DueTotal = st.DueToday

	return st, nil
}

// SemesterStatsList 按学期汇总进度，用于仪表盘的分组展示。
func (s *Store) SemesterStatsList(childID int64, today day.Day) ([]SemesterStats, error) {
	sems, err := s.ListSemesters(childID)
	if err != nil {
		return nil, err
	}

	out := make([]SemesterStats, 0, len(sems))
	for _, sem := range sems {
		var ss SemesterStats
		ss.Semester = sem

		err := s.db.QueryRow(
			`SELECT
			   COUNT(*),
			   COALESCE(SUM(CASE WHEN rs.level >= ? THEN 1 ELSE 0 END), 0),
			   COALESCE(SUM(CASE WHEN rs.level > 0 AND rs.level < ? THEN 1 ELSE 0 END), 0),
			   COALESCE(SUM(CASE WHEN rs.due_on IS NULL THEN 1 ELSE 0 END), 0),
			   COALESCE(SUM(CASE WHEN rs.due_on IS NOT NULL AND rs.due_on <= ? THEN 1 ELSE 0 END), 0)
			 FROM hanzi h
			 JOIN review_state rs ON rs.hanzi_id = h.id
			 WHERE h.semester_id = ? AND h.status <> 'suspended'`,
			MasteryLevel, MasteryLevel, string(today), sem.ID).
			Scan(&ss.Total, &ss.Mastered, &ss.Learning, &ss.New, &ss.DueToday)
		if err != nil {
			return nil, err
		}
		out = append(out, ss)
	}
	return out, nil
}

// HeatCell 是热力图的一天。
type HeatCell struct {
	Day      day.Day
	Answered int
	Known    int
}

// Heatmap 返回最近 n 天的完成情况，从早到晚排列。
func (s *Store) Heatmap(childID int64, today day.Day, n int) ([]HeatCell, error) {
	start := today.Add(-(n - 1))

	rows, err := s.db.Query(
		`SELECT day, answered, known FROM day_session
		 WHERE child_id = ? AND day >= ? AND day <= ?
		 ORDER BY day`, childID, string(start), string(today))
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	byDay := make(map[day.Day]HeatCell)
	for rows.Next() {
		var d string
		var c HeatCell
		if err := rows.Scan(&d, &c.Answered, &c.Known); err != nil {
			return nil, err
		}
		c.Day = day.Day(d)
		byDay[c.Day] = c
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// 补齐没有记录的日期，让前端拿到连续的序列。
	out := make([]HeatCell, 0, n)
	for i := 0; i < n; i++ {
		d := start.Add(i)
		if c, ok := byDay[d]; ok {
			out = append(out, c)
			continue
		}
		out = append(out, HeatCell{Day: d})
	}
	return out, nil
}

// HanziRow 是字表管理页的一行。
type HanziRow struct {
	HanziID      int64
	Ch           string
	SemesterID   int64
	SemesterName string
	Status       string
	Level        int
	DueOn        day.Day
	Correct      int
	Wrong        int
	StreakWrong  int
	LastReviewOn day.Day
	// IsDue 表示今天需要复习（在 Go 里算好，模板不必做日期比较）。
	IsDue bool
}

// ListHanzi 分页列出某个孩子的字。
//
// filter 可取 "all" / "due" / "new" / "mastered" / "wrong"。
func (s *Store) ListHanzi(childID int64, today day.Day, filter string, limit, offset int) ([]HanziRow, int, error) {
	where := "h.child_id = ? AND h.status <> 'suspended'"
	args := []any{childID}

	switch filter {
	case "due":
		where += " AND rs.due_on IS NOT NULL AND rs.due_on <= ?"
		args = append(args, string(today))
	case "new":
		where += " AND rs.due_on IS NULL"
	case "mastered":
		where += " AND rs.level >= ?"
		args = append(args, MasteryLevel)
	case "wrong":
		where += " AND rs.wrong_count > 0"
	}

	var total int
	if err := s.db.QueryRow(
		`SELECT COUNT(*) FROM hanzi h JOIN review_state rs ON rs.hanzi_id = h.id WHERE `+where,
		args...).Scan(&total); err != nil {
		return nil, 0, err
	}

	query := `SELECT h.id, h.ch, h.semester_id, sm.name, h.status, rs.level,
	                 rs.due_on, rs.correct_count, rs.wrong_count, rs.streak_wrong,
	                 rs.last_review_on
	          FROM hanzi h
	          JOIN review_state rs ON rs.hanzi_id = h.id
	          JOIN semester sm ON sm.id = h.semester_id
	          WHERE ` + where + `
	          ORDER BY rs.level ASC, rs.due_on ASC, h.seq ASC
	          LIMIT ? OFFSET ?`
	args = append(args, limit, offset)

	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	var out []HanziRow
	for rows.Next() {
		var r HanziRow
		var dueOn, lastReview sql.NullString
		if err := rows.Scan(&r.HanziID, &r.Ch, &r.SemesterID, &r.SemesterName, &r.Status,
			&r.Level, &dueOn, &r.Correct, &r.Wrong, &r.StreakWrong, &lastReview); err != nil {
			return nil, 0, err
		}
		r.DueOn = day.Day(dueOn.String)
		r.LastReviewOn = day.Day(lastReview.String)
		r.IsDue = !r.DueOn.IsZero() && !r.DueOn.After(today)
		out = append(out, r)
	}
	return out, total, rows.Err()
}

// LogRow 是单字历史流水的一行。
type LogRow struct {
	ReviewedOn  day.Day
	ReviewedAt  string
	Result      string
	Reason      string
	LevelBefore int
	LevelAfter  int
	DueAfter    day.Day
}

// HanziHistory 返回某个字的复习历史，最新的在前。
func (s *Store) HanziHistory(hanziID int64, limit int) ([]LogRow, error) {
	rows, err := s.db.Query(
		`SELECT reviewed_on, reviewed_at, result, reason, level_before, level_after, due_after
		 FROM review_log WHERE hanzi_id = ?
		 ORDER BY id DESC LIMIT ?`, hanziID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []LogRow
	for rows.Next() {
		var r LogRow
		var dueAfter sql.NullString
		if err := rows.Scan(&r.ReviewedOn, &r.ReviewedAt, &r.Result, &r.Reason,
			&r.LevelBefore, &r.LevelAfter, &dueAfter); err != nil {
			return nil, err
		}
		r.DueAfter = day.Day(dueAfter.String)
		out = append(out, r)
	}
	return out, rows.Err()
}

// HanziDetail 是单个字的完整信息，用于详情页。
func (s *Store) HanziDetail(hanziID int64) (HanziRow, error) {
	var r HanziRow
	var dueOn, lastReview sql.NullString
	err := s.db.QueryRow(
		`SELECT h.id, h.ch, h.semester_id, sm.name, h.status, rs.level,
		        rs.due_on, rs.correct_count, rs.wrong_count, rs.streak_wrong,
		        rs.last_review_on
		 FROM hanzi h
		 JOIN review_state rs ON rs.hanzi_id = h.id
		 JOIN semester sm ON sm.id = h.semester_id
		 WHERE h.id = ?`, hanziID).
		Scan(&r.HanziID, &r.Ch, &r.SemesterID, &r.SemesterName, &r.Status,
			&r.Level, &dueOn, &r.Correct, &r.Wrong, &r.StreakWrong, &lastReview)
	if err == sql.ErrNoRows {
		return r, ErrNotFound
	}
	r.DueOn = day.Day(dueOn.String)
	r.LastReviewOn = day.Day(lastReview.String)
	return r, err
}

// HanziDetailToday 与 HanziDetail 相同，但额外标记今天是否到期。
func (s *Store) HanziDetailToday(hanziID int64, today day.Day) (HanziRow, error) {
	r, err := s.HanziDetail(hanziID)
	if err != nil {
		return r, err
	}
	r.IsDue = !r.DueOn.IsZero() && !r.DueOn.After(today)
	return r, nil
}

// SetHanziStatus 手动改字的状态。
//
// action 可取：
//   - "suspend"   暂停，不再出现在队列里
//   - "resume"    恢复
//   - "reset"     清空全部进度，重新当新字学
//   - "learn_now" 提前到今天复习（不改等级）
//
// today 仅在 "learn_now" 时使用。
func (s *Store) SetHanziStatus(hanziID int64, action string, today day.Day) error {
	switch action {
	case "suspend":
		_, err := s.db.Exec(`UPDATE hanzi SET status = 'suspended' WHERE id = ?`, hanziID)
		return err
	case "resume":
		_, err := s.db.Exec(`UPDATE hanzi SET status = 'learning' WHERE id = ?`, hanziID)
		return err
	case "reset":
		return s.withTx(func(tx *sql.Tx) error {
			if _, err := tx.Exec(
				`UPDATE review_state SET level = 0, interval_days = 0, due_on = NULL,
				   last_review_on = NULL, first_learned_on = NULL,
				   correct_count = 0, wrong_count = 0,
				   streak_correct = 0, streak_wrong = 0, lapses = 0
				 WHERE hanzi_id = ?`, hanziID); err != nil {
				return err
			}
			_, err := tx.Exec(
				`UPDATE hanzi SET status = 'new', first_learned_on = NULL WHERE id = ?`, hanziID)
			return err
		})
	case "learn_now":
		// 提前到今天复习：只把到期日提到今天，不动等级与锚点。
		return s.withTx(func(tx *sql.Tx) error {
			if _, err := tx.Exec(
				`UPDATE review_state SET due_on = ? WHERE hanzi_id = ?`,
				string(today), hanziID); err != nil {
				return err
			}
			// 从未学过的字要顺便脱离「新字」状态，否则会同时出现在两个通道里。
			_, err := tx.Exec(
				`UPDATE hanzi SET status = 'learning' WHERE id = ? AND status = 'new'`, hanziID)
			return err
		})
	default:
		return ErrNotFound
	}
}

// DeleteHanzi 删除一个字及其进度。
func (s *Store) DeleteHanzi(hanziID int64) error {
	_, err := s.db.Exec(`DELETE FROM hanzi WHERE id = ?`, hanziID)
	return err
}
