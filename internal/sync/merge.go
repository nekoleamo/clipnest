package sync

import (
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"clipnest/internal/model"
)

// FormatVersion 是载荷格式版本。
const FormatVersion = 1

// Coder 是编解码器（加密与否由调用方决定）。
type Coder func([]byte) ([]byte, error)

// Change 是一个设备传给别的设备的变更。
type Change struct {
	Op          string    `json:"op"`
	Item        *SyncItem `json:"item,omitempty"`
	ContentHash string    `json:"content_hash,omitempty"`
	DeletedAt   string    `json:"deleted_at,omitempty"`
}

// SyncItem 是跨设备传输的条目（去掉本地自增 id，分组换成名字）。
type SyncItem struct {
	ContentHash string  `json:"content_hash"`
	Kind        string  `json:"kind"`
	Content     string  `json:"content"`
	FileTypes   *string `json:"file_types"`
	Width       *int64  `json:"width"`
	Height      *int64  `json:"height"`
	UseCount    int64   `json:"use_count"`
	Pinned      bool    `json:"pinned"`
	Favorite    bool    `json:"favorite"`
	Note        *string `json:"note"`
	Group       *string `json:"group"`
	Sensitive   bool    `json:"sensitive"`
	CreatedAt   string  `json:"created_at"`
	UpdatedAt   string  `json:"updated_at"`
	// MetaAt 是元数据（置顶/收藏/敏感/备注/分组）最后一次改动的时间；
	// 契约 6 不刷新 updated_at，所以增量导出要同时看这一列。
	MetaAt string `json:"meta_at,omitempty"`
}

// Package 是一台设备一次导出的增量包。
type Package struct {
	Version    int      `json:"version"`
	DeviceID   string   `json:"device_id"`
	ExportedAt string   `json:"exported_at"`
	Changes    []Change `json:"changes"`
}

// ApplyReport 是合并结果。
type ApplyReport struct {
	Inserted int
	Updated  int
	Skipped  int
	Deleted  int
}

// Changed 报告这次合并有没有真的改动数据。
func (r ApplyReport) Changed() bool { return r.Inserted+r.Updated+r.Deleted > 0 }

func (r *ApplyReport) merge(other ApplyReport) {
	r.Inserted += other.Inserted
	r.Updated += other.Updated
	r.Skipped += other.Skipped
	r.Deleted += other.Deleted
}

// SyncReport 是一轮同步的结果。
type SyncReport struct {
	Pushed      int
	Pulled      int
	BlobsPushed int
	BlobsPulled int
	Applied     ApplyReport
	// Endpoint 是本轮实际用的入口（局域网 / Tailscale / 本地目录）。
	Endpoint string
}

// ExportChanges 导出「自 since 之后变化」的条目与墓碑；since 为 nil 表示全量。
func ExportChanges(db *sql.DB, since *string) ([]Change, error) {
	sinceAt := ""
	if since != nil {
		sinceAt = *since
	}

	rows, err := db.Query(`SELECT i.content_hash, i.kind, i.content, i.file_types, i.width, i.height,
			i.use_count, i.pinned, i.favorite, i.note, g.name, i.sensitive, i.created_at, i.updated_at, i.meta_at
		FROM items i LEFT JOIN groups g ON g.id = i.group_id
		WHERE i.updated_at >= ? OR i.meta_at >= ?
		ORDER BY i.updated_at, i.id`, sinceAt, sinceAt)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Change
	for rows.Next() {
		var (
			item                   SyncItem
			fileTypes, note, group sql.NullString
			width, height          sql.NullInt64
			pinned, favorite, sens int64
		)
		if err := rows.Scan(&item.ContentHash, &item.Kind, &item.Content, &fileTypes,
			&width, &height, &item.UseCount, &pinned, &favorite, &note, &group, &sens,
			&item.CreatedAt, &item.UpdatedAt, &item.MetaAt); err != nil {
			return nil, err
		}
		item.FileTypes = nullableStr(fileTypes)
		item.Note = nullableStr(note)
		item.Group = nullableStr(group)
		item.Width = nullableInt(width)
		item.Height = nullableInt(height)
		item.Pinned, item.Favorite, item.Sensitive = pinned != 0, favorite != 0, sens != 0
		out = append(out, Change{Op: "upsert", Item: &item})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// 用 >= 而不是 >：上界是导出前取的 exportedAt，同一毫秒内的改动会正好落在边界上；
	// 重复发送是幂等的（合并侧按时间戳判新旧），漏发则会永久丢。
	tombs, err := db.Query("SELECT content_hash, deleted_at FROM tombstones WHERE deleted_at >= ?", sinceAt)
	if err != nil {
		return nil, err
	}
	defer tombs.Close()
	for tombs.Next() {
		var hash, deletedAt string
		if err := tombs.Scan(&hash, &deletedAt); err != nil {
			return nil, err
		}
		out = append(out, Change{Op: "delete", ContentHash: hash, DeletedAt: deletedAt})
	}
	return out, tombs.Err()
}

// mergedResult 是单条合并的结论。
type mergedResult int

const (
	mergedInserted mergedResult = iota
	mergedUpdated
	mergedSkipped
)

// ApplyChanges 把别人的变更合并进本地库（一个事务）。
func ApplyChanges(db *sql.DB, changes []Change) (ApplyReport, error) {
	tx, err := db.Begin()
	if err != nil {
		return ApplyReport{}, err
	}
	defer tx.Rollback()

	var report ApplyReport
	for _, change := range changes {
		switch change.Op {
		case "upsert":
			if change.Item == nil {
				report.Skipped++
				continue
			}
			switch merged, err := mergeItem(tx, change.Item); {
			case err != nil:
				return report, err
			case merged == mergedInserted:
				report.Inserted++
			case merged == mergedUpdated:
				report.Updated++
			default:
				report.Skipped++
			}
		case "delete":
			ok, err := applyTombstone(tx, change.ContentHash, change.DeletedAt)
			if err != nil {
				return report, err
			}
			if ok {
				report.Deleted++
			} else {
				report.Skipped++
			}
		default:
			report.Skipped++
		}
	}
	if err := tx.Commit(); err != nil {
		return report, err
	}
	return report, nil
}

func mergeItem(tx *sql.Tx, remote *SyncItem) (mergedResult, error) {
	// 本地已经删了（墓碑更新的那条）：不要再被旧数据复活。
	if deletedAt, ok, err := tombstoneOf(tx, remote.ContentHash); err != nil {
		return mergedSkipped, err
	} else if ok && deletedAt >= remote.UpdatedAt {
		return mergedSkipped, nil
	}

	var groupID any
	if remote.Group != nil {
		id, err := ensureGroup(tx, *remote.Group)
		if err != nil {
			return mergedSkipped, err
		}
		groupID = id
	}

	var (
		localID      int64
		localUses    int64
		localUpdated string
		localMeta    string
	)
	err := tx.QueryRow("SELECT id, use_count, updated_at, meta_at FROM items WHERE content_hash = ?",
		remote.ContentHash).Scan(&localID, &localUses, &localUpdated, &localMeta)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		if _, err := tx.Exec(`INSERT INTO items(content_hash, kind, content, file_types, width, height,
				use_count, pinned, favorite, note, group_id, sensitive, created_at, updated_at, meta_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			remote.ContentHash, remote.Kind, remote.Content, remote.FileTypes, remote.Width,
			remote.Height, remote.UseCount, boolInt(remote.Pinned), boolInt(remote.Favorite),
			remote.Note, groupID, boolInt(remote.Sensitive), remote.CreatedAt, remote.UpdatedAt,
			freshAt(remote.UpdatedAt, remote.MetaAt)); err != nil {
			return mergedSkipped, err
		}
		return mergedInserted, nil
	case err != nil:
		return mergedSkipped, err
	}

	// 使用次数取 max：重复拉取不会膨胀，也不会丢次数。
	uses := localUses
	if remote.UseCount > uses {
		uses = remote.UseCount
	}
	// 谁更新：内容/使用看 updated_at，元数据看 meta_at，取两者里更晚的那个比
	if freshAt(remote.UpdatedAt, remote.MetaAt) <= freshAt(localUpdated, localMeta) {
		if uses != localUses {
			if _, err := tx.Exec("UPDATE items SET use_count = ? WHERE id = ?", uses, localID); err != nil {
				return mergedSkipped, err
			}
			return mergedUpdated, nil
		}
		return mergedSkipped, nil
	}
	if _, err := tx.Exec(`UPDATE items SET kind = ?, content = ?, file_types = ?, width = ?, height = ?,
			use_count = ?, pinned = ?, favorite = ?, note = ?, group_id = ?, sensitive = ?,
			updated_at = ?, meta_at = ?
		WHERE id = ?`,
		remote.Kind, remote.Content, remote.FileTypes, remote.Width, remote.Height, uses,
		boolInt(remote.Pinned), boolInt(remote.Favorite), remote.Note, groupID,
		boolInt(remote.Sensitive), remote.UpdatedAt, freshAt(remote.UpdatedAt, remote.MetaAt), localID); err != nil {
		return mergedSkipped, err
	}
	return mergedUpdated, nil
}

func tombstoneOf(tx *sql.Tx, hash string) (string, bool, error) {
	var deletedAt string
	err := tx.QueryRow("SELECT deleted_at FROM tombstones WHERE content_hash = ?", hash).Scan(&deletedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	return deletedAt, err == nil, err
}

// applyTombstone 墓碑晚于本地条目的更新时间就删；返回是否真删了。
func applyTombstone(tx *sql.Tx, hash, deletedAt string) (bool, error) {
	var localUpdated string
	err := tx.QueryRow("SELECT updated_at FROM items WHERE content_hash = ?", hash).Scan(&localUpdated)
	hasLocal := err == nil
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return false, err
	}

	// 墓碑照收（幂等，也防止之后再被旧数据复活）。
	if _, err := tx.Exec(`INSERT INTO tombstones(content_hash, deleted_at, device_id) VALUES (?, ?, NULL)
		ON CONFLICT(content_hash) DO UPDATE SET deleted_at = max(deleted_at, excluded.deleted_at)`,
		hash, deletedAt); err != nil {
		return false, err
	}
	if hasLocal && localUpdated <= deletedAt {
		if _, err := tx.Exec("DELETE FROM items WHERE content_hash = ?", hash); err != nil {
			return false, err
		}
		return true, nil
	}
	return false, nil
}

func ensureGroup(tx *sql.Tx, name string) (int64, error) {
	if _, err := tx.Exec("INSERT INTO groups(name) VALUES (?) ON CONFLICT(name) DO NOTHING", name); err != nil {
		return 0, err
	}
	var id int64
	err := tx.QueryRow("SELECT id FROM groups WHERE name = ?", name).Scan(&id)
	return id, err
}

// MarshalPackage 编码增量包（测试与传输复用）。
func MarshalPackage(p Package) ([]byte, error) { return json.Marshal(p) }

// UnmarshalPackage 解码增量包。
func UnmarshalPackage(data []byte) (Package, error) {
	var p Package
	err := json.Unmarshal(data, &p)
	return p, err
}

// Now 是当前时间戳（与 model.Now 一致）。
func Now() string { return model.Now() }

func nullableStr(v sql.NullString) *string {
	if !v.Valid {
		return nil
	}
	s := v.String
	return &s
}

func nullableInt(v sql.NullInt64) *int64 {
	if !v.Valid {
		return nil
	}
	n := v.Int64
	return &n
}

func boolInt(v bool) int64 {
	if v {
		return 1
	}
	return 0
}

var _ = time.Now

// freshAt 是「这条数据最后一次变」的时间：内容/使用看 updated_at，元数据看 meta_at。
//
// 老版本载荷没有 meta_at（空串），此时退回 updated_at，保持向后兼容。
func freshAt(updatedAt, metaAt string) string {
	if metaAt > updatedAt {
		return metaAt
	}
	return updatedAt
}
