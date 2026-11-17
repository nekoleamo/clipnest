package app

import (
	"testing"

	"github.com/egoist/mygo/ui"
)

// TestResetPanelStateClearsPreviousSession 收起后再次唤出，界面要回到初始状态：
// 搜索词、选中行、筛选、滚动位置、备注草稿、底部状态、以及上一轮没跑完的动作都不该留着。
func TestResetPanelStateClearsPreviousSession(t *testing.T) {
	a := newTestApp(t)
	for i := 0; i < 10; i++ {
		record(t, a, "条目 "+itoaPad(i))
	}
	tester := ui.NewTester(a.PanelView, 720, 560)
	tester.Frame()

	// 模拟「上次退出时的样子」
	a.Select(5)
	a.SetKindFilter(2)          // 置顶
	a.SetGroupFilter(groupNone) // 未分组
	a.SetQuery("条目")            // 搜过词
	a.SetStatus("已复制到剪贴板")
	a.ListScroll.Y = 120
	if row, ok := a.Selected(); ok {
		a.NoteValue(row.ID, "上一轮写的备注草稿")
	}
	a.later(func() { a.Select(7) })
	if len(a.pending) == 0 {
		t.Fatal("没排上动作，测试本身有问题")
	}

	a.ResetPanelState()
	a.Refresh()

	if q := a.Query(); q != "" {
		t.Fatalf("搜索词没清：%q", q)
	}
	kind, group := a.FilterState()
	if kind != 0 || group != "" {
		t.Fatalf("筛选没清：kind=%d group=%q", kind, group)
	}
	if sel, total := a.Selection(); sel != 0 || total != 10 {
		t.Fatalf("选中/列表没回初始：sel=%d total=%d", sel, total)
	}
	if s := a.Status(); s != "" {
		t.Fatalf("底部状态没清：%q", s)
	}
	if a.ListScroll.Y != 0 {
		t.Fatalf("滚动位置没回顶部：Y=%.1f", a.ListScroll.Y)
	}
	if a.noteFor != 0 || a.noteDraft != "" {
		t.Fatalf("备注草稿没清：for=%d draft=%q", a.noteFor, a.noteDraft)
	}
	if len(a.pending) != 0 {
		t.Fatalf("上一轮排的动作没丢掉，会在下次渲染时冒出来：%d 个", len(a.pending))
	}

	// 下一帧：搜索框空着并拿到焦点（唤出就能直接打字）
	tester.Frame()
	if a.takeFocusSearchNext() {
		t.Fatal("焦点请求没被渲染取走")
	}
	if sel, _ := a.Selection(); sel != 0 {
		t.Fatalf("渲染后选中被改回：%d", sel)
	}
}

// TestPauseStopsCapture 暂停后不再采集，恢复后恢复；状态只存内存（重启即恢复）。
func TestPauseStopsCapture(t *testing.T) {
	a := newTestApp(t)
	if a.Paused() {
		t.Fatal("新起的应用不该是暂停状态")
	}

	a.TogglePause()
	if !a.Paused() {
		t.Fatal("TogglePause 之后应当是暂停")
	}
	before := len(a.Rows())
	a.Capture() // 暂停期间即使剪贴板有内容也不该入库
	if got := len(a.Rows()); got != before {
		t.Fatalf("暂停期间不该新增条目：%d → %d", before, got)
	}
	if s := a.Status(); s == "" {
		t.Fatal("暂停应当给一句底栏提示")
	}

	a.TogglePause()
	if a.Paused() {
		t.Fatal("再 TogglePause 应当恢复")
	}
}
