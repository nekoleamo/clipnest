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
	updateClient = func(string, string, string) update.Client {
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

// TestCheckUpdateWithoutRepo 仓库留空时用该源上的内置镜像仓库名；自定义源没有内置名，要提示。
func TestCheckUpdateWithoutRepo(t *testing.T) {
	var gotPath string
	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		gotPath = r.URL.Path
		_, _ = w.Write([]byte(`{"tag_name":"v1.0.1"}`))
	}))
	defer srv.Close()
	old := updateClient
	updateClient = func(string, string, string) update.Client {
		return update.Client{APIBase: srv.URL, HTTP: srv.Client()}
	}
	defer func() { updateClient = old }()

	a := newTestApp(t)
	a.Settings.ReleaseSource = update.SourceGitee
	a.Settings.ReleaseRepo = ""
	a.CheckUpdate()
	_ = waitStatus(t, a)
	if hits == 0 {
		t.Fatal("仓库留空应当回落到内置镜像仓库名，而不是什么都不查")
	}
	if want := "/repos/" + a.UpdateRepoFor(update.SourceGitee) + "/releases/latest"; gotPath != want {
		t.Fatalf("查询路径不对：%q，期望 %q", gotPath, want)
	}

	// 自定义源没有内置镜像名：给引导，不发请求
	hits = 0
	a.Settings.ReleaseSource = update.SourceCustom
	a.Settings.ReleaseRepo = ""
	a.SetUpdateStatus("")
	a.CheckUpdate()
	msg, _ := a.UpdateStatus()
	if !strings.Contains(msg, "仓库") {
		t.Fatalf("应当提示先填仓库：%q", msg)
	}
	if hits != 0 {
		t.Fatal("自定义源没填仓库不该发请求")
	}
}

// TestCheckUpdateError 请求失败要有可读提示，且不卡在「检查中」。
func TestCheckUpdateError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer srv.Close()
	old := updateClient
	updateClient = func(string, string, string) update.Client {
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

// TestSaveUpdateSource 切源会落盘（源 + 仓库 + API 根），草稿同步。
func TestSaveUpdateSource(t *testing.T) {
	a := newTestApp(t)
	a.SaveUpdateSource(update.SourceCustom, "me/repo", "https://gh.example.com/api.github.com")

	loaded := settings.Load(a.Paths)
	if loaded.ReleaseSource != update.SourceCustom || loaded.ReleaseRepo != "me/repo" ||
		loaded.ReleaseAPIBase != "https://gh.example.com/api.github.com" {
		t.Fatalf("设置没落盘：%+v", loaded)
	}
	if got := *a.UpdateSourceDraft(); got != update.SourceCustom {
		t.Fatalf("源草稿没同步：%q", got)
	}
	if got := *a.UpdateAPIBaseDraft(); got != "https://gh.example.com/api.github.com" {
		t.Fatalf("API 根草稿没同步：%q", got)
	}
	// 只存仓库名（老入口）不该把源清掉
	a.SaveUpdateRepo("other/repo")
	if got := settings.Load(a.Paths).ReleaseSource; got != update.SourceCustom {
		t.Fatalf("只存仓库名时源被改掉了：%q", got)
	}
}

// TestCheckUpdateCustomWithoutAPIBase 自定义源没填 API 根：给提示，不发请求。
func TestCheckUpdateCustomWithoutAPIBase(t *testing.T) {
	hits := fakeRelease(t, "v9.9.9")
	a := newTestApp(t)
	a.Settings.ReleaseSource = update.SourceCustom
	a.Settings.ReleaseRepo = "me/repo"
	a.Settings.ReleaseAPIBase = ""

	a.CheckUpdate()
	msg, _ := a.UpdateStatus()
	if !strings.Contains(msg, "API 根") {
		t.Fatalf("应当提示填 API 根：%q", msg)
	}
	if *hits != 0 {
		t.Fatal("没填 API 根不该发请求")
	}
}

// TestCheckUpdatePassesSource 选 Gitee 时把源和空 API 根交给客户端工厂（由它决定真实根）。
func TestCheckUpdatePassesSource(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"tag_name":"v1.0.0"}`))
	}))
	defer srv.Close()

	var gotSource, gotBase string
	old := updateClient
	updateClient = func(source, apiBase, _ string) update.Client {
		gotSource, gotBase = source, apiBase
		return update.Client{APIBase: srv.URL, HTTP: srv.Client(), WebBase: "https://gitee.com"}
	}
	defer func() { updateClient = old }()

	a := newTestApp(t)
	a.Settings.ReleaseSource = update.SourceGitee
	a.Settings.ReleaseRepo = "null_593_5354/clipnest"
	a.CheckUpdate()
	_ = waitStatus(t, a)

	if gotSource != update.SourceGitee || gotBase != "" {
		t.Fatalf("源没传到客户端：source=%q base=%q", gotSource, gotBase)
	}
}

// TestUpdateRepoFor 切源时能带出该源上的本仓镜像名。
func TestUpdateRepoFor(t *testing.T) {
	a := newTestApp(t)
	if got := a.UpdateRepoFor(update.SourceGitee); !strings.Contains(got, "/") {
		t.Fatalf("Gitee 没给默认仓库名：%q", got)
	}
	if got := a.UpdateRepoFor(update.SourceCustom); got != "" {
		t.Fatalf("自定义源不该猜仓库名：%q", got)
	}
}

// TestCheckUpdateAutoFallsBack auto 源：Gitee 查不通时自动换 GitHub，并在提示里写明实际源。
func TestCheckUpdateAutoFallsBack(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"tag_name":"v1.0.0"}`))
	}))
	defer srv.Close()

	var tried []string
	old := updateClient
	updateClient = func(source, _, _ string) update.Client {
		tried = append(tried, source)
		if source == update.SourceGitee {
			// 端口 1 上没人：连不上会立刻失败
			return update.Client{APIBase: "http://127.0.0.1:1", HTTP: srv.Client()}
		}
		return update.Client{APIBase: srv.URL, HTTP: srv.Client(), WebBase: "https://github.com"}
	}
	defer func() { updateClient = old }()

	a := newTestApp(t)
	a.Settings.ReleaseSource = update.SourceAuto
	a.Settings.ReleaseRepo = "me/repo"
	a.CheckUpdate()
	msg := waitStatus(t, a)

	if len(tried) != 2 || tried[0] != update.SourceGitee || tried[1] != update.SourceGitHub {
		t.Fatalf("候选顺序不对：%v", tried)
	}
	if !strings.Contains(msg, "已是最新") && !strings.Contains(msg, "有新版本") {
		t.Fatalf("换源后应当拿到结果：%q", msg)
	}
	if !strings.Contains(msg, "源 "+update.SourceGitHub) {
		t.Fatalf("提示里应写明实际用的源：%q", msg)
	}
}

// TestCheckUpdateAutoBothFail 两个源都不通：报最后一次的错误，不卡在「检查中」。
func TestCheckUpdateAutoBothFail(t *testing.T) {
	old := updateClient
	updateClient = func(string, string, string) update.Client {
		return update.Client{APIBase: "http://127.0.0.1:1"}
	}
	defer func() { updateClient = old }()

	a := newTestApp(t)
	a.Settings.ReleaseSource = update.SourceAuto
	a.Settings.ReleaseRepo = "me/repo"
	a.CheckUpdate()
	msg := waitStatus(t, a)
	if !strings.HasPrefix(msg, "❌") {
		t.Fatalf("都失败要给 ❌：%q", msg)
	}
	if a.UpdateChecking() {
		t.Fatal("失败后不该还标着「检查中」")
	}
}

// TestCheckUpdateAutoUsesPerSourceRepo auto + 仓库留空：换到 GitHub 时要用 GitHub 上的镜像名，
// 而不是把 Gitee 的仓库名照抄过去（那样必然 404）。
func TestCheckUpdateAutoUsesPerSourceRepo(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		_, _ = w.Write([]byte(`{"tag_name":"v1.0.0"}`))
	}))
	defer srv.Close()

	old := updateClient
	updateClient = func(source, _, _ string) update.Client {
		if source == update.SourceGitee {
			return update.Client{APIBase: "http://127.0.0.1:1", HTTP: srv.Client()}
		}
		return update.Client{APIBase: srv.URL, HTTP: srv.Client(), WebBase: "https://github.com"}
	}
	defer func() { updateClient = old }()

	a := newTestApp(t)
	a.Settings.ReleaseSource = update.SourceAuto
	a.Settings.ReleaseRepo = ""
	a.CheckUpdate()
	_ = waitStatus(t, a)

	want := "/repos/" + a.UpdateRepoFor(update.SourceGitHub) + "/releases/latest"
	if gotPath != want {
		t.Fatalf("换源后仓库名没跟着换：%q，期望 %q", gotPath, want)
	}
}
