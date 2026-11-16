package app

import (
	"runtime"
	"testing"

	"github.com/egoist/mygo/ui"
)

// TestHighlightSpansCutsMatchedParts 命中的片段要单独成段（大小写不敏感、多处命中）。
func TestHighlightSpansCutsMatchedParts(t *testing.T) {
	base := ui.Color{}
	hi := ui.Color{}

	spans := highlightSpans("hello Hello world", "hello", base, hi)
	if len(spans) != 4 {
		t.Fatalf("应当切成 4 段，实际 %d：%+v", len(spans), spans)
	}
	if spans[0].Text != "hello" || spans[0].Weight == 0 {
		t.Fatalf("第一段应当是高亮的 hello：%+v", spans[0])
	}
	if spans[1].Text != " " {
		t.Fatalf("第二段应当是空格：%+v", spans[1])
	}
	if spans[2].Text != "Hello" {
		t.Fatalf("第三段应当是 Hello：%+v", spans[2])
	}

	// 没命中和空查询都返回 nil，调用方退回普通文本
	if highlightSpans("abc", "zzz", base, hi) != nil {
		t.Fatal("没命中应当返回 nil")
	}
	if highlightSpans("abc", "   ", base, hi) != nil {
		t.Fatal("空查询应当返回 nil")
	}
}

// TestHighlightSpansKeepsOriginalText 拼回去必须和原文一模一样（中文、emoji 都不碎）。
func TestHighlightSpansKeepsOriginalText(t *testing.T) {
	cases := []struct{ text, needle string }{
		{"银河系漫游指南 拷贝了一份", "漫游"},
		{"混合 mixed 文本 🚀 emoji", "文本"},
		{"分\n行\r\n文本说明", "文"},
		{"Hello World 你好", "o"},
	}
	for _, tc := range cases {
		spans := highlightSpans(tc.text, tc.needle, ui.Color{}, ui.Color{})
		if spans == nil {
			t.Fatalf("%q 应当命中 %q", tc.text, tc.needle)
		}
		got := ""
		for _, sp := range spans {
			got += sp.Text
		}
		if got != tc.text {
			t.Fatalf("拼回去不一致：%q != %q", got, tc.text)
		}
	}
}

// TestHotkeyTextFormats 快捷键展示文案。
func TestHotkeyTextFormats(t *testing.T) {
	cases := map[string]string{
		"CmdOrCtrl+Shift+V": "⌘⇧V",
		"Alt+Space":         "⌥SPACE",
		"ctrl+alt+k":        "⌃⌥K",
	}
	for in, want := range cases {
		if got := HotkeyText(in); got != want {
			// 非 macOS 的期望不同，这里只断言 macOS 的格式
			if runtimeIsDarwin() {
				t.Fatalf("HotkeyText(%q)=%q，想要 %q", in, got, want)
			}
		}
	}
}

func runtimeIsDarwin() bool { return runtime.GOOS == "darwin" }
