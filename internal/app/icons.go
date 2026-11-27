package app

import (
	"sync"

	"github.com/egoist/mygo/ui"
)

// 右栏动作按钮用的小图标（内联 SVG，随文字色走）。
const (
	svgCopy   = `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><rect x="9" y="9" width="12" height="12" rx="2"/><path d="M5 15V5a2 2 0 0 1 2-2h10"/></svg>`
	svgExport = `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M12 16V4"/><path d="m7 9 5-5 5 5"/><path d="M4 16v2a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2v-2"/></svg>`
)

var (
	iconOnce   sync.Once
	IconCopy   *ui.SVG
	IconExport *ui.SVG
)

func loadIcons() {
	iconOnce.Do(func() {
		IconCopy, _ = ui.ParseSVG([]byte(svgCopy))
		IconExport, _ = ui.ParseSVG([]byte(svgExport))
	})
}

// 按钮尺寸统一：高 26、圆角 6、内边距 10、图标 13、文字 11、最小宽度 74。
// 跟设置窗里的 ui.Button 同一套观感（Surface 面 + 1px 边框 + 悬停变亮）。
const (
	actionBtnH   = 26
	actionBtnW   = 74
	actionBtnPad = 10
	actionIconSz = 13
)

// iconAction 是「图标 + 文字」的小按钮，两个按钮尺寸与配色完全一致。
func iconAction(c *ui.Context, th *ui.Theme, icon *ui.SVG, label string, onClick func()) {
	loadIcons()
	// 图标与文字整体居中：Justify 居中 + 固定最小宽度
	btn := ui.Row(c).Height(actionBtnH).MinWidth(actionBtnW).
		Padding(actionBtnPad, 0).Gap(6).Radius(6).
		Border(1, th.Border).AlignItems(ui.Center).Justify(ui.Center).OnClick(onClick)
	// 悬停/按下反馈：自己按上一帧的状态着色（mygo 的 styleFn 不对外，行为一致即可）
	switch {
	case btn.Pressed():
		btn.Background(th.SurfacePressed)
	case btn.Hovered():
		btn.Background(th.SurfaceHover)
	default:
		btn.Background(th.Surface)
	}
	btn.Children(func() {
		if icon != nil {
			ui.Icon(c, icon).Size(actionIconSz, actionIconSz).TextColor(th.Text)
		}
		ui.Text(c, label).FontSize(fsCaption).TextColor(th.Text)
	})
}
