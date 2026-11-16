// Package app 是运行时装配：状态、动作（粘贴/复制/删/标记/导出）与窗口/托盘。
package app

import (
	"fmt"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"

	"clipnest/internal/clip"
	"clipnest/internal/db"
	"clipnest/internal/model"
	"clipnest/internal/paste"
	"clipnest/internal/paths"
	"clipnest/internal/sensitive"
	"clipnest/internal/settings"
	"clipnest/internal/store"
	clipsync "clipnest/internal/sync"
)

// PageSize 是面板一次列多少条。
const PageSize = 200

// Row 是面板里一行的预计算结果。
type Row struct {
	ID         int64
	Kind       model.ItemKind
	Tag        string
	Summary    string
	Time       string
	ThumbPath  string
	OriginPath string
	Width      int64
	Height     int64
	Pinned     bool
	Favorite   bool
	Sensitive  bool
	Note       string
}

// App 是进程内的应用状态。
type App struct {
	Paths    paths.Paths
	Store    *store.Store
	Settings settings.Settings
	Guard    *clip.Guard

	mu      sync.Mutex
	panel   *mygo.Window
	prefs   *mygo.Window
	tray    *mygo.Tray
	rows    []Row
	sel     int
	query   string
	status  string
	syncMsg string
	visible bool
	// ready 在窗口都建好、App.Run 即将开始时置位：之前调 Invalidate 会等主线程而死锁。
	ready bool
	// captureMu 序列化采集（OnChanged 可能并发投递，两个回调同时入库会撞唯一键）
	captureMu sync.Mutex
	// syncBusy/syncPending 保证同一时刻只跑一轮同步，期间的请求结束后补跑
	syncBusy    atomic.Bool
	syncPending atomic.Bool
	// debounce 是实时模式下的采集防抖定时器
	debounceMu sync.Mutex
	debounce   *time.Timer
	// axPrompted 记录辅助功能提示是否已弹过
	axPrompted atomic.Bool
	// lastSync/lastSyncVia 是最近一次成功的同步时间与入口
	lastSync    string
	lastSyncVia string
	// opens 是打开次数，面板据此判断「又开了一次」要不要重置查询。
	opens int
}

// New 建好应用（不启动窗口）。
func New(p paths.Paths) (*App, error) {
	st, err := store.Open(p)
	if err != nil {
		return nil, err
	}
	return &App{
		Paths:    p,
		Store:    st,
		Settings: settings.Load(p),
		Guard:    clip.NewGuard(),
		rows:     nil,
	}, nil
}

// logf 是应用日志（写文件 + stderr）。
func logf(format string, args ...any) {
	store.Logf(format, args...)
	settings.Logf(format, args...)
}

// SetStatus 更新面板底部的状态文字。
func (a *App) SetStatus(text string) {
	a.mu.Lock()
	a.status = text
	a.mu.Unlock()
	a.invalidate()
}

// invalidate 请求重画（可以从任何 goroutine 调）。
func (a *App) invalidate() {
	a.mu.Lock()
	ready := a.ready
	w := a.panel
	a.mu.Unlock()
	if ready && w != nil {
		w.Invalidate()
	}
}

// quitting 表示已经进入退出流程：此时窗口的 OnClose 不该再 PreventDefault，
// 否则 mygo 的 prepareQuit 会因为「窗口关不掉」取消整个退出。
var quitting atomic.Bool

// MarkQuitting 在 OnBeforeQuit 里调用。
func (a *App) MarkQuitting() { quitting.Store(true) }

// Quitting 报告是否正在退出。
func (a *App) Quitting() bool { return quitting.Load() }

// SetReady 在窗口建好之后调用：之后才允许触发重画。
func (a *App) SetReady() {
	a.mu.Lock()
	a.ready = true
	w := a.panel
	a.mu.Unlock()
	if w != nil {
		w.Invalidate()
	}
}

func (a *App) panelWindow() *mygo.Window {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.panel
}

// SetPanel 记住面板窗口句柄。
func (a *App) SetPanel(w *mygo.Window) {
	a.mu.Lock()
	a.panel = w
	a.mu.Unlock()
}

// Refresh 重新查一遍列表（动作后调用），并把选中位置夹回范围内。
func (a *App) Refresh() {
	a.mu.Lock()
	q := a.query
	a.mu.Unlock()

	items, err := db.Query(a.Store.DB, q, db.Filter{}, db.Page{Limit: PageSize})
	if err != nil {
		logf("QUERY ERROR %v", err)
		a.SetStatus("❌ 读库失败：" + err.Error())
		return
	}
	rows := make([]Row, 0, len(items))
	for _, item := range items {
		rows = append(rows, a.rowOf(item))
	}

	a.mu.Lock()
	a.rows = rows
	if a.sel >= len(rows) {
		a.sel = max(0, len(rows)-1)
	}
	a.mu.Unlock()
	a.invalidate()
}

// SetQuery 改搜索词并重查。
func (a *App) SetQuery(q string) {
	a.mu.Lock()
	a.query = q
	a.sel = 0
	a.mu.Unlock()
	a.Refresh()
}

func (a *App) rowOf(item model.Item) Row {
	row := Row{
		ID:        item.ID,
		Kind:      item.Kind,
		Tag:       TagOf(item.Kind),
		Summary:   SummaryOf(item),
		Time:      model.RelativeTime(item.UpdatedAt),
		Width:     item.Width,
		Height:    item.Height,
		Pinned:    item.Pinned,
		Favorite:  item.Favorite,
		Sensitive: item.Sensitive,
		Note:      item.Note,
	}
	if item.Kind == model.KindImage {
		hash := strings.TrimSuffix(item.Content, ".png")
		if thumb := a.Paths.ImageThumbnail(hash); fileExists(thumb) {
			row.ThumbPath = thumb
		}
		if origin := a.Paths.ImageOrigin(hash); fileExists(origin) {
			row.OriginPath = origin
		}
	}
	return row
}

// TagOf 是类型标签（中文两字，省空间）。
func TagOf(kind model.ItemKind) string {
	switch kind {
	case model.KindText:
		return "文本"
	case model.KindHtml:
		return "HTML"
	case model.KindRtf:
		return "RTF"
	case model.KindImage:
		return "图片"
	case model.KindFiles:
		return "文件"
	default:
		return string(kind)
	}
}

// SummaryOf 是列表摘要：文件给首个文件名 + 数量，图片给尺寸，其余压平换行截断。
func SummaryOf(item model.Item) string {
	switch item.Kind {
	case model.KindFiles:
		pathsList := strings.Split(item.Content, "\n")
		if len(pathsList) == 0 {
			return ""
		}
		name := baseName(pathsList[0])
		if len(pathsList) > 1 {
			return fmt.Sprintf("%s 等 %d 项", name, len(pathsList))
		}
		return name
	case model.KindImage:
		return fmt.Sprintf("%d×%d", item.Width, item.Height)
	default:
		return item.Excerpt(80)
	}
}

// Selected 取当前选中的行。
func (a *App) Selected() (Row, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.sel < 0 || a.sel >= len(a.rows) {
		return Row{}, false
	}
	return a.rows[a.sel], true
}

// Move 上下移动选中（delta 为 ±1）。
func (a *App) Move(delta int) {
	a.mu.Lock()
	next := a.sel + delta
	if next < 0 {
		next = 0
	}
	if n := len(a.rows); next >= n {
		next = max(0, n-1)
	}
	a.sel = next
	a.mu.Unlock()
	a.invalidate()
}

// Select 直接选中第 index 行。
func (a *App) Select(index int) {
	a.mu.Lock()
	if index >= 0 && index < len(a.rows) {
		a.sel = index
	}
	a.mu.Unlock()
	a.invalidate()
}

// SelectSlot 选中第 n 条（⌘1-9 用）。
func (a *App) SelectSlot(n int) bool {
	a.mu.Lock()
	ok := n >= 0 && n < len(a.rows)
	if ok {
		a.sel = n
	}
	a.mu.Unlock()
	if ok {
		a.invalidate()
	}
	return ok
}

// RowAt 取第 index 行。
func (a *App) RowAt(index int) (Row, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if index < 0 || index >= len(a.rows) {
		return Row{}, false
	}
	return a.rows[index], true
}

// Rows 返回当前行的快照。
func (a *App) Rows() []Row {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]Row(nil), a.rows...)
}

// Selection 返回当前选中下标与总数。
func (a *App) Selection() (int, int) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.sel, len(a.rows)
}

// Query 返回当前搜索词。
func (a *App) Query() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.query
}

// Status 返回面板底部状态文字。
func (a *App) Status() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.syncMsg != "" {
		return a.syncMsg
	}
	return a.status
}

// Paste 粘贴一行：写回剪贴板 → 计一次使用 → 收起面板 → 发 ⌘V。
func (a *App) Paste(row Row) bool {
	item, ok, err := db.Get(a.Store.DB, row.ID)
	if err != nil || !ok {
		logf("PASTE missing id=%d err=%v", row.ID, err)
		return false
	}
	hashes, err := clip.WriteBack(item, a.Paths)
	if err != nil {
		logf("PASTE write-back failed: %v", err)
		a.SetStatus("❌ " + err.Error())
		return false
	}
	a.Guard.Expect(hashes...)
	if err := db.Touch(a.Store.DB, item.ID); err != nil {
		logf("PASTE touch failed: %v", err)
	}
	logf("PASTE id=%d kind=%s", item.ID, item.Kind)
	a.Dismiss()
	pasteAfterWriteBack()
	return true
}

// Copy 复制到剪贴板但不粘贴（留在面板里，方便接着选别的）。
func (a *App) Copy(row Row) bool {
	item, ok, err := db.Get(a.Store.DB, row.ID)
	if err != nil || !ok {
		return false
	}
	hashes, err := clip.WriteBack(item, a.Paths)
	if err != nil {
		a.SetStatus("❌ " + err.Error())
		return false
	}
	a.Guard.Expect(hashes...)
	if err := db.Touch(a.Store.DB, item.ID); err != nil {
		logf("COPY touch failed: %v", err)
	}
	a.SetStatus("已复制到剪贴板")
	a.Refresh()
	// 写剪贴板文件后前台可能被抢回原应用，重新取一次键盘焦点。
	paste.ClaimForeground()
	return true
}

// Remove 删一条（图片等资源连带删）。
func (a *App) Remove(row Row) bool {
	ok, err := a.Store.Remove(row.ID)
	if err != nil {
		logf("REMOVE failed id=%d: %v", row.ID, err)
		return false
	}
	if !ok {
		return false
	}
	a.Refresh()
	a.SetStatus("已删除")
	return true
}

// Flag 是可以单独切换的标记。
type Flag int

// 可切换的标记。
const (
	FlagPinned Flag = iota
	FlagFavorite
	FlagSensitive
)

// ToggleFlag 切换置顶/收藏/敏感。契约 6：这类改动不刷新 updated_at。
func (a *App) ToggleFlag(row Row, flag Flag) bool {
	var patch db.MetaPatch
	var next bool
	switch flag {
	case FlagPinned:
		next = !row.Pinned
		patch.Pinned = &next
	case FlagFavorite:
		next = !row.Favorite
		patch.Favorite = &next
	case FlagSensitive:
		next = !row.Sensitive
		patch.Sensitive = &next
	}
	if err := db.UpdateMeta(a.Store.DB, row.ID, patch); err != nil {
		logf("FLAG failed id=%d: %v", row.ID, err)
		return false
	}
	a.Refresh()
	return next
}

// Export 导出当前条目成文件，并把文件放进剪贴板（拖拽导出的降级方案）。
//
// files 条目不用导出：它们本来就在磁盘上，直接放进剪贴板。
func (a *App) Export(row Row) (string, error) {
	item, ok, err := db.Get(a.Store.DB, row.ID)
	if err != nil || !ok {
		return "", fmt.Errorf("条目不存在")
	}
	var files []string
	switch item.Kind {
	case model.KindFiles:
		for _, line := range strings.Split(item.Content, "\n") {
			if line != "" && fileExists(line) {
				files = append(files, line)
			}
		}
		if len(files) == 0 {
			return "", fmt.Errorf("文件条目里的路径都不在了")
		}
	case model.KindImage:
		hash := strings.TrimSuffix(item.Content, ".png")
		origin := a.Paths.ImageOrigin(hash)
		dest := filepathJoin(a.exportDir(), hash+".png")
		if err := copyFile(origin, dest); err != nil {
			return "", err
		}
		files = []string{dest}
	default:
		name := fmt.Sprintf("clipnest-%d.txt", item.ID)
		dest := filepathJoin(a.exportDir(), name)
		if err := writeFile(dest, []byte(item.Content)); err != nil {
			return "", err
		}
		files = []string{dest}
	}

	hashes, err := clip.PutFiles(files)
	if err != nil {
		return "", err
	}
	a.Guard.Expect(hashes...)
	logf("EXPORT id=%d files=%v", item.ID, files)
	paste.ClaimForeground()
	return files[0], nil
}

func (a *App) exportDir() string {
	if a.Settings.ExportDir != "" {
		return a.Settings.ExportDir
	}
	home, err := homeDir()
	if err != nil {
		return "."
	}
	return filepathJoin(home, "Downloads")
}

// TogglePanel 显示/收起面板（快捷键与托盘共用）。
func (a *App) TogglePanel() {
	w := a.panelWindow()
	if w == nil {
		return
	}
	if w.IsVisible() {
		a.Dismiss()
		return
	}
	a.Show()
}

// Show 打开面板：记下前台应用、摆位置、显示。
func (a *App) Show() {
	w := a.panelWindow()
	if w == nil {
		return
	}
	logf("SHOW ax=%v", paste.Trusted())
	// 面板不激活 app，但仍要记住「刚才在哪个应用」以便粘贴后切回去。
	paste.RememberForeground()

	a.mu.Lock()
	a.opens++
	a.query = ""
	a.sel = 0
	a.visible = true
	a.status = ""
	a.syncMsg = ""
	a.mu.Unlock()
	a.Refresh()

	// 位置：光标所在屏幕，居中或贴底（运行时就能算，不像 nsh 只能创建时定）
	d := mygo.Screen.DisplayNearestPoint(mygo.Screen.CursorScreenPoint())
	width, height := w.Size()
	y := d.WorkArea.Y + (d.WorkArea.Height-height)/2
	if a.Settings.PanelPosition == settings.PositionBottom {
		y = d.WorkArea.Y + d.WorkArea.Height - height - 24
	}
	w.SetPosition(d.WorkArea.X+(d.WorkArea.Width-width)/2, y)
	w.Show()

	// Windows 上面板是前台窗口才收得到键盘；macOS 上 NSPanel 自己就是 key window。
	if !paste.ClaimForeground() {
		logf("PANEL claim-foreground refused")
	}
}

// Dismiss 收起面板（粘贴完成后调用）。
func (a *App) Dismiss() {
	w := a.panelWindow()
	if w == nil {
		return
	}
	logf("DISMISS")
	w.Hide()
	a.mu.Lock()
	a.visible = false
	a.mu.Unlock()
	paste.ReturnToPreviousApp()
}

// pasteAfterWriteBack 延后 120ms 发 ⌘V（面板已经收起、前台已切回）。
func pasteAfterWriteBack() {
	go func() {
		time.Sleep(120 * time.Millisecond)
		if !paste.Trusted() {
			logf("AUTOPASTE skipped: 没有辅助功能授权")
			return
		}
		ok := paste.SendCmdV()
		logf("AUTOPASTE cmd+v posted ok=%v", ok)
	}()
}

// Capture 处理一次剪贴板变化：采集 → 敏感过滤 → 回环抑制 → 落库。
func (a *App) Capture() {
	a.captureMu.Lock()
	defer a.captureMu.Unlock()

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

// StartSync 在后台跑一轮同步（UI 不卡）。
func (a *App) StartSync() {
	clipsync.ResetEndpointCache() // 手动同步：重新探一次局域网/Tailscale
	go a.runSync(true)
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
func (a *App) runSync(loud bool) {
	if !a.syncBusy.CompareAndSwap(false, true) {
		a.syncPending.Store(true)
		return
	}
	defer func() {
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
	logf("SYNC ok via=%s pushed=%d pulled=%d +%d ~%d -%d blobs=%d/%d",
		report.Endpoint, report.Pushed, report.Pulled, report.Applied.Inserted,
		report.Applied.Updated, report.Applied.Deleted, report.BlobsPushed, report.BlobsPulled)
	a.mu.Lock()
	a.lastSync = model.Now()
	a.lastSyncVia = report.Endpoint
	a.mu.Unlock()
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
			if clipsync.LoadConfig(a.Paths).Enabled {
				a.runSync(false)
			}
		}
	}()
}

// SortRowsStable 让行顺序稳定（调试用）。
func SortRowsStable(rows []Row) {
	sort.SliceStable(rows, func(i, j int) bool { return rows[i].ID < rows[j].ID })
}

// RequestAccessibility 处理「去授权」：
//
// 第一次弹系统的辅助功能授权提示（它会顺手把 app 加进名单），用户点里面的
// 「打开系统设置」就行——不再自己另开一个设置窗，免得两个窗口竖在一起。
// 已经弹过还没授权（用户拒了或早就弹过不再弹）时，直接开设置页面。
func (a *App) RequestAccessibility() {
	first := a.axPrompted.CompareAndSwap(false, true)
	if first {
		logf("AXP prompt (first)")
		go func() {
			trusted := paste.PromptTrust()
			logf("AXP prompt returned trusted=%v", trusted)
		}()
		return
	}
	logf("AXP open settings (already prompted)")
	openAccessibilitySettings()
}

// HideAXHint 收起面板顶部的授权提示（写进 settings.json，重启也不再弹）。
// 拖盘菜单里有「显示授权提示」可以把它弄回来。
func (a *App) HideAXHint() {
	next := a.Settings
	next.HideAccessibilityHint = true
	if err := settings.Save(next, a.Paths); err != nil {
		logf("设置保存失败：%v", err)
		return
	}
	a.mu.Lock()
	a.Settings = next
	a.mu.Unlock()
	a.invalidate()
}

// ShowAXHint 重新显示授权提示。
func (a *App) ShowAXHint() {
	next := a.Settings
	next.HideAccessibilityHint = false
	if err := settings.Save(next, a.Paths); err != nil {
		logf("设置保存失败：%v", err)
		return
	}
	a.mu.Lock()
	a.Settings = next
	a.mu.Unlock()
	a.invalidate()
}

// PanelView 是面板的视图函数（给 ui.View 用）。
func (a *App) PanelView(c *ui.Context) { a.panelView(c) }

// PrefsView 是设置窗的视图函数。
func (a *App) PrefsView(c *ui.Context) { a.prefsView(c) }
