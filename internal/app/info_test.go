package app

import (
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"

	"clipnest/internal/db"
	"clipnest/internal/model"
)

// TestSensitiveFilter 顶部筛选的「敏感」只看带 🔒 的条目。
func TestSensitiveFilter(t *testing.T) {
	a := newTestApp(t)
	id := record(t, a, "敏感候选")
	record(t, a, "普通条目")
	if err := db.UpdateMeta(a.Store.DB, id, db.MetaPatch{Sensitive: boolPtr(true)}); err != nil {
		t.Fatal(err)
	}
	a.Refresh()

	a.SetKindFilter(5)
	rows := a.Rows()
	if len(rows) != 1 || rows[0].ID != id {
		t.Fatalf("「敏感」筛选应当只剩带标记的那条，实际 %+v", rows)
	}
	if kind, _ := a.FilterState(); kind != 5 {
		t.Fatalf("筛选状态没记住：%d", kind)
	}

	a.SetKindFilter(0)
	if got := len(a.Rows()); got != 2 {
		t.Fatalf("回「全部」应当是 2 条，实际 %d", got)
	}
}

// TestRowShowsGroupAndNote 列表行要显示分组名与「有备注」标记（数据早就有，之前没显示）。
func TestRowShowsGroupAndNote(t *testing.T) {
	a := newTestApp(t)
	id := record(t, a, "带分组和备注的条目")
	if _, err := a.CreateGroup("工作"); err != nil {
		t.Fatal(err)
	}
	var gid int64
	for _, g := range a.Groups() {
		if g.Name == "工作" {
			gid = g.ID
		}
	}
	if gid == 0 {
		t.Fatal("没建出分组")
	}
	if !a.MoveToGroup(id, &gid) {
		t.Fatal("移入分组失败")
	}
	a.SaveNote(id, "这条以后怎么找")

	tester := ui.NewTester(a.PanelView, 720, 560)
	tester.Frame()
	tester.Frame()
	if _, ok := tester.Find("#工作"); !ok {
		t.Fatal("列表行里没有分组标签")
	}
	if _, ok := tester.Find("📝"); !ok {
		t.Fatal("列表行里没有备注标记")
	}
}

// TestLongTextHasFullViewButton 长文本在预览里给「查看全文」入口。
func TestLongTextHasFullViewButton(t *testing.T) {
	a := newTestApp(t)
	long := strings.Repeat("长", 300)
	record(t, a, long)

	tester := ui.NewTester(a.PanelView, 720, 560)
	tester.Frame()
	tester.Frame()

	label := "查看全文（300 字）"
	if _, ok := tester.Find(label); !ok {
		t.Fatal("长文本没有「查看全文」按钮")
	}
	// 短文本不该出现这个按钮
	a.ResetPanelState()
	record(t, a, "短")
	a.Refresh()
	tester.Frame()
	if _, ok := tester.Find("查看全文（1 字）"); ok {
		t.Fatal("短文本不该有「查看全文」按钮")
	}
}

// TestDetailHintAndSize 详情窗底部说明与尺寸文案。
func TestDetailHintAndSize(t *testing.T) {
	text := Row{Kind: model.KindText, Content: "一二三四五"}
	if got := fmtDetailHint(text); !strings.Contains(got, "5 字") || !strings.Contains(got, "文本") {
		t.Fatalf("文本详情说明不对：%q", got)
	}
	img := Row{Kind: model.KindImage, Width: 640, Height: 480}
	if got := fmtDetailHint(img); !strings.Contains(got, "640 × 480") {
		t.Fatalf("图片详情说明不对：%q", got)
	}
}

func boolPtr(v bool) *bool { return &v }
