package app

import (
	"testing"

	"github.com/egoist/mygo/ui"
)

// TestRowContextMenu 右键行弹出菜单，菜单项能真的改到数据。
func TestRowContextMenu(t *testing.T) {
	a := newTestApp(t)
	record(t, a, "右键菜单测试条目")

	tester := ui.NewTester(a.PanelView, 720, 560)
	tester.Frame()

	rect, ok := tester.Find("右键菜单测试条目")
	if !ok {
		t.Fatal("列表里找不到那一行")
	}
	tester.RightClickAt(rect.X+rect.W/2, rect.Y+rect.H/2)
	labels := tester.Menu()
	if len(labels) == 0 {
		t.Fatal("右键没有弹出菜单")
	}
	for _, want := range []string{"粘贴", "复制", "置顶", "收藏", "移到分组", "删除"} {
		found := false
		for _, l := range labels {
			if l == want {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("菜单里缺少「%s」：%v", want, labels)
		}
	}

	// 收藏：点菜单里的「收藏」后条目应当变成收藏
	if err := tester.ChooseMenuItem("收藏"); err != nil {
		t.Fatalf("点菜单失败：%v", err)
	}
	tester.Frame()
	row, _ := a.RowAt(0)
	if !row.Favorite {
		t.Fatal("点「收藏」没有生效")
	}

	// 取消收藏：菜单项文案应当跟着变
	tester.RightClickAt(rect.X+rect.W/2, rect.Y+rect.H/2)
	labels = tester.Menu()
	hasUnfav := false
	for _, l := range labels {
		if l == "取消收藏" {
			hasUnfav = true
		}
	}
	if !hasUnfav {
		t.Fatalf("收藏后菜单项应当是「取消收藏」：%v", labels)
	}

	// 删除：条目被删掉
	if err := tester.ChooseMenuItem("删除"); err != nil {
		t.Fatalf("点删除失败：%v", err)
	}
	tester.Frame()
	if n := len(a.Rows()); n != 0 {
		t.Fatalf("删除后还剩 %d 条", n)
	}
}

// TestRowContextMenuMovesToGroup 右键菜单里的「移到分组」子菜单生效。
func TestRowContextMenuMovesToGroup(t *testing.T) {
	a := newTestApp(t)
	record(t, a, "归组测试条目")
	if _, err := a.CreateGroup("工作"); err != nil {
		t.Fatalf("建分组失败：%v", err)
	}

	tester := ui.NewTester(a.PanelView, 720, 560)
	tester.Frame()
	rect, _ := tester.Find("归组测试条目")
	tester.RightClickAt(rect.X+rect.W/2, rect.Y+rect.H/2)

	if err := tester.ChooseMenuItem("移到分组", "工作"); err != nil {
		t.Fatalf("点子菜单失败：%v", err)
	}
	tester.Frame()
	row, _ := a.RowAt(0)
	if row.GroupName != "工作" {
		t.Fatalf("没有归到「工作」：%+v", row)
	}
}
