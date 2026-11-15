//! 开机自启。
//!
//! - macOS：`~/Library/LaunchAgents/<label>.plist`（`RunAtLoad`，不 KeepAlive）
//! - Windows：`HKCU\Software\Microsoft\Windows\CurrentVersion\Run`
//!
//! 开发时跑的是 `target/debug/...`，`current_exe()` 就是它；打包后是 `.app` 里的
//! 可执行文件，同样能拿到路径。

use std::path::PathBuf;

/// 自启项的名字（macOS 的 Label / Windows 的注册表值名）。
pub fn label() -> &'static str {
    "com.example.clipnest"
}

/// LaunchAgent 的 plist 内容（纯函数，便于测试）。
pub fn plist(label: &str, exe: &std::path::Path) -> String {
    format!(
        r#"<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key>
  <string>{label}</string>
  <key>ProgramArguments</key>
  <array>
    <string>{exe}</string>
  </array>
  <key>RunAtLoad</key>
  <true/>
  <key>ProcessType</key>
  <string>Interactive</string>
</dict>
</plist>
"#,
        exe = exe.display()
    )
}

/// macOS 的 plist 路径。
#[cfg(target_os = "macos")]
pub fn plist_path() -> Option<PathBuf> {
    let home = std::env::var_os("HOME")?;
    Some(
        PathBuf::from(home)
            .join("Library")
            .join("LaunchAgents")
            .join(format!("{}.plist", label())),
    )
}

#[cfg(not(target_os = "macos"))]
pub fn plist_path() -> Option<PathBuf> {
    None
}

/// 现在是不是开机自启状态。
#[cfg(target_os = "macos")]
pub fn is_enabled() -> bool {
    plist_path().is_some_and(|p| p.exists())
}

#[cfg(target_os = "windows")]
pub fn is_enabled() -> bool {
    read_run_key().is_some()
}

#[cfg(not(any(target_os = "macos", target_os = "windows")))]
pub fn is_enabled() -> bool {
    false
}

/// 开/关开机自启。返回给用户看的结果说明。
#[cfg(target_os = "macos")]
pub fn set_enabled(on: bool) -> Result<(), String> {
    let path = plist_path().ok_or("找不到 ~/Library/LaunchAgents")?;
    if on {
        let exe = std::env::current_exe().map_err(|e| format!("取不到可执行文件路径：{e}"))?;
        if let Some(dir) = path.parent() {
            std::fs::create_dir_all(dir).map_err(|e| format!("建目录失败：{e}"))?;
        }
        std::fs::write(&path, plist(label(), &exe)).map_err(|e| format!("写 plist 失败：{e}"))?;
        // 故意不 `launchctl load`：`RunAtLoad` 会让它**立刻**再起一个实例。
        // 登录时 launchd 自己会扫描 ~/Library/LaunchAgents，下次登录生效就够了。
    } else {
        let _ = std::process::Command::new("launchctl")
            .args(["unload", "-w"])
            .arg(&path)
            .output();
        if path.exists() {
            std::fs::remove_file(&path).map_err(|e| format!("删 plist 失败：{e}"))?;
        }
    }
    Ok(())
}

#[cfg(target_os = "windows")]
const RUN_KEY: &str = r"Software\Microsoft\Windows\CurrentVersion\Run";

#[cfg(target_os = "windows")]
fn read_run_key() -> Option<String> {
    use windows_sys::Win32::Foundation::ERROR_SUCCESS;
    use windows_sys::Win32::System::Registry::{
        HKEY_CURRENT_USER, KEY_QUERY_VALUE, RegCloseKey, RegOpenKeyExW, RegQueryValueExW, REG_SZ,
    };

    let key_path: Vec<u16> = RUN_KEY.encode_utf16().chain([0]).collect();
    let value: Vec<u16> = label().encode_utf16().chain([0]).collect();
    // SAFETY: 句柄成对关闭；缓冲区按查询结果给足。
    unsafe {
        let mut key = std::mem::zeroed();
        if RegOpenKeyExW(HKEY_CURRENT_USER, key_path.as_ptr(), 0, KEY_QUERY_VALUE, &mut key)
            != ERROR_SUCCESS
        {
            return None;
        }
        let mut size: u32 = 0;
        let mut kind: u32 = 0;
        let status = RegQueryValueExW(
            key,
            value.as_ptr(),
            std::ptr::null(),
            &mut kind,
            std::ptr::null_mut(),
            &mut size,
        );
        if status != ERROR_SUCCESS || kind != REG_SZ || size == 0 {
            RegCloseKey(key);
            return None;
        }
        let mut buf = vec![0u8; size as usize];
        let status = RegQueryValueExW(
            key,
            value.as_ptr(),
            std::ptr::null(),
            &mut kind,
            buf.as_mut_ptr(),
            &mut size,
        );
        RegCloseKey(key);
        if status != ERROR_SUCCESS {
            return None;
        }
        let wide: Vec<u16> = buf
            .as_chunks::<2>()
            .0
            .iter()
            .map(|c| u16::from_le_bytes(*c))
            .take_while(|c| *c != 0)
            .collect();
        Some(String::from_utf16_lossy(&wide))
    }
}

#[cfg(target_os = "windows")]
pub fn set_enabled(on: bool) -> Result<(), String> {
    use windows_sys::Win32::Foundation::ERROR_SUCCESS;
    use windows_sys::Win32::System::Registry::{
        HKEY_CURRENT_USER, KEY_SET_VALUE, REG_SZ, RegCloseKey, RegDeleteValueW, RegOpenKeyExW,
        RegSetValueExW,
    };

    let key_path: Vec<u16> = RUN_KEY.encode_utf16().chain([0]).collect();
    let value_name: Vec<u16> = label().encode_utf16().chain([0]).collect();
    // SAFETY: 句柄成对关闭；命令行按 UTF-16 + 结尾 0 传入。
    unsafe {
        let mut key = std::mem::zeroed();
        if RegOpenKeyExW(HKEY_CURRENT_USER, key_path.as_ptr(), 0, KEY_SET_VALUE, &mut key)
            != ERROR_SUCCESS
        {
            return Err("打不开注册表 Run 键".to_owned());
        }
        let status = if on {
            let exe = std::env::current_exe().map_err(|e| format!("取不到可执行文件路径：{e}"))?;
            let cmd = format!("\"{}\"", exe.display());
            let data: Vec<u16> = cmd.encode_utf16().chain([0]).collect();
            RegSetValueExW(
                key,
                value_name.as_ptr(),
                0,
                REG_SZ,
                data.as_ptr().cast(),
                (data.len() * 2) as u32,
            )
        } else {
            RegDeleteValueW(key, value_name.as_ptr())
        };
        RegCloseKey(key);
        // 删一个本来不存在的值不算失败。
        if status != ERROR_SUCCESS && status != windows_sys::Win32::Foundation::ERROR_FILE_NOT_FOUND
        {
            return Err(format!("注册表写入失败（code {status}）"));
        }
    }
    Ok(())
}

#[cfg(not(any(target_os = "macos", target_os = "windows")))]
pub fn set_enabled(_on: bool) -> Result<(), String> {
    Err("这个平台还没实现开机自启".to_owned())
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn plist_escapes_nothing_but_has_the_essentials() {
        let text = plist(
            "com.example.clipnest",
            std::path::Path::new("/Applications/clipnest.app/Contents/MacOS/clipnest"),
        );
        assert!(text.contains("<key>Label</key>"));
        assert!(text.contains("<string>com.example.clipnest</string>"));
        assert!(text.contains("<key>RunAtLoad</key>"));
        assert!(
            text.contains("/Applications/clipnest.app/Contents/MacOS/clipnest"),
            "要写绝对路径"
        );
        assert!(!text.contains("<key>KeepAlive</key>"), "别 KeepAlive：退出后不该被拉起");
        assert!(text.starts_with("<?xml"));
    }

    #[cfg(target_os = "macos")]
    #[test]
    fn plist_path_points_at_launch_agents() {
        let p = plist_path().expect("HOME 存在时应有路径");
        assert!(p.ends_with("Library/LaunchAgents/com.example.clipnest.plist"), "{p:?}");
    }

    /// 打字别真的写用户目录：只验证「写着路径的样子」。
    #[cfg(target_os = "macos")]
    #[test]
    fn label_matches_the_app_id() {
        assert_eq!(label(), "com.example.clipnest");
    }
}
