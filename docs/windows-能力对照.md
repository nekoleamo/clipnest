# Windows 能力对照（Go + mygo 版）

> 目的：逐项确认 **macOS 上的每个行为在 Windows 上有等价实现**。
> 只有真机验证过的才算 ✅，代码写好但没跑过的一律 ⚠️。
> 代码位置都指向 Go 实现；旧 Rust 版对照见 `legacy-rust/src/`。

## 一、逐项对照

| # | 能力 | macOS（Go 实现） | Windows（Go 实现） | 状态 |
| --- | --- | --- | --- | --- |
| 1 | 唤出面板 | `mygo.GlobalShortcut.Register("CmdOrCtrl+Shift+V")` → `App.TogglePanel` | 同一调用（Ctrl+Shift+V） | 🔵 代码一致，⚠️ 待真机 |
| 2 | 面板窗口 | `WindowOptions{Panel: true}` = `NSPanel` 非激活样式 | 同一选项 = `WS_EX_TOOLWINDOW` + topmost 工具窗（框架实现） | 🔵 代码一致，⚠️ 待真机 |
| 3 | 面板收键盘 | NSPanel 自成为 key window，app 保持不激活 | `paste.ClaimForeground()`（`EnumWindows` 找本进程可见顶层窗 → `SetForegroundWindow`），可能被前台锁拒绝 | ⚠️ 待真机 |
| 4 | Esc / ↑↓ / 动作键 | `c.OnWindowShortcut(...)`；Esc / 方向键 / ⌘D ⌘P ⌘S ⌘E ⌘Y ⌘O / ⌘1-9 | 同一套（`ui.Cmd` 在 Windows 是 Ctrl） | 🔵 代码一致，⚠️ 待真机 |
| 5 | 文本采集 | `mygo.Clipboard.OnChanged` + `Formats()` + `ReadFormat` | 同一套（框架用原生剪贴板通知） | ✅ macOS 实测，⚠️ 待真机 |
| 6 | 文件采集 | `mygo.Clipboard.ReadFiles()` → 换行路径 + `d`/`f` 串 | 同一套（`CF_HDROP`） | ✅ macOS 实测，⚠️ 待真机 |
| 7 | 图片采集 | `ReadFormat(image/png)`（框架内部把 TIFF 转 PNG） | 同一套（框架内部把 `CF_DIB`/`CF_DIBV5` 转 PNG） | ✅ macOS 实测，⚠️ 待真机 |
| 8 | 回环抑制 | `clip.Guard`：写回前登记内容哈希，采集到同哈希即跳过（契约 10） | 同一套（纯逻辑，与平台无关） | 🔵 平台无关 |
| 9 | 粘贴注入 | `paste.SendCmdV()`：purego 调 `CGEvent`（⌘V） | `paste.SendCmdV()`：`SendInput`（Ctrl↓ V↓ V↑ Ctrl↑） | ✅ macOS 实测，⚠️ 待真机 |
| 10 | 贴回原应用 | `paste.ReturnToPreviousApp()`：`NSRunningApplication.activateWithOptions` | 同一函数：`IsWindow` + `SetForegroundWindow` | ⚠️ 前台锁未压测 |
| 11 | 托盘 | `mygo.NewTray`（`❐` 模板图标 + 菜单） | 同一调用（`Shell_NotifyIcon`） | ✅ macOS 实测，⚠️ 待真机 |
| 12 | 设置窗 | `mygo.NewWindow{Title: "clipnest 设置"}` | 同一套 | 🔵 代码一致，⚠️ 待真机 |
| 13 | 开机自启 | `mygo.App.SetOpenAtLogin(true)`（登录项 / LaunchAgent） | 同一调用（`HKCU\...\Run`） | ⚠️ 待真机 |
| 14 | 敏感应用过滤 | `sensitive.FrontApp()`（purego 问 `NSWorkspace`，退 `lsappinfo`）→ bundle id | `sensitive.FrontApp()`（`GetForegroundWindow` + `QueryFullProcessImageNameW`）→ 进程名 | ✅ macOS 实测，⚠️ 待真机 |
| 15 | 多显示器定位 | `mygo.Screen.CursorScreenPoint()` + `DisplayNearestPoint()` + `SetPosition`（运行时随时改） | 同一套（框架按 DPI 换算 DIP，`MonitorFromRect`） | ✅ macOS 实测，⚠️ 待真机（高 DPI） |
| 16 | 图片存储 | `images/{origin,thumbnails}/<hh>/<hash>.png`（blake3 命名） | 同（纯文件操作） | 🔵 平台无关 |
| 17 | 导出 | `⌘O` 写文件 + `mygo.Clipboard.WriteFiles()` | 同一套（`CF_HDROP`） | ⚠️ 待真机 |
| 18 | 同步 | `internal/sync`：WebDAV（`net/http` 自定义 PROPFIND/MKCOL/PUT）+ 端到端加密 | 同一套（纯 Go HTTP） | 🔵 平台无关 |
| 19 | 单实例 | `mygo.json` 未配单实例；旧实现用 nsh `singleInstance` | ⚠️ **尚未实现**（见下） | ❌ 待做 |

## 二、已知缺口

1. **单实例**：Go 版还没做。方案：`mygo` 的 `SingleInstance` API（`mygo.SingleInstance` 存在）或自建命名锁（Windows 命名互斥量 / macOS flock）。真机冒烟前必须补，否则双开会出现两个托盘图标。
2. **Windows 前台锁**：`SetForegroundWindow` 被系统拒绝时面板收不到键盘，代码只记日志（返回 false）。真机需要专项测。
3. **高 DPI**：位置与尺寸都交给 mygo 的 DIP 空间，理论上不用自己除 scale；真机 200% 缩放下要复测。

## 三、真机冒烟清单（沿用 `windows-冒烟清单.md` 的流程）

1. `GOOS=windows go build ./...` 通过（macOS 上已通过）
2. 双击 `dist/windows-*/clipnest.exe`，托盘出现 `❐`
3. Ctrl+Shift+V 唤出面板，能直接打字
4. 复制一段文本/一张截图 → 面板出现条目；再复制同一条 → 不新增行、`use_count` +1
5. 回车粘贴到记事本；Esc 收起后前台回到原应用
6. 设置窗打开、保存、重启后配置还在
7. 装/卸开机自启
