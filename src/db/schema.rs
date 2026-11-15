//! schema 与迁移。
//!
//! 约定：
//! - `PRAGMA user_version` 是唯一的版本源，`migrate()` 只往前走，永不回滚；
//! - 每步迁移在一个事务里执行，成功后才推进版本号；
//! - `items_fts` 是外部内容表（`content='items'`），靠触发器与 `items` 同步；
//! - `tokenize='trigram'`：中英文都能按子串命中，代价是查询词至少 3 个字符
//!   （短词由查询层降级 `LIKE`，见契约 7）。

use rusqlite::Connection;

use super::Result;

/// 当前 schema 版本，等于 `MIGRATIONS` 的长度。
pub const SCHEMA_VERSION: i64 = 1;

/// 按序号排列的迁移脚本：第 `n` 个把库从 `n-1` 推到 `n`。
///
/// 已发布的脚本不可修改（用户库里已经跑过），只能追加新脚本。
const MIGRATIONS: &[&str] = &[V1];

const V1: &str = r#"
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
"#;

/// 读当前版本。
pub fn version(conn: &Connection) -> Result<i64> {
    conn.query_row("PRAGMA user_version", [], |r| r.get(0))
}

/// 把库推进到 [`SCHEMA_VERSION`]；已经是最新则什么都不做。
pub fn migrate(conn: &mut Connection) -> Result<()> {
    let mut current = version(conn)?;
    if current > SCHEMA_VERSION {
        return Err(rusqlite::Error::SqliteFailure(
            rusqlite::ffi::Error::new(rusqlite::ffi::SQLITE_ERROR),
            Some(format!(
                "数据库版本 {current} 比本程序支持的 {SCHEMA_VERSION} 新，请升级 clipnest"
            )),
        ));
    }
    while current < SCHEMA_VERSION {
        let next = current + 1;
        let sql = MIGRATIONS[(next - 1) as usize];
        let tx = conn.transaction()?;
        tx.execute_batch(sql)?;
        tx.pragma_update(None, "user_version", next)?;
        tx.commit()?;
        current = next;
        log::info!("db migrate {} -> {}", current - 1, current);
    }
    ensure_install_id(conn)
}

/// 每个库一个安装标识（P4 同步要拿它当设备身份），缺失时补一个。
pub fn install_id(conn: &Connection) -> Result<String> {
    conn.query_row("SELECT value FROM meta WHERE key = 'install_id'", [], |r| {
        r.get(0)
    })
}

fn ensure_install_id(conn: &Connection) -> Result<()> {
    conn.execute(
        "INSERT INTO meta(key, value) VALUES ('install_id', ?1)
           ON CONFLICT(key) DO NOTHING",
        [uuid::Uuid::new_v4().to_string()],
    )?;
    Ok(())
}

/// 重建全文索引（索引与表不同步时的兜底，也用于导入后修复）。
pub fn rebuild_fts(conn: &Connection) -> Result<()> {
    conn.execute("INSERT INTO items_fts(items_fts) VALUES ('rebuild')", [])?;
    Ok(())
}

#[cfg(test)]
mod tests {
    use super::*;

    fn table_exists(conn: &Connection, name: &str) -> bool {
        conn.query_row(
            "SELECT count(*) FROM sqlite_master WHERE name = ?1",
            [name],
            |r| r.get::<_, i64>(0),
        )
        .unwrap_or(0)
            > 0
    }

    /// T2 验收：建库后所有表都在，版本号到位。
    #[test]
    fn fresh_db_has_full_schema() {
        let mut conn = super::super::open_memory().expect("memory db");
        assert_eq!(version(&conn).unwrap(), SCHEMA_VERSION);
        for t in ["items", "groups", "meta", "items_fts"] {
            assert!(table_exists(&conn, t), "缺表 {t}");
        }
        assert!(!install_id(&conn).unwrap().is_empty());

        // 再迁一次是空操作
        migrate(&mut conn).expect("idempotent migrate");
        assert_eq!(version(&conn).unwrap(), SCHEMA_VERSION);
    }

    /// T2 验收：版本从 0 推进，已有数据不丢。
    #[test]
    fn migrate_advances_version_and_keeps_data() {
        let mut conn = Connection::open_in_memory().unwrap();
        conn.pragma_update(None, "foreign_keys", true).unwrap();
        assert_eq!(version(&conn).unwrap(), 0);
        migrate(&mut conn).unwrap();
        assert_eq!(version(&conn).unwrap(), SCHEMA_VERSION);

        conn.execute(
            "INSERT INTO items(content_hash, kind, content, created_at, updated_at)
             VALUES ('h1','text','老数据','2026-01-01T00:00:00.000Z','2026-01-01T00:00:00.000Z')",
            [],
        )
        .unwrap();

        // 模拟旧库：版本号被退回 0 后再迁移，数据仍在
        conn.pragma_update(None, "user_version", 0).unwrap();
        migrate(&mut conn).unwrap();
        let n: i64 = conn
            .query_row("SELECT count(*) FROM items", [], |r| r.get(0))
            .unwrap();
        assert_eq!(n, 1);
    }

    /// 未来版本（用户装过更新的 clipnest）必须报错，不许降级写。
    #[test]
    fn refuses_to_downgrade() {
        let mut conn = Connection::open_in_memory().unwrap();
        conn.pragma_update(None, "user_version", SCHEMA_VERSION + 1)
            .unwrap();
        assert!(migrate(&mut conn).is_err());
    }

    /// T2 验收：触发器让 FTS 跟着 items 增/改/删同步。
    #[test]
    fn fts_tracks_insert_update_delete() {
        let conn = super::super::open_memory().unwrap();
        let hit = |q: &str| -> i64 {
            conn.query_row(
                "SELECT count(*) FROM items_fts WHERE items_fts MATCH ?1",
                [q],
                |r| r.get(0),
            )
            .unwrap_or(0)
        };
        let insert = |content: &str| {
            conn.execute(
                "INSERT INTO items(content_hash, kind, content, created_at, updated_at)
                 VALUES (?1,'text',?2,'2026-01-01T00:00:00.000Z','2026-01-01T00:00:00.000Z')",
                rusqlite::params![content, content],
            )
            .unwrap();
            conn.last_insert_rowid()
        };

        // 增
        let id = insert("hello clipnest 银河系");
        assert_eq!(hit("clipnest"), 1);
        assert_eq!(hit("银河系"), 1, "trigram 应支持中文子串（3 字符起）");
        assert_eq!(hit("银河"), 0, "2 字符短词 trigram 命中不了，靠查询层降级 LIKE");

        // 改
        conn.execute(
            "UPDATE items SET content = 'goodbye world' WHERE id = ?1",
            [id],
        )
        .unwrap();
        assert_eq!(hit("clipnest"), 0, "旧内容必须从索引里消失");
        assert_eq!(hit("goodbye"), 1);

        // 删
        conn.execute("DELETE FROM items WHERE id = ?1", [id]).unwrap();
        assert_eq!(hit("goodbye"), 0);

        // 备注也进索引
        let id = insert("正文");
        conn.execute("UPDATE items SET note = 'tag-银河系' WHERE id = ?1", [id])
            .unwrap();
        assert_eq!(hit("银河系"), 1);
    }

    /// 库若被外部工具改坏索引，rebuild 能救回来。
    #[test]
    fn rebuild_repairs_fts() {
        let conn = super::super::open_memory().unwrap();
        conn.execute(
            "INSERT INTO items(content_hash, kind, content, created_at, updated_at)
             VALUES ('h','text','孤岛数据之岛','2026-01-01T00:00:00.000Z','2026-01-01T00:00:00.000Z')",
            [],
        )
        .unwrap();
        conn.execute("INSERT INTO items_fts(items_fts) VALUES ('delete-all')", [])
            .unwrap();
        let n: i64 = conn
            .query_row(
                "SELECT count(*) FROM items_fts WHERE items_fts MATCH '孤岛数'",
                [],
                |r| r.get(0),
            )
            .unwrap();
        assert_eq!(n, 0);
        rebuild_fts(&conn).unwrap();
        let n: i64 = conn
            .query_row(
                "SELECT count(*) FROM items_fts WHERE items_fts MATCH '孤岛数'",
                [],
                |r| r.get(0),
            )
            .unwrap();
        assert_eq!(n, 1);
    }
}
