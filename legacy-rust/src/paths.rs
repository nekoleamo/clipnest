//! 数据目录与资源布局。
//!
//! 一个根目录装下全部可写状态：
//!
//! ```text
//! <root>/
//!   clipnest.db
//!   images/origin/<hh>/<hash>.png
//!   images/thumbnails/<hh>/<hash>.png
//!   settings.json
//!   backups/
//! ```
//!
//! `<root>` 在 macOS 是 `~/Library/Application Support/clipnest`，
//! Windows 是 `%APPDATA%\clipnest`，Linux 是 `$XDG_DATA_HOME/clipnest`；
//! 环境变量 `CLIPNEST_DATA_DIR` 可整体覆盖（调试与测试用）。
//!
//! 不用 `nsh_core::data_dir()`：它给的是 `<data_dir>/<app id>`（`com.example.clipnest`），
//! 且依赖 nsh 运行时，`cargo test` 里拿不到。

use std::path::{Path, PathBuf};

/// 数据库文件名。
pub const DB_FILE: &str = "clipnest.db";
/// 设置文件名。
pub const SETTINGS_FILE: &str = "settings.json";
/// 同步配置文件（独立于 settings.json，便于开源时单独给模板）。
pub const SYNC_FILE: &str = "sync.json";

/// 应用全部可写状态的根。
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct Paths {
    root: PathBuf,
}

impl Paths {
    /// 用给定根目录（不做平台推断、不创建目录）。
    pub fn from_root(root: impl Into<PathBuf>) -> Self {
        Self { root: root.into() }
    }

    /// 按平台推断根目录；`CLIPNEST_DATA_DIR` 优先。
    pub fn platform() -> Self {
        match std::env::var_os("CLIPNEST_DATA_DIR") {
            Some(dir) => Self::from_root(dir),
            None => Self::from_root(default_root()),
        }
    }

    pub fn root(&self) -> &Path {
        &self.root
    }

    pub fn db(&self) -> PathBuf {
        self.root.join(DB_FILE)
    }

    pub fn settings(&self) -> PathBuf {
        self.root.join(SETTINGS_FILE)
    }

    pub fn sync_config(&self) -> PathBuf {
        self.root.join(SYNC_FILE)
    }

    pub fn backups_dir(&self) -> PathBuf {
        self.root.join("backups")
    }

    /// `backups/<name>`；`name` 由调用方给（建议带时间戳）。
    pub fn backup(&self, name: &str) -> PathBuf {
        self.backups_dir().join(name)
    }

    pub fn images_dir(&self) -> PathBuf {
        self.root.join("images")
    }

    /// 原图路径：`images/origin/<hh>/<hash>.png`。
    pub fn image_origin(&self, hash: &str) -> PathBuf {
        self.images_dir().join("origin").join(shard(hash)).join(image_name(hash))
    }

    /// 缩略图路径：`images/thumbnails/<hh>/<hash>.png`。
    pub fn image_thumbnail(&self, hash: &str) -> PathBuf {
        self.images_dir()
            .join("thumbnails")
            .join(shard(hash))
            .join(image_name(hash))
    }

    /// 建好全部目录（幂等）。
    pub fn ensure(&self) -> std::io::Result<()> {
        for dir in [
            self.root.clone(),
            self.images_dir().join("origin"),
            self.images_dir().join("thumbnails"),
            self.backups_dir(),
        ] {
            std::fs::create_dir_all(dir)?;
        }
        Ok(())
    }

    /// 建好某条图片条目的分片目录，返回 (原图, 缩略图) 路径。
    pub fn ensure_image_dirs(&self, hash: &str) -> std::io::Result<(PathBuf, PathBuf)> {
        let origin = self.image_origin(hash);
        let thumb = self.image_thumbnail(hash);
        for p in [&origin, &thumb] {
            if let Some(dir) = p.parent() {
                std::fs::create_dir_all(dir)?;
            }
        }
        Ok((origin, thumb))
    }
}

/// 契约 4：`items.content` 里存的图片文件名。
pub fn image_name(hash: &str) -> String {
    format!("{hash}.png")
}

/// 分片目录名：哈希前 2 个字符（避免单个目录堆几十万文件）。
fn shard(hash: &str) -> &str {
    if hash.len() >= 2 && hash.is_char_boundary(2) {
        &hash[..2]
    } else {
        "00"
    }
}

/// 平台默认根目录。
pub fn default_root() -> PathBuf {
    platform_root(
        cfg!(target_os = "macos"),
        cfg!(target_os = "windows"),
        std::env::var_os("APPDATA").map(PathBuf::from),
        std::env::var_os("XDG_DATA_HOME").map(PathBuf::from),
        std::env::var_os("HOME").map(PathBuf::from),
    )
}

/// 纯函数版：不读环境，便于测试三分支。
fn platform_root(
    macos: bool,
    windows: bool,
    appdata: Option<PathBuf>,
    xdg: Option<PathBuf>,
    home: Option<PathBuf>,
) -> PathBuf {
    if macos {
        let home = home.unwrap_or_else(|| PathBuf::from("."));
        return home.join("Library").join("Application Support").join("clipnest");
    }
    if windows
        && let Some(appdata) = appdata {
            return appdata.join("clipnest");
        }
    if let Some(xdg) = xdg {
        return xdg.join("clipnest");
    }
    home.unwrap_or_else(|| PathBuf::from("."))
        .join(".local")
        .join("share")
        .join("clipnest")
}

#[cfg(test)]
mod tests {
    use super::*;

    fn temp_root(tag: &str) -> PathBuf {
        std::env::temp_dir().join(format!(
            "clipnest-paths-{tag}-{}-{}",
            std::process::id(),
            crate::model::now()
        ))
    }

    #[test]
    fn layout_is_inside_root() {
        let p = Paths::from_root("/tmp/x");
        assert_eq!(p.db(), Path::new("/tmp/x/clipnest.db"));
        assert_eq!(p.settings(), Path::new("/tmp/x/settings.json"));
        assert_eq!(p.sync_config(), Path::new("/tmp/x/sync.json"));
        assert_eq!(p.backups_dir(), Path::new("/tmp/x/backups"));
        assert_eq!(p.backup("a.cnbak"), Path::new("/tmp/x/backups/a.cnbak"));
        let hash = "ab".repeat(32);
        assert_eq!(
            p.image_origin(&hash),
            Path::new(&format!("/tmp/x/images/origin/ab/{hash}.png"))
        );
        assert_eq!(
            p.image_thumbnail(&hash),
            Path::new(&format!("/tmp/x/images/thumbnails/ab/{hash}.png"))
        );
        assert_eq!(image_name("cd"), "cd.png");
    }

    #[test]
    fn ensure_creates_all_dirs_and_is_idempotent() {
        let root = temp_root("ensure");
        let p = Paths::from_root(&root);
        assert!(!root.exists());
        p.ensure().unwrap();
        p.ensure().unwrap();
        for dir in [
            root.clone(),
            root.join("images/origin"),
            root.join("images/thumbnails"),
            root.join("backups"),
        ] {
            assert!(dir.is_dir(), "缺目录 {}", dir.display());
        }

        // 图片分片目录按需创建
        let hash = "ff".repeat(32);
        let (origin, thumb) = p.ensure_image_dirs(&hash).unwrap();
        assert!(origin.parent().unwrap().is_dir());
        assert!(thumb.parent().unwrap().is_dir());
        assert!(origin.starts_with(root.join("images/origin/ff")));
        let _ = std::fs::remove_dir_all(&root);
    }

    #[test]
    fn image_name_accepts_odd_hashes() {
        let p = Paths::from_root("/tmp/y");
        // 不足 2 字符退到 00 分片，不 panic
        assert!(p.image_origin("a").ends_with("origin/00/a.png"));
        assert!(p.image_origin("").ends_with("origin/00/.png"));
    }

    #[test]
    fn platform_root_follows_each_os() {
        let home = Some(PathBuf::from("/Users/me"));
        let appdata = Some(PathBuf::from("C:\\Users\\me\\AppData\\Roaming"));
        let xdg = Some(PathBuf::from("/home/me/.local/share"));

        assert_eq!(
            platform_root(true, false, None, Some(PathBuf::from("/x")), home.clone()),
            PathBuf::from("/Users/me/Library/Application Support/clipnest")
        );
        assert_eq!(
            platform_root(false, true, appdata.clone(), None, home.clone()),
            PathBuf::from("C:\\Users\\me\\AppData\\Roaming").join("clipnest")
        );
        assert_eq!(
            platform_root(false, false, None, xdg, home.clone()),
            PathBuf::from("/home/me/.local/share/clipnest")
        );
        // 什么都没有也别 panic
        assert!(platform_root(false, false, None, None, None).ends_with("clipnest"));
    }

    #[test]
    fn platform_matches_this_os() {
        let p = Paths::platform();
        assert!(p.root().ends_with("clipnest"), "{}", p.root().display());
    }
}
