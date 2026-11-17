// 同步：定时/实时触发、状态与看门狗。
package app

import (
	"fmt"
	"time"

	"clipnest/internal/model"
	clipsync "clipnest/internal/sync"
)

// StartSync 在后台跑一轮同步（UI 不卡）。
func (a *App) StartSync() {
	clipsync.ResetEndpointCache() // 手动同步：重新探一次局域网/Tailscale
	go a.runSync(true)
}

// CheckSyncStuck 检查当前这轮是不是卡太久了。
//
// 两分钟：只告警 + 让调用方停止再叠加新轮次（卡住的那一轮还在，攒着没用）。
// 十分钟：认为它不会回来了，强制放开标志并返回 true（调用方可以重开一轮）。
//
// 返回 true 表示可以强行开始新一轮。
func (a *App) CheckSyncStuck() bool {
	started := a.syncStartedAt.Load()
	if started == 0 {
		return false
	}
	elapsed := time.Since(time.Unix(0, started))
	if elapsed >= SyncHardStuckAfter {
		a.syncBusy.Store(false)
		a.syncStartedAt.Store(0)
		a.syncPending.Store(false)
		logf("SYNC hard stuck %s：放弃这一轮重开（常见原因：钥匙串授权弹窗一直没人点）", elapsed.Round(time.Second))
		a.setSyncStatus("⚠️ 同步卡了 10 分钟，已放弃这一轮重开：可能有个钥匙串授权弹窗在等确认")
		return true
	}
	if elapsed >= SyncStuckAfter && a.syncStuckLogged.CompareAndSwap(false, true) {
		logf("SYNC stuck %s：先不再叠加新轮次（可能在等钥匙串授权）", elapsed.Round(time.Second))
		a.setSyncStatus("⚠️ 同步卡住了：可能有个钥匙串授权弹窗在等确认")
	}
	return false
}

// RequestRealtimeSync 是采集后的防抖触发：1.2 秒内多次复制只跑一轮。
func (a *App) RequestRealtimeSync() {
	cfg := clipsync.LoadConfig(a.Paths)
	if !cfg.Enabled || !cfg.Realtime {
		return
	}
	a.debounceMu.Lock()
	if a.debounce != nil {
		a.debounce.Stop()
	}
	a.debounce = time.AfterFunc(1200*time.Millisecond, func() { a.runSync(false) })
	a.debounceMu.Unlock()
}

// runSync 跑一轮同步；loud 为 true 时把过程写进面板底部状态。
//
// 同一时刻只跑一轮：正在跑时再来的请求置 pending，结束后补跑一次。
// SyncStuckAfter 是一轮同步最多允许占用的时间。
//
// 超时就认为它卡死了：常见原因是钥匙串授权弹窗没人点（round 里取口令会一直等），
// 或者网络栈卡在某个不可路由的地址上。卡住时 syncBusy 一直是 true，后面每一轮都会
// 被当成「正在跑」静默跳过——表现就是同步再也没动静，日志里也什么都没有。
const SyncStuckAfter = 2 * time.Minute

// SyncHardStuckAfter 是「彻底卡死」的门槛：到这一步才放弃这一轮、允许重开。
//
// 两分钟的阈值只负责告警（并停止再叠加新轮次）；十分钟还没回来就认为它不会回来了
// （zombie），强制放开一次，接受可能的重入（库串行、推送幂等，代价可控）。
const SyncHardStuckAfter = 10 * time.Minute

func (a *App) runSync(loud bool) {
	if !a.syncBusy.CompareAndSwap(false, true) {
		a.syncPending.Store(true)
		return
	}
	a.syncStartedAt.Store(time.Now().UnixNano())
	a.syncAlive.Store(true)
	defer func() {
		a.syncAlive.Store(false)
		a.syncStuckLogged.Store(false)
		a.syncStartedAt.Store(0)
		a.syncBusy.Store(false)
		if a.syncPending.Swap(false) {
			a.runSync(false)
		}
	}()

	cfg := clipsync.LoadConfig(a.Paths)
	if err := cfg.Ready(); err != nil {
		if loud {
			a.setSyncStatus("⚠️ " + err.Error())
		}
		return
	}
	if loud {
		a.setSyncStatus("同步中…")
	}
	report, err := clipsync.RunOnce(a.Store.DB, cfg, a.Paths)
	if err != nil {
		logf("SYNC ERROR %v", err)
		a.setSyncStatus("❌ " + err.Error())
		return
	}
	applied := report.Applied.Inserted + report.Applied.Updated + report.Applied.Deleted
	// 实时模式下 5 秒一轮：只有真有变化（或入口换了）才记日志，否则日志几小时就上兆
	moved := report.Pushed > 0 || applied > 0 || report.BlobsPushed > 0 || report.BlobsPulled > 0
	a.mu.Lock()
	endpointChanged := a.lastSyncVia != "" && a.lastSyncVia != report.Endpoint
	a.lastSync = model.Now()
	a.lastSyncVia = report.Endpoint
	a.mu.Unlock()
	if moved || endpointChanged || loud {
		logf("SYNC ok via=%s pushed=%d pulled=%d +%d ~%d -%d blobs=%d/%d",
			report.Endpoint, report.Pushed, report.Pulled, report.Applied.Inserted,
			report.Applied.Updated, report.Applied.Deleted, report.BlobsPushed, report.BlobsPulled)
	}
	// 实时模式下「什么都没变」就不吐状態，免得每 5 秒闪一下
	if loud || report.Pushed > 0 || applied > 0 {
		a.setSyncStatus(fmt.Sprintf("✅ %s · 推 %d / 拉 %d（改动 %d）",
			report.Endpoint, report.Pushed, report.Pulled, applied))
	}
	if applied > 0 {
		a.Refresh()
	}
}

// SyncStatus 是最近一次同步的状态文字。
func (a *App) SyncStatus() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.syncMsg != "" {
		return a.syncMsg
	}
	if a.lastSync == "" {
		return ""
	}
	return "上次同步 " + model.RelativeTime(a.lastSync) + " · " + a.lastSyncVia
}

func (a *App) setSyncStatus(text string) {
	a.mu.Lock()
	a.syncMsg = text
	a.mu.Unlock()
	a.invalidate()
}

// StartAutoSync 开自动同步：实时模式走短间隔拉取，否则用 IntervalSecs。
func (a *App) StartAutoSync() {
	cfg := clipsync.LoadConfig(a.Paths)
	if !cfg.Enabled {
		return
	}
	period := time.Duration(cfg.IntervalSecs) * time.Second
	if cfg.Realtime {
		period = time.Duration(cfg.PullIntervalSecs()) * time.Second
	} else if cfg.IntervalSecs < 60 {
		return
	}
	logf("SYNC loop period=%s realtime=%v", period, cfg.Realtime)
	go func() {
		ticker := time.NewTicker(period)
		defer ticker.Stop()
		for range ticker.C {
			hard := a.CheckSyncStuck()
			if !clipsync.LoadConfig(a.Paths).Enabled {
				continue
			}
			// 还有一轮真没返回（哪怕被判卡住）就别再叠加：攒着的话，钥匙串弹窗
			// 一被允许，所有停着的轮次会同时醒来一起推同一个包。
			if a.syncAlive.Load() && !hard {
				continue
			}
			// 必须另起 goroutine：这一轮如果卡在钥匙串弹窗上，
			// 直接在这里同步调用会把 ticker 循环本身堵死，看门狗跟着一起停摆。
			go a.runSync(false)
		}
	}()
}
