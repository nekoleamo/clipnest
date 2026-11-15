//! clipnest：跨平台剪贴板历史管理器（macOS 先行）。
//!
//! 本文件只放组件与 UI 装配；其余各在自己的模块：
//! - [`clipboard`]：采集、写回、⌘V 注入、回环抑制
//! - [`store`]：采集结果落库
//! - [`db`]：schema / 去重 / 查询搜索 / 加密备份（[`backup`]）
//! - [`panel`]：面板（列表、搜索、粘贴动作）
//! - [`prefs`] / [`settings`]：偏好设置窗与 `settings.json`
//! - [`tray`]：菜单栏图标与菜单
//!
//! 所有观测写 `/tmp/clipnest.log`（`logs::line`），验证脚本读它。

use std::cell::RefCell;
use std::sync::OnceLock;
use std::sync::atomic::{AtomicU64, Ordering};
use std::time::Duration;

use nsh_core::desktop::{WindowAttributes, WindowFrame, WindowKind};
use nsh_core::element::desktop::{WindowRef, current_window, register_global_shortcut, window};
use nsh_core::prelude::*;

pub mod backup;
pub mod clipboard;
pub mod db;
pub mod logs;
pub mod model;
pub mod panel;
pub mod paths;
pub mod prefs;
pub mod settings;
pub mod store;
pub mod tray;

/// 供打包器/TSX 侧引用的模块注册（`nsh.json` 的 `"rust"`）。
#[native_module("clipnest/native")]
fn native(registry: &mut Registry) {
    registry.register::<App>();
}

thread_local! {
    /// 面板窗口句柄（窗口内容渲染时拿到）。
    static PANEL: RefCell<Option<WindowRef>> = const { RefCell::new(None) };
}

/// 面板 / 设置窗的 `open` 信号。放 static 而不是 thread_local：
/// `set_timeout`、托盘菜单的回调可能在别的线程跑，thread_local 会拿到空值。
static OPEN: OnceLock<Signal<bool>> = OnceLock::new();
static PREFS: OnceLock<Signal<bool>> = OnceLock::new();

/// 托盘只挂一次（可能有两个窗口各自渲染 App，用 static 而不是 thread_local）。
static TRAY_ATTACHED: std::sync::atomic::AtomicBool = std::sync::atomic::AtomicBool::new(false);

static PATHS: OnceLock<paths::Paths> = OnceLock::new();
static SETTINGS: OnceLock<std::sync::Mutex<settings::Settings>> = OnceLock::new();

/// 数据根目录（进程内唯一，按平台推断一次）。
pub fn paths() -> &'static paths::Paths {
    PATHS.get_or_init(paths::Paths::platform)
}

/// 当前设置（拷贝；读不到就给默认值）。
pub fn settings() -> settings::Settings {
    SETTINGS
        .get_or_init(|| std::sync::Mutex::new(settings::Settings::load(paths())))
        .lock()
        .map(|s| s.clone())
        .unwrap_or_default()
}

/// 存设置（越界值夹紧），成功返回 true。
pub fn save_settings(next: settings::Settings) -> bool {
    let s = next.normalized();
    if let Err(e) = s.save(paths()) {
        logs::line(format!("SETTINGS 写入失败：{e}"));
        return false;
    }
    let cell = SETTINGS.get_or_init(|| std::sync::Mutex::new(s.clone()));
    if let Ok(mut guard) = cell.lock() {
        *guard = s;
    }
    true
}

/// 退出应用（托盘菜单用）。
pub fn quit_app() {
    logs::line("QUIT from tray".to_owned());
    nsh_core::element::desktop::quit();
}

/// 显示/收起面板（快捷键与托盘共用）。
pub fn toggle_panel() {
    if let Some(open) = OPEN.get() {
        let before = open.get();
        open.set(!before);
        logs::line(format!("PANEL toggle before={before} after={}", open.get()));
        if !before {
            // 面板会激活自己抢走键盘；先记下当前前台应用，粘贴时切回去。
            run_on_main_thread(clipboard::remember_front_app);
            focus_when_ready(0);
        }
    }
}

/// 打开设置窗。
pub fn open_prefs() {
    logs::line(format!("PREFS request had_signal={}", PREFS.get().is_some()));
    if let Some(open) = PREFS.get() {
        open.set(true);
        logs::line("PREFS set true".to_owned());
    }
}

/// 关掉设置窗。
pub fn close_prefs() {
    if let Some(open) = PREFS.get() {
        open.set(false);
    }
}

/// 面板被打开过多少次（面板据此判断要不要重查列表）。
static OPENS: AtomicU64 = AtomicU64::new(0);

/// 打开计数，面板用它发现「又开了一次」。
pub fn opens() -> u64 {
    OPENS.load(Ordering::Relaxed)
}

/// 收起面板（粘贴完成后由 `panel` 调用）。
pub fn dismiss() {
    if let Some(open) = OPEN.get() {
        open.set(false);
    }
    // 面板抢了前台，收起时要还给用户原来那个应用；粘贴也走这条路
    // （`paste_after_write_back` 在 120 ms 后才发 ⌘V，切回已生效）。
    run_on_main_thread(clipboard::return_to_previous_app);
}

/// 应用自身的根组件。
#[component]
pub fn App() -> impl IntoElement {
    boot();
    let open = signal(false);
    let _ = OPEN.set(open);
    let prefs_open = signal(false);
    let _ = PREFS.set(prefs_open);

    // 托盘：图标 + 菜单（显示面板 / 偏好设置 / 退出）。只挂一次。
    effect(move || {
        if TRAY_ATTACHED.swap(true, Ordering::SeqCst) {
            return;
        }
        tray::attach(toggle_panel, open_prefs);
    });

    // 全局快捷键：切换面板。注册一次即可。
    effect(move || {
        let answer = register_global_shortcut("mod+shift+v", move || {
            logs::line("SHORTCUT mod+shift+v".to_owned());
            toggle_panel();
        });
        spawn(async move {
            let ok = answer.await;
            logs::line(format!("BOOT shortcut mod+shift+v registered ok={ok}"));
        });
    });

    // nsh 的 run_app 总会开一个主窗口；clipnest 是常驻应用，主窗口不该露出来。
    //
    // 不能 hide()：主窗口一隐藏，应用就不算 active，面板永远成不了 key window
    // （实测 `key=None active=false`，键盘全被前台应用吃掉）。改成挪到屏幕外。
    effect(move || {
        if std::env::var("SPIKE_NO_HIDE").is_ok() {
            return;
        }
        let main = current_window();
        set_timeout(Duration::from_millis(60), move || {
            // 先去掉边框与标题栏：带标题栏的窗口被系统强制留 40pt 在屏幕内。
            let mut attrs = WindowAttributes::for_kind(WindowKind::Normal);
            attrs.frame = Some(nsh_core::desktop::WindowFrameStyle::None);
            attrs.transparent = Some(true);
            attrs.title_bar = Some(Default::default());
            // 屏幕上还留着 40pt（系统不让窗口完全移出）：让它别挡鼠标。
            attrs.pointer_events = Some(nsh_core::desktop::WindowPointerEvents::None);
            main.set(attrs);
            main.set_bounds(WindowFrame {
                x: Some(px(-4000.0)),
                y: Some(px(-4000.0)),
                ..Default::default()
            });
            logs::line(format!(
                "MAIN window parked off-screen id={} main={}",
                main.id(),
                main.is_main()
            ));
        });
    });

    // 自检：SPIKE_PREFS=1 时启动 3 秒后打开设置窗（托盘菜单不好自动化）。
    if std::env::var("SPIKE_PREFS").is_ok() {
        set_timeout(Duration::from_secs(3), open_prefs);
    }

    // 自动弹出的自检：SPIKE_AUTOOPEN=1 时启动 4 秒后打开面板，用于测焦点。
    if std::env::var("SPIKE_AUTOOPEN").is_ok() {
        set_timeout(Duration::from_secs(4), move || open.set(true));
    }

    let panel_height = settings().panel_height as f32;

    view()
        .flex_1()
        .bg(move || transparent_black())
        .child(
        window()
            .id("panel")
            .kind(WindowKind::Popup)
            .default_size(size(px(settings::PANEL_WIDTH as f32), px(panel_height)))
            .attributes({
                let mut attrs = WindowAttributes::for_kind(WindowKind::Popup);
                // 激活自己：不激活的话 app 不 active，窗口永远成不了 key window，
                // 键盘全被前台应用吃掉（实测 key=None active=false）。
                // 粘贴时用 clipboard::return_to_previous_app() 切回去。
                attrs.activate = Some(std::env::var("SPIKE_NO_ACTIVATE").is_err());
                // 失焦自动收起有个副作用：重渲染导致 field editor 重挂时也会
                // 收到 blur，面板会自己关掉（实测删条目后必现）。改成只认 Esc。
                attrs.close_on_blur = Some(std::env::var("SPIKE_CLOSE_ON_BLUR").is_ok());
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
        .child(
            window()
                .id("prefs")
                .kind(WindowKind::Dialog)
                .default_size(size(px(540.0), px(340.0)))
                .open(prefs_open)
                .on_open_change(move |e| {
                    if prefs_open.get() != e.open {
                        prefs_open.set(e.open);
                    }
                    logs::line(format!("PREFS open={} reason={:?}", e.open, e.reason));
                })
                .content(|| prefs::prefs()),
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

        // 启动时按设置清理一次（上限改了以后下次启动补偿）。
        let max_items = settings().max_items;
        if let Some(db) = store::db()
            && let Ok(conn) = db.lock()
        {
            match store::prune(&conn, max_items) {
                Ok(n) if n > 0 => logs::line(format!("PRUNE startup removed={n} max={max_items}")),
                Err(e) => logs::line(format!("PRUNE startup ERROR {e}")),
                _ => {}
            }
        }
    });
}
