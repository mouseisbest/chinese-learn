package store

import (
	"database/sql"
	"fmt"

	"chinese-learn/internal/day"
	"chinese-learn/internal/schedule"
)

// QueueItem 是复习队列里的一个字。
type QueueItem struct {
	HanziID    int64
	Ch         string
	SemesterID int64
	Level      int
	Status     string
	DueOn      day.Day
	// Reason 说明这个字今天为什么出现：new / due / anchor1 / sweep 等。
	Reason string
	// ReasonLabel 是给人看的中文说明。
	ReasonLabel string
	// IsNew 表示这是从未学过的新字。
	IsNew bool

	// row 保留原始列，用于重建调度状态计算 Reason。
	row queueFields
}

// Plan 是今天这一批的复习计划。
type Plan struct {
	Day   day.Day
	Items []QueueItem

	// DueTotal 是今天到期的总数（可能大于 Items 里的数量）。
	DueTotal int
	// NewTotal 是当前学期尚未学的新字总数。
	NewTotal int
	// NewRemaining 是今天还能放出的新字配额。
	NewRemaining int
	// Backlog 是逾期未做的数量，用于温和提示。
	Backlog int
}

// BuildPlan 构造今天这一批的复习队列。
//
// 规则：
//   - 待复习的字取自【所有学期】——上学期没掌握的字照样要复习
//   - 新字只从【当前学期】放出
//   - 单次不超过 DailyReviewCap；若到期字已经很多，今天不再加新字
//
// p 是调度参数。返回的 Items 已按优先级排好序，客户端应按顺序出卡。
func (s *Store) BuildPlan(childID int64, p schedule.Params, now day.Day) (Plan, error) {
	plan := Plan{Day: now}

	st, err := s.GetSettings(childID)
	if err != nil {
		return plan, err
	}
	capReview := st.DailyReviewCap
	capNew := st.DailyNewCap

	// 今天已经放过多少个新字？配额要跨多次「再来一批」累计。
	newServed, err := s.newServedToday(childID, now)
	if err != nil {
		return plan, err
	}
	newLeft := capNew - newServed
	if newLeft < 0 {
		newLeft = 0
	}

	// ---- 待复习：所有学期 ----
	dueItems, dueTotal, err := s.dueItems(childID, p, now, capReview)
	if err != nil {
		return plan, err
	}
	plan.Items = append(plan.Items, dueItems...)
	plan.DueTotal = dueTotal

	// 到期复习已经很多时，今天不再加新字，避免一次压垮孩子。
	// 注意必须在取到 dueTotal 之后判断——写在这之前会让条件永远为假。
	if dueTotal >= st.HeaviestThresh {
		newLeft = 0
	}

	// ---- 新字：仅当前学期 ----
	sem, err := s.CurrentSemester(childID)
	if err != nil && err != ErrNotFound {
		return plan, err
	}
	if err == nil {
		newTotal, err := s.countNewHanzi(childID, sem.ID)
		if err != nil {
			return plan, err
		}
		plan.NewTotal = newTotal

		// 到期字已占满这一批时不再插入新字。
		room := capReview - len(plan.Items)
		if room > newLeft {
			room = newLeft
		}
		if room > 0 {
			newItems, err := s.newItems(childID, sem.ID, room)
			if err != nil {
				return plan, err
			}
			plan.Items = append(plan.Items, newItems...)
		}
	}
	plan.NewRemaining = newLeft

	// ---- 逾期数量，用于温和提示 ----
	backlog, err := s.countBacklog(childID, now)
	if err != nil {
		return plan, err
	}
	plan.Backlog = backlog

	return plan, nil
}

// dueItems 取出今天到期（含逾期）的字。
//
// 排序：欠得最久的优先；同为今天到期时，最久没见的优先。
// 后者让处理完的字排到队尾，天然形成轮转，不需要额外的游标。
func (s *Store) dueItems(childID int64, p schedule.Params, today day.Day, limit int) ([]QueueItem, int, error) {
	var total int
	err := s.db.QueryRow(
		`SELECT COUNT(*)
		 FROM review_state rs
		 JOIN hanzi h ON h.id = rs.hanzi_id
		 WHERE rs.child_id = ? AND h.status <> 'suspended'
		   AND rs.due_on IS NOT NULL AND rs.due_on <= ?`,
		childID, string(today)).Scan(&total)
	if err != nil {
		return nil, 0, err
	}

	rows, err := s.db.Query(
		`SELECT rs.hanzi_id, h.ch, h.semester_id, rs.level, h.status,
		        rs.due_on, rs.last_review_on, rs.first_learned_on,
		        rs.streak_wrong, rs.interval_days
		 FROM review_state rs
		 JOIN hanzi h ON h.id = rs.hanzi_id
		 WHERE rs.child_id = ? AND h.status <> 'suspended'
		   AND rs.due_on IS NOT NULL AND rs.due_on <= ?
		 ORDER BY rs.due_on ASC,        -- 欠得最久的先做
		          rs.last_review_on ASC, -- 同为今天到期时，最久没见的先出
		          rs.level ASC,          -- 同一天欠的，先巩固弱的字
		          rs.hanzi_id ASC
		 LIMIT ?`,
		childID, string(today), limit)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	var out []QueueItem
	for rows.Next() {
		it, err := scanQueueItem(rows)
		if err != nil {
			return nil, 0, err
		}
		it.Reason = schedule.Reason(p, it.state(), today)
		it.ReasonLabel = schedule.ReasonLabel(it.Reason)
		out = append(out, it)
	}
	return out, total, rows.Err()
}

// newItems 取出尚未学过的新字，仅限当前学期。
func (s *Store) newItems(childID, semesterID int64, limit int) ([]QueueItem, error) {
	rows, err := s.db.Query(
		`SELECT h.id, h.ch, h.semester_id, 0, h.status,
		        NULL, NULL, NULL, 0, 0
		 FROM hanzi h
		 JOIN review_state rs ON rs.hanzi_id = h.id
		 WHERE h.child_id = ? AND h.semester_id = ?
		   AND h.status = 'new' AND rs.due_on IS NULL
		 ORDER BY h.seq ASC
		 LIMIT ?`,
		childID, semesterID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []QueueItem
	for rows.Next() {
		it, err := scanQueueItem(rows)
		if err != nil {
			return nil, err
		}
		it.IsNew = true
		it.Reason = "new"
		it.ReasonLabel = schedule.ReasonLabel("new")
		out = append(out, it)
	}
	return out, rows.Err()
}

// countNewHanzi 统计当前学期尚未学的新字数量。
func (s *Store) countNewHanzi(childID, semesterID int64) (int, error) {
	var n int
	err := s.db.QueryRow(
		`SELECT COUNT(*)
		 FROM hanzi h
		 JOIN review_state rs ON rs.hanzi_id = h.id
		 WHERE h.child_id = ? AND h.semester_id = ?
		   AND h.status = 'new' AND rs.due_on IS NULL`,
		childID, semesterID).Scan(&n)
	return n, err
}

// countBacklog 统计逾期（到期日在今天之前）的字数。
func (s *Store) countBacklog(childID int64, today day.Day) (int, error) {
	var n int
	err := s.db.QueryRow(
		`SELECT COUNT(*)
		 FROM review_state rs
		 JOIN hanzi h ON h.id = rs.hanzi_id
		 WHERE rs.child_id = ? AND h.status <> 'suspended'
		   AND rs.due_on IS NOT NULL AND rs.due_on < ?`,
		childID, string(today)).Scan(&n)
	return n, err
}

// newServedToday 查询今天已经放出过多少个新字。
func (s *Store) newServedToday(childID int64, today day.Day) (int, error) {
	var n int
	err := s.db.QueryRow(
		`SELECT COALESCE(new_served, 0) FROM day_session WHERE child_id = ? AND day = ?`,
		childID, string(today)).Scan(&n)
	if err == sql.ErrNoRows {
		return 0, nil
	}
	return n, err
}

// rowScanner 抽象 *sql.Rows 的 Scan，便于复用扫描逻辑。
type rowScanner interface {
	Scan(dest ...any) error
}

// queueFields 承载一次扫描的原始列，便于计算 Reason 时重建调度状态。
type queueFields struct {
	dueOn        sql.NullString
	lastReviewOn sql.NullString
	firstLearned sql.NullString
	streakWrong  int
	intervalDays int
}

// scanQueueItem 从一行结果构造 QueueItem。
func scanQueueItem(rows rowScanner) (QueueItem, error) {
	var it QueueItem
	var f queueFields
	var ch, status string
	var semID int64
	var hid int64
	var level int

	err := rows.Scan(&hid, &ch, &semID, &level, &status,
		&f.dueOn, &f.lastReviewOn, &f.firstLearned, &f.streakWrong, &f.intervalDays)
	if err != nil {
		return it, fmt.Errorf("扫描队列行: %w", err)
	}

	it.HanziID = hid
	it.Ch = ch
	it.SemesterID = semID
	it.Level = level
	it.Status = status
	it.DueOn = day.Day(f.dueOn.String)
	it.row = f
	return it, nil
}

// state 重建该字的调度状态，供 Reason 使用。
func (it QueueItem) state() schedule.State {
	f := it.row
	return schedule.State{
		Level:        it.Level,
		StreakWrong:  f.streakWrong,
		HasHistory:   f.firstLearned.Valid,
		FirstLearned: day.Day(f.firstLearned.String),
		LastReviewOn: day.Day(f.lastReviewOn.String),
	}
}
