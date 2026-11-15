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
            if *insecure_tls {
                crate::logs::line(
                    "SYNC insecure_tls：不校验服务器证书（NAS 自签证书时才该打开）",
                );
            }
            let password = crate::sync::secret::get(WEBDAV_SECRET_SERVICE, username)
                .ok_or_else(|| "钥匙串里没有这个 WebDAV 账号的密码".to_owned())?;
            Ok(Box::new(WebDavTransport::new(
                &base,
                username,
                &password,
                *insecure_tls,
            )?))
        }
    }
}

/// WebDAV：一台设备一个文件，PROPFIND 列目录。
///
/// 用 reqwest 而不是 ureq：ureq 的协议层只允许标准方法，**发不出 PROPFIND**
/// （实测 `PROPFIND not valid for HTTP version HTTP/1.1`）。
pub struct WebDavTransport {
    client: reqwest::blocking::Client,
    base: String,
    username: String,
    password: String,
}

impl WebDavTransport {
    /// `insecure_tls`：不校验服务器证书（群晖/飞牛这类 NAS 用自签证书时的必需品）。
    pub fn new(
        base: &str,
        username: &str,
        password: &str,
        insecure_tls: bool,
    ) -> Result<Self, String> {
        if !base.starts_with("http://") && !base.starts_with("https://") {
            return Err("WebDAV 地址要以 http:// 或 https:// 开头".to_owned());
        }
        let client = reqwest::blocking::Client::builder()
            .danger_accept_invalid_certs(insecure_tls)
            // 同步目标是局域网/自建服务，绝不该走系统代理（否则会卡到超时）。
            .no_proxy()
            // NAS 多半是局域网自签证书，超时别等太久。
            .timeout(std::time::Duration::from_secs(20))
            .build()
            .map_err(|e| format!("建 HTTP 客户端失败：{e}"))?;
        Ok(Self {
            client,
            base: base.to_owned(),
            username: username.to_owned(),
            password: password.to_owned(),
        })
    }

    fn url(&self, name: &str) -> String {
        format!("{}{}", self.base, name)
    }

    /// 发一个任意方法的请求（PROPFIND/MKCOL 都不是标准方法）。
    fn request(
        &self,
        method: &str,
        url: &str,
        extra: &[(&str, &str)],
        body: Vec<u8>,
    ) -> Result<(u16, Vec<u8>), String> {
        let method_str = method.to_owned();
        let method = reqwest::Method::from_bytes(method.as_bytes())
            .map_err(|e| format!("方法名不对：{e}"))?;
        let mut request = self
            .client
            .request(method, url)
            .basic_auth(&self.username, Some(&self.password));
        for (k, v) in extra {
            request = request.header(*k, *v);
        }
        let started = std::time::Instant::now();
        let mut response = request
            .body(body)
            .send()
            .map_err(|e| format!("请求 {url} 失败：{e}"))?;
        let status = response.status().as_u16();
        let mut bytes = Vec::new();
        response
            .copy_to(&mut bytes)
            .map_err(|e| format!("读响应失败：{e}"))?;
        let ms = started.elapsed().as_millis();
        if ms > 500 {
            crate::logs::line(format!("SYNC slow {method_str} {url} {status} {ms}ms"));
        }
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
            let (created, _) =
                self.request("MKCOL", self.base.trim_end_matches('/'), &[], Vec::new())?;
            if created >= 400 && created != 405 {
                return Err(format!("远端目录不存在，创建也失败（{created}）"));
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
        let url = self.url(name);
        let (status, _) = self.request(
            "PUT",
            &url,
            &[("Content-Type", "application/octet-stream")],
            bytes.to_vec(),
        )?;
        // 子目录（`blobs/1a/`）可能还不存在。各家的回答不一样：标准是 409，
        // 但也见过 404，飞牛（fnOS）直接回 403——都当"父目录没有"处理。
        if matches!(status, 403 | 404 | 409) {
            let mut parent = self.base.clone();
            let parts: Vec<&str> = name.split('/').collect();
            for dir in &parts[..parts.len().saturating_sub(1)] {
                parent.push_str(dir);
                parent.push('/');
                let _ = self.request("MKCOL", parent.trim_end_matches('/'), &[], Vec::new());
            }
            let (retry, _) = self.request(
                "PUT",
                &url,
                &[("Content-Type", "application/octet-stream")],
                bytes.to_vec(),
            )?;
            if retry >= 400 {
                return Err(format!("上传 {name} 失败（HTTP {retry}）"));
            }
            return Ok(());
        }
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

#[cfg(test)]
mod tests {
    use super::*;
    use crate::sync::config::SyncConfig;

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

    /// 真机联调：口令从系统钥匙串读，命令行不留明文。
    ///
    /// ```bash
    /// WEBDAV_URL=https://192.168.1.2:5006/clipnest/ WEBDAV_USER=me \
    ///   cargo test live_webdav_probe -- --ignored --nocapture
    /// ```
    #[test]
    #[ignore = "需要真实 NAS 与钥匙串里的凭据"]
    fn live_webdav_probe() {
        let url = std::env::var("WEBDAV_URL").expect("WEBDAV_URL");
        let user = std::env::var("WEBDAV_USER").expect("WEBDAV_USER");
        let password = crate::sync::secret::get(crate::sync::config::WEBDAV_SECRET_SERVICE, &user)
            .expect("钥匙串里没有这个账号的密码");
        let t = WebDavTransport::new(&url, &user, &password, true).expect("建 client");
        match t.list() {
            Ok(files) => println!("list OK: {files:?}"),
            Err(e) => println!("list ERROR: {e}"),
        }
        // 下载别的设备的真实包（验证 GET 路径）
        let t0 = std::time::Instant::now();
        match t.get("mac-a.json") {
            Ok(bytes) => println!("get mac-a.json: {} 字节, {:?}", bytes.len(), t0.elapsed()),
            Err(e) => println!("get mac-a.json ERROR ({:?}): {e}", t0.elapsed()),
        }

        // 顺便验证一次写入/读回/删除
        let name = "probe-test.json";
        match t.put(name, b"{\"probe\":true}") {
            Ok(()) => {
                println!("put OK");
                println!("get: {:?}", t.get(name).map(|b| String::from_utf8_lossy(&b).to_string()));
                println!("remove: {:?}", t.remove(name));
            }
            Err(e) => println!("put ERROR: {e}"),
        }
    }

    #[test]
    fn webdav_requires_a_url_scheme() {
        let err = WebDavTransport::new("nas.local/clipnest/", "me", "pw", false)
            .err()
            .expect("应该报错");
        assert!(err.contains("http"), "{err}");
        assert!(WebDavTransport::new("https://nas.local/clipnest/", "me", "pw", false).is_ok());
        assert!(
            WebDavTransport::new("https://192.168.1.2:5006/clipnest/", "me", "pw", true).is_ok(),
            "自签证书：insecure_tls 也要能建"
        );
    }
}
