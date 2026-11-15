package model

import (
	"strings"
	"testing"
	"time"
)

func TestHashIsKindScopedAndStable(t *testing.T) {
	a := ContentHash(KindText, "hello")
	if a != ContentHash(KindText, "hello") {
		t.Fatal("同内容应稳定")
	}
	// 契约 1：类型进哈希，同内容不同类型不是同一条
	if a == ContentHash(KindHtml, "hello") {
		t.Fatal("类型必须参与哈希")
	}
	if len(a) != 64 {
		t.Fatalf("blake3 十六进制应为 64 字符，得 %d", len(a))
	}
	if strings.ToLower(a) != a || strings.Trim(a, "0123456789abcdef") != "" {
		t.Fatalf("应是小写十六进制：%s", a)
	}
}

func TestKindRoundTrips(t *testing.T) {
	for _, k := range []ItemKind{KindText, KindHtml, KindRtf, KindImage, KindFiles} {
		got, ok := ParseKind(string(k))
		if !ok || got != k {
			t.Fatalf("%s 往返失败", k)
		}
	}
	if _, ok := ParseKind("pdf"); ok {
		t.Fatal("未知类型应返回 false")
	}
}

func TestFilesJoinPathsWithNewlines(t *testing.T) {
	it := NewFiles([]string{"/a/x.png", "/b/y.txt"}, "ff")
	if it.Content != "/a/x.png\n/b/y.txt" {
		t.Fatalf("content=%q", it.Content)
	}
	if it.FileTypes != "ff" || it.Kind != KindFiles {
		t.Fatalf("fileTypes=%q kind=%q", it.FileTypes, it.Kind)
	}
}

func TestExcerptFlattensAndTruncatesOnCharBoundary(t *testing.T) {
	item := Item{Content: "第一行\n第二行很长很长"}
	if got := item.Excerpt(40); got != "第一行 第二行很长很长" {
		t.Fatalf("excerpt=%q", got)
	}
	item.Content = "一二三四五"
	if got := item.Excerpt(3); got != "一二三…" {
		t.Fatalf("excerpt=%q", got)
	}
}

func TestRelativeTimeReadsLikeAHuman(t *testing.T) {
	at := func(secs int64) string {
		return time.Now().UTC().Add(-time.Duration(secs) * time.Second).Format("2006-01-02T15:04:05.000Z")
	}
	cases := map[string]string{
		at(5):         "刚刚",
		at(90):        "1 分钟前",
		at(3 * 3600):  "3 小时前",
		at(3 * 86400): "3 天前",
	}
	for in, want := range cases {
		if got := RelativeTime(in); got != want {
			t.Fatalf("RelativeTime(%s)=%q，期望 %q", in, got, want)
		}
	}
	if got := RelativeTime(at(40 * 86400)); len(got) != 10 {
		t.Fatalf("超过一周给日期：%q", got)
	}
	if got := RelativeTime("不是时间"); got != "不是时间" {
		t.Fatalf("原样返回，得 %q", got)
	}
}

func TestNowIsSortableISO8601(t *testing.T) {
	a, b := Now(), Now()
	if a > b {
		t.Fatalf("%s 应 <= %s", a, b)
	}
	if !strings.HasSuffix(a, "Z") || !strings.Contains(a, "T") {
		t.Fatalf("格式不对：%s", a)
	}
}
