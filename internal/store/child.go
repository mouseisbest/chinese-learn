package store

import (
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"time"

	"chinese-learn/internal/day"
)

// Child 是一个孩子的档案。
type Child struct {
	ID        int64
	Name      string
	BirthYear sql.NullInt64
	Archived  bool
}

// Semester 是一个学期，例如「一年级上册」。
type Semester struct {
	ID        int64
	ChildID   int64
	Name      string
	SortOrder int
	IsCurrent bool
}

// 默认值和设置项的键名。
const (
	DefaultChildName    = "宝宝"
	DefaultSemesterName = "一年级上册"

	KeyDailyNewCap    = "daily_new_cap"
	KeyDailyReviewCap = "daily_review_cap"
	KeyHeaviestThresh = "heaviest_thresh"
	KeyDayCutoffHour  = "day_cutoff_hour"
)

// 设置项的默认值。
const (
	DefaultDailyNewCap    = 5
	DefaultDailyReviewCap = 20
	DefaultHeaviestThresh = 15
	DefaultDayCutoffHour  = 4
)

// MaxSettingValue 是数量类设置的防呆上限。
//
// 它不构成实际约束——一学期几百个字，远用不到这个量级——
// 但能挡住手滑输入 999999999 这类值：那会让队列查询去尝试
// 返回天文数字的行，页面直接卡死。
const MaxSettingValue = 10000

// EnsureSeed 保证至少有一个孩子和一个当前学期。
//
// 首次启动时自动建好「宝宝」和「一年级上册」，用户打开页面就能直接用，
// 而不是先看到空的「请添加孩子」页。
func (s *Store) EnsureSeed() error {
	var n int
	if err := s.db.QueryRow("SELECT COUNT(*) FROM child").Scan(&n); err != nil {
		return fmt.Errorf("统计孩子数量: %w", err)
	}
	if n > 0 {
		// 已有人，但可能缺当前学期（例如手工删过），补一下。
		return s.ensureCurrentSemester()
	}

	return s.withTx(func(tx *sql.Tx) error {
		res, err := tx.Exec(
			`INSERT INTO child (name, created_at) VALUES (?, datetime('now','localtime'))`,
			DefaultChildName)
		if err != nil {
			return fmt.Errorf("创建默认孩子: %w", err)
		}
		childID, err := res.LastInsertId()
		if err != nil {
			return err
		}

		if _, err := tx.Exec(
			`INSERT INTO semester (child_id, name, sort_order, is_current) VALUES (?, ?, 0, 1)`,
			childID, DefaultSemesterName); err != nil {
			return fmt.Errorf("创建默认学期: %w", err)
		}
		return nil
	})
}

// ensureCurrentSemester 确保每个孩子都有一个当前学期。
func (s *Store) ensureCurrentSemester() error {
	children, err := s.ListChildren()
	if err != nil {
		return err
	}
	for _, c := range children {
		var n int
		err := s.db.QueryRow(
			`SELECT COUNT(*) FROM semester WHERE child_id = ? AND is_current = 1`,
			c.ID).Scan(&n)
		if err != nil {
			return err
		}
		if n > 0 {
			continue
		}
		// 没有当前学期：优先把第一个学期设为当前，没有学期就建一个。
		var firstID int64
		err = s.db.QueryRow(
			`SELECT id FROM semester WHERE child_id = ? ORDER BY sort_order, id LIMIT 1`,
			c.ID).Scan(&firstID)
		switch {
		case err == nil:
			if _, err := s.db.Exec(
				`UPDATE semester SET is_current = 1 WHERE id = ?`, firstID); err != nil {
				return err
			}
		case errors.Is(err, sql.ErrNoRows):
			if _, err := s.db.Exec(
				`INSERT INTO semester (child_id, name, sort_order, is_current) VALUES (?, ?, 0, 1)`,
				c.ID, DefaultSemesterName); err != nil {
				return err
			}
		default:
			return err
		}
	}
	return nil
}

// ListChildren 返回未归档的孩子。
func (s *Store) ListChildren() ([]Child, error) {
	rows, err := s.db.Query(
		`SELECT id, name, birth_year, archived FROM child WHERE archived = 0 ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Child
	for rows.Next() {
		var c Child
		if err := rows.Scan(&c.ID, &c.Name, &c.BirthYear, &c.Archived); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// GetChild 按 ID 取孩子。
func (s *Store) GetChild(id int64) (Child, error) {
	var c Child
	err := s.db.QueryRow(
		`SELECT id, name, birth_year, archived FROM child WHERE id = ?`, id).
		Scan(&c.ID, &c.Name, &c.BirthYear, &c.Archived)
	if errors.Is(err, sql.ErrNoRows) {
		return c, ErrNotFound
	}
	return c, err
}

// CreateChild 新建一个孩子，并自动建一个当前学期。
func (s *Store) CreateChild(name string) (int64, error) {
	if name == "" {
		return 0, errors.New("孩子姓名不能为空")
	}
	var id int64
	err := s.withTx(func(tx *sql.Tx) error {
		res, err := tx.Exec(
			`INSERT INTO child (name, created_at) VALUES (?, datetime('now','localtime'))`, name)
		if err != nil {
			return err
		}
		id, err = res.LastInsertId()
		if err != nil {
			return err
		}
		_, err = tx.Exec(
			`INSERT INTO semester (child_id, name, sort_order, is_current) VALUES (?, ?, 0, 1)`,
			id, DefaultSemesterName)
		return err
	})
	return id, err
}

// ListSemesters 返回某个孩子的所有学期。
func (s *Store) ListSemesters(childID int64) ([]Semester, error) {
	rows, err := s.db.Query(
		`SELECT id, child_id, name, sort_order, is_current
		 FROM semester WHERE child_id = ? ORDER BY sort_order, id`, childID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Semester
	for rows.Next() {
		var m Semester
		if err := rows.Scan(&m.ID, &m.ChildID, &m.Name, &m.SortOrder, &m.IsCurrent); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// CurrentSemester 返回某孩子的当前学期。没有学期时返回 ErrNotFound。
func (s *Store) CurrentSemester(childID int64) (Semester, error) {
	var m Semester
	err := s.db.QueryRow(
		`SELECT id, child_id, name, sort_order, is_current
		 FROM semester WHERE child_id = ? AND is_current = 1`, childID).
		Scan(&m.ID, &m.ChildID, &m.Name, &m.SortOrder, &m.IsCurrent)
	if errors.Is(err, sql.ErrNoRows) {
		return m, ErrNotFound
	}
	return m, err
}

// CreateSemester 新建学期。makeCurrent 为 true 时同时切为当前学期。
func (s *Store) CreateSemester(childID int64, name string, makeCurrent bool) (int64, error) {
	if name == "" {
		return 0, errors.New("学期名称不能为空")
	}

	var id int64
	err := s.withTx(func(tx *sql.Tx) error {
		var nextOrder int
		if err := tx.QueryRow(
			`SELECT COALESCE(MAX(sort_order), -1) + 1 FROM semester WHERE child_id = ?`,
			childID).Scan(&nextOrder); err != nil {
			return err
		}

		// 第一个学期自动成为当前学期。
		var count int
		if err := tx.QueryRow(
			`SELECT COUNT(*) FROM semester WHERE child_id = ?`, childID).Scan(&count); err != nil {
			return err
		}
		current := makeCurrent || count == 0

		if current {
			// 唯一索引保证只有一个 current，先清掉旧的。
			if _, err := tx.Exec(
				`UPDATE semester SET is_current = 0 WHERE child_id = ?`, childID); err != nil {
				return err
			}
		}

		res, err := tx.Exec(
			`INSERT INTO semester (child_id, name, sort_order, is_current) VALUES (?, ?, ?, ?)`,
			childID, name, nextOrder, boolToInt(current))
		if err != nil {
			return err
		}
		id, err = res.LastInsertId()
		return err
	})
	return id, err
}

// SetCurrentSemester 把一个学期切为当前学期。
//
// 切换后：新字只从新学期放出，但其他学期未掌握的字仍在复习队列里
// ——这正是「升学期后老字继续排、新字受限」的实现。
func (s *Store) SetCurrentSemester(childID, semesterID int64) error {
	return s.withTx(func(tx *sql.Tx) error {
		var owner int64
		err := tx.QueryRow(`SELECT child_id FROM semester WHERE id = ?`, semesterID).Scan(&owner)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		if owner != childID {
			return errors.New("该学期不属于这个孩子")
		}

		if _, err := tx.Exec(
			`UPDATE semester SET is_current = 0 WHERE child_id = ?`, childID); err != nil {
			return err
		}
		_, err = tx.Exec(`UPDATE semester SET is_current = 1 WHERE id = ?`, semesterID)
		return err
	})
}

// RenameSemester 改名。
func (s *Store) RenameSemester(semesterID int64, name string) error {
	if name == "" {
		return errors.New("学期名称不能为空")
	}
	_, err := s.db.Exec(`UPDATE semester SET name = ? WHERE id = ?`, name, semesterID)
	return err
}

// DeleteSemester 删除学期，连带删除其下的字与进度。
func (s *Store) DeleteSemester(semesterID int64) error {
	return s.withTx(func(tx *sql.Tx) error {
		var childID int64
		var isCurrent bool
		err := tx.QueryRow(
			`SELECT child_id, is_current FROM semester WHERE id = ?`, semesterID).
			Scan(&childID, &isCurrent)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}

		if _, err := tx.Exec(`DELETE FROM semester WHERE id = ?`, semesterID); err != nil {
			return err
		}

		// 删掉当前学期后，把剩下的第一个补为当前，避免出现「没有当前学期」。
		if isCurrent {
			var next int64
			err := tx.QueryRow(
				`SELECT id FROM semester WHERE child_id = ? ORDER BY sort_order, id LIMIT 1`,
				childID).Scan(&next)
			if errors.Is(err, sql.ErrNoRows) {
				return nil // 一个学期都不剩，等用户新建
			}
			if err != nil {
				return err
			}
			if _, err := tx.Exec(
				`UPDATE semester SET is_current = 1 WHERE id = ?`, next); err != nil {
				return err
			}
		}
		return nil
	})
}

// ---------------- settings ----------------

// Settings 是某个孩子的可调参数。
type Settings struct {
	DailyNewCap    int
	DailyReviewCap int
	HeaviestThresh int
	DayCutoffHour  int
}

// DefaultSettings 返回默认参数。
func DefaultSettings() Settings {
	return Settings{
		DailyNewCap:    DefaultDailyNewCap,
		DailyReviewCap: DefaultDailyReviewCap,
		HeaviestThresh: DefaultHeaviestThresh,
		DayCutoffHour:  DefaultDayCutoffHour,
	}
}

// Cutoff 把设置里的日切点小时转成 duration。
func (st Settings) Cutoff() time.Duration {
	if st.DayCutoffHour < 0 || st.DayCutoffHour > 23 {
		return day.DefaultCutoff
	}
	return time.Duration(st.DayCutoffHour) * time.Hour
}

// GetSettings 读取设置，缺失的键用默认值补齐。
func (s *Store) GetSettings(childID int64) (Settings, error) {
	out := DefaultSettings()

	rows, err := s.db.Query(`SELECT key, value FROM settings WHERE child_id = ?`, childID)
	if err != nil {
		return out, err
	}
	defer rows.Close()

	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return out, err
		}
		n, err := strconv.Atoi(v)
		if err != nil {
			continue // 忽略坏值，用默认
		}
		switch k {
		case KeyDailyNewCap:
			out.DailyNewCap = clampInt(n, 0, MaxSettingValue)
		case KeyDailyReviewCap:
			out.DailyReviewCap = clampInt(n, 1, MaxSettingValue)
		case KeyHeaviestThresh:
			out.HeaviestThresh = clampInt(n, 0, MaxSettingValue)
		case KeyDayCutoffHour:
			out.DayCutoffHour = clampInt(n, 0, 23)
		}
	}
	return out, rows.Err()
}

// SaveSettings 写入设置。
func (s *Store) SaveSettings(childID int64, st Settings) error {
	pairs := map[string]int{
		KeyDailyNewCap:    st.DailyNewCap,
		KeyDailyReviewCap: st.DailyReviewCap,
		KeyHeaviestThresh: st.HeaviestThresh,
		KeyDayCutoffHour:  st.DayCutoffHour,
	}
	return s.withTx(func(tx *sql.Tx) error {
		for k, v := range pairs {
			if _, err := tx.Exec(
				`INSERT INTO settings (child_id, key, value) VALUES (?, ?, ?)
				 ON CONFLICT(child_id, key) DO UPDATE SET value = excluded.value`,
				childID, k, strconv.Itoa(v)); err != nil {
				return err
			}
		}
		return nil
	})
}

func clampInt(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
