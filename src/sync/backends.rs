//! 同步后端：把 `sync.json` 变成 [`Transport`]。
//!
//! 首期两种：WebDAV 与本地目录。加 S3/SFTP 时在这里加一个分支 + 一个 `Transport` 实现，
//! 配置那边只需在 [`Backend`] 里加一个变体。

use crate::sync::config::{Backend, SyncConfig, WEBDAV_SECRET_SERVICE};
use crate::sync::{LocalTransport, Transport};

/// 按配置建后端；配置不完整或不会实现的形式返回人能看懂的错。
pub fn open(config: &SyncConfig) -> Result<Box<dyn Transport>, String> {
    match &config.backend {
        Backend::Off => Err("没有选择同步方式".to_owned()),
        Backend::LocalDir { path } => {
            if path.trim().is_empty() {
                return Err("还没填本地目录".to_owned());
            }
            Ok(Box::new(LocalTransport::new(path.trim())))
        }
        Backend::WebDav {
            url,
            username,
            remote_dir,
            insecure_tls,
        } => {
            let base = format!("{}{}/", url.trim_end_matches('/'), remote_dir);
            // 自签证书的 NAS 很常见，但 ureq 的 rustls 不做「跳过校验」的简单开关；
            // 明说清楚比默默失败好。
            if *insecure_tls {
                crate::logs::line(
                    "SYNC insecure_tls 目前不生效：请给 NAS 装有效证书，或改用 local_dir（挂载网络盘）",
                );
            }
            let password = crate::sync::secret::get(WEBDAV_SECRET_SERVICE, username)
                .ok_or_else(|| "钥匙串里没有这个 WebDAV 账号的密码".to_owned())?;
            Ok(Box::new(WebDavTransport::new(&base, username, &password)?))
        }
    }
}

/// WebDAV：一台设备一个文件，PROPFIND 列目录。
pub struct WebDavTransport {
    agent: ureq::Agent,
    base: String,
    /// Basic 认证头（`Basic base64(user:pass)`）。
    authorization: String,
}

impl WebDavTransport {
    pub fn new(base: &str, username: &str, password: &str) -> Result<Self, String> {
        if !base.starts_with("http://") && !base.starts_with("https://") {
            return Err("WebDAV 地址要以 http:// 或 https:// 开头".to_owned());
        }
        Ok(Self {
            agent: ureq::Agent::new_with_defaults(),
            base: base.to_owned(),
            authorization: format!(
                "Basic {}",
                base64_encode(format!("{username}:{password}").as_bytes())
            ),
        })
    }

    fn url(&self, name: &str) -> String {
        format!("{}{}", self.base, name)
    }

    /// 发一个任意方法的请求（PROPFIND 不在 ureq 的快捷方法里）。
    fn request(
        &self,
        method: &str,
        url: &str,
        extra: &[(&str, &str)],
        body: Vec<u8>,
    ) -> Result<(u16, Vec<u8>), String> {
        let mut builder = ureq::http::Request::builder()
            .method(method)
            .uri(url)
            .header("Authorization", &self.authorization);
        for (k, v) in extra {
            builder = builder.header(*k, *v);
        }
        let request = builder
            .body(body)
            .map_err(|e| format!("构造请求失败：{e}"))?;
        let mut response = self
            .agent
            .run(request)
            .map_err(|e| format!("请求 {method} {url} 失败：{e}"))?;
        let status = response.status().as_u16();
        let bytes = response
            .body_mut()
            .read_to_vec()
            .map_err(|e| format!("读响应失败：{e}"))?;
        Ok((status, bytes))
    }
}

impl Transport for WebDavTransport {
    fn list(&self) -> Result<Vec<String>, String> {
        // Depth: 1 —— 只要这一层。
        let body = br#"<?xml version="1.0"?><d:propfind xmlns:d="DAV:"><d:prop><d:resourcetype/></d:prop></d:propfind>"#.to_vec();
        let (status, bytes) = self.request(
            "PROPFIND",
            &self.base,
            &[("Depth", "1"), ("Content-Type", "application/xml")],
            body,
        )?;
        // 目录不存在时先建（有的 NAS 不会自动建）。
        if status == 404 {
            let (put_status, _) =
                self.request("MKCOL", self.base.trim_end_matches('/'), &[], Vec::new())?;
            if put_status >= 400 {
                return Err(format!("远端目录不存在，创建也失败（{put_status}）"));
            }
            return Ok(Vec::new());
        }
        if status >= 400 {
            return Err(format!("列目录失败（HTTP {status}）"));
        }
        let text = String::from_utf8_lossy(&bytes);
        let mut out = Vec::new();
        for href in hrefs(&text) {
            // 只认本层的 .json 文件
            let name = href.trim_end_matches('/').rsplit('/').next().unwrap_or("");
            if name.ends_with(".json") || name.ends_with(".cnsl") {
                out.push(name.to_owned());
            }
        }
        out.sort();
        out.dedup();
        Ok(out)
    }

    fn get(&self, name: &str) -> Result<Vec<u8>, String> {
        let (status, bytes) = self.request("GET", &self.url(name), &[], Vec::new())?;
        if status >= 400 {
            return Err(format!("下载 {name} 失败（HTTP {status}）"));
        }
        Ok(bytes)
    }

    fn put(&self, name: &str, bytes: &[u8]) -> Result<(), String> {
        let (status, _) = self.request(
            "PUT",
            &self.url(name),
            &[("Content-Type", "application/octet-stream")],
            bytes.to_vec(),
        )?;
        if status >= 400 {
            return Err(format!("上传 {name} 失败（HTTP {status}）"));
        }
        Ok(())
    }

    fn remove(&self, name: &str) -> Result<(), String> {
        let (status, _) = self.request("DELETE", &self.url(name), &[], Vec::new())?;
        if status >= 400 && status != 404 {
            return Err(format!("删除 {name} 失败（HTTP {status}）"));
        }
        Ok(())
    }
}

/// 从 PROPFIND 的 XML 里抠出 `href`（不引 XML 依赖，够用就行）。
fn hrefs(xml: &str) -> Vec<String> {
    let mut out = Vec::new();
    let mut rest = xml;
    while let Some(start) = rest.find("<") {
        // 找 `:href>` 结尾的标签开标签
        let after = &rest[start..];
        let Some(tag_end) = after.find('>') else { break };
        let tag = &after[..=tag_end];
        if tag.contains("href") && !tag.starts_with("</") {
            let content_start = start + tag_end + 1;
            if let Some(close) = rest[content_start..].find("</") {
                let value = &rest[content_start..content_start + close];
                out.push(value.trim().to_owned());
                rest = &rest[content_start + close..];
                continue;
            }
        }
        rest = &rest[tag_end + 1..];
    }
    out
}

/// 最简 base64（Basic 认证用；不引依赖）。
fn base64_encode(input: &[u8]) -> String {
    const TABLE: &[u8; 64] = b"ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/";
    let mut out = String::new();
    for chunk in input.chunks(3) {
        let b = [
            chunk[0],
            *chunk.get(1).unwrap_or(&0),
            *chunk.get(2).unwrap_or(&0),
        ];
        let n = (u32::from(b[0]) << 16) | (u32::from(b[1]) << 8) | u32::from(b[2]);
        out.push(TABLE[((n >> 18) & 63) as usize] as char);
        out.push(TABLE[((n >> 12) & 63) as usize] as char);
        out.push(if chunk.len() > 1 {
            TABLE[((n >> 6) & 63) as usize] as char
        } else {
            '='
        });
        out.push(if chunk.len() > 2 {
            TABLE[(n & 63) as usize] as char
        } else {
            '='
        });
    }
    out
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::sync::config::SyncConfig;

    #[test]
    fn base64_matches_known_values() {
        assert_eq!(base64_encode(b""), "");
        assert_eq!(base64_encode(b"f"), "Zg==");
        assert_eq!(base64_encode(b"fo"), "Zm8=");
        assert_eq!(base64_encode(b"foo"), "Zm9v");
        assert_eq!(base64_encode(b"me:pass"), "bWU6cGFzcw==");
    }

    #[test]
    fn hrefs_are_pulled_out_of_propfind_xml() {
        let xml = r#"<?xml version="1.0"?>
<d:multistatus xmlns:d="DAV:">
  <d:response><d:href>/clipnest/</d:href></d:response>
  <d:response><d:href>/clipnest/MacBook.json</d:href></d:response>
  <d:response><d:href>https://nas/clipnest/PC.json</d:href></d:response>
</d:multistatus>"#;
        let found = hrefs(xml);
        assert!(found.contains(&"/clipnest/".to_owned()));
        assert!(found.iter().any(|h| h.ends_with("MacBook.json")));
        assert!(found.iter().any(|h| h.ends_with("PC.json")));
    }

    #[test]
    fn off_and_empty_configs_are_rejected_with_reasons() {
        let mut c = SyncConfig::default();
        let err = open(&c).err().expect("应该报错");
        assert!(err.contains("没有选择同步方式"), "{err}");

        c.backend = Backend::LocalDir {
            path: "  ".to_owned(),
        };
        let err = open(&c).err().expect("应该报错");
        assert!(err.contains("本地目录"), "{err}");
    }

    #[test]
    fn local_dir_opens() {
        let c = SyncConfig {
            backend: Backend::LocalDir {
                path: "/tmp/clipnest-open-test".to_owned(),
            },
            ..SyncConfig::default()
        };
        assert!(open(&c).is_ok());
    }

    #[test]
    fn webdav_requires_a_url_scheme() {
        let err = WebDavTransport::new("nas.local/clipnest/", "me", "pw")
            .err()
            .expect("应该报错");
        assert!(err.contains("http"), "{err}");
        assert!(WebDavTransport::new("https://nas.local/clipnest/", "me", "pw").is_ok());
    }
}
