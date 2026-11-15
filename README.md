# clipnest

跨平台（macOS + Windows）剪贴板历史管理器，剪贴板记录可通过自有 NAS 同步。

- **技术栈**：Rust + [nsh (Native Shell)](https://github.com/longbridge/nsh) 0.5.1，原生控件渲染，无 WebView
- **同步**：自建（NAS 上的 WebDAV 或自写服务），不依赖第三方云
- **形态**：菜单栏 / 托盘常驻应用，全局快捷键唤出面板，回车粘贴

## 当前状态

已完成 **macOS 侧技术验证**（见 `docs/2026-11-15-nsh-验证记录.md`），`src/` 下的代码是可运行的最小验证工程（v0 骨架），不是产品代码。

| 能力 | 状态 |
| --- | --- |
| 不抢焦点弹出面板 + 弹出即可打字 | ✅ 已验证 |
| Esc / 失焦收起 | ✅ 已验证 |
| 全局快捷键 | ✅ 已验证 |
| 剪贴板 text/html/rtf/image/files 采集 | ✅ 已验证（延迟 median 128 ms） |
| 写回回环抑制 | ✅ 已验证 |
| 捕获 → 写回 → ⌘V 自动粘贴 | ✅ 已验证 |
| Windows 端 | ❌ 未验证（最大风险项） |
| SQLite FTS5 / 拖拽导出 / 缩略图 / 多显示器 | ❌ 未开始 |

## 构建与运行

```bash
# 注意：PATH 里必须优先 homebrew 的 cargo（~/.local/bin/cargo 是个空壳 shim）
source "$HOME/.cargo/env"        # rustup 工具链（brew rust 已卸载）

cargo check                                  # 类型检查
nsh package --target macos                   # 打包 → .nsh/out/clipnest.app
nsh test                                     # headless 测试
```

产物固定约 50 MB `.app` / 17 MB zip（nsh 0.5.1 的 runner 特性无法裁剪，已知项）。

## 目录

```
src/          nsh 应用（lib.rs 组件 + main.rs 原生入口）
tools/        验证用辅助工具（Swift 编写，非产品代码）
docs/         验证记录与设计文档
nsh.json      nsh 应用清单（窗口、激活策略）
```

## 文档

| 文档 | 内容 |
| --- | --- |
| `docs/路线图.md` | **总纲**：产品定位、技术决策（已定案）、P0–P6 阶段规划、12 条行为契约、风险登记 |
| `docs/执行计划-下一步.md` | **开工用**：当前交接状态、P1 数据层任务分解（T1–T7，含验收标准）、P2 预告、会话开场提示词 |
| `docs/2026-11-15-nsh-验证记录.md` | P0 实测数据与复跑方法 |
| `AGENTS.md` | 项目约定 + nsh 0.5.1 的 10 条实测坑 |

## 相关

- 上游参照实现：[EcoPaste](https://github.com/EcoPasteHub/EcoPaste)（Tauri 版 + 正在进行的 Go+NSGo 重构）
- 迁移规格可参考 EcoPaste `refactor/nsgo-migration` 分支的 `docs/nsgo-migration/`（8 篇文档、12 条行为契约、R1–R12 风险登记）
