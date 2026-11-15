//! P4 同步核心（不依赖网络的那一半）。
//!
//! 设计要点见 `docs/执行计划-下一步.md` 第 4 节，这里落成代码：
//! - 载荷是**增量包**：自上次导出以来变更的条目 + 删除墓碑
//! - 合并键是 `content_hash`（契约 1），重复合并结果不变（幂等）
//! - `use_count` 取两边的最大值：既不会因为多次拉取而膨胀，也不会丢使用次数
//! - 冲突一律「`updated_at` 更新的赢」；删除用墓碑，墓碑时间晚于条目更新就删本地
//! - 分组按**名字**对齐（id 是各自的自增值，不能照搬）
//!
//! 传输层是 [`Transport`]：现在只有 [`LocalTransport`]（测试用目录），
//! WebDAV 之后按同一个接口实现。载荷本身交给调用方加密（见 `backup` 的原语）。

pub mod backends;
pub mod config;
pub mod secret;

use std::collections::HashMap;
use std::io::ErrorKind;
use std::path::{Path, PathBuf};

use rusqlite::{Connection, OptionalExtension, params};
use serde::{Deserialize, Serialize};

use crate::db::items::COLUMNS;
use crate::model::{Group, Item, ItemKind, now};

/// 载荷格式版本。
pub const FORMAT_VERSION: u32 = 1;

/// 载荷编解码（加密与否由调用方决定：`backup::sealer` 或 [`plain`]）。
pub type Encode = dyn Fn(&[u8]) -> std::result::Result<Vec<u8>, String>;

/// 一个设备传给别的设备的变更。
#[derive(Debug, Clone, PartialEq, Serialize, Deserialize)]
#[serde(tag = "op", rename_all = "lowercase")]
pub enum Change {
    /// 新增或更新一条（`id` 不跨设备，所以不传）。
    Upsert { item: SyncItem },
    /// 删除（墓碑）。
    Delete {
        content_hash: String,
        deleted_at: String,
    },
}

/// 跨设备传输的条目（`Item` 去掉本地自增 id，分组换成名字）。
#[derive(Debug, Clone, PartialEq, Serialize, Deserialize)]
pub struct SyncItem {
    pub content_hash: String,
    pub kind: ItemKind,
    pub content: String,
    pub file_types: Option<String>,
    pub width: Option<i64>,
    pub height: Option<i64>,
    pub use_count: i64,
    pub pinned: bool,
    pub favorite: bool,
    pub note: Option<String>,
    /// 分组名（不是 id）。
    pub group: Option<String>,
    pub sensitive: bool,
    pub created_at: String,
    pub updated_at: String,
}

impl SyncItem {
    /// 本地行 → 传输格式；`group` 由调用方按 id 查好名字。
    fn of(item: &Item, group: Option<String>) -> Self {
        Self {
            content_hash: item.content_hash.clone(),
            kind: item.kind,
            content: item.content.clone(),
            file_types: item.file_types.clone(),
            width: item.width,
            height: item.height,
            use_count: item.use_count,
            pinned: item.pinned,
            favorite: item.favorite,
            note: item.note.clone(),
            group,
            sensitive: item.sensitive,
            created_at: item.created_at.clone(),
            updated_at: item.updated_at.clone(),
        }
    }
}

/// 一台设备一次导出的增量包。
#[derive(Debug, Clone, PartialEq, Serialize, Deserialize)]
pub struct Package {
    pub version: u32,
    pub device_id: String,
    pub exported_at: String,
    pub changes: Vec<Change>,
}

/// 合并结果。
#[derive(Debug, Clone, Copy, Default, PartialEq, Eq)]
pub struct ApplyReport {
    /// 新插入的条目。
    pub inserted: usize,
    /// 本地已有、按 `updated_at` 更新的一侧覆盖。
    pub updated: usize,
    /// 本地更新（或相同），没动。
    pub skipped: usize,
    /// 按墓碑删掉的条目。
    pub deleted: usize,
}

impl ApplyReport {
    fn merge(&mut self, other: ApplyReport) {
        self.inserted += other.inserted;
        self.updated += other.updated;
        self.skipped += other.skipped;
        self.deleted += other.deleted;
    }

    pub fn changed(&self) -> bool {
        self.inserted + self.updated + self.deleted > 0
    }
}

/// 一轮同步的结果。
#[derive(Debug, Clone, Default, PartialEq, Eq)]
pub struct SyncReport {
    /// 自己推上去的变更条数。
    pub pushed: usize,
    /// 从别的设备拉到并合并的变更条数。
    pub pulled: usize,
    /// 上传的图片文件数。
    pub blobs_pushed: usize,
    /// 下载的图片文件数。
    pub blobs_pulled: usize,
    /// 合并细节。
    pub applied: ApplyReport,
}

// ───────────────────────── 传输 ─────────────────────────

/// 传输入口：一台设备一个文件，文件名 `<device_id>.json`。
pub trait Transport {
    fn list(&self) -> Result<Vec<String>, String>;
    fn get(&self, name: &str) -> Result<Vec<u8>, String>;
    fn put(&self, name: &str, bytes: &[u8]) -> Result<(), String>;
    /// 删掉一个设备文件（比如对方已退役）；不存在不算错。
    fn remove(&self, name: &str) -> Result<(), String>;
}

/// 本地目录当"远端"：测试与「用网盘目录同步」的兜底。
#[derive(Debug, Clone)]
pub struct LocalTransport {
    root: PathBuf,
}

impl LocalTransport {
    pub fn new(root: impl Into<PathBuf>) -> Self {
        Self { root: root.into() }
    }

    pub fn root(&self) -> &Path {
        &self.root
    }
}

impl Transport for LocalTransport {
    fn list(&self) -> Result<Vec<String>, String> {
        let dir = std::fs::read_dir(&self.root).map_err(|e| format!("读目录失败：{e}"))?;
        let mut out = Vec::new();
        for entry in dir {
            let entry = entry.map_err(|e| format!("读目录项失败：{e}"))?;
            let name = entry.file_name().to_string_lossy().to_string();
            if name.ends_with(".json") {
                out.push(name);
            }
        }
        out.sort();
        Ok(out)
    }

    fn get(&self, name: &str) -> Result<Vec<u8>, String> {
        std::fs::read(self.root.join(name)).map_err(|e| format!("读 {name} 失败：{e}"))
    }

    fn put(&self, name: &str, bytes: &[u8]) -> Result<(), String> {
        let target = self.root.join(name);
        if let Some(dir) = target.parent() {
            std::fs::create_dir_all(dir).map_err(|e| format!("建目录失败：{e}"))?;
        }
        let tmp = target.with_extension("tmp");
        std::fs::write(&tmp, bytes).map_err(|e| format!("写 {name} 失败：{e}"))?;
        std::fs::rename(&tmp, &target).map_err(|e| format!("改名失败：{e}"))
    }

    fn remove(&self, name: &str) -> Result<(), String> {
        match std::fs::remove_file(self.root.join(name)) {
            Ok(()) => Ok(()),
            Err(e) if e.kind() == ErrorKind::NotFound => Ok(()),
            Err(e) => Err(format!("删 {name} 失败：{e}")),
        }
    }
}

// ───────────────────────── 导出 / 合并 ─────────────────────────

/// 导出「自 `since` 之后变化」的条目与墓碑；`None` 表示全量。
pub fn export_changes(conn: &Connection, since: Option<&str>) -> rusqlite::Result<Vec<Change>> {
    let mut out = Vec::new();

    // 分组 id → 名字
    let mut groups: HashMap<i64, String> = HashMap::new();
    let mut stmt = conn.prepare("SELECT id, name, sort FROM groups")?;
    let rows = stmt.query_map([], |r| {
        Ok(Group {
            id: r.get(0)?,
            name: r.get(1)?,
            sort: r.get(2)?,
        })
    })?;
    for g in rows {
        let g = g?;
        groups.insert(g.id, g.name);
    }

    let sql = format!("SELECT {COLUMNS} FROM items WHERE updated_at > ?1 ORDER BY updated_at, id");
    let mut stmt = conn.prepare(&sql)?;
    let items = stmt.query_map([since.unwrap_or("")], crate::db::items::from_row)?;
    for item in items {
        let item = item?;
        let group = item.group_id.and_then(|id| groups.get(&id).cloned());
        out.push(Change::Upsert {
            item: SyncItem::of(&item, group),
        });
    }

    let mut stmt =
        conn.prepare("SELECT content_hash, deleted_at FROM tombstones WHERE deleted_at > ?1")?;
    let tombs = stmt.query_map([since.unwrap_or("")], |r| {
        Ok((r.get::<_, String>(0)?, r.get::<_, String>(1)?))
    })?;
    for t in tombs {
        let (content_hash, deleted_at) = t?;
        out.push(Change::Delete {
            content_hash,
            deleted_at,
        });
    }

    Ok(out)
}

/// 把别人的变更合并进本地库。
pub fn apply_changes(conn: &Connection, changes: &[Change]) -> rusqlite::Result<ApplyReport> {
    let tx = conn.unchecked_transaction()?;
    let mut report = ApplyReport::default();

    for change in changes {
        match change {
            Change::Upsert { item } => match merge_item(&tx, item)? {
                Merged::Inserted => report.inserted += 1,
                Merged::Updated => report.updated += 1,
                Merged::Skipped => report.skipped += 1,
            },
            Change::Delete {
                content_hash,
                deleted_at,
            } => {
                if apply_tombstone(&tx, content_hash, deleted_at)? {
                    report.deleted += 1;
                } else {
                    report.skipped += 1;
                }
            }
        }
    }

    tx.commit()?;
    Ok(report)
}

enum Merged {
    Inserted,
    Updated,
    Skipped,
}

fn merge_item(conn: &Connection, remote: &SyncItem) -> rusqlite::Result<Merged> {
    // 本地已经删了（墓碑更新的那条）：不要再被旧数据复活。
    if let Some(deleted_at) = tombstone_of(conn, &remote.content_hash)?
        && deleted_at >= remote.updated_at
    {
        return Ok(Merged::Skipped);
    }

    let group_id = match &remote.group {
        Some(name) => Some(ensure_group(conn, name)?),
        None => None,
    };

    let local: Option<(i64, i64, String)> = conn
        .query_row(
            "SELECT id, use_count, updated_at FROM items WHERE content_hash = ?1",
            [&remote.content_hash],
            |r| Ok((r.get(0)?, r.get(1)?, r.get(2)?)),
        )
        .optional()?;

    match local {
        None => {
            conn.execute(
                "INSERT INTO items(content_hash, kind, content, file_types, width, height,
                                    use_count, pinned, favorite, note, group_id, sensitive,
                                    created_at, updated_at)
                 VALUES (?1, ?2, ?3, ?4, ?5, ?6, ?7, ?8, ?9, ?10, ?11, ?12, ?13, ?14)",
                params![
                    remote.content_hash,
                    remote.kind.as_str(),
                    remote.content,
                    remote.file_types,
                    remote.width,
                    remote.height,
                    remote.use_count,
                    i64::from(remote.pinned),
                    i64::from(remote.favorite),
                    remote.note,
                    group_id,
                    i64::from(remote.sensitive),
                    remote.created_at,
                    remote.updated_at,
                ],
            )?;
            Ok(Merged::Inserted)
        }
        Some((id, local_uses, local_updated)) => {
            // 使用次数取 max：重复拉取不会膨胀，也不会丢次数。
            let uses = local_uses.max(remote.use_count);
            if remote.updated_at <= local_updated {
                // 本地更新：只把 use_count 往上并一下。
                if uses != local_uses {
                    conn.execute(
                        "UPDATE items SET use_count = ?1 WHERE id = ?2",
                        params![uses, id],
                    )?;
                    return Ok(Merged::Updated);
                }
                return Ok(Merged::Skipped);
            }
            conn.execute(
                "UPDATE items SET kind = ?1, content = ?2, file_types = ?3, width = ?4, height = ?5,
                                 use_count = ?6, pinned = ?7, favorite = ?8, note = ?9,
                                 group_id = ?10, sensitive = ?11, updated_at = ?12
                 WHERE id = ?13",
                params![
                    remote.kind.as_str(),
                    remote.content,
                    remote.file_types,
                    remote.width,
                    remote.height,
                    uses,
                    i64::from(remote.pinned),
                    i64::from(remote.favorite),
                    remote.note,
                    group_id,
                    i64::from(remote.sensitive),
                    remote.updated_at,
                    id,
                ],
            )?;
            Ok(Merged::Updated)
        }
    }
}

fn tombstone_of(conn: &Connection, hash: &str) -> rusqlite::Result<Option<String>> {
    conn.query_row(
        "SELECT deleted_at FROM tombstones WHERE content_hash = ?1",
        [hash],
        |r| r.get(0),
    )
    .optional()
}

/// 墓碑晚于本地条目的更新时间就删；返回是否真删了。
fn apply_tombstone(
    conn: &Connection,
    hash: &str,
    deleted_at: &str,
) -> rusqlite::Result<bool> {
    let local: Option<String> = conn
        .query_row(
            "SELECT updated_at FROM items WHERE content_hash = ?1",
            [hash],
            |r| r.get(0),
        )
        .optional()?;
    // 墓碑照收（幂等，也防止之后再被旧数据复活）。
    conn.execute(
        "INSERT INTO tombstones(content_hash, deleted_at, device_id) VALUES (?1, ?2, NULL)
           ON CONFLICT(content_hash) DO UPDATE SET deleted_at = max(deleted_at, excluded.deleted_at)",
        params![hash, deleted_at],
    )?;
    match local {
        Some(updated_at) if updated_at.as_str() <= deleted_at => {
            conn.execute("DELETE FROM items WHERE content_hash = ?1", [hash])?;
            Ok(true)
        }
        _ => Ok(false),
    }
}

fn ensure_group(conn: &Connection, name: &str) -> rusqlite::Result<i64> {
    conn.execute(
        "INSERT INTO groups(name) VALUES (?1) ON CONFLICT(name) DO NOTHING",
        [name],
    )?;
    conn.query_row("SELECT id FROM groups WHERE name = ?1", [name], |r| {
        r.get(0)
    })
}

// ───────────────────────── 一轮同步 ─────────────────────────

/// 推进（或读）上次导出的时间点。
fn last_export(conn: &Connection) -> rusqlite::Result<Option<String>> {
    conn.query_row(
        "SELECT value FROM meta WHERE key = 'sync_last_export'",
        [],
        |r| r.get(0),
    )
    .optional()
}

fn set_last_export(conn: &Connection, at: &str) -> rusqlite::Result<()> {
    conn.execute(
        "INSERT INTO meta(key, value) VALUES ('sync_last_export', ?1)
           ON CONFLICT(key) DO UPDATE SET value = excluded.value",
        [at],
    )?;
    Ok(())
}

/// 图片在远端的位置：`blobs/<hash 前2位>/<hash>.png`（与本地分片一致，便于排查）。
pub fn blob_path(hash: &str) -> String {
    let shard = &hash[..2.min(hash.len())];
    format!("blobs/{shard}/{hash}.png")
}

/// 把包里的图片原图传到远端；远端已有同一份（按 hash 命名）就是幂等覆盖。
fn push_blobs(transport: &dyn Transport, changes: &[Change]) -> usize {
    let paths = crate::paths();
    let mut sent = 0;
    for change in changes {
        let Change::Upsert { item } = change else {
            continue;
        };
        if item.kind != ItemKind::Image {
            continue;
        }
        let hash = item.content.strip_suffix(".png").unwrap_or(&item.content);
        let origin = paths.image_origin(hash);
        let Ok(bytes) = std::fs::read(&origin) else {
            crate::logs::line(format!("SYNC blob missing local: {hash}"));
            continue;
        };
        match transport.put(&blob_path(hash), &bytes) {
            Ok(()) => sent += 1,
            Err(e) => crate::logs::line(format!("SYNC blob push failed {hash}: {e}")),
        }
    }
    sent
}

/// 拉取包里本地还缺的图片原图（并顺手生成缩略图）。
fn pull_blobs(transport: &dyn Transport, changes: &[Change]) -> usize {
    let paths = crate::paths();
    let mut got = 0;
    for change in changes {
        let Change::Upsert { item } = change else {
            continue;
        };
        if item.kind != ItemKind::Image {
            continue;
        }
        let hash = item.content.strip_suffix(".png").unwrap_or(&item.content);
        let origin = paths.image_origin(hash);
        if origin.exists() {
            continue;
        }
        let Ok(bytes) = transport.get(&blob_path(hash)) else {
            // 对端没传图（旧版本或传失败）：记录还在，缩略图退化成尺寸
            crate::logs::line(format!("SYNC blob not on remote: {hash}"));
            continue;
        };
        if let Err(e) = paths.ensure_image_dirs(hash) {
            crate::logs::line(format!("SYNC blob dir failed {hash}: {e}"));
            continue;
        }
        if let Err(e) = std::fs::write(&origin, &bytes) {
            crate::logs::line(format!("SYNC blob write failed {hash}: {e}"));
            continue;
        }
        let thumb = paths.image_thumbnail(hash);
        let _ = crate::store::write_thumbnail(&bytes, &thumb);
        got += 1;
    }
    got
}

/// 把本地增量推上去，再把别的设备的增量拉下来合并。
///
/// `encode`/`decode` 由调用方给（加密在 `backup` 里）——传输层只见字节。
pub fn sync_round(
    conn: &Connection,
    transport: &dyn Transport,
    device_id: &str,
    encode: &Encode,
    decode: &Encode,
) -> std::result::Result<SyncReport, String> {
    let mut report = SyncReport::default();

    // 1) 推：只发上次导出之后的变更
    let since = last_export(conn).map_err(|e| e.to_string())?;
    let changes = export_changes(conn, since.as_deref()).map_err(|e| e.to_string())?;
    let exported_at = now();
    if !changes.is_empty() {
        let package = Package {
            version: FORMAT_VERSION,
            device_id: device_id.to_owned(),
            exported_at: exported_at.clone(),
            changes,
        };
        let plain = serde_json::to_vec(&package).map_err(|e| e.to_string())?;
        let sealed = encode(&plain)?;
        transport.put(&format!("{device_id}.json"), &sealed)?;
        report.pushed = package.changes.len();
        report.blobs_pushed = push_blobs(transport, &package.changes);
    }
    // 没有变更也要记时间点，否则下次会把老变更再发一遍。
    set_last_export(conn, &exported_at).map_err(|e| e.to_string())?;

    // 2) 拉：别的设备的包
    for name in transport.list()? {
        if name == format!("{device_id}.json") {
            continue;
        }
        let sealed = transport.get(&name)?;
        let plain = decode(&sealed)?;
        let package: Package = match serde_json::from_slice(&plain) {
            Ok(p) => p,
            Err(e) => {
                crate::logs::line(format!("SYNC skip {name}: {e}"));
                continue;
            }
        };
        if package.version != FORMAT_VERSION {
            crate::logs::line(format!(
                "SYNC skip {name}: 版本 {} 不支持",
                package.version
            ));
            continue;
        }
        report.pulled += package.changes.len();
        let applied = apply_changes(conn, &package.changes).map_err(|e| e.to_string())?;
        report.applied.merge(applied);
        report.blobs_pulled += pull_blobs(transport, &package.changes);
    }

    Ok(report)
}

/// 按配置跑一轮同步（建后端、取口令、选编解码）。
///
/// UI 与定时任务都走这一条；错误都转成人能看懂的字符串。
pub fn run_once(conn: &Connection, config: &config::SyncConfig) -> Result<SyncReport, String> {
    config.ready()?;
    let transport = backends::open(config)?;
    let device = config.device_name.clone();

    if config.encryption.enabled {
        let passphrase = secret::get(config::SECRET_SERVICE, &device)
            .ok_or_else(|| "钥匙串里没有同步口令（先在设置里填一个）".to_owned())?;
        let (seal, open) = crate::backup::sealer(passphrase);
        sync_round(conn, transport.as_ref(), &device, &seal, &open)
    } else {
        sync_round(conn, transport.as_ref(), &device, &plain, &plain)
    }
}

/// 不加密的编解码（本地目录测试与「信任的网盘目录」用）。
pub fn plain(_bytes: &[u8]) -> Result<Vec<u8>, String> {
    Ok(_bytes.to_vec())
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::db::items::{MetaPatch, update_meta, upsert};
    use crate::db::{open_memory, search};
    use crate::model::NewItem;

    fn temp_dir(tag: &str) -> PathBuf {
        std::env::temp_dir().join(format!(
            "clipnest-sync-{tag}-{}-{}",
            std::process::id(),
            now()
        ))
    }

    fn all(conn: &Connection) -> Vec<Item> {
        search::query(
            conn,
            "",
            &search::Filter::default(),
            search::Page::new(1000, 0),
        )
        .unwrap()
    }

    /// 两台设备经本地目录互同步。
    #[test]
    fn two_devices_exchange_items() {
        let dir = temp_dir("exchange");
        let transport = LocalTransport::new(&dir);
        let a = open_memory().unwrap();
        let b = open_memory().unwrap();

        upsert(&a, &NewItem::text("设备 A 的条目")).unwrap();
        let pushed = sync_round(&a, &transport, "dev-a", &plain, &plain).unwrap();
        assert_eq!(pushed.pushed, 1);
        assert_eq!(pulled_items(&b, &transport, "dev-b").len(), 1);
        assert_eq!(all(&b)[0].content, "设备 A 的条目");
        let _ = std::fs::remove_dir_all(&dir);
    }

    fn pulled_items(conn: &Connection, transport: &dyn Transport, device: &str) -> Vec<Item> {
        sync_round(conn, transport, device, &plain, &plain).unwrap();
        all(conn)
    }

    #[test]
    fn use_count_takes_the_max_and_pull_is_idempotent() {
        let dir = temp_dir("uses");
        let transport = LocalTransport::new(&dir);
        let a = open_memory().unwrap();
        let b = open_memory().unwrap();

        // 两端同一条内容，本地各用了几次
        for _ in 0..3 {
            upsert(&a, &NewItem::text("共同条目")).unwrap();
        }
        upsert(&b, &NewItem::text("共同条目")).unwrap();
        sync_round(&a, &transport, "dev-a", &plain, &plain).unwrap();
        sync_round(&b, &transport, "dev-b", &plain, &plain).unwrap();
        sync_round(&a, &transport, "dev-a", &plain, &plain).unwrap();

        assert_eq!(all(&a).len(), 1);
        assert_eq!(all(&b).len(), 1);
        assert_eq!(all(&b)[0].use_count, 3, "取 max，不累加");

        // 再同步一轮：条数不变、use_count 也不膨胀
        sync_round(&b, &transport, "dev-b", &plain, &plain).unwrap();
        assert_eq!(all(&b)[0].use_count, 3);
    }

    #[test]
    fn delete_propagates_through_tombstones() {
        let dir = temp_dir("tombstone");
        let transport = LocalTransport::new(&dir);
        let a = open_memory().unwrap();
        let b = open_memory().unwrap();

        let id = upsert(&a, &NewItem::text("要删掉的条目")).unwrap().id();
        sync_round(&a, &transport, "dev-a", &plain, &plain).unwrap();
        sync_round(&b, &transport, "dev-b", &plain, &plain).unwrap();
        assert_eq!(all(&b).len(), 1);

        std::thread::sleep(std::time::Duration::from_millis(3));
        crate::db::items::delete(&a, id).unwrap();
        sync_round(&a, &transport, "dev-a", &plain, &plain).unwrap();
        let report = sync_round(&b, &transport, "dev-b", &plain, &plain).unwrap();
        assert_eq!(report.applied.deleted, 1);
        assert!(all(&b).is_empty(), "墓碑要传到对端");

        // 再拉一轮不会复活
        sync_round(&b, &transport, "dev-b", &plain, &plain).unwrap();
        assert!(all(&b).is_empty());
    }

    #[test]
    fn newer_updated_at_wins_for_flags() {
        let dir = temp_dir("conflict");
        let transport = LocalTransport::new(&dir);
        let a = open_memory().unwrap();
        let b = open_memory().unwrap();

        let id_a = upsert(&a, &NewItem::text("会冲突的条目")).unwrap().id();
        upsert(&b, &NewItem::text("会冲突的条目")).unwrap();
        sync_round(&a, &transport, "dev-a", &plain, &plain).unwrap();

        // A 上置顶（更晚）
        std::thread::sleep(std::time::Duration::from_millis(3));
        update_meta(
            &a,
            id_a,
            &MetaPatch {
                pinned: Some(true),
                ..Default::default()
            },
        )
        .unwrap();
        // 注意：update_meta 不刷新 updated_at（契约 6），所以同步不会传播它——
        // 这正是契约 6 的代价：置顶/收藏不参与冲突比较。这里用一次真实入库刷新时间。
        upsert(&a, &NewItem::text("会冲突的条目")).unwrap();

        sync_round(&a, &transport, "dev-a", &plain, &plain).unwrap();
        sync_round(&b, &transport, "dev-b", &plain, &plain).unwrap();
        let b_item = &all(&b)[0];
        assert!(b_item.pinned, "更新的那条赢");
        assert_eq!(b_item.use_count, 2, "use_count 取 max");
    }

    #[test]
    fn groups_are_matched_by_name() {
        let dir = temp_dir("groups");
        let transport = LocalTransport::new(&dir);
        let a = open_memory().unwrap();
        let b = open_memory().unwrap();

        a.execute("INSERT INTO groups(name) VALUES ('工作')", [])
            .unwrap();
        let gid = a.last_insert_rowid();
        // B 里同名分组已存在但 id 不同
        b.execute("INSERT INTO groups(name) VALUES ('别的不相关')", [])
            .unwrap();
        b.execute("INSERT INTO groups(name) VALUES ('工作')", [])
            .unwrap();

        let id = upsert(&a, &NewItem::text("分组里的条目")).unwrap().id();
        update_meta(
            &a,
            id,
            &MetaPatch {
                group_id: Some(Some(gid)),
                ..Default::default()
            },
        )
        .unwrap();
        // 分组变更不刷 updated_at，这里为了让条目被导出重新入库一次
        upsert(&a, &NewItem::text("分组里的条目")).unwrap();

        sync_round(&a, &transport, "dev-a", &plain, &plain).unwrap();
        sync_round(&b, &transport, "dev-b", &plain, &plain).unwrap();

        let item = &all(&b)[0];
        let name: String = b
            .query_row(
                "SELECT name FROM groups WHERE id = ?1",
                [item.group_id.expect("要有分组")],
                |r| r.get(0),
            )
            .unwrap();
        assert_eq!(name, "工作", "按名字对齐，不是照搬 id");
        let _ = std::fs::remove_dir_all(&dir);
    }

    #[test]
    fn export_since_only_sends_new_changes() {
        let conn = open_memory().unwrap();
        upsert(&conn, &NewItem::text("第一条")).unwrap();
        let marker = now();
        std::thread::sleep(std::time::Duration::from_millis(3));
        upsert(&conn, &NewItem::text("第二条")).unwrap();

        let all_changes = export_changes(&conn, None).unwrap();
        assert_eq!(all_changes.len(), 2);
        let delta = export_changes(&conn, Some(&marker)).unwrap();
        assert_eq!(delta.len(), 1, "只发之后变化的");
    }

    #[test]
    fn transport_round_trips_files() {
        let dir = temp_dir("transport");
        let t = LocalTransport::new(&dir);
        t.put("a.json", b"hello").unwrap();
        assert_eq!(t.list().unwrap(), vec!["a.json".to_owned()]);
        assert_eq!(t.get("a.json").unwrap(), b"hello");
        t.remove("a.json").unwrap();
        t.remove("a.json").unwrap();
        assert!(t.list().unwrap().is_empty());
        let _ = std::fs::remove_dir_all(&dir);
    }
}
