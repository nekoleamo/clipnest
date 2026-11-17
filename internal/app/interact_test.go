package app

import (
	"os"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"clipnest/internal/db"
	"clipnest/internal/model"
)

// TestUndoDeleteRestoresRow 删掉之后撤销，条目与元数据都回来，墓碑也清掉。
func TestUndoDeleteRestoresRow(t *testing.T) {
	a := newTestApp(t)
	id := record(t, a, "要撤销的条目")
	yes := true
	note := "备注也要回来"
	if err := db.UpdateMeta(a.Store.DB, id, db.MetaPatch{Pinned: &yes, Note: &note}); err != nil {
		t.Fatal(err)
	}
	a.Refresh()

	row, _ := a.Selected()
	if n := a.DeleteMarked(row, true); n != 1 {
		t.Fatalf("应当删掉 1 条，实际 %d", n)
	}
	if got := len(a.Rows()); got != 0 {
		t.Fatalf("删完应当是 0 条，实际 %d", got)
	}
	if !a.CanUndo() {
		t.Fatal("刚删完应当可撤销")
	}

	if n := a.UndoDelete(); n != 1 {
		t.Fatalf("撤销应当恢复 1 条，实际 %d", n)
	}
	if got := len(a.Rows()); got != 1 {
		t.Fatalf("撤销后应当是 1 条，实际 %d", got)
	}
	item, ok, err := db.Get(a.Store.DB, id)
	if err != nil || !ok {
		t.Fatalf("恢复后取不到条目：%v", err)
	}
	if !item.Pinned || item.Note != note {
		t.Fatalf("元数据没恢复：pinned=%v note=%q", item.Pinned, item.Note)
	}
	if a.CanUndo() {
		t.Fatal("撤销一次之后不该还能再撤销")
	}
	// 墓碑要清掉，否则同步会把删除再推回来
	var n int
	if err := a.Store.DB.QueryRow("SELECT count(*) FROM tombstones WHERE content_hash = ?", item.ContentHash).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("墓碑应当被清掉，实际 %d 条", n)
	}
}

// TestUndoKeepsImageFilesUntilWindowEnds 撤销窗口内图片文件不能被删（否则恢复不出原图）。
func TestUndoKeepsImageFilesUntilWindowEnds(t *testing.T) {
	a := newTestApp(t)
	hash := "abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789"
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

	row, _ := a.Selected()
	a.DeleteMarked(row, true)
	if _, err := os.Stat(origin); err != nil {
		t.Fatalf("撤销窗口内原图不该被删：%v", err)
	}

	a.UndoDelete()
	if _, err := os.Stat(origin); err != nil {
		t.Fatalf("撤销后原图应当还在：%v", err)
	}
	if _, ok, err := db.Get(a.Store.DB, res.ID); err != nil || !ok {
		t.Fatalf("撤销后条目应当回来：%v", err)
	}
}

// TestUndoWindowExpires 窗口过了就不能撤销，图片文件此时才真删。
func TestUndoWindowExpires(t *testing.T) {
	a := newTestApp(t)
	id := record(t, a, "过期的撤销")
	a.Refresh()
	row, _ := a.Selected()
	a.DeleteMarked(row, true)

	// 把删除时间往前拨，模拟 10 秒已过
	a.mu.Lock()
	a.undo.at = time.Now().Add(-2 * UndoAfter)
	a.mu.Unlock()
	if a.CanUndo() {
		t.Fatal("窗口过了不该还能撤销")
	}
	if n := a.UndoDelete(); n != 0 {
		t.Fatalf("窗口过了撤销应当无效，实际恢复 %d", n)
	}
	if _, ok, _ := db.Get(a.Store.DB, id); ok {
		t.Fatal("条目不应当被恢复")
	}
}

// TestSearchClearButton 搜索框有内容时出现 ✕，点它清空搜索。
func TestSearchClearButton(t *testing.T) {
	a := newTestApp(t)
	record(t, a, "被搜索的条目")
	a.SetQuery("被搜索")

	tester := ui.NewTester(a.PanelView, 720, 560)
	tester.Frame()
	tester.Frame()
	if _, ok := tester.Find("✕"); !ok {
		t.Fatal("有搜索词时应当出现清除按钮")
	}
	if err := tester.Click("✕"); err != nil {
		t.Fatalf("点清除失败：%v", err)
	}
	tester.Frame()
	if q := a.Query(); q != "" {
		t.Fatalf("清除后搜索词应当是空，实际 %q", q)
	}
	if _, ok := tester.Find("✕"); ok {
		t.Fatal("清空之后清除按钮应当消失")
	}
}

// TestStepDetailMovesInList 详情窗 ←→ 在列表里换相邻条目，越界不动。
func TestStepDetailMovesInList(t *testing.T) {
	a := newTestApp(t)
	idA := record(t, a, "第一条")
	idB := record(t, a, "第二条")
	a.Refresh() // 新的在前：rows[0]=idB, rows[1]=idA

	a.ShowDetail(a.Rows()[0])
	a.mu.Lock()
	cur := a.imageRow.ID
	a.mu.Unlock()
	if cur != idB {
		t.Fatalf("详情窗当前条目不对：%d", cur)
	}

	a.StepDetail(1)
	a.mu.Lock()
	got := a.imageRow.ID
	a.mu.Unlock()
	if got != idA {
		t.Fatalf("→ 应当切到下一条 %d，实际 %d", idA, got)
	}

	a.StepDetail(1) // 越界
	a.mu.Lock()
	got = a.imageRow.ID
	a.mu.Unlock()
	if got != idA {
		t.Fatalf("越界不该动，实际 %d", got)
	}
}

// TestDoubleClickPastes 双击列表行 = 直接粘贴（点行本身与点摘要文字都要生效）。
func TestDoubleClickPastes(t *testing.T) {
	a := newTestApp(t)
	id := record(t, a, "双击粘贴的目标")

	var pasted int64
	a.probe.paste = func(r Row) { pasted = r.ID }

	tester := ui.NewTester(a.PanelView, 720, 560)
	tester.Frame()
	tester.Frame()

	rect, ok := tester.Find("双击粘贴的目标")
	if !ok {
		t.Fatal("找不到那一行")
	}
	// 同一位置连点两下（headless 按时间+位置算连击）
	tester.ClickAt(rect.X+rect.W/2, rect.Y+rect.H/2)
	tester.Frame()
	tester.ClickAt(rect.X+rect.W/2, rect.Y+rect.H/2)
	tester.Frame()

	if pasted != id {
		t.Fatalf("双击没有触发布局粘贴（probe id=%d，期望 %d）", pasted, id)
	}
}
