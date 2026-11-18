package app

import (
	"testing"

	"github.com/egoist/mygo/ui"

	"clipnest/internal/settings"
	"clipnest/internal/update"
)

// TestPrefsUpdateSourceRows 更新源三件套在设置窗里渲染得出来：默认只有下拉+仓库，
// 选「自定义」时多一行 API 根；切换下拉会把源落盘并带出该源上的镜像仓库名。
func TestPrefsUpdateSourceRows(t *testing.T) {
	a := newTestApp(t)

	t1 := ui.NewTester(a.PrefsView, 620, 720)
	t1.Frame()
	t1.Frame()
	if _, ok := t1.Find("保存更新源"); !ok {
		t.Fatal("更新源那行没渲染出来")
	}
	if _, ok := t1.Find("检查更新"); !ok {
		t.Fatal("检查更新按钮没渲染出来")
	}
	if _, ok := t1.Find("API 根"); ok {
		t.Fatal("非自定义源不该显示 API 根那行")
	}

	a.Settings.ReleaseSource = update.SourceCustom
	a.Settings.ReleaseRepo = "me/repo"
	a.mu.Lock()
	a.releaseSourceDraft = ""
	a.mu.Unlock()
	t2 := ui.NewTester(a.PrefsView, 620, 720)
	t2.Frame()
	t2.Frame()
	if _, ok := t2.Find("API 根"); !ok {
		t.Fatal("自定义源应当多一行 API 根")
	}

	// 切源：仓库名还是空/上一个源的默认值时，换成新源上的镜像（下拉回调走的就是这条）
	a.SaveUpdateSource(update.SourceCustom, "", "")
	a.switchUpdateSource(update.SourceGitee)
	if got := a.Settings.ReleaseSource; got != update.SourceGitee {
		t.Fatalf("切源没生效：%q", got)
	}
	if got := a.Settings.ReleaseRepo; got != a.UpdateRepoFor(update.SourceGitee) {
		t.Fatalf("切源没带出镜像仓库名：%q", got)
	}
	if got := settings.Load(a.Paths).ReleaseSource; got != update.SourceGitee {
		t.Fatalf("切源没落盘：%q", got)
	}

	// 用户自己填过仓库名：切源不该把它冲掉
	a.SaveUpdateSource(update.SourceGitee, "my/own-repo", "")
	a.switchUpdateSource(update.SourceGitHub)
	if got := a.Settings.ReleaseRepo; got != "my/own-repo" {
		t.Fatalf("自填仓库名被切源冲掉了：%q", got)
	}
}
