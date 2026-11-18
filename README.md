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
- **设置**：面板高度与位置、快捷键（点一下直接在键盘上按组合键录，不用打 accelerator 语法）、
  清理策略、同步与口令、分组、备份、开机自启；「清空全部历史」要输入确认词，且清空前自动导一份加密备份
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

## 两端同步怎么填（WebDAV + 端到端加密）

设置 → 同步，两台机器各填一次。**两个口令是两回事**，分别存进系统凭据：

| 填哪 | 存成 | 要求 |
| --- | --- | --- |
| WebDAV 用户名 / **WebDAV 账号口令** | `clipnest-webdav/<本机名称>`（Windows 凭据管理器 / macOS 钥匙串） | NAS 上那个账号的密码，各设备可以不同 |
| **加密口令** | `clipnest-sync/<本机名称>` | 端到端加密与 `.cnbak` 备份共用；**两台机器必须同一个字符串**，否则拉下来解不开 |

- 本机名称默认取主机名，别两台填成一样（远端文件名按它区分：`<本机名称>.json`）。
- 改了本机名称 → 口令按新名字找，读不到旧的，重填一次即可。
- NAS 用自签证书时勾**「允许自签证书」**（等效 `InsecureSkipVerify`，局域网与备用地址一并生效）；
  不想降级就把 NAS 的根证书导进系统信任库，然后不勾。
- 口令只从设置窗改：Windows 凭据管理器 GUI 存的是 UTF-16，和 app 读的 UTF-8 字节不匹配。
- 排查：面板底栏/设置窗会直接写错误原文 —— `PROPFIND: 401 Unauthorized` 是 WebDAV 口令不对，
  `x509: certificate signed by unknown authority` 是自签证书没勾，`…里没有同步口令` 是加密口令没写进去。

## 开发

```bash
go build ./...                   # 编译
go test ./...                    # 测试（185 项）
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
| `docs/变更记录.md` | 版本变更摘要（当前 1.3.1） |
| `docs/windows-能力对照.md` | 逐项对照 macOS 能力在 Windows 的等价实现与验证状态 |
| `AGENTS.md` | 协作约定与框架坑位（开发前必读） |
| `docs/nsh-时代的历史坑.md` | nsh 0.5.1 与 Rust 时代的实测坑（历史参考） |

## 许可

MIT，见 `LICENSE`。二进制里链接的第三方代码各自保留原许可证，全文见 `THIRD-PARTY-LICENSES.md`。

## 相关

- 框架：[mygo](https://github.com/egoist/mygo)
- 上游参照实现：[EcoPaste](https://github.com/EcoPasteHub/EcoPaste)（行为契约的来源）
