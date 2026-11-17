package app

import (
	"testing"

	"github.com/egoist/mygo/ui"

	"clipnest/internal/db"
	"clipnest/internal/model"
)

// seedMany 往库里塞 n 条文本（编号三位补零，方便断言）。
func seedMany(t *testing.T, a *App, n int) {
	t.Helper()
	for i := 0; i < n; i++ {
		if _, err := db.Upsert(a.Store.DB, model.NewText("分页条目 "+itoaPad(i))); err != nil {
			t.Fatalf("入库失败：%v", err)
		}
	}
	a.Refresh()
}

// TestPageFirstPageOnly 第一页只取 200 条，但总数是真实的。
func TestPageFirstPageOnly(t *testing.T) {
	a := newTestApp(t)
	seedMany(t, a, PageSize+30)

	if got := len(a.Rows()); got != PageSize {
		t.Fatalf("第一页应当是 %d 条，实际 %d", PageSize, got)
	}
	if !a.HasMore() {
		t.Fatal("还有 30 条没加载，HasMore 应当为 true")
	}
	if got := a.Total(); got != int64(PageSize+30) {
		t.Fatalf("总数应当是 %d，实际 %d", PageSize+30, got)
	}
}

// TestLoadMoreAll 连续 LoadMore 直到取完，且不会多加载。
func TestLoadMoreAll(t *testing.T) {
	a := newTestApp(t)
	seedMany(t, a, PageSize+30)

	if !a.LoadMore() {
		t.Fatal("应当能加载下一页")
	}
	if got := len(a.Rows()); got != PageSize+30 {
		t.Fatalf("全部应当是 %d 条，实际 %d", PageSize+30, got)
	}
	if a.HasMore() {
		t.Fatal("已经取完了，HasMore 应当为 false")
	}
	if a.LoadMore() {
		t.Fatal("取完之后 LoadMore 应当返回 false")
	}
}

// TestScrollToBottomLoadsNextPage 列表滚到底会自动取下一页（不用手动点）。
func TestScrollToBottomLoadsNextPage(t *testing.T) {
	a := newTestApp(t)
	seedMany(t, a, PageSize+20)

	tester := ui.NewTester(a.PanelView, 720, 560)
	tester.Frame()
	tester.Frame()
	if !a.HasMore() {
		t.Fatal("测试前提不成立：应当还有下一页")
	}

	// 直接滚到“底部”（MaxY 由布局算出）
	a.ListScroll.Y = a.ListScroll.MaxY
	tester.Frame() // 本帧排上 LoadMore
	tester.Frame() // 下一帧执行，随即重查
	tester.Frame()

	if got := len(a.Rows()); got != PageSize+20 {
		t.Fatalf("滚到底应当加载到 %d 条，实际 %d", PageSize+20, got)
	}
	if a.HasMore() {
		t.Fatal("取完了 HasMore 应当为 false")
	}
}

// TestQueryResetsPage 改搜索词/筛选要把分页收回第一页（否则新结果只有第一页却顶到很后面）。
func TestQueryResetsPage(t *testing.T) {
	a := newTestApp(t)
	seedMany(t, a, PageSize+30)
	a.LoadMore()
	if len(a.Rows()) != PageSize+30 {
		t.Fatalf("前置加载失败：%d", len(a.Rows()))
	}

	a.SetQuery("分页条目 001")
	if got := len(a.Rows()); got != 1 {
		t.Fatalf("搜索「分页条目 001」应当只剩 1 条，实际 %d 条", got)
	}
	if a.HasMore() {
		t.Fatal("只有 1 条结果，HasMore 应当为 false")
	}
	a.SetQuery("分页条目")
	if got := len(a.Rows()); got != PageSize {
		t.Fatalf("搜索后应当回到第一页 %d 条，实际 %d", PageSize, got)
	}
	if !a.HasMore() {
		t.Fatal("搜索结果还有 30 条，HasMore 应当为 true")
	}

	// 唤出面板：分页收回第一页（不是把上次加载的 230 条留着）
	a.LoadMore()
	a.ResetPanelState()
	a.Refresh()
	if got := len(a.Rows()); got != PageSize {
		t.Fatalf("唤出后应当回到第一页 %d 条，实际 %d", PageSize, got)
	}
}
