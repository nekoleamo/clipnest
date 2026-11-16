// uishot 用 mygo 的 headless 渲染把面板画成 PNG，方便在不动真实鼠标的情况下看版式。
//
//	go run ./cmd/uishot -out /tmp/panel.png -w 720 -h 560
//
// 会在临时数据根里塞几条样例（长文本、高图、小图、文件列表），然后渲染选中第 index 条。
package main

import (
	"flag"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"log"
	"os"
	"path/filepath"

	"github.com/egoist/mygo/ui"

	"clipnest/internal/app"
	"clipnest/internal/db"
	"clipnest/internal/model"
	"clipnest/internal/paths"
)

func main() {
	out := flag.String("out", "/tmp/panel.png", "输出 PNG 路径")
	w := flag.Int("w", 720, "面板宽度")
	h := flag.Int("h", 560, "面板高度")
	index := flag.Int("i", 0, "选中第几条（0 起）")
	dark := flag.Bool("dark", true, "深色主题")
	flag.Parse()

	root, err := os.MkdirTemp("", "uishot-")
	if err != nil {
		log.Fatal(err)
	}
	defer os.RemoveAll(root)
	p := paths.FromRoot(root)
	a, err := app.New(p)
	if err != nil {
		log.Fatal(err)
	}
	defer a.Store.Close()

	seed(a, p)
	a.Refresh()
	a.Select(*index)

	tester := ui.NewTester(a.PanelView, *w, *h)
	tester.SetDark(*dark)
	tester.Frame()
	tester.Frame()
	tester.Frame()

	img := tester.Image()
	f, err := os.Create(*out)
	if err != nil {
		log.Fatal(err)
	}
	if err := png.Encode(f, img); err != nil {
		log.Fatal(err)
	}
	if err := f.Close(); err != nil {
		log.Fatal(err)
	}
	fmt.Printf("已渲染 %s（%dx%d，选中第 %d 条）\n", *out, *w, *h, *index)
}

// seed 造几条有代表性的条目：长文本、超高图、超宽图、小图、文件。
func seed(a *app.App, p paths.Paths) {
	upsert := func(item model.NewItem) {
		if _, err := db.Upsert(a.Store.DB, item); err != nil {
			log.Fatalf("入库失败：%v", err)
		}
	}

	upsert(model.NewText("这条文本很长，用来看看右栏正文换行与卡片高度怎么排：" +
		"curl -fsSL https://gitee.com/example/packnest/raw/main/scripts/install-gui.sh | sh " +
		"然后再补充一些中文，看看中英混排的换行是否自然，以及末尾省略号是否出现。"))

	addImage(a, p, 496, 860, "tall")
	addImage(a, p, 1600, 400, "wide")
	addImage(a, p, 128, 128, "small")
	upsert(model.NewItem{Kind: model.KindFiles, Content: "/tmp/a.txt\n/tmp/b.txt", FileTypes: "ff"})
	upsert(model.NewText("第二条文本：把 purego 那半天的活做"))
}

// addImage 造一张纯色图，写进 images/origin 并入库。
func addImage(a *app.App, p paths.Paths, w, h int, name string) {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			t := float64(x+y) / float64(w+h)
			img.SetRGBA(x, y, color.RGBA{
				R: uint8(120 + 100*t), G: uint8(80 + 80*t), B: uint8(200 - 60*t), A: 255,
			})
		}
	}
	hash := fmt.Sprintf("%064x", len(name)*7919+len(fmt.Sprint(w, h)))
	origin, _, err := p.EnsureImageDirs(hash)
	if err != nil {
		log.Fatalf("建图片目录失败：%v", err)
	}
	f, err := os.Create(origin)
	if err != nil {
		log.Fatal(err)
	}
	if err := png.Encode(f, img); err != nil {
		log.Fatal(err)
	}
	_ = f.Close()
	_ = filepath.Ext(origin)

	// 缩略图给一份同图（版式检查不关心清晰度）
	thumb := p.ImageThumbnail(hash)
	if err := os.MkdirAll(filepath.Dir(thumb), 0o755); err == nil {
		if data, err := os.ReadFile(origin); err == nil {
			_ = os.WriteFile(thumb, data, 0o644)
		}
	}

	if _, err := db.Upsert(a.Store.DB, model.NewItem{
		Kind: model.KindImage, Content: hash + ".png",
		Width: int64(w), Height: int64(h),
	}); err != nil {
		log.Fatalf("图片入库失败：%v", err)
	}
}
