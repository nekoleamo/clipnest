// 分组：items.group_id 指向这里，同步按名字对齐（id 是各自的本地自增值，不能照搬）。
package db

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// Group 是一个分组。
type Group struct {
	ID   int64
	Name string
	Sort int64
}

// ListGroups 按 sort、name 顺序列出全部分组。
func ListGroups(db *sql.DB) ([]Group, error) {
	rows, err := db.Query("SELECT id, name, sort FROM groups ORDER BY sort, name")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Group
	for rows.Next() {
		var g Group
		if err := rows.Scan(&g.ID, &g.Name, &g.Sort); err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

// EnsureGroup 按名字取分组，没有就建一个，返回 id（同步合并要用）。
func EnsureGroup(db *sql.DB, name string) (int64, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return 0, errors.New("分组名不能为空")
	}
	var id int64
	err := db.QueryRow("SELECT id FROM groups WHERE name = ?", name).Scan(&id)
	if err == nil {
		return id, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return 0, err
	}
	res, err := db.Exec("INSERT INTO groups(name) VALUES (?)", name)
	if err != nil {
		// 并发插入撞唯一键：再读一次
		if scanErr := db.QueryRow("SELECT id FROM groups WHERE name = ?", name).Scan(&id); scanErr == nil {
			return id, nil
		}
		return 0, fmt.Errorf("建分组失败：%w", err)
	}
	return res.LastInsertId()
}

// RenameGroup 改分组名；重名会报错（调用方给用户提示）。
func RenameGroup(db *sql.DB, id int64, name string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return errors.New("分组名不能为空")
	}
	_, err := db.Exec("UPDATE groups SET name = ? WHERE id = ?", name, id)
	if err != nil {
		return fmt.Errorf("改分组名失败：%w", err)
	}
	return nil
}

// DeleteGroup 删分组；组内条目变成「未分组」（外键 ON DELETE SET NULL）。
func DeleteGroup(db *sql.DB, id int64) error {
	if _, err := db.Exec("DELETE FROM groups WHERE id = ?", id); err != nil {
		return fmt.Errorf("删分组失败：%w", err)
	}
	return nil
}

// GroupCounts 返回每个分组里的条目数（不含 0 条的组）。
func GroupCounts(db *sql.DB) (map[int64]int, error) {
	rows, err := db.Query("SELECT group_id, COUNT(*) FROM items WHERE group_id IS NOT NULL GROUP BY group_id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := map[int64]int{}
	for rows.Next() {
		var id int64
		var n int
		if err := rows.Scan(&id, &n); err != nil {
			return nil, err
		}
		out[id] = n
	}
	return out, rows.Err()
}
