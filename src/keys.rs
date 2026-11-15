//! 平台相关的键位**展示**。
//!
//! 注册用的 keystroke 一直是 `mod+shift+v`（nsh 里 `mod` 在 macOS 是 Command、
//! 其它平台是 Control），但显示必须跟系统一致：macOS 用 `⌘⇧V`，Windows 用 `Ctrl+Shift+V`。
//! 面板底部提示、行内序号、设置窗都从这里取，别在 UI 里写死符号。

#[cfg(target_os = "macos")]
mod platform {
    /// 唤出面板的快捷键。
    pub const HOTKEY: &str = "⌘⇧V";
    /// 组合键前缀（行内序号 `1`–`9` 前面加它）。
    pub const SLOT_PREFIX: &str = "⌘";
    /// 面板底部的操作提示。
    pub const HINTS: &str =
        "↑↓ 选择 · ⏎ 粘贴 · ⌘1-9 直贴 · ⌘D 删除 · ⌘P 置顶 · ⌘S 收藏 · ⌘E 敏感 · ⌘Y 复制";
}

#[cfg(not(target_os = "macos"))]
mod platform {
    pub const HOTKEY: &str = "Ctrl+Shift+V";
    pub const SLOT_PREFIX: &str = "Ctrl+";
    pub const HINTS: &str =
        "↑↓ 选择 · ⏎ 粘贴 · Ctrl+1-9 直贴 · Ctrl+D 删除 · Ctrl+P 置顶 · Ctrl+S 收藏 · Ctrl+E 敏感 · Ctrl+Y 复制";
}

pub use platform::{HINTS, HOTKEY, SLOT_PREFIX};

/// 行内序号标签：`⌘1` / `Ctrl+1`。
pub fn slot(index: usize) -> String {
    format!("{SLOT_PREFIX}{}", index + 1)
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn labels_follow_the_platform() {
        if cfg!(target_os = "macos") {
            assert_eq!(HOTKEY, "⌘⇧V");
            assert_eq!(SLOT_PREFIX, "⌘");
            assert!(HINTS.contains('⌘'));
        } else {
            assert_eq!(HOTKEY, "Ctrl+Shift+V");
            assert_eq!(SLOT_PREFIX, "Ctrl+");
            assert!(HINTS.contains("Ctrl+"));
            assert!(!HINTS.contains('⌘'), "Windows 上不该出现 macOS 符号");
        }
    }

    #[test]
    fn slot_numbers_from_one() {
        assert_eq!(slot(0), format!("{SLOT_PREFIX}1"));
        assert_eq!(slot(8), format!("{SLOT_PREFIX}9"));
    }

    /// HINTS 里的每个动作都要能对上真实绑定（防文案飘）。
    #[test]
    fn hints_mention_every_action() {
        for word in ["直贴", "删除", "置顶", "收藏", "敏感", "复制"] {
            assert!(HINTS.contains(word), "提示里缺 {word}");
        }
    }
}
