# Windows 能力对照（Go + mygo 版）

> 目的：逐项确认 **macOS 上的每个行为在 Windows 上有等价实现**。
> 只有真机验证过的才算 ✅，代码写好但没跑过的一律 ⚠️。
> 代码位置都指向 Go 实现；旧 Rust 版已删除（git 历史 `7284efe` 之前）。

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
| 18 | 同步 | `internal/sync`：WebDAV（`net/http` 自定义 PROPFIND/MKCOL/PUT）+ 端到端加密；增量按 `updated_at`/`meta_at` 双时间戳 | 同一套（纯 Go HTTP） | 🔵 平台无关 |
| 18b | 口令存储 | macOS 钥匙串（按 App 的 ACL，重装后 cdhash 变了会弹一次授权） | `mygo.Secrets` → Windows 凭据管理器里的通用凭据（`clipnest-sync/<设备名>`），DPAPI 按用户加密，**读不弹框**；作用域是「本用户」而不是「本 App」 | 🔵 代码一致，⚠️ 待真机（域名账号下凭据会随漫游配置走） |
| 19 | 单实例 | `mygo.App.RequestSingleInstanceLock()` + `OnSecondInstance` 唤出面板 | 同一套（框架用命名 socket/互斥量，第二实例转发 argv 后退出） | 🔵 代码一致，⚠️ 待真机 |

| 20a | 详情窗（大图/全文） | `ShowDetail`：图片等比放大、长文本可滚，Esc/点击关闭 | 同一套（`mygo.Window` + `ui.Scroll`） | 🔵 平台无关 |
| 20 | 列表分页 | 一页 200 条、滚到底续页、顶栏显示真实总数 | 同一套（`db.CountQuery` + `ui.ScrollState`，与平台无关） | 🔵 平台无关 |
| 21 | 键盘导航 | `↑↓←→`、`PgUp/PgDn` 翻页、`⌘F` 回搜索框、`⌥⏎` 纯文本 | 同一套（`ui.Cmd` 在 Windows 是 Ctrl，`ui.Alt` 是 Alt） | 🔵 代码一致，⚠️ 待真机（Alt+⏎ 会不会被输入框吃掉） |
| 22 | 暂停记录 | 托盘勾选「暂停记录」，暂停期间不读剪贴板 | 同一套（`Shell_NotifyIcon` 菜单勾选项） | 🔵 代码一致，⚠️ 待真机 |
| 23 | 粘贴为纯文本 | `clip.PlainText`：HTML 剥标签、RTF 去壳、文件给路径 | 同一套（纯逻辑，与平台无关） | 🔵 平台无关 |
| 24 | 批量删除 | `⌘/Ctrl` 点击勾选、右键全选、`⌘D` 删勾选 | 同一套（`ClickModifiers()&ui.Cmd` 在 Windows 就是 Ctrl） | 🔵 代码一致，⚠️ 待真机 |
| 25 | 导出/备份对话框 | `mygo.Dialog.Save/Open`（文本选文件、文件条目选目录、备份选 `.cnbak`） | 同一套（`IFileDialog`） | 🔵 代码一致，⚠️ 待真机 |
| 26 | 日志 | `<数据根>/clipnest.log`，运行期 2MB 轮转成 `.1` | 同一套（纯文件操作） | 🔵 平台无关 |
| 27 | 托盘菜单项 | 辅助功能授权两项**只在 macOS 出现**（`paste.NeedsTrust()`） | 不显示，其余一致 | 🔵 代码一致 |
| 28 | 键位文案 | 底栏/状态里的命令键走 `ModPrefix()`：⌘ / Ctrl+ | 同一函数按 `runtime.GOOS` 切换 | 🔵 平台无关 |

## 二、已知缺口

1. **RTF 条目在 Windows 上不会产生**：`mygo` 的剪贴板没有映射 Windows 的 `Rich Text Format`（只注册了 `HTML Format`、`PNG`），所以 `clip.Capture` 拿不到 `text/rtf`。影响：Windows 复制富文本只会落 HTML/纯文本条目；拿到 macOS 同步过来的 RTF 条目仍可粘贴（写回时带 `text/plain` 兜底）。要补只能在框架侧注册自定义格式，暂不做。
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
