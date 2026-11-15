//! 同步配置：`<数据根>/sync.json`。
//!
//! 独立于 `settings.json`：开源项目里要能单独给模板、单独写文档、单独迁移版本，
//! 也不让「钥匙串条目名」这类东西混进主设置。
//!
//! 后端用 serde 的 internally-tagged 枚举（`"kind": "webdav"`）：加一种同步方式
//! 只加一个变体 + 一个 `Transport` 实现，配置文件里旧字段全部忽略，向前兼容。

use serde::{Deserialize, Serialize};

use crate::paths::Paths;

/// 当前配置格式版本。
pub const CONFIG_VERSION: u32 = 1;

/// 钥匙串里存同步口令的条目名（服务名）。
pub const SECRET_SERVICE: &str = "clipnest-sync";
/// 钥匙串里存 WebDAV 口令的条目名。
pub const WEBDAV_SECRET_SERVICE: &str = "clipnest-webdav";

/// 同步配置。
#[derive(Debug, Clone, PartialEq, Serialize, Deserialize)]
#[serde(default)]
pub struct SyncConfig {
    pub version: u32,
    /// 总开关。
    pub enabled: bool,
    /// 本机名字，出现在远端文件名里（`<device>.json`）。
    pub device_name: String,
    /// 后端。
    pub backend: Backend,
    /// 端到端加密。
    pub encryption: Encryption,
    /// 自动同步间隔（秒；0 = 只手动）。
    pub interval_secs: u64,
}

impl Default for SyncConfig {
    fn default() -> Self {
        Self {
            version: CONFIG_VERSION,
            enabled: false,
            device_name: default_device_name(),
            backend: Backend::Off,
            encryption: Encryption::default(),
            interval_secs: 1800,
        }
    }
}

/// 同步后端。加新形式就在这里加变体。
#[derive(Debug, Clone, Default, PartialEq, Serialize, Deserialize)]
#[serde(tag = "kind", rename_all = "snake_case")]
pub enum Backend {
    /// 不同步。
    #[default]
    Off,
    /// WebDAV（群晖、威联通、坚果云、Nextcloud 都是这个）。
    #[serde(rename = "webdav")]
    WebDav {
        url: String,
        username: String,
        /// 远端目录（`/clipnest`）。
        remote_dir: String,
        /// 自签证书的 NAS 常见，允许跳过校验（会提示风险）。
        insecure_tls: bool,
    },
    /// 本地目录：网盘同步盘、移动硬盘、或者一台机器上手动互拷贝。
    LocalDir { path: String },
}

impl Backend {
    /// 给 UI 用的一行摘要。
    pub fn summary(&self) -> String {
        match self {
            Backend::Off => "未启用".to_owned(),
            Backend::WebDav { url, remote_dir, .. } => {
                format!("WebDAV {url}{remote_dir}")
            }
            Backend::LocalDir { path } => format!("本地目录 {path}"),
        }
    }
}

/// 端到端加密。
#[derive(Debug, Clone, PartialEq, Serialize, Deserialize)]
#[serde(default)]
pub struct Encryption {
    pub enabled: bool,
}

impl Default for Encryption {
    fn default() -> Self {
        Self { enabled: true }
    }
}

impl SyncConfig {
    /// 规范化：版本号、设备名、空字段兜底。
    pub fn normalized(mut self) -> Self {
        self.version = CONFIG_VERSION;
        self.device_name = self.device_name.trim().to_owned();
        if self.device_name.is_empty() {
            self.device_name = default_device_name();
        }
        // 设备名要当文件名用，去掉路径分隔符与不安全字符。
        self.device_name = self
            .device_name
            .chars()
            .map(|c| {
                if c.is_ascii_alphanumeric() || c == '-' || c == '_' {
                    c
                } else {
                    '-'
                }
            })
            .collect();
        match &mut self.backend {
            Backend::WebDav {
                url, remote_dir, ..
            } => {
                *url = url.trim().trim_end_matches('/').to_owned();
                let dir = remote_dir.trim();
                *remote_dir = if dir.is_empty() {
                    "/clipnest".to_owned()
                } else if dir.starts_with('/') {
                    dir.to_owned()
                } else {
                    format!("/{dir}")
                };
            }
            Backend::LocalDir { path } => *path = path.trim().to_owned(),
            Backend::Off => {}
        }
        self
    }

    /// 配置有没有填完整（能建 backend 才会去同步）。
    pub fn ready(&self) -> Result<(), String> {
        if !self.enabled {
            return Err("同步未启用".to_owned());
        }
        match &self.backend {
            Backend::Off => Err("没有选择同步方式".to_owned()),
            Backend::WebDav { url, username, .. } => {
                if url.is_empty() {
                    return Err("还没填 WebDAV 地址".to_owned());
                }
                if username.is_empty() {
                    return Err("还没填 WebDAV 用户名".to_owned());
                }
                Ok(())
            }
            Backend::LocalDir { path } => {
                if path.is_empty() {
                    return Err("还没填本地目录".to_owned());
                }
                Ok(())
            }
        }
    }

    /// 读配置（坏了就给默认值并记日志，别让应用起不来）。
    pub fn load(paths: &Paths) -> Self {
        let path = paths.sync_config();
        match std::fs::read_to_string(&path) {
            Ok(text) => match serde_json::from_str::<SyncConfig>(&text) {
                Ok(c) => return c.normalized(),
                Err(e) => crate::logs::line(format!("SYNC 配置解析失败（用默认值）：{e}")),
            },
            Err(e) if e.kind() == std::io::ErrorKind::NotFound => {}
            Err(e) => crate::logs::line(format!("SYNC 配置读取失败（用默认值）：{e}")),
        }
        SyncConfig::default()
    }

    /// 原子写。
    pub fn save(&self, paths: &Paths) -> std::io::Result<()> {
        let text = serde_json::to_string_pretty(&self.clone().normalized())?;
        let path = paths.sync_config();
        if let Some(dir) = path.parent() {
            std::fs::create_dir_all(dir)?;
        }
        let tmp = path.with_extension("json.tmp");
        std::fs::write(&tmp, text.as_bytes())?;
        std::fs::rename(&tmp, path)
    }
}

/// 默认设备名：主机名里能当文件名的部分。
fn default_device_name() -> String {
    let host = std::env::var("COMPUTERNAME")
        .or_else(|_| std::env::var("HOSTNAME"))
        .unwrap_or_else(|_| "device".to_owned());
    let name: String = host
        .chars()
        .map(|c| {
            if c.is_ascii_alphanumeric() || c == '-' {
                c
            } else {
                '-'
            }
        })
        .collect();
    if name.is_empty() {
        "device".to_owned()
    } else {
        name
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    fn temp_paths(tag: &str) -> Paths {
        Paths::from_root(std::env::temp_dir().join(format!(
            "clipnest-syncconfig-{tag}-{}",
            crate::model::now()
        )))
    }

    #[test]
    fn defaults_are_off_and_encrypted() {
        let c = SyncConfig::default();
        assert!(!c.enabled);
        assert_eq!(c.backend, Backend::Off);
        assert!(c.encryption.enabled);
        assert_eq!(c.version, CONFIG_VERSION);
        assert!(!c.device_name.is_empty());
        assert!(c.ready().is_err(), "默认配置不能同步");
    }

    #[test]
    fn round_trips_each_backend() {
        let paths = temp_paths("roundtrip");

        let mut c = SyncConfig {
            enabled: true,
            backend: Backend::WebDav {
                url: "https://nas.local:5006/".to_owned(),
                username: "me".to_owned(),
                remote_dir: "clipnest".to_owned(),
                insecure_tls: true,
            },
            ..SyncConfig::default()
        };
        c = c.normalized();
        // 规范化：去尾斜杠、补前导斜杠
        match &c.backend {
            Backend::WebDav {
                url, remote_dir, ..
            } => {
                assert_eq!(url, "https://nas.local:5006");
                assert_eq!(remote_dir, "/clipnest");
            }
            _ => panic!("后端类型变了"),
        }
        c.save(&paths).unwrap();
        assert_eq!(SyncConfig::load(&paths), c);
        assert!(c.ready().is_ok());

        let local = SyncConfig {
            enabled: true,
            backend: Backend::LocalDir {
                path: "/Volumes/NAS/clipnest".to_owned(),
            },
            ..SyncConfig::default()
        };
        local.save(&paths).unwrap();
        assert_eq!(SyncConfig::load(&paths), local);
        let _ = std::fs::remove_dir_all(paths.root());
    }

    #[test]
    fn json_shape_is_what_the_docs_promise() {
        let c = SyncConfig {
            enabled: true,
            backend: Backend::WebDav {
                url: "https://nas.local/clipnest".to_owned(),
                username: "me".to_owned(),
                remote_dir: "/clipnest".to_owned(),
                insecure_tls: false,
            },
            ..SyncConfig::default()
        };
        let json = serde_json::to_value(&c).unwrap();
        assert_eq!(json["backend"]["kind"], "webdav", "kind 是配置里的判别字段");
        assert_eq!(json["backend"]["url"], "https://nas.local/clipnest");
        assert!(json["encryption"]["enabled"].as_bool().unwrap());
        // 口令不能出现在配置里（只在钥匙串）
        let text = serde_json::to_string(&c).unwrap();
        assert!(!text.contains("password"));
    }

    #[test]
    fn broken_config_falls_back_to_defaults() {
        let paths = temp_paths("broken");
        std::fs::create_dir_all(paths.root()).unwrap();
        std::fs::write(paths.sync_config(), b"{ not json").unwrap();
        assert_eq!(SyncConfig::load(&paths), SyncConfig::default());
        let _ = std::fs::remove_dir_all(paths.root());
    }

    #[test]
    fn unknown_fields_and_missing_fields_are_tolerated() {
        let paths = temp_paths("partial");
        std::fs::create_dir_all(paths.root()).unwrap();
        // 只有 backend：其余用默认值；多出来的字段忽略（开源项目里版本不同也要能读）
        std::fs::write(
            paths.sync_config(),
            br#"{"backend":{"kind":"local_dir","path":"/tmp/x"},"future_field":123}"#,
        )
        .unwrap();
        let c = SyncConfig::load(&paths);
        assert_eq!(c.backend, Backend::LocalDir { path: "/tmp/x".to_owned() });
        assert!(c.encryption.enabled, "缺字段用默认值");
        assert!(!c.enabled, "缺字段用默认值");
        let _ = std::fs::remove_dir_all(paths.root());
    }

    #[test]
    fn device_name_becomes_a_safe_file_name() {
        let c = SyncConfig {
            device_name: "MacBook Pro / 家里".to_owned(),
            ..SyncConfig::default()
        }
        .normalized();
        assert_eq!(c.device_name, "MacBook-Pro-----");
        assert!(!c.device_name.contains('/'), "不能带路径分隔符");
    }

    #[test]
    fn ready_explains_what_is_missing() {
        let cases = [
            (
                SyncConfig {
                    enabled: false,
                    ..SyncConfig::default()
                },
                "未启用",
            ),
            (
                SyncConfig {
                    enabled: true,
                    ..SyncConfig::default()
                },
                "没有选择同步方式",
            ),
            (
                SyncConfig {
                    enabled: true,
                    backend: Backend::WebDav {
                        url: String::new(),
                        username: "me".to_owned(),
                        remote_dir: "/c".to_owned(),
                        insecure_tls: false,
                    },
                    ..SyncConfig::default()
                },
                "地址",
            ),
            (
                SyncConfig {
                    enabled: true,
                    backend: Backend::LocalDir {
                        path: String::new(),
                    },
                    ..SyncConfig::default()
                },
                "本地目录",
            ),
        ];
        for (config, want) in cases {
            let err = config.ready().unwrap_err();
            assert!(err.contains(want), "{err} 里应包含 {want}");
        }
    }

    #[test]
    fn summary_reads_like_a_sentence() {
        assert!(Backend::Off.summary().contains("未启用"));
        assert!(
            Backend::LocalDir {
                path: "/tmp/x".to_owned()
            }
            .summary()
            .contains("/tmp/x")
        );
        assert!(
            Backend::WebDav {
                url: "https://nas".to_owned(),
                username: "u".to_owned(),
                remote_dir: "/clipnest".to_owned(),
                insecure_tls: false,
            }
            .summary()
            .contains("https://nas/clipnest")
        );
    }
}
