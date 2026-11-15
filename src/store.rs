//! 采集结果落库：把剪贴板快照翻译成条目并写进数据库。
//!
//! 这一层只做「存」，不含 UI；面板读库走 `db::search`。
//! 契约 3：文件条目的 `content` 是换行分隔的路径，`file_types` 是 `d`/`f` 串。
//! 目前只落 `text` 与 `files`，图片要等 P3 的缩略图管线。

use std::path::PathBuf;
use std::sync::{Mutex, OnceLock};

use rusqlite::Connection;

use crate::db;
use crate::db::items::{self, UpsertResult};
use crate::model::{ItemKind, NewItem};
use crate::paths::Paths;

/// 一次采集里值得入库的内容。
#[derive(Debug, Clone, Default, PartialEq, Eq)]
pub struct Captured {
    /// 纯文本 flavor。
    pub text: Option<String>,
    /// 文件 flavor 的路径列表。
    pub files: Vec<String>,
}

impl Captured {
    pub fn is_empty(&self) -> bool {
        self.text.is_none() && self.files.is_empty()
    }

    /// 翻译成要入库的条目。
    ///
    /// 文件优先：复制文件时系统同时给文本（路径字符串），只留文件条目。
    pub fn to_new_items(&self) -> Vec<NewItem> {
        if !self.files.is_empty() {
            let types: String = self.files.iter().map(|p| file_type_of(p)).collect();
            return vec![NewItem::files(&self.files, types)];
        }
        match &self.text {
            Some(t) if !t.is_empty() => vec![NewItem::text(t.clone())],
            _ => Vec::new(),
        }
    }
}

/// `'d'` 目录 / `'f'` 文件（读不到时按文件算）。
fn file_type_of(path: &str) -> char {
    match std::fs::metadata(path) {
        Ok(m) if m.is_dir() => 'd',
        _ => 'f',
    }
}

/// 一条已落库的记录。
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub struct Stored {
    pub kind: ItemKind,
    pub result: UpsertResult,
}

impl Stored {
    /// 日志用："INSERTED id=3" / "DEDUPED id=3"。
    pub fn label(&self) -> String {
        let tag = match self.result {
            UpsertResult::Inserted(_) => "INSERTED",
            UpsertResult::Deduped(_) => "DEDUPED",
        };
        format!("{tag} id={}", self.result.id())
    }
}

/// 把快照写库；返回每条的结果。
pub fn record(conn: &Connection, cap: &Captured) -> rusqlite::Result<Vec<Stored>> {
    let mut out = Vec::new();
    for item in cap.to_new_items() {
        let result = items::upsert(conn, &item)?;
        out.push(Stored {
            kind: item.kind,
            result,
        });
    }
    Ok(out)
}

static DB: OnceLock<Mutex<Connection>> = OnceLock::new();

/// 数据库文件路径（平台相关）。
pub fn db_path() -> PathBuf {
    Paths::platform().db()
}

/// 全局连接；首次调用时建目录、开库、迁移。失败返回 `None`（不 panic，应用照常跑）。
pub fn db() -> Option<&'static Mutex<Connection>> {
    if let Some(conn) = DB.get() {
        return Some(conn);
    }
    let paths = Paths::platform();
    match open(&paths) {
        Ok(conn) => Some(DB.get_or_init(|| Mutex::new(conn))),
        Err(e) => {
            log::error!("数据库打开失败（{}）：{e}", paths.db().display());
            None
        }
    }
}

/// 按路径建好目录并打开库（首次启动自动建目录）。
pub fn open(paths: &Paths) -> Result<Connection, Box<dyn std::error::Error>> {
    paths.ensure()?;
    Ok(db::open(paths.db())?)
}

/// 契约 9：按上限裁剪历史，收藏与置顶永远保留。
///
/// 只删「既不收藏也不置顶」里最旧的那些，`max_items` 是总条数上限，
/// 受保护条目先把名额占掉。返回删除条数。
pub fn prune(conn: &Connection, max_items: i64) -> rusqlite::Result<usize> {
    if max_items <= 0 {
        return Ok(0); // 0 = 不限
    }
    let protected: i64 = conn.query_row(
        "SELECT count(*) FROM items WHERE pinned = 1 OR favorite = 1",
        [],
        |r| r.get(0),
    )?;
    let keep = (max_items - protected).max(0);
    let deleted = conn.execute(
        "DELETE FROM items WHERE id IN (
             SELECT id FROM items
             WHERE pinned = 0 AND favorite = 0
             ORDER BY updated_at DESC, id DESC
             LIMIT -1 OFFSET ?1
         )",
        [keep],
    )?;
    Ok(deleted)
}

/// 库里现有条目数，供启动日志与面板显示。
pub fn count() -> Option<i64> {
    let conn = db()?.lock().ok()?;
    items::count(&conn).ok()
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::db::items::upsert;
    use crate::db::search::{Filter, Page, query};

    #[test]
    fn text_is_stored_once_and_deduped_after() {
        let conn = db::open_memory().unwrap();
        let cap = Captured {
            text: Some("接线的第一段文本".to_owned()),
            ..Default::default()
        };
        let first = record(&conn, &cap).unwrap();
        assert_eq!(first.len(), 1);
        assert!(matches!(first[0].result, UpsertResult::Inserted(_)));
        assert_eq!(first[0].kind, ItemKind::Text);
        assert!(first[0].label().starts_with("INSERTED"));

        for _ in 0..2 {
            let again = record(&conn, &cap).unwrap();
            assert!(matches!(again[0].result, UpsertResult::Deduped(_)));
            assert!(again[0].label().starts_with("DEDUPED"));
        }
        assert_eq!(items::count(&conn).unwrap(), 1);
        let item = items::get(&conn, first[0].result.id()).unwrap().unwrap();
        assert_eq!(item.use_count, 3);
    }

    #[test]
    fn empty_capture_writes_nothing() {
        let conn = db::open_memory().unwrap();
        assert!(record(&conn, &Captured::default()).unwrap().is_empty());
        assert!(record(
            &conn,
            &Captured {
                text: Some(String::new()),
                files: vec![],
            }
        )
        .unwrap()
        .is_empty());
        assert_eq!(items::count(&conn).unwrap(), 0);
    }

    #[test]
    fn files_take_priority_and_record_types() {
        let dir = std::env::temp_dir().join(format!("clipnest-store-{}", crate::model::now()));
        std::fs::create_dir_all(&dir).unwrap();
        let file = dir.join("a.txt");
        std::fs::write(&file, b"x").unwrap();

        let conn = db::open_memory().unwrap();
        let cap = Captured {
            // 复制文件时系统还会给一段路径文本，不该额外入库
            text: Some(format!("{}", file.display())),
            files: vec![dir.display().to_string(), file.display().to_string()],
        };
        let stored = record(&conn, &cap).unwrap();
        assert_eq!(stored.len(), 1);
        let item = items::get(&conn, stored[0].result.id()).unwrap().unwrap();
        assert_eq!(item.kind, ItemKind::Files);
        assert_eq!(item.file_types.as_deref(), Some("df"), "目录 d、文件 f，顺序跟随路径");
        assert_eq!(item.content.lines().count(), 2);

        let hit = query(&conn, "a.txt", &Filter::default(), Page::default()).unwrap();
        assert_eq!(hit.len(), 1, "文件名要能被搜到");
        let _ = std::fs::remove_dir_all(&dir);
    }

    #[test]
    fn prune_keeps_the_newest_and_protects_pinned_and_favorite() {
        let conn = db::open_memory().unwrap();
        let mut ids = Vec::new();
        for i in 0..10 {
            ids.push(
                upsert(&conn, &NewItem::text(format!("条目{i}"))).unwrap().id(),
            );
            std::thread::sleep(std::time::Duration::from_millis(2));
        }
        // 最旧的两条：一条置顶、一条收藏
        items::update_meta(
            &conn,
            ids[0],
            &crate::db::items::MetaPatch {
                pinned: Some(true),
                ..Default::default()
            },
        )
        .unwrap();
        items::update_meta(
            &conn,
            ids[1],
            &crate::db::items::MetaPatch {
                favorite: Some(true),
                ..Default::default()
            },
        )
        .unwrap();

        // 上限 4：两条受保护 + 最新的两条
        assert_eq!(prune(&conn, 4).unwrap(), 6);
        let left = query(&conn, "", &Filter::default(), Page::new(100, 0)).unwrap();
        assert_eq!(left.len(), 4);
        let kept: Vec<i64> = left.iter().map(|i| i.id).collect();
        assert!(kept.contains(&ids[0]) && kept.contains(&ids[1]), "受保护的不能删");
        assert!(kept.contains(&ids[9]) && kept.contains(&ids[8]), "最新的要留");

        // 0 = 不限；超过上限的受保护条目也不会删
        assert_eq!(prune(&conn, 0).unwrap(), 0);
        assert_eq!(prune(&conn, 1).unwrap(), 2, "名额被受保护条目占满时只删其余的");
        assert_eq!(items::count(&conn).unwrap(), 2);
    }

    #[test]
    fn open_creates_dirs_and_reopen_keeps_rows() {
        let root = std::env::temp_dir().join(format!("clipnest-store-open-{}", crate::model::now()));
        let paths = Paths::from_root(&root);
        let cap = Captured {
            text: Some("重启后还要在".to_owned()),
            ..Default::default()
        };
        {
            let conn = open(&paths).unwrap();
            record(&conn, &cap).unwrap();
        }
        assert!(paths.db().exists());
        assert!(root.join("images/origin").is_dir());
        let conn = open(&paths).unwrap();
        assert_eq!(items::count(&conn).unwrap(), 1, "落盘后重开还在");
        let _ = std::fs::remove_dir_all(&root);
    }
}
