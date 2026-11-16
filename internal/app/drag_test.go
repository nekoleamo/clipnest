package app

import (
	"os"
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
