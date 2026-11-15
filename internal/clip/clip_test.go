package clip

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"strings"
	"testing"

	"clipnest/internal/model"
	"clipnest/internal/store"
)

// fakeSource 是可控的剪贴板假实现。
type fakeSource struct {
	formats map[string][]byte
	files   []string
	hasFile bool
}

func (f fakeSource) Has(format string) bool {
	if format == FormatFile {
		return f.hasFile
	}
	_, ok := f.formats[format]
	return ok
}

func (f fakeSource) Bytes(format string) ([]byte, bool) {
	b, ok := f.formats[format]
	return b, ok
}

func (f fakeSource) Files() ([]string, bool) {
	if !f.hasFile || len(f.files) == 0 {
		return nil, false
	}
	return f.files, true
}

func pngBytes(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.SetRGBA(x, y, color.RGBA{200, 10, 10, 255})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestCaptureTextOnly(t *testing.T) {
	cap, snap, ok := Capture(fakeSource{formats: map[string][]byte{FormatText: []byte("hello 银河")}})
	if !ok {
		t.Fatal("应采到内容")
	}
	if cap.Text != "hello 银河" || len(cap.Files) != 0 || cap.Image != nil {
		t.Fatalf("采集结果：%+v", cap)
	}
	if snap.Flavors != "text" || snap.Excerpt != "hello 银河" {
		t.Fatalf("快照：%+v", snap)
	}
}

func TestCaptureEmptyClipboard(t *testing.T) {
	if _, _, ok := Capture(fakeSource{}); ok {
		t.Fatal("空剪贴板不该采到内容")
	}
}

func TestCaptureImageIsPreferredInStoreAndKeepsSizes(t *testing.T) {
	pngData := pngBytes(t, 320, 200)
	cap, snap, ok := Capture(fakeSource{formats: map[string][]byte{
		FormatPNG:  pngData,
		FormatText: []byte("不该入库的文本"),
	}})
	if !ok {
		t.Fatal("应采到内容")
	}
	if cap.Image == nil || cap.Image.Width != 320 || cap.Image.Height != 200 {
		t.Fatalf("图片：%+v", cap.Image)
	}
	if !bytes.Equal(cap.Image.PNG, pngData) {
		t.Fatal("PNG 字节应原样保留")
	}
	if snap.Flavors != "image+text" {
		t.Fatalf("flavor 顺序：%s", snap.Flavors)
	}
	if snap.Excerpt != "<图片 320×200>" {
		t.Fatalf("摘要：%s", snap.Excerpt)
	}
}

func TestCaptureBrokenImageKeepsFlavorButNotImage(t *testing.T) {
	cap, snap, ok := Capture(fakeSource{formats: map[string][]byte{
		FormatPNG:  []byte("not a png"),
		FormatText: []byte("回退到文本"),
	}})
	if !ok {
		t.Fatal("应采到内容")
	}
	if cap.Image != nil {
		t.Fatal("解不出尺寸就不该落图片")
	}
	if snap.Excerpt != "回退到文本" {
		t.Fatalf("摘要应退到文本：%s", snap.Excerpt)
	}
}

func TestCaptureFilesTakePriorityOverPathText(t *testing.T) {
	cap, snap, _ := Capture(fakeSource{
		hasFile: true,
		files:   []string{"/tmp/a.png", "/tmp/b.txt"},
		formats: map[string][]byte{FormatText: []byte("/tmp/a.png")},
	})
	if len(cap.Files) != 2 || cap.Text != "/tmp/a.png" {
		t.Fatalf("采集：%+v", cap)
	}
	if snap.Excerpt != "/tmp/a.png|/tmp/b.txt" {
		t.Fatalf("摘要：%s", snap.Excerpt)
	}
	if snap.Flavors != "files+text" {
		t.Fatalf("flavor：%s", snap.Flavors)
	}
}

func TestCaptureExcerptFlattensAndTruncates(t *testing.T) {
	long := strings.Repeat("长", 80) + "\n第二行"
	_, snap, _ := Capture(fakeSource{formats: map[string][]byte{FormatText: []byte(long)}})
	if strings.Contains(snap.Excerpt, "\n") {
		t.Fatal("换行要压平")
	}
	if len([]rune(snap.Excerpt)) > 60 {
		t.Fatalf("摘要太长：%d", len([]rune(snap.Excerpt)))
	}
}

func TestHashesCoverEachKind(t *testing.T) {
	cap := store.Captured{Text: "t", Files: []string{"/a", "/b"}}
	got := Hashes(cap)
	wantText := model.ContentHash(model.KindText, "t")
	wantFiles := model.ContentHash(model.KindFiles, "/a\n/b")
	if len(got) != 2 || got[0] != wantFiles || got[1] != wantText {
		t.Fatalf("哈希：%v", got)
	}

	pngData := pngBytes(t, 10, 10)
	imgCap := store.Captured{Image: &store.ImageBytes{PNG: pngData, Width: 10, Height: 10}}
	imgHash := Hashes(imgCap)
	name := store.ImageFileName(model.HashBytes(pngData))
	if len(imgHash) != 1 || imgHash[0] != model.ContentHash(model.KindImage, name) {
		t.Fatalf("图片哈希：%v", imgHash)
	}
}

// 契约 10：自身写回不产生新记录。
func TestGuardSwallowsSelfWriteOnce(t *testing.T) {
	g := NewGuard()
	cap := store.Captured{Text: "写回的文本"}
	hashes := Hashes(cap)

	g.Expect(hashes...)
	if g.Pending() != 1 {
		t.Fatalf("登记数 %d", g.Pending())
	}
	if !g.Consume(hashes...) {
		t.Fatal("自己写回的变化应被吞掉")
	}
	if g.Pending() != 0 {
		t.Fatal("命中后应清理登记")
	}
	// 同内容再次从外部复制：不再被吞
	if g.Consume(hashes...) {
		t.Fatal("没有登记时不该跳过")
	}
}

func TestGuardIgnoresDifferentContentAndEmptyExpect(t *testing.T) {
	g := NewGuard()
	g.Expect()
	if g.Pending() != 0 {
		t.Fatal("空登记不该记东西")
	}
	g.Expect(model.ContentHash(model.KindText, "A"))
	if g.Consume(model.ContentHash(model.KindText, "B")) {
		t.Fatal("不同内容不该命中")
	}
	if g.Pending() != 1 {
		t.Fatal("未命中的登记应保留")
	}
}

func TestGuardMatchesAnyOfSeveralRegistered(t *testing.T) {
	g := NewGuard()
	a := model.ContentHash(model.KindFiles, "/a\n/b")
	b := model.ContentHash(model.KindText, "/a")
	g.Expect(a, b)
	if !g.Consume(b) {
		t.Fatal("应命中其中一个")
	}
	if g.Pending() != 1 {
		t.Fatalf("只清理命中的那个，剩 %d", g.Pending())
	}
}

// 图片条目写回时文件不在，要报清楚的错（对照旧实现 image_without_file_reports_missing）。
func TestStripRTFFallsBackToPlainText(t *testing.T) {
	if got := stripRTF(`{\rtf1\ansi hello\par world}`); got != "hello world" && got != "helloworld" {
		t.Fatalf("RTF 去壳：%q", got)
	}
}
