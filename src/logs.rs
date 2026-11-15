//! 运行时观测：日志同时进 `log`、追加 `/tmp/clipnest.log`、留最近若干条在内存。
//!
//! `/tmp/clipnest.log` 是验证脚本的唯一入口（见 `docs/2026-11-15-nsh-验证记录.md`），
//! 内存副本给面板底部那行自检用，不用另开窗口。

use std::io::Write;
use std::sync::{Mutex, OnceLock};

/// 日志文件路径（验证约定，别改）。
pub const LOG_PATH: &str = "/tmp/clipnest.log";

/// 内存里留多少条。
const KEEP: usize = 200;

fn ring() -> &'static Mutex<Vec<String>> {
    static RING: OnceLock<Mutex<Vec<String>>> = OnceLock::new();
    RING.get_or_init(|| Mutex::new(Vec::new()))
}

/// 记一行。
pub fn line(msg: impl AsRef<str>) {
    let msg = msg.as_ref();
    log::info!("{msg}");
    if let Ok(mut f) = std::fs::OpenOptions::new()
        .create(true)
        .append(true)
        .open(LOG_PATH)
    {
        let _ = writeln!(f, "{msg}");
    }
    remember(msg);
}

fn remember(msg: &str) {
    if let Ok(mut v) = ring().lock() {
        v.push(msg.to_owned());
        if v.len() > KEEP {
            let drop = v.len() - KEEP;
            v.drain(..drop);
        }
    }
}

/// 最近 `n` 条（新的在后）。
pub fn recent(n: usize) -> Vec<String> {
    match ring().lock() {
        Ok(v) => {
            let from = v.len().saturating_sub(n);
            v[from..].to_vec()
        }
        Err(_) => Vec::new(),
    }
}

/// 最后一条。
pub fn last() -> Option<String> {
    recent(1).pop()
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn ring_keeps_only_the_last_lines() {
        for i in 0..(KEEP + 5) {
            remember(&format!("第{i}行"));
        }
        let all = recent(KEEP * 2);
        assert_eq!(all.len(), KEEP);
        assert_eq!(all.last().unwrap(), &format!("第{}行", KEEP + 4));
        assert_eq!(last().unwrap(), format!("第{}行", KEEP + 4));
        assert!(recent(2)[0].starts_with("第"));
    }
}
