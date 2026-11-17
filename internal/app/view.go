package app

import (
	"fmt"
	"os"
	"strings"
	"sync"

	"github.com/egoist/mygo/transfer"
	"github.com/egoist/mygo/ui"

	"clipnest/internal/model"
	"clipnest/internal/paste"
)

// 预览区宽度（点）与图片边界。
const (
	previewWidth = 260
	thumbSize    = 26
)

// bitmap 缓存：图片条目按文件路径缓存 GPU 位图（每帧解码会卡）。
var (
	bitmapMu  sync.Mutex
	bitmapMap = map[string]*ui.Bitmap{}
)

// bitmapFor 读并缓存一张位图；读不到就是 nil。
func bitmapFor(path string) *ui.Bitmap {
	bitmapMu.Lock()
	if b, ok := bitmapMap[path]; ok {
		bitmapMu.Unlock()
		return b
	}
	bitmapMu.Unlock()

	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	b, err := ui.DecodeBitmap(data)
	if err != nil {
		return nil
	}
	bitmapMu.Lock()
	if len(bitmapMap) > 128 { // 简单上限：满了就整个清掉，避免无限增长
		bitmapMap = map[string]*ui.Bitmap{}
	}
	bitmapMap[path] = b
	bitmapMu.Unlock()
	return b
}

// panelView 是面板的视图函数。
func (a *App) panelView(c *ui.Context) {
	th := c.Theme()
	// 面板是自绘的浮动窗：菜单也用窗口内画的那种，跟整体观感一致，
	// 而且不依赖 app 是否在前台（系统菜单在非激活的 accessory app 里弹不出来）
	// c.SetMenuPresentation(ui.PopoverMenus)

	root := ui.Column(c).Fill().Gap(8).Padding(8).Background(th.Background)
	root.Children(func() {
		// 顶部这条空白就是标题栏：按住拖可以移动面板（DragWindow 只认「最深命中元素」）
		// 顶部居中的小把手：按住可以拖面板（没有标题栏，靠它移动）
		ui.Row(c).Fill().Height(8).Justify(ui.Center).Children(func() {
			grip := ui.Box(c).Size(80, 14).Radius(4).Background(th.Surface)
			if b := grip.Bounds(); b.W > 0 && os.Getenv("CLIPNEST_DEBUG_RECTS") != "" {
				px, py := 0, 0
				if w := a.panelWindow(); w != nil {
					px, py = w.Position()
				}
				logf("RECT grip win=(%d,%d) x=%.0f y=%.0f w=%.0f h=%.0f", px, py, b.X, b.Y, b.W, b.H)
			}
			grip.DragWindow()
		})
		if !paste.Trusted() && !a.Settings.HideAccessibilityHint {
			// FillWidth 而不是 Fill：在 Column 里 Fill 会连高度一起吃掉，
			// 把下面的搜索框/列表全顶到窗口外（测试里没 AX 授权就会踩到）
			ui.Row(c).FillWidth().Gap(6).Children(func() {
				ui.Text(c, "⚠️ 没有辅助功能授权，回车只复制不粘贴").
					FontSize(11).TextColor(th.Warning).Grow(1)
				grantBtn := ui.Button(c, "去授权")
				if b := grantBtn.Bounds(); b.W > 0 && os.Getenv("CLIPNEST_DEBUG_RECTS") != "" {
					logf("RECT grant x=%.0f y=%.0f w=%.0f h=%.0f", b.X, b.Y, b.W, b.H)
				}
				if grantBtn.Clicked() {
					a.Dismiss() // 面板是 layer=3 的浮动窗，先收起来别抢镜头
					a.RequestAccessibility()
				}
				if ui.Button(c, "忽略").Clicked() {
					a.HideAXHint()
				}
			})
		}
		// 布局跟着面板宽度走：窄了先去掉条数、再收窄预览、最后只留列表
		l := layoutFor(c.Root().Bounds().W)

		if sb := a.searchField(c).Bounds(); os.Getenv("CLIPNEST_DEBUG_RECTS") != "" {
			logf("DBG search=(%.1f,%.1f,%.1fx%.1f)", sb.X, sb.Y, sb.W, sb.H)
		}
		if fb := a.filterBar(c, l).Bounds(); os.Getenv("CLIPNEST_DEBUG_RECTS") != "" {
			logf("DBG filter=(%.1f,%.1f,%.1fx%.1f)", fb.X, fb.Y, fb.W, fb.H)
		}
		a.bindKeys(root, c)

		ui.Row(c).Grow(1).Gap(8).Children(func() {
			a.listView(c)
			if l.showPreview {
				a.previewView(c, l)
			}
		})

		sel, total := a.Selection()
		hints := Hints()
		if l.compactHints {
			hints = CompactHints()
		}
		footer := hints
		if total > 0 {
			footer = fmt.Sprintf("%d/%d · %s", min(sel, total-1)+1, total, hints)
		}
		if status := a.Status(); status != "" {
			footer = status + " · " + footer
		}
		ui.Text(c, footer).FontSize(11).TextColor(th.Text)
	})

	a.runPending()
}

// searchField 是搜索框（首次显示自动聚焦）。
func (a *App) searchField(c *ui.Context) ui.Element {
	th := c.Theme()
	q := a.Query()
	in := ui.TextInput(c, &q).
		Placeholder("搜索剪贴板历史…").
		FontSize(13).
		Padding(8, 6).
		Radius(6).
		Background(th.Surface).
		TextColor(th.Text).
		PlaceholderColor(th.TextMuted).
		CaretColor(th.Text).
		AutoFocus().
		Bind(&a.searchInput)
	if a.takeFocusSearchNext() {
		in.Focus() // 刚唤出：焦点放回搜索框，可以直接打字
	}
	a.bindSearchInput(&a.searchInput, c)
	if in.Changed() {
		a.later(func() { a.SetQuery(q) })
	}
	if in.Submitted() {
		a.later(func() {
			if row, ok := a.Selected(); ok {
				a.Paste(row)
			}
		})
	}
	return in
}

// panelLayout 是依面板宽度算出来的布局取舍。
type panelLayout struct {
	showPreview   bool    // 右栏预览
	previewW      float32 // 预览栏宽度
	showCount     bool    // 筛选栏右侧的「共 N 条」
	compactFilter bool    // 筛选标签用短文案
	compactHints  bool    // 底部提示用短文案
}

// layoutFor 按宽度决定放得下什么：720（默认）全给；窄了先省条数、再收预览，最后只留列表。
func layoutFor(w float32) panelLayout {
	l := panelLayout{showPreview: true, previewW: previewWidth, showCount: true}
	switch {
	case w < 500: // 只剩列表
		l.showPreview = false
		l.showCount = false
		l.compactFilter = true
		l.compactHints = true
	case w < 620: // 预览很窄，筛选与提示都用短文案
		l.previewW = 180
		l.showCount = false
		l.compactFilter = true
		l.compactHints = true
	case w < 700: // 预览收窄
		l.previewW = 220
		l.showCount = false
		l.compactHints = true
	case w < 780: // 默认宽度：只把底栏提示缩短
		l.compactHints = true
	}
	return l
}

// filterBar 是搜索框下面那排筛选：类型分段 + 分组下拉。
func (a *App) filterBar(c *ui.Context, l panelLayout) ui.Element {
	th := c.Theme()
	kind, groupNow := a.FilterState()

	kindNow := kind
	labels := []string{"全部", "收藏", "置顶", "图片", "文件"}
	if l.compactFilter {
		labels = []string{"全部", "★", "📌", "图", "件"}
	}
	// 这一行留白处可以拖着走（frameless 面板没有标题栏）
	bar := ui.Row(c).Height(26).Gap(8).AlignItems(ui.Center).DragWindow()
	bar.Children(func() {
		if ui.Segmented(c, &kindNow, labels...).Changed() {
			k := kindNow
			a.later(func() { a.SetKindFilter(k) })
		}
		groups := a.Groups()
		if len(groups) > 0 {
			names := make([]string, 0, len(groups)+2)
			names = append(names, "全部分组", "未分组")
			for _, g := range groups {
				names = append(names, g.Name)
			}
			current := "全部分组"
			switch groupNow {
			case "":
			case groupNone:
				current = "未分组"
			default:
				current = groupNow
			}
			choice := current
			selW := float32(140)
			if l.compactFilter {
				selW = 100
			}
			ui.Select(c, &choice, names).Width(selW)
			if choice != current {
				pick := choice
				a.later(func() {
					switch pick {
					case "全部分组":
						a.SetGroupFilter("")
					case "未分组":
						a.SetGroupFilter(groupNone)
					default:
						a.SetGroupFilter(pick)
					}
				})
			}
		}
		if l.showCount {
			ui.Text(c, fmt.Sprintf("共 %d 条", len(a.Rows()))).FontSize(10).TextColor(th.TextMuted)
		}
		// 剩下的空白当窗口拖拽把手（比手指还好用，且点不到任何控件）
		ui.Box(c).Grow(1).FillHeight().DragWindow()
	})
	return bar
}

// bindKeys 绑定面板的键盘动作（焦点在搜索框里也要能生效）。
func (a *App) bindKeys(root ui.Element, c *ui.Context) {
	rowAction := func(fn func(Row) bool) func() {
		return func() {
			if row, ok := a.Selected(); ok {
				fn(row)
			}
		}
	}

	root.OnShortcut(0, ui.KeyEscape, func() { a.Dismiss() })
	root.OnShortcut(0, ui.KeyDown, func() { a.Move(1) })
	root.OnShortcut(0, ui.KeyUp, func() { a.Move(-1) })
	root.OnShortcut(0, ui.KeyRight, func() { a.Move(1) })
	root.OnShortcut(0, ui.KeyLeft, func() { a.Move(-1) })

	root.OnShortcut(ui.Cmd, ui.KeyD, rowAction(func(r Row) bool { return a.Remove(r) }))
	root.OnShortcut(ui.Cmd, ui.KeyP, rowAction(func(r Row) bool { return a.ToggleFlag(r, FlagPinned) }))
	root.OnShortcut(ui.Cmd, ui.KeyS, rowAction(func(r Row) bool { return a.ToggleFlag(r, FlagFavorite) }))
	root.OnShortcut(ui.Cmd, ui.KeyE, rowAction(func(r Row) bool { return a.ToggleFlag(r, FlagSensitive) }))
	root.OnShortcut(ui.Cmd, ui.KeyY, rowAction(func(r Row) bool { return a.Copy(r) }))
	root.OnShortcut(ui.Cmd, ui.KeyO, rowAction(func(r Row) bool {
		if file, err := a.Export(r); err != nil {
			a.SetStatus("❌ " + err.Error())
		} else {
			a.SetStatus("已导出 " + baseName(file) + "（⌘V 可粘文件）")
		}
		return true
	}))

	for i := 0; i < 9; i++ {
		slot := i
		key := digitKey(i + 1)
		if key == ui.KeyUnknown {
			continue
		}
		root.OnShortcut(ui.Cmd, key, func() {
			if a.SelectSlot(slot) {
				if row, ok := a.RowAt(slot); ok {
					a.Paste(row)
				}
			}
		})
	}
}

// listView 是左栏列表。
func (a *App) listView(c *ui.Context) {
	th := c.Theme()
	q := a.Query()
	rows := a.Rows()
	sel, _ := a.Selection()

	var accY float32
	var selTop, selH float32
	ui.Column(c).Fill().Children(func() {
		scroll := ui.Scroll(c).Grow(1).TrackScroll(&a.ListScroll)
		b := scroll.Bounds()
		if b.H > 0 {
			// 窗口被拖动放大/缩小时视口会变：重新保证选中行露出来
			if a.listViewH != 0 && b.H != a.listViewH {
				a.requestScrollToSelection()
			}
			a.listViewH = b.H
		}
		if os.Getenv("CLIPNEST_DEBUG_RECTS") != "" {
			_, wh := 0, 0
			if w := a.panelWindow(); w != nil {
				_, wh = w.Size()
			}
			logf("DBG bounds scroll=(%.1f,%.1f,%.1fx%.1f) winH=%d viewH=%.1f",
				b.X, b.Y, b.W, b.H, wh, a.listViewH)
		}
		scroll.Children(func() {
			ui.Column(c).Fill().Gap(2).Children(func() {
				for i := range rows {
					index := i
					row := rows[i]
					bg := th.Background
					if index == sel {
						bg = th.Accent
					}
					fg := th.Text
					if index == sel {
						fg = th.AccentText
					}

					rowID := row.ID
					rowEl := ui.Column(c).Key(fmt.Sprintf("row-%d", row.ID)).
						Radius(6).
						Padding(6, 4).
						Background(bg).
						OnClick(func() {
							// 点完之后把焦点还给搜索框：否则方向键会被滚动容器拿去滚列表
							a.later(func() { a.Select(index); a.refocusSearch() })
						}).
						// 按住往窗口外拖 = 拖拽导出（文本给纯文本，图片/文件给文件）
						DragDataFrom(func() transfer.Data { return a.DragData(rowID) })
					rowH := rowEl.Bounds().H
					if rowH <= 0 {
						rowH = 28 // 首帧还没有高度，用估的
					}
					if index == sel {
						selTop = accY
						selH = rowH
					}
					accY += rowH + 2
					if index == 0 && os.Getenv("CLIPNEST_DEBUG_RECTS") != "" {
						if b := rowEl.Bounds(); b.W > 0 {
							logf("RECT row0 x=%.0f y=%.0f w=%.0f h=%.0f", b.X, b.Y, b.W, b.H)
						}
					}
					rowEl.Children(func() {
						ui.Row(c).Fill().Gap(6).Children(func() {
							if row.ThumbPath != "" {
								if b := bitmapFor(row.ThumbPath); b != nil {
									ui.Image(c, b).Size(thumbSize, thumbSize).Fit(ui.Contain).Radius(3)
								}
							}
							if index < 9 {
								ui.Text(c, Slot(index)).FontSize(9).TextColor(fg)
							}
							ui.Text(c, row.Tag).FontSize(10).TextColor(fg)
							mark := ""
							if row.Pinned {
								mark += "📌 "
							}
							if row.Favorite {
								mark += "★ "
							}
							if row.Sensitive {
								mark += "🔒 "
							}
							a.summaryText(c, mark+row.Summary, q, fg, th).FontSize(12).TextColor(fg).Grow(1).Ellipsis("…").
								OnClick(func() {
									a.later(func() { a.Select(index); a.refocusSearch() })
								}).
								ContextMenu(func(m *ui.Menu) { a.rowMenu(m, row) })
							if row.UseCount > 1 {
								ui.Text(c, fmt.Sprintf("×%d", row.UseCount)).FontSize(9).TextColor(fg)
							}
							ui.Text(c, row.Time).FontSize(9).TextColor(fg)
						})
					})
				}
				if len(rows) > 0 && a.takeScrollToSelection() {
					a.scrollSelectionIntoView(selTop, selH)
					if os.Getenv("CLIPNEST_DEBUG_RECTS") != "" {
						logf("DBG scroll sel=%d top=%.1f h=%.1f view=%.1f y=%.1f maxY=%.1f",
							sel, selTop, selH, a.listViewH, a.ListScroll.Y, a.ListScroll.MaxY)
					}
				}
				if len(rows) == 0 {
					title, hint := emptyState(a.Query())
					ui.Column(c).Fill().Justify(ui.Center).AlignItems(ui.Center).Gap(4).Children(func() {
						ui.Text(c, title).FontSize(13).TextColor(th.TextMuted)
						ui.Text(c, hint).FontSize(11).TextColor(th.TextMuted)
					})
				}
			})
		})
	})
}

// previewView 是右栏：一张卡片，从上到下是头部、内容、属性、备注、动作。
//
// 排版原则：卡片底色与列表行区分层次；头部一行给出类型/时间/次数；
// 内容区限高，备注吃掉剩余高度——列再高也不会显得空。
func (a *App) previewView(c *ui.Context, l panelLayout) {
	th := c.Theme()
	row, ok := a.Selected()

	card := ui.Column(c).Width(l.previewW).FillHeight().Gap(8).
		Padding(12).Radius(10).Background(th.Surface).Justify(ui.Start).
		// 点右栏空白/图片/卡片背景后把焦点还给搜索框，方向键继续选列表
		OnClick(func() { a.later(a.refocusSearch) })
	if b := card.Bounds(); b.H > 0 {
		a.previewH = b.H
		a.previewCard = b
	}
	card.Children(func() {
		if !ok {
			ui.Column(c).Fill().Justify(ui.Center).AlignItems(ui.Center).Gap(4).Children(func() {
				ui.Text(c, "没有选中条目").FontSize(12).TextColor(th.TextMuted)
				ui.Text(c, "用 ↑↓ 或点击左侧列表").FontSize(11).TextColor(th.TextMuted)
			})
			return
		}

		if os.Getenv("CLIPNEST_DEBUG_RECTS") != "" {
			hb := a.previewHeader(c, row, th).Bounds()
			logf("DBG pv header=(%.1f,%.1f,%.1fx%.1f) card=(%.1fx%.1f)", hb.X, hb.Y, hb.W, hb.H, card.Bounds().W, card.Bounds().H)
		} else {
			a.previewHeader(c, row, th)
		}

		// 内容：图片整张看，文本限高滚动看
		switch {
		case row.Kind == model.KindImage:
			a.previewImage(c, row, l, th)
		case row.Kind == model.KindText || row.Kind == model.KindHtml || row.Kind == model.KindRtf:
			// 只看个大概：截断显示，不做内嵌滚动（长文本来回滚也没人看）
			if content := excerpt(row.Content, 220); content != "" {
				a.summaryText(c, content, a.Query(), th.Text, th).FontSize(12).Wrap().
					MaxHeight(previewContentBudget(a.previewH))
			}
		case row.Kind == model.KindFiles:
			ui.Text(c, excerpt(row.Content, 300)).FontSize(11).TextColor(th.TextMuted).Wrap()
		}

		ui.Divider(c)
		a.previewGroup(c, row, th)

		// 备注吃掉剩下的高度，卡片自然填满
		ui.Row(c).Height(16).Gap(8).AlignItems(ui.Center).Children(func() {
			ui.Text(c, "备注").FontSize(11).TextColor(th.TextMuted).Width(40)
			n := len([]rune(row.Note))
			counter := fmt.Sprintf("%d / %d", n, noteMaxRunes)
			if n > noteMaxRunes {
				counter = fmt.Sprintf("%d / %d（超出部分不会保存）", n, noteMaxRunes)
			}
			ui.Text(c, counter).FontSize(10).TextColor(th.TextMuted)
		})
		note := a.NoteValue(row.ID, row.Note)
		if ui.TextArea(c, note).Grow(1).MinHeight(72).FontSize(11).
			Placeholder("写点什么，比如这条以后怎么找…").Changed() {
			text := *note
			a.later(func() { a.SaveNote(row.ID, text) })
		}

		// 动作：和键盘快捷键对应，省得去记
		ui.Row(c).Height(30).Gap(6).Children(func() {
			iconAction(c, th, IconCopy, "复制", func() {
				r := row
				a.later(func() { a.Copy(r) })
			})
			iconAction(c, th, IconExport, "导出", func() {
				r := row
				a.later(func() {
					if file, err := a.Export(r); err != nil {
						a.SetStatus("❌ " + err.Error())
					} else {
						a.SetStatus("已导出 " + baseName(file) + "（⌘V 可粘文件）")
					}
				})
			})
			ui.Box(c).Grow(1)
			ui.Text(c, "⌘Y / ⌘O").FontSize(10).TextColor(th.TextMuted)
		})
	})
}

// previewHeader 是卡片顶部：类型胶囊 + 时间/次数 + 标记。
func (a *App) previewHeader(c *ui.Context, row Row, th *ui.Theme) ui.Element {
	head := ui.Row(c).Fill().Height(20).Gap(6).AlignItems(ui.Center)
	head.Children(func() {
		ui.Badge(c, row.Tag)
		meta := row.Time
		if row.UseCount > 1 {
			meta += fmt.Sprintf(" · 用过 %d 次", row.UseCount)
		}
		ui.Text(c, meta).FontSize(11).TextColor(th.TextMuted)
		ui.Box(c).Grow(1)
		mark := ""
		if row.Pinned {
			mark += "📌"
		}
		if row.Favorite {
			mark += "★"
		}
		if row.Sensitive {
			mark += "🔒"
		}
		if mark != "" {
			ui.Text(c, mark).FontSize(11).TextColor(th.Accent)
		}
	})
	return head
}

// previewChromeH 是卡片里除内容区与备注以外的固定高度：
// 内边距 24 + 头部 20 + 分隔线 1 + 分组 28 + 备注标签 16 + 动作 30 + 6 个间距 48。
const previewChromeH = 167

// previewNoteReserve 是留给备注的最小高度。
const previewNoteReserve = 84

// noteMaxRunes 是备注的字数上限（超出的部分不保存）。
const noteMaxRunes = 500

// previewContentBudget 是内容区能用多高：卡片实际高度减去固定块，再给备注留位置。
func previewContentBudget(cardH float32) float32 {
	if cardH <= 0 {
		cardH = 432 // 首帧还没量到，用默认面板高度估
	}
	budget := cardH - previewChromeH - previewNoteReserve
	if budget < 80 {
		budget = 80
	}
	if budget > 320 {
		budget = 320
	}
	return budget
}

// previewImage 在固定高度的框里居中显示原图，尺寸字也在框内——高图不会压到下面的控件。
func (a *App) previewImage(c *ui.Context, row Row, l panelLayout, th *ui.Theme) {
	if row.OriginPath == "" {
		ui.Text(c, "⚠️ 原图文件缺失").FontSize(11).TextColor(th.Warning)
		return
	}
	b := bitmapFor(row.OriginPath)
	if b == nil {
		ui.Text(c, "⚠️ 原图读不出来（文件可能已被清理）").FontSize(11).TextColor(th.Warning)
		return
	}
	box := previewContentBudget(a.previewH)
	maxW := l.previewW - 24
	// 小图按栏宽放大（最多 2.4 倍），大图等比缩到框内
	w, h := fitSizeUp(row.Width, row.Height, maxW, box-18, 2.4)

	// 容器要有底色才进命中链（裸 Image 摸不到点击）；点它 = 看大图
	shot := row
	area := ui.Column(c).Height(box).Background(th.Surface).Radius(8).
		Justify(ui.Center).AlignItems(ui.Center).Gap(6).Cursor(ui.CursorPointer).
		OnClick(func() { a.later(func() { a.ShowImage(shot) }) })
	a.imageRect = area.Bounds()
	if os.Getenv("CLIPNEST_DEBUG_RECTS") != "" {
		b := a.imageRect
		logf("DBG imgrect=(%.0f,%.0f) %.0fx%.0f", b.X, b.Y, b.W, b.H)
	}
	area.Children(func() {
		ui.Image(c, b).Size(w, h).Fit(ui.Contain).Radius(8)
		if row.Width > 0 && row.Height > 0 {
			pct := 100
			if row.Width > 0 {
				pct = int(100 * w / float32(row.Width))
			}
			ui.Text(c, fmt.Sprintf("%d × %d · 显示 %d%% · 点击看大图", row.Width, row.Height, pct)).
				FontSize(10).TextColor(th.TextMuted)
		}
	})
}

// previewGroup 是「分组」一行：标签固定宽度，下拉占满剩余。
func (a *App) previewGroup(c *ui.Context, row Row, th *ui.Theme) {
	groups := a.Groups()
	names := make([]string, 0, len(groups)+1)
	names = append(names, "未分组")
	for _, g := range groups {
		names = append(names, g.Name)
	}
	current := "未分组"
	if row.GroupName != "" {
		current = row.GroupName
	}
	choice := current
	ui.Row(c).Fill().Height(28).Gap(8).AlignItems(ui.Center).Children(func() {
		ui.Text(c, "分组").FontSize(11).TextColor(th.TextMuted).Width(40)
		ui.Select(c, &choice, names).Grow(1)
	})
	if choice != current {
		pick := choice
		a.later(func() {
			defer a.refocusSearch()
			var gid *int64
			for _, g := range groups {
				if g.Name == pick {
					id := g.ID
					gid = &id
					break
				}
			}
			a.MoveToGroup(row.ID, gid)
		})
	}
}

// summaryText 是带搜索高亮的文本：命中 query 的片段用强调色 + 加粗。
//
// 没查询词或者没命中时就是普通 Text；命中时切成 RichText 分段。
func (a *App) summaryText(c *ui.Context, text, query string, base ui.Color, th *ui.Theme) ui.Element {
	spans := highlightSpans(text, query, base, th.Accent)
	if len(spans) == 0 {
		return ui.Text(c, text)
	}
	return ui.RichText(c, spans...).TextColor(base)
}

// highlightSpans 把 text 里所有命中 query 的片段切出来（大小写不敏感）。
// 没命中返回 nil，调用方退回普通文本。
func highlightSpans(text, query string, base, hi ui.Color) []ui.Span {
	q := []rune(strings.ToLower(strings.TrimSpace(query)))
	if len(q) == 0 {
		return nil
	}
	runes := []rune(text)
	lower := []rune(strings.ToLower(text))

	var spans []ui.Span
	hit := false
	start := 0
	for i := 0; i+len(q) <= len(lower); {
		match := true
		for j := range q {
			if lower[i+j] != q[j] {
				match = false
				break
			}
		}
		if !match {
			i++
			continue
		}
		hit = true
		if i > start {
			spans = append(spans, ui.Span{Text: string(runes[start:i]), Color: base})
		}
		spans = append(spans, ui.Span{Text: string(runes[i : i+len(q)]), Color: hi, Weight: 600})
		i += len(q)
		start = i
	}
	if !hit {
		return nil
	}
	if start < len(runes) {
		spans = append(spans, ui.Span{Text: string(runes[start:]), Color: base})
	}
	return spans
}

// excerpt 把内容压平换行并按字符截断。
func excerpt(text string, maxChars int) string {
	flat := strings.NewReplacer("\n", " ", "\r", " ").Replace(text)
	runes := []rune(flat)
	if len(runes) <= maxChars {
		return flat
	}
	return string(runes[:maxChars]) + "…"
}

// emptyState 是列表空态文案：区分「搜不到」与「还没有历史」。
func emptyState(q string) (title, hint string) {
	if q != "" {
		return fmt.Sprintf("没有匹配「%s」的条目", q), "换个更短的关键词试试"
	}
	return "还没有历史记录", "复制文字、图片或文件，会自动收进这里"
}

// fitSize 按比例把图片塞进 maxW × maxH（等比，不裁切）。
func fitSize(w, h int64, maxW, maxH float32) (float32, float32) {
	return fitSizeUp(w, h, maxW, maxH, 1)
}

// fitSizeUp 同 fitSize，但允许放大到 maxUpscale 倍（小图按栏宽放大，不显得小）。
func fitSizeUp(w, h int64, maxW, maxH, maxUpscale float32) (float32, float32) {
	if w <= 0 || h <= 0 {
		return maxW, maxH
	}
	scale := maxW / float32(w)
	if s := maxH / float32(h); s < scale {
		scale = s
	}
	if scale > maxUpscale {
		scale = maxUpscale
	}
	return float32(w) * scale, float32(h) * scale
}

// digitKey 把 1-9 映射成 ui 的按键常量。
func digitKey(n int) ui.Key {
	switch n {
	case 1:
		return ui.Key1
	case 2:
		return ui.Key2
	case 3:
		return ui.Key3
	case 4:
		return ui.Key4
	case 5:
		return ui.Key5
	case 6:
		return ui.Key6
	case 7:
		return ui.Key7
	case 8:
		return ui.Key8
	case 9:
		return ui.Key9
	}
	return ui.KeyUnknown
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
