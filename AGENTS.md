# clipnest 项目约定

> 新会话先读：`docs/路线图.md`（总纲）→ `docs/执行计划-下一步.md`（从哪继续）→ 本文（约定与坑）。
> `legacy-rust/` 是**已归档**的 nsh + Rust 实现（含全部 nsh 实测坑，见 `legacy-rust/AGENTS-历史坑.md`），只作逐行对照，不再改动、不再构建。

## 技术栈（不要偏离）

- **Go 1.27+**，模块名 `clipnest`，纯 Go **无 cgo**
- **mygo v0.9.1**（锁定，升级单独一轮并复跑 `docs/` 验证项）
- UI 走 **mygo `ui` 包纯 Go 自绘**：不用 WebView、不用 TSX、不引入 Node
- 存储 `modernc.org/sqlite`（FTS5 内置），同步 `net/http`（WebDAV），哈希 `blake3`
- 需要框架没暴露的平台 API（前台应用身份、`CGEvent` 合成 ⌘V）用 `purego`，不引入 cgo

## 构建

```bash
go build ./...                 # 编译
go test ./...                  # 测试
go vet ./...
GOOS=windows go build ./...    # Windows 交叉编译（纯 Go，无需 xwin）
go tool mygo build             # 打包（bundle / dmg / 安装包，需 mygo.config.ts）
```

## 目录规划

```
main.go         入口：App.Run + 装配
internal/paths   数据根目录（不吃 mygo 的 app data 路径）
internal/db      schema/迁移（只追加）、items、search（FTS5 + LIKE 降级）
internal/store   记录读写、去重、图片分片、GC
internal/clip    剪贴板采集（mygo Clipboard.OnChanged + Formats）
internal/sensitive 敏感应用判定（前台应用身份，purego）
internal/sync    WebDAV 增量同步 + 端到端加密
internal/backup  加密备份导入导出
ui/              面板（mygo ui 自绘）、设置窗、托盘菜单
tools/           验证脚本（Swift，非产品代码，nsh 时代留下的仍可用）
docs/            路线图、执行计划、验证记录
```

## 迁移铁律

1. `docs/路线图.md` 第四节的 **12 条行为契约**在 Go 侧必须逐条成立，改一条要重开评估
2. `internal/db` 的迁移脚本**只追加**，已发布脚本不可改
3. `content_hash = blake3("<kind>:<content>")`；去重命中只 `use_count + 1` 并刷新 `updated_at`
4. 搜索：长词走 FTS5，**trigram 查询词 <3 字符必须降级 `LIKE`**（2 个汉字命中不了）
5. 自身写回必须被 guard 抑制，不产生新记录
6. 图片优先于文本；`content` 为 PNG 文件名，文件按 `blake3` 前 2 位分片
7. 敏感过滤按**应用身份**（macOS bundle id / Windows 进程名）判定，不猜内容

## mygo 已确认的坑（探针实测，2026-11-16）

1. `go get` 后必须 `go mod tidy`，否则 `ui`/`internal/text` 的间接依赖缺 `go.sum` 条目编译失败
2. `Tray.Bounds()` 在 `NewTray` 后立即读是 `Height:0`，要等布局完成（点击/托盘弹出位置别用它算）
3. 面板位置在**运行时**可以随便改：`Screen.CursorScreenPoint()` + `Screen.DisplayNearestPoint()` + `panel.SetPosition()`，不存在 nsh 那种「只能在创建时定死」
4. `Panel: true` 的窗口在 macOS 是 `NSPanel` 非激活样式：抢键盘但不激活 app；`OnClose` 里必须 `PreventDefault` + `Hide`，否则窗口被销毁、下次唤醒要重建
5. `Clipboard.OnChanged` 在 macOS 是 250ms `changeCount` 轮询（不在复制瞬间触发），采集逻辑要容忍这个延迟
66. 应用是 accessory（无 Dock 图标）时要同时 `OnWindowAllClosed(func(){})`，否则关掉最后一个窗口就退出
7. **纯 Go 程序里 `objc_getClass("NSWorkspace")` 返回 0**：必须先 `purego.Dlopen(".../AppKit.framework/AppKit", RTLD_NOW|RTLD_GLOBAL)` 把框架真的载进进程（Foundation 同理），并把句柄保活；否则只能退到 `lsappinfo`
8. `purego.RegisterLibFunc` **不返回 error**，失败是 panic：包一层 `recover` 转 error，否则初始化阶段直接崩
9. 读 `NSString` 不要解引用 `UTF8String` 指针（uintptr→unsafe.Pointer 会被 go vet 判 unsafeptr）：用 `getCString:maxLength:encoding:` 让系统写进 Go 的缓冲区
10. `lsappinfo info -only bundleid` 的输出是 `CFBundleIdentifier"="com.x.y`，取 `=` 之后并去引号，别取首尾引号之间
11. **`WhenReady` 回调里调 `Window.Invalidate()` 会死锁**：该回调早于事件循环，向主线程投递的调用等不到回应。用 `App.SetReady()` 置位后才允许重画（`internal/app/app.go` 的 `invalidate`）
12. `Clipboard.OnChanged` 的回调可能并发投递 → 采集必须加锁（`App.Capture` 的 `captureMu`），否则两个回调同时入库会撞 `content_hash` 唯一键
13. 单实例靠 `mygo.App.RequestSingleInstanceLock()`（不返回则直接 `return`）+ `OnSecondInstance(func(args, cwd){ a.Show() })`
14. **面板 `OnClose` 里无条件 `PreventDefault` 会让 `App.Quit()` 静默失败**：`prepareQuit` 会关所有窗口，关不掉就取消退出。要在 `OnBeforeQuit` 里置「正在退出」标志，退出流程中跳过 PreventDefault
15. 打包：`go get -tool github.com/egoist/mygo/cmd/mygo` 后 `go tool mygo build`，配置在 `mygo.json`（native UI 项目不用 TS 配置）
16. **中文输入法下 `TextInput.Submitted()` 需要第二次回车**：合成期间 AppKit 把 `KeyPressed` 扣住（mygo `surface.keyDown` 的 `withheld` 分支），只有 `KeyUp` 到达，第一次 ⏎ 只提交候选。不输入直接 ⏎ 一次就粘；`⌘1-9` 带 Cmd 不受输入法影响。另外 `OnWindowShortcut(0, ui.KeyEnter)` 永远不会触发——焦点在输入框时 Enter 被编辑框优先吃掉，要判提交就用 `Submitted()`
17. `go tool mygo build` 每次重新 ad-hoc 签名，**辅助功能的 TCC 授权可能在换包后失效**；装完先看面板顶部有没有「没有辅助功能授权」提示条（带「去授权」按钮）

## 每轮收尾

- `go test ./...` 全绿、`go vet ./...` 无新告警
- 改了平台行为 → 复跑 `tools/` 脚本并更新 `docs/` 验证记录
- 新发现的坑补到本文
- 提交消息用中文前缀：`feat:` / `fix:` / `docs:` / `chore:`
