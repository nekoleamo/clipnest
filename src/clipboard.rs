//! 剪贴板读写：采集（读）、写回（粘贴）、回环抑制、⌘V 注入。
//!
//! P0 验证过的时序都留在这里，改平台行为必须回 `docs/2026-11-15-nsh-验证记录.md` 复测：
//! - 轮询 `changeCount`，120 ms 间隔（全 flavor 采集实测 128 ms）
//! - 自身写回后登记 changeCount，见到它即跳过（契约 10）
//! - 粘贴：先写回，再延后 120 ms 发 ⌘V（面板不抢焦点，前台应用不变）

use std::sync::atomic::{AtomicI64, Ordering};

use crate::model::{Item, ItemKind};

/// 一次采集的结果。
#[derive(Debug, Clone, Default, PartialEq, Eq)]
pub struct Snapshot {
    /// flavor 名单，如 `text+html`。
    pub flavors: String,
    /// 摘要（日志用，已压平换行）。
    pub excerpt: String,
    /// 纯文本 flavor 的原文。
    pub text: Option<String>,
    /// 文件 flavor 的路径。
    pub files: Vec<String>,
}

/// 自身写回后登记的 changeCount：watcher 见到它即跳过。
static OWN_WRITE: AtomicI64 = AtomicI64::new(-1);

/// 登记一次自身写回，返回登记到的 changeCount。
pub fn mark_self_write() -> i64 {
    let cc = change_count();
    OWN_WRITE.store(cc, Ordering::SeqCst);
    cc
}

/// 这个 changeCount 是不是自己写回造成的。
pub fn is_self_write(cc: i64) -> bool {
    OWN_WRITE.load(Ordering::SeqCst) == cc
}

/// 读一次剪贴板；没有任何 flavor 时返回 `None`。
pub fn capture() -> Option<Snapshot> {
    use clipboard_rs::{Clipboard, ClipboardContext, ContentFormat};

    let ctx = ClipboardContext::new().ok()?;
    let mut flavors: Vec<&str> = Vec::new();
    let mut excerpt = String::new();
    let mut text = None;
    let mut files = Vec::new();

    if ctx.has(ContentFormat::Files) {
        flavors.push("files");
        if let Ok(list) = ctx.get_files() {
            excerpt = list.join("|");
            files = list;
        }
    }
    if ctx.has(ContentFormat::Image) {
        flavors.push("image");
    }
    if ctx.has(ContentFormat::Html) {
        flavors.push("html");
        if excerpt.is_empty()
            && let Ok(html) = ctx.get_html()
        {
            excerpt = html;
        }
    }
    if ctx.has(ContentFormat::Rtf) {
        flavors.push("rtf");
        if excerpt.is_empty()
            && let Ok(rtf) = ctx.get_rich_text()
        {
            excerpt = rtf;
        }
    }
    if ctx.has(ContentFormat::Text) {
        flavors.push("text");
        if let Ok(t) = ctx.get_text() {
            if excerpt.is_empty() {
                excerpt = t.clone();
            }
            if !t.is_empty() {
                text = Some(t);
            }
        }
    }

    if flavors.is_empty() {
        return None;
    }
    let excerpt: String = excerpt.chars().take(60).collect();
    Some(Snapshot {
        flavors: flavors.join("+"),
        excerpt: excerpt.replace('\n', "\\n"),
        text,
        files,
    })
}

/// 把一条历史写回剪贴板，并登记回环抑制。返回写回后的 changeCount。
pub fn write_back(item: &Item) -> Result<i64, String> {
    use clipboard_rs::{Clipboard, ClipboardContext};

    if item.kind == ItemKind::Image {
        return Err("图片条目粘贴要等 P3 的资源管线".to_owned());
    }
    let ctx = ClipboardContext::new().map_err(|e| format!("剪贴板不可用：{e}"))?;
    match item.kind {
        ItemKind::Text => ctx.set_text(item.content.clone()),
        ItemKind::Html => ctx.set_html(item.content.clone()),
        ItemKind::Rtf => ctx.set_rich_text(item.content.clone()),
        ItemKind::Image => unreachable!("图片在建剪贴板上下文前就被拒绝"),
        ItemKind::Files => ctx.set_files(
            item.content
                .lines()
                .filter(|l| !l.is_empty())
                .map(str::to_owned)
                .collect(),
        ),
    }
    .map_err(|e| format!("写回剪贴板失败：{e}"))?;
    Ok(mark_self_write())
}

/// 写回 + 隐藏面板后调用：延后 120 ms 发 ⌘V。
///
/// 用独立线程睡，避免卡住 UI；P0 已验证从非 UI 线程发 CGEvent 有效。
pub fn paste_after_write_back() {
    std::thread::spawn(|| {
        std::thread::sleep(std::time::Duration::from_millis(120));
        let ok = post_cmd_v();
        crate::logs::line(format!("AUTOPASTE cmd+v posted ok={ok}"));
    });
}

/// 面板出现前的前台应用 pid（粘贴时切回去）。
static PREV_PID: std::sync::atomic::AtomicI32 = std::sync::atomic::AtomicI32::new(0);

/// 记下当前前台应用（主线程调用：面板还没抢走焦点时）。
#[cfg(target_os = "macos")]
pub fn remember_front_app() {
    use objc2_app_kit::NSWorkspace;
    if objc2::MainThreadMarker::new().is_none() {
        log::warn!("remember_front_app 必须从主线程调用");
        return;
    }
    let ws = NSWorkspace::sharedWorkspace();
    if let Some(app) = ws.frontmostApplication() {
        let pid = app.processIdentifier();
        if pid != std::process::id() as i32 {
            PREV_PID.store(pid, Ordering::SeqCst);
            crate::logs::line(format!("FRONT remember pid={pid}"));
        }
    }
}

#[cfg(not(target_os = "macos"))]
pub fn remember_front_app() {}

/// 记下的前台应用 pid（0 = 还没记过）。
pub fn front_pid() -> i32 {
    PREV_PID.load(Ordering::SeqCst)
}

/// 切回面板出现前的应用；粘贴的 ⌘V 要发给它。
#[cfg(target_os = "macos")]
pub fn return_to_previous_app() -> bool {
    use objc2_app_kit::{NSApplicationActivationOptions, NSRunningApplication};
    let pid = front_pid();
    if pid <= 0 || pid == std::process::id() as i32 {
        return false;
    }
    let Some(app) = NSRunningApplication::runningApplicationWithProcessIdentifier(pid) else {
        crate::logs::line(format!("FRONT gone pid={pid}"));
        return false;
    };
    // macOS 14 起 ignoringOtherApps 无效果但也不报错，仍用它兼容旧系统。
    #[allow(deprecated)]
    let ok = app.activateWithOptions(NSApplicationActivationOptions::ActivateIgnoringOtherApps);
    crate::logs::line(format!("FRONT activate pid={pid} ok={ok}"));
    ok
}

#[cfg(not(target_os = "macos"))]
pub fn return_to_previous_app() -> bool {
    false
}

/// 诊断用：当前 key window 与 app 是否 active。
///
/// P0 记录的「面板能打字」依赖窗口成为 key window；这条日志用来在实测里确认。
#[cfg(target_os = "macos")]
pub fn key_window_state() -> String {
    use objc2_app_kit::NSApplication;
    // SAFETY: 只在 UI 线程调用（窗口命令都在主线程执行）。
    let Some(mtm) = objc2::MainThreadMarker::new() else {
        return "<非主线程>".to_owned();
    };
    let app = NSApplication::sharedApplication(mtm);
    let active = app.isActive();
    match app.keyWindow() {
        Some(w) => format!("key=Some({:?}) active={active}", w.title()),
        None => format!("key=None active={active}"),
    }
}

#[cfg(not(target_os = "macos"))]
pub fn key_window_state() -> String {
    "<n/a>".to_owned()
}

/// 当前剪贴板 changeCount（非 macOS 先给 0，Windows 后端 P5 再实现）。
#[cfg(target_os = "macos")]
pub fn change_count() -> i64 {
    use objc2_app_kit::NSPasteboard;
    NSPasteboard::generalPasteboard().changeCount() as i64
}

#[cfg(not(target_os = "macos"))]
pub fn change_count() -> i64 {
    0
}

/// 辅助功能授权状态（自动粘贴 CGEvent 的前置条件）。
#[cfg(target_os = "macos")]
pub fn ax_trusted() -> bool {
    #[link(name = "ApplicationServices", kind = "framework")]
    unsafe extern "C" {
        fn AXIsProcessTrusted() -> bool;
    }
    unsafe { AXIsProcessTrusted() }
}

#[cfg(not(target_os = "macos"))]
pub fn ax_trusted() -> bool {
    false
}

/// 发送 ⌘V（进程内 CGEvent）。
#[cfg(target_os = "macos")]
fn post_cmd_v() -> bool {
    use core_graphics::event::{CGEvent, CGEventFlags, CGEventTapLocation, CGKeyCode};
    use core_graphics::event_source::{CGEventSource, CGEventSourceStateID};

    const KEY_V: CGKeyCode = 9;
    let Ok(source) = CGEventSource::new(CGEventSourceStateID::HIDSystemState) else {
        return false;
    };
    let Ok(down) = CGEvent::new_keyboard_event(source.clone(), KEY_V, true) else {
        return false;
    };
    down.set_flags(CGEventFlags::CGEventFlagCommand);
    down.post(CGEventTapLocation::HID);
    std::thread::sleep(std::time::Duration::from_millis(40));
    let Ok(up) = CGEvent::new_keyboard_event(source, KEY_V, false) else {
        return false;
    };
    up.set_flags(CGEventFlags::CGEventFlagCommand);
    up.post(CGEventTapLocation::HID);
    true
}

#[cfg(not(target_os = "macos"))]
fn post_cmd_v() -> bool {
    false
}

/// 当前前台应用的本地化名字（用于判断面板是否抢了焦点）。
pub fn frontmost() -> String {
    let out = std::process::Command::new("sh")
        .arg("-c")
        .arg("lsappinfo info -only name $(lsappinfo front) 2>/dev/null | head -1")
        .output();
    match out {
        Ok(o) => String::from_utf8_lossy(&o.stdout).trim().to_string(),
        Err(e) => format!("<err {e}>"),
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    fn item(kind: ItemKind, content: &str) -> Item {
        Item {
            id: 1,
            content_hash: String::new(),
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
            created_at: String::new(),
            updated_at: String::new(),
        }
    }

    #[test]
    fn self_write_is_recognised_by_its_change_count() {
        // 真实 changeCount 在测试环境可能拿不到（无 GUI 会话时也有值），
        // 这里只验证登记/比对的逻辑本身。
        let cc = mark_self_write();
        assert!(is_self_write(cc));
        assert!(!is_self_write(cc + 1));
        // 再登记一次就只认新的
        let cc2 = mark_self_write();
        assert!(is_self_write(cc2));
        if cc != cc2 {
            assert!(!is_self_write(cc));
        }
    }

    #[test]
    fn image_items_refuse_to_paste_yet() {
        let err = write_back(&item(ItemKind::Image, "abc.png")).unwrap_err();
        assert!(err.contains("P3"), "{err}");
    }
}
