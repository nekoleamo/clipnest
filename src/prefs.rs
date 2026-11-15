//! 偏好设置窗（`WindowKind::Dialog`）。
//!
//! 改一项就存一项（`settings.json`），不需要「保存」按钮；越界值当场夹紧。
//! 面板高度改完**下次启动**生效（窗口尺寸在创建时给）。

use std::time::Duration;

use nsh_base::theme;
use nsh_core::element::{View, input};
use nsh_core::prelude::*;

use crate::db::items;
use crate::logs;
use crate::settings::Settings;
use crate::store;

/// 设置窗组件。
#[component]
pub fn Prefs() -> impl IntoElement {
    let current = signal(crate::settings());
    let note = signal(String::new());

    let max_now = current;
    let note_max = note;
    let max_field = input()
        .value(move || max_now.with(|s| s.max_items.to_string()))
        .text_size(px(12.0))
        .px_2()
        .py_1()
        .rounded_md()
        .w(px(80.0))
        .bg(move || theme().muted)
        .on_change(move |text: &str| {
            let Ok(n) = text.trim().parse::<i64>() else {
                note_max.set("历史条数要填数字".to_owned());
                return;
            };
            let saved = apply(
                current,
                Settings {
                    max_items: n,
                    ..current.get()
                },
            );
            note_max.set(format!("已保存：最多保留 {} 条", saved.max_items));
        });

    let height_now = current;
    let note_h = note;
    let current_h = current;
    let height_field = input()
        .value(move || height_now.with(|s| s.panel_height.to_string()))
        .text_size(px(12.0))
        .px_2()
        .py_1()
        .rounded_md()
        .w(px(80.0))
        .bg(move || theme().muted)
        .on_change(move |text: &str| {
            let Ok(n) = text.trim().parse::<i32>() else {
                note_h.set("面板高度要填数字".to_owned());
                return;
            };
            let saved = apply(
                current_h,
                Settings {
                    panel_height: n,
                    ..current_h.get()
                },
            );
            note_h.set(format!(
                "已保存：面板高度 {}（下次启动生效）",
                saved.panel_height
            ));
        });

    let note_clean = note;
    let clean = button("立即清理").on_press(move |_: &()| {
        let s = crate::settings();
        let Some(db) = store::db() else {
            note_clean.set("数据库打不开".to_owned());
            return;
        };
        let Ok(conn) = db.lock() else {
            note_clean.set("数据库被占用，稍后再试".to_owned());
            return;
        };
        let pruned = match store::prune(&conn, s.max_items) {
            Ok(n) => n,
            Err(e) => {
                note_clean.set(format!("清理失败：{e}"));
                return;
            }
        };
        let orphans = store::collect_garbage(&conn).unwrap_or(0);
        if pruned == 0 && orphans == 0 {
            note_clean.set("没有需要清理的条目或图片".to_owned());
            return;
        }
        logs::line(format!("PRUNE manual removed={pruned} images={orphans}"));
        note_clean.set(format!(
            "已清理 {pruned} 条（收藏与置顶保留）、{orphans} 个孤儿图片文件"
        ));
    });

    let note_reveal = note;
    let reveal = button("打开数据目录").on_press(move |_: &()| {
        // 结果不重要：系统要么打开了，要么没反应（没有需要处理的失败分支）。
        drop(reveal_path(crate::paths().root()));
        note_reveal.set("已在访达中打开".to_owned());
    });

    // 敏感应用过滤：点一下开关（名单本身在 settings.json 里改）。
    let sens_now = current;
    let note_sens = note;
    let sens_toggle = view()
        .px_3()
        .py_1()
        .rounded_md()
        .bg(move || theme().secondary)
        .on_press(move |_: &()| {
            let on = !sens_now.with(|s| s.ignore_sensitive_apps);
            let saved = apply(
                sens_now,
                Settings {
                    ignore_sensitive_apps: on,
                    ..sens_now.get()
                },
            );
            note_sens.set(if saved.ignore_sensitive_apps {
                "已开启：密码管理器里复制的不会进历史".to_owned()
            } else {
                "已关闭：任何应用的内容都会进历史".to_owned()
            });
        })
        .child(
            text(move || {
                if sens_now.with(|s| s.ignore_sensitive_apps) {
                    "已拦截（点我关掉）"
                } else {
                    "已关闭（点我开启）"
                }
            })
            .text_size(px(11.0))
            .text_color(move || theme().secondary_foreground),
        );

    // 开机自启：写/删 LaunchAgent（macOS）或注册表 Run 键（Windows）。
    let auto_on = signal(crate::autostart::is_enabled());
    let note_auto = note;
    let auto_toggle = view()
        .px_3()
        .py_1()
        .rounded_md()
        .bg(move || theme().secondary)
        .on_press(move |_: &()| {
            let on = !auto_on.get();
            match crate::autostart::set_enabled(on) {
                Ok(()) => {
                    auto_on.set(crate::autostart::is_enabled());
                    note_auto.set(if on {
                        "已设置开机自启（下次登录生效）".to_owned()
                    } else {
                        "已取消开机自启".to_owned()
                    });
                }
                Err(e) => note_auto.set(format!("设置失败：{e}")),
            }
        })
        .child(
            text(move || {
                if auto_on.get() {
                    "已开启（点我关掉）"
                } else {
                    "已关闭（点我开启）"
                }
            })
            .text_size(px(11.0))
            .text_color(move || theme().secondary_foreground),
        );

    let sens_count = crate::sensitive::list(&crate::settings()).len();
    // 检查更新 / 安装（自更新：应用自己下载的文件不带 Gatekeeper 标记）
    let upd_note = signal(crate::update::status());
    effect(move || {
        set_interval(Duration::from_millis(500), move || {
            let text = crate::update::status();
            if text != upd_note.get() {
                upd_note.set(text);
            }
        });
    });
    let upd_status = upd_note;
    let check_btn = button("检查更新").on_press(move |_: &()| {
        crate::update::check_async();
    });
    let install_btn = button("安装更新").on_press(|_: &()| {
        crate::update::install_async();
    });

    let dir = crate::paths().root().display().to_string();

    view()
        .flex()
        .flex_col()
        .gap_3()
        .p_4()
        .bg(move || theme().background)
        .child(
            text("偏好设置")
                .text_size(px(15.0))
                .text_color(move || theme().foreground),
        )
        .child(
            row()
                .child(label("历史保留条数"))
                .child(max_field)
                .child(hint("0 = 不限；收藏与置顶不会被清掉")),
        )
        .child(
            row()
                .child(label("面板高度"))
                .child(height_field)
                .child(hint("点，下次启动生效")),
        )
        .child(
            row()
                .child(label("数据目录"))
                .child(
                    text(dir)
                        .text_size(px(10.0))
                        .text_color(move || theme().muted_foreground),
                )
                .child(reveal),
        )
        .child(
            row()
                .child(label("敏感应用"))
                .child(sens_toggle)
                .child(hint_dyn(format!(
                    "{} 个内置应用（密码管理器/钥匙串）；名单可在 settings.json 的 sensitive_apps 里改",
                    sens_count
                ))),
        )
        .child(
            row()
                .child(label("开机自启"))
                .child(auto_toggle)
                .child(hint("登录后自动启动，不弹窗")),
        )
        .child(
            row()
                .child(label("唤出快捷键"))
                .child(
                    text(crate::keys::HOTKEY)
                        .text_size(px(12.0))
                        .text_color(move || theme().foreground),
                )
                .child(hint("Esc 收起；↑↓ 选择；⏎ 粘贴")),
        )
        .child(
            row()
                .child(label("更新"))
                .child(check_btn)
                .child(install_btn)
                .child(
                    text(move || upd_status.get())
                        .text_size(px(10.0))
                        .text_color(move || theme().muted_foreground),
                ),
        )
        .child(sync_section())
        .child(
            row()
                .child(clean)
                .child(
                    text(move || note.get())
                        .text_size(px(10.0))
                        .text_color(move || theme().muted_foreground),
                ),
        )
}

/// 存设置、刷新界面状态，并按新上限清理一次。
fn apply(current: Signal<Settings>, next: Settings) -> Settings {
    let s = next.normalized();
    if !crate::save_settings(s.clone()) {
        logs::line("SETTINGS 保存失败");
        return crate::settings();
    }
    current.set(s.clone());
    if let Some(db) = store::db()
        && let Ok(conn) = db.lock()
    {
        match store::prune(&conn, s.max_items) {
            Ok(n) if n > 0 => logs::line(format!("PRUNE after settings removed={n}")),
            Err(e) => logs::line(format!("PRUNE ERROR {e}")),
            _ => {}
        }
        if let Ok(total) = items::count(&conn) {
            logs::line(format!("SETTINGS max_items={} items={total}", s.max_items));
        }
    }
    s
}

fn row() -> View {
    view().flex().items_center().gap_3()
}

fn label(t: &'static str) -> View {
    view().w(px(110.0)).child(
        text(t)
            .text_size(px(11.0))
            .text_color(move || theme().muted_foreground),
    )
}

fn hint(t: &'static str) -> View {
    view().child(
        text(t)
            .text_size(px(9.0))
            .text_color(move || theme().muted_foreground),
    )
}

fn hint_dyn(t: String) -> View {
    view().child(
        text(t)
            .text_size(px(9.0))
            .text_color(move || theme().muted_foreground),
    )
}

/// 最简按钮（P2 不引 nsh-base 的按钮组件，样式自给）。
fn button(label: &'static str) -> View {
    view()
        .px_3()
        .py_1()
        .rounded_md()
        .bg(move || theme().secondary)
        .child(
            text(label)
                .text_size(px(11.0))
                .text_color(move || theme().secondary_foreground),
        )
}

// ───────────────────────── 同步 ─────────────────────────

/// 同步区块：配置存 `sync.json`，口令存系统钥匙串。
fn sync_section() -> View {
    use crate::sync::config::{Backend, SECRET_SERVICE, WEBDAV_SECRET_SERVICE};

    let cfg = signal(crate::sync_config());
    let note = signal(crate::sync_status());

    // 状态每 500 ms 刷一次（同步在后台线程跑）。
    let note_poll = note;
    effect(move || {
        set_interval(Duration::from_millis(500), move || {
            let text = crate::sync_status();
            if text != note_poll.get() {
                note_poll.set(text);
            }
        });
    });

    // 启用开关
    let cfg_on = cfg;
    let on_toggle = view()
        .px_3()
        .py_1()
        .rounded_md()
        .bg(move || theme().secondary)
        .on_press(move |_: &()| {
            let enabled = !cfg_on.with(|c| c.enabled);
            let next = crate::sync::config::SyncConfig {
                enabled,
                ..cfg_on.get()
            };
            let n = next.normalized();
            if crate::save_sync_config(n.clone()) {
                cfg_on.set(n);
            }
        })
        .child(
            text(move || {
                if cfg_on.with(|c| c.enabled) {
                    "已启用（点我关掉）"
                } else {
                    "未启用（点我开启）"
                }
            })
            .text_size(px(11.0))
            .text_color(move || theme().secondary_foreground),
        );

    // 方式切换：本地目录 / WebDAV
    let pick = move |kind: &'static str, cfg: Signal<crate::sync::config::SyncConfig>| {
        view()
            .px_2()
            .py_1()
            .rounded_md()
            .bg(move || {
                let now = cfg.with(|c| {
                    matches!(
                        (&c.backend, kind),
                        (Backend::LocalDir { .. }, "local") | (Backend::WebDav { .. }, "webdav")
                    )
                });
                if now { theme().accent } else { theme().muted }
            })
            .on_press(move |_: &()| {
                let current = cfg.get();
                let backend = match kind {
                    "local" => Backend::LocalDir {
                        path: match &current.backend {
                            Backend::LocalDir { path } => path.clone(),
                            _ => String::new(),
                        },
                    },
                    _ => match &current.backend {
                        Backend::WebDav {
                            url,
                            username,
                            remote_dir,
                            insecure_tls,
                        } => Backend::WebDav {
                            url: url.clone(),
                            username: username.clone(),
                            remote_dir: remote_dir.clone(),
                            insecure_tls: *insecure_tls,
                        },
                        _ => Backend::WebDav {
                            url: String::new(),
                            username: String::new(),
                            remote_dir: "/clipnest".to_owned(),
                            insecure_tls: false,
                        },
                    },
                };
                let next = crate::sync::config::SyncConfig {
                    backend,
                    ..current
                }
                .normalized();
                if crate::save_sync_config(next.clone()) {
                    cfg.set(next);
                }
            })
            .child(
                text(if kind == "local" { "本地目录" } else { "WebDAV" })
                    .text_size(px(11.0))
                    .text_color(move || theme().foreground),
            )
    };

    // 字段：受控输入 + 失焦/回车即存
    let field = move |value: String, cfg: Signal<crate::sync::config::SyncConfig>, which: &'static str| {
        input()
            .value(value)
            .text_size(px(11.0))
            .px_2()
            .py_1()
            .rounded_md()
            .bg(move || theme().muted)
            .on_change(move |text: &str| {
                let mut next = cfg.get();
                match (&mut next.backend, which) {
                    (Backend::LocalDir { path }, "path") => *path = text.to_owned(),
                    (Backend::WebDav { url, .. }, "url") => *url = text.to_owned(),
                    (Backend::WebDav { username, .. }, "user") => *username = text.to_owned(),
                    (Backend::WebDav { remote_dir, .. }, "dir") => *remote_dir = text.to_owned(),
                    _ => return,
                }
                let normalized = next.normalized();
                if crate::save_sync_config(normalized.clone()) {
                    cfg.set(normalized);
                }
            })
    };

    let backend_row = cfg.with(|c| match &c.backend {
        Backend::LocalDir { path } => {
            row()
                .child(label("目录"))
                .child(field(path.clone(), cfg, "path"))
                .child(hint("网盘同步盘或移动硬盘里的目录"))
        }
        Backend::WebDav {
            url,
            username,
            remote_dir,
            ..
        } => row()
            .child(label("地址"))
            .child(field(url.clone(), cfg, "url"))
            .child(label("用户"))
            .child(field(username.clone(), cfg, "user"))
            .child(label("目录"))
            .child(field(remote_dir.clone(), cfg, "dir")),
        Backend::Off => row().child(hint("先选一种同步方式")),
    });

    // WebDAV 密码：输入后写钥匙串，不落配置文件
    let note_pw = note;
    let user_now = cfg.with(|c| match &c.backend {
        Backend::WebDav { username, .. } => username.clone(),
        _ => String::new(),
    });
    let pw_field = input()
        .value(String::new())
        .text_size(px(11.0))
        .px_2()
        .py_1()
        .rounded_md()
        .bg(move || theme().muted)
        .on_submit(move |text: &str| {
            if text.is_empty() || user_now.is_empty() {
                note_pw.set("先填用户名，再填密码".to_owned());
                return;
            }
            match crate::sync::secret::set(WEBDAV_SECRET_SERVICE, &user_now, text) {
                Ok(()) => note_pw.set("WebDAV 密码已存进钥匙串".to_owned()),
                Err(e) => note_pw.set(format!("存密码失败：{e}")),
            }
        });
    let pw_row = row()
        .child(label("密码"))
        .child(pw_field)
        .child(hint("输完按回车存进钥匙串（不写进配置文件）"));

    // 同步口令（端到端加密）
    let device_now = cfg.with(|c| c.device_name.clone());
    let note_key = note;
    let device_for_field = device_now.clone();
    let key_field = input()
        .value(String::new())
        .text_size(px(11.0))
        .px_2()
        .py_1()
        .rounded_md()
        .bg(move || theme().muted)
        .on_submit(move |text: &str| {
            if text.is_empty() {
                note_key.set("口令不能为空".to_owned());
                return;
            }
            let device = device_for_field.clone();
            match crate::sync::secret::set(SECRET_SERVICE, &device, text) {
                Ok(()) => note_key.set("同步口令已存进钥匙串".to_owned()),
                Err(e) => note_key.set(format!("存口令失败：{e}")),
            }
        });
    let key_exists = crate::sync::secret::exists(SECRET_SERVICE, &device_now);
    let key_row = row()
        .child(label("同步口令"))
        .child(key_field)
        .child(hint(if key_exists {
            "已保存（改就重输，回车覆盖）"
        } else {
            "还没设置：输完回车存钥匙串"
        }));

    let note_run = note;
    let run_btn = button("立即同步").on_press(move |_: &()| {
        note_run.set("同步中…".to_owned());
        crate::start_sync();
    });

    view()
        .flex()
        .flex_col()
        .gap_2()
        .pt_3()
        .child(
            view().flex().items_center().gap_2().child(
                text("同步")
                    .text_size(px(13.0))
                    .text_color(move || theme().foreground),
            ),
        )
        .child(row().child(label("开关")).child(on_toggle))
        .child(
            row()
                .child(label("方式"))
                .child(pick("local", cfg))
                .child(pick("webdav", cfg))
                .child(hint_dyn(cfg.with(|c| c.backend.summary()))),
        )
        .child(backend_row)
        .child(pw_row)
        .child(key_row)
        .child(
            row()
                .child(run_btn)
                .child(
                    text(move || note.get())
                        .text_size(px(10.0))
                        .text_color(move || theme().muted_foreground),
                ),
        )
        .child(hint_dyn(
            "配置在数据目录的 sync.json（可单独备份/分享模板）；密码与同步口令在系统钥匙串里".to_owned(),
        ))
}
