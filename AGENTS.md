# clipnest 项目约定

> 新会话先读：`docs/路线图.md`（总纲）→ `docs/执行计划-下一步.md`（从哪继续）→ 本文（约定与坑）。

## 技术栈（不要偏离）

- **Rust + nsh 0.5.1**，UI 用 Rust 组件（`nsh_core` / `nsh_base`），不用 TSX
- 不引入 WebView、Node、Electron 类依赖
- `nsh` 版本锁在 0.5.1，升级单独做一次并复跑 `docs/` 里的验证项

## 构建

```bash
source "$HOME/.cargo/env"   # rustup（cargo 在 ~/.cargo/bin；/opt/homebrew/bin/cargo 已不存在）
cargo check
nsh package --target macos
nsh test

# Windows：交叉编译与打包（macOS 上可跑，真机冒烟另算）
cargo xwin check --target x86_64-pc-windows-msvc --all-targets
nsh package --target windows
```

## nsh 0.5.1 已确认的坑（每条都实测过）

1. **打包后的应用不执行 `src/main.rs`**：`nsh package` 生成自己的 runner 入口，初始化逻辑必须放在组件里（如 `App()` 顶部调用一次性的 `boot()`）。
2. **`nsh_core::prelude::window()` 返回 `Size`**，窗口元素是 `nsh_core::element::desktop::window()`。
3. **`Signal::update(|v| *v = !*v)` 在全局快捷键回调里不执行**，必须 `set(!get())`。
4. **`WindowAttributes` 是 `non_exhaustive`**，只能用 `WindowAttributes::for_kind(kind)` 再改字段。
5. **`WindowKind::Popup` 默认 `activate = true`**，要不抢焦点必须显式设 `activate = Some(false)`。
6. **面板要在打开后延后约 120 ms 再 `focus()`**，否则窗口尚未创建、`focus` 落空，键入会漏到前台应用。
7. **`WindowRef` 只能从窗口内容渲染里取**（`current_window()`），且非 `Send`，存 `thread_local`。
8. **`nsh.json` 必须有 `"root": "App"`**，否则被当成 TSX 应用报 `no App.tsx`。
9. **`lib.rs` 必须有 `#[native_module("<app>/native")]`**，否则打包时 describe bin 编译失败。
10. **产物体积固定约 50 MB**：`nsh-cli` 生成的 runner 硬编码 8 个 features，`default-features = false` 无效。

## 数据层已确认的坑（P1 实测）

1. **FTS5 `trigram` 查询词必须 ≥3 个字符**：2 个汉字（「银河」）命中不了，必须降级 `LIKE`（`db::search::FTS_MIN_CHARS`）。
2. `cargo add chacha20poly1305 argon2` 现在拉到 **0.11 / 0.6**：`Key::from([u8;32])`、`Nonce::from([u8;12])`，`from_slice` 已废弃（编译警告）。
3. 私有模块里的 `pub` 项会触发 `dead_code` 警告 → 模块本身也要 `pub mod`。
4. **不要用 `nsh_core::data_dir()` 当数据根**：它返回 `<数据目录>/<app id>`（`com.example.clipnest`）且依赖 nsh 运行时，`cargo test` 里拿不到；根目录统一走 `src/paths.rs`。
5. 编辑库里加列/加表只能追加迁移脚本（`src/db/schema.rs` 的 `MIGRATIONS`），已发布的脚本不可改。

### 面板/键盘（P2 实测）

11. **`WindowKind::Popup` 必须 `activate = Some(true)`** 才能收键盘：app 不 active 时 `key=None active=false`，字符全被前台应用吃掉。（P0 记录的「`activate=false` 也能打字」在 0.5.1 当前构建上不成立。）
12. **`WindowRef::focus()` 必须从主线程调用**：`set_timeout` 的回调里 `MainThreadMarker::new()` 返回 `None`，从那儿调 AppKit 是空操作 → 面板永远成不了 key。要 `run_on_main_thread(...)`。
13. 输入框 `auto_focus(true)` 拿 field editor 就够了；**再手动 `focus()` 一次会让输入失效**（field editor 重挂）。
14. 焦点在 `input` 里时 **Esc / ↑↓ 都被输入框吃掉**：必须 `on_key_with(..., When::Always, ...)` 自己绑。⏎ 用 `input().on_submit`。
15. 列表要包 `dynamic(|| ...)`；静态 `children(...)` 不刷新（底部响应式计数会正常，容易误判成「列表没问题」）。
16. `#[component] fn Panel()` 的构造器是**小写** `panel()`（宏生成 `PanelElement` + `panel()`），`Panel()` 编译不过。
17. `run_app` 总会开一个主窗口，常驻应用要在根组件里 `current_window().hide()`。
18. **`Signal` 要放 `static`，不要放 `thread_local`**：`set_timeout`、托盘菜单的回调可能不在主线程，thread_local 里读到的是 `None`（设置窗因此打不开）。`Signal` 是 `Copy + Send + Sync`；`WindowRef`/`ElementRef` 才留在 thread_local。
19. 托盘图标 16 pt 纯图标既难确认也点不准：给 `Tray::title()` 配一个字符（如 `❐`），`System Events` 就能按名字点到。SVG 图标走模板渲染，笔画只用 `black`。
20. 受控 `input`（`.value(信号)`）在 `on_change` 里必须同步更新那个信号，否则输入被回滚。
21. **主窗口不能 `hide()`**：一隐藏应用就不算 active，面板永远成不了 key window（`key=None active=false`，键盘全丢）。做法：去掉标题栏 + `transparent` + `set_bounds` 挪到屏幕外（系统最多只让到 `x=-380`，剩 40pt）+ `pointer_events: None` + 根视图背景透明。
22. **`⌘⇧X` 这类 shift 组合到不了 keymap**（`mod+shift+c` 实测不触发，osascript 与 CGEvent 都不行）；单修饰组合（`⌘P`/`⌘S`/`⌘D`/`⌘Y`）正常。
23. **`close_on_blur` 会和重渲染打架**：列表变化 → field editor 重挂 → 收到 blur → 面板自己关掉。设 `close_on_blur = Some(false)`，收起只认 Esc（或粘贴后 `dismiss()`）。
24. 响应式列表用 `each(rows, |r| r.id, …)`：静态 `children` 不刷新，`dynamic` 整块重建会把搜索框焦点搞丢。行渲染函数返回具体 `View`（`impl IntoElement` 会带出 `&Row` 生命周期，E0515）。
25. 面板激活自己会抢走前台：粘贴流程 = 写回 → `dismiss()`（隐藏 + `NSRunningApplication::activate` 原应用）→ 120 ms → ⌘V；原应用 pid 在打开前用 `NSWorkspace.frontmostApplication()` 记。

## 导出与剪贴板文件（P3 实测）

1. **nsh 0.5.1 没有 drag-out**（只能接收 drop，也不暴露 NSView）：导出走「写文件 + `set_files` 放剪贴板」。
2. **写剪贴板文件后前台会被抢回原应用**：面板还在但收不到键盘 → 导出成功后补一次 `focus_panel()`。
3. **小图别让 `image` 重编码当缩略图**：128×128 的图 AppKit 编码 3.8K、`image` 重编码 39K（比原图还大）。只在最长边 > 320 时才真缩，否则拷原图字节。

## 同步（P4 实测）

1. **`sync.json` 独立于 `settings.json`**，口令一律进系统钥匙串；配置文件里出现密码就是 bug。
2. serde `rename_all = "snake_case"` 会把 `WebDav` 写成 `web_dav`，要显式 `#[serde(rename = "webdav")]`。
3. `boot()` 不在响应式上下文里：别在里面调 `set_timeout` / `signal`（会 panic `no reactive runtime`），用普通线程。
4. WebDAV 实现用 **reqwest**（blocking + rustls + http2）：ureq 的协议层只允许标准方法，**发不出 PROPFIND**（实测 `PROPFIND not valid for HTTP version HTTP/1.1`）。
5. reqwest 必须 `.no_proxy()`：本机 `HTTPS_PROXY` 会把局域网 IP 也送进代理，每个请求卡到 20 秒超时。
6. 飞牛 fnOS：根目录只读（`MKCOL`/`PUT` 都 405），要用 `/data/...`；父目录不存在时回 **403**（不是 409），`put()` 要按 403/404/409 都先 `MKCOL`。
7. 自签证书走 `danger_accept_invalid_certs`（`sync.json` 的 `insecure_tls`）。

## 开机自启与权限（P3 实测）

1. **macOS 写完 LaunchAgent plist 不要 `launchctl load`**：`RunAtLoad` 会让它立刻再起一个实例（实测进程变 2 个）。只写文件，登录时 launchd 自己会加载。
2. 辅助功能权限用 `AXIsProcessTrusted()`；没授权时面板顶部给提示 + 跳系统设置（`x-apple.systempreferences:…Privacy_Accessibility`）。自检可用 `SPIKE_NO_AX=1` 伪造。

## 列表与过滤（P3 实测）

1. **`each(rows, |r| r.id, …)` 的 key 要带上影响显示的字段**：只用 id 时，改置顶/收藏/敏感不会重画（行内容是一次性快照），要把 `(id, pinned, favorite, sensitive, summary, time)` 都放进 key。
2. 敏感过滤按**应用身份**（macOS bundle id / Windows 进程名）判定，别猜内容；名单见 `src/sensitive.rs`，`settings.json` 的 `sensitive_apps` 可覆盖，`ignore_sensitive_apps` 是总开关。

## 多屏与平台 API（P3 实测）

1. **`nsh_core::element::desktop::displays()` 不能在 `run_on_main_thread` 回调里调用**：它读 nsh 的响应式环境，会 panic（`no reactive runtime`）。要屏幕信息就自己问系统（macOS `NSScreen`、Windows `MonitorFromPoint`），只把算好的坐标交给 `WindowRef::set_bounds`。
2. macOS `NSScreen.frame` 是左下原点、y 向上；nsh 的屏幕坐标是左上原点、y 向下，换算 `top = 主屏高度 - (frame.y + frame.height)`。

## 图片（P3 实测）

1. **`clipboard-rs` 的 `image` 只开 `png`/`jpeg` 解码**：macOS 截图常只给 TIFF，`get_image()` 会失败。兜底走 AppKit 重编码（`NSImage → NSBitmapImageRep → representationUsingType(PNG)`），别指望 `image` crate 解 NSImage 出的 TIFF。
2. 图片文件名 = `blake3(png 字节)`：内容哈希即文件名，去重与分片（`<hash前2>/`）都靠它。
3. 图片优先于文本：复制截图时系统同时给文本/位图 flavor，`store::record` 只留图片条目。

## 代码检索

本仓库已有 `.codegraph/` 索引（codegraph 1.6.2）。改代码前优先走 codegraph MCP（`codegraph_explore`，传 `projectPath=/Users/nekoleamo/Documents/Working/clipnest`），不要逐个 grep/read。改动较大后重建：`codegraph init`（增量）或 `codegraph status` 查是否过期。

## 代码约定

- 注释、文档一律中文
- `src/lib.rs` 只放组件与 UI 装配，平台/剪贴板/存储逻辑按模块拆到 `src/<模块>.rs`
- 验证脚本放 `tools/`，用 `//` 注释说明用途，不参与构建
- 每个平台行为（焦点、权限、剪贴板时序）改动后，必须回到 `docs/2026-11-15-nsh-验证记录.md` 对应项复测并更新状态

## 每轮收尾

- `cargo test` 全绿、`cargo check` 不新增 warning
- 改了平台行为 → 复跑 `tools/` 脚本并更新 `docs/2026-11-15-nsh-验证记录.md` 的状态
- 新发现的坑补到本文（nsh 版本相关的必须记）
- 改动多则刷新索引：`codegraph init`
- 提交消息用中文前缀：`feat:` / `fix:` / `docs:` / `chore:`

## 验证方法（无需人工点鼠标的部分）

- 窗口层级/是否可见：`tools/wins.swift`（CGWindowList，`layer=3` 即 floating panel）
- 前台应用：`lsappinfo info -only name $(lsappinfo front)`
- 合成按键：`tools/keys3.swift`（⌘⇧V / ⌘V / Esc / 单字符）
- 粘贴目标：`tools/paste_target.swift`（自带 Edit 菜单的 NSTextView 窗口，粘贴后打印内容）
- 剪贴板写入：`tools/pb.swift`（text / html / rtf / image / files）

需要 macOS 辅助功能授权（系统设置 → 隐私与安全性 → 辅助功能），否则合成按键与 ⌘V 会被系统丢弃。
