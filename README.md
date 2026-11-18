# clipnest

macOS / Windows 剪贴板历史管理器。菜单栏（托盘）常驻，`⌘⇧V`（Windows `Ctrl+Shift+V`）唤出面板，回车粘贴。
历史可通过**自有 NAS**（WebDAV）端到端加密同步，不依赖第三方云。

纯 Go：UI 由 [mygo](https://github.com/egoist/mygo) v0.10.0 自绘，无 WebView、无 Node、无 cgo。

## 能做什么

- **采集**：文本 / 富文本（HTML、RTF）/ 图片 / 文件；按内容哈希去重并统计使用次数
- **面板**：搜索（中文可搜、短词自动降级模糊匹配）、类型与分组筛选、右栏预览、滚到底自动续页
- **粘贴**：`⏎` 粘贴、`⌘1-9` 直贴、`⌥⏎` 粘纯文本、双击行直接粘
- **整理**：置顶 / 收藏 / 备注 / 分组 / 敏感标记；`⌘` 点多选批量删除，删后 10 秒内可撤销
- **导出**：`⌘O` 或右键导出（文本存文件、文件条目复制到目录）、拖拽到 Finder / 资源管理器
- **同步**：WebDAV 增量同步 + 端到端加密（argon2id + chacha20poly1305），局域网与 Tailscale 双地址自动切换，采集后 1.2 秒实时推送
- **其他**：加密备份导入导出、暂停记录、运行期日志轮转、检查更新（Gitee / GitHub / 自动 / 自定义源，只查不装）、开机自启

## 安装

> 国内网络直接用下面的 Gitee 镜像（国内快得多；脚本的 `auto` 选源也是先 Gitee）。GitHub 的发行版资产域在国内常常
> 「API 能探到、真下载断」，那是卡十几秒后失败的主要原因。

| 平台 | 方式 | 命令 |
| --- | --- | --- |
| macOS | Homebrew | `brew tap nekoleamo/clipnest && brew trust --tap nekoleamo/clipnest && brew install --cask clipnest` |
| macOS | 一键脚本 | `curl -fsSL https://github.com/nekoleamo/clipnest/releases/latest/download/install.sh \| sh` |
| Windows | Scoop | `scoop bucket add clipnest https://github.com/nekoleamo/scoop-clipnest && scoop install clipnest` |
| Windows | 一键脚本 | `irm https://github.com/nekoleamo/clipnest/releases/latest/download/install.ps1 \| iex` |

Gitee 镜像（国内推荐）：

```bash
brew tap null_593_5354/clipnest https://gitee.com/null_593_5354/homebrew-clipnest
curl -fsSL https://gitee.com/null_593_5354/clipnest/raw/main/install.sh | sh
scoop bucket add clipnest https://gitee.com/null_593_5354/scoop-clipnest
irm https://gitee.com/null_593_5354/clipnest/raw/main/install.ps1 | iex
```

未签名、未公证：包管理器与 `curl`/`irm` 下载不带隔离标记，不会触发 Gatekeeper / SmartScreen；
只有用浏览器直接下 zip 才需要「右键 → 打开」或 `xattr -dr com.apple.quarantine`。
一键脚本会校验 `SHA256SUMS`，不符就中止、什么都不装。

## 开发

```bash
go build ./...                   # 编译
go test ./...                    # 测试（183 项）
go vet ./...
GOOS=windows go build ./...      # Windows 交叉编译
go tool mygo build               # 打包 .app 与 dmg
python3 tools/thirdparty.py      # 依赖变了就重生成第三方许可证清单
```

提交前装钩子（跑 vet + test + Windows 交叉编译）：`git config core.hooksPath .githooks`。

## 结构

```
main.go              入口：窗口 / 托盘 / 快捷键 / 单实例装配
internal/app         面板与设置窗的视图、动作、同步触发（按职责分文件）
internal/db          schema 与迁移（只追加）、查询、FTS5
internal/store       读写、去重、图片分片与回收
internal/clip        采集、写回、回环抑制、纯文本降级
internal/paste       粘贴注入（CGEvent / SendInput）
internal/sensitive   敏感应用过滤（前台应用身份）
internal/sync        WebDAV 增量同步 + 端到端加密
internal/backup      CNST1 加密容器
internal/update      检查更新（Gitee / GitHub / 自动 / 自定义源，只查不装）
tools/               发版、安装脚本与验证工具（非产品代码）
docs/                路线图、执行计划、发布流程
```

## 文档

| 文档 | 用途 |
| --- | --- |
| `docs/路线图.md` | 产品定位、技术决策、阶段状态、**12 条行为契约** |
| `docs/执行计划-下一步.md` | 当前状态与待做事项 |
| `docs/发布.md` | 打包、发版、双源分发、Homebrew / Scoop |
| `docs/变更记录.md` | 版本变更摘要 |
| `docs/windows-能力对照.md` | 逐项对照 macOS 能力在 Windows 的等价实现与验证状态 |
| `AGENTS.md` | 协作约定与框架坑位（开发前必读） |
| `docs/nsh-时代的历史坑.md` | nsh 0.5.1 与 Rust 时代的实测坑（历史参考） |

## 许可

MIT，见 `LICENSE`。二进制里链接的第三方代码各自保留原许可证，全文见 `THIRD-PARTY-LICENSES.md`。

## 相关

- 框架：[mygo](https://github.com/egoist/mygo)
- 上游参照实现：[EcoPaste](https://github.com/EcoPasteHub/EcoPaste)（行为契约的来源）
