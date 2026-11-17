package app

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"clipnest/internal/settings"
	"clipnest/internal/update"
)

// fakeRelease 起一个假「更新源」，返回固定 tag。
func fakeRelease(t *testing.T, tag string) *int {
	t.Helper()
	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"tag_name":"` + tag + `","html_url":"https://example.com/rel","name":"x","body":"y"}`))
	}))
	t.Cleanup(srv.Close)
	old := updateClient
	updateClient = func(string) update.Client {
		return update.Client{APIBase: srv.URL, HTTP: srv.Client(), UserAgent: "clipnest/test"}
	}
	t.Cleanup(func() { updateClient = old })
	return &hits
}

// waitStatus 等后台检查把文案写出来（goroutine，最多等 2 秒）。
func waitStatus(t *testing.T, a *App) string {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if msg, _ := a.UpdateStatus(); msg != "" && msg != "正在检查…" {
			return msg
		}
		time.Sleep(5 * time.Millisecond)
	}
	msg, _ := a.UpdateStatus()
	return msg
}

// TestCheckUpdateReportsNewer 有新版本时给提示，并记住发布页链接。
func TestCheckUpdateReportsNewer(t *testing.T) {
	hits := fakeRelease(t, "v99.0.0")
	a := newTestApp(t)
	a.Settings.ReleaseRepo = "owner/repo"

	a.CheckUpdate()
	msg := waitStatus(t, a)
	if *hits == 0 {
		t.Fatal("没有发出请求")
	}
	if !strings.Contains(msg, "有新版本") && !strings.Contains(msg, "已是最新") {
		t.Fatalf("提示看不懂：%q（当前版本 %s）", msg, a.AppVersion())
	}
	if _, hasURL := a.UpdateStatus(); !hasURL {
		t.Fatal("应当记住发布页链接")
	}
	if a.UpdateChecking() {
		t.Fatal("检查结束后不该还标着「检查中」")
	}
}

// TestCheckUpdateWithoutRepo 没填更新源时给引导，不发请求。
func TestCheckUpdateWithoutRepo(t *testing.T) {
	hits := fakeRelease(t, "v1.0.1")
	a := newTestApp(t)
	a.Settings.ReleaseRepo = ""

	a.CheckUpdate()
	msg, _ := a.UpdateStatus()
	if !strings.Contains(msg, "更新源") {
		t.Fatalf("应当提示先填更新源：%q", msg)
	}
	if *hits != 0 {
		t.Fatal("没填更新源不该发请求")
	}
}

// TestCheckUpdateError 请求失败要有可读提示，且不卡在「检查中」。
func TestCheckUpdateError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer srv.Close()
	old := updateClient
	updateClient = func(string) update.Client {
		return update.Client{APIBase: srv.URL, HTTP: srv.Client()}
	}
	defer func() { updateClient = old }()

	a := newTestApp(t)
	a.Settings.ReleaseRepo = "owner/repo"
	a.CheckUpdate()
	msg := waitStatus(t, a)
	if !strings.HasPrefix(msg, "❌") {
		t.Fatalf("失败要有 ❌ 提示，实际 %q", msg)
	}
	if a.UpdateChecking() {
		t.Fatal("失败后不该还标着「检查中」")
	}
}

// TestSaveUpdateRepo 保存更新源会写进 settings.json 并清掉上次结果。
func TestSaveUpdateRepo(t *testing.T) {
	a := newTestApp(t)
	a.SetUpdateStatus("旧结果")
	a.SaveUpdateRepo("neko/clipnest")

	loaded := settings.Load(a.Paths)
	if loaded.ReleaseRepo != "neko/clipnest" {
		t.Fatalf("设置没落盘：%q", loaded.ReleaseRepo)
	}
	if msg, _ := a.UpdateStatus(); msg != "" {
		t.Fatalf("保存后应当清掉旧结果，实际 %q", msg)
	}
	if got := *a.UpdateRepoDraft(); got != "neko/clipnest" {
		t.Fatalf("草稿没同步：%q", got)
	}
}
