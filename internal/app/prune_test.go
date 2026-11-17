package app

import (
	"testing"

	"clipnest/internal/db"
	"clipnest/internal/model"
)

// TestPruneIsThrottled 裁剪不是每条都跑：攒够 pruneEvery 条才裁一次（省掉两条查询）。
func TestPruneIsThrottled(t *testing.T) {
	a := newTestApp(t)
	a.Settings.MaxItems = 3
	for i := 0; i < 20; i++ {
		if _, err := db.Upsert(a.Store.DB, model.NewText("裁剪测试 "+itoaPad(i))); err != nil {
			t.Fatal(err)
		}
	}

	// 不到次数：不该裁
	for i := 0; i < pruneEvery-1; i++ {
		a.pruneIfDue()
	}
	if n, err := a.Store.Count(); err != nil || n != 20 {
		t.Fatalf("还没到裁剪次数就裁了：count=%d err=%v", n, err)
	}

	// 第 pruneEvery 次：裁到上限
	a.pruneIfDue()
	n, err := a.Store.Count()
	if err != nil {
		t.Fatal(err)
	}
	if n != 3 {
		t.Fatalf("到次数应当裁到上限 3，实际 %d", n)
	}
}

// TestTotalIsExactWhenTruncated 一页装不下时总数要准（走 count），装得下时用 len。
func TestTotalIsExactWhenTruncated(t *testing.T) {
	a := newTestApp(t)

	small := 40
	for i := 0; i < small; i++ {
		if _, err := db.Upsert(a.Store.DB, model.NewText("总数小 "+itoaPad(i))); err != nil {
			t.Fatal(err)
		}
	}
	a.Refresh()
	if got := a.Total(); got != int64(small) {
		t.Fatalf("装得下时总数应当是 %d，实际 %d", small, got)
	}
	if a.HasMore() {
		t.Fatal("40 条不该有下一页")
	}

	for i := 0; i < PageSize; i++ {
		if _, err := db.Upsert(a.Store.DB, model.NewText("总数大 "+itoaPad(i))); err != nil {
			t.Fatal(err)
		}
	}
	a.Refresh()
	if !a.HasMore() {
		t.Fatal("超过一页时应当有下一页")
	}
	if got := a.Total(); got != int64(small+PageSize) {
		t.Fatalf("截断时总数应当是 %d，实际 %d", small+PageSize, got)
	}
}
