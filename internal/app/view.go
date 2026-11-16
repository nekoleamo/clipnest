package app

import (
	"fmt"
	"os"
	"sync"

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
	var after []func()
	applyAfter := func() {
		for _, fn := range after {
			fn()
		}
	}

	root := ui.Column(c).Fill().Gap(8).Padding(8).Background(th.Background)
	root.Children(func() {
		if !paste.Trusted() {
			ui.Row(c).Fill().Gap(6).Children(func() {
				ui.Text(c, "⚠️ 没有辅助功能授权，回车只复制不粘贴").
					FontSize(11).TextColor(th.Warning).Grow(1)
				if ui.Button(c, "去授权").Clicked() {
					paste.PromptTrust() // 弹系统授权提示，把 app 加进名单
					openAccessibilitySettings()
				}
			})
		}
		a.searchField(c, &after)
		a.bindKeys(c, &after)

		ui.Row(c).Fill().Gap(8).Children(func() {
			a.listView(c, &after)
			a.previewView(c)
		})

		sel, total := a.Selection()
		footer := Hints()
		if total > 0 {
			footer = fmt.Sprintf("%d/%d · %s", min(sel, total-1)+1, total, Hints())
		}
		if status := a.Status(); status != "" {
			footer = status + " · " + footer
		}
		ui.Text(c, footer).FontSize(11).TextColor(th.Text)
	})

	applyAfter()
}

// searchField 是搜索框（首次显示自动聚焦）。
func (a *App) searchField(c *ui.Context, after *[]func()) {
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
		AutoFocus()
	if in.Changed() {
		*after = append(*after, func() { a.SetQuery(q) })
	}
	if in.Submitted() {
		*after = append(*after, func() {
			if row, ok := a.Selected(); ok {
				a.Paste(row)
			}
		})
	}
}

// bindKeys 绑定面板的键盘动作（焦点在搜索框里也要能生效）。
func (a *App) bindKeys(c *ui.Context, after *[]func()) {
	rowAction := func(fn func(Row) bool) func() {
		return func() {
			if row, ok := a.Selected(); ok {
				fn(row)
			}
		}
	}

	c.OnWindowShortcut(0, ui.KeyEscape, func() { a.Dismiss() })
	c.OnWindowShortcut(0, ui.KeyDown, func() { a.Move(1) })
	c.OnWindowShortcut(0, ui.KeyUp, func() { a.Move(-1) })
	c.OnWindowShortcut(0, ui.KeyRight, func() { a.Move(1) })
	c.OnWindowShortcut(0, ui.KeyLeft, func() { a.Move(-1) })

	c.OnWindowShortcut(ui.Cmd, ui.KeyD, rowAction(func(r Row) bool { return a.Remove(r) }))
	c.OnWindowShortcut(ui.Cmd, ui.KeyP, rowAction(func(r Row) bool { return a.ToggleFlag(r, FlagPinned) }))
	c.OnWindowShortcut(ui.Cmd, ui.KeyS, rowAction(func(r Row) bool { return a.ToggleFlag(r, FlagFavorite) }))
	c.OnWindowShortcut(ui.Cmd, ui.KeyE, rowAction(func(r Row) bool { return a.ToggleFlag(r, FlagSensitive) }))
	c.OnWindowShortcut(ui.Cmd, ui.KeyY, rowAction(func(r Row) bool { return a.Copy(r) }))
	c.OnWindowShortcut(ui.Cmd, ui.KeyO, rowAction(func(r Row) bool {
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
		c.OnWindowShortcut(ui.Cmd, key, func() {
			if a.SelectSlot(slot) {
				if row, ok := a.RowAt(slot); ok {
					a.Paste(row)
				}
			}
		})
	}
}

// listView 是左栏列表。
func (a *App) listView(c *ui.Context, after *[]func()) {
	th := c.Theme()
	rows := a.Rows()
	sel, _ := a.Selection()

	ui.Column(c).Fill().Children(func() {
		ui.Scroll(c).Fill().Children(func() {
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

					ui.Column(c).Key(fmt.Sprintf("row-%d", row.ID)).
						Radius(6).
						Padding(6, 4).
						Background(bg).
						OnClick(func() {
							*after = append(*after, func() { a.Select(index) })
						}).
						Children(func() {
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
								if row.Sensitive {
									mark += "🔒 "
								}
								ui.Text(c, mark+row.Summary).FontSize(12).TextColor(fg).Grow(1).Ellipsis("…")
								ui.Text(c, row.Time).FontSize(9).TextColor(fg)
							})
						})
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

// previewView 是右栏大图预览（不是图片条目时给元信息）。
func (a *App) previewView(c *ui.Context) {
	th := c.Theme()
	row, ok := a.Selected()
	ui.Column(c).Width(previewWidth).FillHeight().Gap(6).Children(func() {
		if !ok {
			return
		}
		if row.OriginPath != "" {
			if b := bitmapFor(row.OriginPath); b != nil {
				w, h := fitSize(row.Width, row.Height, previewWidth, 320)
				ui.Image(c, b).Size(w, h).Fit(ui.Contain).Radius(8)
			} else {
				ui.Text(c, "⚠️ 原图读不出来（文件可能已被清理）").FontSize(11).TextColor(th.Warning)
			}
		} else if row.Kind == model.KindImage {
			ui.Text(c, "⚠️ 原图文件缺失").FontSize(11).TextColor(th.Warning)
		}
		ui.Text(c, fmt.Sprintf("%s · %s", row.Tag, row.Time)).FontSize(11).TextColor(th.TextMuted)
		if row.Width > 0 && row.Height > 0 {
			ui.Text(c, fmt.Sprintf("%d×%d", row.Width, row.Height)).FontSize(11).TextColor(th.TextMuted)
		}
		if row.Note != "" {
			ui.Text(c, row.Note).FontSize(11).TextColor(th.Text)
		}
	})
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
	if w <= 0 || h <= 0 {
		return maxW, maxH
	}
	scale := maxW / float32(w)
	if s := maxH / float32(h); s < scale {
		scale = s
	}
	if scale > 1 {
		scale = 1 // 小图不放大，避免糊
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
