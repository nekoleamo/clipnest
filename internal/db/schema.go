// schema 与迁移。
//
// 约定：
//   - PRAGMA user_version 是唯一的版本源，Migrate 只往前走，永不回滚；
//   - 每步迁移在一个事务里执行，成功后才推进版本号；
//   - items_fts 是外部内容表（content='items'），靠触发器与 items 同步；
//   - tokenize='trigram'：中英文都能按子串命中，代价是查询词至少 3 个字符
//     （短词由查询层降级 LIKE，见契约 7）。
package db

import (
	"database/sql"
	"errors"
	"fmt"

	"github.com/google/uuid"
)

// migrations 按序号排列：第 n 个把库从 n-1 推到 n。
//
// 已发布的脚本不可修改（用户库里已经跑过），只能追加新脚本。
var migrations = []string{v1, v2}

// SchemaVersion 是当前 schema 版本，等于 migrations 的长度。
var SchemaVersion = int64(len(migrations))

const v1 = `
CREATE TABLE IF NOT EXISTS groups (
  id    INTEGER PRIMARY KEY,
  name  TEXT NOT NULL UNIQUE,
  sort  INTEGER NOT NULL DEFAULT 0
);

CREATE TABLE IF NOT EXISTS items (
  id            INTEGER PRIMARY KEY,
  content_hash  TEXT NOT NULL UNIQUE,
  kind          TEXT NOT NULL CHECK (kind IN ('text','html','rtf','image','files')),
  content       TEXT NOT NULL,
  file_types    TEXT,
  width         INTEGER,
  height        INTEGER,
  use_count     INTEGER NOT NULL DEFAULT 1,
  pinned        INTEGER NOT NULL DEFAULT 0,
  favorite      INTEGER NOT NULL DEFAULT 0,
  note          TEXT,
  group_id      INTEGER REFERENCES groups(id) ON DELETE SET NULL,
  sensitive     INTEGER NOT NULL DEFAULT 0,
  created_at    TEXT NOT NULL,
  updated_at    TEXT NOT NULL
);

-- 契约 8：置顶恒排最前；列表默认按 updated_at 倒序
CREATE INDEX IF NOT EXISTS items_order ON items(pinned DESC, updated_at DESC);
CREATE INDEX IF NOT EXISTS items_group ON items(group_id);

CREATE VIRTUAL TABLE IF NOT EXISTS items_fts USING fts5(
  content,
  note,
  content='items',
  content_rowid='id',
  tokenize='trigram'
);

CREATE TRIGGER IF NOT EXISTS items_fts_ai AFTER INSERT ON items BEGIN
  INSERT INTO items_fts(rowid, content, note) VALUES (new.id, new.content, new.note);
END;

CREATE TRIGGER IF NOT EXISTS items_fts_ad AFTER DELETE ON items BEGIN
  INSERT INTO items_fts(items_fts, rowid, content, note)
    VALUES ('delete', old.id, old.content, old.note);
END;

CREATE TRIGGER IF NOT EXISTS items_fts_au AFTER UPDATE ON items BEGIN
  INSERT INTO items_fts(items_fts, rowid, content, note)
    VALUES ('delete', old.id, old.content, old.note);
  INSERT INTO items_fts(rowid, content, note) VALUES (new.id, new.content, new.note);
END;

CREATE TABLE IF NOT EXISTS meta (
  key   TEXT PRIMARY KEY,
  value TEXT NOT NULL
);
`

// v2：删除留下墓碑，供同步把「删除」传播到其它设备。
const v2 = `
CREATE TABLE IF NOT EXISTS tombstones (
  content_hash  TEXT PRIMARY KEY,
  deleted_at    TEXT NOT NULL,
  device_id     TEXT
);
`

// ErrTooNew 表示库版本比本程序支持的新，不许降级写。
var ErrTooNew = errors.New("数据库版本比本程序支持的更新，请升级 clipnest")

// Version 读当前版本。
func Version(db *sql.DB) (int64, error) {
	var v int64
	err := db.QueryRow("PRAGMA user_version").Scan(&v)
	return v, err
}

// Migrate 把库推进到 SchemaVersion；已经是最新则什么都不做。
func Migrate(db *sql.DB) error {
	current, err := Version(db)
	if err != nil {
		return err
	}
	if current > SchemaVersion {
		return fmt.Errorf("%w：库 %d / 程序 %d", ErrTooNew, current, SchemaVersion)
	}
	for current < SchemaVersion {
		next := current + 1
		tx, err := db.Begin()
		if err != nil {
			return err
		}
		if _, err := tx.Exec(migrations[next-1]); err != nil {
			tx.Rollback()
			return fmt.Errorf("迁移 %d -> %d: %w", current, next, err)
		}
		if _, err := tx.Exec(fmt.Sprintf("PRAGMA user_version = %d", next)); err != nil {
			tx.Rollback()
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
		current = next
	}
	return ensureInstallID(db)
}

// InstallID 是每个库一个的安装标识（同步要拿它当设备身份），缺失时补一个。
func InstallID(db *sql.DB) (string, error) {
	var id string
	err := db.QueryRow("SELECT value FROM meta WHERE key = 'install_id'").Scan(&id)
	return id, err
}

func ensureInstallID(db *sql.DB) error {
	_, err := db.Exec("INSERT INTO meta(key, value) VALUES ('install_id', ?) ON CONFLICT(key) DO NOTHING", uuid.NewString())
	return err
}

// RebuildFTS 重建全文索引（索引与表不同步时的兜底，也用于导入后修复）。
func RebuildFTS(db *sql.DB) error {
	_, err := db.Exec("INSERT INTO items_fts(items_fts) VALUES ('rebuild')")
	return err
}
