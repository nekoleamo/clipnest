package app

import (
	"testing"

	"github.com/egoist/mygo/ui"
)

// TestMarkAndDeleteMarked 勾选多条后一次删掉，未勾选的不受影响。
func TestMarkAndDeleteMarked(t *testing.T) {
	a := newTestApp(t)
	ids := []int64{
		record(t, a, "批量 A"),
		record(t, a, "批量 B"),
		record(t, a, "批量 C"),
		record(t, a, "保留 D"),
	}

	a.ToggleMark(ids[0])
	a.ToggleMark(ids[2])
	if got := len(a.Marked()); got != 2 {
		t.Fatalf("应当标记 2 条，实际 %d", got)
	}
	if !a.IsMarked(ids[0]) || a.IsMarked(ids[1]) {
		t.Fatal("标记状态不对")
	}

	row, _ := a.Selected()
	n := a.DeleteMarked(row, true)
	if n != 2 {
		t.Fatalf("应当删掉 2 条，实际 %d", n)
	}
	if total := len(a.Rows()); total != 2 {
		t.Fatalf("删完还剩 %d 条，期望 2", total)
	}
	if len(a.Marked()) != 0 {
		t.Fatal("删完应当清空标记")
	}
	for _, r := range a.Rows() {
		if r.ID == ids[0] || r.ID == ids[2] {
			t.Fatalf("被标记的条目还在：%+v", r)
		}
	}
}

// TestMarkAllLoadedAndClear 全选已加载 → 再按一次取消；Esc 先取消勾选而不是收起面板。
func TestMarkAllLoadedAndClear(t *testing.T) {
	a := newTestApp(t)
	for i := 0; i < 5; i++ {
		record(t, a, "全选 "+itoaPad(i))
	}

	a.MarkAllLoaded()
	if got := len(a.Marked()); got != 5 {
		t.Fatalf("全选应当勾上 5 条，实际 %d", got)
	}
	a.MarkAllLoaded()
	if got := len(a.Marked()); got != 0 {
		t.Fatalf("再按一次应当取消全选，实际还剩 %d", got)
	}

	a.ToggleMark(a.Rows()[0].ID)
	if !a.ClearMarks() {
		t.Fatal("有勾选时 ClearMarks 应当返回 true（Esc 用它决定不收起面板）")
	}
	if a.ClearMarks() {
		t.Fatal("没有勾选时 ClearMarks 应当返回 false（Esc 才收起面板）")
	}
	if _, ok := a.Selected(); !ok {
		t.Fatal("取消勾选不该把列表清空")
	}
}

// TestDeleteMarkedFallsBackToSelected 没有勾选时 ⌘D 删当前选中那条。
func TestDeleteMarkedFallsBackToSelected(t *testing.T) {
	a := newTestApp(t)
	record(t, a, "第一条")
	record(t, a, "第二条")
	row, _ := a.Selected()

	if n := a.DeleteMarked(row, true); n != 1 {
		t.Fatalf("应当删掉当前选中 1 条，实际 %d", n)
	}
	if got := len(a.Rows()); got != 1 {
		t.Fatalf("还剩 %d 条，期望 1", got)
	}
	if a.Rows()[0].ID == row.ID {
		t.Fatal("删掉的不是当前选中那条")
	}
}

// TestMarkClickWithCmd ⌘点击行是勾选（不是选中）；普通点击仍是选中。
func TestMarkClickWithCmd(t *testing.T) {
	a := newTestApp(t)
	record(t, a, "点击 A")
	record(t, a, "点击 B")

	tester := ui.NewTester(a.PanelView, 720, 560)
	tester.Frame()
	rect, ok := tester.Find("点击 B")
	if !ok {
		t.Fatal("找不到第二行")
	}
	tester.ClickAtWith(ui.Cmd, rect.X+rect.W/2, rect.Y+rect.H/2)
	tester.Frame()
	if got := len(a.Marked()); got != 1 {
		t.Fatalf("⌘点击应当勾选 1 条，实际 %d", got)
	}
	if a.Rows()[0].ID != 0 && a.IsMarked(a.Rows()[1].ID) {
		t.Fatal("勾的应当是点的那一行")
	}
}
