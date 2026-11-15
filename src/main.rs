//! 在机器的原生后端上直接运行 Rust 应用。

#![cfg_attr(target_os = "windows", windows_subsystem = "windows")]

#[cfg(target_os = "linux")]
use nsh_platform_linux as platform;
#[cfg(target_os = "macos")]
use nsh_platform_macos as platform;
#[cfg(target_os = "windows")]
use nsh_platform_windows as platform;

#[cfg(any(target_os = "linux", target_os = "macos", target_os = "windows"))]
fn main() -> impl std::process::Termination {
    env_logger::init();
    nsh_app_clipnest::boot();
    platform::run_app(
        platform::AppSource::Rust(platform::Root::of::<nsh_app_clipnest::App>()),
        platform::AppOptions {
            title: "clipnest".to_owned(),
            #[cfg(any(target_os = "linux", target_os = "windows"))]
            app_id: "com.example.clipnest".to_owned(),
            size: (420, 320),
            keep_alive_on_close: true,
            #[cfg(target_os = "macos")]
            activation_policy: platform::ActivationPolicy::Accessory,
            ..Default::default()
        },
    )
}

#[cfg(not(any(target_os = "linux", target_os = "macos", target_os = "windows")))]
compile_error!("Run this binary on macOS, Windows or Linux.");
