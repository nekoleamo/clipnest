// Package db 是 SQLite 连接、schema 与迁移。
package db

import (
	"database/sql"
	"fmt"

	_ "modernc.org/sqlite" // 纯 Go 驱动，FTS5 内置
)

// Open 打开库文件并按需迁移；目录由调用方保证存在。
func Open(path string) (*sql.DB, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("打开数据库 %s: %w", path, err)
	}
	// 单连接：SQLite 写串行，也避免 :memory: 被拆成多个库
	db.SetMaxOpenConns(1)
	if err := tune(db); err != nil {
		db.Close()
		return nil, err
	}
	if err := Migrate(db); err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}

// OpenMemory 是测试用的内存库（已迁移）。
func OpenMemory() (*sql.DB, error) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if err := tune(db); err != nil {
		db.Close()
		return nil, err
	}
	if err := Migrate(db); err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}

// tune 设连接级 PRAGMA。
func tune(db *sql.DB) error {
	for _, pragma := range []string{
		"PRAGMA foreign_keys = ON",
		"PRAGMA busy_timeout = 5000",
		"PRAGMA journal_mode = WAL",
	} {
		if _, err := db.Exec(pragma); err != nil {
			return fmt.Errorf("%s: %w", pragma, err)
		}
	}
	return nil
}
