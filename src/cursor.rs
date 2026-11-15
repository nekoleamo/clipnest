//! 鼠标所在显示器：多屏时把面板摆到用户正在看的那块屏上。
//!
//! 不用 `nsh_core::element::desktop::displays()`：它读 nsh 的响应式环境，
//! 在 `run_on_main_thread` 的回调里调用会 panic（实测
//! `no reactive runtime: signals are used on the engine thread`）。
//! 这里直接问系统要屏幕区域，只算窗口坐标，再交给 `WindowRef::set_bounds`。
//!
//! 坐标约定：nsh 的屏幕坐标是**左上原点、y 向下**；
//! macOS 的 `NSScreen.frame` 是左下原点、y 向上，需要按主屏高度翻转。

/// 面板放在屏幕的哪个位置。
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum Position {
    /// 屏幕正中。
    Center,
    /// 贴屏幕底部（距底 24pt，留出 Dock/输入法条的空间）。
    Bottom,
}

/// 从设置里的字符串来。
pub fn position_of(text: &str) -> Position {
    if text == "center" {
        Position::Center
    } else {
        Position::Bottom
    }
}

/// 面板在主屏上的位置（左上角，nsh 坐标）。拿不到就返回 `None`。
///
/// **只在窗口创建时用得上**：nsh 的 `WindowRef::set_bounds` 对 `WindowKind::Popup`
/// 不生效（实测窗口纹丝不动），所以位置由 `window().default_position(..)` 在创建时给。
///
/// 这里刻意只用 CoreGraphics（`CGDisplay::main().bounds()`）：`NSScreen`/`NSEvent`
/// 要求主线程，而打包后的 runner 里 `boot()` 不在主线程（实测 `at=None`）。
/// 代价是不跟随鼠标所在屏——多屏时面板总在主屏（nsh 也拿不到可靠的鼠标位置）。
#[cfg(target_os = "macos")]
pub fn panel_origin(width: f32, height: f32, position: Position) -> Option<(f32, f32)> {
    use core_graphics::display::CGDisplay;

    let b = CGDisplay::main().bounds();
    // 底部定位给 Dock 让位（CGDisplayBounds 是整屏，不扣 Dock）。
    let h = match position {
        Position::Bottom => (b.size.height - 90.0).max(0.0),
        Position::Center => b.size.height,
    };
    Some(place(
        b.origin.x,
        b.origin.y,
        b.size.width,
        h,
        width,
        height,
        position,
    ))
}

/// Windows：`GetCursorPos` + 该点所在显示器的工作区（已扣掉任务栏）。
///
/// **坐标单位**：Win32 的 `GetCursorPos` / `rcWork` 都是**物理像素**，而 nsh 的
/// `WindowRef::set_bounds` 收的是**逻辑点**（内部再乘 DPI/96 换算）。所以这里必须
/// 按该显示器的 scale 除一遍，否则 125%/150% 缩放下面板会跑偏。
#[cfg(target_os = "windows")]
pub fn panel_origin(width: f32, height: f32, position: Position) -> Option<(f32, f32)> {
    use windows_sys::Win32::Foundation::POINT;
    use windows_sys::Win32::Graphics::Gdi::{
        GetMonitorInfoW, MONITOR_DEFAULTTONEAREST, MONITORINFO, MonitorFromPoint,
    };
    use windows_sys::Win32::UI::HiDpi::{GetDpiForMonitor, MDT_EFFECTIVE_DPI};
    use windows_sys::Win32::UI::WindowsAndMessaging::GetCursorPos;

    let mut p = POINT { x: 0, y: 0 };
    // SAFETY: 只写 out 参数。
    if unsafe { GetCursorPos(&mut p) } == 0 {
        return None;
    }
    // SAFETY: 句柄与结构体都是值语义，cbSize 按文档填好。
    unsafe {
        let monitor = MonitorFromPoint(p, MONITOR_DEFAULTTONEAREST);
        let mut info: MONITORINFO = std::mem::zeroed();
        info.cbSize = std::mem::size_of::<MONITORINFO>() as u32;
        if GetMonitorInfoW(monitor, &mut info) == 0 {
            return None;
        }
        // 拿这块屏的 DPI；失败就按 100% 算，不至于摆错位置。
        let (mut dx, mut dy) = (96u32, 96u32);
        let _ = GetDpiForMonitor(monitor, MDT_EFFECTIVE_DPI, &mut dx, &mut dy);
        let scale = if dx == 0 { 1.0 } else { f64::from(dx) / 96.0 };

        let r = info.rcWork;
        let (x, y, w, h) = (
            f64::from(r.left) / scale,
            f64::from(r.top) / scale,
            f64::from(r.right - r.left) / scale,
            f64::from(r.bottom - r.top) / scale,
        );
        Some(place(x, y, w, h, width, height, position))
    }
}

#[cfg(not(any(target_os = "macos", target_os = "windows")))]
pub fn panel_origin(_width: f32, _height: f32, _position: Position) -> Option<(f32, f32)> {
    None
}

/// 在 `(x, y, w, h)` 这块屏幕工作区里放一个 `width × height` 的框。
fn place(
    x: f64,
    y: f64,
    w: f64,
    h: f64,
    width: f32,
    height: f32,
    position: Position,
) -> (f32, f32) {
    match position {
        Position::Center => center(x, y, w, h, width, height),
        Position::Bottom => {
            let (width, height) = (f64::from(width), f64::from(height));
            let margin = 24.0;
            let cx = x + ((w - width) / 2.0).max(0.0);
            let cy = y + (h - height - margin).max(0.0);
            (
                cx.clamp(x, (x + w - width).max(x)) as f32,
                cy.clamp(y, (y + h - height).max(y)) as f32,
            )
        }
    }
}

/// 在 `(x, y, w, h)` 这块屏幕里居中放一个 `width × height` 的框，不超出边界。
fn center(x: f64, y: f64, w: f64, h: f64, width: f32, height: f32) -> (f32, f32) {
    let (width, height) = (f64::from(width), f64::from(height));
    let cx = x + ((w - width) / 2.0).max(0.0);
    let cy = y + ((h - height) / 2.0).max(0.0);
    // 屏幕比窗口还小的时候（缩放、竖屏）至少贴着左上角，别跑到屏幕外。
    (
        cx.clamp(x, (x + w - width).max(x)) as f32,
        cy.clamp(y, (y + h - height).max(y)) as f32,
    )
}

#[cfg(test)]
mod tests {
    use super::{Position, center, place};

    #[test]
    fn centers_in_the_screen() {
        // 主屏 1440×900 放 480×600
        let (x, y) = center(0.0, 0.0, 1440.0, 900.0, 480.0, 600.0);
        assert_eq!((x, y), (480.0, 150.0));
    }

    #[test]
    fn works_for_screens_left_of_the_primary() {
        // 副屏在主屏左边：原点是负数
        let (x, y) = center(-1920.0, 0.0, 1920.0, 1080.0, 480.0, 600.0);
        assert_eq!((x, y), (-1920.0 + 720.0, 240.0));
    }

    #[test]
    fn bottom_position_sits_near_the_dock() {
        let (x, y) = place(0.0, 0.0, 1440.0, 900.0, 480.0, 560.0, Position::Bottom);
        assert_eq!(x, 480.0, "底部也是水平居中");
        assert_eq!(y, 900.0 - 560.0 - 24.0);
    }

    #[test]
    fn stays_inside_a_small_screen() {
        // 屏幕比面板还矮：贴顶，不跑出去
        let (x, y) = center(0.0, 0.0, 1200.0, 400.0, 480.0, 600.0);
        assert_eq!((x, y), (360.0, 0.0));
    }
}
