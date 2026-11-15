# Windows 冒烟清单（P2 之后的插入点）

> 目的：只用最少的事确认「nsh 的 Windows 后端能不能撑起 clipnest」，避免 P4 同步投入建立在错误假设上（路线图 R1）。
> 结论分三档：**通过**（继续做 P5）、**部分通过**（记录缺口，能绕就绕）、**不成立**（换壳或放弃双端）。
>
> 前置：**编译和打包可以在 macOS 上完成**（2026-11-15 实测，见下），只有运行验证需要 Windows 真机。
>
> ## 交叉编译已打通（2026-11-15 实测）
>
> 环境：rustup（brew rust 已卸载）+ `rustup target add x86_64-pc-windows-msvc` + `cargo install cargo-xwin`。
>
> | 步骤 | 结果 |
> | --- | --- |
> | `cargo xwin check --target x86_64-pc-windows-msvc` | ✅ 通过（全仓，含 nsh-platform-windows、clipboard-rs、rusqlite bundled） |
> | `cargo xwin build --release --target x86_64-pc-windows-msvc` | ✅ 1m48s，产物 `target/x86_64-pc-windows-msvc/release/nsh-app-clipnest.exe` 43 MB，PE32+ GUI x86-64 |
> | `nsh package --target windows` | ✅ 2m5s，产出 `.nsh/out/clipnest.zip`（20 MB）与 `.nsh/out/clipnest/`（exe + `nsh/app.json`） |
> | `clipnest.iss`（Inno Setup 脚本） | ⚠️ 已生成但未打安装包 —— 需要 Windows 侧 `winget install JRSoftware.InnoSetup` |
>
> 导入表实测（`llvm-objdump -p`）：`d2d1.dll`、`dwrite.dll`、`d3d11.dll`、`dxgi.dll`、`dcomp.dll`、`dwmapi.dll`、`comctl32.dll`、`bcrypt.dll`、`api-ms-win-crt-*`
> → 证实 nsh 的 Windows 后端确实是 Win32 + Direct2D 路线，且 UCRT 走系统自带（Win10+ 无需装 VC 运行库）。
>
> **所以冒烟的最短路径**：在 Mac 上 `nsh package --target windows`，把 `.nsh/out/clipnest.zip` 拷到 Windows 解压双击，**不需要在 Windows 上装 Rust/nsh/VS**。
>
> 交叉编译**无法**验证的（必须真机）：窗口是否抢焦、Popup 层级、`RegisterHotKey` 是否触发、剪贴板事件、`SendInput` 粘贴、托盘图标、多显示器定位。

## 0. 必须先补的代码（当前只有 macOS 分支）

`src/clipboard.rs` 里这些函数目前只有 macOS 实现，Windows 分支要么是空壳要么返回假值：

| 函数 | 现状 | Windows 要换成 |
| --- | --- | --- |
| `change_count()` | 返回 0（watcher 永远不触发） | `GetClipboardSequenceNumber()`（user32） |
| `post_cmd_v()` | 返回 false | `SendInput` 发 Ctrl+V（两个 KEYEVENTF 事件） |
| `ax_trusted()` | false | Windows 无此概念，返回 true |
| `remember_front_app()` / `return_to_previous_app()` | 空实现 | `GetForegroundWindow()` + `SetForegroundWindow()` |
| `key_window_state()` | `"<n/a>"` | 可留空（只是诊断） |

依赖：`[target.'cfg(windows)'.dependencies] windows-sys = { version = "0.59", features = ["Win32_Foundation", "Win32_UI_WindowsAndMessaging", "Win32_UI_Input_KeyboardAndMouse", "Win32_System_DataExchange", "Win32_System_Memory"] }`

补完先在本机跑 `cargo check`（Windows 上），再进下面的冒烟。

## 1. 打包与启动

**已在 macOS 上完成**，只需把产物拷过去：

```bash
# macOS 侧
nsh package --target windows
# → .nsh/out/clipnest.zip（20 MB）
```

```powershell
# Windows 侧：解压后双击 clipnest.exe
Expand-Archive clipnest.zip -DestinationPath clipnest
.\clipnest\clipnest.exe
```

（若要在 Windows 上自行构建：`winget install Rustlang.Rustup` + `irm https://nsh.dev/install.ps1 | iex` + VS Build Tools 的 VCTools 工作负载。）

**判定**：能出可执行文件、双击不闪退。
**已知**：`AppOptions` 在 Windows 后端缺 `keep_alive_on_close` / `activation_policy` / `background` 三个字段（macOS 独有），关窗即退与「菜单栏常驻」形态在 Windows 上需要另外确认。
**失败信号**：链接错误缺 `windows-sys` / `nsh-platform-windows` 拉不下来 / 启动即退出（看事件查看器或控制台重定向）。

## 2. 窗口：Popup 能不能开、是不是不抢焦点

```powershell
# 先开一个记事本当前台，再按 Ctrl+Shift+V（nsh 的全局热键在 Windows 上是 Carbon 的 RegisterHotKey 等价物）
notepad.exe
# 另一窗口执行：tools\win-smoke.ps1 -Action windows
```

**判定**：
- 面板窗口出现，且前台进程**仍是 notepad**（`win-smoke.ps1` 会打印）
- 面板窗口不是置顶覆盖整个屏幕，尺寸接近 480×600
- 按 Esc 能收起，且前台回到 notepad

**失败信号**：前台变成 clipnest（说明 `activate` 抢了焦点，粘贴链路要改成「记录原窗口 + SetForegroundWindow 切回」，macOS 那套逻辑可以复用）、面板尺寸错乱、Esc 无效。

## 3. 键盘：面板里能不能打字、↑↓ 能不能选

面板打开后直接敲 `abc`。

**判定**：搜索框出现 `abc`，列表被过滤；↑↓ 能改选中项。
**失败信号**：字符进了 notepad（= 窗口拿不到键盘焦点，这是 macOS 上踩过的同一个坑）。

## 4. 剪贴板：采集与去重

```powershell
# 连续复制同一段文本三次
"clipnest 冒烟 A" | Set-Clipboard; Start-Sleep -Milliseconds 400
"clipnest 冒烟 A" | Set-Clipboard; Start-Sleep -Milliseconds 400
"clipnest 冒烟 A" | Set-Clipboard
```

**判定**：`%TEMP%\clipnest.log`（或控制台日志）出现 1 次 `DB INSERTED` + 2 次 `DB DEDUPED`，库里 1 行 `use_count=3`。
**失败信号**：`WM_CLIPBOARDUPDATE` 没监听（change_count 恒 0）、或文件/图片 flavor 读不到。

## 5. 粘贴：Ctrl+V 注入

1. 记事本里按 Ctrl+Shift+V 打开面板
2. ↑↓ 选中「clipnest 冒烟 A」
3. Enter

**判定**：文本出现在记事本里，日志有 `PASTE id=… → FRONT activate → AUTOPASTE`。
**失败信号**：`SetForegroundWindow` 被系统拒绝（Windows 的前台锁）、SendInput 被 UIPI 拦（以管理员身份运行的目标窗口）。

## 6. 托盘

**判定**：通知区出现图标，右键菜单三项都在（显示面板 / 偏好设置… / 退出），左键点击能切换面板。
**失败信号**：`Shell_NotifyIcon` 没生效、菜单项回调不触发。

## 7. 记录结果

把下面这段填好，追加到 `docs/2026-11-15-nsh-验证记录.md` 的第十二节（或新建 Windows 文档）：

```
环境：Windows 11 22631 / nsh 0.5.1 / clipnest <commit>
打包：ok / 失败（错误原文）
窗口：ok / 抢焦点 / 尺寸错乱
键盘：ok / 字符进前台应用
剪贴板：INSERTED-1 DEDUPED-2 / 监听失效
粘贴：ok / SetForegroundWindow 失败 / SendInput 被拦
托盘：ok / 无图标 / 菜单无效
结论：通过 / 部分通过（缺 XX）/ 不成立
```

## 附：app_id

`nsh.json` 里 `app_id` 现在是占位的 `com.example.clipnest`，Windows 上会影响任务栏分组与单实例；冒烟时如果出现「第二个实例又开一个托盘图标」，就是 `single_instance` 没配上，先改 app_id 再复测。
