//! 数据模型：条目类型、哈希、入库输入与数据库行。
//!
//! 行为契约（见 `docs/路线图.md` 第四节）在这里落成代码：
//! - 契约 1：`content_hash = blake3("<kind>:<content>")`
//! - 契约 3：文件条目 `content` 为换行分隔的路径，`file_types` 为 `d`/`f` 串
//! - 契约 4：图片 `content` 是 PNG 文件名（像素尺寸另存 `width`/`height`）

use serde::{Deserialize, Serialize};

/// 条目类型，对应数据库 `items.kind`（小写字符串）。
#[derive(Debug, Clone, Copy, PartialEq, Eq, PartialOrd, Ord, Serialize, Deserialize)]
#[serde(rename_all = "lowercase")]
pub enum ItemKind {
    Text,
    Html,
    Rtf,
    Image,
    Files,
}

impl ItemKind {
    /// 落库用的字符串。
    pub fn as_str(self) -> &'static str {
        match self {
            ItemKind::Text => "text",
            ItemKind::Html => "html",
            ItemKind::Rtf => "rtf",
            ItemKind::Image => "image",
            ItemKind::Files => "files",
        }
    }

    /// 从库里的字符串还原；未知值返回 `None`。
    pub fn parse(s: &str) -> Option<Self> {
        Some(match s {
            "text" => ItemKind::Text,
            "html" => ItemKind::Html,
            "rtf" => ItemKind::Rtf,
            "image" => ItemKind::Image,
            "files" => ItemKind::Files,
            _ => return None,
        })
    }
}

/// 契约 1：`content_hash = blake3("<kind>:<content>")`，十六进制小写。
///
/// 唯一键就是它：同一内容不论入库多少次都只有一行。
pub fn content_hash(kind: ItemKind, content: &str) -> String {
    let mut hasher = blake3::Hasher::new();
    hasher.update(kind.as_str().as_bytes());
    hasher.update(b":");
    hasher.update(content.as_bytes());
    hasher.finalize().to_hex().to_string()
}

/// 当前时间戳（UTC、毫秒精度 ISO-8601），用于 `created_at` / `updated_at`。
///
/// 字符串字典序即时间序，排序可以直接交给 SQLite。
pub fn now() -> String {
    chrono::Utc::now().to_rfc3339_opts(chrono::SecondsFormat::Millis, true)
}

/// 待入库的条目。`id` 与时间戳由数据库层补。
#[derive(Debug, Clone, PartialEq)]
pub struct NewItem {
    pub kind: ItemKind,
    pub content: String,
    /// `d`/`f` 串，仅文件条目有值。
    pub file_types: Option<String>,
    /// 像素尺寸，仅图片条目有值。
    pub width: Option<i64>,
    pub height: Option<i64>,
    pub sensitive: bool,
}

impl NewItem {
    pub fn text(content: impl Into<String>) -> Self {
        Self::plain(ItemKind::Text, content)
    }

    pub fn html(content: impl Into<String>) -> Self {
        Self::plain(ItemKind::Html, content)
    }

    pub fn rtf(content: impl Into<String>) -> Self {
        Self::plain(ItemKind::Rtf, content)
    }

    fn plain(kind: ItemKind, content: impl Into<String>) -> Self {
        Self {
            kind,
            content: content.into(),
            file_types: None,
            width: None,
            height: None,
            sensitive: false,
        }
    }

    /// 契约 3：路径换行拼接，`file_types` 按同一顺序给 `d`/`f`。
    pub fn files<I, S>(paths: I, file_types: impl Into<String>) -> Self
    where
        I: IntoIterator<Item = S>,
        S: AsRef<str>,
    {
        let content = paths
            .into_iter()
            .map(|p| p.as_ref().to_owned())
            .collect::<Vec<_>>()
            .join("\n");
        Self {
            kind: ItemKind::Files,
            content,
            file_types: Some(file_types.into()),
            width: None,
            height: None,
            sensitive: false,
        }
    }

    /// 契约 4：`content` 存 PNG 文件名，尺寸单独存。
    pub fn image(file_name: impl Into<String>, width: i64, height: i64) -> Self {
        Self {
            kind: ItemKind::Image,
            content: file_name.into(),
            file_types: None,
            width: Some(width),
            height: Some(height),
            sensitive: false,
        }
    }

    pub fn sensitive(mut self, on: bool) -> Self {
        self.sensitive = on;
        self
    }

    /// 本条目在库里的唯一键。
    pub fn hash(&self) -> String {
        content_hash(self.kind, &self.content)
    }
}

/// 一条剪贴板历史（数据库行）。
#[derive(Debug, Clone, PartialEq, Serialize, Deserialize)]
pub struct Item {
    pub id: i64,
    pub content_hash: String,
    pub kind: ItemKind,
    pub content: String,
    pub file_types: Option<String>,
    pub width: Option<i64>,
    pub height: Option<i64>,
    pub use_count: i64,
    pub pinned: bool,
    pub favorite: bool,
    pub note: Option<String>,
    pub group_id: Option<i64>,
    pub sensitive: bool,
    pub created_at: String,
    pub updated_at: String,
}

impl Item {
    /// 面板里显示的一行摘要（换行压平、按字符截断，不劈开中文）。
    pub fn excerpt(&self, max_chars: usize) -> String {
        let flat = self.content.replace(['\n', '\r'], " ");
        if flat.chars().count() <= max_chars {
            return flat;
        }
        let mut out: String = flat.chars().take(max_chars).collect();
        out.push('…');
        out
    }
}

/// 面板里显示的相对时间：一分钟内「刚刚」，之后按分钟/小时/天数，超过一周给日期。
pub fn relative_time(ts: &str) -> String {
    let Ok(at) = chrono::DateTime::parse_from_rfc3339(ts) else {
        return ts.to_owned();
    };
    let at = at.with_timezone(&chrono::Utc);
    let secs = (chrono::Utc::now() - at).num_seconds();
    match secs {
        s if s < 0 => "刚刚".to_owned(),
        s if s < 60 => "刚刚".to_owned(),
        s if s < 3600 => format!("{} 分钟前", s / 60),
        s if s < 86_400 => format!("{} 小时前", s / 3600),
        s if s < 7 * 86_400 => format!("{} 天前", s / 86_400),
        _ => at
            .with_timezone(&chrono::Local)
            .format("%Y-%m-%d")
            .to_string(),
    }
}

/// 分组（`groups` 表）。
#[derive(Debug, Clone, PartialEq, Serialize, Deserialize)]
pub struct Group {
    pub id: i64,
    pub name: String,
    pub sort: i64,
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn hash_is_kind_scoped_and_stable() {
        let a = content_hash(ItemKind::Text, "hello");
        assert_eq!(a, content_hash(ItemKind::Text, "hello"));
        // 契约 1：类型进哈希，同内容不同类型不是同一条
        assert_ne!(a, content_hash(ItemKind::Html, "hello"));
        // blake3 十六进制小写、64 字符
        assert_eq!(a.len(), 64);
        assert!(a.chars().all(|c| c.is_ascii_hexdigit() && !c.is_ascii_uppercase()));
    }

    #[test]
    fn kind_round_trips() {
        for k in [
            ItemKind::Text,
            ItemKind::Html,
            ItemKind::Rtf,
            ItemKind::Image,
            ItemKind::Files,
        ] {
            assert_eq!(ItemKind::parse(k.as_str()), Some(k));
        }
        assert_eq!(ItemKind::parse("pdf"), None);
    }

    #[test]
    fn files_join_paths_with_newlines() {
        let it = NewItem::files(["/a/x.png", "/b/y.txt"], "ff");
        assert_eq!(it.content, "/a/x.png\n/b/y.txt");
        assert_eq!(it.file_types.as_deref(), Some("ff"));
        assert_eq!(it.kind, ItemKind::Files);
    }

    #[test]
    fn excerpt_flattens_and_truncates_on_char_boundary() {
        let mut item = Item {
            id: 1,
            content_hash: String::new(),
            kind: ItemKind::Text,
            content: "第一行\n第二行很长很长".to_owned(),
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
        };
        assert_eq!(item.excerpt(40), "第一行 第二行很长很长");
        item.content = "一二三四五".to_owned();
        assert_eq!(item.excerpt(3), "一二三…");
    }

    #[test]
    fn relative_time_reads_like_a_human() {
        let at = |secs: i64| {
            (chrono::Utc::now() - chrono::Duration::seconds(secs))
                .to_rfc3339_opts(chrono::SecondsFormat::Millis, true)
        };
        assert_eq!(relative_time(&at(5)), "刚刚");
        assert_eq!(relative_time(&at(90)), "1 分钟前");
        assert_eq!(relative_time(&at(3 * 3600)), "3 小时前");
        assert_eq!(relative_time(&at(3 * 86_400)), "3 天前");
        let old = relative_time(&at(40 * 86_400));
        assert_eq!(old.len(), 10, "超过一周给日期：{old}");
        assert_eq!(relative_time("不是时间"), "不是时间");
    }

    #[test]
    fn now_is_sortable_iso8601() {
        let a = now();
        let b = now();
        assert!(a <= b, "{a} <= {b}");
        assert!(a.ends_with('Z'));
        assert!(a.contains('T'));
    }
}
