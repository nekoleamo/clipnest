package app

import (
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/egoist/mygo/transfer"
	"github.com/egoist/mygo/ui"

	"clipnest/internal/db"
	"clipnest/internal/model"
	"clipnest/internal/paths"
)

// newTestApp 建一个临时数据根的应用（不碰真实数据）。
func newTestApp(t *testing.T) *App {
	t.Helper()
	a, err := New(paths.FromRoot(t.TempDir()))
	if err != nil {
		t.Fatalf("建应用失败：%v", err)
	}
	t.Cleanup(func() { a.Store.Close() })
	return a
}

// record 往库里塞一条文本，返回 id。
func record(t *testing.T, a *App, text string) int64 {
	t.Helper()
	res, err := db.Upsert(a.Store.DB, model.NewText(text))
	if err != nil {
		t.Fatalf("入库失败：%v", err)
	}
	a.Refresh()
	return res.ID
}

// TestRowDragCarriesClipboardPayload 行拖拽会产出原生载荷（文本给纯文本）。
func TestRowDragCarriesClipboardPayload(t *testing.T) {
	a := newTestApp(t)
	record(t, a, "拖拽导出测试内容")

	var got transfer.Data
	a.dragProbe = func(d transfer.Data) { got = d }

	tester := ui.NewTester(a.PanelView, 720, 560)
	tester.Frame()

	rect, ok := tester.Find("拖拽导出测试内容")
	if !ok {
		t.Fatal("列表里找不到那一行")
	}
	x, y := rect.X+rect.W/2, rect.Y+rect.H/2
	tester.Press(x, y)
	tester.Move(x+40, y+30) // 超过起始阈值，触发拖拽
	tester.Frame()

	if len(got.Formats()) == 0 {
		t.Fatal("拖拽没有产出载荷")
	}
	raw, err := got.Read(transfer.Text)
	if err != nil {
		t.Fatalf("载荷里没有纯文本：%v", err)
	}
	if string(raw) != "拖拽导出测试内容" {
		t.Fatalf("载荷内容不对：%q", string(raw))
	}
	// 同时给一个真文件（拖进 Finder 就是 .txt）
	paths, err := got.Files()
	if err != nil || len(paths) != 1 {
		t.Fatalf("载荷里没有文件：%v %v", paths, err)
	}
	if strings.Contains(paths[0], "://") {
		t.Fatalf("文件路径应当是本地路径：%q", paths[0])
	}
	if _, err := os.Stat(paths[0]); err != nil {
		t.Fatalf("落盘的 txt 不存在：%v", err)
	}
}

// TestFilterBarAndPreviewLayout 窄面板时预览栏收起，宽的时候出现。
func TestFilterBarAndPreviewLayout(t *testing.T) {
	wide := layoutFor(720)
	if !wide.showPreview || wide.previewW != previewWidth || !wide.showCount {
		t.Fatalf("720 宽应当完整显示：%+v", wide)
	}
	mid := layoutFor(660)
	if !mid.showPreview || mid.previewW >= previewWidth || mid.showCount {
		t.Fatalf("660 宽应当收窄预览、去掉条数：%+v", mid)
	}
	narrow := layoutFor(480)
	if narrow.showPreview || narrow.showCount || !narrow.compactFilter {
		t.Fatalf("480 宽应当只剩列表：%+v", narrow)
	}
}

// TestSearchFiltersAndHighlights 查询词既过滤列表也产生高亮分段。
func TestSearchFiltersAndHighlights(t *testing.T) {
	a := newTestApp(t)
	record(t, a, "银河系漫游指南：测试文档一")
	record(t, a, "另一个银河系条目 带 emoji 🚀")
	record(t, a, "无关的第三条文本")

	a.SetQuery("银河")
	rows := a.Rows()
	if len(rows) != 2 {
		t.Fatalf("应当只剩 2 条命中，实际 %d", len(rows))
	}
	for _, row := range rows {
		spans := highlightSpans(row.Summary, "银河", ui.Color{}, ui.Color{})
		if spans == nil {
			t.Fatalf("行 %q 里没有高亮分段", row.Summary)
		}
	}
	a.SetQuery("文本")
	if got := len(a.Rows()); got != 1 {
		t.Fatalf("「文本」应当命中 1 条，实际 %d", got)
	}
	a.SetQuery("")
	if got := len(a.Rows()); got != 3 {
		t.Fatalf("清空查询应当回到 3 条，实际 %d", got)
	}
}

// TestKeyboardMoveScrollsSelectionIntoView 键盘移动选中时列表要跟着滚。
func TestKeyboardMoveScrollsSelectionIntoView(t *testing.T) {
	a := newTestApp(t)
	for i := 0; i < 60; i++ {
		res, err := db.Upsert(a.Store.DB, model.NewText("条目编号 "+itoaPad(i)))
		if err != nil {
			t.Fatalf("入库失败：%v", err)
		}
		_ = res
	}
	a.Refresh()

	tester := ui.NewTester(a.PanelView, 720, 560)
	tester.Frame()

	// 用键盘往下走 25 行：选中的行必须还在面板可见区域内
	for i := 0; i < 25; i++ {
		tester.KeyDown(ui.KeyDown)
		tester.Frame()
	}
	tester.Frame() // 滚动在布局后生效，给一帧
	sel, total := a.Selection()
	if total != 60 || sel != 25 {
		t.Fatalf("选中位置不对：sel=%d total=%d", sel, total)
	}
	if a.ListScroll.Y <= 0 {
		t.Fatalf("列表没滚动：Y=%.1f（视口高 %.0f）", a.ListScroll.Y, a.listViewH)
	}
	// 选中行的底部不能超出一屏（按行高与滚动量核对）
	rowH := float32(28)
	top := float32(sel)*rowH - a.ListScroll.Y
	if top < 0 || top+rowH > a.listViewH+1 {
		t.Fatalf("选中行没完全露出来：top=%.1f 视口=%.1f 滚动=%.1f", top, a.listViewH, a.ListScroll.Y)
	}
}

// itoaPad 是三位补零的编号，保证每行摘要不重复。
func itoaPad(n int) string {
	s := "000" + strconv.Itoa(n)
	return s[len(s)-3:]
}
