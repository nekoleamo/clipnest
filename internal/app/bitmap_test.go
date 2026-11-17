package app

import (
	"fmt"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"testing"

	"github.com/egoist/mygo/ui"
)

// TestBitmapCacheEvictsOldest 缓存满了先丢最早进来的，而不是整表清空。
//
// 整表清空会让「滚大图列表」每 128 张就把屏幕上正在看的图全部重新解码一遍。
func TestBitmapCacheEvictsOldest(t *testing.T) {
	bitmapMu.Lock()
	bitmapMap = map[string]*ui.Bitmap{}
	bitmapOrder = nil
	bitmapMu.Unlock()
	t.Cleanup(func() {
		bitmapMu.Lock()
		bitmapMap = map[string]*ui.Bitmap{}
		bitmapOrder = nil
		bitmapMu.Unlock()
	})

	dir := t.TempDir()
	img := image.NewRGBA(image.Rect(0, 0, 4, 4))
	var files []string
	for i := 0; i < bitmapCacheLimit+3; i++ {
		path := filepath.Join(dir, fmt.Sprintf("img-%03d.png", i))
		f, err := os.Create(path)
		if err != nil {
			t.Fatal(err)
		}
		if err := png.Encode(f, img); err != nil {
			t.Fatal(err)
		}
		_ = f.Close()
		files = append(files, path)
	}

	for _, path := range files {
		if b := bitmapFor(path); b == nil {
			t.Fatalf("解不出位图：%s", path)
		}
	}
	if got := bitmapCacheLen(); got != bitmapCacheLimit {
		t.Fatalf("缓存应当稳定在 %d 条，实际 %d", bitmapCacheLimit, got)
	}

	// 最早的几张被淘汰，最近的还在
	bitmapMu.Lock()
	_, oldestOk := bitmapMap[files[0]]
	_, newestOk := bitmapMap[files[len(files)-1]]
	bitmapMu.Unlock()
	if oldestOk {
		t.Fatal("最早的图应当被淘汰")
	}
	if !newestOk {
		t.Fatal("最新的图不该被淘汰")
	}

	// 再取最早那张：应当重新解码并挤掉当前最老的，而不是清空
	if b := bitmapFor(files[0]); b == nil {
		t.Fatal("淘汰后应当还能重新读到")
	}
	if got := bitmapCacheLen(); got != bitmapCacheLimit {
		t.Fatalf("重新读入后仍应是 %d 条，实际 %d", bitmapCacheLimit, got)
	}
}
