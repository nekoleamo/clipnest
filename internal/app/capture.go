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

	// 采集回调可能并发投递（不在主线程），设置要从锁里读一份
	set := a.settingsSnapshot()
	sensitiveOn := sensitive.ShouldSkip(sensitive.Config{
		SensitiveApps:       set.SensitiveApps,
		IgnoreSensitiveApps: set.IgnoreSensitiveApps,
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
	a.pruneIfDue()
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

// pruneEvery 是「隔多少条新记录才裁剪一次」。
//
// 裁剪要两条查询（数总数 + 找出该删的），而每复制一次就跑一遍纯属浪费：
// 条数上限是软约束，短暂超出十几条用户看不见。启动时仍会完整裁一次。
const pruneEvery = 16

// pruneIfDue 按计数决定这一条要不要触发裁剪（调用方持 captureMu）。
func (a *App) pruneIfDue() {
	a.mu.Lock()
	a.sincePrune++
	due := a.sincePrune >= pruneEvery
	if due {
		a.sincePrune = 0
	}
	a.mu.Unlock()
	if due {
		a.Prune()
	}
}

// Prune 按设置裁剪历史（收藏与置顶受保护）。
func (a *App) Prune() {
	n, err := a.Store.Prune(a.settingsSnapshot().MaxItems)
	if err != nil {
		logf("PRUNE ERROR %v", err)
		return
	}
	if n > 0 {
		logf("PRUNE deleted=%d", n)
	}
}
