// 详情窗：图片可放大看，长文本可看全文（独立窗口，Esc / 点击关闭）。
//
// 两种内容共用同一个窗口（main 里建一次），因为「看大图」和「看全文」是同一个
// 交互意图：把右栏里被裁掉的东西完整看一遍。
package app

import (
	"fmt"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"

	"clipnest/internal/model"
)

var _ = mygo.App

// ShowDetail 打开（或重用）详情窗：图片按屏幕可用区缩放到最大 85%，长文本铺满窗口。
func (a *App) ShowDetail(row Row) {
	isImage := row.Kind == model.KindImage
	if isImage {
		if row.OriginPath == "" {
			a.SetStatus("⚠️ 原图文件缺失")
			return
		}
		if bitmapFor(row.OriginPath) == nil {
			a.SetStatus("⚠️ 原图读不出来")
			return
		}
	}

	a.mu.Lock()
	win := a.imageWin
	a.imageRow = row
	a.mu.Unlock()
	if a.probe.zoom != nil {
		a.probe.zoom(row)
	}
	if win == nil {
		// 没有窗口（测试环境或还没建好）：到此为止，别去碰 Screen（会等主线程）
		return
	}
	logf("DETAIL show id=%d kind=%s size=%dx%d", row.ID, row.Kind, row.Width, row.Height)

	d := mygo.Screen.DisplayNearestPoint(mygo.Screen.CursorScreenPoint())
	maxW := float32(d.WorkArea.Width) * 0.85
	maxH := float32(d.WorkArea.Height) * 0.85
	if !isImage {
		// 文本：给一个读得舒服的固定尺寸（比面板宽一点、高一点），别铺满整屏
		w := maxW * 0.62
		if w < 620 {
			w = 620
		}
		if w > maxW {
			w = maxW
		}
		h := maxH * 0.72
		if h < 420 {
			h = 420
		}
		if h > maxH {
			h = maxH
		}
		win.SetSize(int(w), int(h))
		win.SetPosition(d.WorkArea.X+(d.WorkArea.Width-int(w))/2, d.WorkArea.Y+(d.WorkArea.Height-int(h))/2)
		win.SetAlwaysOnTop(true)
		win.Show()
		win.Focus()
		a.invalidateImage()
		return
	}
	// 小图放大到看起来舒服的尺寸（最多 4 倍），大图缩到屏幕 85%
	w, h := fitSizeUp(row.Width, row.Height, maxW, maxH-40, 4)
	if w < 360 {
		scale := 360 / w
		w, h = w*scale, h*scale
	}
	if h < 240 {
		scale := 240 / h
		w, h = w*scale, h*scale
	}
	if w > maxW {
		w = maxW
	}
	if h > maxH-40 {
		h = maxH - 40
	}

	if win == nil {
		// 窗口在启动时由 main 建好（在帧回调里 NewWindow 不安全），这里只兜底提示
		a.SetStatus("⚠️ 预览窗口还没准备好")
		return
	}

	win.SetSize(int(w), int(h)+28)
	win.SetPosition(d.WorkArea.X+(d.WorkArea.Width-int(w))/2, d.WorkArea.Y+(d.WorkArea.Height-int(h)-28)/2)
	win.SetAlwaysOnTop(true)
	win.Show()
	win.Focus()
	a.invalidateImage()
}

// SetImageWindow 由 main 在建窗阶段注入大图窗口。
func (a *App) SetImageWindow(win *mygo.Window) {
	a.mu.Lock()
	a.imageWin = win
	a.mu.Unlock()
}

// StepDetail 在详情窗里切到相邻条目（+-1）；越界就停在原地。
func (a *App) StepDetail(delta int) {
	a.mu.Lock()
	cur := a.imageRow.ID
	rows := a.rows
	a.mu.Unlock()
	if len(rows) == 0 {
		return
	}
	next := -1
	for i := range rows {
		if rows[i].ID == cur {
			next = i + delta
			break
		}
	}
	if next < 0 || next >= len(rows) {
		return
	}
	a.ShowDetail(rows[next])
}

// HideImage 关掉大图窗口。
func (a *App) HideImage() {
	a.mu.Lock()
	win := a.imageWin
	a.mu.Unlock()
	if win != nil {
		win.Hide()
	}
}

func (a *App) invalidateImage() {
	a.mu.Lock()
	win := a.imageWin
	a.mu.Unlock()
	if win != nil {
		win.Invalidate()
	}
}

// DetailView 是详情窗的视图：图片居中、长文本可滚动，点一下或 Esc 关闭。
func (a *App) DetailView(c *ui.Context) {
	th := c.Theme()
	a.mu.Lock()
	row := a.imageRow
	a.mu.Unlock()

	c.OnWindowShortcut(0, ui.KeyEscape, func() { a.HideImage() })
	// ←→ 在同一条「历史」里换相邻条目（图片看下一张、长文本看下一条）
	c.OnWindowShortcut(0, ui.KeyLeft, func() { a.StepDetail(-1) })
	c.OnWindowShortcut(0, ui.KeyRight, func() { a.StepDetail(1) })

	root := ui.Column(c).Fill().Padding(10).Gap(6).Background(th.Background)
	root.OnClick(func() { a.later(a.HideImage) })
	root.Children(func() {
		if row.Kind != model.KindImage {
			// 文本：整段铺满，能滚；不截断也不再点一下关（点了会误关，键盘 Esc 足够）
			ui.Scroll(c).Grow(1).Children(func() {
				ui.Text(c, row.Content).FontSize(12).TextColor(th.Text).Wrap()
			})
			ui.Text(c, fmtDetailHint(row)).FontSize(11).TextColor(th.TextMuted)
			return
		}
		if row.OriginPath != "" {
			if b := bitmapFor(row.OriginPath); b != nil {
				boxW := float32(c.Root().Bounds().W) - 20
				boxH := float32(c.Root().Bounds().H) - 44
				// 大图窗里也允许放大（小图看着才有意义）
				w, h := fitSizeUp(row.Width, row.Height, boxW, boxH, 6)
				ui.Column(c).Fill().Justify(ui.Center).AlignItems(ui.Center).Children(func() {
					ui.Image(c, b).Size(w, h).Fit(ui.Contain).Radius(6)
				})
			}
		}
		hint := fmtSize(row)
		ui.Text(c, hint+" · 点任意处或 Esc 关闭").FontSize(11).TextColor(th.TextMuted)
	})
}

// fmtSize 是「宽 × 高」文案。
func fmtSize(row Row) string {
	if row.Kind != model.KindImage || row.Width <= 0 || row.Height <= 0 {
		return ""
	}
	return itoa64(row.Width) + " × " + itoa64(row.Height)
}

// fmtDetailHint 是详情窗底部那行说明。
func fmtDetailHint(row Row) string {
	if row.Kind == model.KindImage {
		return fmtSize(row) + " · ←→ 换图 · 点任意处或 Esc 关闭"
	}
	n := len([]rune(row.Content))
	return fmt.Sprintf("%s · %d 字 · ←→ 换条 · Esc 关闭", TagOf(row.Kind), n)
}

func itoa64(v int64) string {
	if v == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for v > 0 {
		i--
		buf[i] = byte('0' + v%10)
		v /= 10
	}
	return string(buf[i:])
}
