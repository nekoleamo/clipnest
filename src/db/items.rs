//! 条目入库与去重。
//!
//! 契约 5：去重命中只 `use_count + 1` 并刷新 `updated_at`。
//! 契约 6：收藏/置顶/备注/分组的变更**不**刷新 `updated_at`（走 [`update_meta`]）。

use rusqlite::{Connection, OptionalExtension, Row, params};

use super::Result;
use crate::model::{Item, ItemKind, NewItem, now};

/// `items` 全列，顺序与 [`from_row`] 一致。
pub(crate) const COLUMNS: &str = "id, content_hash, kind, content, file_types, width, height, \
                       use_count, pinned, favorite, note, group_id, sensitive, \
                       created_at, updated_at";

/// 入库结果。
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum UpsertResult {
    /// 新条目，附带 id。
    Inserted(i64),
    /// 命中已有条目（去重），附带 id。
    Deduped(i64),
}

impl UpsertResult {
    pub fn id(self) -> i64 {
        match self {
            UpsertResult::Inserted(id) | UpsertResult::Deduped(id) => id,
        }
    }
}

/// 入库一条：以 `content_hash` 为唯一键 upsert。
///
/// 命中时只动 `use_count` 和 `updated_at`，其余字段（含收藏、备注、分组）保持用户改过的值。
pub fn upsert(conn: &Connection, item: &NewItem) -> Result<UpsertResult> {
    let hash = item.hash();
    let ts = now();
    let tx = conn.unchecked_transaction()?;

    let found: Option<i64> = tx
        .query_row("SELECT id FROM items WHERE content_hash = ?1", [&hash], |r| {
            r.get(0)
        })
        .optional()?;

    let result = match found {
        Some(id) => {
            tx.execute(
                "UPDATE items SET use_count = use_count + 1, updated_at = ?1 WHERE id = ?2",
                params![ts, id],
            )?;
            UpsertResult::Deduped(id)
        }
        None => {
            tx.execute(
                "INSERT INTO items(content_hash, kind, content, file_types, width, height,
                                    use_count, pinned, favorite, note, group_id, sensitive,
                                    created_at, updated_at)
                 VALUES (?1, ?2, ?3, ?4, ?5, ?6, 1, 0, 0, NULL, NULL, ?7, ?8, ?8)",
                params![
                    hash,
                    item.kind.as_str(),
                    item.content,
                    item.file_types,
                    item.width,
                    item.height,
                    i64::from(item.sensitive),
                    ts,
                ],
            )?;
            UpsertResult::Inserted(tx.last_insert_rowid())
        }
    };

    tx.commit()?;
    Ok(result)
}

/// 元数据补丁：只写出现的字段。
///
/// `note` / `group_id` 用双层 `Option` 表达「不改」与「清空」。
#[derive(Debug, Clone, Default)]
pub struct MetaPatch {
    pub pinned: Option<bool>,
    pub favorite: Option<bool>,
    pub note: Option<Option<String>>,
    pub group_id: Option<Option<i64>>,
    pub sensitive: Option<bool>,
}

impl MetaPatch {
    pub fn is_empty(&self) -> bool {
        self.pinned.is_none()
            && self.favorite.is_none()
            && self.note.is_none()
            && self.group_id.is_none()
            && self.sensitive.is_none()
    }
}

/// 契约 6：改元数据，**不**碰 `updated_at`，避免长期收藏被顶到列表最前。
pub fn update_meta(conn: &Connection, id: i64, patch: &MetaPatch) -> Result<()> {
    if patch.is_empty() {
        return Ok(());
    }
    // 动态 SQL 只拼列名（全是代码内的常量），值一律走参数。
    let mut sets: Vec<&str> = Vec::new();
    let mut values: Vec<rusqlite::types::Value> = Vec::new();
    if let Some(v) = patch.pinned {
        sets.push("pinned = ?");
        values.push(i64::from(v).into());
    }
    if let Some(v) = patch.favorite {
        sets.push("favorite = ?");
        values.push(i64::from(v).into());
    }
    if let Some(v) = patch.sensitive {
        sets.push("sensitive = ?");
        values.push(i64::from(v).into());
    }
    if let Some(v) = &patch.note {
        sets.push("note = ?");
        values.push(v.clone().into());
    }
    if let Some(v) = patch.group_id {
        sets.push("group_id = ?");
        values.push(v.into());
    }
    values.push(id.into());

    let sql = format!("UPDATE items SET {} WHERE id = ?", sets.join(", "));
    conn.execute(&sql, rusqlite::params_from_iter(values))?;
    Ok(())
}

/// 按 id 取一条。
pub fn get(conn: &Connection, id: i64) -> Result<Option<Item>> {
    let sql = format!("SELECT {COLUMNS} FROM items WHERE id = ?1");
    conn.query_row(&sql, [id], from_row).optional()
}

/// 删一条（图片文件等资源由调用方清理）。
pub fn delete(conn: &Connection, id: i64) -> Result<bool> {
    Ok(conn.execute("DELETE FROM items WHERE id = ?1", [id])? > 0)
}

/// 总条数。
pub fn count(conn: &Connection) -> Result<i64> {
    conn.query_row("SELECT count(*) FROM items", [], |r| r.get(0))
}

/// 数据库行 → [`Item`]。
pub fn from_row(row: &Row<'_>) -> Result<Item> {
    let kind: String = row.get(2)?;
    let kind = ItemKind::parse(&kind).ok_or_else(|| {
        rusqlite::Error::FromSqlConversionFailure(
            2,
            rusqlite::types::Type::Text,
            format!("未知的 kind: {kind}").into(),
        )
    })?;
    Ok(Item {
        id: row.get(0)?,
        content_hash: row.get(1)?,
        kind,
        content: row.get(3)?,
        file_types: row.get(4)?,
        width: row.get(5)?,
        height: row.get(6)?,
        use_count: row.get(7)?,
        pinned: row.get::<_, i64>(8)? != 0,
        favorite: row.get::<_, i64>(9)? != 0,
        note: row.get(10)?,
        group_id: row.get(11)?,
        sensitive: row.get::<_, i64>(12)? != 0,
        created_at: row.get(13)?,
        updated_at: row.get(14)?,
    })
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::db::open_memory;

    #[test]
    fn same_content_three_times_is_one_row() {
        let conn = open_memory().unwrap();
        let mut ids = Vec::new();
        for _ in 0..3 {
            ids.push(upsert(&conn, &NewItem::text("复制的同一句话")).unwrap());
            std::thread::sleep(std::time::Duration::from_millis(2));
        }
        assert!(matches!(ids[0], UpsertResult::Inserted(_)));
        assert_eq!(ids[1], UpsertResult::Deduped(ids[0].id()));
        assert_eq!(ids[2], UpsertResult::Deduped(ids[0].id()));
        assert_eq!(count(&conn).unwrap(), 1);

        let item = get(&conn, ids[0].id()).unwrap().expect("row");
        assert_eq!(item.use_count, 3);
        assert!(item.updated_at > item.created_at, "去重要刷新 updated_at");
    }

    #[test]
    fn same_content_different_kind_is_two_rows() {
        let conn = open_memory().unwrap();
        let a = upsert(&conn, &NewItem::text("<b>x</b>")).unwrap();
        let b = upsert(&conn, &NewItem::html("<b>x</b>")).unwrap();
        assert!(matches!(a, UpsertResult::Inserted(_)));
        assert!(matches!(b, UpsertResult::Inserted(_)));
        assert_ne!(a.id(), b.id());
        assert_eq!(count(&conn).unwrap(), 2);
    }

    /// 契约 6 + 去重后用户改过的元数据不被覆盖。
    #[test]
    fn meta_updates_keep_updated_at_and_survive_dedup() {
        let conn = open_memory().unwrap();
        let id = upsert(&conn, &NewItem::text("要被复制的文本")).unwrap().id();
        let before = get(&conn, id).unwrap().unwrap();

        update_meta(
            &conn,
            id,
            &MetaPatch {
                pinned: Some(true),
                favorite: Some(true),
                note: Some(Some("重要".to_owned())),
                ..Default::default()
            },
        )
        .unwrap();

        let mid = get(&conn, id).unwrap().unwrap();
        assert!(mid.pinned && mid.favorite);
        assert_eq!(mid.note.as_deref(), Some("重要"));
        assert_eq!(mid.updated_at, before.updated_at, "契约 6：元数据不刷新 updated_at");

        // 再次入库：use_count 增加，但收藏/备注保持
        std::thread::sleep(std::time::Duration::from_millis(2));
        assert!(matches!(
            upsert(&conn, &NewItem::text("要被复制的文本")).unwrap(),
            UpsertResult::Deduped(_)
        ));
        let after = get(&conn, id).unwrap().unwrap();
        assert_eq!(after.use_count, 2);
        assert!(after.pinned && after.favorite);
        assert_eq!(after.note.as_deref(), Some("重要"));
        assert!(after.updated_at > mid.updated_at);
    }

    #[test]
    fn note_and_group_can_be_cleared() {
        let conn = open_memory().unwrap();
        conn.execute("INSERT INTO groups(name) VALUES ('工作')", [])
            .unwrap();
        let gid = conn.last_insert_rowid();
        let id = upsert(&conn, &NewItem::text("x")).unwrap().id();
        update_meta(
            &conn,
            id,
            &MetaPatch {
                note: Some(Some("n".to_owned())),
                group_id: Some(Some(gid)),
                ..Default::default()
            },
        )
        .unwrap();
        update_meta(
            &conn,
            id,
            &MetaPatch {
                note: Some(None),
                group_id: Some(None),
                ..Default::default()
            },
        )
        .unwrap();
        let item = get(&conn, id).unwrap().unwrap();
        assert_eq!(item.note, None);
        assert_eq!(item.group_id, None);

        // 空补丁是空操作，不报错
        update_meta(&conn, id, &MetaPatch::default()).unwrap();
    }

    #[test]
    fn image_and_files_round_trip() {
        let conn = open_memory().unwrap();
        let img = upsert(&conn, &NewItem::image("abc.png", 1920, 1080)).unwrap();
        let item = get(&conn, img.id()).unwrap().unwrap();
        assert_eq!(item.kind, ItemKind::Image);
        assert_eq!(item.content, "abc.png");
        assert_eq!((item.width, item.height), (Some(1920), Some(1080)));

        let files = upsert(
            &conn,
            &NewItem::files(["/tmp/a.png", "/tmp/b"], "fd").sensitive(true),
        )
        .unwrap();
        let item = get(&conn, files.id()).unwrap().unwrap();
        assert_eq!(item.kind, ItemKind::Files);
        assert_eq!(item.content, "/tmp/a.png\n/tmp/b");
        assert_eq!(item.file_types.as_deref(), Some("fd"));
        assert!(item.sensitive);
    }

    #[test]
    fn delete_removes_row_and_fts_entry() {
        let conn = open_memory().unwrap();
        let id = upsert(&conn, &NewItem::text("待删除条目")).unwrap().id();
        assert!(delete(&conn, id).unwrap());
        assert!(get(&conn, id).unwrap().is_none());
        assert!(!delete(&conn, id).unwrap(), "重复删返回 false");
        let n: i64 = conn
            .query_row(
                "SELECT count(*) FROM items_fts WHERE items_fts MATCH '待删除'",
                [],
                |r| r.get(0),
            )
            .unwrap();
        assert_eq!(n, 0);
    }
}
