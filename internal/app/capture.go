// 采集与保留策略：剪贴板变化入库、暂停开关、按条数裁剪。
package app

import (
	"strings"

	"clipnest/internal/clip"
	"clipnest/internal/sensitive"
)

// Capture 处理一次剪贴板变化：采集 → 敏感过滤 → 回环抑制 → 落库。
func (a *App) Capture() {
	a.captureMu.Lock()
	defer a.captureMu.Unlock()

	if a.Paused() {
		return // 手动暂停：连读剪贴板都省了
	}
	cap, snap, ok := clip.Capture(clip.MygoSource{})
	if !ok {
		return
	}
	hashes := clip.Hashes(cap)
	if a.Guard.Consume(hashes...) {
		logf("CAPTURE skipped self-write flavors=%s", snap.Flavors)
		return
	}

	sensitiveOn := sensitive.ShouldSkip(sensitive.Config{
		SensitiveApps:       a.Settings.SensitiveApps,
		IgnoreSensitiveApps: a.Settings.IgnoreSensitiveApps,
	}, sensitive.FrontApp())
	if sensitiveOn && cap.Image == nil {
		logf("CAPTURE skipped sensitive app flavors=%s", snap.Flavors)
		return
	}

	if sensitiveOn {
		if cap.Image != nil {
			// 图片来自敏感应用时整条跳过（内容和尺寸都没意义，别落盘）
			logf("CAPTURE skipped sensitive image")
			return
		}
	}

	stored, err := a.Store.Record(cap)
	if err != nil {
		logf("STORE ERROR %v", err)
		return
	}
	var labels []string
	for _, s := range stored {
		labels = append(labels, s.Label())
	}
	logf("CAPTURE flavors=%s excerpt=%q %s", snap.Flavors, snap.Excerpt, strings.Join(labels, " "))
	a.Prune()
	a.Refresh()
	a.RequestRealtimeSync() // 实时模式：1.2 秒后把这条推到远端
}

// Paused 报告是否暂停采集。
func (a *App) Paused() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.paused
}

// TogglePause 暂停/恢复采集；暂停期间复制的东西不会进历史。
//
// 只存在内存里：重启后自动恢复采集，免得忘了恢复、以为程序坏了。
func (a *App) TogglePause() {
	a.mu.Lock()
	a.paused = !a.paused
	paused := a.paused
	tray := a.tray
	a.mu.Unlock()
	logf("CAPTURE paused=%v", paused)
	if paused {
		a.SetStatus("⏸ 已暂停记录，恢复请点菜单栏图标")
	} else {
		a.SetStatus("▶ 已恢复记录")
		a.RequestRealtimeSync()
	}
	if tray != nil {
		tray.SetMenu(a.buildMenu())
	}
}

// Prune 按设置裁剪历史（收藏与置顶受保护）。
func (a *App) Prune() {
	n, err := a.Store.Prune(a.Settings.MaxItems)
	if err != nil {
		logf("PRUNE ERROR %v", err)
		return
	}
	if n > 0 {
		logf("PRUNE deleted=%d", n)
	}
}
