//! 应用设置：`<数据根>/settings.json`。
//!
//! 只存「改了立刻能看出效果」的项；未知字段解析时忽略（向前兼容），
//! 文件坏了就退回默认值并记一条日志，不让应用起不来。

use std::path::Path;

use serde::{Deserialize, Serialize};

use crate::paths::Paths;

/// 面板高度下限 / 上限（点）。
pub const PANEL_HEIGHT_MIN: i32 = 240;
pub const PANEL_HEIGHT_MAX: i32 = 1200;

/// 面板宽度（固定，P2 不开放调整）。
pub const PANEL_WIDTH: i32 = 480;

/// 用户设置。
#[derive(Debug, Clone, PartialEq, Serialize, Deserialize)]
#[serde(default)]
pub struct Settings {
    /// 历史保留条数上限（0 = 不限）。清理时收藏与置顶受保护（契约 9）。
    pub max_items: i64,
    /// 面板高度（点）。
    pub panel_height: i32,
    /// 这些应用里复制的内容不进历史（bundle id / 进程名）。
    ///
    /// 空 = 用 `sensitive::DEFAULT_APPS` 的内置表；填了就以这里为准。
    pub sensitive_apps: Vec<String>,
    /// 关掉就不按应用过滤（用户自担风险，自己用 `⌘E` 手动标)。
    pub ignore_sensitive_apps: bool,
    /// 导出目录（空 = `~/Downloads`）。
    pub export_dir: String,
}

impl Default for Settings {
    fn default() -> Self {
        Self {
            max_items: 5000,
            panel_height: 420,
            sensitive_apps: Vec::new(),
            ignore_sensitive_apps: true,
            export_dir: String::new(),
        }
    }
}

impl Settings {
    /// 把越界的值拉回合法范围（手改过 json 也安全）。
    pub fn normalized(mut self) -> Self {
        self.max_items = self.max_items.clamp(0, 1_000_000);
        self.panel_height = self.panel_height.clamp(PANEL_HEIGHT_MIN, PANEL_HEIGHT_MAX);
        self
    }

    /// 读设置；文件不存在或解析失败时给默认值。
    pub fn load(paths: &Paths) -> Self {
        let path = paths.settings();
        match std::fs::read_to_string(&path) {
            Ok(text) => match serde_json::from_str::<Settings>(&text) {
                Ok(s) => return s.normalized(),
                Err(e) => crate::logs::line(format!("SETTINGS 解析失败（用默认值）：{e}")),
            },
            Err(e) if e.kind() == std::io::ErrorKind::NotFound => {}
            Err(e) => crate::logs::line(format!("SETTINGS 读取失败（用默认值）：{e}")),
        }
        Settings::default()
    }

    /// 原子写（先写 .tmp 再改名）。
    pub fn save(&self, paths: &Paths) -> std::io::Result<()> {
        let text = serde_json::to_string_pretty(self)?;
        write_atomic(&paths.settings(), text.as_bytes())
    }
}

fn write_atomic(path: &Path, bytes: &[u8]) -> std::io::Result<()> {
    if let Some(dir) = path.parent()
        && !dir.as_os_str().is_empty()
    {
        std::fs::create_dir_all(dir)?;
    }
    let tmp = path.with_extension("json.tmp");
    std::fs::write(&tmp, bytes)?;
    std::fs::rename(&tmp, path)
}

#[cfg(test)]
mod tests {
    use super::*;

    fn temp_paths(tag: &str) -> Paths {
        Paths::from_root(std::env::temp_dir().join(format!(
            "clipnest-settings-{tag}-{}",
            crate::model::now()
        )))
    }

    #[test]
    fn missing_file_gives_defaults() {
        let paths = temp_paths("missing");
        assert_eq!(Settings::load(&paths), Settings::default());
        let _ = std::fs::remove_dir_all(paths.root());
    }

    #[test]
    fn save_then_load_round_trips() {
        let paths = temp_paths("roundtrip");
        let want = Settings {
            max_items: 123,
            panel_height: 600,
            ..Settings::default()
        };
        want.save(&paths).unwrap();
        assert!(paths.settings().exists());
        assert_eq!(Settings::load(&paths), want);
        // 不留临时文件
        assert!(!paths.settings().with_extension("json.tmp").exists());
        let _ = std::fs::remove_dir_all(paths.root());
    }

    #[test]
    fn broken_or_partial_files_fall_back_or_fill_in() {
        let paths = temp_paths("broken");
        std::fs::create_dir_all(paths.root()).unwrap();

        std::fs::write(paths.settings(), b"{ not json").unwrap();
        assert_eq!(Settings::load(&paths), Settings::default(), "坏文件用默认值");

        // 只写了一个字段：其余用默认值（serde default）
        std::fs::write(paths.settings(), br#"{"max_items": 7}"#).unwrap();
        let s = Settings::load(&paths);
        assert_eq!(s.max_items, 7);
        assert_eq!(s.panel_height, Settings::default().panel_height);
        let _ = std::fs::remove_dir_all(paths.root());
    }

    #[test]
    fn out_of_range_values_are_clamped() {
        let s = Settings {
            max_items: -5,
            panel_height: 99_999,
            ..Settings::default()
        }
        .normalized();
        assert_eq!(s.max_items, 0);
        assert_eq!(s.panel_height, PANEL_HEIGHT_MAX);
        let low = Settings {
            max_items: 1,
            panel_height: 1,
            ..Settings::default()
        }
        .normalized();
        assert_eq!(low.panel_height, PANEL_HEIGHT_MIN);
    }
}
