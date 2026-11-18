package app

import (
	"fmt"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"

	"clipnest/internal/backup"
	"clipnest/internal/db"
	"clipnest/internal/settings"
	clipsync "clipnest/internal/sync"
	"clipnest/internal/update"
)

// prefsForm 是设置窗的编辑草稿。
//
// 视图是每帧重建的（immediate mode），控件必须绑在**跨帧保留**的地方：绑在
// prefsView 的局部变量上，下一帧又被 settings.json 里的旧值重新赋值一遍，表现就是
// 「+ 只跳一次、− 卡住不动」「复选框点了不回勾」「输入框打不进字」「选项卡选不中」。
// 只有点「保存」才把草稿写回 settings.json / sync.json。
type prefsForm struct {
	loaded bool
	// status 是本窗口的操作结果提示（保存/导出/导入），要跨帧留着才看得见
	status string

	// 面板
	maxItems    float64
	panelHeight float64
	position    int // 0 = 屏幕居中, 1 = 贴屏幕底部
	scroll      ui.ScrollState

	// 快捷键
	shortcut   string
	recording  bool   // 正在录快捷键（等下一个带修饰键的按键）
	recordHint string // 录制中途的提醒（比如「要带 ⌘/⌃/⌥/⇧」）
	pruneDays  float64

	// 清空全部的解锁词（输入「清空」才让确认按钮变成可点）
	clearWord string

	// 过滤与导出
	ignoreSensitive bool
	sensitiveApps   string
	exportDir       string

	// 同步
	syncOn      bool
	deviceName  string
	backendKind int // 0 = 本地目录, 1 = WebDAV
	syncDir     string
	remoteURL   string
	fallbackURL string
	remoteUser  string
	remoteDir   string
	passphrase  string
	insecure    bool
	encrypt     bool
	realtime    bool
	interval    float64
	pullSecs    float64
}

// loadPrefsFormLocked 用已保存的配置填一遍草稿（调用方持有 a.mu）。
func (a *App) loadPrefsFormLocked() {
	s := a.Settings
	cfg := clipsync.LoadConfig(a.Paths)

	var f prefsForm
	f.loaded = true
	f.maxItems = float64(s.MaxItems)
	f.panelHeight = float64(s.PanelHeight)
	if s.PanelPosition == settings.PositionBottom {
		f.position = 1
	}
	f.shortcut = s.Shortcut
	f.pruneDays = 30
	if a.form.pruneDays > 0 {
		f.pruneDays = a.form.pruneDays // 天内清理的数不存盘，但本轮不重置
	}
	f.ignoreSensitive = s.IgnoreSensitiveApps
	f.sensitiveApps = joinLines(s.SensitiveApps)
	f.exportDir = s.ExportDir

	f.syncOn = cfg.Enabled
	f.deviceName = cfg.DeviceName
	if cfg.Backend.Kind == clipsync.BackendWebDav {
		f.backendKind = 1
	}
	f.syncDir = cfg.Backend.Path
	f.remoteURL = cfg.Backend.URL
	f.fallbackURL = cfg.Backend.FallbackURL
	f.remoteUser = cfg.Backend.Username
	f.remoteDir = cfg.Backend.RemoteDir
	f.interval = float64(cfg.IntervalSecs)
	f.pullSecs = float64(cfg.PullIntervalSecs())
	f.insecure = cfg.Backend.InsecureTLS
	f.encrypt = cfg.Encryption.Enabled
	f.realtime = cfg.Realtime

	// 滚动位置不跟着重置，否则用户改到一半重开窗口又要翻回去
	f.scroll = a.form.scroll
	a.form = f
}

// PrefsForm 返回设置窗的草稿（第一次用时从已保存的配置读一份）。
func (a *App) PrefsForm() *prefsForm {
	a.mu.Lock()
	defer a.mu.Unlock()
	if !a.form.loaded {
		a.loadPrefsFormLocked()
	}
	return &a.form
}

// ResetPrefsForm 按已保存的配置重新填草稿：每次打开设置窗都重来一遍，
// 不留上次没保存的编辑（和面板的 ResetPanelState 一个道理）。
func (a *App) ResetPrefsForm() {
	a.mu.Lock()
	wasRecording := a.form.recording
	a.loadPrefsFormLocked()
	a.form.status = ""
	a.form.scroll.Y = 0
	a.mu.Unlock()
	if wasRecording {
		// 上次关窗时还在录快捷键：录制态没了，但键还没挂回去
		a.restoreShortcut()
	}
}

// SavePrefs 把设置窗的草稿写回 settings.json / sync.json（点「保存」时调用）。
func (a *App) SavePrefs() {
	f := a.PrefsForm()
	f.status = "" // 先清上次的结果，否则保存成功后还掉着上一次的报错
	next := a.Settings
	next.MaxItems = int64(f.maxItems)
	next.PanelHeight = int32(f.panelHeight)
	next.PanelPosition = settings.PositionCenter
	if f.position == 1 {
		next.PanelPosition = settings.PositionBottom
	}
	next.SensitiveApps = splitLines(f.sensitiveApps)
	next.IgnoreSensitiveApps = f.ignoreSensitive
	next.ExportDir = f.exportDir
	next = settings.Normalize(next)
	if err := settings.Save(next, a.Paths); err != nil {
		f.status = "❌ 设置保存失败：" + err.Error()
		return
	}
	a.mu.Lock()
	a.Settings = next
	a.mu.Unlock()
	// 面板尺寸是窗口属性：改完立刻生效，不用重启（位置在每次唤出时按设置算）
	if w := a.panelWindow(); w != nil {
		width, _ := w.Size()
		w.SetSize(width, int(next.PanelHeight))
	}

	nextCfg := clipsync.LoadConfig(a.Paths)
	nextCfg.Enabled = f.syncOn
	nextCfg.DeviceName = f.deviceName
	nextCfg.IntervalSecs = int64(f.interval)
	nextCfg.Encryption.Enabled = f.encrypt
	nextCfg.Realtime = f.realtime
	nextCfg.PullSecs = int64(f.pullSecs)
	if f.backendKind == 1 {
		nextCfg.Backend = clipsync.Backend{
			Kind: clipsync.BackendWebDav, URL: f.remoteURL, FallbackURL: f.fallbackURL,
			Username: f.remoteUser, RemoteDir: f.remoteDir, InsecureTLS: f.insecure,
		}
	} else {
		nextCfg.Backend = clipsync.Backend{Kind: clipsync.BackendLocalDir, Path: f.syncDir}
	}
	nextCfg = nextCfg.Normalize()
	if err := clipsync.SaveConfig(nextCfg, a.Paths); err != nil {
		f.status = "❌ 同步配置保存失败：" + err.Error()
		return
	}
	if f.encrypt && f.passphrase != "" {
		if err := clipsync.SecretSet(clipsync.SecretService, nextCfg.DeviceName, f.passphrase); err != nil {
			f.status = "⚠️ 口令没写进" + clipsync.SecretStoreName() + "：" + err.Error()
		}
	}
	if f.backendKind == 1 && f.passphrase != "" {
		// WebDAV 账号口令也存一份；存不进去要报出来，否则之后同步发请求时
		// 只会得到一个「认证失败」，看不出是这一步没落盘
		if err := clipsync.SecretSet(clipsync.WebDavSecretService, nextCfg.DeviceName, f.passphrase); err != nil {
			logf("WEBDAV secret save failed: %v", err)
			f.status = "⚠️ WebDAV 口令没写进" + clipsync.SecretStoreName() + "：" + err.Error()
		}
	}
	f.passphrase = ""
	a.Refresh()
	if f.status == "" {
		f.status = "✅ 已保存"
	}
}

// prefsView 是设置窗：改完点保存写回 settings.json / sync.json。
func (a *App) prefsView(c *ui.Context) {
	th := c.Theme()
	f := a.PrefsForm()

	col := ui.Column(c).Fill().Gap(10).Padding(16).Background(th.Background)
	// 录制快捷键时点到别处就当取消（不然焦点走了还挂着「按下组合键…」，
	// 而那时旧快捷键已经被摘掉，等于用户没了唤出键）。刚点进录制框的那一下
	// 早在上一帧就结束了，不会被这里误判。
	col.ObserveInput(func(ev ui.InputEvent) {
		if f.recording && ev.Kind == ui.InputPointerDown {
			a.StopShortcutRecord()
			f.recordHint = ""
		}
	})
	col.Children(func() {
		ui.Scroll(c).Fill().TrackScroll(&f.scroll).KeepScrollbars(true).Children(func() {
			ui.Column(c).Gap(10).Children(func() {
				ui.Text(c, "clipnest 设置").FontSize(16).Bold().TextColor(th.Text)

				label(c, "历史保留条数（0 = 不限）", th)
				ui.NumberInput(c, &f.maxItems, 0, 1_000_000, 100)

				label(c, "面板高度（点）", th)
				ui.NumberInput(c, &f.panelHeight, float64(settings.PanelHeightMin), float64(settings.PanelHeightMax), 20)

				label(c, "面板位置", th)
				ui.Segmented(c, &f.position, "屏幕居中", "贴屏幕底部")

				label(c, "唤起快捷键（点右边的框，然后在键盘上按组合键；Esc 取消）", th)
				ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
					a.shortcutRecorder(c, f, th)
					if ui.Button(c, "恢复默认").Clicked() {
						a.ApplyShortcutDraft(settings.DefaultShortcut)
					}
				})
				if f.recordHint != "" {
					ui.Text(c, f.recordHint).FontSize(11).TextColor(th.TextMuted)
				}
				if msg := a.ShortcutMessage(); msg != "" {
					ui.Text(c, msg).FontSize(11).TextColor(th.TextMuted)
				}

				ui.Divider(c)
				ui.Text(c, "历史清理").FontSize(14).Bold().TextColor(th.Text)
				label(c, "清掉多少天之前的条目（收藏与置顶受保护）", th)
				ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
					ui.NumberInput(c, &f.pruneDays, 1, 3650, 1)
					if ui.Button(c, "按天数清理").Clicked() {
						f.status = a.PruneOlderThanDays(int(f.pruneDays))
					}
				})
				// 清空是唯一不可撤销的操作：要点两下、输入「清空」、而且必须先备份
				ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
					if a.ClearConfirming() {
						ui.TextInput(c, &f.clearWord).Placeholder("输入 清空").Width(110)
						confirm := ui.Button(c, "确认清空（先自动备份）")
						confirm.Disabled(f.clearWord != "清空")
						if confirm.Clicked() {
							f.status = a.ClearAllHistoryWithBackup(f.passphrase)
							f.clearWord = ""
						}
						if ui.Button(c, "取消").Clicked() {
							f.clearWord = ""
							a.CancelClearAll()
						}
					} else if ui.Button(c, "清空全部历史…").Clicked() {
						f.clearWord = ""
						a.AskClearAll()
					}
				})
				if a.ClearConfirming() {
					label(c, "⚠️ 清空前会先导一份加密备份到 backups/（用这里的口令，没填就用系统凭据里存着的；两者都没有就不清）", th)
				}

				label(c, "敏感应用白名单（每行一个 bundle id / 进程名；留空用内置表）", th)
				ui.TextArea(c, &f.sensitiveApps).Height(90)
				ui.Checkbox(c, &f.ignoreSensitive, "按应用过滤敏感内容（关掉则不拦）")

				label(c, "导出目录（留空 = ~/Downloads）", th)
				ui.TextInput(c, &f.exportDir)

				ui.Divider(c)
				ui.Text(c, "同步").FontSize(14).Bold().TextColor(th.Text)
				ui.Checkbox(c, &f.syncOn, "启用同步")
				label(c, "本机名称（远端文件名用）", th)
				ui.TextInput(c, &f.deviceName)
				label(c, "同步方式", th)
				ui.Segmented(c, &f.backendKind, "本地目录 / 网盘", "WebDAV")
				if f.backendKind == 0 {
					label(c, "本地目录（网盘同步盘、移动硬盘都行）", th)
					ui.TextInput(c, &f.syncDir)
				} else {
					label(c, "WebDAV 地址（如 https://nas.local:5006/data/dav）", th)
					ui.TextInput(c, &f.remoteURL)
					label(c, "备用地址（Tailscale / 外网；局域网不通时自动切过去）", th)
					ui.TextInput(c, &f.fallbackURL).Placeholder("https://nas.<tailnet>.ts.net:5006")
					label(c, "用户名（WebDAV 账号）", th)
					ui.TextInput(c, &f.remoteUser)
					label(c, "远端目录", th)
					ui.TextInput(c, &f.remoteDir)
					ui.Checkbox(c, &f.insecure, "允许自签证书（NAS 常见，有风险）")
				}
				// 口令不分模式：WebDAV 拿它当账号口令，加密与备份拿它派生密钥。
				// 以前它嵌在 WebDAV 分支里，选本地目录时既设不了加密口令，也导不了备份。
				label(c, "口令（WebDAV 账号口令 / 加密与备份口令；写进"+clipsync.SecretStoreName()+"，留空不改）", th)
				ui.TextInput(c, &f.passphrase).Placeholder("口令（留空不改）")
				ui.Checkbox(c, &f.encrypt, "端到端加密（用口令派生密钥）")
				label(c, "自动同步间隔（秒；<60 不自动跑）", th)
				ui.NumberInput(c, &f.interval, 0, 86400, 300)
				ui.Checkbox(c, &f.realtime, "实时同步（复制后立即推，并按下面间隔拉取）")
				label(c, "实时拉取间隔（秒，2–300）", th)
				ui.NumberInput(c, &f.pullSecs, 2, 300, 1)

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
						if f.passphrase == "" {
							f.status = "⚠️ 请先填口令再导出"
						} else if path, err := backup.ExportTo(a.Store.DB, a.Paths, f.passphrase); err != nil {
							f.status = "❌ 导出失败：" + err.Error()
						} else {
							f.status = "✅ 已导出 " + baseName(path)
						}
					}
					if ui.Button(c, "导入备份…").Clicked() {
						if f.passphrase == "" {
							f.status = "⚠️ 请先填口令再导入"
						} else if path := a.pickBackupFile(); path == "" {
							f.status = "已取消导入"
						} else if report, err := backup.Import(a.Store.DB, path, f.passphrase); err != nil {
							f.status = "❌ 导入失败：" + err.Error()
						} else {
							f.status = fmt.Sprintf("✅ 导入 %d 条（跳过 %d）来自 %s",
								report.Inserted, report.Skipped, baseName(path))
							a.Refresh()
						}
					}
				})

				ui.Divider(c)
				label(c, "开机启动", th)
				ui.Row(c).Gap(8).Children(func() {
					if ui.Button(c, "设为登录时启动").Clicked() {
						if err := mygo.App.SetOpenAtLogin(true); err != nil {
							f.status = "❌ " + err.Error()
						} else {
							f.status = "✅ 已设为登录时启动"
						}
					}
					if ui.Button(c, "取消登录启动").Clicked() {
						if err := mygo.App.SetOpenAtLogin(false); err != nil {
							f.status = "❌ " + err.Error()
						} else {
							f.status = "✅ 已取消"
						}
					}
				})
			})
		})

		ui.Divider(c)
		ui.Text(c, "版本与更新").FontSize(14).Bold().TextColor(th.Text)
		label(c, fmt.Sprintf("当前版本 %s · 只检查不自动装（未签名，装包要你自己点）", a.AppVersion()), th)
		ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
			// 更新源下拉：切了就立刻存（不然点「检查更新」还按旧源跑）
			srcIdx := update.IndexOf(*a.UpdateSourceDraft())
			pick := update.SourceLabels[srcIdx]
			ui.Select(c, &pick, update.SourceLabels).Width(96)
			if pick != update.SourceLabels[srcIdx] {
				chosen := update.SourceOf(pick)
				a.later(func() { a.switchUpdateSource(chosen) })
			}
			ui.TextInput(c, a.UpdateRepoDraft()).Placeholder("仓库（留空用内置镜像）").Grow(1)
			if ui.Button(c, "保存更新源").Clicked() {
				a.later(func() {
					a.SaveUpdateSource(*a.UpdateSourceDraft(), *a.UpdateRepoDraft(), *a.UpdateAPIBaseDraft())
				})
			}
		})

		// 自定义源要填 API 根，自动源给一句说明：都很长，各占一行（与上面那行挤会溢出）
		switch *a.UpdateSourceDraft() {
		case update.SourceCustom:
			ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
				ui.Text(c, "API 根").FontSize(11).TextColor(th.TextMuted)
				ui.TextInput(c, a.UpdateAPIBaseDraft()).Placeholder("https://api.github.com").Grow(1)
			})
		case update.SourceAuto:
			label(c, "自动：先查 Gitee，不通再查 GitHub（国内推荐）", th)
		}

		ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
			if a.UpdateChecking() {
				ui.Text(c, "检查中…").FontSize(11).TextColor(th.TextMuted)
			} else if ui.Button(c, "检查更新").Clicked() {
				a.later(a.CheckUpdate)
			}
			msg, hasURL := a.UpdateStatus()
			if hasURL {
				if ui.Button(c, "打开发布页").Clicked() {
					a.later(a.OpenReleasePage)
				}
			}
			if msg != "" {
				ui.Text(c, msg).FontSize(11).TextColor(th.TextMuted)
			}
		})

		ui.Row(c).Gap(8).Children(func() {
			if ui.PrimaryButton(c, "保存").Clicked() {
				a.SavePrefs()
			}
			if ui.Button(c, "立即同步").Clicked() {
				a.StartSync()
				f.status = "同步已发起"
			}
			if ui.Button(c, "关闭").Clicked() {
				a.HidePrefs()
			}
		})

		if syncMsg := a.SyncStatus(); syncMsg != "" {
			ui.Text(c, syncMsg).FontSize(11).TextColor(th.TextMuted)
		}
		if f.status != "" {
			ui.Text(c, f.status).FontSize(11).TextColor(th.Text)
		}
		ui.Text(c, fmt.Sprintf("数据目录：%s", a.Paths.Root())).FontSize(10).TextColor(th.TextMuted)
	})
	// 本窗口也排了延后动作（切更新源、检查更新…）：必须在自己的帧末跑掉。
	// a.runPending 只在面板视图里跑的话，这里排的活永远等不到执行。
	a.runPending()
}

func label(c *ui.Context, text string, th *ui.Theme) {
	ui.Text(c, text).FontSize(11).TextColor(th.TextMuted)
}

// shortcutRecorder 是「点一下、再按组合键」的快捷键录制框。
//
// 不用输入框：让用户手打 accelerator（CmdOrCtrl+Shift+V）既容易打错，也没人记得语法。
// 点一下进入录制态，下一个带修饰键的按键就是新快捷键，Esc 取消。
func (a *App) shortcutRecorder(c *ui.Context, f *prefsForm, th *ui.Theme) {
	text := HotkeyText(f.shortcut)
	border := th.Border
	fg := th.Text
	if f.recording {
		text = "按下组合键…"
		border = th.Accent
		fg = th.Accent
	}
	box := ui.Row(c).Width(200).Height(30).Focusable().
		Gap(6).AlignItems(ui.Center).Justify(ui.Center).
		Radius(th.Radius).Border(1, border).Background(th.Surface)
	box.Children(func() {
		ui.Text(c, text).FontSize(13).TextColor(fg)
	})
	box.OnClick(func() { a.StartShortcutRecord() })
	// 录制时自己吃掉键盘：换成输入框的话，带 ⌘ 的组合会被当成文本编辑命令
	box.HandleInput(func(ev ui.InputEvent) bool {
		if !f.recording || ev.Kind != ui.InputKeyDown || ev.Repeat {
			return false
		}
		if ev.Key == ui.KeyEscape || ev.PhysicalKey == ui.KeyEscape {
			a.StopShortcutRecord()
			f.recordHint = "已取消"
			return true
		}
		if acc, ok := AcceleratorFromInput(ev); ok {
			f.recordHint = ""
			a.ApplyShortcutDraft(acc)
			a.StopShortcutRecord()
			return true
		}
		if _, ok := InputKeyName(ev); !ok {
			return true // 纯修饰键、媒体键这类认不出的键：接着等
		}
		f.recordHint = "要按住 ⌘ / ⌃ / ⌥ 再按一个键（⇧ 单独不算；也可以直接按 F1–F12）"
		return true
	})
	if f.recording {
		box.Focus()
	}
}

// HidePrefs 收起设置窗；正在录快捷键就先收尾（否则那个键会一直没挂上），
// 清空的「确认态」也一并取消，下次打开不会一上来就是待确认。
func (a *App) HidePrefs() {
	a.StopShortcutRecord()
	a.CancelClearAll()
	if a.prefs != nil {
		a.prefs.Hide()
	}
}

// OpenPrefs 打开设置窗。
func (a *App) OpenPrefs() {
	if a.prefs == nil {
		return
	}
	a.ResetPrefsForm()
	a.prefs.Show()
	a.prefs.Focus()
}

// SetPrefsWindow 记住设置窗句柄。
func (a *App) SetPrefsWindow(w *mygo.Window) { a.prefs = w }
