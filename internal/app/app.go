// Package app 是运行时装配：状态、动作（粘贴/复制/删/标记/导出）与窗口/托盘。
package app

import (
	"sync"
	"sync/atomic"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/transfer"
	"github.com/egoist/mygo/ui"

	"clipnest/internal/clip"
	"clipnest/internal/db"
	"clipnest/internal/model"
	"clipnest/internal/paths"
	"clipnest/internal/settings"
	"clipnest/internal/store"
)

// PageSize 是面板一次列多少条。
const PageSize = 200

// Row 是面板里一行的预计算结果。
type Row struct {
	ID      int64
	Kind    model.ItemKind
	Tag     string
	Summary string
	Time    string
	// CreatedAt 是「首次入库」的时刻（预览里显示，列表用相对时间）
	CreatedAt  string
	ThumbPath  string
	OriginPath string
	Width      int64
	Height     int64
	Pinned     bool
	Favorite   bool
	Sensitive  bool
	UseCount   int64
	Content    string
	Note       string
	GroupID    *int64
	GroupName  string
}

// App 是进程内的应用状态。
//
// 锁的约定：a.mu 保护下面所有 *可变* 状态（标注 atomic 的除外），
// 拿到锁再碰字段；原子字段（sync*）可以从任何 goroutine 读写。
// 采集回调、菜单回调、视图构建都跑在主线程，同步轮跑在自己的 goroutine 里，
// 所以「主线程 + mu 保护」这两条就够，不需要给每个子系统单独配锁。
type App struct {
	// —— 地基：数据根、库、设置、回环抑制 ——
	Paths    paths.Paths
	Store    *store.Store
	Settings settings.Settings // 写入在 mu 下（ApplyShortcut 等），读取基本都在主线程
	Guard    *clip.Guard

	// —— 并发原语 ——
	mu         sync.Mutex // 保护下面所有可变状态
	captureMu  sync.Mutex // 序列化采集：OnChanged 可能并发投递，两个回调同时入库会撞唯一键
	sincePrune int        // 上次裁剪之后又进了多少条（裁剪不必每条都跑）
	debounceMu sync.Mutex // 保护 debounce

	// —— 窗口与视图句柄 ——
	panel       *mygo.Window
	prefs       *mygo.Window
	tray        *mygo.Tray
	imageWin    *mygo.Window // 「点预览图放大看」的独立窗口
	imageRow    Row          // 大图窗正在显示的那条
	ready       bool         // 窗口都建好了：之前调 Invalidate 会等主线程而死锁
	visible     bool
	searchInput ui.Handle // 搜索框句柄：点过列表后把焦点还给它，方向键才不被滚动容器吃掉
	searchCtx   *ui.Context
	imageRect   ui.Rect // 右栏图片区域（测试与点击放大用）
	previewCard ui.Rect // 右栏卡片区域
	probe       probes  // 测试观察口，生产路径全 nil

	// —— 列表与分页 ——
	rows    []Row
	sel     int
	query   string
	limit   int   // 列表当前取到第几页（每次 +PageSize）
	hasMore bool  // 库里还有没有下一页（Refresh 多取一条判断）
	total   int64 // 当前查询条件的总条数（顶栏「共 N 条」，不是已加载数）
	// ListScroll 是列表的滚动位置（视图里 TrackScroll 绑定它）
	ListScroll ui.ScrollState
	listViewH  float32 // 列表可视区高度（每帧量），供滚动计算
	previewH   float32 // 右栏卡片高度（每帧量），供图片/正文的高度预算
	// scrollToSel 是一次性的「把选中行滚进可视区」请求（键盘移动时置位）
	scrollToSel bool
	// focusSearchNext 让下一次渲染把焦点放回搜索框
	focusSearchNext bool
	// pending 是本帧要延后执行的动作（改动数据、发命令），由 runPending 统一跑
	pending []func()

	// —— 筛选、分组、多选、备注 ——
	// kindFilter 是顶部筛选：0 全部 / 1 收藏 / 2 置顶 / 3 图片 / 4 文件
	kindFilter int
	// groupFilter 是分组筛选："" 全部、"\x00" 未分组、其它为分组名
	groupFilter string
	groups      []db.Group // 分组缓存（每次 Refresh 一起刷新）
	// marked 是批量操作勾上的条目 id（⌘/Ctrl 点击切换）；刷新列表不丢，删完/唤出清空
	marked map[int64]bool
	// noteDraft/noteFor 是备注编辑器的草稿与它属于哪一条
	noteDraft string
	noteFor   int64
	// paused 为 true 时不再采集（托盘里手动暂停；只在内存里，重启恢复）
	paused bool
	// status 是面板底栏那句提示；syncMsg 是同步写给底栏的（有就优先显示）
	status  string
	syncMsg string

	// —— 设置窗的跨帧草稿 ——
	shortcutDraft string
	shortcutMsg   string
	pruneDays     float64
	clearConfirm  bool
	// 分组管理的草稿（跨帧保留，否则一个字就丢）
	newGroupDraft string
	renameDrafts  map[int64]*string

	// —— 同步（atomic 字段任何 goroutine 可读）——
	syncBusy    atomic.Bool // 「逻辑上在跑」
	syncPending atomic.Bool // 期间的请求，结束后补跑一次
	// syncStartedAt 是当前这轮开始的时刻（UnixNano，0 = 没在跑），卡住时靠它发现
	syncStartedAt atomic.Int64
	// syncAlive 表示真的还有一轮没返回（只有它返回时才清）。syncBusy 卡住时会被强制放开，
	// 但不能因此再起一轮：否则钥匙串弹窗点掉的那一下，之前每次强放攒下的 goroutine
	// 会同时醒来，一起推同一个包（实测一次冒 7 轮）。
	syncAlive atomic.Bool
	// syncStuckLogged 让「卡住」只记一次，不要每 5 秒刷屏
	syncStuckLogged atomic.Bool
	// debounce 是实时模式下的采集防抖定时器
	debounce *time.Timer
	// lastSync/lastSyncVia 是最近一次成功的同步时间与入口
	lastSync    string
	lastSyncVia string

	// —— 平台 ——
	// axPrompted 记录辅助功能提示是否已弹过（只有 macOS 会用到）
	axPrompted atomic.Bool
}

// probes 是测试观察口；生产路径上三个字段都是 nil（零成本）。
type probes struct {
	// drag 在拖拽开始时把载荷交出去
	drag func(transfer.Data)
	// zoom 在点预览图缩放时把 row 交出去
	zoom func(Row)
	// plain 在纯文本粘贴时把 row 交出去（测试里不碰真剪贴板）
	plain func(Row)
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

// Quit 退出应用：先置「正在退出」并收起所有窗口。
//
// 窗口的 OnClose 默认会 PreventDefault 拦下关闭（否则关窗就销毁了），
// 这一步不先做，mygo 的 prepareQuit 会因为「窗口关不掉」把整个退出取消，
// 表现就是：点退出后界面没了、托盘还在、点什么都没反应。
func (a *App) Quit() {
	a.MarkQuitting()
	a.mu.Lock()
	wins := []*mygo.Window{a.panel, a.prefs, a.imageWin}
	tray := a.tray
	a.mu.Unlock()
	for _, w := range wins {
		if w != nil {
			w.Hide()
		}
	}
	// 先把状态栏图标拆掉：从托盘菜单点退出时，菜单还在跟踪中就退进程，
	// AppKit 会把图标留在原地变成「幽灵」——之后点什么都没反应，只能重新登录。
	if tray != nil {
		tray.Destroy()
	}
	// 稍等一下让菜单收起、图标移除完成，再真正退出
	time.AfterFunc(150*time.Millisecond, func() {
		mygo.App.Quit()
	})
}

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

// Selected 取当前选中的行。
func (a *App) Selected() (Row, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.sel < 0 || a.sel >= len(a.rows) {
		return Row{}, false
	}
	return a.rows[a.sel], true
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

// Total 是当前查询条件下的总条数（不是已加载条数）。
func (a *App) Total() int64 {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.total > 0 {
		return a.total
	}
	return int64(len(a.rows))
}

// HasMore 报告库里还有没有下一页。
func (a *App) HasMore() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.hasMore
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

// PanelView 是面板的视图函数（给 ui.View 用）。
func (a *App) PanelView(c *ui.Context) { a.panelView(c) }

// PrefsView 是设置窗的视图函数。
func (a *App) PrefsView(c *ui.Context) { a.prefsView(c) }
