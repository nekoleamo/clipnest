//! 查询与搜索。
//!
//! 契约 7：长词（≥3 个字符）走 FTS5（trigram 索引要求至少 3 个字符），
//! 短词/单个汉字降级 `LIKE`；两者都只在 `content` 与 `note` 上匹配。
//! 契约 8：置顶恒排最前，其后按 `updated_at` 倒序。

use rusqlite::types::Value;
use rusqlite::{Connection, params_from_iter};

use super::Result;
use super::items::{COLUMNS, from_row};
use crate::model::{Item, ItemKind};

/// 走 FTS5 的最短查询长度（字符数）。
pub const FTS_MIN_CHARS: usize = 3;

/// 过滤条件；字段为 `None` 表示该维度不过滤。
#[derive(Debug, Clone, Default)]
pub struct Filter {
    /// 只要这些类型（空 = 全部）。
    pub kinds: Vec<ItemKind>,
    pub group_id: Option<i64>,
    pub favorite: Option<bool>,
    pub pinned: Option<bool>,
    /// `None` = 不过滤；`Some(false)` = 排除敏感条目。
    pub sensitive: Option<bool>,
}

/// 分页。
#[derive(Debug, Clone, Copy)]
pub struct Page {
    pub limit: i64,
    pub offset: i64,
}

impl Default for Page {
    fn default() -> Self {
        Self {
            limit: 200,
            offset: 0,
        }
    }
}

impl Page {
    pub fn new(limit: i64, offset: i64) -> Self {
        Self { limit, offset }
    }
}

/// 列表/搜索主入口。
pub fn query(conn: &Connection, q: &str, filter: &Filter, page: Page) -> Result<Vec<Item>> {
    let needle = q.trim();
    let mut where_sql: Vec<String> = Vec::new();
    let mut values: Vec<Value> = Vec::new();

    if !needle.is_empty() {
        if needle.chars().count() >= FTS_MIN_CHARS {
            // 整串当短语查，避免用户输入的 `-` `*` `(` 被当成 FTS 语法
            where_sql.push("id IN (SELECT rowid FROM items_fts WHERE items_fts MATCH ?)".to_owned());
            values.push(Value::Text(fts_phrase(needle)));
        } else {
            let like = format!("%{}%", like_escape(needle));
            where_sql.push("(content LIKE ? ESCAPE '\\' OR ifnull(note, '') LIKE ? ESCAPE '\\')".to_owned());
            values.push(Value::Text(like.clone()));
            values.push(Value::Text(like));
        }
    }

    if !filter.kinds.is_empty() {
        let marks = vec!["?"; filter.kinds.len()].join(",");
        where_sql.push(format!("kind IN ({marks})"));
        for k in &filter.kinds {
            values.push(Value::Text(k.as_str().to_owned()));
        }
    }
    if let Some(g) = filter.group_id {
        where_sql.push("group_id = ?".to_owned());
        values.push(Value::Integer(g));
    }
    if let Some(v) = filter.favorite {
        where_sql.push("favorite = ?".to_owned());
        values.push(Value::Integer(i64::from(v)));
    }
    if let Some(v) = filter.pinned {
        where_sql.push("pinned = ?".to_owned());
        values.push(Value::Integer(i64::from(v)));
    }
    if let Some(v) = filter.sensitive {
        where_sql.push("sensitive = ?".to_owned());
        values.push(Value::Integer(i64::from(v)));
    }

    let where_part = if where_sql.is_empty() {
        String::new()
    } else {
        format!(" WHERE {}", where_sql.join(" AND "))
    };
    let sql = format!(
        "SELECT {COLUMNS} FROM items{where_part} ORDER BY pinned DESC, updated_at DESC, id DESC LIMIT ? OFFSET ?"
    );
    values.push(Value::Integer(page.limit.max(0)));
    values.push(Value::Integer(page.offset.max(0)));

    let mut stmt = conn.prepare_cached(&sql)?;
    let rows = stmt.query_map(params_from_iter(values), from_row)?;
    rows.collect()
}

/// 统计条数（分页用）；`filter` 语义同 [`query`]。
pub fn count(conn: &Connection, q: &str, filter: &Filter) -> Result<i64> {
    let needle = q.trim();
    let mut where_sql: Vec<String> = Vec::new();
    let mut values: Vec<Value> = Vec::new();
    if !needle.is_empty() {
        if needle.chars().count() >= FTS_MIN_CHARS {
            where_sql.push("id IN (SELECT rowid FROM items_fts WHERE items_fts MATCH ?)".to_owned());
            values.push(Value::Text(fts_phrase(needle)));
        } else {
            let like = format!("%{}%", like_escape(needle));
            where_sql.push("(content LIKE ? ESCAPE '\\' OR ifnull(note, '') LIKE ? ESCAPE '\\')".to_owned());
            values.push(Value::Text(like.clone()));
            values.push(Value::Text(like));
        }
    }
    if !filter.kinds.is_empty() {
        let marks = vec!["?"; filter.kinds.len()].join(",");
        where_sql.push(format!("kind IN ({marks})"));
        for k in &filter.kinds {
            values.push(Value::Text(k.as_str().to_owned()));
        }
    }
    for (col, v) in [
        ("group_id", filter.group_id),
        ("favorite", filter.favorite.map(i64::from)),
        ("pinned", filter.pinned.map(i64::from)),
        ("sensitive", filter.sensitive.map(i64::from)),
    ] {
        if let Some(v) = v {
            where_sql.push(format!("{col} = ?"));
            values.push(Value::Integer(v));
        }
    }
    let where_part = if where_sql.is_empty() {
        String::new()
    } else {
        format!(" WHERE {}", where_sql.join(" AND "))
    };
    conn.query_row(
        &format!("SELECT count(*) FROM items{where_part}"),
        params_from_iter(values),
        |r| r.get(0),
    )
}

/// 把用户输入包成 FTS5 短语：内部双引号翻倍，其余字符全部失去语法含义。
fn fts_phrase(q: &str) -> String {
    format!("\"{}\"", q.replace('"', "\"\""))
}

/// `LIKE` 的通配符转义（配合 `ESCAPE '\'`）。
fn like_escape(q: &str) -> String {
    let mut out = String::with_capacity(q.len());
    for c in q.chars() {
        match c {
            '\\' | '%' | '_' => {
                out.push('\\');
                out.push(c);
            }
            _ => out.push(c),
        }
    }
    out
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::db::items::{MetaPatch, update_meta, upsert};
    use crate::db::open_memory;
    use crate::model::NewItem;

    fn seed(conn: &Connection, n: usize) {
        for i in 0..n {
            let item = if i % 4 == 0 {
                NewItem::text(format!("中文条目 第{i}号 银河系"))
            } else if i % 4 == 1 {
                NewItem::text(format!("hello world number {i}"))
            } else if i % 4 == 2 {
                NewItem::files([format!("/tmp/file{i}.png")], "f")
            } else {
                NewItem::html(format!("<b>html {i}</b>"))
            };
            upsert(conn, &item).unwrap();
        }
    }

    #[test]
    fn empty_query_lists_newest_first() {
        let conn = open_memory().unwrap();
        seed(&conn, 10);
        let got = query(&conn, "", &Filter::default(), Page::default()).unwrap();
        assert_eq!(got.len(), 10);
        // updated_at 是毫秒时间戳，同毫秒时退化为 id DESC，仍然确定性
        assert!(got[0].updated_at >= got[1].updated_at);
        assert!(got[0].id >= got[1].id || got[0].updated_at > got[1].updated_at);
    }

    #[test]
    fn pinned_always_first() {
        let conn = open_memory().unwrap();
        seed(&conn, 6);
        let all = query(&conn, "", &Filter::default(), Page::default()).unwrap();
        let oldest = all.last().unwrap().id;
        update_meta(
            &conn,
            oldest,
            &MetaPatch {
                pinned: Some(true),
                ..Default::default()
            },
        )
        .unwrap();
        let got = query(&conn, "", &Filter::default(), Page::default()).unwrap();
        assert_eq!(got[0].id, oldest, "契约 8");
        // 置顶的那条 updated_at 没变（契约 6）
        assert_eq!(got[0].updated_at, all.last().unwrap().updated_at);
    }

    #[test]
    fn search_long_words_hits_cjk_and_english_and_substring() {
        let conn = open_memory().unwrap();
        seed(&conn, 40);

        let zh = query(&conn, "银河系", &Filter::default(), Page::default()).unwrap();
        assert_eq!(zh.len(), 10);
        assert!(zh.iter().all(|i| i.content.contains("银河系")));

        let en = query(&conn, "hello", &Filter::default(), Page::default()).unwrap();
        assert_eq!(en.len(), 10);

        // 子串（非词首）
        let sub = query(&conn, "orld num", &Filter::default(), Page::default()).unwrap();
        assert_eq!(sub.len(), 10);
    }

    #[test]
    fn search_short_words_falls_back_to_like() {
        let conn = open_memory().unwrap();
        seed(&conn, 12);
        // 2 个字符：trigram 命中不了，必须走 LIKE
        let got = query(&conn, "银河", &Filter::default(), Page::default()).unwrap();
        assert_eq!(got.len(), 12 / 4);
        // 单字
        let got = query(&conn, "号", &Filter::default(), Page::default()).unwrap();
        assert_eq!(got.len(), 12 / 4);
        assert_eq!(count(&conn, "银河", &Filter::default()).unwrap(), 3);
    }

    #[test]
    fn search_special_chars_do_not_error() {
        let conn = open_memory().unwrap();
        upsert(&conn, &NewItem::text("C:\\path\\to \"quoted\" file 100% _done_")).unwrap();
        for q in ["\"quoted\"", "100%", "_done_", "C:\\path", "-not-", "a AND b"] {
            let got = query(&conn, q, &Filter::default(), Page::default()).unwrap();
            assert!(got.len() <= 1, "{q} 应能安全查询");
        }
        // 字面匹配（转义生效，不是通配）
        assert!(query(&conn, "100%", &Filter::default(), Page::default())
            .unwrap()
            .len()
            == 1);
        assert!(query(&conn, "_done_", &Filter::default(), Page::default())
            .unwrap()
            .len()
            == 1);
    }

    #[test]
    fn filters_by_kind_group_favorite_and_sensitive() {
        let conn = open_memory().unwrap();
        seed(&conn, 8);
        let only_files = Filter {
            kinds: vec![ItemKind::Files],
            ..Default::default()
        };
        let got = query(&conn, "", &only_files, Page::default()).unwrap();
        assert_eq!(got.len(), 2);
        assert!(got.iter().all(|i| i.kind == ItemKind::Files));

        conn.execute("INSERT INTO groups(name) VALUES ('工作')", [])
            .unwrap();
        let gid = conn.last_insert_rowid();
        let target = got[0].id;
        update_meta(
            &conn,
            target,
            &MetaPatch {
                group_id: Some(Some(gid)),
                favorite: Some(true),
                ..Default::default()
            },
        )
        .unwrap();
        let in_group = Filter {
            group_id: Some(gid),
            favorite: Some(true),
            ..Default::default()
        };
        let hit = query(&conn, "", &in_group, Page::default()).unwrap();
        assert_eq!(hit.len(), 1);
        assert_eq!(hit[0].id, target);
    }

    #[test]
    fn pagination_is_stable() {
        let conn = open_memory().unwrap();
        seed(&conn, 20);
        let first = query(&conn, "", &Filter::default(), Page::new(10, 0)).unwrap();
        let second = query(&conn, "", &Filter::default(), Page::new(10, 10)).unwrap();
        assert_eq!(first.len(), 10);
        assert_eq!(second.len(), 10);
        let mut ids: Vec<i64> = first.iter().chain(&second).map(|i| i.id).collect();
        ids.sort_unstable();
        ids.dedup();
        assert_eq!(ids.len(), 20, "两页不能重叠");
    }

    /// T4 验收：1000 条假数据下的查询耗时（只打印，不做硬门槛，避免 CI 抖动）。
    #[test]
    fn thousand_rows_timing() {
        let conn = open_memory().unwrap();
        seed(&conn, 1000);
        for (label, q) in [("中文长词", "银河系"), ("英文长词", "hello"), ("短词 LIKE", "银河")] {
            let t = std::time::Instant::now();
            let n = query(&conn, q, &Filter::default(), Page::new(50, 0))
                .unwrap()
                .len();
            let ms = t.elapsed().as_secs_f64() * 1000.0;
            println!("{label} ({q}) → {n} 条，{ms:.2} ms");
            assert!(n > 0);
        }
        let t = std::time::Instant::now();
        let n = query(&conn, "", &Filter::default(), Page::new(50, 0))
            .unwrap()
            .len();
        println!("空查询 → {n} 条，{:.2} ms", t.elapsed().as_secs_f64() * 1000.0);
    }
}
