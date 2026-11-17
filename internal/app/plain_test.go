package app

import (
	"testing"

	"github.com/egoist/mygo/ui"

	"clipnest/internal/clip"
	"clipnest/internal/db"
	"clipnest/internal/model"
)

// TestPastePlainShortcutReachesPanel ⌥⏎ 要能穿过搜索框触发（焦点平时在输入框里）。
func TestPastePlainShortcutReachesPanel(t *testing.T) {
	a := newTestApp(t)
	res, err := db.Upsert(a.Store.DB, model.NewItem{Kind: model.KindHtml, Content: "<p>加粗的正文</p>"})
	if err != nil {
		t.Fatal(err)
	}
	id := res.ID
	a.Refresh()

	var probed int64
	a.plainProbe = func(r Row) { probed = r.ID }

	tester := ui.NewTester(a.PanelView, 720, 560)
	tester.Frame()
	tester.Frame()
	tester.Key(ui.Alt, ui.KeyEnter)
	tester.Frame()

	if probed != id {
		t.Fatalf("⌥⏎ 没触发纯文本粘贴（probe id=%d，期望 %d）", probed, id)
	}
}

// TestPastePlainDowngradesHTML 选中 HTML 条目按纯文本粘贴，落到剪贴板的必须是剥好的文本。
func TestPastePlainDowngradesHTML(t *testing.T) {
	a := newTestApp(t)
	if _, err := db.Upsert(a.Store.DB, model.NewItem{Kind: model.KindHtml, Content: "<b>粗</b>体"}); err != nil {
		t.Fatal(err)
	}
	a.Refresh()
	row, ok := a.Selected()
	if !ok {
		t.Fatal("没有选中条目")
	}
	// 没有真实窗口时 Dismiss 会直接返回，这里只验写回的内容
	item, _, err := db.Get(a.Store.DB, row.ID)
	if err != nil {
		t.Fatal(err)
	}
	text, err := clip.PlainText(item)
	if err != nil || text != "粗体" {
		t.Fatalf("纯文本应当是「粗体」，得到 %q err=%v", text, err)
	}
}
