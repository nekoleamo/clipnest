//! 偏好设置窗（`WindowKind::Dialog`）。
//!
//! 改一项就存一项（`settings.json`），不需要「保存」按钮；越界值当场夹紧。
//! 面板高度改完**下次启动**生效（窗口尺寸在创建时给）。

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
        match store::prune(&conn, s.max_items) {
            Ok(0) => note_clean.set("没有需要清理的条目".to_owned()),
            Ok(n) => {
                logs::line(format!("PRUNE manual removed={n}"));
                note_clean.set(format!("已清理 {n} 条（收藏与置顶保留）"));
            }
            Err(e) => note_clean.set(format!("清理失败：{e}")),
        }
    });

    let note_reveal = note;
    let reveal = button("打开数据目录").on_press(move |_: &()| {
        // 结果不重要：系统要么打开了，要么没反应（没有需要处理的失败分支）。
        drop(reveal_path(crate::paths().root()));
        note_reveal.set("已在访达中打开".to_owned());
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
                .child(label("唤出快捷键"))
                .child(
                    text("⌘⇧V")
                        .text_size(px(12.0))
                        .text_color(move || theme().foreground),
                )
                .child(hint("Esc 收起；↑↓ 选择；⏎ 粘贴")),
        )
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
