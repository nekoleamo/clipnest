//! 菜单栏图标与菜单（macOS 菜单栏右侧 / Windows 通知区）。
//!
//! 图标用内联 SVG：macOS 会当模板图，跟随菜单栏文字颜色。

use std::rc::Rc;

use nsh_core::element::ImageSrc;
use nsh_core::desktop::TrayTitleRun;
use nsh_core::element::desktop::{MenuItem, set_tray, tray};

/// 剪贴板图标（16×16，笔画用黑色：模板图只认不透明度）。
const ICON: &str = r#"<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 16 16"><rect x="3.7" y="3.3" width="8.6" height="11" rx="1.8" fill="none" stroke="black" stroke-width="1.3"/><rect x="5.9" y="1.7" width="4.2" height="3" rx="1" fill="black"/></svg>"#;

/// 挂上托盘图标。
///
/// `on_toggle`：点图标 / 「显示面板」；
/// `on_prefs`：「偏好设置…」。
pub fn attach(on_toggle: impl Fn() + 'static, on_prefs: impl Fn() + 'static) {
    let toggle: Rc<dyn Fn()> = Rc::new(on_toggle);
    let show = toggle.clone();
    let click = toggle.clone();
    set_tray(
        tray()
            .icon(ImageSrc::svg_markup(ICON))
            .tooltip("clipnest 剪贴板历史")
            // 图标在菜单栏里太小/太素，配一个字更容易点中（也方便自动化验证）。
            .title([TrayTitleRun::new("❐")])
            .menu(vec![
                MenuItem::new("显示面板")
                    .shortcut("mod+shift+v")
                    .on_select(move || show()),
                MenuItem::new("偏好设置…")
                    .shortcut("mod+,")
                    .on_select(on_prefs),
                MenuItem::separator(),
                MenuItem::new("退出 clipnest").on_select(crate::quit_app),
            ])
            .on_click(move |_| click()),
    );
    crate::logs::line("TRAY attached".to_owned());
}
