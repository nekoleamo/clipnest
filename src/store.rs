//! 采集结果落库：把剪贴板快照翻译成条目并写进数据库。
//!
//! 这一层只做「存」，不含 UI；面板读库走 `db::search`。
//! 契约 3：文件条目的 `content` 是换行分隔的路径，`file_types` 是 `d`/`f` 串。
//! 目前只落 `text` 与 `files`，图片要等 P3 的缩略图管线。

use std::path::PathBuf;
use std::sync::{Mutex, OnceLock};

use rusqlite::{Connection, OptionalExtension};

use crate::db;
use crate::db::items::{self, UpsertResult};
use crate::clipboard::ImageBytes;
use crate::model::{ItemKind, NewItem};
use crate::paths::Paths;

/// 一次采集里值得入库的内容。
#[derive(Debug, Clone, Default, PartialEq, Eq)]
pub struct Captured {
    /// 纯文本 flavor。
    pub text: Option<String>,
    /// 文件 flavor 的路径列表。
    pub files: Vec<String>,
    /// 图片 flavor（PNG 字节 + 尺寸）。
    pub image: Option<ImageBytes>,
}

impl Captured {
    pub fn is_empty(&self) -> bool {
        self.text.is_none() && self.files.is_empty() && self.image.is_none()
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
///
/// 图片优先：复制截图时系统同时给文本/位图 flavor，只留图片条目。
pub fn record(conn: &Connection, cap: &Captured) -> rusqlite::Result<Vec<Stored>> {
    record_at(crate::paths(), conn, cap)
}

/// [`record`] 指定数据根（测试用临时目录）。
pub fn record_at(
    paths: &Paths,
    conn: &Connection,
    cap: &Captured,
) -> rusqlite::Result<Vec<Stored>> {
    let mut out = Vec::new();
    if let Some(image) = &cap.image
        && let Ok(item) = save_image(paths, image)
    {
        let result = items::upsert(conn, &item)?;
        out.push(Stored {
            kind: item.kind,
            result,
        });
        return Ok(out);
    }
    for item in cap.to_new_items() {
        let result = items::upsert(conn, &item)?;
        out.push(Stored {
            kind: item.kind,
            result,
        });
    }
    Ok(out)
}

/// 缩略图最长边（面板里显示用）。
pub const THUMBNAIL_MAX: u32 = 320;

/// 把图片写进 `images/origin` 与 `images/thumbnails`，返回要入库的条目。
///
/// 文件名就是图片内容的 blake3：同一张图再复制一次不会产生新文件。
fn save_image(paths: &Paths, image: &ImageBytes) -> std::io::Result<NewItem> {
    let hash = blake3::hash(&image.png).to_hex().to_string();
    let name = crate::paths::image_name(&hash);
    let (origin, thumb) = paths.ensure_image_dirs(&hash)?;

    if !origin.exists() {
        std::fs::write(&origin, &image.png)?;
    }
    if !thumb.exists() {
        // 小图不缩：直接拷原图字节。让 `image` 重新编码反而可能更大
        // （实测 128×128：AppKit 编码 3.8K，`image` 重编码 39K）。
        let small = image.width.max(image.height) <= THUMBNAIL_MAX;
        let result = if small {
            std::fs::copy(&origin, &thumb).map(|_| ())
        } else {
            write_thumbnail(&image.png, &thumb)
        };
        if let Err(e) = result {
            // 缩略图失败不影响原图与入库（面板退化成显示尺寸）。
            crate::logs::line(format!("THUMB ERROR {e}"));
        }
    }
    Ok(NewItem::image(
        name,
        i64::from(image.width),
        i64::from(image.height),
    ))
}

/// 生成缩略图（等比缩到 [`THUMBNAIL_MAX`] 以内）。
pub(crate) fn write_thumbnail(png: &[u8], to: &std::path::Path) -> std::io::Result<()> {
    let img = image::load_from_memory(png).map_err(std::io::Error::other)?;
    let small = img.thumbnail(THUMBNAIL_MAX, THUMBNAIL_MAX);
    small
        .save_with_format(to, image::ImageFormat::Png)
        .map_err(std::io::Error::other)
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

/// 删一条：库里删掉，图片条目连 `images/` 里的原图与缩略图一起删。
///
/// 图片文件名就是内容哈希，所以同一张图只有一条记录、一组文件，删了不会误伤别的条目。
pub fn remove(conn: &Connection, id: i64) -> rusqlite::Result<bool> {
    remove_at(crate::paths(), conn, id)
}

/// [`remove`] 指定数据根（测试用）。
pub fn remove_at(paths: &Paths, conn: &Connection, id: i64) -> rusqlite::Result<bool> {
    let image: Option<String> = conn
        .query_row(
            "SELECT content FROM items WHERE id = ?1 AND kind = 'image'",
            [id],
            |r| r.get(0),
        )
        .optional()?;
    if !items::delete(conn, id)? {
        return Ok(false);
    }
    if let Some(content) = image {
        delete_image_files_at(paths, &content);
    }
    Ok(true)
}

/// 删掉某个图片条目的原图与缩略图（文件不在就跳过）。
fn delete_image_files_at(paths: &Paths, content: &str) {
    let hash = content.strip_suffix(".png").unwrap_or(content);
    for path in [paths.image_origin(hash), paths.image_thumbnail(hash)] {
        match std::fs::remove_file(&path) {
            Ok(()) => {}
            Err(e) if e.kind() == std::io::ErrorKind::NotFound => {}
            Err(e) => crate::logs::line(format!("IMAGE delete failed {}: {e}", path.display())),
        }
    }
}

/// 扫 `images/` 里没有对应条目的孤儿文件（历史遗留、导入失败留下的）并删掉。
///
/// 返回删除的文件数。启动时跑一次就够了，成本是一次目录遍历。
pub fn collect_garbage(conn: &Connection) -> rusqlite::Result<usize> {
    collect_garbage_at(crate::paths(), conn)
}

/// [`collect_garbage`] 指定数据根（测试用）。
pub fn collect_garbage_at(paths: &Paths, conn: &Connection) -> rusqlite::Result<usize> {
    let mut wanted: std::collections::HashSet<String> = std::collections::HashSet::new();
    let mut stmt = conn.prepare("SELECT content FROM items WHERE kind = 'image'")?;
    let rows = stmt.query_map([], |r| r.get::<_, String>(0))?;
    for row in rows {
        let content = row?;
        wanted.insert(content.strip_suffix(".png").unwrap_or(&content).to_owned());
    }

    let mut removed = 0;
    for dir in ["origin", "thumbnails"] {
        let root = paths.images_dir().join(dir);
        let Ok(shards) = std::fs::read_dir(&root) else {
            continue;
        };
        for shard in shards.flatten() {
            let Ok(files) = std::fs::read_dir(shard.path()) else {
                continue;
            };
            for file in files.flatten() {
                let name = file.file_name().to_string_lossy().to_string();
                let hash = name.strip_suffix(".png").unwrap_or(&name);
                if wanted.contains(hash) {
                    continue;
                }
                if std::fs::remove_file(file.path()).is_ok() {
                    removed += 1;
                }
            }
        }
    }
    Ok(removed)
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
    // 先取出要删的 id：删条目要连带删图片文件（`remove` 里做）。
    let victims: Vec<i64> = {
        let mut stmt = conn.prepare(
            "SELECT id FROM items
             WHERE pinned = 0 AND favorite = 0
             ORDER BY updated_at DESC, id DESC
             LIMIT -1 OFFSET ?1",
        )?;
        let rows = stmt.query_map([keep], |r| r.get::<_, i64>(0))?;
        rows.collect::<rusqlite::Result<Vec<_>>>()?
    };
    let mut deleted = 0;
    for id in victims {
        if remove(conn, id)? {
            deleted += 1;
        }
    }
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
                image: None,
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
            image: None,
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

    /// 造一张纯色 PNG，够测落盘与缩略图。
    fn png_of(w: u32, h: u32, rgba: [u8; 4]) -> Vec<u8> {
        let img = image::RgbaImage::from_pixel(w, h, image::Rgba(rgba));
        let mut out = Vec::new();
        img.write_to(&mut std::io::Cursor::new(&mut out), image::ImageFormat::Png)
            .unwrap();
        out
    }

    #[test]
    fn image_is_saved_with_thumbnail_and_deduped() {
        let root = std::env::temp_dir().join(format!("clipnest-store-img-{}", crate::model::now()));
        let paths = Paths::from_root(&root);
        let conn = db::open_memory().unwrap();

        let png = png_of(640, 480, [255, 0, 0, 255]);
        let cap = Captured {
            image: Some(ImageBytes {
                png: png.clone(),
                width: 640,
                height: 480,
            }),
            // 复制截图时系统也给文本 flavor，图片优先
            text: Some("不该入库的文本".to_owned()),
            ..Default::default()
        };

        let stored = record_at(&paths, &conn, &cap).unwrap();
        assert_eq!(stored.len(), 1, "图片优先，不再落文本");
        assert_eq!(stored[0].kind, ItemKind::Image);

        let item = items::get(&conn, stored[0].result.id()).unwrap().unwrap();
        assert_eq!(item.width, Some(640));
        assert_eq!(item.height, Some(480));
        let hash = item.content.strip_suffix(".png").unwrap();
        assert_eq!(hash.len(), 64, "文件名就是图片内容的 blake3");

        let origin = paths.image_origin(hash);
        let thumb = paths.image_thumbnail(hash);
        assert!(origin.exists(), "原图要落盘");
        assert_eq!(std::fs::read(&origin).unwrap(), png, "原图字节不变");
        let t = image::open(&thumb).unwrap();
        assert!(t.width().max(t.height()) <= THUMBNAIL_MAX, "缩略图不超过上限");
        assert!(t.width() > 1, "缩略图不能缩成一条线");
        // 640×480 > 320 → 真缩过，字节必然不同且更小
        assert_ne!(std::fs::read(&origin).unwrap(), std::fs::read(&thumb).unwrap());

        // 同一张图再复制：只涨 use_count，不新增行、不重写文件
        let again = record_at(&paths, &conn, &cap).unwrap();
        assert!(matches!(again[0].result, UpsertResult::Deduped(_)));
        assert_eq!(items::count(&conn).unwrap(), 1);

        let _ = std::fs::remove_dir_all(&root);
    }

    #[test]
    fn small_image_thumbnail_is_not_reencoded() {
        let root = std::env::temp_dir().join(format!("clipnest-store-tiny-{}", crate::model::now()));
        let paths = Paths::from_root(&root);
        let conn = db::open_memory().unwrap();
        let png = png_of(64, 64, [0, 128, 255, 255]);
        let cap = Captured {
            image: Some(ImageBytes {
                png: png.clone(),
                width: 64,
                height: 64,
            }),
            ..Default::default()
        };
        record_at(&paths, &conn, &cap).unwrap();
        let item = items::get(&conn, 1).unwrap().unwrap();
        let hash = item.content.strip_suffix(".png").unwrap();
        let origin = std::fs::read(paths.image_origin(hash)).unwrap();
        let thumb = std::fs::read(paths.image_thumbnail(hash)).unwrap();
        assert_eq!(thumb, origin, "小图不该让 image 重编码（会更大）");
        let _ = std::fs::remove_dir_all(&root);
    }

    #[test]
    fn deleting_an_image_removes_its_files() {
        let root = std::env::temp_dir().join(format!("clipnest-store-del-{}", crate::model::now()));
        let paths = Paths::from_root(&root);
        let conn = db::open_memory().unwrap();
        let png = png_of(64, 64, [10, 20, 30, 255]);
        let cap = Captured {
            image: Some(ImageBytes {
                png,
                width: 64,
                height: 64,
            }),
            ..Default::default()
        };
        let stored = record_at(&paths, &conn, &cap).unwrap();
        let item = items::get(&conn, stored[0].result.id()).unwrap().unwrap();
        let hash = item.content.strip_suffix(".png").unwrap().to_owned();
        assert!(paths.image_origin(&hash).exists());

        assert!(remove_at(&paths, &conn, stored[0].result.id()).unwrap());
        assert!(!paths.image_origin(&hash).exists(), "原图要删掉");
        assert!(!paths.image_thumbnail(&hash).exists(), "缩略图也要删");
        assert!(items::get(&conn, stored[0].result.id()).unwrap().is_none());
        // 删第二次返回 false，不报错
        assert!(!remove_at(&paths, &conn, stored[0].result.id()).unwrap());
        let _ = std::fs::remove_dir_all(&root);
    }

    #[test]
    fn garbage_collector_removes_orphan_images() {
        let root = std::env::temp_dir().join(format!("clipnest-store-gc-{}", crate::model::now()));
        let paths = Paths::from_root(&root);
        let conn = db::open_memory().unwrap();
        // 一条有记录的图 + 一个没人引用的孤儿文件
        let png = png_of(32, 32, [1, 2, 3, 255]);
        record_at(
            &paths,
            &conn,
            &Captured {
                image: Some(ImageBytes {
                    png,
                    width: 32,
                    height: 32,
                }),
                ..Default::default()
            },
        )
        .unwrap();
        let orphan_hash = "ff".repeat(32);
        let (origin, thumb) = paths.ensure_image_dirs(&orphan_hash).unwrap();
        std::fs::write(&origin, b"stale").unwrap();
        std::fs::write(&thumb, b"stale").unwrap();

        let removed = collect_garbage_at(&paths, &conn).unwrap();
        assert_eq!(removed, 2, "两个孤儿文件都要清掉");
        assert!(!origin.exists());
        // 有记录的图要留着
        let item = items::get(&conn, 1).unwrap().unwrap();
        let hash = item.content.strip_suffix(".png").unwrap();
        assert!(paths.image_origin(hash).exists());
        let _ = std::fs::remove_dir_all(&root);
    }

    #[test]
    fn broken_image_bytes_do_not_panic() {
        let root = std::env::temp_dir().join(format!("clipnest-store-bad-{}", crate::model::now()));
        let paths = Paths::from_root(&root);
        let conn = db::open_memory().unwrap();
        let cap = Captured {
            image: Some(ImageBytes {
                png: b"not a png".to_vec(),
                width: 10,
                height: 10,
            }),
            text: Some("回退到文本".to_owned()),
            ..Default::default()
        };
        // 原图能落盘（字节不校验），缩略图失败只记日志；条目仍然入库
        let stored = record_at(&paths, &conn, &cap).unwrap();
        assert_eq!(stored.len(), 1);
        assert_eq!(stored[0].kind, ItemKind::Image);
        let _ = std::fs::remove_dir_all(&root);
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
