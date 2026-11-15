//! 数据层：连接管理 + schema/迁移 + 条目写入 + 查询搜索。
//!
//! 只用 `rusqlite`（bundled，自带 FTS5），不引 SQL 框架；
//! 迁移靠 `PRAGMA user_version` 递增，见 `schema`。

pub mod items;
pub mod schema;
pub mod search;

use std::path::Path;

use rusqlite::Connection;

pub type Result<T> = rusqlite::Result<T>;

/// 打开（必要时创建）指定路径的数据库。
///
/// 父目录不存在会创建；连接上来先设好所有连接都要有的 pragma。
pub fn open(path: impl AsRef<Path>) -> Result<Connection> {
    let path = path.as_ref();
    if let Some(dir) = path.parent()
        && !dir.as_os_str().is_empty()
    {
        std::fs::create_dir_all(dir).map_err(|e| {
            rusqlite::Error::SqliteFailure(
                rusqlite::ffi::Error::new(rusqlite::ffi::SQLITE_CANTOPEN),
                Some(format!("create dir {}: {e}", dir.display())),
            )
        })?;
    }
    let conn = Connection::open(path)?;
    let mut conn = prepare(conn)?;
    schema::migrate(&mut conn)?;
    Ok(conn)
}

/// 内存库，测试用（也跑一遍迁移，schema 与真实库一致）。
pub fn open_memory() -> Result<Connection> {
    let mut conn = prepare(Connection::open_in_memory()?)?;
    schema::migrate(&mut conn)?;
    Ok(conn)
}

/// 每条连接都要做的 pragma。
fn prepare(conn: Connection) -> Result<Connection> {
    // 写并发靠 WAL；内存库忽略失败（返回 "memory"）。
    let _: String = conn.query_row("PRAGMA journal_mode=WAL", [], |r| r.get(0))?;
    conn.pragma_update(None, "foreign_keys", true)?;
    // 采集线程与 UI 线程同时读写，给足忙等预算。
    conn.busy_timeout(std::time::Duration::from_secs(5))?;
    Ok(conn)
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn opens_in_memory_at_current_version() {
        let conn = open_memory().expect("memory db");
        assert_eq!(
            schema::version(&conn).expect("user_version"),
            schema::SCHEMA_VERSION
        );
    }

    #[test]
    fn creates_parent_dir_and_reopens() {
        let dir = std::env::temp_dir().join(format!("clipnest-db-{}", crate::model::now()));
        let path = dir.join("nested").join("clipnest.db");
        {
            let conn = open(&path).expect("create");
            drop(conn);
        }
        assert!(path.exists(), "库文件应已创建于 {}", path.display());
        // 二次打开不能报错（WAL 残留文件也要能处理）
        let conn = open(&path).expect("reopen");
        assert_eq!(schema::version(&conn).unwrap(), schema::SCHEMA_VERSION);
        let _ = std::fs::remove_dir_all(&dir);
    }
}
