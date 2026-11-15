//! 把条目导出成文件——「拖拽导出」的降级方案。
//!
//! nsh 0.5.1 只有接收 drop、没有发起拖拽的 API（见 `docs/2026-11-15-nsh-验证记录.md` 第十八节），
//! 所以导出成文件后把路径放进剪贴板：`⌘V` 到 Finder / 微信 / 邮件里粘出来的就是文件。
//!
//! 文件名：`clipnest-<YYYYmmdd-HHMMSS>.<ext>`，同一秒重名时加 `-2`、`-3`。

use std::path::{Path, PathBuf};

use crate::model::{Item, ItemKind};
use crate::settings::Settings;

/// 导出目录：设置里给了就用，否则 `~/Downloads`。
pub fn dir(settings: &Settings) -> PathBuf {
    let custom = settings.export_dir.trim();
    if !custom.is_empty() {
        return PathBuf::from(custom);
    }
    match std::env::var_os("HOME") {
        Some(home) => PathBuf::from(home).join("Downloads"),
        None => std::env::temp_dir(),
    }
}

/// 扩展名（`files` 不导出，返回 `None`）。
pub fn extension(kind: ItemKind) -> Option<&'static str> {
    match kind {
        ItemKind::Text => Some("txt"),
        ItemKind::Html => Some("html"),
        ItemKind::Rtf => Some("rtf"),
        ItemKind::Image => Some("png"),
        ItemKind::Files => None,
    }
}

/// 写出一条记录，返回落盘路径。
pub fn write(item: &Item, dir: &Path) -> Result<PathBuf, String> {
    let ext = extension(item.kind)
        .ok_or_else(|| "文件条目本身就是文件，不需要导出".to_owned())?;
    std::fs::create_dir_all(dir).map_err(|e| format!("建目录失败：{e}"))?;

    let stamp = chrono::Local::now().format("%Y%m%d-%H%M%S").to_string();
    let path = unique_path(dir, &format!("clipnest-{stamp}"), ext);

    match item.kind {
        ItemKind::Image => {
            // 原图已经在 images/origin 里，直接拷一份（不重新编码）。
            let hash = item.content.strip_suffix(".png").unwrap_or(&item.content);
            let origin = crate::paths().image_origin(hash);
            if !origin.exists() {
                return Err(format!("图片文件不见了：{}", origin.display()));
            }
            std::fs::copy(&origin, &path).map_err(|e| format!("拷图片失败：{e}"))?;
        }
        _ => {
            std::fs::write(&path, item.content.as_bytes())
                .map_err(|e| format!("写文件失败：{e}"))?;
        }
    }
    Ok(path)
}

/// `dir/<base>.<ext>`，被占了就加 `-2`、`-3`……
fn unique_path(dir: &Path, base: &str, ext: &str) -> PathBuf {
    let first = dir.join(format!("{base}.{ext}"));
    if !first.exists() {
        return first;
    }
    for n in 2..1000 {
        let candidate = dir.join(format!("{base}-{n}.{ext}"));
        if !candidate.exists() {
            return candidate;
        }
    }
    // 极端情况：交给系统报错，别死循环。
    first
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::model::{Item, ItemKind};

    fn item(kind: ItemKind, content: &str) -> Item {
        Item {
            id: 1,
            content_hash: "h".to_owned(),
            kind,
            content: content.to_owned(),
            file_types: None,
            width: None,
            height: None,
            use_count: 1,
            pinned: false,
            favorite: false,
            note: None,
            group_id: None,
            sensitive: false,
            created_at: crate::model::now(),
            updated_at: crate::model::now(),
        }
    }

    fn temp_dir(tag: &str) -> PathBuf {
        std::env::temp_dir().join(format!("clipnest-export-{tag}-{}", std::process::id()))
    }

    #[test]
    fn text_is_written_with_a_timestamped_name() {
        let dir = temp_dir("txt");
        let path = write(&item(ItemKind::Text, "导出的文本\n第二行"), &dir).unwrap();
        assert_eq!(std::fs::read_to_string(&path).unwrap(), "导出的文本\n第二行");
        let name = path.file_name().unwrap().to_string_lossy().to_string();
        assert!(name.starts_with("clipnest-"), "{name}");
        assert!(name.ends_with(".txt"), "{name}");
        let _ = std::fs::remove_dir_all(&dir);
    }

    #[test]
    fn same_second_does_not_overwrite() {
        let dir = temp_dir("collide");
        let a = write(&item(ItemKind::Text, "第一次"), &dir).unwrap();
        let b = write(&item(ItemKind::Text, "第二次"), &dir).unwrap();
        assert_ne!(a, b, "同秒要换名，不能覆盖");
        assert_eq!(std::fs::read_to_string(&a).unwrap(), "第一次");
        assert_eq!(std::fs::read_to_string(&b).unwrap(), "第二次");
        let _ = std::fs::remove_dir_all(&dir);
    }

    #[test]
    fn extensions_cover_every_kind_but_files() {
        assert_eq!(extension(ItemKind::Text), Some("txt"));
        assert_eq!(extension(ItemKind::Html), Some("html"));
        assert_eq!(extension(ItemKind::Rtf), Some("rtf"));
        assert_eq!(extension(ItemKind::Image), Some("png"));
        assert_eq!(extension(ItemKind::Files), None, "文件条目不用导出");
        assert!(write(&item(ItemKind::Files, "/a/b"), &temp_dir("files")).is_err());
    }

    #[test]
    fn export_dir_falls_back_to_downloads() {
        let mut s = Settings::default();
        let d = dir(&s);
        assert!(d.ends_with("Downloads"), "{d:?}");
        s.export_dir = "  /tmp/my-exports  ".to_owned();
        assert_eq!(dir(&s), PathBuf::from("/tmp/my-exports"), "去掉空白");
    }
}
