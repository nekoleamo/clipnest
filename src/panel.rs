//! 剪贴板面板：搜索框 + 历史列表 + 粘贴动作。
//!
//! 数据来自 `db::search::query`（列表与搜索的唯一入口），选中项动作走 `clipboard`。
//! 键盘：↑/↓ 选择，Enter 粘贴（输入框的 submit），Esc 由窗口自己收起（P0 已验证）。

use std::time::Duration;

use nsh_base::theme;
use nsh_core::element::{input, scroll};
use nsh_core::prelude::*;

use crate::db;
use crate::db::search::{Filter, Page, query};
use crate::logs;
use crate::model::{Item, ItemKind, relative_time};
use crate::store;

/// 面板一次列多少条（P2 用普通滚动，不分页加载）。
pub const PAGE: i64 = 200;

/// 列表里的一行（渲染用的预计算结果）。
#[derive(Debug, Clone, PartialEq)]
pub struct Row {
    pub id: i64,
    pub kind: ItemKind,
    /// 类型标签，如「文本」「文件」。
    pub tag: String,
    /// 单行摘要。
    pub summary: String,
    /// 相对时间。
    pub time: String,
    pub pinned: bool,
    pub favorite: bool,
}

impl Row {
    fn of(item: &Item) -> Self {
        Self {
            id: item.id,
            kind: item.kind,
            tag: tag_of(item.kind).to_owned(),
            summary: summary_of(item),
            time: relative_time(&item.updated_at),
            pinned: item.pinned,
            favorite: item.favorite,
        }
    }
}

/// 类型标签（中文两字，省空间）。
pub fn tag_of(kind: ItemKind) -> &'static str {
    match kind {
        ItemKind::Text => "文本",
        ItemKind::Html => "HTML",
        ItemKind::Rtf => "RTF",
        ItemKind::Image => "图片",
        ItemKind::Files => "文件",
    }
}

/// 列表摘要：文件给首个文件名 + 数量，图片给尺寸，其余压平换行截断。
pub fn summary_of(item: &Item) -> String {
    match item.kind {
        ItemKind::Files => {
            let paths: Vec<&str> = item.content.lines().filter(|l| !l.is_empty()).collect();
            let first = paths
                .first()
                .map(|p| p.rsplit('/').next().unwrap_or(p))
                .unwrap_or("")
                .to_owned();
            match paths.len() {
                0 => "（空文件列表）".to_owned(),
                1 => first,
                n => format!("{first} 等 {n} 项"),
            }
        }
        ItemKind::Image => match (item.width, item.height) {
            (Some(w), Some(h)) => format!("{w}×{h}"),
            _ => "图片".to_owned(),
        },
        _ => item.excerpt(120),
    }
}

/// 查一次库，返回渲染用的行。
pub fn rows_for(q: &str) -> Vec<Row> {
    let Some(db) = store::db() else {
        return Vec::new();
    };
    let Ok(conn) = db.lock() else {
        return Vec::new();
    };
    match query(&conn, q, &Filter::default(), Page::new(PAGE, 0)) {
        Ok(items) => items.iter().map(Row::of).collect(),
        Err(e) => {
            logs::line(format!("QUERY ERROR {e}"));
            Vec::new()
        }
    }
}

/// 粘贴一行：写回剪贴板 → 计一次使用 → 收起面板 → 发 ⌘V。
pub fn paste(row: &Row) -> bool {
    let Some(db) = store::db() else {
        return false;
    };
    let Ok(conn) = db.lock() else {
        return false;
    };
    let item = match db::items::get(&conn, row.id) {
        Ok(Some(item)) => item,
        Ok(None) => {
            logs::line(format!("PASTE GONE id={}", row.id));
            return false;
        }
        Err(e) => {
            logs::line(format!("PASTE ERROR id={} {e}", row.id));
            return false;
        }
    };
    match crate::clipboard::write_back(&item) {
        Ok(cc) => {
            // 使用次数 +1；写回产生的 changeCount 已被登记，不会新增记录（契约 10）
            if let Err(e) = db::items::touch(&conn, item.id) {
                logs::line(format!("TOUCH ERROR id={} {e}", item.id));
            }
            logs::line(format!(
                "PASTE id={} kind={} cc={cc}",
                item.id,
                item.kind.as_str()
            ));
            drop(conn);
            crate::dismiss();
            crate::clipboard::paste_after_write_back();
            true
        }
        Err(e) => {
            logs::line(format!("PASTE FAILED id={} {e}", item.id));
            false
        }
    }
}

/// 面板组件（放在 Popup 窗口的 content 里）。
#[component]
pub fn Panel() -> impl IntoElement {
    let q = signal(String::new());
    let rows = signal(Vec::<Row>::new());
    let sel = signal(0usize);
    let list: nsh_core::element::ElementRef = element_ref();

    // 首次显示时从库里取一次（之后由搜索/粘贴触发）。
    let loaded = signal(false);
    if !loaded.get() {
        loaded.set(true);
        rows.set(rows_for(""));
        logs::line(format!("PANEL rows={}", rows.with(Vec::len)));
    }

    // 面板可能只是隐藏后重开：轮询「打开次数 / 库条数」，变了才重查。
    // 初值故意用一个不可能的值：第一次 tick 一定走「刚打开」分支，
    // 保证首次显示时搜索框被聚焦、列表是最新的。
    let known_open = signal(u64::MAX);
    let known_count = signal(store::count().unwrap_or(-1));
    let q_poll = q.clone();
    let rows_poll = rows.clone();
    let sel_poll = sel.clone();
    effect(move || {
        let known_open = known_open.clone();
        let known_count = known_count.clone();
        let q_poll = q_poll.clone();
        let rows_poll = rows_poll.clone();
        let sel_poll = sel_poll.clone();
        set_interval(Duration::from_millis(150), move || {
            let opens = crate::opens();
            let count = store::count().unwrap_or(-1);
            if opens != known_open.get() {
                known_open.set(opens);
                // 每次打开都从空查询开始，否则会带着上次的搜索结果。
                q_poll.set(String::new());
                rows_poll.set(rows_for(""));
                sel_poll.set(0);
                known_count.set(store::count().unwrap_or(-1));
            }
            if count == known_count.get() && rows_poll.with(|r| !r.is_empty()) {
                return;
            }
            known_count.set(count);
            let q = untracked(|| q_poll.get());
            rows_poll.set(rows_for(&q));
            let n = rows_poll.with(Vec::len);
            if sel_poll.get() >= n {
                sel_poll.set(n.saturating_sub(1));
            }
        });
    });

    // 上下键只绑一次：handler 内部读最新的 signal。
    // 方向键必须 `When::Always`：焦点在搜索框里，单行 input 会自己吃掉方向键。
    let arrows = KeyOptions {
        when: nsh_core::keymap::When::Always,
        repeat: true,
        pass_through: false,
    };
    let list_keys = list.clone();
    let rows_key = rows.clone();
    let sel_key = sel.clone();
    effect(move || {
        let rows_dn = rows_key.clone();
        let sel_dn = sel_key.clone();
        let list_dn = list_keys.clone();
        on_key_with("down", arrows, move |_| {
            let n = rows_dn.with(Vec::len);
            if n == 0 {
                return;
            }
            let next = (sel_dn.get() + 1).min(n - 1);
            sel_dn.set(next);
            list_dn.scroll_to_index(next as u32, percentage(50.0), px(0.0), false);
        });
        // 焦点在搜索框里时 Esc 会被 input 吃掉，窗口自己收不起来，自己绑。
        on_key_with("escape", arrows, |_| {
            logs::line("ESC dismiss".to_owned());
            crate::dismiss();
        });
        let rows_up = rows_key.clone();
        let sel_up = sel_key.clone();
        let list_up = list_keys.clone();
        on_key_with("up", arrows, move |_| {
            if rows_up.with(Vec::len) == 0 {
                return;
            }
            let prev = sel_up.get().saturating_sub(1);
            sel_up.set(prev);
            list_up.scroll_to_index(prev as u32, percentage(50.0), px(0.0), false);
        });
    });

    let q_change = q.clone();
    let rows_change = rows.clone();
    let sel_change = sel.clone();
    let field = input()
        .auto_focus(true)
        .value(move || q.get())
        .placeholder("搜索剪贴板历史…")
        .text_size(px(13.0))
        .px_3()
        .py_2()
        .rounded_md()
        .bg(move || theme().muted)
        .on_change(move |text: &str| {
            q_change.set(text.to_owned());
            rows_change.set(rows_for(text));
            sel_change.set(0);
            logs::line(format!(
                "SEARCH q={text:?} rows={}",
                rows_change.with(Vec::len)
            ));
        })
        .on_focus(move |_: &()| logs::line("FIELD focused".to_owned()))
        .on_blur(move |_: &()| logs::line("FIELD blurred".to_owned()))
        .on_submit(move |_: &str| {
            let Some(row) = rows_change.with(|r| r.get(sel.get()).cloned()) else {
                return;
            };
            paste(&row);
        });

    let rows_view = rows.clone();
    let sel_view = sel.clone();


    view()
        .flex()
        .flex_col()
        .flex_1()
        .gap_2()
        .p_2()
        .bg(move || theme().background)
        .child(field)
        .child(
            scroll()
                .element_ref(&list)
                .flex_1()
                .child(
                    // dynamic：rows 一变就重建列表。用静态 children 的话列表停在首帧，
                    // 搜索/新增都不会反映（底部那行是响应式的，所以会骗人）。
                    dynamic(move || {
                        let items = rows_view.get();
                        view()
                            .flex()
                            .flex_col()
                            .gap_1()
                            .children(
                                items
                                    .iter()
                                    .enumerate()
                                    .map(|(i, row)| row_view(row, i, sel_view.clone())),
                            )
                            .child(empty_hint(rows_view.clone()))
                    }),
                ),
        )
        .child(
            text(move || {
                let n = rows.with(Vec::len);
                if n == 0 {
                    "↑↓ 选择 · ⏎ 粘贴".to_owned()
                } else {
                    format!("{}/{} · ↑↓ 选择 · ⏎ 粘贴", sel.get().min(n - 1) + 1, n)
                }
            })
            .text_size(px(10.0))
            .text_color(move || theme().muted_foreground),
        )
}

/// 一行：标签 + 摘要 + 时间，选中态高亮。
fn row_view(row: &Row, index: usize, sel: Signal<usize>) -> impl IntoElement {
    let id = row.id;
    let tag = row.tag.clone();
    let summary = row.summary.clone();
    let time = row.time.clone();
    let pinned = row.pinned;
    let favorite = row.favorite;
    let sel_press = sel.clone();
    let sel_bg = sel.clone();
    view()
        .flex()
        .items_center()
        .gap_2()
        .px_2()
        .py_1()
        .rounded_md()
        .bg(move || {
            if sel_bg.get() == index {
                theme().accent
            } else {
                theme().background
            }
        })
        .on_press(move |_: &()| sel_press.set(index))
        .child(
            text(format!("{tag}"))
                .text_size(px(10.0))
                .text_color(move || theme().muted_foreground),
        )
        .child(
            view()
                .flex()
                .flex_col()
                .flex_1()
                .child(
                    text(format!("{}{summary}", if pinned { "📌 " } else { "" }))
                        .text_size(px(12.0))
                        .truncate()
                        .text_color(move || theme().foreground),
                )
                .child(
                    text(format!("{}{time}", if favorite { "★ " } else { "" }))
                        .text_size(px(9.0))
                        .text_color(move || theme().muted_foreground),
                ),
        )
        .child(view().opacity(0.0).child(text(format!("{id}"))))
}

/// 列表为空时的一句提示。
fn empty_hint(rows: Signal<Vec<Row>>) -> impl IntoElement {
    view().p_4().child(
        text(move || {
            if rows.with(Vec::is_empty) {
                "还没有历史；复制点东西再试"
            } else {
                ""
            }
        })
        .text_size(px(11.0))
        .text_color(move || theme().muted_foreground),
    )
}

#[cfg(test)]
mod tests {
    use super::*;

    fn item(kind: ItemKind, content: &str) -> Item {
        Item {
            id: 7,
            content_hash: "h".to_owned(),
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
            created_at: crate::model::now(),
            updated_at: crate::model::now(),
        }
    }

    #[test]
    fn tags_cover_every_kind() {
        for k in [
            ItemKind::Text,
            ItemKind::Html,
            ItemKind::Rtf,
            ItemKind::Image,
            ItemKind::Files,
        ] {
            assert!(!tag_of(k).is_empty());
        }
    }

    #[test]
    fn files_summary_counts_items() {
        assert_eq!(
            summary_of(&item(ItemKind::Files, "/tmp/a.png\n/tmp/b")),
            "a.png 等 2 项"
        );
        assert_eq!(summary_of(&item(ItemKind::Files, "/tmp/solo.txt")), "solo.txt");
        assert_eq!(summary_of(&item(ItemKind::Files, "")), "（空文件列表）");
    }

    #[test]
    fn image_summary_shows_size_and_text_is_flattened() {
        let mut img = item(ItemKind::Image, "ab.png");
        img.width = Some(1920);
        img.height = Some(1080);
        assert_eq!(summary_of(&img), "1920×1080");
        assert_eq!(summary_of(&item(ItemKind::Image, "ab.png")), "图片");
        assert_eq!(summary_of(&item(ItemKind::Text, "第一行\n第二行")), "第一行 第二行");
    }

    #[test]
    fn row_keeps_identity_and_time() {
        let mut it = item(ItemKind::Text, "内容");
        it.pinned = true;
        let row = Row::of(&it);
        assert_eq!(row.id, 7);
        assert_eq!(row.tag, "文本");
        assert!(row.pinned);
        assert!(!row.time.is_empty());
    }
}
