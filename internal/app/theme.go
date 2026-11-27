// 设计令牌：CDS 便携版数值（规格书见 ~/Documents/Plan/23-mygo-UI两条路线/mygo-ui-purego规格书.md）。
//
// 搬的是「③语义层 + 组件几何」，不搬 React 组件：open-claude-ui 的代码在纯 Go 自绘里没有用武之地，
// 有用的部分是表面阶梯、字号阶梯、圆角矩阵、控件高度与发丝边透明度。
//
// 强调色（accent / danger / warning / success / selection / focus）一律不动，
// 沿用 c.Theme() 里框架按系统偏好算好的值 —— 用户选了系统强调色，工具就该跟着。
package app

import (
	"sync"

	"github.com/egoist/mygo/ui"
)

// 字号阶梯（规格书 §4：caption 11 / footnote 12 / body 13.2→13 / heading 14.1→14 / title 20.7→16）。
// 光学系数 0.9412 已并入取整，别在调用处再乘。
const (
	fsMicro    = 9  // 序号角标、×N、时间戳
	fsTiny     = 10 // 计数、快捷键提示
	fsCaption  = 11 // 标签、次要说明
	fsFootnote = 12 // 列表摘要、备注框
	fsBody     = 13 // 正文、输入框
	fsHeading  = 14 // 小节标题
	fsTitle    = 16 // 窗口标题
)

// 圆角矩阵（规格书 §7）：面板/弹出层 10、按钮 8、输入框 6。
// 框架的 Theme.Radius 是全局默认值，这里取按钮的 8，其余靠元素级 .Radius() 覆盖。
const (
	rField = 6
	rCtl   = 8
	rPanel = 10
)

// 控件高度阶梯（§7）：XS 24 / SM 28 / MD 32 / LG 40，本地取值贴合桌面剪贴板工具的密度。
const (
	hXS = 26
	hSM = 28
	hMD = 32
	hLG = 40
)

// 间距阶梯（§7）：6 / 8 / 12 / 16 / 24。
const (
	sp1 = 6
	sp2 = 8
	sp3 = 12
	sp4 = 16
	sp5 = 24
)

// palette 是语义层里框架 Theme 没有槽位的那几档：
// surface-0..3 四阶梯、发丝边三档、悬停/按下叠加（alpha 层 §5）。
type palette struct {
	canvas         ui.Color // surface-0：画布/列表底
	panel          ui.Color // surface-panel：卡片
	card           ui.Color // surface-2
	field          ui.Color // surface-3：输入框/文本域
	hover          ui.Color // neutral @5%
	pressed        ui.Color // neutral @10%
	hairline       ui.Color // 标准边框 @10%
	hairlineStrong ui.Color // 强边框 @20%
	text           ui.Color
	textMuted      ui.Color
	scrollbar      ui.Color
}

// cdsPalette 给出浅色（暖）/ 暗色（冷）两套；glass 为真时表面改半透明，
// 让窗口材质（Vibrancy）透出来 —— 面板是毛玻璃的，铺不透明色会把它盖死。
func cdsPalette(dark, glass, contrast bool) palette {
	alpha := func(c ui.Color, a float32) ui.Color { return c.Alpha(a) }
	var p palette
	if dark {
		p = palette{
			canvas:         ui.Hex("#0b0b0b"),
			panel:          ui.Hex("#1a1a19"),
			card:           ui.Hex("#20201f"),
			field:          ui.Hex("#20201f"),
			hover:          ui.RGBA(255, 255, 255, 0.06),
			pressed:        ui.RGBA(255, 255, 255, 0.12),
			hairline:       ui.RGBA(255, 255, 255, 0.10),
			hairlineStrong: ui.RGBA(255, 255, 255, 0.18),
			text:           ui.Hex("#ecebe8"),
			textMuted:      ui.Hex("#9a9892"),
			scrollbar:      ui.RGBA(255, 255, 255, 0.30),
		}
		if glass {
			// 画布留 75%：再透就压不住背后（浅色主题叠在深色窗口上时正文会看不清），
			// 再实就看不见材质了。注意这层只能有一层 —— 见 themeFor 里 Background 置透明。
			p.canvas = alpha(p.canvas, 0.75)
			p.panel = alpha(p.panel, 0.55)
			p.card = alpha(p.card, 0.50)
			p.field = alpha(p.field, 0.62)
		}
	} else {
		p = palette{
			canvas:         ui.Hex("#f9f9f7"),
			panel:          ui.Hex("#fcfcfb"),
			card:           ui.Hex("#ffffff"),
			field:          ui.Hex("#ffffff"),
			hover:          ui.RGBA(24, 24, 27, 0.05),
			pressed:        ui.RGBA(24, 24, 27, 0.10),
			hairline:       ui.RGBA(24, 24, 27, 0.10),
			hairlineStrong: ui.RGBA(24, 24, 27, 0.20),
			text:           ui.Hex("#1f1e1d"),
			textMuted:      ui.Hex("#6f6d68"),
			scrollbar:      ui.RGBA(0, 0, 0, 0.28),
		}
		if glass {
			// 画布留 75%：再透就压不住背后（浅色主题叠在深色窗口上时正文会看不清），
			// 再实就看不见材质了。注意这层只能有一层 —— 见 themeFor 里 Background 置透明。
			p.canvas = alpha(p.canvas, 0.75)
			p.panel = alpha(p.panel, 0.55)
			p.card = alpha(p.card, 0.50)
			p.field = alpha(p.field, 0.62)
		}
	}
	if contrast { // macOS「增强对比度」：边框与次级文字向正文色靠
		p.hairline = p.hairline.Mix(p.text, 0.45)
		p.hairlineStrong = p.hairlineStrong.Mix(p.text, 0.45)
		p.textMuted = p.textMuted.Mix(p.text, 0.4)
	}
	return p
}

// themeKey 把影响结果的输入收成一个可比较的键：系统亮暗、是否有毛玻璃、是否高对比、强调色、字号。
type themeKey struct {
	dark, glass, contrast bool
	accent                ui.Color
	fontSize              float32
}

var (
	themeMu      sync.Mutex
	themeCache   = map[themeKey]*ui.Theme{}
	themeLastKey themeKey
	themeLogged  bool
)

// themeFor 由当前窗口的外观派生 CDS 主题并装上（每个 View 开头调一次）。
//
// 只覆盖表面/文字/边框与几何，accent 系列、Danger/Warning/Success、字体与字号留 base 原样。
// 结果按 key 缓存：每帧新建 Theme 会让绘制层每帧比对新指针，缓存能省掉这份差价。
func themeFor(c *ui.Context) *ui.Theme {
	base := c.Theme()
	key := themeKey{
		dark:     base.Dark,
		glass:    c.Vibrancy() && !c.Preferences().ReduceTransparency,
		contrast: c.Preferences().HighContrast,
		accent:   base.Accent,
		fontSize: base.FontSize,
	}

	themeMu.Lock()
	defer themeMu.Unlock()
	if !themeLogged || key != themeLastKey {
		logf("THEME dark=%v glass=%v contrast=%v accent=#%02x%02x%02x", key.dark, key.glass, key.contrast, key.accent.R, key.accent.G, key.accent.B)
		themeLastKey, themeLogged = key, true
	}
	if t, ok := themeCache[key]; ok {
		c.SetTheme(t)
		return t
	}

	p := cdsPalette(key.dark, key.glass, key.contrast)
	t := *base
	t.Background = p.canvas
	if key.glass {
		// 窗口根节点会用 Theme.Background 先铺一遍，视图根又铺一遍 canvas，
		// 两层 0.75 叠起来就是 94% 不透明 —— 材质全被盖掉（这是「只有白底」的真正原因）。
		// 根交给视图根那一层，这里必须置空。
		t.Background = ui.Transparent
	}
	t.Surface = p.card
	t.SurfaceHover = p.hover
	t.SurfacePressed = p.pressed
	t.Border = p.hairline
	t.Text = p.text
	t.TextMuted = p.textMuted
	t.Radius = rCtl
	t.Spacing = 4
	t.Scrollbar = p.scrollbar
	t.ScrollbarWidth = 6
	t.ScrollbarStyle = ui.ScrollbarStyle{Radius: 3, MinLength: 24}
	themeCache[key] = &t
	c.SetTheme(&t)
	return &t
}

// colorsOf 取当前主题的调色板（框架 Theme 只有一档 Surface，画不出分层）。
func colorsOf(c *ui.Context) palette {
	base := c.Theme()
	return cdsPalette(base.Dark, c.Vibrancy() && !c.Preferences().ReduceTransparency, c.Preferences().HighContrast)
}

// section 画一张设置窗卡片：小节标题 + 内容，surface-panel 一档 + 发丝边。
// 设置窗内容很长，靠卡片分段比靠分隔线好认（滚动时也能看出「这截属于哪一节」）。
func section(c *ui.Context, title string, pal palette, body func()) {
	card := ui.Column(c).FillWidth().Gap(sp2).Padding(sp3).
		Radius(rPanel).Background(pal.panel).Border(1, pal.hairline)
	liftCard(card, c.Theme().Dark)
	card.Children(func() {
		if title != "" {
			ui.Text(c, title).FontSize(fsHeading).Bold().TextColor(c.Theme().Text)
		}
		body()
	})
}

// liftCard 给卡片加 CDS 的 shadow-sm：暗色下阴影要重得多，不然浮层在暗底上「糊」在一起。
func liftCard(e ui.Element, dark bool) ui.Element {
	if dark {
		return e.Shadow(0, 2, 6, 0, ui.RGBA(0, 0, 0, 0.5))
	}
	return e.Shadow(0, 2, 4, 0, ui.RGBA(0, 0, 0, 0.07))
}
