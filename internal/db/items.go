// 条目入库与去重。
//
// 契约 5：去重命中只 use_count + 1 并刷新 updated_at。
// 契约 6：收藏/置顶/备注/分组的变更不刷新 updated_at（走 UpdateMeta）。
package db

import (
	"database/sql"
	"errors"
	"fmt"

	"clipnest/internal/model"
)

// ItemColumns 是 items 全列，顺序与 scanItem 一致。
const ItemColumns = "id, content_hash, kind, content, file_types, width, height, " +
	"use_count, pinned, favorite, note, group_id, sensitive, created_at, updated_at"

// UpsertResult 是入库结果。
type UpsertResult struct {
	ID       int64
	Inserted bool // false = 命中已有条目（去重）
}

// Upsert 入库一条：以 content_hash 为唯一键 upsert。
//
// 命中时只动 use_count 和 updated_at，其余字段（含收藏、备注、分组）保持用户改过的值。
func Upsert(db *sql.DB, item model.NewItem) (UpsertResult, error) {
	hash := item.Hash()
	ts := model.Now()

	// 一条语句搞定「有就 use_count+1、没有就插入」（契约 5）。
	// RETURNING 里用 use_count = 1 判断这次是不是新插入：更新路径一定是 +1 后的值。
	var (
		id       int64
		inserted int64
	)
	err := db.QueryRow(`INSERT INTO items(content_hash, kind, content, file_types, width, height,
			use_count, pinned, favorite, note, group_id, sensitive, created_at, updated_at, meta_at)
		VALUES (?, ?, ?, ?, ?, ?, 1, 0, 0, NULL, NULL, ?, ?, ?, ?)
		ON CONFLICT(content_hash) DO UPDATE SET
			use_count  = items.use_count + 1,
			updated_at = excluded.updated_at
		RETURNING id, (use_count = 1)`,
		hash, string(item.Kind), item.Content, nullable(item.FileTypes), item.Width, item.Height,
		boolInt(item.Sensitive), ts, ts, ts).Scan(&id, &inserted)
	if err != nil {
		return UpsertResult{}, err
	}
	return UpsertResult{ID: id, Inserted: inserted != 0}, nil
}

// MetaPatch 是元数据补丁：只写出现的字段。
//
// Note / GroupID 用双层指针表达「不改」与「清空」。
type MetaPatch struct {
	Pinned     *bool
	Favorite   *bool
	Note       *string // non-nil = 改成这个值
	ClearNote  bool    // true = 清空 note
	GroupID    *int64
	ClearGroup bool
	Sensitive  *bool
}

// IsEmpty 判断补丁是否有内容。
func (p MetaPatch) IsEmpty() bool {
	return p.Pinned == nil && p.Favorite == nil && p.Note == nil && !p.ClearNote &&
		p.GroupID == nil && !p.ClearGroup && p.Sensitive == nil
}

// UpdateMeta 是契约 6：改元数据，不碰 updated_at，避免长期收藏被顶到列表最前。
func UpdateMeta(db *sql.DB, id int64, patch MetaPatch) error {
	if patch.IsEmpty() {
		return nil
	}
	// 动态 SQL 只拼列名（全是代码内的常量），值一律走参数。
	var sets []string
	var values []any
	set := func(col string, v any) {
		sets = append(sets, col+" = ?")
		values = append(values, v)
	}
	if patch.Pinned != nil {
		set("pinned", boolInt(*patch.Pinned))
	}
	if patch.Favorite != nil {
		set("favorite", boolInt(*patch.Favorite))
	}
	if patch.Sensitive != nil {
		set("sensitive", boolInt(*patch.Sensitive))
	}
	if patch.Note != nil {
		set("note", *patch.Note)
	} else if patch.ClearNote {
		set("note", nil)
	}
	if patch.GroupID != nil {
		set("group_id", *patch.GroupID)
	} else if patch.ClearGroup {
		set("group_id", nil)
	}
	// 契约 6：不刷新 updated_at（列表顺序不变）；但要让同步知道这条元数据变了
	set("meta_at", model.Now())
	values = append(values, id)

	_, err := db.Exec(fmt.Sprintf("UPDATE items SET %s WHERE id = ?", joinComma(sets)), values...)
	return err
}

// Touch 是契约 5 的「使用一次」：use_count + 1 并刷新 updated_at（粘贴成功时调用）。
func Touch(db *sql.DB, id int64) error {
	_, err := db.Exec("UPDATE items SET use_count = use_count + 1, updated_at = ? WHERE id = ?", model.Now(), id)
	return err
}

// Get 按 id 取一条。
func Get(db *sql.DB, id int64) (model.Item, bool, error) {
	row := db.QueryRow("SELECT "+ItemColumns+" FROM items WHERE id = ?", id)
	item, err := scanItem(row)
	if errors.Is(err, sql.ErrNoRows) {
		return model.Item{}, false, nil
	}
	if err != nil {
		return model.Item{}, false, err
	}
	return item, true, nil
}

// Delete 删一条（图片文件等资源由调用方清理）。
//
// 同时留一条墓碑：同步靠它把「删除」传播到别的设备（items 行没了，光看表看不出删过什么）。
func Delete(db *sql.DB, id int64) (bool, error) {
	tx, err := db.Begin()
	if err != nil {
		return false, err
	}
	defer tx.Rollback()

	var hash string
	err = tx.QueryRow("SELECT content_hash FROM items WHERE id = ?", id).Scan(&hash)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if _, err := tx.Exec("DELETE FROM items WHERE id = ?", id); err != nil {
		return false, err
	}
	if _, err := tx.Exec(`INSERT INTO tombstones(content_hash, deleted_at, device_id) VALUES (?, ?, NULL)
		ON CONFLICT(content_hash) DO UPDATE SET deleted_at = excluded.deleted_at`, hash, model.Now()); err != nil {
		return false, err
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	return true, nil
}

// Count 是总条数。
func Count(db *sql.DB) (int64, error) {
	var n int64
	err := db.QueryRow("SELECT count(*) FROM items").Scan(&n)
	return n, err
}

// rowScanner 是 *sql.Row 与 *sql.Rows 的公共部分。
type rowScanner interface {
	Scan(dest ...any) error
}

// scanItem 把数据库行转成 model.Item。
func scanItem(row rowScanner) (model.Item, error) {
	var (
		item                        model.Item
		kind                        string
		fileTypes, note             sql.NullString
		width, height               sql.NullInt64
		groupID                     sql.NullInt64
		pinned, favorite, sensitive int64
	)
	if err := row.Scan(&item.ID, &item.ContentHash, &kind, &item.Content, &fileTypes,
		&width, &height, &item.UseCount, &pinned, &favorite, &note, &groupID,
		&sensitive, &item.CreatedAt, &item.UpdatedAt); err != nil {
		return model.Item{}, err
	}
	parsed, ok := model.ParseKind(kind)
	if !ok {
		return model.Item{}, fmt.Errorf("未知的 kind: %s", kind)
	}
	item.Kind = parsed
	item.FileTypes = fileTypes.String
	item.Width, item.Height = width.Int64, height.Int64
	item.Pinned, item.Favorite = pinned != 0, favorite != 0
	item.Sensitive = sensitive != 0
	item.Note = note.String
	if groupID.Valid {
		gid := groupID.Int64
		item.GroupID = &gid
	}
	return item, nil
}

func boolInt(v bool) int64 {
	if v {
		return 1
	}
	return 0
}

func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func joinComma(parts []string) string {
	out := ""
	for i, p := range parts {
		if i > 0 {
			out += ", "
		}
		out += p
	}
	return out
}
