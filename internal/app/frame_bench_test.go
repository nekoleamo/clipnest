package app

import (
	"testing"

	"github.com/egoist/mygo/ui"
)

// BenchmarkFrame 是「面板画一帧」的成本（200 行、1200 字正文的条目）。
func BenchmarkFrame(b *testing.B) {
	a := benchApp(b, 300)
	a.SetQuery("")
	tester := ui.NewTester(a.PanelView, 720, 560)
	tester.Frame()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		tester.Frame()
	}
}

// BenchmarkFrameWithQuery 有搜索词时每行都要算高亮片段。
func BenchmarkFrameWithQuery(b *testing.B) {
	a := benchApp(b, 300)
	a.SetQuery("历史")
	tester := ui.NewTester(a.PanelView, 720, 560)
	tester.Frame()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		tester.Frame()
	}
}
