package clip

import (
	"strings"
	"testing"

	"clipnest/internal/model"
	"clipnest/internal/paths"
)

// TestStripHTMLKeepsText 剥标签要留正文、块级标签变换行、行内标签不插空格。
func TestStripHTMLKeepsText(t *testing.T) {
	cases := []struct{ in, want string }{
		{`<p>第一段</p><p>第二段</p>`, "第一段\n第二段"},
		{`<b>粗</b>体`, "粗体"},
		{`a<br>b`, "a\nb"},
		{`<div>hi &amp; bye</div>`, "hi & bye"},
		{`<style>p{color:red}</style><p>正文</p>`, "正文"},
		{`<script>alert(1)</script>可见`, "可见"},
		{`<a href="https://x.y">链接</a>`, "链接"},
		{`&lt;tag&gt; 不是标签`, "<tag> 不是标签"},
		{"a\u00a0b", "a b"},
	}
	for _, tc := range cases {
		if got := stripHTML(tc.in); got != tc.want {
			t.Fatalf("stripHTML(%q) = %q，期望 %q", tc.in, got, tc.want)
		}
	}
}

// TestPlainTextByKind 各类型的纯文本形态。
func TestPlainTextByKind(t *testing.T) {
	htmlItem := model.Item{Kind: model.KindHtml, Content: "<p>加粗</p>"}
	got, err := PlainText(htmlItem)
	if err != nil || got != "加粗" {
		t.Fatalf("HTML 条目应当剥成「加粗」，得到 %q err=%v", got, err)
	}

	rtfItem := model.Item{Kind: model.KindRtf, Content: `{\rtf1 你好\par 世界}`}
	if got, err := PlainText(rtfItem); err != nil || !strings.Contains(got, "你好") {
		t.Fatalf("RTF 条目应当留下正文，得到 %q err=%v", got, err)
	}

	filesItem := model.Item{Kind: model.KindFiles, Content: "/a/b.txt\n/c/d.txt"}
	if got, err := PlainText(filesItem); err != nil || got != "/a/b.txt\n/c/d.txt" {
		t.Fatalf("文件条目应当给路径，得到 %q err=%v", got, err)
	}

	if _, err := PlainText(model.Item{Kind: model.KindImage, Content: "x.png"}); err == nil {
		t.Fatal("图片条目应当报错（没有纯文本形态）")
	}
}

// TestWriteBackPlainEmpty 空白内容不往剪贴板里塞东西。
func TestWriteBackPlainEmpty(t *testing.T) {
	item := model.Item{Kind: model.KindHtml, Content: "<p>   </p>"}
	if _, err := WriteBackPlain(item, paths.Paths{}); err == nil {
		t.Fatal("空内容应当报错，而不是把剪贴板清空")
	}
}
