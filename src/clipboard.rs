//! 剪贴板读写：采集（读）、写回（粘贴）、回环抑制、⌘V 注入。
//!
//! P0 验证过的时序都留在这里，改平台行为必须回 `docs/2026-11-15-nsh-验证记录.md` 复测：
//! - 轮询 `changeCount`，120 ms 间隔（全 flavor 采集实测 128 ms）
//! - 自身写回后登记 changeCount，见到它即跳过（契约 10）
//! - 粘贴：先写回，再延后 120 ms 发 ⌘V（面板不抢焦点，前台应用不变）

use std::sync::atomic::{AtomicI64, Ordering};

use crate::model::{Item, ItemKind};

/// 采集到的图片（PNG 编码后的字节）。
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct ImageBytes {
    pub png: Vec<u8>,
    pub width: u32,
    pub height: u32,
}

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
    /// 图片 flavor（已编码成 PNG）。
    pub image: Option<ImageBytes>,
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
    use clipboard_rs::common::RustImage;
    use clipboard_rs::{Clipboard, ClipboardContext, ContentFormat};

    let ctx = ClipboardContext::new().ok()?;
    let mut flavors: Vec<&str> = Vec::new();
    let mut excerpt = String::new();
    let mut text = None;
    let mut files = Vec::new();
    let mut image = None;

    if ctx.has(ContentFormat::Files) {
        flavors.push("files");
        if let Ok(list) = ctx.get_files() {
            excerpt = list.join("|");
            files = list;
        }
    }
    if ctx.has(ContentFormat::Image) {
        flavors.push("image");
        // 编码成 PNG 才能落盘与算内容哈希；解码失败就只记 flavor 不落库。
        if let Ok(data) = ctx.get_image()
            && let Ok(png) = data.to_png()
        {
            let (width, height) = data.get_size();
            image = Some(ImageBytes {
                png: png.get_bytes().to_vec(),
                width,
                height,
            });
            if excerpt.is_empty() {
                excerpt = format!("<图片 {width}×{height}>");
            }
        }
    }
    // clipboard-rs 的 image 只开 png/jpeg：macOS 截图常只有 TIFF，自己兜底解一次。
    if image.is_none() && flavors.contains(&"image") {
        image = read_pasteboard_image();
        if let Some(img) = &image
            && excerpt.is_empty()
        {
            excerpt = format!("<图片 {}×{}>", img.width, img.height);
        }
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
        image,
    })
}

/// macOS：直接从 `NSPasteboard` 取 PNG/TIFF 并自己解码成 PNG。
///
/// `clipboard-rs` 的 `image` 依赖只开 `png`/`jpeg` 解码，而系统截图常常只在
/// 剪贴板里放 TIFF，所以这条兜底是必需的（实测 `tools/pb.swift image` 就走到这里）。
#[cfg(target_os = "macos")]
fn read_pasteboard_image() -> Option<ImageBytes> {
    use objc2_app_kit::{
        NSBitmapImageFileType, NSBitmapImageRep, NSImage, NSPasteboard,
    };
    use objc2::AnyThread;
    use objc2_foundation::{NSDictionary, NSString};

    let pb = NSPasteboard::generalPasteboard();

    // 1) 直接有 PNG 就用（最快，也不经系统重编码）
    let png_type = NSString::from_str("public.png");
    if let Some(data) = pb.dataForType(&png_type) {
        let bytes = data.to_vec();
        if let Ok(img) = image::load_from_memory(&bytes) {
            let (width, height) = (img.width(), img.height());
            return Some(ImageBytes {
                png: bytes,
                width,
                height,
            });
        }
    }

    // 2) 只有 TIFF（macOS 截图的常态）：让系统解成位图再编码成 PNG，
    //    避免依赖 image crate 的 TIFF 解码器（实测它解不了 NSImage 出的 TIFF）。
    let image = NSImage::initWithPasteboard(NSImage::alloc(), &pb)?;
    let tiff = image.TIFFRepresentation()?;
    let rep = NSBitmapImageRep::initWithData(NSBitmapImageRep::alloc(), &tiff)?;
    let empty = NSDictionary::new();
    // SAFETY: 属性字典为空是文档允许的用法。
    let data = unsafe { rep.representationUsingType_properties(NSBitmapImageFileType::PNG, &empty) }?;
    let png = data.to_vec();
    let img = image::load_from_memory(&png).ok()?;
    let (width, height) = (img.width(), img.height());
    Some(ImageBytes { png, width, height })
}

#[cfg(not(target_os = "macos"))]
fn read_pasteboard_image() -> Option<ImageBytes> {
    None
}

/// 把一条历史写回剪贴板，并登记回环抑制。返回写回后的 changeCount。
pub fn write_back(item: &Item) -> Result<i64, String> {
    use clipboard_rs::{Clipboard, ClipboardContext};

    let ctx = ClipboardContext::new().map_err(|e| format!("剪贴板不可用：{e}"))?;
    match item.kind {
        ItemKind::Text => ctx.set_text(item.content.clone()),
        ItemKind::Html => ctx.set_html(item.content.clone()),
        ItemKind::Rtf => ctx.set_rich_text(item.content.clone()),
        ItemKind::Image => {
            use clipboard_rs::common::{RustImage, RustImageData};
            // `content` 存的是 `<hash>.png`，文件在 images/origin/<hh>/ 下。
            let hash = item.content.strip_suffix(".png").unwrap_or(&item.content);
            let path = crate::paths().image_origin(hash);
            if !path.exists() {
                return Err(format!("图片文件不见了：{}", path.display()));
            }
            let path = path.to_str().ok_or("图片路径不是 UTF-8")?;
            let data = RustImageData::from_path(path).map_err(|e| format!("读图片失败：{e}"))?;
            ctx.set_image(data)
        }
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

/// 把一批文件放到剪贴板（导出后调用：⌘V 到 Finder/微信就是粘文件）。
pub fn put_files_on_clipboard(paths: &[String]) -> Result<i64, String> {
    use clipboard_rs::{Clipboard, ClipboardContext};
    if paths.is_empty() {
        return Err("没有文件可放".to_owned());
    }
    let ctx = ClipboardContext::new().map_err(|e| format!("剪贴板不可用：{e}"))?;
    ctx.set_files(paths.to_vec())
        .map_err(|e| format!("放文件失败：{e}"))?;
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

/// 面板出现前的前台目标（macOS 存 pid，Windows 存 HWND；0 = 还没记过）。
static PREV_TARGET: std::sync::atomic::AtomicI64 = std::sync::atomic::AtomicI64::new(0);

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
            PREV_TARGET.store(pid as i64, Ordering::SeqCst);
            crate::logs::line(format!("FRONT remember pid={pid}"));
        }
    }
}

/// Windows：记下前台窗口句柄（粘贴时用它 SetForegroundWindow）。
#[cfg(target_os = "windows")]
pub fn remember_front_app() {
    use windows_sys::Win32::UI::WindowsAndMessaging::{
        GetForegroundWindow, GetWindowThreadProcessId,
    };
    // SAFETY: 只读系统状态。
    unsafe {
        let hwnd = GetForegroundWindow();
        if hwnd.is_null() {
            return;
        }
        let mut pid = 0u32;
        GetWindowThreadProcessId(hwnd, &mut pid);
        if pid == std::process::id() {
            return;
        }
        PREV_TARGET.store(hwnd as i64, Ordering::SeqCst);
        crate::logs::line(format!("FRONT remember hwnd={} pid={pid}", hwnd as i64));
    }
}

#[cfg(not(any(target_os = "macos", target_os = "windows")))]
pub fn remember_front_app() {}

/// 记下的前台目标（macOS: pid；Windows: HWND；0 = 还没记过）。
pub fn front_target() -> i64 {
    PREV_TARGET.load(Ordering::SeqCst)
}

/// 切回面板出现前的应用；粘贴的 ⌘V 要发给它。
#[cfg(target_os = "macos")]
pub fn return_to_previous_app() -> bool {
    use objc2_app_kit::{NSApplicationActivationOptions, NSRunningApplication};
    let pid = front_target() as i32;
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

/// Windows：把前台还给刚才那个窗口；被前台锁拒绝时返回 false。
#[cfg(target_os = "windows")]
pub fn return_to_previous_app() -> bool {
    use windows_sys::Win32::UI::WindowsAndMessaging::{IsWindow, SetForegroundWindow};
    let raw = front_target();
    if raw == 0 {
        return false;
    }
    let hwnd = raw as windows_sys::Win32::Foundation::HWND;
    // SAFETY: 句柄来自 GetForegroundWindow，IsWindow 先确认它还活着。
    unsafe {
        if IsWindow(hwnd) == 0 {
            crate::logs::line(format!("FRONT gone hwnd={raw}"));
            return false;
        }
        let ok = SetForegroundWindow(hwnd) != 0;
        crate::logs::line(format!("FRONT activate hwnd={raw} ok={ok}"));
        ok
    }
}

#[cfg(not(any(target_os = "macos", target_os = "windows")))]
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

/// Windows：前台窗口是不是自己（等价于 macOS 的 key window 判断）。
#[cfg(target_os = "windows")]
pub fn key_window_state() -> String {
    use windows_sys::Win32::UI::WindowsAndMessaging::{
        GetForegroundWindow, GetWindowThreadProcessId,
    };
    // SAFETY: 只读系统状态。
    unsafe {
        let hwnd = GetForegroundWindow();
        if hwnd.is_null() {
            return "key=None active=false".to_owned();
        }
        let mut pid = 0u32;
        GetWindowThreadProcessId(hwnd, &mut pid);
        let mine = pid == std::process::id();
        format!("key=Some(hwnd={}) active={mine}", hwnd as i64)
    }
}

#[cfg(not(any(target_os = "macos", target_os = "windows")))]
pub fn key_window_state() -> String {
    "<n/a>".to_owned()
}

/// 当前剪贴板版本号（macOS 是 `changeCount`，Windows 是剪贴板序列号）。
#[cfg(target_os = "macos")]
pub fn change_count() -> i64 {
    use objc2_app_kit::NSPasteboard;
    NSPasteboard::generalPasteboard().changeCount() as i64
}

#[cfg(target_os = "windows")]
pub fn change_count() -> i64 {
    use windows_sys::Win32::System::DataExchange::GetClipboardSequenceNumber;
    // SAFETY: 无参数、只读。
    unsafe { GetClipboardSequenceNumber() as i64 }
}

#[cfg(not(any(target_os = "macos", target_os = "windows")))]
pub fn change_count() -> i64 {
    0
}

/// 辅助功能授权状态（自动粘贴 CGEvent 的前置条件）。
#[cfg(target_os = "macos")]
pub fn ax_trusted() -> bool {
    // 自检：SPIKE_NO_AX=1 假装没授权，用来验面板的权限提示条。
    if std::env::var("SPIKE_NO_AX").is_ok() {
        return false;
    }
    #[link(name = "ApplicationServices", kind = "framework")]
    unsafe extern "C" {
        fn AXIsProcessTrusted() -> bool;
    }
    unsafe { AXIsProcessTrusted() }
}

/// Windows 没有辅助功能授权这道门槛（UIPI 的例外在粘贴失败时再处理）。
#[cfg(target_os = "windows")]
pub fn ax_trusted() -> bool {
    true
}

#[cfg(not(any(target_os = "macos", target_os = "windows")))]
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

/// 发送 Ctrl+V（四个 INPUT：Ctrl 按下、V 按下、V 抬起、Ctrl 抬起）。
#[cfg(target_os = "windows")]
fn post_cmd_v() -> bool {
    use windows_sys::Win32::UI::Input::KeyboardAndMouse::{
        INPUT, INPUT_0, INPUT_KEYBOARD, KEYBDINPUT, KEYBD_EVENT_FLAGS, KEYEVENTF_KEYUP, SendInput,
        VIRTUAL_KEY, VK_CONTROL, VK_V,
    };

    fn key(vk: VIRTUAL_KEY, flags: KEYBD_EVENT_FLAGS) -> INPUT {
        INPUT {
            r#type: INPUT_KEYBOARD,
            Anonymous: INPUT_0 {
                ki: KEYBDINPUT {
                    wVk: vk,
                    wScan: 0,
                    dwFlags: flags,
                    time: 0,
                    dwExtraInfo: 0,
                },
            },
        }
    }

    let inputs = [
        key(VK_CONTROL, 0),
        key(VK_V, 0),
        key(VK_V, KEYEVENTF_KEYUP),
        key(VK_CONTROL, KEYEVENTF_KEYUP),
    ];
    // SAFETY: 数组与长度一致，结构体全部零初始化后再填。
    let sent = unsafe {
        SendInput(
            inputs.len() as u32,
            inputs.as_ptr(),
            std::mem::size_of::<INPUT>() as i32,
        )
    };
    sent == inputs.len() as u32
}

#[cfg(not(any(target_os = "macos", target_os = "windows")))]
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

    /// 图片条目粘贴要读 `images/origin/<hh>/<hash>.png`；文件没了要给清楚的错，而不是 panic。
    #[test]
    fn image_without_file_reports_missing() {
        let err = write_back(&item(ItemKind::Image, "abcdef.png")).unwrap_err();
        assert!(err.contains("图片文件不见了"), "{err}");
    }
}
