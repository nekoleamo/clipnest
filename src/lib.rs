//! nsh 剪贴板管理器可行性 spike（macOS）。
//!
//! 验证四件事：
//! V1 Popup 窗口能否在不抢焦点的情况下弹出（activationPolicy=accessory + WindowKind::Popup）
//! V2 剪贴板多 flavor（text / html / rtf / image / files）能否读取，changeCount 轮询延迟多少
//! V3 写回回环抑制（自身写回不产生新记录）
//! V4 关闭 nsh 全部可选特性后，产物体积与构建时间
//!
//! 所有观测写入 /tmp/clipnest.log，便于脚本断言。

use std::cell::RefCell;
use std::sync::{Arc, Mutex, OnceLock};
use std::sync::atomic::{AtomicI64, Ordering};
use std::time::Duration;

pub mod backup;
pub mod db;
pub mod model;
pub mod paths;
pub mod store;

use nsh_base::theme;
use nsh_core::desktop::{WindowAttributes, WindowKind};
use nsh_core::element::desktop::{WindowRef, current_window, register_global_shortcut, window};
use nsh_core::prelude::*;

/// 供打包器/TSX 侧引用的模块注册（`nsh.json` `"rust"`）。
#[native_module("clipnest/native")]
fn native(registry: &mut Registry) {
    registry.register::<App>();
}

thread_local! {
    /// 面板窗口的句柄（渲染时拿到）。
    static PANEL: RefCell<Option<WindowRef>> = const { RefCell::new(None) };
}

/// 应用自身的根组件。
#[component]
pub fn App() -> impl IntoElement {
    boot();
    let open = signal(false);
    let lines = signal(String::from("waiting…"));

    // 面板里的行：轮询共享日志，避免跨线程碰 signal。
    set_interval(Duration::from_millis(400), move || {
        let snap = items()
            .lock()
            .map(|v| {
                v.iter()
                    .rev()
                    .take(6)
                    .cloned()
                    .collect::<Vec<_>>()
                    .join("\n")
            })
            .unwrap_or_default();
        lines.set(snap);
    });

    // 全局快捷键：切换面板。注册一次即可。
    effect(move || {
        let answer = register_global_shortcut("mod+shift+v", move || {
            let before = open.get();
            open.set(!before);
            log_line(format!("SHORTCUT before={before} after={}", open.get()));
            if !before {
                // 窗口在 commit 后才创建：延后一拍再取 key 焦点
                set_timeout(Duration::from_millis(120), || {
                    PANEL.with(|slot| match slot.borrow().as_ref() {
                        Some(panel) => {
                            panel.focus();
                            log_line(format!("PANEL focus() after open, canKey? state={:?}", panel.state().is_some()));
                        }
                        None => log_line("PANEL ref not ready".to_owned()),
                    });
                });
            }
        });
        spawn(async move {
            let ok = answer.await;
            log_line(format!("BOOT shortcut mod+shift+v registered ok={ok}"));
        });
    });

    // 自动弹出的自检：SPIKE_AUTOOPEN=1 时启动 4 秒后打开面板，用于测焦点。
    if std::env::var("SPIKE_AUTOOPEN").is_ok() {
        set_timeout(Duration::from_secs(4), move || open.set(true));
    }

    view().flex_1().bg(move || theme().background).child(
        window()
            .id("panel")
            .kind(WindowKind::Popup)
            .default_size(size(px(420.0), px(320.0)))
            .attributes({
                let mut attrs = WindowAttributes::for_kind(WindowKind::Popup);
                attrs.activate = Some(std::env::var("SPIKE_ACTIVATE").is_ok());
                attrs
            })
            .open(open)
            .on_open_change(move |e| {
                log_line(format!(
                    "PANEL open={} reason={:?} front={:?}",
                    e.open,
                    e.reason,
                    frontmost()
                ));
            })
            .content(move || {
                let me = current_window();
                PANEL.with(|slot| {
                    if slot.borrow().is_none() {
                        *slot.borrow_mut() = Some(me.clone());
                    }
                });
                on_key("x", |_| log_line("PANEL key x received".to_owned()));
                view()
                    .flex_1()
                    .p_4()
                    .gap_2()
                    .bg(move || theme().background)
                    .child(text("clipnest panel").text_size(px(15.0)))
                    .child(
                        text(move || format!("items: {}", count()))
                            .text_size(px(12.0))
                            .text_color(move || theme().muted_foreground),
                    )
                    .child(text(move || lines.get()).text_size(px(11.0)))
            }),
    )
}

// ───────────────────────── 观测与剪贴板 ─────────────────────────

fn items() -> &'static Arc<Mutex<Vec<String>>> {
    static ITEMS: OnceLock<Arc<Mutex<Vec<String>>>> = OnceLock::new();
    ITEMS.get_or_init(|| Arc::new(Mutex::new(Vec::new())))
}

fn count() -> usize {
    items().lock().map(|v| v.len()).unwrap_or(0)
}

fn log_line(line: String) {
    use std::io::Write;
    log::info!("{line}");
    if let Ok(mut f) = std::fs::OpenOptions::new()
        .create(true)
        .append(true)
        .open("/tmp/clipnest.log")
    {
        let _ = writeln!(f, "{line}");
    }
    if let Ok(mut v) = items().lock() {
        v.push(line);
    }
}

/// 辅助功能授权状态（自动粘贴 CGEvent 的前置条件）。
#[cfg(target_os = "macos")]
fn ax_trusted() -> bool {
    #[link(name = "ApplicationServices", kind = "framework")]
    unsafe extern "C" {
        fn AXIsProcessTrusted() -> bool;
    }
    unsafe { AXIsProcessTrusted() }
}

#[cfg(not(target_os = "macos"))]
fn ax_trusted() -> bool {
    false
}

/// 当前前台应用的本地化名字（用于判断面板是否抢了焦点）。
fn frontmost() -> String {
    let out = std::process::Command::new("sh")
        .arg("-c")
        .arg("lsappinfo info -only name $(lsappinfo front) 2>/dev/null | head -1")
        .output();
    match out {
        Ok(o) => String::from_utf8_lossy(&o.stdout).trim().to_string(),
        Err(e) => format!("<err {e}>"),
    }
}

#[cfg(target_os = "macos")]
fn change_count() -> i64 {
    use objc2_app_kit::NSPasteboard;
    NSPasteboard::generalPasteboard().changeCount() as i64
}

#[cfg(not(target_os = "macos"))]
fn change_count() -> i64 {
    0
}

/// 一次采集的结果。
struct Snapshot {
    /// flavor 名单，如 `text+html`。
    flavors: String,
    /// 摘要（日志与面板用，已压平换行）。
    excerpt: String,
    /// 要入库的纯文本。
    text: Option<String>,
    /// 要入库的文件路径。
    files: Vec<String>,
}

/// 读一次剪贴板。
fn capture_once() -> Option<Snapshot> {
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

/// 把一次采集落库；结果写日志（`DB INSERTED/DEDUPED id=…`）。
fn store_capture(snap: &Snapshot) {
    let captured = store::Captured {
        text: snap.text.clone(),
        files: snap.files.clone(),
    };
    if captured.is_empty() {
        log_line("DB SKIP 无可入库内容".to_owned());
        return;
    }
    let Some(db) = store::db() else {
        log_line("DB UNAVAILABLE 跳过入库".to_owned());
        return;
    };
    let Ok(conn) = db.lock() else {
        log_line("DB LOCK ERROR 跳过入库".to_owned());
        return;
    };
    match store::record(&conn, &captured) {
        Ok(stored) => {
            for s in stored {
                log_line(format!("DB {} kind={}", s.label(), s.kind.as_str()));
            }
        }
        Err(e) => log_line(format!("DB ERROR {e}")),
    }
}

/// 发送 ⌘V（用 core-graphics 的 CGEvent，进程内实现）。
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
    std::thread::sleep(Duration::from_millis(40));
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

/// 自身写回后登记的 changeCount：watcher 见到它即跳过。
static OWN_WRITE: AtomicI64 = AtomicI64::new(-1);

fn start_watcher() {
    std::thread::spawn(|| {
        let mut last = change_count();
        log_line(format!("WATCH start changeCount={last}"));
        loop {
            std::thread::sleep(Duration::from_millis(120));
            let now = change_count();
            if now == last {
                continue;
            }
            last = now;

            if OWN_WRITE.load(Ordering::SeqCst) == now {
                log_line(format!("SKIP self-write changeCount={now}"));
                continue;
            }

            match capture_once() {
                Some(snap) => {
                    log_line(format!(
                        "CAPTURE cc={now} flavors={} :: {}",
                        snap.flavors, snap.excerpt
                    ));
                    store_capture(&snap);
                    let excerpt = snap.excerpt.clone();
                    if std::env::var("SPIKE_WRITEBACK").is_ok() {
                        use clipboard_rs::{Clipboard, ClipboardContext};
                        if let Ok(ctx) = ClipboardContext::new() {
                            if ctx.set_text(excerpt.clone()).is_ok() {
                                let after = change_count();
                                OWN_WRITE.store(after, Ordering::SeqCst);
                                log_line(format!("WRITEBACK cc={after} expect SKIP next"));
                                if std::env::var("SPIKE_AUTOPASTE").is_ok() {
                                    std::thread::sleep(Duration::from_millis(120));
                                    let ok = post_cmd_v();
                                    log_line(format!("AUTOPASTE cmd+v posted ok={ok}"));
                                }
                            }
                        }
                    }
                }
                None => log_line(format!("CAPTURE cc={now} flavors=<none>")),
            }
        }
    });
}

/// 让 main 启动 watcher（幂等）。
pub fn boot() {
    static ONCE: OnceLock<()> = OnceLock::new();
    ONCE.get_or_init(|| {
        log_line(format!(
            "BOOT pid={} front={} axTrusted={} db={} items={:?}",
            std::process::id(),
            frontmost(),
            ax_trusted(),
            store::db_path().display(),
            store::count()
        ));
        start_watcher();
    });
}
