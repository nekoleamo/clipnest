package app

import (
	"fmt"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"

	"clipnest/internal/backup"
	"clipnest/internal/db"
	"clipnest/internal/settings"
	clipsync "clipnest/internal/sync"
)

// prefsView 是设置窗：改完点保存写回 settings.json / sync.json。
func (a *App) prefsView(c *ui.Context) {
	th := c.Theme()
	s := a.Settings
	cfg := clipsync.LoadConfig(a.Paths)

	maxItems := float64(s.MaxItems)
	panelHeight := float64(s.PanelHeight)
	position := 0
	if s.PanelPosition == settings.PositionBottom {
		position = 1
	}
	ignoreSensitive := s.IgnoreSensitiveApps
	sensitiveApps := joinLines(s.SensitiveApps)
	exportDir := s.ExportDir

	syncOn := cfg.Enabled
	backendKind := 0 // 0 = 本地目录, 1 = WebDAV
	if cfg.Backend.Kind == clipsync.BackendWebDav {
		backendKind = 1
	}
	syncDir := cfg.Backend.Path
	remoteURL := cfg.Backend.URL
	fallbackURL := cfg.Backend.FallbackURL
	remoteUser := cfg.Backend.Username
	remoteDir := cfg.Backend.RemoteDir
	deviceName := cfg.DeviceName
	interval := float64(cfg.IntervalSecs)
	insecure := cfg.Backend.InsecureTLS
	encrypt := cfg.Encryption.Enabled
	realtime := cfg.Realtime
	shortcutDraft := *a.ShortcutDraft()
	pruneDays := *a.PruneDaysDraft()
	pullSecs := float64(cfg.PullIntervalSecs())
	passphrase := ""

	var status string
	apply := func() {
		next := s
		next.MaxItems = int64(maxItems)
		next.PanelHeight = int32(panelHeight)
		if position == 1 {
			next.PanelPosition = settings.PositionBottom
		} else {
			next.PanelPosition = settings.PositionCenter
		}
		next.SensitiveApps = splitLines(sensitiveApps)
		next.IgnoreSensitiveApps = ignoreSensitive
		next.ExportDir = exportDir
		next = settings.Normalize(next)
		if err := settings.Save(next, a.Paths); err != nil {
			status = "❌ 设置保存失败：" + err.Error()
			return
		}
		a.Settings = next

		nextCfg := cfg
		nextCfg.Enabled = syncOn
		nextCfg.DeviceName = deviceName
		nextCfg.IntervalSecs = int64(interval)
		nextCfg.Encryption.Enabled = encrypt
		nextCfg.Realtime = realtime
		nextCfg.PullSecs = int64(pullSecs)
		if backendKind == 1 {
			nextCfg.Backend = clipsync.Backend{
				Kind: clipsync.BackendWebDav, URL: remoteURL, FallbackURL: fallbackURL,
				Username: remoteUser, RemoteDir: remoteDir, InsecureTLS: insecure,
			}
		} else {
			nextCfg.Backend = clipsync.Backend{Kind: clipsync.BackendLocalDir, Path: syncDir}
		}
		nextCfg = nextCfg.Normalize()
		if err := clipsync.SaveConfig(nextCfg, a.Paths); err != nil {
			status = "❌ 同步配置保存失败：" + err.Error()
			return
		}
		if encrypt && passphrase != "" {
			if err := clipsync.SecretSet(clipsync.SecretService, nextCfg.DeviceName, passphrase); err != nil {
				status = "⚠️ 口令没写进钥匙串：" + err.Error()
			}
		}
		if backendKind == 1 && passphrase != "" {
			_ = clipsync.SecretSet(clipsync.WebDavSecretService, nextCfg.DeviceName, passphrase)
		}
		a.Refresh()
		if status == "" {
			status = "✅ 已保存"
		}
	}

	ui.Column(c).Fill().Gap(10).Padding(16).Background(th.Background).Children(func() {
		ui.Scroll(c).Fill().Children(func() {
			ui.Column(c).Gap(10).Children(func() {
				ui.Text(c, "clipnest 设置").FontSize(16).Bold().TextColor(th.Text)

				label(c, "历史保留条数（0 = 不限）", th)
				ui.NumberInput(c, &maxItems, 0, 1_000_000, 100)

				label(c, "面板高度（点）", th)
				ui.NumberInput(c, &panelHeight, float64(settings.PanelHeightMin), float64(settings.PanelHeightMax), 20)

				label(c, "面板位置", th)
				ui.Segmented(c, &position, "屏幕居中", "贴屏幕底部")

				label(c, "唤起快捷键（accelerator，如 CmdOrCtrl+Shift+V / Alt+Space；当前 "+a.Hotkey()+"）", th)
				ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
					ui.TextInput(c, &shortcutDraft).Width(240)
					if ui.Button(c, "应用快捷键").Clicked() {
						a.ApplyShortcutDraft(shortcutDraft)
					}
					if ui.Button(c, "恢复默认").Clicked() {
						a.ApplyShortcutDraft(settings.DefaultShortcut)
					}
				})
				if msg := a.ShortcutMessage(); msg != "" {
					ui.Text(c, msg).FontSize(11).TextColor(th.TextMuted)
				}

				ui.Divider(c)
				ui.Text(c, "历史清理").FontSize(14).Bold().TextColor(th.Text)
				label(c, "清掉多少天之前的条目（收藏与置顶受保护）", th)
				ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
					ui.NumberInput(c, &pruneDays, 1, 3650, 1)
					if ui.Button(c, "按天数清理").Clicked() {
						a.PruneOlderThanDays(int(pruneDays))
					}
				})
				ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
					if a.ClearConfirming() {
						if ui.Button(c, "再点一次确认清空全部").Clicked() {
							a.ClearAllHistory()
						}
						if ui.Button(c, "取消").Clicked() {
							a.CancelClearAll()
						}
					} else if ui.Button(c, "清空全部历史…").Clicked() {
						a.AskClearAll()
					}
				})

				label(c, "敏感应用白名单（每行一个 bundle id / 进程名；留空用内置表）", th)
				ui.TextArea(c, &sensitiveApps).Height(90)
				ui.Checkbox(c, &ignoreSensitive, "按应用过滤敏感内容（关掉则不拦）")

				label(c, "导出目录（留空 = ~/Downloads）", th)
				ui.TextInput(c, &exportDir)

				ui.Divider(c)
				ui.Text(c, "同步").FontSize(14).Bold().TextColor(th.Text)
				ui.Checkbox(c, &syncOn, "启用同步")
				label(c, "本机名称（远端文件名用）", th)
				ui.TextInput(c, &deviceName)
				label(c, "同步方式", th)
				ui.Segmented(c, &backendKind, "本地目录 / 网盘", "WebDAV")
				if backendKind == 0 {
					label(c, "本地目录（网盘同步盘、移动硬盘都行）", th)
					ui.TextInput(c, &syncDir)
				} else {
					label(c, "WebDAV 地址（如 https://nas.local:5006/data/dav）", th)
					ui.TextInput(c, &remoteURL)
					label(c, "备用地址（Tailscale / 外网；局域网不通时自动切过去）", th)
					ui.TextInput(c, &fallbackURL).Placeholder("https://nas.<tailnet>.ts.net:5006")
					label(c, "用户名 / 口令（口令写进系统钥匙串）", th)
					ui.TextInput(c, &remoteUser)
					ui.TextInput(c, &passphrase).Placeholder("口令 / 同步口令（留空不改）")
					label(c, "远端目录", th)
					ui.TextInput(c, &remoteDir)
					ui.Checkbox(c, &insecure, "允许自签证书（NAS 常见，有风险）")
				}
				ui.Checkbox(c, &encrypt, "端到端加密（用口令派生密钥）")
				label(c, "自动同步间隔（秒；<60 不自动跑）", th)
				ui.NumberInput(c, &interval, 0, 86400, 300)
				ui.Checkbox(c, &realtime, "实时同步（复制后立即推，并按下面间隔拉取）")
				label(c, "实时拉取间隔（秒，2–300）", th)
				ui.NumberInput(c, &pullSecs, 2, 300, 1)

				ui.Divider(c)
				ui.Text(c, "分组").FontSize(14).Bold().TextColor(th.Text)
				label(c, "面板右栏给单条条目选组；同步按名字对齐", th)
				ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
					ui.TextInput(c, a.NewGroupDraft()).Placeholder("新分组名字").Width(200)
					if ui.Button(c, "新建").Clicked() {
						if _, err := a.CreateGroup(*a.NewGroupDraft()); err == nil {
							*a.NewGroupDraft() = ""
						}
					}
				})
				groups := a.Groups()
				if len(groups) == 0 {
					ui.Text(c, "还没有分组").FontSize(11).TextColor(th.TextMuted)
				}
				counts, _ := db.GroupCounts(a.Store.DB)
				for _, g := range groups {
					gid := g.ID
					ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
						ui.TextInput(c, a.RenameDraft(gid, g.Name)).Width(200)
						ui.Text(c, fmt.Sprintf("%d 条", counts[gid])).FontSize(10).TextColor(th.TextMuted)
						if ui.Button(c, "改名").Clicked() {
							_ = a.RenameGroup(gid, *a.RenameDraft(gid, g.Name))
						}
						if ui.Button(c, "删除").Clicked() {
							_ = a.DeleteGroup(gid)
						}
					})
				}

				ui.Divider(c)
				ui.Text(c, "备份").FontSize(14).Bold().TextColor(th.Text)
				label(c, "导出整个历史成一个加密 .cnbak（口令同上）", th)
				ui.Row(c).Gap(8).Children(func() {
					if ui.Button(c, "导出备份").Clicked() {
						if passphrase == "" {
							status = "⚠️ 请先填口令再导出"
						} else if path, err := backup.ExportTo(a.Store.DB, a.Paths, passphrase); err != nil {
							status = "❌ 导出失败：" + err.Error()
						} else {
							status = "✅ 已导出 " + baseName(path)
						}
					}
					if ui.Button(c, "导入最新备份").Clicked() {
						list := backup.Backups(a.Paths)
						if len(list) == 0 {
							status = "⚠️ backups/ 里没有 .cnbak"
						} else if passphrase == "" {
							status = "⚠️ 请先填口令再导入"
						} else if report, err := backup.Import(a.Store.DB, list[0], passphrase); err != nil {
							status = "❌ 导入失败：" + err.Error()
						} else {
							status = fmt.Sprintf("✅ 导入 %d 条（跳过 %d）", report.Inserted, report.Skipped)
							a.Refresh()
						}
					}
				})

				ui.Divider(c)
				label(c, "开机启动", th)
				ui.Row(c).Gap(8).Children(func() {
					if ui.Button(c, "设为登录时启动").Clicked() {
						if err := mygo.App.SetOpenAtLogin(true); err != nil {
							status = "❌ " + err.Error()
						} else {
							status = "✅ 已设为登录时启动"
						}
					}
					if ui.Button(c, "取消登录启动").Clicked() {
						if err := mygo.App.SetOpenAtLogin(false); err != nil {
							status = "❌ " + err.Error()
						} else {
							status = "✅ 已取消"
						}
					}
				})
			})
		})

		ui.Row(c).Gap(8).Children(func() {
			if ui.PrimaryButton(c, "保存").Clicked() {
				apply()
			}
			if ui.Button(c, "立即同步").Clicked() {
				a.StartSync()
				status = "同步已发起"
			}
			if ui.Button(c, "关闭").Clicked() {
				if a.prefs != nil {
					a.prefs.Hide()
				}
			}
		})

		if syncMsg := a.SyncStatus(); syncMsg != "" {
			ui.Text(c, syncMsg).FontSize(11).TextColor(th.TextMuted)
		}
		if status != "" {
			ui.Text(c, status).FontSize(11).TextColor(th.Text)
		}
		ui.Text(c, fmt.Sprintf("数据目录：%s", a.Paths.Root())).FontSize(10).TextColor(th.TextMuted)
	})
}

func label(c *ui.Context, text string, th *ui.Theme) {
	ui.Text(c, text).FontSize(11).TextColor(th.TextMuted)
}

// OpenPrefs 打开设置窗。
func (a *App) OpenPrefs() {
	if a.prefs == nil {
		return
	}
	a.prefs.Show()
	a.prefs.Focus()
}

// SetPrefsWindow 记住设置窗句柄。
func (a *App) SetPrefsWindow(w *mygo.Window) { a.prefs = w }
