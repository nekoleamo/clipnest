//! 敏感应用过滤：密码管理器里复制的东西默认不进历史。
//!
//! 规则按**应用身份**（macOS 的 bundle id、Windows 的进程名）判断，不看内容——
//! 猜密码长得像什么既不可靠也没必要。
//!
//! 三道口子：
//! 1. 内置白名单（[`DEFAULT_APPS`]），可在 `settings.json` 的 `sensitive_apps` 覆盖；
//! 2. `settings.ignore_sensitive_apps = false` 整体关掉；
//! 3. 面板里 `⌘E` 可以把单条标记/取消标记，标记的条目显示 🔒。

use crate::settings::Settings;

/// 内置白名单：密码管理器与系统钥匙串相关进程。
///
/// macOS 用 bundle id（大小写不敏感地比对），Windows 用进程名（不带 `.exe`）。
pub const DEFAULT_APPS: &[&str] = &[
    // 1Password
    "com.1password.1password",
    "com.agilebits.onepassword7",
    "1password",
    // Bitwarden
    "com.bitwarden.desktop",
    "bitwarden",
    // KeePassXC / KeePass
    "org.keepassxc.keepassxc",
    "keepassxc",
    "keepass",
    // LastPass / Dashlane / NordPass / Enpass / Proton Pass
    "com.lastpass.lastpass",
    "lastpass",
    "com.dashlane.dashlane",
    "dashlane",
    "com.nordpass.nordpass",
    "nordpass",
    "com.enpass.enpass",
    "enpass",
    "me.proton.pass.electron",
    "protonpass",
    // 系统钥匙串（注意：不要放 com.apple.safari —— Safari 里复制普通文本很常见）
    "com.apple.keychainaccess",
    "com.apple.securityagent",
    "securityagent",
];

/// 生效的白名单：设置在 `sensitive_apps` 里给了就用它，否则用内置表。
pub fn list(settings: &Settings) -> Vec<String> {
    if settings.sensitive_apps.is_empty() {
        DEFAULT_APPS.iter().map(|s| (*s).to_owned()).collect()
    } else {
        settings.sensitive_apps.clone()
    }
}

/// 这个应用要不要拦。
///
/// 比对规则：都转小写、去掉 `.exe`，然后
/// - 相等；
/// - 以 `名单项.` 开头（macOS 的 `com.1password.1password.helper` 这类子进程）；
/// - 以 `.名单项` 结尾。
///
/// 不做裸 `ends_with`：`notkeepassxc` 不该命中 `keepassxc`。
pub fn matches(app: &str, apps: &[String]) -> bool {
    let app = app.trim().to_ascii_lowercase();
    if app.is_empty() {
        return false;
    }
    let app = app.strip_suffix(".exe").unwrap_or(&app);
    apps.iter().any(|candidate| {
        let c = candidate.trim().to_ascii_lowercase();
        if c.is_empty() {
            return false;
        }
        app == c || app.starts_with(&format!("{c}.")) || app.ends_with(&format!(".{c}"))
    })
}

/// 按当前设置判断：这个前台应用要不要跳过。
pub fn should_skip(settings: &Settings, app: Option<&str>) -> bool {
    if !settings.ignore_sensitive_apps {
        return false;
    }
    let Some(app) = app else {
        return false;
    };
    matches(app, &list(settings))
}

/// 当前前台应用的标识（macOS: bundle id 优先、退化为可执行名；Windows: 进程名）。
#[cfg(target_os = "macos")]
pub fn front_app() -> Option<String> {
    use objc2_app_kit::NSWorkspace;

    // 这些查询是只读的，允许多线程；watcher 线程也会调。
    let ws = NSWorkspace::sharedWorkspace();
    let app = ws.frontmostApplication()?;
    if let Some(id) = app.bundleIdentifier() {
        return Some(id.to_string());
    }
    app.localizedName().map(|n| n.to_string())
}

#[cfg(target_os = "windows")]
pub fn front_app() -> Option<String> {
    use windows_sys::Win32::Foundation::CloseHandle;
    use windows_sys::Win32::System::Threading::{
        OpenProcess, PROCESS_NAME_WIN32, QueryFullProcessImageNameW,
    };
    use windows_sys::Win32::UI::WindowsAndMessaging::{
        GetForegroundWindow, GetWindowThreadProcessId,
    };

    // SAFETY: 句柄用完即关；缓冲区按 API 语义给足长度。
    unsafe {
        let hwnd = GetForegroundWindow();
        if hwnd.is_null() {
            return None;
        }
        let mut pid = 0u32;
        GetWindowThreadProcessId(hwnd, &mut pid);
        if pid == 0 {
            return None;
        }
        let process = OpenProcess(0x1000 /* PROCESS_QUERY_LIMITED_INFORMATION */, 0, pid);
        if process.is_null() {
            return None;
        }
        let mut buf = [0u16; 1024];
        let mut len = buf.len() as u32;
        let ok = QueryFullProcessImageNameW(process, PROCESS_NAME_WIN32, buf.as_mut_ptr(), &mut len);
        CloseHandle(process);
        if ok == 0 {
            return None;
        }
        let path = String::from_utf16_lossy(&buf[..len as usize]);
        Some(
            path.rsplit(['\\', '/'])
                .next()
                .unwrap_or(&path)
                .to_ascii_lowercase(),
        )
    }
}

#[cfg(not(any(target_os = "macos", target_os = "windows")))]
pub fn front_app() -> Option<String> {
    None
}

#[cfg(test)]
mod tests {
    use super::*;

    fn apps(list: &[&str]) -> Vec<String> {
        list.iter().map(|s| (*s).to_owned()).collect()
    }

    #[test]
    fn bundle_ids_match_exactly_and_ignore_case() {
        let list = apps(&["com.1password.1password", "com.bitwarden.desktop"]);
        assert!(matches("com.1password.1password", &list));
        assert!(matches("COM.1PASSWORD.1PASSWORD", &list));
        assert!(matches("com.bitwarden.desktop", &list));
        assert!(!matches("com.apple.safari", &list));
        assert!(!matches("", &list));
    }

    #[test]
    fn windows_process_names_ignore_exe_suffix() {
        let list = apps(&["keepassxc", "bitwarden"]);
        assert!(matches("KeePassXC.exe", &list));
        assert!(matches("keepassxc", &list));
        assert!(matches("Bitwarden.exe", &list));
        assert!(!matches("explorer.exe", &list));
    }

    #[test]
    fn helper_processes_of_a_listed_app_also_match() {
        // 1Password 有 com.1password.1password.helper 之类的子进程
        let list = apps(&["com.1password.1password"]);
        assert!(matches("com.1password.1password.helper", &list));
    }

    #[test]
    fn settings_override_the_builtin_list() {
        let mut s = Settings::default();
        assert!(list(&s).iter().any(|a| a == "com.bitwarden.desktop"), "默认用内置表");

        s.sensitive_apps = apps(&["com.my.pass"]);
        assert_eq!(list(&s), apps(&["com.my.pass"]));
    }

    #[test]
    fn can_be_turned_off_and_handles_no_app() {
        let mut s = Settings::default();
        assert!(should_skip(&s, Some("com.bitwarden.desktop")));
        assert!(!should_skip(&s, Some("com.apple.mail")));
        assert!(!should_skip(&s, None), "拿不到应用就不拦");

        s.ignore_sensitive_apps = false;
        assert!(!should_skip(&s, Some("com.bitwarden.desktop")));
    }

    #[test]
    fn builtin_list_is_lowercase_and_deduped() {
        let mut seen = std::collections::HashSet::new();
        for app in DEFAULT_APPS {
            assert_eq!(*app, app.to_ascii_lowercase(), "{app} 要小写");
            assert!(seen.insert(*app), "{app} 重复了");
        }
    }
}
