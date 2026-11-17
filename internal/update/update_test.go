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
