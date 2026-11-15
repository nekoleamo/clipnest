//! 应用内自更新（不依赖 Apple 签名/公证）。
//!
//! 思路：**不带「下载标记」的文件不会被 Gatekeeper / SmartScreen 拦**。浏览器下载会打
//! `com.apple.quarantine`（macOS）或 Mark-of-the-Web（Windows），而应用自己下载、自己替换的
//! 文件没有这些标记 —— 于是首次安装放行一次之后，后续更新全都没有拦截。
//!
//! 完整性靠**自己的 ed25519 公钥**：`latest.json` 必须带 `latest.json.sig`，
//! 公钥写死在 app 的 `nsh-updater.json` 里（`nsh.json` 的 `updater.publicKey`）。
//! 这条路不需要花任何钱，而且比「什么都不签」安全。
//!
//! 资产按平台命名：`macos-aarch64`、`macos-x86_64`、`windows-x86_64`。

use std::path::PathBuf;

use nsh_updater::install::Installation;
use nsh_updater::{Config, Download};

/// 当前平台在 manifest 里的键。
pub fn platform() -> &'static str {
    #[cfg(all(target_os = "macos", target_arch = "aarch64"))]
    {
        "macos-aarch64"
    }
    #[cfg(all(target_os = "macos", target_arch = "x86_64"))]
    {
        "macos-x86_64"
    }
    #[cfg(all(target_os = "windows", target_arch = "x86_64"))]
    {
        "windows-x86_64"
    }
    #[cfg(all(target_os = "windows", target_arch = "aarch64"))]
    {
        "windows-aarch64"
    }
    #[cfg(not(any(target_os = "macos", target_os = "windows")))]
    {
        "linux-x86_64"
    }
}

/// 平台键要和 nsh 的 `platform()` 一致（它用 `std::env::consts`）。
#[cfg(test)]
fn platform_matches_nsh() -> bool {
    platform() == nsh_updater::platform()
}

/// 找到 app 自带的 `nsh-updater.json`（`nsh package` 打进去的那份）。
pub fn config_path() -> Option<PathBuf> {
    let exe = std::env::current_exe().ok()?;
    let dir = exe.parent()?;
    #[cfg(target_os = "macos")]
    let candidates = [
        // <app>.app/Contents/MacOS/clipnest → <app>.app/Contents/Resources/nsh/assets/
        dir.parent()?.join("Resources/nsh/assets/nsh-updater.json"),
    ];
    #[cfg(not(target_os = "macos"))]
    let candidates = [
        // <dir>/clipnest.exe → <dir>/nsh/assets/
        dir.join("nsh/assets/nsh-updater.json"),
        dir.join("nsh/nsh-updater.json"),
    ];
    candidates.into_iter().find(|p| p.exists())
}

/// 读配置（没有就说明这个包不是 `nsh package` 打的，或者没配 `updater`）。
pub fn config() -> Result<(Config, Vec<u8>), String> {
    let path = config_path()
        .ok_or_else(|| "找不到 nsh-updater.json（这个包不是 nsh package 打的？）".to_owned())?;
    let bytes = std::fs::read(&path).map_err(|e| format!("读 {} 失败：{e}", path.display()))?;
    let config: Config =
        serde_json::from_slice(&bytes).map_err(|e| format!("解析更新配置失败：{e}"))?;
    Ok((config, bytes))
}

/// 发现的新版本。
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct Available {
    pub version: String,
    pub notes: String,
    pub url: String,
    pub sha256: String,
    pub size: Option<u64>,
}

fn http() -> Result<reqwest::blocking::Client, String> {
    // 更新走公网：**要**用系统/环境代理（与局域网同步相反）。
    reqwest::blocking::Client::builder()
        .timeout(std::time::Duration::from_secs(30))
        .build()
        .map_err(|e| format!("建 HTTP 客户端失败：{e}"))
}

fn manifest_url(config: &Config) -> String {
    match &config.github {
        Some(repo) => format!("https://github.com/{repo}/releases/latest/download/latest.json"),
        None => config.url.clone().unwrap_or_default(),
    }
}

/// 检查是否有新版本（联网）。
pub fn check() -> Result<Option<Available>, String> {
    let (config, _) = config()?;
    let base = manifest_url(&config);
    if base.is_empty() {
        return Err("更新配置里没有 github 或 url".to_owned());
    }
    let client = http()?;

    let mut manifest = client
        .get(&base)
        .send()
        .map_err(|e| format!("拉取 {base} 失败：{e}"))?;
    if !manifest.status().is_success() {
        return Err(format!("拉取更新清单失败（HTTP {}）", manifest.status()));
    }
    let mut bytes = Vec::new();
    manifest
        .copy_to(&mut bytes)
        .map_err(|e| format!("读更新清单失败：{e}"))?;

    // 签名是必须的：没有 sig 的清单一律不信。
    let sig_url = format!("{base}.sig");
    let mut sig_response = client
        .get(&sig_url)
        .send()
        .map_err(|e| format!("拉取签名 {sig_url} 失败：{e}"))?;
    let mut sig = Vec::new();
    sig_response
        .copy_to(&mut sig)
        .map_err(|e| format!("读签名失败：{e}"))?;

    let available = config
        .check(&bytes, Some(&sig), platform())
        .map_err(|e| format!("更新清单不可信：{e}"))?;
    Ok(available.and_then(|a| {
        // 没有 sha256 说明这是个"说明页"而不是包，不能自动装。
        a.sha256.map(|sha256| Available {
            version: a.version,
            notes: a.notes.unwrap_or_default(),
            url: a.url,
            sha256,
            size: a.size,
        })
    }))
}

/// 下载并替换当前安装（替换完需要重启才生效）。
pub fn install(available: &Available) -> Result<(), String> {
    let (cfg, config_bytes) = config()?;
    let installation = Installation::current(&cfg.id, &config_bytes)
        .ok_or_else(|| "认不出当前安装位置（不是 nsh package 打的包？）".to_owned())?;

    let client = http()?;
    let mut response = client
        .get(&available.url)
        .send()
        .map_err(|e| format!("下载更新失败：{e}"))?;
    if !response.status().is_success() {
        return Err(format!("下载更新失败（HTTP {}）", response.status()));
    }
    let mut download = Download::new(&available.sha256, available.size);
    let mut body: Vec<u8> = Vec::with_capacity(available.size.unwrap_or(0) as usize);
    let mut buffer = [0u8; 64 * 1024];
    loop {
        let read = std::io::Read::read(&mut response, &mut buffer)
            .map_err(|e| format!("读取更新失败：{e}"))?;
        if read == 0 {
            break;
        }
        download
            .update(&buffer[..read])
            .map_err(|e| format!("校验更新包失败：{e}"))?;
        // 攒起来最后落盘（几十 MB，交给 updater 解包）
        body.extend_from_slice(&buffer[..read]);
    }
    download
        .finish()
        .map_err(|e| format!("更新包不完整或校验失败：{e}"))?;

    // 落到临时文件再交给 updater（它负责解包、校验 id、替换）。
    let scratch = std::env::temp_dir().join(format!("clipnest-update-{}.pkg", available.version));
    std::fs::write(&scratch, &body).map_err(|e| format!("写临时文件失败：{e}"))?;
    let result = installation.install(&scratch);
    let _ = std::fs::remove_file(&scratch);
    result.map_err(|e| format!("安装更新失败：{e}"))
}

/// 最近一次检查/安装的状态，UI 每几百毫秒读一次。
static STATUS: std::sync::Mutex<String> = std::sync::Mutex::new(String::new());
/// 检查到的可用版本（有值才能安装）。
static AVAILABLE: std::sync::Mutex<Option<Available>> = std::sync::Mutex::new(None);

pub fn status() -> String {
    STATUS.lock().map(|s| s.clone()).unwrap_or_default()
}

fn set_status(text: String) {
    if let Ok(mut s) = STATUS.lock() {
        *s = text.clone();
    }
    crate::logs::line(format!("UPDATE {text}"));
}

/// 有没有检查到可用版本。
pub fn has_available() -> bool {
    AVAILABLE.lock().map(|a| a.is_some()).unwrap_or(false)
}

/// 后台检查更新（不卡 UI）。
pub fn check_async() {
    set_status("检查中…".to_owned());
    std::thread::spawn(|| match check() {
        Ok(None) => set_status("已是最新版本".to_owned()),
        Ok(Some(a)) => {
            if let Ok(mut slot) = AVAILABLE.lock() {
                *slot = Some(a.clone());
            }
            set_status(format!("发现新版本 {}", a.version));
        }
        Err(e) => set_status(format!("❌ {e}")),
    });
}

/// 后台下载并安装（替换当前安装，重启后生效）。
pub fn install_async() {
    let available = AVAILABLE.lock().ok().and_then(|a| a.clone());
    let Some(available) = available else {
        set_status("先点「检查更新」".to_owned());
        return;
    };
    set_status(format!("正在下载 {}", available.version));
    std::thread::spawn(move || match install(&available) {
        Ok(()) => set_status(format!(
            "✅ 已安装 {}，退出重开生效",
            available.version
        )),
        Err(e) => set_status(format!("❌ {e}")),
    });
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn platform_key_matches_the_manifest_vocabulary() {
        let p = platform();
        assert!(
            ["macos-aarch64", "macos-x86_64", "windows-x86_64", "linux-x86_64"]
                .contains(&p),
            "{p}"
        );
        if cfg!(target_os = "macos") && cfg!(target_arch = "aarch64") {
            assert_eq!(p, "macos-aarch64");
        }
        assert!(platform_matches_nsh(), "平台键要和 nsh-updater 的 platform() 一致");
    }

    /// 用 `tools/release.sh` 产出的真实 `latest.json` + `.sig` 验一遍签名链：
    /// 把"当前版本"伪装成 0.0.1，`check` 就应该认为 1.0.0 是可用的新版本。
    /// 需要先跑过 release.sh（`dist/latest.json` 存在）。
    #[test]
    #[ignore = "需要先跑 tools/release.sh 生成 dist/latest.json"]
    fn signed_manifest_verifies_and_offers_a_newer_version() {
        let manifest = std::fs::read("dist/latest.json").expect("先跑 tools/release.sh");
        let sig = std::fs::read("dist/latest.json.sig").expect("缺签名");
        let pubkey = std::fs::read_to_string(".keys/clipnest-release.pub")
            .ok()
            .map(|s| s.trim().to_owned())
            .expect("缺公钥");

        // Config 是 non_exhaustive：只能从 JSON 反序列化，不能直接构造
        let config: Config = serde_json::from_value(serde_json::json!({
            "id": "com.example.clipnest",
            "version": "0.0.1",          // 伪装的"当前版本"
            "url": "https://example.com/latest.json",
            "publicKey": pubkey,
            "build": 1
        }))
        .expect("构造 Config");
        let available = config
            .check(&manifest, Some(&sig), platform())
            .expect("签名应通过")
            .expect("0.0.1 应该看到 1.0.0");
        assert_eq!(available.version, "1.0.0");
        assert!(available.sha256.is_some(), "manifest 要带 sha256 才能自动安装");

        // 篡改一个字节就必须被拒
        let mut broken = manifest.clone();
        let pos = broken.len() / 2;
        broken[pos] ^= 0x01;
        assert!(config.check(&broken, Some(&sig), platform()).is_err(), "篡改要报错");
        // 没有签名也必须被拒
        assert!(config.check(&manifest, None, platform()).is_err(), "无签名要报错");
    }

    #[test]
    fn missing_config_is_reported_clearly() {
        // 测试进程不是 nsh package 打的 app，通常找不到 nsh-updater.json；
        // 找到也算通过（developer 在本机跑过打包后的二进制）。
        match config() {
            Ok((config, bytes)) => {
                assert!(!config.id.is_empty());
                assert!(!bytes.is_empty());
            }
            Err(e) => assert!(e.contains("nsh-updater.json"), "{e}"),
        }
    }
}
