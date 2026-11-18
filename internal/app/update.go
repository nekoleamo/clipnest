// 检查更新：只查不装（理由见 internal/update 的包注释）。
package app

import (
	"context"

	"github.com/egoist/mygo"

	"clipnest/internal/settings"
	"clipnest/internal/update"
)

// updateClient 可注入（测试里指向假服务器）。
var updateClient = update.ClientFor

// 各更新源上的本仓镜像名：切源时库里自带的这对名能直接拿来用。
var sourceRepos = map[string]string{
	update.SourceGitHub: "nekoleamo/clipnest",
	update.SourceGitee:  "null_593_5354/clipnest",
}

// UpdateRepoDraft 是设置窗里「仓库」输入框的绑定值。
func (a *App) UpdateRepoDraft() *string {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.releaseRepoDraft == "" {
		a.releaseRepoDraft = a.Settings.ReleaseRepo
	}
	return &a.releaseRepoDraft
}

// UpdateSourceDraft 是设置窗里「更新源」下拉的绑定值（github/gitee/custom）。
func (a *App) UpdateSourceDraft() *string {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.releaseSourceDraft == "" {
		a.releaseSourceDraft = a.Settings.ReleaseSource
	}
	if a.releaseSourceDraft == "" {
		a.releaseSourceDraft = update.SourceGitHub
	}
	return &a.releaseSourceDraft
}

// UpdateAPIBaseDraft 是「自定义」源时 API 根输入框的绑定值。
func (a *App) UpdateAPIBaseDraft() *string {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.releaseAPIBaseDraft == "" {
		a.releaseAPIBaseDraft = a.Settings.ReleaseAPIBase
	}
	return &a.releaseAPIBaseDraft
}

// SaveUpdateSource 保存更新源、仓库与（自定义时）API 根，顺手清掉上次的检查结果。
func (a *App) SaveUpdateSource(source, repo, apiBase string) {
	next := a.Settings
	next.ReleaseSource = source
	next.ReleaseRepo = repo
	next.ReleaseAPIBase = apiBase
	next = settings.Normalize(next)
	if err := settings.Save(next, a.Paths); err != nil {
		a.SetUpdateStatus("❌ 保存失败：" + err.Error())
		return
	}
	a.mu.Lock()
	a.Settings = next
	a.releaseSourceDraft = next.ReleaseSource
	a.releaseRepoDraft = next.ReleaseRepo
	a.releaseAPIBaseDraft = next.ReleaseAPIBase
	a.updateMsg = ""
	a.updateURL = ""
	a.mu.Unlock()
	a.invalidatePrefs()
}

// SaveUpdateRepo 只存仓库名（保持兼容：老设置的输入框单独保存）。
func (a *App) SaveUpdateRepo(repo string) {
	a.SaveUpdateSource(a.Settings.ReleaseSource, repo, a.Settings.ReleaseAPIBase)
}

// switchUpdateSource 是设置窗下拉切源时做的事：换源，并把仓库名换成该源上的镜像
// （仓库名还是上一个源的默认值/空着时才换，用户自己填过就不动）。
func (a *App) switchUpdateSource(chosen string) {
	repo := a.Settings.ReleaseRepo
	if repo == "" || repo == a.UpdateRepoFor(a.Settings.ReleaseSource) {
		if def := a.UpdateRepoFor(chosen); def != "" {
			repo = def
		}
	}
	a.SaveUpdateSource(chosen, repo, a.Settings.ReleaseAPIBase)
}

// UpdateRepoFor 是切源时该带出的仓库名（自带镜像对，用户改过就不动）。
func (a *App) UpdateRepoFor(source string) string {
	return sourceRepos[source]
}

// CheckUpdate 在后台查一次最新发布（不阻塞界面）。
func (a *App) CheckUpdate() {
	a.mu.Lock()
	repo := a.Settings.ReleaseRepo
	source := a.Settings.ReleaseSource
	apiBase := a.Settings.ReleaseAPIBase
	busy := a.updateBusy
	if busy {
		a.mu.Unlock()
		return
	}
	if repo == "" {
		a.updateMsg = "先在下面填更新源（形如 owner/repo），再点检查"
		a.mu.Unlock()
		a.invalidatePrefs()
		return
	}
	if source == update.SourceCustom && apiBase == "" {
		a.updateMsg = "自定义更新源要填 API 根（例如 https://api.github.com）"
		a.mu.Unlock()
		a.invalidatePrefs()
		return
	}
	a.updateBusy = true
	a.updateMsg = "正在检查…"
	a.mu.Unlock()
	a.invalidatePrefs()

	go func() {
		defer func() {
			a.mu.Lock()
			a.updateBusy = false
			a.mu.Unlock()
			a.invalidatePrefs()
		}()
		client := updateClient(source, apiBase, mygo.App.Version())
		rel, err := client.Latest(context.Background(), repo)
		if err != nil {
			a.SetUpdateStatus("❌ " + err.Error())
			return
		}
		current := mygo.App.Version()
		a.mu.Lock()
		a.updateURL = rel.URL
		a.mu.Unlock()
		if update.Newer(current, rel.Tag) {
			a.SetUpdateStatus("🆕 有新版本 " + rel.Tag + "（当前 " + current + "）")
			logf("UPDATE newer current=%s latest=%s", current, rel.Tag)
			return
		}
		a.SetUpdateStatus("✅ 已是最新（" + current + "，最新 " + rel.Tag + "）")
	}()
}

// OpenReleasePage 用默认浏览器打开最新发布页。
func (a *App) OpenReleasePage() {
	a.mu.Lock()
	url := a.updateURL
	a.mu.Unlock()
	if url == "" {
		a.SetUpdateStatus("先检查一次更新，才有发布页可打开")
		return
	}
	if err := mygo.Shell.OpenExternal(url); err != nil {
		a.SetUpdateStatus("❌ 打不开浏览器：" + err.Error())
	}
}

// UpdateStatus 是「检查更新」那行文案与是否已有发布页链接。
func (a *App) UpdateStatus() (msg string, hasURL bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.updateMsg, a.updateURL != ""
}

// UpdateChecking 报告是否正在检查（按钮禁用用）。
func (a *App) UpdateChecking() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.updateBusy
}

func (a *App) SetUpdateStatus(msg string) {
	a.mu.Lock()
	a.updateMsg = msg
	a.mu.Unlock()
	a.invalidatePrefs()
}

// AppVersion 是当前版本（设置窗里显示）。
func (a *App) AppVersion() string {
	if v := mygo.App.Version(); v != "" {
		return v
	}
	return "dev"
}
