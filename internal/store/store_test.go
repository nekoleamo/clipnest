package store

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"clipnest/internal/db"
	"clipnest/internal/model"
	"clipnest/internal/paths"
)

// newStore 建一个用内存库 + 临时数据根的 Store。
func newStore(t *testing.T) *Store {
	t.Helper()
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	p := paths.FromRoot(t.TempDir())
	if err := p.Ensure(); err != nil {
		t.Fatal(err)
	}
	return OpenWithDB(p, database)
}

// pngOf 造一张纯色 PNG，够测落盘与缩略图。
func pngOf(t *testing.T, w, h int, c color.RGBA) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.SetRGBA(x, y, c)
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestTextIsStoredOnceAndDedupedAfter(t *testing.T) {
	s := newStore(t)
	cap := Captured{Text: "接线的第一段文本"}
	first, err := s.Record(cap)
	if err != nil {
		t.Fatal(err)
	}
	if len(first) != 1 || !first[0].Result.Inserted || first[0].Kind != model.KindText {
		t.Fatalf("首次入库：%+v", first)
	}
	if !strings.HasPrefix(first[0].Label(), "INSERTED") {
		t.Fatalf("label=%s", first[0].Label())
	}
	for i := 0; i < 2; i++ {
		again, err := s.Record(cap)
		if err != nil {
			t.Fatal(err)
		}
		if again[0].Result.Inserted || !strings.HasPrefix(again[0].Label(), "DEDUPED") {
			t.Fatalf("二次入库：%+v", again)
		}
	}
	if n, _ := s.Count(); n != 1 {
		t.Fatalf("行数 %d", n)
	}
	item, _, _ := db.Get(s.DB, first[0].Result.ID)
	if item.UseCount != 3 {
		t.Fatalf("use_count=%d", item.UseCount)
	}
}

func TestEmptyCaptureWritesNothing(t *testing.T) {
	s := newStore(t)
	out, err := s.Record(Captured{})
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 0 {
		t.Fatalf("空采集不该入库：%+v", out)
	}
	out, err = s.Record(Captured{Text: ""})
	if err != nil || len(out) != 0 {
		t.Fatalf("空文本不该入库：%+v %v", out, err)
	}
	if n, _ := s.Count(); n != 0 {
		t.Fatalf("行数 %d", n)
	}
}

func TestFilesTakePriorityAndRecordTypes(t *testing.T) {
	s := newStore(t)
	dir := t.TempDir()
	file := filepath.Join(dir, "a.txt")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	stored, err := s.Record(Captured{
		// 复制文件时系统还会给一段路径文本，不该额外入库
		Text:  file,
		Files: []string{dir, file},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(stored) != 1 {
		t.Fatalf("应只落一条：%+v", stored)
	}
	item, _, _ := db.Get(s.DB, stored[0].Result.ID)
	if item.Kind != model.KindFiles {
		t.Fatalf("kind=%s", item.Kind)
	}
	if item.FileTypes != "df" {
		t.Fatalf("目录 d、文件 f，顺序跟随路径，得 %q", item.FileTypes)
	}
	if strings.Count(item.Content, "\n") != 1 {
		t.Fatalf("content=%q", item.Content)
	}
	hit, err := db.Query(s.DB, "a.txt", db.Filter{}, db.DefaultPage)
	if err != nil || len(hit) != 1 {
		t.Fatalf("文件名要能被搜到：%d %v", len(hit), err)
	}
}

func TestPruneKeepsNewestAndProtectsPinnedAndFavorite(t *testing.T) {
	s := newStore(t)
	var ids []int64
	for i := 0; i < 10; i++ {
		res, err := db.Upsert(s.DB, model.NewText("条目"+itoa(int64(i))))
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, res.ID)
		time.Sleep(2 * time.Millisecond)
	}
	yes := true
	if err := db.UpdateMeta(s.DB, ids[0], db.MetaPatch{Pinned: &yes}); err != nil {
		t.Fatal(err)
	}
	if err := db.UpdateMeta(s.DB, ids[1], db.MetaPatch{Favorite: &yes}); err != nil {
		t.Fatal(err)
	}

	// 上限 4：两条受保护 + 最新的两条
	deleted, err := s.Prune(4)
	if err != nil {
		t.Fatal(err)
	}
	if deleted != 6 {
		t.Fatalf("删除 %d 条", deleted)
	}
	left, err := db.Query(s.DB, "", db.Filter{}, db.Page{Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	if len(left) != 4 {
		t.Fatalf("剩余 %d 条", len(left))
	}
	kept := map[int64]bool{}
	for _, item := range left {
		kept[item.ID] = true
	}
	for _, id := range []int64{ids[0], ids[1], ids[8], ids[9]} {
		if !kept[id] {
			t.Fatalf("该留的被删了：id=%d", id)
		}
	}

	// 0 = 不限；超过上限的受保护条目也不会删
	if n, err := s.Prune(0); err != nil || n != 0 {
		t.Fatalf("0 应不限：%d %v", n, err)
	}
	if n, err := s.Prune(1); err != nil || n != 2 {
		t.Fatalf("名额被受保护条目占满时只删其余的：%d %v", n, err)
	}
	if n, _ := s.Count(); n != 2 {
		t.Fatalf("行数 %d", n)
	}
}

func TestImageIsSavedWithThumbnailAndDeduped(t *testing.T) {
	s := newStore(t)
	data := pngOf(t, 640, 480, color.RGBA{255, 0, 0, 255})
	cap := Captured{
		Image: &ImageBytes{PNG: data, Width: 640, Height: 480},
		// 复制截图时系统也给文本 flavor，图片优先
		Text: "不该入库的文本",
	}

	stored, err := s.Record(cap)
	if err != nil {
		t.Fatal(err)
	}
	if len(stored) != 1 || stored[0].Kind != model.KindImage {
		t.Fatalf("图片优先：%+v", stored)
	}
	item, _, _ := db.Get(s.DB, stored[0].Result.ID)
	if item.Width != 640 || item.Height != 480 {
		t.Fatalf("尺寸 %dx%d", item.Width, item.Height)
	}
	hash := strings.TrimSuffix(item.Content, ".png")
	if len(hash) != 64 {
		t.Fatalf("文件名应是图片内容的 blake3：%s", hash)
	}
	if hash != model.HashBytes(data) {
		t.Fatal("文件名与内容哈希不一致")
	}

	origin := s.Paths.ImageOrigin(hash)
	thumb := s.Paths.ImageThumbnail(hash)
	originBytes, err := os.ReadFile(origin)
	if err != nil {
		t.Fatalf("原图没落盘：%v", err)
	}
	if !bytes.Equal(originBytes, data) {
		t.Fatal("原图字节被改过")
	}
	thumbBytes, err := os.ReadFile(thumb)
	if err != nil {
		t.Fatalf("缩略图没落盘：%v", err)
	}
	cfg, err := png.DecodeConfig(bytes.NewReader(thumbBytes))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Width > ThumbnailMax || cfg.Height > ThumbnailMax {
		t.Fatalf("缩略图超过上限：%dx%d", cfg.Width, cfg.Height)
	}
	if cfg.Width <= 1 || cfg.Height <= 1 {
		t.Fatalf("缩略图不能缩成一条线：%dx%d", cfg.Width, cfg.Height)
	}
	if bytes.Equal(originBytes, thumbBytes) {
		t.Fatal("640×480 > 320，缩略图应真缩过")
	}

	// 同一张图再复制：只涨 use_count，不新增行
	again, err := s.Record(cap)
	if err != nil {
		t.Fatal(err)
	}
	if again[0].Result.Inserted {
		t.Fatal("同一张图应去重")
	}
	if n, _ := s.Count(); n != 1 {
		t.Fatalf("行数 %d", n)
	}
}

func TestSmallImageThumbnailIsNotReencoded(t *testing.T) {
	s := newStore(t)
	data := pngOf(t, 64, 64, color.RGBA{0, 128, 255, 255})
	stored, err := s.Record(Captured{Image: &ImageBytes{PNG: data, Width: 64, Height: 64}})
	if err != nil {
		t.Fatal(err)
	}
	item, _, _ := db.Get(s.DB, stored[0].Result.ID)
	hash := strings.TrimSuffix(item.Content, ".png")
	origin, err := os.ReadFile(s.Paths.ImageOrigin(hash))
	if err != nil {
		t.Fatal(err)
	}
	thumb, err := os.ReadFile(s.Paths.ImageThumbnail(hash))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(thumb, origin) {
		t.Fatal("小图不该重编码（会更大）")
	}
}

func TestDeletingAnImageRemovesItsFiles(t *testing.T) {
	s := newStore(t)
	data := pngOf(t, 64, 64, color.RGBA{10, 20, 30, 255})
	stored, err := s.Record(Captured{Image: &ImageBytes{PNG: data, Width: 64, Height: 64}})
	if err != nil {
		t.Fatal(err)
	}
	item, _, _ := db.Get(s.DB, stored[0].Result.ID)
	hash := strings.TrimSuffix(item.Content, ".png")
	if _, err := os.Stat(s.Paths.ImageOrigin(hash)); err != nil {
		t.Fatal("原图应在")
	}

	ok, err := s.Remove(stored[0].Result.ID)
	if err != nil || !ok {
		t.Fatalf("删除失败：%v", err)
	}
	if _, err := os.Stat(s.Paths.ImageOrigin(hash)); !os.IsNotExist(err) {
		t.Fatal("原图要删掉")
	}
	if _, err := os.Stat(s.Paths.ImageThumbnail(hash)); !os.IsNotExist(err) {
		t.Fatal("缩略图也要删")
	}
	if _, ok, _ := db.Get(s.DB, stored[0].Result.ID); ok {
		t.Fatal("行还在")
	}
	// 删第二次返回 false，不报错
	if ok, err := s.Remove(stored[0].Result.ID); ok || err != nil {
		t.Fatalf("重复删：%v %v", ok, err)
	}
}

func TestGarbageCollectorRemovesOrphanImages(t *testing.T) {
	s := newStore(t)
	data := pngOf(t, 32, 32, color.RGBA{1, 2, 3, 255})
	stored, err := s.Record(Captured{Image: &ImageBytes{PNG: data, Width: 32, Height: 32}})
	if err != nil {
		t.Fatal(err)
	}
	orphan := strings.Repeat("ff", 32)
	origin, thumb, err := s.Paths.EnsureImageDirs(orphan)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{origin, thumb} {
		if err := os.WriteFile(p, []byte("stale"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	removed, err := s.CollectGarbage()
	if err != nil {
		t.Fatal(err)
	}
	if removed != 2 {
		t.Fatalf("两个孤儿文件都要清掉，实际 %d", removed)
	}
	if _, err := os.Stat(origin); !os.IsNotExist(err) {
		t.Fatal("孤儿没删掉")
	}
	// 有记录的图要留着
	item, _, _ := db.Get(s.DB, stored[0].Result.ID)
	hash := strings.TrimSuffix(item.Content, ".png")
	if _, err := os.Stat(s.Paths.ImageOrigin(hash)); err != nil {
		t.Fatal("有记录的图被误删")
	}
}

func TestBrokenImageBytesDoNotPanic(t *testing.T) {
	s := newStore(t)
	stored, err := s.Record(Captured{
		Image: &ImageBytes{PNG: []byte("not a png"), Width: 10, Height: 10},
		Text:  "回退到文本",
	})
	if err != nil {
		t.Fatal(err)
	}
	// 原图能落盘（字节不校验），缩略图失败只记日志；条目仍然入库
	if len(stored) != 1 || stored[0].Kind != model.KindImage {
		t.Fatalf("%+v", stored)
	}
}

func TestOpenCreatesDirsAndReopenKeepsRows(t *testing.T) {
	root := filepath.Join(t.TempDir(), "root")
	p := paths.FromRoot(root)
	cap := Captured{Text: "重启后还要在"}
	{
		s, err := Open(p)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.Record(cap); err != nil {
			t.Fatal(err)
		}
		s.Close()
	}
	if _, err := os.Stat(p.DB()); err != nil {
		t.Fatalf("库文件没建：%v", err)
	}
	if fi, err := os.Stat(filepath.Join(root, "images", "origin")); err != nil || !fi.IsDir() {
		t.Fatal("images/origin 没建")
	}
	s, err := Open(p)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if n, _ := s.Count(); n != 1 {
		t.Fatalf("落盘后重开还在：%d", n)
	}
}

func itoa(v int64) string {
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

// TestPruneOlderThanAndClearAll 按时间清理（收藏/置顶受保护）与清空全部。
func TestPruneOlderThanAndClearAll(t *testing.T) {
	s := newStore(t)
	var ids []int64
	for i := 0; i < 4; i++ {
		res, err := db.Upsert(s.DB, model.NewText("老条目"+itoa(int64(i))))
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, res.ID)
	}
	yes := true
	if err := db.UpdateMeta(s.DB, ids[0], db.MetaPatch{Pinned: &yes}); err != nil {
		t.Fatal(err)
	}

	// 把 cutoff 放到未来：除了受保护的那条，其余都该被删
	future := model.Now()
	time.Sleep(5 * time.Millisecond)
	future = model.Now()
	deleted, err := s.PruneOlderThan(future)
	if err != nil {
		t.Fatalf("按时间清理失败：%v", err)
	}
	if deleted != 3 {
		t.Fatalf("应当删 3 条，实际 %d", deleted)
	}
	left, _ := db.Query(s.DB, "", db.Filter{}, db.Page{Limit: 10})
	if len(left) != 1 || left[0].ID != ids[0] {
		t.Fatalf("只该剩置顶那条：%+v", left)
	}

	// 清空全部：连置顶的也删
	cleared, err := s.ClearAll()
	if err != nil {
		t.Fatalf("清空失败：%v", err)
	}
	if cleared != 1 {
		t.Fatalf("应当清 1 条，实际 %d", cleared)
	}
	if n, _ := s.Count(); n != 0 {
		t.Fatalf("清空后还剩 %d 条", n)
	}
}
