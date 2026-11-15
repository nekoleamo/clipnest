//! 凭据：同步口令与 WebDAV 密码存系统钥匙串，**不**进 `sync.json`。
//!
//! - macOS：`security-framework`（Keychain 的 generic password）
//! - Windows：Credential Manager（`CredWriteW` / `CredReadW` / `CredDeleteW`）
//!
//! 开源项目里配置模板只带「服务名」，用户自己的口令留在系统里。

/// 存一个口令（同名覆盖）。
#[cfg(target_os = "macos")]
pub fn set(service: &str, account: &str, secret: &str) -> Result<(), String> {
    use security_framework::passwords::{delete_generic_password, set_generic_password};

    // 先删后写：`set_generic_password` 对已存在的条目会报错。
    let _ = delete_generic_password(service, account);
    set_generic_password(service, account, secret.as_bytes())
        .map_err(|e| format!("写钥匙串失败：{e}"))
}

/// 读一个口令；没有就 `None`。
#[cfg(target_os = "macos")]
pub fn get(service: &str, account: &str) -> Option<String> {
    use security_framework::passwords::get_generic_password;
    let bytes = get_generic_password(service, account).ok()?;
    String::from_utf8(bytes).ok()
}

/// 删除一个口令；不存在不算错。
#[cfg(target_os = "macos")]
pub fn delete(service: &str, account: &str) -> Result<(), String> {
    use security_framework::passwords::delete_generic_password;
    match delete_generic_password(service, account) {
        Ok(()) => Ok(()),
        // 条目不存在时 security-framework 报 -25300（errSecItemNotFound）
        Err(_) => Ok(()),
    }
}

#[cfg(target_os = "windows")]
fn wide(s: &str) -> Vec<u16> {
    s.encode_utf16().chain([0]).collect()
}

#[cfg(target_os = "windows")]
pub fn set(service: &str, account: &str, secret: &str) -> Result<(), String> {
    use windows_sys::Win32::Foundation::ERROR_SUCCESS;
    use windows_sys::Win32::Security::Credentials::{
        CRED_TYPE_GENERIC, CREDENTIALW, CredWriteW,
    };

    let target = wide(&format!("{service}/{account}"));
    // 用户名要活到 CredWriteW 调用结束（悬垂指针是 clippy 抓出来的真 bug）。
    let user = wide(account);
    let mut blob = secret.as_bytes().to_vec();
    let mut cred: CREDENTIALW = unsafe { std::mem::zeroed() };
    cred.Type = CRED_TYPE_GENERIC;
    cred.TargetName = target.as_ptr() as *mut u16;
    cred.CredentialBlobSize = blob.len() as u32;
    cred.CredentialBlob = blob.as_mut_ptr();
    cred.Persist = 2; // CRED_PERSIST_LOCAL_MACHINE
    cred.UserName = user.as_ptr() as *mut u16;
    // SAFETY: 结构体字段全部指向上面的有效缓冲区。
    // CredWriteW 返回 BOOL（i32），ERROR_SUCCESS 是 u32。
    let ok = unsafe { CredWriteW(&cred, 0) };
    if ok == ERROR_SUCCESS as i32 {
        Ok(())
    } else {
        Err(format!("写凭据管理器失败（code {ok}）"))
    }
}

#[cfg(target_os = "windows")]
pub fn get(service: &str, account: &str) -> Option<String> {
    use windows_sys::Win32::Foundation::ERROR_SUCCESS;
    use windows_sys::Win32::Security::Credentials::{CRED_TYPE_GENERIC, CREDENTIALW, CredFree, CredReadW};

    let target = wide(&format!("{service}/{account}"));
    let mut out: *mut CREDENTIALW = std::ptr::null_mut();
    // SAFETY: CredReadW 分配结构体，读完用 CredFree 释放。
    unsafe {
        if CredReadW(target.as_ptr(), CRED_TYPE_GENERIC, 0, &mut out) != ERROR_SUCCESS as i32
            || out.is_null()
        {
            return None;
        }
        let cred = &*out;
        let bytes = std::slice::from_raw_parts(cred.CredentialBlob, cred.CredentialBlobSize as usize);
        let text = String::from_utf8(bytes.to_vec()).ok();
        CredFree(out as *const core::ffi::c_void);
        text
    }
}

#[cfg(target_os = "windows")]
pub fn delete(service: &str, account: &str) -> Result<(), String> {
    use windows_sys::Win32::Security::Credentials::{CRED_TYPE_GENERIC, CredDeleteW};
    let target = wide(&format!("{service}/{account}"));
    // SAFETY: 只读调用；目标不存在时返回非零，按成功处理（幂等）。
    unsafe {
        CredDeleteW(target.as_ptr(), CRED_TYPE_GENERIC, 0);
    }
    Ok(())
}

#[cfg(not(any(target_os = "macos", target_os = "windows")))]
pub fn set(_service: &str, _account: &str, _secret: &str) -> Result<(), String> {
    Err("这个平台还没接钥匙串".to_owned())
}

#[cfg(not(any(target_os = "macos", target_os = "windows")))]
pub fn get(_service: &str, _account: &str) -> Option<String> {
    None
}

#[cfg(not(any(target_os = "macos", target_os = "windows")))]
pub fn delete(_service: &str, _account: &str) -> Result<(), String> {
    Ok(())
}

/// 钥匙串里有没有这个口令。
pub fn exists(service: &str, account: &str) -> bool {
    get(service, account).is_some()
}

#[cfg(test)]
mod tests {

    /// 只在真机上跑钥匙串读写：不写业务用的 service 名，避免污染。
    #[test]
    fn keychain_round_trip() {
        #[cfg(target_os = "macos")]
        {
            use super::*;
            let service = "clipnest-test-only";
            let account = "unit-test";
            set(service, account, "secret-123").unwrap();
            assert_eq!(get(service, account).as_deref(), Some("secret-123"));
            assert!(exists(service, account));
            // 覆盖写
            set(service, account, "secret-456").unwrap();
            assert_eq!(get(service, account).as_deref(), Some("secret-456"));
            delete(service, account).unwrap();
            assert!(get(service, account).is_none());
            // 删两次不报错
            delete(service, account).unwrap();
        }
    }
}
