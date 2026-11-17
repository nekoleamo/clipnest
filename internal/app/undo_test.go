package app

import (
	"os"
	"testing"
	"time"

	"clipnest/internal/db"
	"clipnest/internal/model"
)

// makeImageItem 造一条带真文件（origin+thumb）的图片条目。
func makeImageItem(t *testing.T, a *App, hash string) int64 {
	t.Helper()
	origin, thumb, err := a.Paths.EnsureImageDirs(hash)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{origin, thumb} {
		if err := os.WriteFile(p, []byte("png"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	res, err := db.Upsert(a.Store.DB, model.NewItem{Kind: model.KindImage, Content: hash + ".png", Width: 8, Height: 8})
	if err != nil {
		t.Fatal(err)
	}
	a.Refresh()
	return res.ID
}

// TestSecondDeleteKeepsItsOwnImageFiles 连删两次时，先到的定时器只能清理「自己那一批」的
// 图片文件：否则第二批还在撤销窗口内的图片会被提前删掉，撤销回来是个坏条目。
func TestSecondDeleteKeepsItsOwnImageFiles(t *testing.T) {
	old := UndoAfter
	UndoAfter = 300 * time.Millisecond
	defer func() { UndoAfter = old }()

	a := newTestApp(t)
	h1 := "1111111111111111111111111111111111111111111111111111111111111111"
	h2 := "2222222222222222222222222222222222222222222222222222222222222222"
	makeImageItem(t, a, h1)
	makeImageItem(t, a, h2) // 新的在前：rows[0]=h2
	origin1, _, _ := a.Paths.EnsureImageDirs(h1)
	origin2, _, _ := a.Paths.EnsureImageDirs(h2)

	// 第一批：删 h2（t=0，窗口到 t=300ms）
	row, _ := a.RowAt(0)
	a.DeleteMarked(row, true)

	time.Sleep(150 * time.Millisecond)

	// 第二批：删 h1（t=150ms，窗口到 t=450ms）；上一批的文件立刻回收
	row2, _ := a.RowAt(0)
	a.DeleteMarked(row2, true)
	if _, err := os.Stat(origin2); err == nil {
		t.Fatal("上一批被替换后，它的图片文件应当立刻回收")
	}

	// t≈340ms：第一批的定时器已经触发，但它不能动第二批的文件
	time.Sleep(190 * time.Millisecond)
	if _, err := os.Stat(origin1); err != nil {
		t.Fatalf("第一批的定时器删掉了第二批的图片（撤销不回来）：%v", err)
	}
	if !a.CanUndo() {
		t.Fatal("第二批还在窗口内，应当仍可撤销")
	}

	// t≈560ms：第二批自己也过期了，这时才回收
	time.Sleep(250 * time.Millisecond)
	if _, err := os.Stat(origin1); err == nil {
		t.Fatal("窗口过了图片应当被回收")
	}
	if a.CanUndo() {
		t.Fatal("窗口过了不该还能撤销")
	}
}
