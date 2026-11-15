//! clipnest：跨平台剪贴板历史管理器（macOS 先行）。
//!
//! 本文件只放组件与 UI 装配；其余各在自己的模块：
//! - [`clipboard`]：采集、写回、⌘V 注入、回环抑制
//! - [`store`]：采集结果落库
//! - [`db`]：schema / 去重 / 查询搜索 / 加密备份（[`backup`]）
//! - [`panel`]：面板（列表、搜索、粘贴动作）
//!
//! 所有观测写 `/tmp/clipnest.log`（`logs::line`），验证脚本读它。

use std::cell::{Cell, RefCell};
use std::sync::OnceLock;
use std::sync::atomic::{AtomicU64, Ordering};
use std::time::Duration;

use nsh_base::theme;
use nsh_core::desktop::{WindowAttributes, WindowKind};
use nsh_core::element::desktop::{WindowRef, current_window, register_global_shortcut, window};
use nsh_core::prelude::*;

pub mod backup;
pub mod clipboard;
pub mod db;
pub mod logs;
pub mod model;
pub mod panel;
pub mod paths;
pub mod store;

/// 供打包器/TSX 侧引用的模块注册（`nsh.json` 的 `"rust"`）。
#[native_module("clipnest/native")]
fn native(registry: &mut Registry) {
    registry.register::<App>();
}

thread_local! {
    /// 面板窗口句柄（窗口内容渲染时拿到）。
    static PANEL: RefCell<Option<WindowRef>> = const { RefCell::new(None) };
    /// 面板的 `open` 信号，供 [`dismiss`] 收起面板。
    static OPEN: Cell<Option<Signal<bool>>> = const { Cell::new(None) };
}

/// 面板被打开过多少次（面板据此判断要不要重查列表）。
static OPENS: AtomicU64 = AtomicU64::new(0);

/// 打开计数，面板用它发现「又开了一次」。
pub fn opens() -> u64 {
    OPENS.load(Ordering::Relaxed)
}

/// 收起面板（粘贴完成后由 `panel` 调用）。
pub fn dismiss() {
    OPEN.with(|slot| {
        if let Some(open) = slot.get() {
            open.set(false);
        }
    });
    // 面板抢了前台，收起时要还给用户原来那个应用；粘贴也走这条路
    // （`paste_after_write_back` 在 120 ms 后才发 ⌘V，切回已生效）。
    run_on_main_thread(clipboard::return_to_previous_app);
}

/// 应用自身的根组件。
#[component]
pub fn App() -> impl IntoElement {
    boot();
    let open = signal(false);
    OPEN.with(|slot| slot.set(Some(open)));

    // 全局快捷键：切换面板。注册一次即可。
    effect(move || {
        let answer = register_global_shortcut("mod+shift+v", move || {
            let before = open.get();
            open.set(!before);
            logs::line(format!("SHORTCUT before={before} after={}", open.get()));
            if !before {
                // 面板会激活自己抢走键盘；先记下当前前台应用，粘贴时切回去。
                run_on_main_thread(clipboard::remember_front_app);
                focus_when_ready(0);
            }
        });
        spawn(async move {
            let ok = answer.await;
            logs::line(format!("BOOT shortcut mod+shift+v registered ok={ok}"));
        });
    });

    // nsh 的 run_app 总会开一个主窗口；clipnest 是常驻应用，主窗口不该露出来。
    effect(move || {
        if std::env::var("SPIKE_NO_HIDE").is_ok() {
            return;
        }
        let main = current_window();
        set_timeout(Duration::from_millis(60), move || {
            main.hide();
            logs::line(format!(
                "MAIN window hidden id={} main={}",
                main.id(),
                main.is_main()
            ));
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
            .default_size(size(px(480.0), px(420.0)))
            .attributes({
                let mut attrs = WindowAttributes::for_kind(WindowKind::Popup);
                // 激活自己：不激活的话 app 不 active，窗口永远成不了 key window，
                // 键盘全被前台应用吃掉（实测 key=None active=false）。
                // 粘贴时用 clipboard::return_to_previous_app() 切回去。
                attrs.activate = Some(std::env::var("SPIKE_NO_ACTIVATE").is_err());
                attrs
            })
            .open(open)
            .on_open_change(move |e| {
                // 系统也会关窗口（Esc / 失焦）：把 signal 拉回来，
                // 否则下次快捷键会把「以为还开着」的 open 置成 false，面板再也打不开。
                if open.get() != e.open {
                    open.set(e.open);
                }
                if e.open {
                    OPENS.fetch_add(1, Ordering::Relaxed);
                } else {
                    // 放弃粘贴（Esc / 失焦）也要把前台还给原来的应用。
                    run_on_main_thread(|| {
                        clipboard::return_to_previous_app();
                    });
                }
                logs::line(format!(
                    "PANEL open={} reason={:?} front={:?}",
                    e.open,
                    e.reason,
                    clipboard::frontmost()
                ));
            })
            .content(move || {
                let me = current_window();
                PANEL.with(|slot| {
                    if slot.borrow().is_none() {
                        *slot.borrow_mut() = Some(me.clone());
                    }
                });
                panel::panel()
            }),
    )
}

/// 窗口在 commit 之后才创建，focus 偶尔会落空（P0 坑 6）：
/// 每 80 ms 重试，直到窗口真的能接键盘。
fn focus_when_ready(attempt: u32) {
    if attempt > 10 {
        return;
    }
    set_timeout(
        Duration::from_millis(if attempt == 0 { 120 } else { 80 }),
        move || {
            // 关键：`focus()` 必须从主线程发出。`set_timeout` 的回调可能在
            // 别的线程跑（实测 `MainThreadMarker::new()` 为 None），从非主线程
            // 调 AppKit 的 makeKeyAndOrderFront 是空操作——面板收不到键盘。
            let answer = run_on_main_thread(move || {
                PANEL.with(|slot| {
                    if let Some(panel) = slot.borrow().as_ref() {
                        panel.focus();
                    }
                });
                let state = clipboard::key_window_state();
                logs::line(format!("PANEL focus try={attempt} {state}"));
                // 成了就停：面板成为 key window 后才能收键盘。
                state.starts_with("key=Some(")
            });
            spawn(async move {
                let focused = answer.await.unwrap_or(false);
                if !focused {
                    focus_when_ready(attempt + 1);
                }
            });
        },
    );
}

// ───────────────────────── 采集 ─────────────────────────

fn start_watcher() {
    std::thread::spawn(|| {
        let mut last = clipboard::change_count();
        logs::line(format!("WATCH start changeCount={last}"));
        loop {
            std::thread::sleep(Duration::from_millis(120));
            let now = clipboard::change_count();
            if now == last {
                continue;
            }
            last = now;

            if clipboard::is_self_write(now) {
                logs::line(format!("SKIP self-write changeCount={now}"));
                continue;
            }

            match clipboard::capture() {
                Some(snap) => {
                    logs::line(format!(
                        "CAPTURE cc={now} flavors={} :: {}",
                        snap.flavors, snap.excerpt
                    ));
                    store_capture(&snap);
                    spike_writeback(&snap);
                }
                None => logs::line(format!("CAPTURE cc={now} flavors=<none>")),
            }
        }
    });
}

/// 把一次采集落库；结果写日志（`DB INSERTED/DEDUPED id=…`）。
fn store_capture(snap: &clipboard::Snapshot) {
    let captured = store::Captured {
        text: snap.text.clone(),
        files: snap.files.clone(),
    };
    if captured.is_empty() {
        logs::line("DB SKIP 无可入库内容");
        return;
    }
    let Some(db) = store::db() else {
        logs::line("DB UNAVAILABLE 跳过入库");
        return;
    };
    let Ok(conn) = db.lock() else {
        logs::line("DB LOCK ERROR 跳过入库");
        return;
    };
    match store::record(&conn, &captured) {
        Ok(stored) => {
            for s in stored {
                logs::line(format!("DB {} kind={}", s.label(), s.kind.as_str()));
            }
        }
        Err(e) => logs::line(format!("DB ERROR {e}")),
    }
}

/// P0 留下的自检开关：`SPIKE_WRITEBACK=1` 时把采集到的内容写回剪贴板，
/// 验证回环抑制；再给 `SPIKE_AUTOPASTE=1` 就连 ⌘V 一起验。
fn spike_writeback(snap: &clipboard::Snapshot) {
    if std::env::var("SPIKE_WRITEBACK").is_err() {
        return;
    }
    use clipboard_rs::{Clipboard, ClipboardContext};
    let Ok(ctx) = ClipboardContext::new() else {
        return;
    };
    if ctx.set_text(snap.excerpt.clone()).is_ok() {
        let after = clipboard::mark_self_write();
        logs::line(format!("WRITEBACK cc={after} expect SKIP next"));
        if std::env::var("SPIKE_AUTOPASTE").is_ok() {
            clipboard::paste_after_write_back();
        }
    }
}

/// 让 main 启动 watcher（幂等）。
pub fn boot() {
    static ONCE: OnceLock<()> = OnceLock::new();
    ONCE.get_or_init(|| {
        logs::line(format!(
            "BOOT pid={} front={} axTrusted={} db={} items={:?}",
            std::process::id(),
            clipboard::frontmost(),
            clipboard::ax_trusted(),
            store::db_path().display(),
            store::count()
        ));
        start_watcher();
    });
}
