package store

import (
	"fmt"
)

// schemaVersion 是当前结构版本，对应 schema.sql。
// 将来改结构时递增，并在 migrate 里补一段迁移逻辑。
const schemaVersion = 1

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
		if _, err := s.db.Exec(fmt.Sprintf("PRAGMA user_version = %d", schemaVersion)); err != nil {
			return fmt.Errorf("写入结构版本: %w", err)
		}
	case ver > schemaVersion:
		return fmt.Errorf("数据库结构版本 %d 高于程序支持的 %d，请升级程序", ver, schemaVersion)
	default:
		// 将来在这里按版本号顺序补迁移：
		//   if ver < 2 { ... }
	}
	return nil
}
