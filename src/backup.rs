//! 加密备份（自有格式 v1）。
//!
//! 容器字节布局：
//!
//! ```text
//! "CNST1" (5) | salt (16) | nonce (12) | ciphertext + tag
//! ```
//!
//! - key = argon2id(passphrase, salt, 32B)（`Argon2::default()`：Argon2id v19，m=19MiB，t=2，p=1）
//! - 明文是 JSON（[`Payload`]：版本 + 全部 items/groups）
//! - AAD 绑定 magic，格式头被改过会解密失败
//!
//! 只承诺本程序自己的格式；EcoPaste `.ecopastebak` 兼容留到 P6 再评估。

use std::collections::HashMap;
use std::path::Path;

use chacha20poly1305::aead::{Aead, KeyInit, Payload as AeadPayload};
use chacha20poly1305::{ChaCha20Poly1305, Key, Nonce};
use rusqlite::{Connection, OptionalExtension, params};
use serde::{Deserialize, Serialize};

use crate::model::{Group, Item, now};

/// 容器 magic（同时作为 AEAD 的 AAD）。
const MAGIC: &[u8; 5] = b"CNST1";
const SALT_LEN: usize = 16;
const NONCE_LEN: usize = 12;
const KEY_LEN: usize = 32;

/// 明文 JSON 的格式版本。
pub const FORMAT_VERSION: u32 = 1;

/// 备份/恢复的错误。
#[derive(Debug)]
pub enum Error {
    Io(std::io::Error),
    Db(rusqlite::Error),
    Json(serde_json::Error),
    /// 口令错误，或文件被截断/篡改（AEAD 无法区分，故意合并成一种）。
    Crypto,
    /// 不是 clipnest 备份，或格式版本不支持。
    Format(String),
}

impl std::fmt::Display for Error {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        match self {
            Error::Io(e) => write!(f, "读写备份文件失败：{e}"),
            Error::Db(e) => write!(f, "数据库错误：{e}"),
            Error::Json(e) => write!(f, "备份内容损坏：{e}"),
            Error::Crypto => write!(f, "口令错误，或备份文件已损坏"),
            Error::Format(s) => write!(f, "备份格式不支持：{s}"),
        }
    }
}

impl std::error::Error for Error {}

impl From<std::io::Error> for Error {
    fn from(e: std::io::Error) -> Self {
        Error::Io(e)
    }
}
impl From<rusqlite::Error> for Error {
    fn from(e: rusqlite::Error) -> Self {
        Error::Db(e)
    }
}
impl From<serde_json::Error> for Error {
    fn from(e: serde_json::Error) -> Self {
        Error::Json(e)
    }
}

pub type Result<T> = std::result::Result<T, Error>;

/// 备份里的明文内容。
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct Payload {
    /// [`FORMAT_VERSION`]
    pub format: u32,
    pub app: String,
    pub created_at: String,
    pub items: Vec<Item>,
    pub groups: Vec<Group>,
}

/// 导入结果。
#[derive(Debug, Clone, Copy, Default, PartialEq, Eq)]
pub struct ImportReport {
    /// 新写入的条目数。
    pub inserted: usize,
    /// 已存在（`content_hash` 相同）而跳过的条目数。
    pub skipped: usize,
    /// 新建的分组数。
    pub groups_inserted: usize,
}

/// 导出整库到 `path`（先写临时文件再改名，避免半个文件）。
pub fn export(conn: &Connection, path: &Path, passphrase: &str) -> Result<()> {
    let blob = export_bytes(conn, passphrase)?;
    if let Some(dir) = path.parent()
        && !dir.as_os_str().is_empty()
    {
        std::fs::create_dir_all(dir)?;
    }
    let tmp = path.with_extension("tmp");
    std::fs::write(&tmp, &blob)?;
    std::fs::rename(&tmp, path)?;
    Ok(())
}

/// 导出并返回容器字节（测试与同步载荷复用）。
pub fn export_bytes(conn: &Connection, passphrase: &str) -> Result<Vec<u8>> {
    let payload = collect(conn)?;
    let plain = serde_json::to_vec(&payload)?;
    seal(&plain, passphrase)
}

/// 从 `path` 导入：只补齐缺失条目，不覆盖本地已有的同哈希条目。
pub fn import(conn: &Connection, path: &Path, passphrase: &str) -> Result<ImportReport> {
    let blob = std::fs::read(path)?;
    import_bytes(conn, &blob, passphrase)
}

/// 从容器字节导入。
///
/// 重复导入同一份备份不会产生重复行（`content_hash` 唯一键 + `DO NOTHING`）。
pub fn import_bytes(conn: &Connection, blob: &[u8], passphrase: &str) -> Result<ImportReport> {
    let plain = open(blob, passphrase)?;
    let payload: Payload = serde_json::from_slice(&plain)?;
    if payload.format != FORMAT_VERSION {
        return Err(Error::Format(format!(
            "备份版本 {}，本程序支持 {}",
            payload.format, FORMAT_VERSION
        )));
    }
    apply(conn, &payload)
}

/// 只解密、不写库（给「预览备份内容」这类只读用途）。
pub fn read_payload(blob: &[u8], passphrase: &str) -> Result<Payload> {
    let plain = open(blob, passphrase)?;
    Ok(serde_json::from_slice(&plain)?)
}

fn collect(conn: &Connection) -> Result<Payload> {
    let mut stmt = conn.prepare(&format!(
        "SELECT {} FROM items ORDER BY id",
        crate::db::items::COLUMNS
    ))?;
    let items = stmt
        .query_map([], crate::db::items::from_row)?
        .collect::<rusqlite::Result<Vec<_>>>()?;
    drop(stmt);

    let mut stmt = conn.prepare("SELECT id, name, sort FROM groups ORDER BY id")?;
    let groups = stmt
        .query_map([], |r| {
            Ok(Group {
                id: r.get(0)?,
                name: r.get(1)?,
                sort: r.get(2)?,
            })
        })?
        .collect::<rusqlite::Result<Vec<_>>>()?;

    Ok(Payload {
        format: FORMAT_VERSION,
        app: "clipnest".to_owned(),
        created_at: now(),
        items,
        groups,
    })
}

fn apply(conn: &Connection, payload: &Payload) -> Result<ImportReport> {
    let tx = conn.unchecked_transaction()?;
    let mut report = ImportReport::default();

    // 分组按名字对齐（id 是本地自增，不能照搬）
    let mut group_map: HashMap<i64, i64> = HashMap::new();
    for g in &payload.groups {
        let existed: Option<i64> = tx
            .query_row("SELECT id FROM groups WHERE name = ?1", [&g.name], |r| {
                r.get(0)
            })
            .optional()?;
        let id = match existed {
            Some(id) => id,
            None => {
                tx.execute(
                    "INSERT INTO groups(name, sort) VALUES (?1, ?2)",
                    params![g.name, g.sort],
                )?;
                report.groups_inserted += 1;
                tx.last_insert_rowid()
            }
        };
        group_map.insert(g.id, id);
    }

    for it in &payload.items {
        let group_id = it.group_id.and_then(|old| group_map.get(&old).copied());
        let changed = tx.execute(
            "INSERT INTO items(content_hash, kind, content, file_types, width, height,
                                use_count, pinned, favorite, note, group_id, sensitive,
                                created_at, updated_at)
             VALUES (?1, ?2, ?3, ?4, ?5, ?6, ?7, ?8, ?9, ?10, ?11, ?12, ?13, ?14)
             ON CONFLICT(content_hash) DO NOTHING",
            params![
                it.content_hash,
                it.kind.as_str(),
                it.content,
                it.file_types,
                it.width,
                it.height,
                it.use_count,
                i64::from(it.pinned),
                i64::from(it.favorite),
                it.note,
                group_id,
                i64::from(it.sensitive),
                it.created_at,
                it.updated_at,
            ],
        )?;
        if changed > 0 {
            report.inserted += 1;
        } else {
            report.skipped += 1;
        }
    }

    tx.commit()?;
    Ok(report)
}

/// 加/解密函数的签名（同步载荷复用）。
pub type Coder = Box<dyn Fn(&[u8]) -> std::result::Result<Vec<u8>, String>>;

/// 用口令做一对「加/解密」函数，给同步载荷复用同一套原语。
///
/// 每一轮同步最多各调一次，argon2id 的几十毫秒可以接受；
/// 想省这次派生就在上层缓存 key（P4 接 WebDAV 时再看）。
pub fn sealer(passphrase: String) -> (Coder, Coder) {
    let seal_key = passphrase.clone();
    let seal: Coder = Box::new(move |plain: &[u8]| seal(plain, &seal_key).map_err(|e| e.to_string()));
    let open: Coder = Box::new(move |blob: &[u8]| open(blob, &passphrase).map_err(|e| e.to_string()));
    (seal, open)
}

fn derive_key(passphrase: &str, salt: &[u8]) -> Result<[u8; KEY_LEN]> {
    let argon = argon2::Argon2::default();
    let mut key = [0u8; KEY_LEN];
    argon
        .hash_password_into(passphrase.as_bytes(), salt, &mut key)
        .map_err(|e| Error::Format(format!("密钥派生失败：{e}")))?;
    Ok(key)
}

fn seal(plain: &[u8], passphrase: &str) -> Result<Vec<u8>> {
    let mut salt = [0u8; SALT_LEN];
    let mut nonce_bytes = [0u8; NONCE_LEN];
    getrandom::fill(&mut salt).map_err(|e| Error::Format(format!("取随机数失败：{e}")))?;
    getrandom::fill(&mut nonce_bytes).map_err(|e| Error::Format(format!("取随机数失败：{e}")))?;

    let cipher = ChaCha20Poly1305::new(&Key::from(derive_key(passphrase, &salt)?));
    let nonce = Nonce::from(nonce_bytes);
    let ciphertext = cipher
        .encrypt(
            &nonce,
            AeadPayload {
                msg: plain,
                aad: MAGIC,
            },
        )
        .map_err(|_| Error::Crypto)?;

    let mut out = Vec::with_capacity(MAGIC.len() + SALT_LEN + NONCE_LEN + ciphertext.len());
    out.extend_from_slice(MAGIC);
    out.extend_from_slice(&salt);
    out.extend_from_slice(&nonce_bytes);
    out.extend_from_slice(&ciphertext);
    Ok(out)
}

fn open(blob: &[u8], passphrase: &str) -> Result<Vec<u8>> {
    let head = MAGIC.len() + SALT_LEN + NONCE_LEN;
    if blob.len() <= head || &blob[..MAGIC.len()] != MAGIC {
        return Err(Error::Format("不是 clipnest 备份文件".to_owned()));
    }
    let salt = &blob[MAGIC.len()..MAGIC.len() + SALT_LEN];
    let nonce_bytes: [u8; NONCE_LEN] = blob[MAGIC.len() + SALT_LEN..head]
        .try_into()
        .map_err(|_| Error::Format("非随机数字段长度不对".to_owned()))?;
    let ciphertext = &blob[head..];

    let cipher = ChaCha20Poly1305::new(&Key::from(derive_key(passphrase, salt)?));
    let nonce = Nonce::from(nonce_bytes);
    cipher
        .decrypt(
            &nonce,
            AeadPayload {
                msg: ciphertext,
                aad: MAGIC,
            },
        )
        .map_err(|_| Error::Crypto)
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::db::items::{MetaPatch, update_meta, upsert};
    use crate::db::{items, open_memory};
    use crate::db::search::{Filter, Page, query};
    use crate::model::{ItemKind, NewItem};

    /// 造一个内容丰富的库：三种类型、去重过的条目、元数据、分组。
    fn seeded() -> Connection {
        let conn = open_memory().unwrap();
        conn.execute("INSERT INTO groups(name, sort) VALUES ('工作', 1)", [])
            .unwrap();
        let gid = conn.last_insert_rowid();

        let t = upsert(&conn, &NewItem::text("备份测试 银河系文本")).unwrap().id();
        upsert(&conn, &NewItem::text("备份测试 银河系文本")).unwrap();
        update_meta(
            &conn,
            t,
            &MetaPatch {
                pinned: Some(true),
                favorite: Some(true),
                note: Some(Some("置顶备注".to_owned())),
                group_id: Some(Some(gid)),
                ..Default::default()
            },
        )
        .unwrap();

        upsert(&conn, &NewItem::files(["/tmp/a.png", "/tmp/b"], "fd")).unwrap();
        upsert(&conn, &NewItem::image("deadbeef.png", 800, 600).sensitive(true)).unwrap();
        conn
    }

    fn all_items(conn: &Connection) -> Vec<Item> {
        query(conn, "", &Filter::default(), Page::new(1000, 0)).unwrap()
    }

    #[test]
    fn container_starts_with_magic() {
        let blob = export_bytes(&seeded(), "pw").unwrap();
        assert!(blob.starts_with(MAGIC));
        assert!(blob.len() > MAGIC.len() + SALT_LEN + NONCE_LEN);
        // 明文不泄漏
        assert!(!String::from_utf8_lossy(&blob).contains("银河系"));
    }

    /// T5 验收：导出 → 导入，数据逐字段一致。
    #[test]
    fn round_trip_is_field_identical() {
        let src = seeded();
        let blob = export_bytes(&src, "口令 pass 123").unwrap();

        let dst = open_memory().unwrap();
        let report = import_bytes(&dst, &blob, "口令 pass 123").unwrap();
        assert_eq!(report.inserted, 3);
        assert_eq!(report.skipped, 0);
        assert_eq!(report.groups_inserted, 1);

        let mut want = all_items(&src);
        let mut got = all_items(&dst);
        for v in [&mut want, &mut got] {
            v.sort_by(|a, b| a.content_hash.cmp(&b.content_hash));
        }
        assert_eq!(want.len(), got.len());
        for (w, g) in want.iter().zip(&got) {
            assert_eq!(w.content_hash, g.content_hash);
            assert_eq!(w.kind, g.kind);
            assert_eq!(w.content, g.content);
            assert_eq!(w.file_types, g.file_types);
            assert_eq!((w.width, w.height), (g.width, g.height));
            assert_eq!(w.use_count, g.use_count, "use_count 也要还原");
            assert_eq!((w.pinned, w.favorite), (g.pinned, g.favorite));
            assert_eq!(w.note, g.note);
            assert_eq!(w.sensitive, g.sensitive);
            assert_eq!(w.created_at, g.created_at);
            assert_eq!(w.updated_at, g.updated_at);
        }

        // 分组关系按名字重建
        let gid_src = want
            .iter()
            .find(|i| i.note.as_deref() == Some("置顶备注"))
            .and_then(|i| i.group_id);
        let gid_dst = got
            .iter()
            .find(|i| i.note.as_deref() == Some("置顶备注"))
            .and_then(|i| i.group_id);
        let name = |conn: &Connection, id: i64| -> String {
            conn.query_row("SELECT name FROM groups WHERE id = ?1", [id], |r| r.get(0))
                .unwrap()
        };
        assert_eq!(
            name(&src, gid_src.expect("src group")),
            name(&dst, gid_dst.expect("dst group"))
        );
    }

    #[test]
    fn second_import_creates_no_duplicates() {
        let blob = export_bytes(&seeded(), "pw").unwrap();
        let dst = open_memory().unwrap();
        assert_eq!(import_bytes(&dst, &blob, "pw").unwrap().inserted, 3);
        let again = import_bytes(&dst, &blob, "pw").unwrap();
        assert_eq!(again.inserted, 0);
        assert_eq!(again.skipped, 3);
        assert_eq!(again.groups_inserted, 0);
        assert_eq!(items::count(&dst).unwrap(), 3);
        let groups: i64 = dst
            .query_row("SELECT count(*) FROM groups", [], |r| r.get(0))
            .unwrap();
        assert_eq!(groups, 1);
    }

    /// T5 验收：错误口令报错、不 panic、不写库。
    #[test]
    fn wrong_passphrase_errors_without_side_effects() {
        let blob = export_bytes(&seeded(), "right").unwrap();
        let dst = open_memory().unwrap();
        let err = import_bytes(&dst, &blob, "wrong").unwrap_err();
        assert!(matches!(err, Error::Crypto));
        assert_eq!(items::count(&dst).unwrap(), 0, "失败的导入不能留半截数据");
        assert!(err.to_string().contains("口令错误"));
    }

    #[test]
    fn corrupted_or_foreign_files_are_rejected() {
        // 不是备份文件
        assert!(matches!(
            import_bytes(&open_memory().unwrap(), b"not a backup at all", "pw"),
            Err(Error::Format(_))
        ));

        // 头部合法但内容被截断 / 翻位
        let blob = export_bytes(&seeded(), "pw").unwrap();
        assert!(matches!(
            import_bytes(&open_memory().unwrap(), &blob[..blob.len() - 1], "pw"),
            Err(Error::Crypto)
        ));
        let mut tampered = blob.clone();
        let last = tampered.len() - 1;
        tampered[last] ^= 0xff;
        assert!(matches!(
            import_bytes(&open_memory().unwrap(), &tampered, "pw"),
            Err(Error::Crypto)
        ));

        // magic 被改（AAD 绑定 + magic 校验，任一都要拦住）
        let mut bad = blob.clone();
        bad[0] = b'X';
        assert!(matches!(
            import_bytes(&open_memory().unwrap(), &bad, "pw"),
            Err(Error::Format(_))
        ));
    }

    #[test]
    fn future_format_is_refused() {
        let mut payload = collect(&seeded()).unwrap();
        payload.format = FORMAT_VERSION + 1;
        let blob = seal(&serde_json::to_vec(&payload).unwrap(), "pw").unwrap();
        assert!(matches!(
            import_bytes(&open_memory().unwrap(), &blob, "pw"),
            Err(Error::Format(_))
        ));
    }

    #[test]
    fn file_export_import_round_trip() {
        let dir = std::env::temp_dir().join(format!("clipnest-backup-{}", crate::model::now()));
        let path = dir.join("nested").join("x.cnbak");
        let src = seeded();
        export(&src, &path, "pw").unwrap();
        assert!(path.exists());
        assert!(!path.with_extension("tmp").exists(), "临时文件要改名掉");

        let dst = open_memory().unwrap();
        assert_eq!(import(&dst, &path, "pw").unwrap().inserted, 3);
        let _ = std::fs::remove_dir_all(&dir);
    }

    #[test]
    fn empty_db_round_trips() {
        let conn = open_memory().unwrap();
        let blob = export_bytes(&conn, "pw").unwrap();
        let dst = open_memory().unwrap();
        let report = import_bytes(&dst, &blob, "pw").unwrap();
        assert_eq!(report, ImportReport::default());
        assert_eq!(items::count(&dst).unwrap(), 0);
    }

    /// 同步载荷复用这套加密：密文里看不到明文，解密能还原。
    #[test]
    fn sealer_round_trips_and_hides_plaintext() {
        let (seal, open) = sealer("同步口令".to_owned());
        let plain = r#"{"content":"银河系"}"#.as_bytes();
        let sealed = seal(plain).unwrap();
        assert!(sealed.starts_with(MAGIC));
        assert!(!String::from_utf8_lossy(&sealed).contains("银河系"));
        assert_eq!(open(&sealed).unwrap(), plain.to_vec());
        // 错口令要报错，不 panic
        let (_s, wrong) = sealer("另一个口令".to_owned());
        assert!(wrong(&sealed).is_err());
    }

    #[test]
    fn imported_items_are_searchable() {
        let blob = export_bytes(&seeded(), "pw").unwrap();
        let dst = open_memory().unwrap();
        import_bytes(&dst, &blob, "pw").unwrap();
        let hit = query(&dst, "银河系", &Filter::default(), Page::default()).unwrap();
        assert_eq!(hit.len(), 1);
        assert_eq!(hit[0].kind, ItemKind::Text);
    }
}
