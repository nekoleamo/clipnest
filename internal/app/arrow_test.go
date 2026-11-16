package app

import (
	"testing"

	"github.com/egoist/mygo/ui"
)

// TestArrowKeysAfterMouseClick 鼠标点过列表之后，方向键仍然逐行移动选中
// （不能卡住，也不能一下跳好几行）。
func TestArrowKeysAfterMouseClick(t *testing.T) {
	a := newTestApp(t)
	for i := 0; i < 8; i++ {
		record(t, a, "行 "+string(rune('A'+i)))
	}
	tester := ui.NewTester(a.PanelView, 720, 560)
	tester.Frame()

	// 最新在最前："行 C" 是第 6 条（下标 5）
	if err := tester.Click("行 C"); err != nil {
		t.Fatalf("点不到行 C：%v", err)
	}
	tester.Frame()
	if sel, _ := a.Selection(); sel != 5 {
		t.Fatalf("点击后应当选中下标 5，实际 %d", sel)
	}

	for i := 0; i < 2; i++ {
		tester.KeyDown(ui.KeyDown)
		tester.Frame()
	}
	if sel, _ := a.Selection(); sel != 7 {
		t.Fatalf("点过列表后方向键应当逐行下移：期望 7，实际 %d", sel)
	}
	tester.KeyDown(ui.KeyUp)
	tester.Frame()
	if sel, _ := a.Selection(); sel != 6 {
		t.Fatalf("上键应当逐行上移：期望 6，实际 %d", sel)
	}
}

// TestMouseClickSelectsRow 点任意一行都能选中（含文本、图片缩略图区域）。
func TestMouseClickSelectsRow(t *testing.T) {
	a := newTestApp(t)
	for _, text := range []string{"第一行", "第二行", "第三行"} {
		record(t, a, text)
	}
	tester := ui.NewTester(a.PanelView, 720, 560)
	tester.Frame()

	for want, text := range map[int]string{0: "第三行", 1: "第二行", 2: "第一行"} {
		if err := tester.Click(text); err != nil {
			t.Fatalf("点不到「%s」：%v", text, err)
		}
		tester.Frame()
		if sel, _ := a.Selection(); sel != want {
			t.Fatalf("点「%s」后应当选中 %d，实际 %d", text, want, sel)
		}
	}
}
