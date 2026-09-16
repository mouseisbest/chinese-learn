package store

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"chinese-learn/internal/hanzi"
)

// ImportPreview 是导入前的预览结果，此时尚未写库。
type ImportPreview struct {
	// Ordered 是抽出的有序去重汉字。
	Ordered []rune
	// TotalRunes 是输入的总字符数。
	TotalRunes int
	// UniqueHanzi 是抽出的唯一汉字数。
	UniqueHanzi int
	// AlreadyExists 是库中已存在、将自动跳过的字数。
	AlreadyExists int
	// ToInsert 是将新增的字数。
	ToInsert int
	// Skipped 是按类别统计的跳过字符数。
	Skipped map[string]int
	// Preview 是前若干个字，供用户核对数据源有没有被正确解析。
	Preview []rune
	// Truncated 表示因为超出上限被截断。
	Truncated bool
	// SHA256 是原始文本的摘要，用于识别重复导入同一份数据源。
	SHA256 string
	// RawBytes 是原始文本的字节数。
	RawBytes int
}

// ImportRequest 是一次导入的请求。
type ImportRequest struct {
	ChildID    int64
	SemesterID int64
	SourceName string
	SourceKind string // paste | upload | manual
	Text       string
}

// PreviewImport 抽取汉字并统计，但不写库。
//
// 这一步是导入流程的关键：数据源的格式无法预期，用户必须先看到
// 「抽出了哪些字、有多少是新的」才能确认导入是否正确。
func (s *Store) PreviewImport(req ImportRequest) (ImportPreview, error) {
	if req.ChildID == 0 {
		return ImportPreview{}, errors.New("缺少孩子 ID")
	}

	ex := hanzi.Extract(req.Text)

	pv := ImportPreview{
		Ordered:     ex.Ordered,
		TotalRunes:  ex.TotalRunes,
		UniqueHanzi: len(ex.Ordered),
		Skipped:     ex.Skipped,
		Truncated:   ex.Truncated,
		RawBytes:    len(req.Text),
	}
	if len(ex.Ordered) > 20 {
		pv.Preview = ex.Ordered[:20]
	} else {
		pv.Preview = ex.Ordered
	}

	sum := sha256.Sum256([]byte(req.Text))
	pv.SHA256 = hex.EncodeToString(sum[:])

	// 统计已存在的字数。
	if len(ex.Ordered) > 0 {
		exists, err := s.countExisting(req.ChildID, ex.Ordered)
		if err != nil {
			return pv, err
		}
		pv.AlreadyExists = exists
	}
	pv.ToInsert = pv.UniqueHanzi - pv.AlreadyExists

	return pv, nil
}

// countExisting 统计给定字中有多少已在库里。
func (s *Store) countExisting(childID int64, runes []rune) (int, error) {
	n := 0
	// 分批查询，避免 SQL 变量数超限（SQLite 默认上限 999）。
	const batch = 500
	for start := 0; start < len(runes); start += batch {
		end := start + batch
		if end > len(runes) {
			end = len(runes)
		}
		chunk := runes[start:end]

		placeholders := strings.Repeat("?,", len(chunk))
		placeholders = placeholders[:len(placeholders)-1]

		args := make([]any, 0, len(chunk)+1)
		args = append(args, childID)
		for _, r := range chunk {
			args = append(args, string(r))
		}

		var cnt int
		q := fmt.Sprintf(
			`SELECT COUNT(*) FROM hanzi WHERE child_id = ? AND ch IN (%s)`, placeholders)
		if err := s.db.QueryRow(q, args...).Scan(&cnt); err != nil {
			return 0, err
		}
		n += cnt
	}
	return n, nil
}

// CommitImport 把预览过的字写入数据库。
//
// 幂等：同一个字重复导入不会产生重复行，只增加 hanzi_source 的关联。
// 整个导入在一个事务里完成，中途失败不会留下半截数据。
func (s *Store) CommitImport(req ImportRequest) (int64, error) {
	if req.ChildID == 0 {
		return 0, errors.New("缺少孩子 ID")
	}
	if req.SemesterID == 0 {
		return 0, errors.New("请先选择学期")
	}
	if req.SourceKind == "" {
		req.SourceKind = "paste"
	}
	if req.SourceName == "" {
		req.SourceName = "未命名"
	}

	ex := hanzi.Extract(req.Text)
	if len(ex.Ordered) == 0 {
		return 0, errors.New("没有从内容中识别出汉字")
	}

	sum := sha256.Sum256([]byte(req.Text))
	skipJSON, _ := json.Marshal(ex.Skipped)

	var batchID int64
	err := s.withTx(func(tx *sql.Tx) error {
		// 校验学期归属，避免把字导入到别人的学期。
		var owner int64
		err := tx.QueryRow(`SELECT child_id FROM semester WHERE id = ?`, req.SemesterID).Scan(&owner)
		if errors.Is(err, sql.ErrNoRows) {
			return errors.New("指定的学期不存在")
		}
		if err != nil {
			return err
		}
		if owner != req.ChildID {
			return errors.New("该学期不属于这个孩子")
		}

		res, err := tx.Exec(
			`INSERT INTO import_batch
			   (child_id, semester_id, source_name, source_kind, raw_sha256, raw_bytes, char_count, skip_json)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			req.ChildID, req.SemesterID, req.SourceName, req.SourceKind,
			hex.EncodeToString(sum[:]), len(req.Text), len(ex.Ordered), string(skipJSON))
		if err != nil {
			return fmt.Errorf("创建导入批次: %w", err)
		}
		batchID, err = res.LastInsertId()
		if err != nil {
			return err
		}

		// 新字的 seq 从当前学期已有最大值之后接续，
		// 这样多次导入能保持「先导入的先学」的顺序。
		var baseSeq int
		if err := tx.QueryRow(
			`SELECT COALESCE(MAX(seq), -1) + 1 FROM hanzi WHERE semester_id = ?`,
			req.SemesterID).Scan(&baseSeq); err != nil {
			return err
		}

		for i, r := range ex.Ordered {
			ch := string(r)

			// 幂等核心：已存在则跳过，不报错。
			res, err := tx.Exec(
				`INSERT INTO hanzi (child_id, ch, codepoint, seq, semester_id, status)
				 VALUES (?, ?, ?, ?, ?, 'new')
				 ON CONFLICT(child_id, ch) DO NOTHING`,
				req.ChildID, ch, int(r), baseSeq+i, req.SemesterID)
			if err != nil {
				return fmt.Errorf("写入汉字 %s: %w", ch, err)
			}

			var hanziID int64
			if n, _ := res.RowsAffected(); n > 0 {
				// 新插入的字：建立调度状态和批次关联。
				hanziID, err = res.LastInsertId()
				if err != nil {
					return err
				}
				if _, err := tx.Exec(
					`INSERT INTO review_state (hanzi_id, child_id, level, due_on)
					 VALUES (?, ?, 0, NULL)`,
					hanziID, req.ChildID); err != nil {
					return fmt.Errorf("初始化复习状态: %w", err)
				}
			} else {
				// 已存在：取出 ID，稍后只更新批次关联。
				if err := tx.QueryRow(
					`SELECT id FROM hanzi WHERE child_id = ? AND ch = ?`,
					req.ChildID, ch).Scan(&hanziID); err != nil {
					return err
				}
			}

			if _, err := tx.Exec(
				`INSERT INTO hanzi_source (hanzi_id, batch_id, seen_count)
				 VALUES (?, ?, 1)
				 ON CONFLICT(hanzi_id, batch_id) DO UPDATE SET seen_count = seen_count + 1`,
				hanziID, batchID); err != nil {
				return fmt.Errorf("记录字与批次的关联: %w", err)
			}
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	return batchID, nil
}

// BatchInfo 是导入批次的信息。
type BatchInfo struct {
	ID         int64
	SemesterID int64
	SourceName string
	SourceKind string
	CharCount  int
	CreatedAt  string
}

// ListBatches 返回某学期的导入批次。
func (s *Store) ListBatches(semesterID int64) ([]BatchInfo, error) {
	rows, err := s.db.Query(
		`SELECT id, semester_id, source_name, source_kind, char_count, created_at
		 FROM import_batch WHERE semester_id = ? ORDER BY created_at DESC, id DESC`,
		semesterID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []BatchInfo
	for rows.Next() {
		var b BatchInfo
		if err := rows.Scan(&b.ID, &b.SemesterID, &b.SourceName, &b.SourceKind,
			&b.CharCount, &b.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

// DeleteBatch 删除一个批次。字本身保留（可能被别的批次引用），
// 只解除关联；若某个字因此不再属于任何批次，则把它一并删掉。
func (s *Store) DeleteBatch(batchID int64) (int, error) {
	deleted := 0
	err := s.withTx(func(tx *sql.Tx) error {
		rows, err := tx.Query(
			`SELECT hanzi_id FROM hanzi_source WHERE batch_id = ?`, batchID)
		if err != nil {
			return err
		}
		var ids []int64
		for rows.Next() {
			var id int64
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return err
			}
			ids = append(ids, id)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}

		if _, err := tx.Exec(`DELETE FROM import_batch WHERE id = ?`, batchID); err != nil {
			return err
		}

		for _, id := range ids {
			var remaining int
			if err := tx.QueryRow(
				`SELECT COUNT(*) FROM hanzi_source WHERE hanzi_id = ?`, id).Scan(&remaining); err != nil {
				return err
			}
			if remaining > 0 {
				continue // 还被别的批次引用，保留
			}
			// 未被任何批次引用：只在从未学过时才删除，
			// 有学习记录的字保留，避免误删孩子的进度。
			res, err := tx.Exec(
				`DELETE FROM hanzi WHERE id = ? AND status = 'new'
				   AND NOT EXISTS (SELECT 1 FROM review_log WHERE hanzi_id = ?)`,
				id, id)
			if err != nil {
				return err
			}
			if n, _ := res.RowsAffected(); n > 0 {
				deleted++
			}
		}
		return nil
	})
	return deleted, err
}
