# clipnest 项目约定

## 技术栈（不要偏离）

- **Rust + nsh 0.5.1**，UI 用 Rust 组件（`nsh_core` / `nsh_base`），不用 TSX
- 不引入 WebView、Node、Electron 类依赖
- `nsh` 版本锁在 0.5.1，升级单独做一次并复跑 `docs/` 里的验证项

## 构建

```bash
export PATH=/opt/homebrew/bin:$PATH   # ~/.local/bin/cargo 是空壳 shim，会让打包失败
cargo check
nsh package --target macos
nsh test
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

## 代码约定

- 注释、文档一律中文
- `src/lib.rs` 只放组件与 UI 装配，平台/剪贴板/存储逻辑按模块拆到 `src/<模块>.rs`
- 验证脚本放 `tools/`，用 `//` 注释说明用途，不参与构建
- 每个平台行为（焦点、权限、剪贴板时序）改动后，必须回到 `docs/2026-11-15-nsh-验证记录.md` 对应项复测并更新状态

## 验证方法（无需人工点鼠标的部分）

- 窗口层级/是否可见：`tools/wins.swift`（CGWindowList，`layer=3` 即 floating panel）
- 前台应用：`lsappinfo info -only name $(lsappinfo front)`
- 合成按键：`tools/keys3.swift`（⌘⇧V / ⌘V / Esc / 单字符）
- 粘贴目标：`tools/paste_target.swift`（自带 Edit 菜单的 NSTextView 窗口，粘贴后打印内容）
- 剪贴板写入：`tools/pb.swift`（text / html / rtf / image / files）

需要 macOS 辅助功能授权（系统设置 → 隐私与安全性 → 辅助功能），否则合成按键与 ⌘V 会被系统丢弃。
