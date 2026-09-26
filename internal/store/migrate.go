package store

import (
	"database/sql"
	"fmt"

	"chinese-learn/internal/hanzi"
)

// schemaVersion 是当前结构版本。
//
// 改结构时递增，并在 migrate 里补一段对应版本的迁移逻辑。
//
//	1 → 初始版本
//	2 → hanzi 表增加 pinyin / pinyin_all 两列（拼音翻牌阶段要用）
const schemaVersion = 2

// migrate 建表并做版本升级。
//
// 用 SQLite 的 user_version 记录版本，避免额外的元数据表。
func (s *Store) migrate() error {
	var ver int
	if err := s.db.QueryRow("PRAGMA user_version").Scan(&ver); err != nil {
		return fmt.Errorf("读取结构版本: %w", err)
	}

	switch {
	case ver == 0:
		// 全新数据库：直接建表。
		if _, err := s.db.Exec(schemaSQL); err != nil {
			return fmt.Errorf("初始化数据库结构: %w", err)
		}
		if err := s.setVersion(schemaVersion); err != nil {
			return err
		}
		// 新库的 schema 已包含拼音列，但还没有数据，无需回填。
		return nil

	case ver > schemaVersion:
		return fmt.Errorf("数据库结构版本 %d 高于程序支持的 %d，请升级程序", ver, schemaVersion)
	}

	// 逐版本升级，每一步都在事务里完成。
	if ver < 2 {
		if err := s.migrateTo2(); err != nil {
			return fmt.Errorf("升级到版本 2: %w", err)
		}
	}

	return s.setVersion(schemaVersion)
}

func (s *Store) setVersion(v int) error {
	if _, err := s.db.Exec(fmt.Sprintf("PRAGMA user_version = %d", v)); err != nil {
		return fmt.Errorf("写入结构版本: %w", err)
	}
	return nil
}

// migrateTo2 给 hanzi 表加拼音列，并回填已有字的读音。
//
// 回填在 Go 里做而不是写 SQL：拼音是查字典算出来的，
// SQLite 里没有这个能力。
func (s *Store) migrateTo2() error {
	// 加列。SQLite 不支持 ADD COLUMN IF NOT EXISTS，
	// 所以先查一下，避免重复执行时报错。
	hasPinyin, err := s.columnExists("hanzi", "pinyin")
	if err != nil {
		return err
	}
	if !hasPinyin {
		if _, err := s.db.Exec(
			`ALTER TABLE hanzi ADD COLUMN pinyin TEXT NOT NULL DEFAULT ''`); err != nil {
			return fmt.Errorf("添加 pinyin 列: %w", err)
		}
	}

	hasAll, err := s.columnExists("hanzi", "pinyin_all")
	if err != nil {
		return err
	}
	if !hasAll {
		if _, err := s.db.Exec(
			`ALTER TABLE hanzi ADD COLUMN pinyin_all TEXT NOT NULL DEFAULT ''`); err != nil {
			return fmt.Errorf("添加 pinyin_all 列: %w", err)
		}
	}

	return s.backfillPinyin()
}

// backfillPinyin 给所有还没拼音的字补上读音。
func (s *Store) backfillPinyin() error {
	rows, err := s.db.Query(`SELECT id, ch FROM hanzi WHERE pinyin = ''`)
	if err != nil {
		return err
	}

	type item struct {
		id    int64
		ch    string
		pin   string
		pinAl string
	}
	var items []item
	for rows.Next() {
		var it item
		if err := rows.Scan(&it.id, &it.ch); err != nil {
			rows.Close()
			return err
		}
		p := hanzi.LookupPinyin(it.ch)
		it.pin, it.pinAl = p.Primary, p.All
		items = append(items, it)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	if len(items) == 0 {
		return nil
	}

	return s.withTx(func(tx *sql.Tx) error {
		for _, it := range items {
			if _, err := tx.Exec(
				`UPDATE hanzi SET pinyin = ?, pinyin_all = ? WHERE id = ?`,
				it.pin, it.pinAl, it.id); err != nil {
				return err
			}
		}
		return nil
	})
}

// columnExists 判断某张表是否有某一列。
func (s *Store) columnExists(table, column string) (bool, error) {
	rows, err := s.db.Query(fmt.Sprintf(`PRAGMA table_info(%s)`, table))
	if err != nil {
		return false, err
	}
	defer rows.Close()

	for rows.Next() {
		var (
			cid     int
			name    string
			ctype   string
			notNull int
			dflt    sql.NullString
			pk      int
		)
		if err := rows.Scan(&cid, &name, &ctype, &notNull, &dflt, &pk); err != nil {
			return false, err
		}
		if name == column {
			return true, nil
		}
	}
	return false, rows.Err()
}
