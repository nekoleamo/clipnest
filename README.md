# clipnest

跨平台（macOS + Windows）剪贴板历史管理器，剪贴板记录可通过自有 NAS 同步。

- **技术栈**：Go 1.27 + [mygo](https://github.com/egoist/mygo) v0.9.1，UI 用 `mygo/ui` 纯 Go 自绘，无 WebView、无 cgo
- **同步**：自建（NAS 上的 WebDAV 或自写服务），不依赖第三方云
- **形态**：菜单栏 / 托盘常驻应用，全局快捷键唤出面板，回车粘贴

## 当前状态

**迁移中**：从 nsh + Rust（已归档到 `legacy-rust/`）全量重写到 Go + mygo。原实现已完成 P0–P4（技术验证、数据层、面板产品化、系统集成、WebDAV 同步），迁移期以它作为逐行对照与验收基准。

| 能力 | nsh 版 | Go 版 |
| --- | --- | --- |
| 不抢焦点弹出面板 + 弹出即可打字 | ✅ | 框架自带（`Panel`） |
| Esc / 失焦收起 | ✅ | 待接 |
| 全局快捷键（含 ⌘⇧ 组合） | ✅ | 框架自带 |
| 剪贴板 text/html/rtf/image/files 采集 | ✅ | 框架自带（`OnChanged` + `Formats`） |
| 写回回环抑制 | ✅ | 待搬 |
| 捕获 → 写回 → ⌘V 自动粘贴 | ✅ | 待搬（`purego` + `CGEvent`） |
| SQLite FTS5 搜索 / 图片与文件条目 | ✅ | 待搬 |
| 多显示器定位、运行时挪窗 | ⚠️ nsh 限制 | 框架自带（`Screen`） |
| WebDAV 端到端加密同步 | ✅ | 待搬 |
| Windows 端 | ⚠️ 真机未验 | 待交叉编译 + 冒烟 |

## 构建与运行

```bash
go build ./...                 # 编译
go test ./...                  # 测试
GOOS=windows go build ./...    # Windows 交叉编译
go tool mygo build             # 打包（bundle / dmg / Windows 安装包）
```

产物从 nsh 的固定 50 MB 降到个位数 MB。

## 目录

```
main.go / internal/ / ui/   Go 实现（见 AGENTS.md 的目录规划）
tools/                      验证用辅助工具（Swift / PowerShell，非产品代码）
docs/                       路线图、执行计划、验证记录
legacy-rust/                已归档的 nsh + Rust 实现，仅作对照，不构建
```

## 文档

| 文档 | 内容 |
| --- | --- |
| `docs/路线图.md` | **总纲**：产品定位、技术决策、阶段规划、12 条行为契约、风险登记 |
| `docs/执行计划-下一步.md` | **开工用**：当前交接状态与任务分解 |
| `docs/2026-11-15-nsh-验证记录.md` | nsh 时代 P0 实测数据（Go 版验收时的行为基准） |
| `AGENTS.md` | 项目约定 + mygo 已确认的坑 |
| `legacy-rust/AGENTS-历史坑.md` | nsh 0.5.1 与 Rust 数据层的全部实测坑（归档参考） |

## 相关

- 上游参照实现：[EcoPaste](https://github.com/EcoPasteHub/EcoPaste)（Tauri 版 + 正在进行的 Go 重构）
- 框架选型记录见 `docs/执行计划-下一步.md` 的「技术栈迁移」一节
