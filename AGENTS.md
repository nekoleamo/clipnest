# clipnest 项目约定

> 新会话先读：`docs/路线图.md`（总纲）→ `docs/执行计划-下一步.md`（从哪继续）→ 本文（约定与坑）。
> nsh + Rust 旧实现**已从仓库删除**（2026-11-17，需要时看 git 历史 `7284efe` 之前）；其中「nsh 0.5.1 实测坑」那份文档单独保留在 `docs/nsh-时代的历史坑.md`，只在追溯「为什么现在这么做」时翻。

## 技术栈（不要偏离）

- **Go 1.27+**，模块名 `clipnest`，纯 Go **无 cgo**
- **mygo v0.10.0**（锁定；升级单独一轮并复跑 `docs/` 验证项——0.9.1 → 0.10.0 时 API 与行为均无变化，坑位仍适用）
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
tools/           验证脚本与发版/渲染脚本（Swift 与 sh，非产品代码）
site/            官网纯静态页（index.html 中文默认 + en/，共用 style.css）
docs/            路线图、执行计划、验证记录
```

官网用 `sh tools/build-site.sh` 渲染到 `site/dist/`（不入库），版本号只从 `mygo.json` 注入；
改安装方式 / 快捷键 / 同步口令名 / Windows 状态时，`README.md`、`docs/` 与 `site/` 三处一起改。

## 迁移铁律

1. `docs/路线图.md` 第四节的 **12 条行为契约**在 Go 侧必须逐条成立，改一条要重开评估
2. `internal/db` 的迁移脚本**只追加**，已发布脚本不可改。步骤现在是 `func(*sql.Tx) error`：纯 SQL 用 `sqlMigration(...)`，**加列类迁移必须先 `hasColumn` 再 ALTER**（SQLite 没有 `ADD COLUMN IF NOT EXISTS`，重跑会炸——`TestMigrateAdvancesVersionAndKeepsData` 会退化重跑一遍迁移）
2b. **元数据与同步**：契约 6 规定置顶/收藏/敏感/备注/分组**不刷新 `updated_at`**，所以增量同步另看 `items.meta_at`（迁移 v3）。新增任何「改了但不该动排序」的字段，要同时写 `meta_at`，否则那项改动永远出不了本机。**`meta_at` 还必须严格大于本条原来的 `updated_at`/`meta_at`**（`db.nextMetaAt`）：时间戳只到毫秒，同一毫秒里插入再改元数据会打平，对端 merge 认作「不比本地新」→ 改动静默丢失。图片原图推送前先查 `synced_blobs`（迁移 v4）：图片条目改备注/置顶会重新进增量，不查台账就会每次重传整张原图
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
14. **退出必须走 `App.Quit()`，且先把托盘图标拆掉**：从托盘菜单点退出时若菜单还在跟踪中就退进程，AppKit 会把状态栏图标留在原地变成「幽灵」——点它没反应、强制退出里也找不到（accessory app 不进那个列表），只能重新登录才清。正解：置退出标志 → 收起窗口 → `tray.Destroy()` → 150ms 后 `mygo.App.Quit()`。另外**调试用的 `/tmp/cn-*` 二进制也会各占一个状态栏图标**，卡住退出时先 `pkill -f /tmp/cn-` 排除干扰。以下两点仍然成立：
    - 面板 `OnClose` 里无条件 `PreventDefault` 会让 `App.Quit()` 静默失败：`prepareQuit` 会关所有窗口，关不掉就取消退出。**且 `OnBeforeQuit` 可能晚于关窗**，只靠它置标志不稳——`/Applications` 实测症状：点托盘「退出」后窗口全没了、托盘图标还在、点什么都没反应。正解是所有窗口的 `OnClose` 都在 `App.Quitting()` 为真时放行，并且退出统一走 `App.Quit()`（先置标志 + 收起面板/设置/预览，再 `mygo.App.Quit()`）
15. 打包：`go get -tool github.com/egoist/mygo/cmd/mygo` 后 `go tool mygo build`，配置在 `mygo.json`（native UI 项目不用 TS 配置）
16. **中文输入法下 `TextInput.Submitted()` 需要第二次回车**：合成期间 AppKit 把 `KeyPressed` 扣住（mygo `surface.keyDown` 的 `withheld` 分支），只有 `KeyUp` 到达，第一次 ⏎ 只提交候选。不输入直接 ⏎ 一次就粘；`⌘1-9` 带 Cmd 不受输入法影响。另外 `OnWindowShortcut(0, ui.KeyEnter)` 永远不会触发——焦点在输入框时 Enter 被编辑框优先吃掉，要判提交就用 `Submitted()`
17. 签名与 TCC：**已改用固定自签身份 `clipnest-dev`（`mygo.json` 的 `macos.signingIdentity`）**，实测重打包后辅助功能授权与钥匙串 ACL 都保持不变，不用再点。两个坑：①从 ad-hoc 换到证书时，TCC 里会残留一条匹配不上的旧记录（日志：`Failed to match existing code requirement`），面板就会永远提示去授权——`tccutil reset Accessibility com.clipnest.app` 后重新勾一次就好；②若换机器/丢了身份（`mygo build` 回退 ad-hoc cdhash），授权会每次重打包失效，跑 `tools/signing-identity.sh` 重建。排查入口：`<data>/clipnest.log` 里的 `SHOW ax=`、`go run ./cmd/syncprobe -probe`、`open -a clipnest.app --args -ax-prompt`
17c. **口令存哪儿按平台不同**：macOS 钥匙串（按 App 的 ACL、重装会弹）/ Windows 凭据管理器通用凭据（DPAPI 按用户加密、`clipnest-sync/<设备名>`、读不弹框、作用域是本用户）/ Linux libsecret。给用户看的文案统一走 `sync.SecretStoreName()`，别写死「钥匙串」
17b. **重装 .app 后钥匙串会再弹一次授权**（`securityd` 日志：`ACL partition mismatch: client cdhash` 变了），同步口令取不出来，那一轮会一直等弹窗。点「始终允许」即可；**现在有看门狗**：一轮超过 2 分钟只告警（日志 + 底栏提示，并停止再叠加新轮次），10 分钟才强制放开重开——之前没有它时，卡住的那一轮会让后续每 5 秒的同步被静默跳过、日志里一个字都没有；而「一直强放 + 继续起新轮」的写法会在弹窗被允许的瞬间让攒下的 6~7 轮同时推同一个包（实测）
18. **Column 里的子行要用 `FillWidth()` 而不是 `Fill()`**：`Fill()` 连高度一起占满，会把下面所有东西顶到窗口外（症状：整版偏下、搜索框跑到 400px 处、列表看不见）。辅助功能提示条就踩过这个坑——没授 AX 授权时整块版式崩掉，而且 `go test ./internal/app` 会随测试进程有没有 AX 授权时绿时红
19. **面板只是 `Hide` 不是销毁，跨轮状态会留着**：搜索词、选中行、筛选、`ListScroll`、备注草稿、以及排进 `App.pending` 却没跑完的点击动作，都会在下次呼出时冒出来。`Show` 必须先走 `ResetPanelState()`（`internal/app/app.go`），否则「Esc 退出再呼出」看到的就是上次的样子
20. **焦点在搜索框里时，单行输入框会吃掉一些键**：`Home/End` 被当成「光标到行首/行尾」吃掉（root 上的 `OnShortcut` 永远收不到，实测过），`⌘A`/`⇧⌘A` 同理（全选文本），所以「全选」只能走右键菜单。能穿过输入框的有 `↑↓←→`、`PgUp/PgDn`、`⌥⏎`、`⌘F`。要加列表快捷键先确认这一点，否则会写出一条永不触发的绑定
21. **行点击要看修饰键就得读自己元素的 `ClickModifiers()`**：点击回调在下一帧跑，元素变量得先 `var el ui.Element` 声明再赋值；另外点在「摘要文字」上是子元素自己的 `OnClick`（父行收不到），父子两处都要绑，否则 ⌘点击在文字上不生效
22. **`DoubleClicked()` 必须在构建期问**（建完元素立刻判），放进 `OnClick` 回调里永远是 `false`——点击回调跑在下一帧，连击状态那时已经清掉。另外双击判定还会被「第一次点击改变了元素外观/焦点」影响，所以别指望在 `OnClick` 里补判
23. **`⌘Z` 与 `⌘A` 一样会被搜索框吃掉**（输入框把它当「撤销输入」/「全选文本」）。需要入口就放成可见控件（本轮：筛选栏的「撤销删除」按钮），别指望快捷键
24. **设置窗（以及任何表单）的控件不能绑局部变量**：视图每帧重建，`x := a.Settings.Foo` + `ui.NumberInput(c, &x, …)` 在下一帧就被 settings.json 里的旧值盖回去。症状是一整套连着的：「+ 跳到 5100 再点不动、− 卡在 4900」「复选框点了不回勾」「输入框打不进字」「选项卡选不中」。正解是把整张表的草稿放成 `App` 的字段（`prefsForm`，`internal/app/prefs.go`），点「保存」才写盘，打开窗口时 `ResetPrefsForm()` 重来一遍。列表/筛选里那种「改了就落盘」的写法（`if x.Changed() { a.later(SetX) }`）没问题——因为真值每帧都被持久状态**回填**
25. **`a.later()` 排的活只有 `runPending()` 会跑，而它以前只在「面板视图」的帧末被调用**：设置窗/预览窗里排的动作（切更新源、检查更新、点预览窗关闭）以前压根不执行——按钮点了毫无反应、也不报错。每个 `ui.View` 入口的帧末都要调一次 `a.runPending()`
26. **`TrayOptions.Title` 在 macOS 是画在图标旁边的文字，不是 tooltip**：既给 `Icon` 又给 `Title: "❐"`，菜单栏里就会出现两个「图标」（实测被当成重复图标报 bug）。只要图标就别填 `Title`，提示文字用 `ToolTip`
27. **设置窗内容比窗口高得多（≈1350pt），滚动条默认只在悬停时出现**：`ui.Scroll(...).KeepScrollbars(true)`，否则用户根本不知道下面还有「同步 / 分组 / 备份 / 开机启动」几节（`uishot -prefs` 出图能直接看出这点）
28. **录快捷键要靠 `Element.HandleInput`，别用输入框**：输入框会把带 ⌘ 的组合当文本编辑命令吃掉，用户还得手打 `CmdOrCtrl+Shift+V` 这种语法。做法是 `ui.Row(...).Focusable().HandleInput(...)`（带 ⌘/⌃/⌥/⇧ 或 F1–F12 才算组合键，见 `AcceleratorFromInput`），点一下进入录制态、Esc/点别处退出。**录制期间要把当前全局快捷键摘掉**，否则按到旧组合时 Carbon 照样触发、面板被唤出，录制框根本收不到键；退出（含关窗、重开）一定要挂回去
29. **`mygo.GlobalShortcut.Register` 在 headless 测试里会永久卡住**：它把活投给主线程再等回音，测试进程没有事件循环在跑。`registerShortcut`/`unregisterShortcut` 这两个包级变量就是给测试替的（`fakeShortcuts`）

## 每轮收尾

- `go test ./...` 全绿、`go vet ./...` 无新告警
- 改了平台行为 → 复跑 `tools/` 脚本并更新 `docs/` 验证记录
- 新发现的坑补到本文
- 提交消息用中文前缀：`feat:` / `fix:` / `docs:` / `chore:`
