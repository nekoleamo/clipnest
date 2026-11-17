package app

import (
	"fmt"
	"testing"

	"clipnest/internal/db"
	"clipnest/internal/model"
	"clipnest/internal/paths"
	"clipnest/internal/store"
)

// benchApp 造一个已有 n 条历史的应用（不读剪贴板、不建窗口）。
func benchApp(b *testing.B, n int) *App {
	b.Helper()
	a, err := New(paths.FromRoot(b.TempDir()))
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { a.Store.Close() })
	for i := 0; i < n; i++ {
		if _, err := db.Upsert(a.Store.DB, model.NewText(fmt.Sprintf("历史条目 %05d", i))); err != nil {
			b.Fatal(err)
		}
	}
	a.Refresh()
	return a
}

// BenchmarkRefresh 是「采集/动作之后重建列表」的成本：一页 200 条 + 真实总数。
func BenchmarkRefresh(b *testing.B) {
	a := benchApp(b, 2000)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		a.Refresh()
	}
}

// BenchmarkRecord 是落库本身的成本（含去重检查与 FTS 触发器）。
func BenchmarkRecord(b *testing.B) {
	a := benchApp(b, 2000)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		cap := store.Captured{Text: fmt.Sprintf("基准内容 %d", i)}
		if _, err := a.Store.Record(cap); err != nil {
			b.Fatal(err)
		}
	}
}
