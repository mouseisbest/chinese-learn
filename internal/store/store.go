// Package store 集中所有数据库访问。
//
// 上层的算法包（day / schedule / hanzi）是纯函数，这一层只负责读写，
// 两者之间的分界线是刻意的：调度逻辑可以被穷举测试，不受数据库细节影响。
package store

import (
	"database/sql"
	_ "embed"
	"errors"
	"fmt"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

//go:embed schema.sql
var schemaSQL string

// Store 是数据库句柄。
type Store struct {
	db *sql.DB
	// loc 用于把墙钟时间换算成学习日，由调用方注入以便测试。
	loc *time.Location
	// cutoff 是日切点，凌晨之前算作前一天。
	cutoff time.Duration
}

// Options 是打开数据库的参数。
type Options struct {
	// Path 是数据库文件路径。传 ":memory:" 可用于测试。
	Path string
	// Location 用于学习日换算，nil 时用系统本地时区。
	Location *time.Location
	// Cutoff 是日切点，零值时用 day.DefaultCutoff（凌晨 4 点）。
	Cutoff time.Duration
}

// ErrNotFound 表示查询的目标不存在。
var ErrNotFound = errors.New("未找到")

// Open 打开（必要时创建）数据库并应用结构。
func Open(opt Options) (*Store, error) {
	if opt.Path == "" {
		return nil, errors.New("数据库路径不能为空")
	}
	if opt.Location == nil {
		opt.Location = time.Local
	}

	// _txlock=immediate 让事务一开始就取得写锁，避免 WAL 模式下
	// 「先读后升级为写」导致的 SQLITE_BUSY_SNAPSHOT。
	dsn := fmt.Sprintf("file:%s?_busy_timeout=5000&_journal_mode=WAL"+
		"&_foreign_keys=on&_synchronous=NORMAL&_txlock=immediate", opt.Path)

	db, err := sql.Open("sqlite3", dsn)
	if err != nil {
		return nil, fmt.Errorf("打开数据库: %w", err)
	}

	// 家用场景并发极低，串行化访问可彻底消除 SQLITE_BUSY，
	// 且避免「database is locked」在导入或复习时偶发。
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	db.SetConnMaxLifetime(0)

	if err := db.Ping(); err != nil {
		db.Close()
		return nil, fmt.Errorf("连接数据库: %w", err)
	}

	s := &Store{db: db, loc: opt.Location, cutoff: opt.Cutoff}
	if err := s.migrate(); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

// Close 关闭数据库。
func (s *Store) Close() error { return s.db.Close() }

// DB 暴露底层句柄，供测试使用。
func (s *Store) DB() *sql.DB { return s.db }

// Location 返回用于学习日换算的时区。
func (s *Store) Location() *time.Location { return s.loc }

// Cutoff 返回日切点。
func (s *Store) Cutoff() time.Duration { return s.cutoff }

// SetCutoff 更新日切点（设置页改「日切点小时」时调用）。
func (s *Store) SetCutoff(d time.Duration) { s.cutoff = d }

// withTx 在一个事务里执行 fn，出错自动回滚。
func (s *Store) withTx(fn func(*sql.Tx) error) error {
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("开始事务: %w", err)
	}
	if err := fn(tx); err != nil {
		tx.Rollback()
		return err
	}
	return tx.Commit()
}
