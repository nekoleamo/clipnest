# Windows 侧说明

> 结论（2026-11-19，v1.3.5）：**Windows 端已可在真机日常使用**。
>
> 这里原来是一张 28 行的「逐项对照 + 验证状态」表。整体可用之后，那张表的维护成本高过收益
> （每一行都得单独跑一次、单独记一次，而实际结论只有「能用」），2026-11-19 删除 ——
> 需要旧表看本文件的上一个版本（`git log -p -- docs/windows-能力对照.md`）。
> 保留下来的是：**为什么两端行为等价**、**出问题先查哪几处**、**已知缺口**。

## 一、为什么等价

两端是同一份 Go 代码，平台差异只在三处适配点，都已按平台分支实现：

| 适配点 | macOS | Windows |
| --- | --- | --- |
| 剪贴板读写 | `mygo.Clipboard`（TIFF → PNG） | 同一套（`CF_DIB`/`CF_DIBV5` → PNG、`CF_HDROP`） |
| 粘贴注入 | `paste.SendCmdV`：`CGEvent` 合成 ⌘V | 同一函数名：`SendInput` 合成 Ctrl+V |
| 前台应用身份 | bundle id（`NSWorkspace` / `lsappinfo`） | 进程名（`GetForegroundWindow` + `GetWindowThreadProcessId`） |

纯逻辑部分与平台无关，不需要在 Windows 上重验：数据层与迁移、`blake3` 去重、FTS5/LIKE 搜索、
分页与滚动续页、纯文本降级、取消成组的批量删除、日志轮转、键位文案（`ModPrefix()` 按 `runtime.GOOS` 切换）。

## 二、出问题先查这几处

这几项依赖 Windows 自己的行为，**没有逐项验证记录**，症状对得上再往深里查：

| 症状 | 先看 |
| --- | --- |
| Ctrl+Shift+V 没反应 | 全局快捷键是否被别的软件占用（`mygo.GlobalShortcut` 失败会返回错误并记日志） |
| 面板弹出但打不了字 | `paste.ClaimForeground()`：`SetForegroundWindow` 被前台锁拒绝时只记日志（见第三节第 2 条） |
| 回车只复制不粘贴 | 面板底栏/日志里的 `SendInput` 结果；或目标程序以更高完整性级别运行（管理员窗口） |
| 图片贴出来是空白 | DIB → PNG 那条路径；`clipnest.log` 里找 `capture` 相关行 |
| 界面偏大/偏小 | 200% 缩放下的 DIP 换算（见第三节第 3 条） |
| 托盘图标不在、或退出后残留 | 托盘与 `App.Quit()` 的时序（macOS 那套「先 `tray.Destroy()` 再延迟 `Quit`」两端共用） |

低层探针：`tools/win-smoke.ps1`（剪贴板格式、DPI、前台窗口这些平台能力的最小验证）。

## 三、已知缺口

1. **RTF 条目在 Windows 上不会产生**：`mygo` 没映射 Windows 的 `Rich Text Format`（只注册了
   `HTML Format`、`PNG`），所以 `clip.Capture` 拿不到 `text/rtf`。影响：Windows 复制富文本只会落
   HTML/纯文本条目；拿到 macOS 同步过来的 RTF 条目仍可粘贴（写回时带 `text/plain` 兜底）。
   要补只能在框架侧注册自定义格式，暂不做。
2. **前台锁可能被拒绝**：`SetForegroundWindow` 失败时面板收不到键盘，代码只记日志并返回 false，
   不重试也不弹窗。
3. **高 DPI**：位置与尺寸都交给 mygo 的 DIP 空间，理论上是框架的事；200% 缩放没有专门的验证记录。

## 四、要复核时的最短路径

1. `GOOS=windows go build ./...`（macOS 上即可，产物在 `dist/windows-*/`）
2. 双击 `clipnest.exe`，托盘出现 `❐`
3. `Ctrl+Shift+V` 唤出面板，直接在输入框里打字
4. 复制一段文本 / 一张截图 → 面板出现条目；再复制同一条 → 不新增行、`×N` +1
5. 回车粘贴进记事本；Esc 收起后前台回到原程序
6. 设置窗打开 → 改一项 → 保存 → 重启后配置还在
7. 装一次开机自启、再卸掉
8. 两台机（macOS + Windows）同步一轮：新增、置顶、图片各改一次，看对端是否都到位

前面 3–5 项是「能用」的底线，6–8 项是设置窗与同步这两块容易只在单端测到的地方。
