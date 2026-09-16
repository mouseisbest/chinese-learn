package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"chinese-learn/internal/day"
	"chinese-learn/internal/schedule"
)

// SubmitRequest 是一次复习提交。
type SubmitRequest struct {
	ChildID   int64
	HanziID   int64
	Result    schedule.Result
	SessionID string
	LatencyMS int
	// Now 是提交时刻对应的学习日，由调用方注入以便测试。
	Now day.Day
}

// SubmitOutcome 是提交后的结果。
type SubmitOutcome struct {
	Level       int
	IntervalDay int
	DueOn       day.Day
	Bound       string
	Lapsed      bool
	// FirstTime 表示这是该字的首次学习。
	FirstTime bool
}

// ErrDuplicate 表示这个字在本批次里已经提交过了（连点或双标签页）。
var ErrDuplicate = errors.New("这个字已经答过了")

// stateSnapshot 是撤销用的前像。
type stateSnapshot struct {
	Level          int    `json:"level"`
	IntervalDays   int    `json:"interval_days"`
	DueOn          string `json:"due_on"`
	LastReviewOn   string `json:"last_review_on"`
	FirstLearnedOn string `json:"first_learned_on"`
	CorrectCount   int    `json:"correct_count"`
	WrongCount     int    `json:"wrong_count"`
	StreakCorrect  int    `json:"streak_correct"`
	StreakWrong    int    `json:"streak_wrong"`
	Lapses         int    `json:"lapses"`
	Status         string `json:"status"`
}

// SubmitReview 记录一次复习，并推进该字的调度状态。
//
// 整个过程在一个事务里：读状态 → 算新状态 → 写状态 → 记流水 → 记配额。
// 任一步失败则整体回滚，不会留下「状态变了但没记流水」这类半截数据。
func (s *Store) SubmitReview(p schedule.Params, req SubmitRequest) (SubmitOutcome, error) {
	var out SubmitOutcome
	if req.Now.IsZero() {
		return out, errors.New("缺少学习日")
	}

	err := s.withTx(func(tx *sql.Tx) error {
		// ---- 读当前状态 ----
		var (
			snap    stateSnapshot
			hid     int64
			dueOn   sql.NullString
			lastRev sql.NullString
			firstLr sql.NullString
			status  string
		)
		err := tx.QueryRow(
			`SELECT rs.hanzi_id, rs.level, rs.interval_days, rs.due_on, rs.last_review_on,
			        rs.first_learned_on, rs.correct_count, rs.wrong_count,
			        rs.streak_correct, rs.streak_wrong, rs.lapses, h.status
			 FROM review_state rs
			 JOIN hanzi h ON h.id = rs.hanzi_id
			 WHERE rs.hanzi_id = ? AND rs.child_id = ?`,
			req.HanziID, req.ChildID).
			Scan(&hid, &snap.Level, &snap.IntervalDays, &dueOn, &lastRev,
				&firstLr, &snap.CorrectCount, &snap.WrongCount,
				&snap.StreakCorrect, &snap.StreakWrong, &snap.Lapses, &status)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return fmt.Errorf("读取复习状态: %w", err)
		}
		snap.DueOn = dueOn.String
		snap.LastReviewOn = lastRev.String
		snap.FirstLearnedOn = firstLr.String
		snap.Status = status

		if status == "suspended" {
			return errors.New("这个字已被暂停")
		}

		// 防连点：同一批次里同一个字只能提交一次。
		if req.SessionID != "" {
			var n int
			if err := tx.QueryRow(
				`SELECT COUNT(*) FROM review_log WHERE session_id = ? AND hanzi_id = ?`,
				req.SessionID, req.HanziID).Scan(&n); err != nil {
				return err
			}
			if n > 0 {
				return ErrDuplicate
			}
		}

		// ---- 构造调度输入 ----
		state := schedule.State{
			Level:         snap.Level,
			StreakCorrect: snap.StreakCorrect,
			StreakWrong:   snap.StreakWrong,
			HasHistory:    firstLr.Valid,
			FirstLearned:  day.Day(firstLr.String),
			LastReviewOn:  day.Day(lastRev.String),
		}
		out.FirstTime = !firstLr.Valid
		if out.FirstTime {
			// 首次学习：今天成为锚点基准日。
			state = schedule.BeginFirst(req.Now)
		}

		reason := schedule.Reason(p, state, req.Now)

		// ---- 计算新状态 ----
		next := schedule.Evaluate(p, state, req.Result, req.Now)

		newStreakCorrect, newStreakWrong := snap.StreakCorrect, snap.StreakWrong
		if req.Result == schedule.Known {
			newStreakCorrect++
			newStreakWrong = 0
		} else {
			newStreakWrong++
			newStreakCorrect = 0
		}
		newCorrect, newWrong := snap.CorrectCount, snap.WrongCount
		if req.Result == schedule.Known {
			newCorrect++
		} else {
			newWrong++
		}
		newLapses := snap.Lapses
		if next.Lapsed {
			newLapses++
		}

		firstLearned := snap.FirstLearnedOn
		if out.FirstTime {
			firstLearned = string(req.Now)
		}

		newStatus := deriveStatus(next.Level, status)

		// ---- 写回状态 ----
		if _, err := tx.Exec(
			`UPDATE review_state SET
			   level = ?, interval_days = ?, due_on = ?, last_review_on = ?,
			   first_learned_on = ?, correct_count = ?, wrong_count = ?,
			   streak_correct = ?, streak_wrong = ?, lapses = ?,
			   updated_at = datetime('now','localtime')
			 WHERE hanzi_id = ?`,
			next.Level, next.Interval, string(next.DueOn), string(req.Now),
			firstLearned, newCorrect, newWrong,
			newStreakCorrect, newStreakWrong, newLapses, req.HanziID); err != nil {
			return fmt.Errorf("更新复习状态: %w", err)
		}

		if _, err := tx.Exec(
			`UPDATE hanzi SET status = ?, first_learned_on = COALESCE(first_learned_on, ?)
			 WHERE id = ?`,
			newStatus, string(req.Now), req.HanziID); err != nil {
			return fmt.Errorf("更新汉字状态: %w", err)
		}

		// ---- 记流水 ----
		snapJSON, err := json.Marshal(snap)
		if err != nil {
			return err
		}
		elapsed := 0
		if !state.LastReviewOn.IsZero() {
			elapsed = req.Now.Sub(state.LastReviewOn)
		}

		var sessionID any
		if req.SessionID != "" {
			sessionID = req.SessionID
		}

		if _, err := tx.Exec(
			`INSERT INTO review_log
			   (child_id, hanzi_id, session_id, reviewed_on, result, reason,
			    level_before, level_after, interval_after, due_before, due_after,
			    elapsed_days, latency_ms, state_before_json)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			req.ChildID, req.HanziID, sessionID, string(req.Now), req.Result.String(),
			reason, snap.Level, next.Level, next.Interval,
			nullIfEmpty(snap.DueOn), nullIfEmpty(string(next.DueOn)),
			elapsed, req.LatencyMS, string(snapJSON)); err != nil {
			return fmt.Errorf("记录复习流水: %w", err)
		}

		// ---- 累加当日配额 ----
		newServedInc := 0
		if out.FirstTime {
			newServedInc = 1
		}
		knownInc, unknownInc := 0, 0
		if req.Result == schedule.Known {
			knownInc = 1
		} else {
			unknownInc = 1
		}

		if _, err := tx.Exec(
			`INSERT INTO day_session (child_id, day, new_served, answered, known, unknown)
			 VALUES (?, ?, ?, 1, ?, ?)
			 ON CONFLICT(child_id, day) DO UPDATE SET
			   new_served = new_served + ?,
			   answered   = answered + 1,
			   known      = known + ?,
			   unknown    = unknown + ?`,
			req.ChildID, string(req.Now), newServedInc, knownInc, unknownInc,
			newServedInc, knownInc, unknownInc); err != nil {
			return fmt.Errorf("累加当日配额: %w", err)
		}

		out = SubmitOutcome{
			Level:       next.Level,
			IntervalDay: next.Interval,
			DueOn:       next.DueOn,
			Bound:       next.Bound,
			Lapsed:      next.Lapsed,
			FirstTime:   out.FirstTime,
		}
		return nil
	})
	return out, err
}

// UndoLast 撤销某孩子最近一次复习，把状态复原。
//
// 返回被撤销的字 ID 和那次判定结果（"known"/"unknown"），
// 供前端回退本地计数。撤销后该字会重新回到今天的队列。
func (s *Store) UndoLast(childID int64) (int64, string, error) {
	var undone int64
	var undoneResult string
	err := s.withTx(func(tx *sql.Tx) error {
		var (
			logID   int64
			hanziID int64
			dayStr  string
			result  string
			snapRaw string
		)
		err := tx.QueryRow(
			`SELECT id, hanzi_id, reviewed_on, result, state_before_json
			 FROM review_log WHERE child_id = ?
			 ORDER BY id DESC LIMIT 1`, childID).
			Scan(&logID, &hanziID, &dayStr, &result, &snapRaw)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}

		var snap stateSnapshot
		if err := json.Unmarshal([]byte(snapRaw), &snap); err != nil {
			return fmt.Errorf("解析状态快照: %w", err)
		}

		// 复原状态。
		if _, err := tx.Exec(
			`UPDATE review_state SET
			   level = ?, interval_days = ?, due_on = ?, last_review_on = ?,
			   first_learned_on = ?, correct_count = ?, wrong_count = ?,
			   streak_correct = ?, streak_wrong = ?, lapses = ?,
			   updated_at = datetime('now','localtime')
			 WHERE hanzi_id = ?`,
			snap.Level, snap.IntervalDays, nullIfEmpty(snap.DueOn), nullIfEmpty(snap.LastReviewOn),
			nullIfEmpty(snap.FirstLearnedOn), snap.CorrectCount, snap.WrongCount,
			snap.StreakCorrect, snap.StreakWrong, snap.Lapses, hanziID); err != nil {
			return err
		}
		if _, err := tx.Exec(
			`UPDATE hanzi SET status = ?, first_learned_on = ? WHERE id = ?`,
			snap.Status, nullIfEmpty(snap.FirstLearnedOn), hanziID); err != nil {
			return err
		}

		if _, err := tx.Exec(`DELETE FROM review_log WHERE id = ?`, logID); err != nil {
			return err
		}

		// 回滚当日配额。
		knownInc, unknownInc := 0, 0
		if result == "known" {
			knownInc = 1
		} else {
			unknownInc = 1
		}
		// 首次学习的标志是「前像里没有上次复习日」。
		newServedInc := 0
		if snap.LastReviewOn == "" {
			newServedInc = 1
		}
		if _, err := tx.Exec(
			`UPDATE day_session SET
			   new_served = MAX(0, new_served - ?),
			   answered   = MAX(0, answered - 1),
			   known      = MAX(0, known - ?),
			   unknown    = MAX(0, unknown - ?)
			 WHERE child_id = ? AND day = ?`,
			newServedInc, knownInc, unknownInc, childID, dayStr); err != nil {
			return err
		}

		undone = hanziID
		undoneResult = result
		return nil
	})
	return undone, undoneResult, err
}

// RecentMistakes 返回某天答错的字，用于小结页「再看一眼」。
func (s *Store) RecentMistakes(childID int64, d day.Day) ([]string, error) {
	rows, err := s.db.Query(
		`SELECT DISTINCT h.ch
		 FROM review_log rl
		 JOIN hanzi h ON h.id = rl.hanzi_id
		 WHERE rl.child_id = ? AND rl.reviewed_on = ? AND rl.result = 'unknown'
		 ORDER BY h.ch`, childID, string(d))
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []string
	for rows.Next() {
		var ch string
		if err := rows.Scan(&ch); err != nil {
			return nil, err
		}
		out = append(out, ch)
	}
	return out, rows.Err()
}

// DayStats 是某一天的完成情况。
type DayStats struct {
	Answered int
	Known    int
	Unknown  int
}

// GetDayStats 读取某天的完成情况。
func (s *Store) GetDayStats(childID int64, d day.Day) (DayStats, error) {
	var st DayStats
	err := s.db.QueryRow(
		`SELECT answered, known, unknown FROM day_session WHERE child_id = ? AND day = ?`,
		childID, string(d)).Scan(&st.Answered, &st.Known, &st.Unknown)
	if errors.Is(err, sql.ErrNoRows) {
		return st, nil
	}
	return st, err
}

// deriveStatus 由等级推导展示用的状态。
//
// 注意 status 只影响展示与统计，不影响调度——
// 即使 marked 为 mastered，该字仍会按长间隔被复习到。
func deriveStatus(level int, current string) string {
	if current == "suspended" {
		return current
	}
	switch {
	case level >= MasteryLevel:
		return "mastered"
	case level >= 3:
		return "reviewing"
	default:
		return "learning"
	}
}

func nullIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}
