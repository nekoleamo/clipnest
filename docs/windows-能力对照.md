# Windows 能力对照（P5）

> 目的：P5 不是「再写一遍」，而是逐项确认 **macOS 上的每个行为在 Windows 上有等价实现**。
> 每行标明实现位置与验证状态；只有真机验证过的才算 ✅，其余一律 ⚠️。
>
> 上次冒烟（2026-11-15）已确认：窗口能开、面板不抢焦点、键盘可输入、剪贴板采集与去重、
> Ctrl+V 注入、托盘与菜单。下表把「已确认」与「只是代码写好」分开列。

## 一、逐项对照

| # | 能力 | macOS | Windows | 等价性 | 状态 |
| --- | --- | --- | --- | --- | --- |
| 1 | 唤出面板 | `register_global_shortcut("mod+shift+v")`（⌘⇧V） | 同一调用（Ctrl+Shift+V），nsh 用 `RegisterHotKey` | 等价 | ✅ 冒烟 |
| 2 | 面板窗口 | `WindowKind::Popup`（NSPanel + `NonactivatingPanel`） | 同 `WindowKind::Popup`（`WS_EX_TOOLWINDOW` + 置顶层） | 等价 | ✅ 冒烟 |
| 3 | 面板收键盘 | `activate=true` + 主线程 `focus()`（`makeKeyAndOrderFront`） | `activate` 只做 `ShowWindow(SW_SHOW)`，**不保证前台**；补了 `clipboard::claim_foreground()`（枚举本进程窗口 → `SetForegroundWindow`） | 机制不同、目标一致 | ⚠️ 待真机 |
| 4 | Esc / ↑↓ / 动作键 | `on_key_with(When::Always)` | 同一套 keymap | 等价 | ⚠️ 待真机 |
| 5 | 文本采集 | `clipboard-rs` + `changeCount` 轮询 | `clipboard-rs` + `GetClipboardSequenceNumber` | 等价 | ✅ 冒烟 |
| 6 | 文件采集 | `get_files` → 换行路径 + `d`/`f` 串 | 同一实现（`CF_HDROP`） | 等价 | ✅ 冒烟 |
| 7 | 图片采集 | `NSPasteboard` PNG，只有 TIFF 时用 `NSBitmapImageRep` 重编码 | `clipboard-rs`：`CF_PNG` → `CF_DIBV5` → `CF_DIB`（DIB 走 BmpDecoder，依赖 `image` 的 `bmp` feature，**已给它加上**） | 等价（路径不同） | ⚠️ 待真机 |
| 8 | 回环抑制 | 写回后登记 `changeCount` | 写回后登记剪贴板序列号 | 等价 | ✅ 冒烟 |
| 9 | 粘贴注入 | `CGEvent` ⌘V | `SendInput`（Ctrl↓ V↓ V↑ Ctrl↑） | 等价 | ✅ 冒烟 |
| 10 | 贴回原应用 | `NSRunningApplication::activate` | `SetForegroundWindow`（先 `IsWindow` 校验；可能被前台锁拒） | 等价（有系统限制） | ✅ 冒烟 / ⚠️ 前台锁未压测 |
| 11 | 托盘 | `set_tray`（菜单栏 `❐`） | 同 `set_tray`（`Shell_NotifyIcon`） | 等价 | ✅ 冒烟 |
| 12 | 设置窗 | `WindowKind::Dialog` | 同 | 等价 | ⚠️ 待真机 |
| 13 | 开机自启 | `~/Library/LaunchAgents/com.example.clipnest.plist` | `HKCU\Software\Microsoft\Windows\CurrentVersion\Run` | 目标一致 | ⚠️ 待真机 |
| 14 | 敏感应用过滤 | bundle id（`com.1password.1password`） | 进程名（`KeePassXC.exe` → 去 `.exe` 比对） | 规则同、标识不同 | ⚠️ 待真机 |
| 15 | 多显示器定位 | `NSScreen` + `NSEvent.mouseLocation`（左下→左上原点） | `GetCursorPos` + `MonitorFromPoint` + **`GetDpiForMonitor` 除 scale**（Win32 是物理像素，nsh 的 `set_bounds` 要逻辑点） | 已按平台各自换算 | ⚠️ 待真机（高 DPI 尤其） |
| 16 | 图片存储 | `images/{origin,thumbnails}/<hh>/<hash>.png` | 同（纯文件） | 等价 | 🔵 平台无关 |
| 17 | 导出 | `⌘O` 写文件 + `set_files` | 同（`set_files` 走 `CF_HDROP`） | 等价 | ⚠️ 待真机 |
| 18 | 同步 | WebDAV（reqwest） | 同 | 等价 | 🔵 平台无关 |
| 19 | 单实例 | macOS 由 Launch Services 保证 | `nsh.json` `"singleInstance": true`（命名互斥量 + `WM_COPYDATA`） | 目标一致 | ⚠️ 待真机（本次新加） |

## 二、这一轮补的四处

1. **高 DPI 坐标**（第 15 项）：Win32 的 `GetCursorPos`/`rcWork` 是物理像素，nsh 的 `set_bounds`
   收逻辑点并在内部乘 `DPI/96`。之前直接把物理像素当逻辑点传，125%/150% 缩放下位置会偏。
   现在用 `GetDpiForMonitor(MDT_EFFECTIVE_DPI)` 除一遍。
2. **抢前台兜底**（第 3 项）：Windows 上「收键盘 = 必须是前台窗口」，nsh 的 `activate` 只
   `ShowWindow(SW_SHOW)`。`focus_when_ready` 失败时会调 `claim_foreground()`（`EnumWindows`
   找本进程可见顶层窗口 → `SetForegroundWindow`）。
3. **图片解码依赖**（第 7 项）：`clipboard-rs` 在 Windows 上对 `CF_DIB`/`CF_DIBV5` 用 `BmpDecoder`，
   而 `image` 的 `bmp` feature 之前没开（我们只开了 `png`/`tiff`）——现在开了，否则截图工具
   （Win+Shift+S 写 `CF_DIB`）采到的图片会解码失败。
4. **单实例**（第 19 项）：`nsh.json` 加 `"singleInstance": true`，避免双开两个托盘图标、
   两个进程同时写库。

## 三、真机上要重点看的

- **第 3 项**：面板打开后直接敲字能不能进搜索框。若日志出现
  `PANEL 抢前台失败，重试 N` 或 `FOREGROUND claim hwnd=… ok=false`，说明被前台锁拦了，
  需要 `AttachThreadInput` 那套绕过（还没写）。
- **第 7 项**：用 Win+Shift+S 截图后看日志有没有 `DB INSERTED kind=image`，
  以及 `images/origin/` 下有没有文件。若解密失败会打 `IMAGE` 相关错误。
- **第 15 项**：在 125%/150% 缩放的屏幕上按快捷键，面板应该出现在鼠标所在屏的正中；
  偏了就是 scale 换算还不对（记录实际 bounds 与期望值）。
- **第 19 项**：连点两次 `clipnest.exe`，应只有一份进程与一个托盘图标。

## 四、怎么复跑

```bash
# macOS 侧（交叉编译 + clippy）
source "$HOME/.cargo/env"
cargo xwin check  --target x86_64-pc-windows-msvc --all-targets
cargo xwin clippy --target x86_64-pc-windows-msvc --all-targets
nsh package --target windows        # → .nsh/out/clipnest.zip

# Windows 侧
# 解压后双击 clipnest.exe；日志在 %TEMP%\clipnest.log
powershell -ExecutionPolicy Bypass -File tools\win-smoke.ps1 -Action windows
```
