//! 鼠标所在显示器：多屏时把面板摆到用户正在看的那块屏上。
//!
//! 不用 `nsh_core::element::desktop::displays()`：它读 nsh 的响应式环境，
//! 在 `run_on_main_thread` 的回调里调用会 panic（实测
//! `no reactive runtime: signals are used on the engine thread`）。
//! 这里直接问系统要屏幕区域，只算窗口坐标，再交给 `WindowRef::set_bounds`。
//!
//! 坐标约定：nsh 的屏幕坐标是**左上原点、y 向下**；
//! macOS 的 `NSScreen.frame` 是左下原点、y 向上，需要按主屏高度翻转。

/// 面板在鼠标所在屏幕上的居中位置（左上角，nsh 坐标）。拿不到就返回 `None`。
#[cfg(target_os = "macos")]
pub fn panel_origin(width: f32, height: f32) -> Option<(f32, f32)> {
    use objc2::MainThreadMarker;
    use objc2_app_kit::{NSEvent, NSScreen};

    let mtm = MainThreadMarker::new()?;
    let main = NSScreen::mainScreen(mtm)?;
    let main_height = main.frame().size.height;
    let mouse = NSEvent::mouseLocation();

    let screens = NSScreen::screens(mtm);
    let target = screens
        .iter()
        .find(|s| {
            let f = s.frame();
            mouse.x >= f.origin.x
                && mouse.x < f.origin.x + f.size.width
                && mouse.y >= f.origin.y
                && mouse.y < f.origin.y + f.size.height
        })
        .unwrap_or(main);

    let f = target.frame();
    // 左下原点 → 左上原点
    let top = main_height - (f.origin.y + f.size.height);
    Some(center(f.origin.x, top, f.size.width, f.size.height, width, height))
}

/// Windows：`GetCursorPos` + 该点所在显示器的工作区（已扣掉任务栏）。
#[cfg(target_os = "windows")]
pub fn panel_origin(width: f32, height: f32) -> Option<(f32, f32)> {
    use windows_sys::Win32::Foundation::POINT;
    use windows_sys::Win32::Graphics::Gdi::{
        GetMonitorInfoW, MONITOR_DEFAULTTONEAREST, MONITORINFO, MonitorFromPoint,
    };
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
        let r = info.rcWork;
        Some(center(
            f64::from(r.left),
            f64::from(r.top),
            f64::from(r.right - r.left),
            f64::from(r.bottom - r.top),
            width,
            height,
        ))
    }
}

#[cfg(not(any(target_os = "macos", target_os = "windows")))]
pub fn panel_origin(_width: f32, _height: f32) -> Option<(f32, f32)> {
    None
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
    use super::center;

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
    fn stays_inside_a_small_screen() {
        // 屏幕比面板还矮：贴顶，不跑出去
        let (x, y) = center(0.0, 0.0, 1200.0, 400.0, 480.0, 600.0);
        assert_eq!((x, y), (360.0, 0.0));
    }
}
