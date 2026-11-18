package update

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// newTestClient 指向 httptest 服务器；handler 决定返回什么。
func newTestClient(t *testing.T, handler http.HandlerFunc) Client {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return Client{APIBase: srv.URL, HTTP: srv.Client(), UserAgent: "clipnest/test"}
}

// TestCompareVersions 版本号比较：点分数字、v 前缀、位数不齐。
func TestCompareVersions(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"1.0.0", "1.0.0", 0},
		{"v1.0.0", "1.0.0", 0},
		{"1.0.1", "1.0.0", 1},
		{"1.0.0", "1.0.1", -1},
		{"1.2", "1.2.0", 0},
		{"1.10.0", "1.9.9", 1},
		{"2.0.0-beta.1", "1.9.0", 1},
		{"1.0.0+build7", "1.0.0", 0},
	}
	for _, tc := range cases {
		if got := Compare(tc.a, tc.b); got != tc.want {
			t.Fatalf("Compare(%q,%q)=%d，期望 %d", tc.a, tc.b, got, tc.want)
		}
	}
	if !Newer("1.0.0", "v1.2.0") {
		t.Fatal("1.0.0 → v1.2.0 应当算有新版")
	}
	if Newer("1.2.0", "1.2.0") {
		t.Fatal("同版本不该算有新版")
	}
}

// TestCompareFallsBackOnGarbage 解析不了的版本按字符串比，不 panic。
func TestCompareFallsBackOnGarbage(t *testing.T) {
	if got := Compare("nightly", "nightly"); got != 0 {
		t.Fatalf("相同字符串应当相等，得 %d", got)
	}
	_ = Compare("abc", "1.0")
	_ = Newer("", "x")
}

// TestLatestParsesRelease 正常响应：取到 tag/链接/说明，说明过长要截断。
func TestLatestParsesRelease(t *testing.T) {
	var gotPath, gotUA string
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotUA = r.Header.Get("User-Agent")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"tag_name": " v1.3.0 ",
			"name": "1.3.0",
			"html_url": "https://example.com/releases/v1.3.0",
			"body": "` + strings.Repeat("改", notesLimit+50) + `",
			"published_at": "2026-11-17T10:00:00Z"
		}`))
	})
	rel, err := c.Latest(context.Background(), "owner/repo")
	if err != nil {
		t.Fatalf("查更新失败：%v", err)
	}
	if gotPath != "/repos/owner/repo/releases/latest" {
		t.Fatalf("请求路径不对：%s", gotPath)
	}
	if gotUA != "clipnest/test" {
		t.Fatalf("User-Agent 没带上：%q", gotUA)
	}
	if rel.Tag != "v1.3.0" {
		t.Fatalf("tag 没去掉空白：%q", rel.Tag)
	}
	if rel.URL == "" {
		t.Fatal("没取到发布链接")
	}
	if !strings.HasSuffix(rel.Notes, "…") {
		t.Fatalf("超长说明应当截断并以省略号结尾，长度 %d", len([]rune(rel.Notes)))
	}
	if !Newer("1.0.0", rel.Tag) {
		t.Fatal("1.3.0 应当比 1.0.0 新")
	}
}

// TestLatestErrors 各种失败的提示要能看懂。
func TestLatestErrors(t *testing.T) {
	notFound := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})
	if _, err := notFound.Latest(context.Background(), "owner/repo"); err == nil || !strings.Contains(err.Error(), "还没有发布版本") {
		t.Fatalf("404 的提示不对：%v", err)
	}

	forbidden := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	})
	if _, err := forbidden.Latest(context.Background(), "owner/repo"); err == nil {
		t.Fatal("403 应当报错")
	}

	badJSON := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("not json"))
	})
	if _, err := badJSON.Latest(context.Background(), "owner/repo"); err == nil || !strings.Contains(err.Error(), "解析失败") {
		t.Fatalf("坏 JSON 的提示不对：%v", err)
	}

	if _, err := badJSON.Latest(context.Background(), "  "); err == nil {
		t.Fatal("没填更新源应当直接报错，不该发请求")
	}
}

// TestLatestTimeout 服务端不返回时要按客户端超时失败，不能一直挂着。
func TestLatestTimeout(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(200 * time.Millisecond)
	})
	c.HTTP.Timeout = 30 * time.Millisecond
	start := time.Now()
	if _, err := c.Latest(context.Background(), "owner/repo"); err == nil {
		t.Fatal("超时应当报错")
	}
	if time.Since(start) > 2*time.Second {
		t.Fatalf("超时没生效，等了 %s", time.Since(start))
	}
}

// TestClientForSources 三个源各自的 API 根与网页根（Gitee 的 API 在 /api/v5 下）。
func TestClientForSources(t *testing.T) {
	gh := ClientFor(SourceGitHub, "", "v1.0.0")
	if gh.APIBase != "https://api.github.com" || gh.WebBase != "https://github.com" {
		t.Fatalf("GitHub 源配错了：%+v", gh)
	}
	gt := ClientFor(SourceGitee, "", "v1.0.0")
	if gt.APIBase != "https://gitee.com/api/v5" || gt.WebBase != "https://gitee.com" {
		t.Fatalf("Gitee 源配错了：%+v", gt)
	}
	cu := ClientFor(SourceCustom, "https://gh.example.com/api.github.com/", "v1.0.0")
	if cu.APIBase != "https://gh.example.com/api.github.com" {
		t.Fatalf("自定义源没去掉末尾斜杠：%q", cu.APIBase)
	}
	if cu.WebBase != "https://gh.example.com" {
		t.Fatalf("自定义源的网页根推导错了：%q", cu.WebBase)
	}
	// 空的自定义源退回 GitHub，不至于拼出空 URL
	if empty := ClientFor(SourceCustom, "  ", "v1"); empty.APIBase != "https://api.github.com" {
		t.Fatalf("空自定义源没兜底：%q", empty.APIBase)
	}
	// 不认识的值按 GitHub 处理
	if bad := ClientFor("npm", "", "v1"); bad.APIBase != "https://api.github.com" {
		t.Fatalf("未知源没兜底：%q", bad.APIBase)
	}
}

// TestLatestGiteeShape Gitee 不返回 html_url、发布时间叫 created_at：链接要自己拼。
func TestLatestGiteeShape(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"tag_name":"v1.1.0","name":"1.1.0","body":"notes","created_at":"2026-11-18T10:00:00Z"}`))
	})
	c.WebBase = "https://gitee.com"
	rel, err := c.Latest(context.Background(), "me/clipnest")
	if err != nil {
		t.Fatalf("查更新失败：%v", err)
	}
	if rel.URL != "https://gitee.com/me/clipnest/releases/tag/v1.1.0" {
		t.Fatalf("Gitee 发布页链接拼错了：%q", rel.URL)
	}
	if rel.PublishedAt != "2026-11-18T10:00:00Z" {
		t.Fatalf("created_at 没当发布时间用：%q", rel.PublishedAt)
	}
	// 网页根为空时不瞎猜，宁可没有链接
	c2 := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"tag_name":"v1.1.0"}`))
	})
	rel2, _ := c2.Latest(context.Background(), "me/clipnest")
	if rel2.URL != "" {
		t.Fatalf("没有网页根不该编链接：%q", rel2.URL)
	}
}

// TestSourceIndexAndLabels 下拉文案与取值互相对应。
func TestSourceIndexAndLabels(t *testing.T) {
	for i, label := range SourceLabels {
		if got := IndexOf(SourceOf(label)); got != i {
			t.Fatalf("文案 %q 对不上源：index=%d", label, got)
		}
	}
	if IndexOf("nope") != 0 || SourceOf("Nope") != SourceGitHub {
		t.Fatal("不认识的源应当归到 GitHub")
	}
}
