package app

import (
	"image"
	"image/png"
	"os"
	"testing"

	"github.com/egoist/mygo/ui"

	"clipnest/internal/db"
	"clipnest/internal/model"
)

// TestClickPreviewImageOpensZoom 点右栏的图片区域会触发放大预览。
func TestClickPreviewImageOpensZoom(t *testing.T) {
	a := newTestApp(t)

	// 造一张真图片放进 images/origin
	hash := "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	origin, _, err := a.Paths.EnsureImageDirs(hash)
	if err != nil {
		t.Fatalf("建图片目录失败：%v", err)
	}
	img := image.NewRGBA(image.Rect(0, 0, 200, 300))
	f, err := os.Create(origin)
	if err != nil {
		t.Fatal(err)
	}
	if err := png.Encode(f, img); err != nil {
		t.Fatal(err)
	}
	_ = f.Close()
	if _, err := db.Upsert(a.Store.DB, model.NewItem{
		Kind: model.KindImage, Content: hash + ".png", Width: 200, Height: 300,
	}); err != nil {
		t.Fatal(err)
	}
	a.Refresh()

	if b := bitmapFor(origin); b == nil {
		t.Fatal("bitmapFor 解不出这张图")
	}

	var got Row
	a.zoomProbe = func(r Row) { got = r }

	tester := ui.NewTester(a.PanelView, 720, 560)
	tester.Frame()
	tester.Frame()

	row, _ := a.Selected()
	t.Logf("选中：kind=%s origin=%q size=%dx%d", row.Kind, row.OriginPath, row.Width, row.Height)
	rect := a.imageRect
	if rect.W <= 0 || rect.H <= 0 {
		t.Fatalf("图片区域没量到：%+v", rect)
	}
	tester.ClickAt(rect.X+rect.W/2, rect.Y+rect.H/2)
	tester.Frame()

	if got.ID == 0 {
		t.Fatal("点图片没有触发放大")
	}
	if got.Width != 200 || got.Height != 300 {
		t.Fatalf("放大的是错的条目：%+v", got)
	}
}
