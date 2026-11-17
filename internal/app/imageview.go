// 点右栏预览图可以单独放大看（独立窗口，Esc / 点击关闭）。
package app

import (
	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"

	"clipnest/internal/model"
)

var _ = mygo.App

// ShowImage 打开（或重用）大图窗口，按屏幕可用区等比缩放到最大 85%。
func (a *App) ShowImage(row Row) {
	if row.OriginPath == "" {
		a.SetStatus("⚠️ 原图文件缺失")
		return
	}
	b := bitmapFor(row.OriginPath)
	if b == nil {
		a.SetStatus("⚠️ 原图读不出来")
		return
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
	logf("ZOOM show id=%d size=%dx%d", row.ID, row.Width, row.Height)

	d := mygo.Screen.DisplayNearestPoint(mygo.Screen.CursorScreenPoint())
	maxW := float32(d.WorkArea.Width) * 0.85
	maxH := float32(d.WorkArea.Height) * 0.85
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

// ImageView 是大图窗口的视图：图片居中，点一下或 Esc 关闭。
func (a *App) ImageView(c *ui.Context) {
	th := c.Theme()
	a.mu.Lock()
	row := a.imageRow
	a.mu.Unlock()

	c.OnWindowShortcut(0, ui.KeyEscape, func() { a.HideImage() })

	root := ui.Column(c).Fill().Padding(10).Gap(6).Background(th.Background)
	root.OnClick(func() { a.later(a.HideImage) })
	root.Children(func() {
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
