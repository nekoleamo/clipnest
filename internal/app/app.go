// Package app 是运行时装配：状态、动作（粘贴/复制/删/标记/导出）与窗口/托盘。
package app

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/transfer"
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
	UseCount   int64
	Content    string
	Note       string
	GroupID    *int64
	GroupName  string
}

// App 是进程内的应用状态。
type App struct {
	Paths    paths.Paths
	Store    *store.Store
	Settings settings.Settings
	Guard    *clip.Guard

	mu    sync.Mutex
	panel *mygo.Window
	prefs *mygo.Window
	tray  *mygo.Tray
	rows  []Row
	sel   int
	query string
	// limit 是列表当前取到第几页（每次+PageSize）；hasMore 表示库里还有更多；
	// total 是当前查询条件下的总条数（顶栏「共 N 条」用它，不是已加载数）
	limit   int
	hasMore bool
	total   int64
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
	// syncStartedAt 是当前这轮开始的时刻（UnixNano，0 = 没在跑），卡住时靠它发现
	syncStartedAt atomic.Int64
	// debounce 是实时模式下的采集防抖定时器
	debounceMu sync.Mutex
	debounce   *time.Timer
	// axPrompted 记录辅助功能提示是否已弹过
	axPrompted atomic.Bool
	// dragProbe 只在测试里用：拖拽开始时把载荷交出去断言
	dragProbe func(transfer.Data)
	// imageRect 是右栏图片区域的位置（测试与点击放大用）
	imageRect ui.Rect
	// previewCard 是右栏卡片的区域
	previewCard ui.Rect
	// zoomProbe 只在测试里用：点预览图时把 row 交出去断言
	zoomProbe func(Row)
	// plainProbe 只在测试里用：纯文本粘贴时把 row 交出去断言（不碰真剪贴板）
	plainProbe func(Row)
	// imageWin/imageRow 是「点预览图放大看」的独立窗口
	imageWin *mygo.Window
	imageRow Row
	// pending 是本帧要延后执行的动作（改动数据、发命令），由 runPending 统一跑
	pending []func()
	// searchInput 是搜索框的句柄：点过列表后把焦点还给它，
	// 这样方向键仍然走面板快捷键，而不会被滚动容器吃掉
	searchInput ui.Handle
	searchCtx   *ui.Context
	// focusSearchNext 让下一次渲染把焦点放回搜索框
	focusSearchNext bool
	// scrollToSel 是一次性的「把选中行滚进可视区」请求（键盘移动时置位）
	scrollToSel bool
	// ListScroll 是列表的滚动位置（视图里 TrackScroll 绑定它）
	ListScroll ui.ScrollState
	// listViewH 是列表可视区高度（每帧量），供滚动计算用
	listViewH float32
	// previewH 是右栏卡片的高度（每帧量），供图片/正文的高度预算用
	previewH float32
	// 设置窗里的跨帧草稿与状态
	shortcutDraft string
	shortcutMsg   string
	pruneDays     float64
	clearConfirm  bool
	// kindFilter 是顶部筛选：0 全部 / 1 收藏 / 2 置顶 / 3 图片 / 4 文件
	kindFilter int
	// groupFilter 是分组筛选："" 全部、"\x00" 未分组、其它为分组名
	groupFilter string
	// groups 是分组缓存（每次 Refresh 一起刷新）
	groups []db.Group
	// noteDraft/noteFor 是备注编辑器的草稿与它属于哪一条
	noteDraft string
	noteFor   int64
	// 设置窗里分组管理的草稿（跨帧保留，否则一个字就丢）
	newGroupDraft string
	renameDrafts  map[int64]*string
	// lastSync/lastSyncVia 是最近一次成功的同步时间与入口
	lastSync    string
	lastSyncVia string
	// paused 为 true 时不再采集（托盘里手动暂停；只在内存里，重启恢复采集）
	paused bool
	// marked 是批量操作勾上的条目 id（⌘/Ctrl 点击切换）；刷新列表不丢，删完/唤出清空
	marked map[int64]bool
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

// Refresh 重新查一遍列表（动作后调用），并把选中位置夹回范围内。
func (a *App) Refresh() {
	a.mu.Lock()
	q := a.query
	filter := a.currentFilterLocked()
	limit := a.limit
	if limit < PageSize {
		limit = PageSize
	}
	a.mu.Unlock()

	// 多取一条：取回来比 limit 多，说明还有下一页
	items, err := db.Query(a.Store.DB, q, filter, db.Page{Limit: int64(limit) + 1})
	if err != nil {
		logf("QUERY ERROR %v", err)
		a.SetStatus("❌ 读库失败：" + err.Error())
		return
	}
	groups, err := db.ListGroups(a.Store.DB)
	if err != nil {
		logf("GROUP LIST ERROR %v", err)
	}
	byID := make(map[int64]string, len(groups))
	for _, g := range groups {
		byID[g.ID] = g.Name
	}

	hasMore := len(items) > limit
	if hasMore {
		items = items[:limit]
	}
	total, err := db.CountQuery(a.Store.DB, q, filter)
	if err != nil {
		logf("COUNT ERROR %v", err)
		total = int64(len(items))
	}

	rows := make([]Row, 0, len(items))
	for _, item := range items {
		row := a.rowOf(item)
		if item.GroupID != nil {
			row.GroupName = byID[*item.GroupID]
		}
		rows = append(rows, row)
	}

	a.mu.Lock()
	a.rows = rows
	a.groups = groups
	a.limit = limit
	a.hasMore = hasMore
	a.total = total
	if a.sel >= len(rows) {
		a.sel = max(0, len(rows)-1)
	}
	a.mu.Unlock()
	a.invalidate()
}

// 分组筛选里代表「未分组」的哨兵值（分组名不可能含 \x00）。
const groupNone = "\x00"

// currentFilterLocked 把 UI 上的筛选状态翻成 db.Filter（调用方持锁）。
func (a *App) currentFilterLocked() db.Filter {
	var f db.Filter
	switch a.kindFilter {
	case 1:
		yes := true
		f.Favorite = &yes
	case 2:
		yes := true
		f.Pinned = &yes
	case 3:
		f.Kinds = []model.ItemKind{model.KindImage}
	case 4:
		f.Kinds = []model.ItemKind{model.KindFiles}
	}
	switch {
	case a.groupFilter == "":
	case a.groupFilter == groupNone:
		f.NoGroup = true
	default:
		for _, g := range a.groups {
			if g.Name == a.groupFilter {
				id := g.ID
				f.GroupID = &id
				break
			}
		}
	}
	return f
}

// SetKindFilter 切换顶部筛选并重查。
func (a *App) SetKindFilter(kind int) {
	a.mu.Lock()
	a.kindFilter = kind
	a.sel = 0
	a.limit = PageSize
	a.mu.Unlock()
	a.Refresh()
}

// SetGroupFilter 切换分组筛选（"" 全部、groupNone 未分组、其它为组名）。
func (a *App) SetGroupFilter(name string) {
	a.mu.Lock()
	a.groupFilter = name
	a.sel = 0
	a.limit = PageSize
	a.mu.Unlock()
	a.Refresh()
}

// FilterState 返回当前的筛选状态（给视图用）。
func (a *App) FilterState() (kind int, group string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.kindFilter, a.groupFilter
}

// Groups 返回分组缓存。
func (a *App) Groups() []db.Group {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]db.Group(nil), a.groups...)
}

// SetQuery 改搜索词并重查。
func (a *App) SetQuery(q string) {
	a.mu.Lock()
	a.query = q
	a.sel = 0
	a.limit = PageSize
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
		UseCount:  item.UseCount,
		Content:   item.Content,
		Note:      item.Note,
		GroupID:   item.GroupID,
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

// scrollSelectionIntoView 按行位置调整列表滚动，让选中行完整露出来。
//
// 行高不固定（图片行带缩略图更高），所以用渲染时累加出来的 top/h。
func (a *App) scrollSelectionIntoView(top, height float32) {
	view := a.listViewH
	if view <= 0 {
		return
	}
	y := a.ListScroll.Y
	switch {
	case top < y:
		a.ListScroll.Y = top
	case top+height > y+view:
		a.ListScroll.Y = top + height - view
	}
}

// later 把一个动作排到本帧构建结束后执行。
//
// 注意：不能用「每帧的局部 slice」，因为点击/菜单回调是在**下一帧**构建时执行的，
// 那时上一帧的局部 slice 早被丢掉了，动作会静默丢失（症状：点了列表不选中、列表乱跳）。
func (a *App) later(fn func()) {
	a.mu.Lock()
	a.pending = append(a.pending, fn)
	a.mu.Unlock()
}

// runPending 跑掉排好的动作（本帧构建结束时调用）。
func (a *App) runPending() {
	a.mu.Lock()
	fns := a.pending
	a.pending = nil
	a.mu.Unlock()
	for _, fn := range fns {
		fn()
	}
}

// previewCardRect 返回右栏卡片上一帧的区域（测试用）。
func (a *App) previewCardRect() ui.Rect {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.previewCard
}

// bindSearchInput 记下搜索框句柄与当前渲染上下文。
func (a *App) bindSearchInput(h *ui.Handle, c *ui.Context) {
	a.mu.Lock()
	a.searchInput = *h
	a.searchCtx = c
	a.mu.Unlock()
}

// FocusSearch 把焦点放到搜索框（⌘F 与「点过列表之后」都走它）。
func (a *App) FocusSearch() {
	a.mu.Lock()
	a.focusSearchNext = true
	a.mu.Unlock()
	a.refocusSearch()
	a.invalidate()
}

// refocusSearch 把键盘焦点还给搜索框（点过列表之后调用）。
func (a *App) refocusSearch() {
	if a.searchCtx == nil {
		return
	}
	a.searchInput.Focus(a.searchCtx)
}

// requestScrollToSelection 让下一次渲染把选中行滚进可视区（一次性）。
func (a *App) requestScrollToSelection() {
	a.mu.Lock()
	a.scrollToSel = true
	a.mu.Unlock()
}

// takeScrollToSelection 取走一次性请求（渲染时只需要滚一次）。
func (a *App) takeScrollToSelection() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	want := a.scrollToSel
	a.scrollToSel = false
	return want
}

// takeFocusSearchNext 取走一次性请求：面板刚唤出，把焦点放回搜索框。
func (a *App) takeFocusSearchNext() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	want := a.focusSearchNext
	a.focusSearchNext = false
	return want
}

// MoveTo 直接跳到第 index 行（Home/End 用）。
//
// 跳过去之后同样要把选中行滚进可视区，否则跳到末尾看不见。
func (a *App) MoveTo(index int) {
	a.mu.Lock()
	n := len(a.rows)
	switch {
	case n == 0:
		a.sel = 0
	case index < 0:
		a.sel = 0
	case index >= n:
		a.sel = n - 1
	default:
		a.sel = index
	}
	a.mu.Unlock()
	a.requestScrollToSelection()
	a.invalidate()
}

// PageStep 是一屏能放几行：按列表可视区高度与实测行高估，至少 1 行。
func (a *App) PageStep() int {
	a.mu.Lock()
	view := a.listViewH
	a.mu.Unlock()
	const rowH = 30 // 行高不固定（图片行更高），取个偏小的估计，翻页不会跳过头
	if view <= 0 {
		return 5
	}
	if n := int(view / rowH); n > 1 {
		return n
	}
	return 5
}

// MovePage 翻页（delta 为 ±1）。
func (a *App) MovePage(delta int) {
	step := a.PageStep()
	a.mu.Lock()
	sel, n := a.sel, len(a.rows)
	a.mu.Unlock()
	a.MoveTo(sel + delta*step)
	if delta > 0 && sel+step >= n && a.HasMore() {
		// 翻到底部先补一页，别让 PgDn 卡在已加载的尽头；排到帧末，别在回调里重查
		a.later(func() { a.LoadMore() })
	}
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
	a.requestScrollToSelection()
	a.invalidate()
}

// Select 直接选中第 index 行。
func (a *App) Select(index int) {
	a.mu.Lock()
	if index >= 0 && index < len(a.rows) {
		a.sel = index
	}
	a.mu.Unlock()
	a.requestScrollToSelection()
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
		a.requestScrollToSelection()
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

// LoadMore 再取一页（列表滚到底时调用）；已取完就什么都不做。
//
// 返回是否真的加载了，调用方据此决定要不要继续问。
func (a *App) LoadMore() bool {
	a.mu.Lock()
	if !a.hasMore {
		a.mu.Unlock()
		return false
	}
	a.limit += PageSize
	a.mu.Unlock()
	logf("PAGE load more limit=%d", a.limit)
	a.Refresh()
	return true
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

// PastePlain 以纯文本粘贴：HTML/RTF 剥成纯文本、文件给路径，再走一次粘贴。
//
// 富文本条目原样写回时，粘进纯文本目标会出标签/控制符，所以留一个降级入口。
func (a *App) PastePlain(row Row) bool {
	if a.plainProbe != nil {
		a.plainProbe(row)
		return true
	}
	item, ok, err := db.Get(a.Store.DB, row.ID)
	if err != nil || !ok {
		a.SetStatus("❌ 条目不存在")
		return false
	}
	hashes, err := clip.WriteBackPlain(item, a.Paths)
	if err != nil {
		logf("PASTEPLAIN failed id=%d: %v", row.ID, err)
		a.SetStatus("❌ " + err.Error())
		return false
	}
	a.Guard.Expect(hashes...)
	if err := db.Touch(a.Store.DB, item.ID); err != nil {
		logf("PASTEPLAIN touch failed: %v", err)
	}
	logf("PASTEPLAIN id=%d kind=%s", item.ID, item.Kind)
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

// NoteValue 返回备注编辑器的绑定值：选中条目变了就把草稿重置成库里那条的备注。
func (a *App) NoteValue(id int64, note string) *string {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.noteFor != id {
		a.noteFor = id
		a.noteDraft = note
	}
	return &a.noteDraft
}

// SaveNote 写备注（契约 6：不刷新 updated_at）。
func (a *App) SaveNote(id int64, text string) {
	// 超过上限的部分直接截掉（设置窗里有字数提示）
	if runes := []rune(text); len(runes) > noteMaxRunes {
		text = string(runes[:noteMaxRunes])
	}
	if err := db.UpdateMeta(a.Store.DB, id, db.MetaPatch{Note: &text}); err != nil {
		logf("NOTE save failed id=%d: %v", id, err)
		a.SetStatus("❌ 备注保存失败：" + err.Error())
		return
	}
	a.mu.Lock()
	for i := range a.rows {
		if a.rows[i].ID == id {
			a.rows[i].Note = text
			break
		}
	}
	a.mu.Unlock()
	a.invalidate()
}

// MoveToGroup 把条目放进分组（gid 为 nil 表示移出分组）。
func (a *App) MoveToGroup(id int64, gid *int64) bool {
	patch := db.MetaPatch{GroupID: gid}
	if gid == nil {
		patch = db.MetaPatch{ClearGroup: true}
	}
	if err := db.UpdateMeta(a.Store.DB, id, patch); err != nil {
		logf("GROUP move failed id=%d: %v", id, err)
		a.SetStatus("❌ 分组保存失败：" + err.Error())
		return false
	}
	a.Refresh()
	return true
}

// NewGroupDraft 是设置窗「新分组名字」输入框的绑定值。
func (a *App) NewGroupDraft() *string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return &a.newGroupDraft
}

// RenameDraft 是某个分组改名输入框的绑定值（换组时重置成库里的名字）。
func (a *App) RenameDraft(id int64, current string) *string {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.renameDrafts == nil {
		a.renameDrafts = map[int64]*string{}
	}
	if draft, ok := a.renameDrafts[id]; ok && *draft == "" {
		// 名字被清空过：重新拉回库里的值，避免输入框空着
		*draft = current
	}
	if _, ok := a.renameDrafts[id]; !ok {
		draft := current
		a.renameDrafts[id] = &draft
	}
	return a.renameDrafts[id]
}

// CreateGroup 新建分组并把 id 返回给调用方（设置窗刷新用）。
func (a *App) CreateGroup(name string) (int64, error) {
	id, err := db.EnsureGroup(a.Store.DB, name)
	if err != nil {
		a.SetStatus("❌ " + err.Error())
		return 0, err
	}
	a.Refresh()
	return id, nil
}

// RenameGroup 改分组名。
func (a *App) RenameGroup(id int64, name string) error {
	if err := db.RenameGroup(a.Store.DB, id, name); err != nil {
		a.SetStatus("❌ " + err.Error())
		return err
	}
	a.Refresh()
	return nil
}

// DeleteGroup 删分组（组内条目回落到未分组）。
func (a *App) DeleteGroup(id int64) error {
	if err := db.DeleteGroup(a.Store.DB, id); err != nil {
		a.SetStatus("❌ " + err.Error())
		return err
	}
	a.Refresh()
	return nil
}

// Flag 是可以单独切换的标记。
type Flag int

// 可切换的标记。
const (
	FlagPinned Flag = iota
	FlagFavorite
	FlagSensitive
)

// 批量标记（多选删除用）。

// IsMarked 报告某条是否被标记。
func (a *App) IsMarked(id int64) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.marked[id]
}

// Marked 返回已标记的 id（按列表顺序，方便状态文案）。
func (a *App) Marked() []int64 {
	a.mu.Lock()
	defer a.mu.Unlock()
	if len(a.marked) == 0 {
		return nil
	}
	out := make([]int64, 0, len(a.marked))
	for _, row := range a.rows {
		if a.marked[row.ID] {
			out = append(out, row.ID)
		}
	}
	return out
}

// ToggleMark 切换一条的标记状态。
func (a *App) ToggleMark(id int64) {
	a.mu.Lock()
	if a.marked == nil {
		a.marked = map[int64]bool{}
	}
	if a.marked[id] {
		delete(a.marked, id)
	} else {
		a.marked[id] = true
	}
	n := len(a.marked)
	a.mu.Unlock()
	if n > 0 {
		a.SetStatus(fmt.Sprintf("已选 %d 条 · %sD 删除 · Esc 取消", n, ModPrefix()))
	} else {
		a.SetStatus("")
	}
	a.invalidate()
}

// MarkAllLoaded 标记当前已加载的所有行；已经有标记时再按一次就取消全选。
func (a *App) MarkAllLoaded() {
	a.mu.Lock()
	if len(a.marked) > 0 {
		a.marked = nil
		a.mu.Unlock()
		a.SetStatus("")
		a.invalidate()
		return
	}
	a.marked = make(map[int64]bool, len(a.rows))
	for _, row := range a.rows {
		a.marked[row.ID] = true
	}
	n := len(a.marked)
	a.mu.Unlock()
	if n > 0 {
		a.SetStatus(fmt.Sprintf("已全选 %d 条 · %sD 删除 · Esc 取消", n, ModPrefix()))
	}
	a.invalidate()
}

// ClearMarks 清掉所有标记；返回是否真的清过（Esc 用：有标记就先取消标记，不收起面板）。
func (a *App) ClearMarks() bool {
	a.mu.Lock()
	had := len(a.marked) > 0
	a.marked = nil
	a.mu.Unlock()
	if had {
		a.SetStatus("")
		a.invalidate()
	}
	return had
}

// DeleteMarked 删掉所有被标记的条目；没有标记时删 fallback 那一行。
func (a *App) DeleteMarked(fallback Row, hasFallback bool) int {
	ids := a.Marked()
	multi := len(ids) > 1
	if len(ids) == 0 {
		if !hasFallback {
			return 0
		}
		ids = []int64{fallback.ID}
	}
	n, err := a.Store.RemoveMany(ids)
	if err != nil {
		logf("REMOVE MANY failed n=%d: %v", len(ids), err)
		a.SetStatus("❌ " + err.Error())
		return 0
	}
	a.mu.Lock()
	a.marked = nil
	a.mu.Unlock()
	a.Refresh()
	switch {
	case n == 0:
		a.SetStatus("没有删掉任何条目")
	case multi:
		a.SetStatus(fmt.Sprintf("已删除 %d 条", n))
	default:
		a.SetStatus("已删除")
	}
	return n
}

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

// DragData 是拖拽导出用的载荷：图片与文件给文件 URL，文本给纯文本。
//
// 图片给的是 origin 里的真文件（拖出去默认是复制，原文件不会被动）。
func (a *App) DragData(id int64) transfer.Data {
	data := a.dragData(id)
	if a.dragProbe != nil {
		a.dragProbe(data)
	}
	return data
}

// dragData 是 DragData 的实际实现（probe 不影响它）。
func (a *App) dragData(id int64) transfer.Data {
	item, ok, err := db.Get(a.Store.DB, id)
	if err != nil || !ok {
		return transfer.Data{}
	}
	switch item.Kind {
	case model.KindFiles:
		var paths []string
		for _, line := range strings.Split(item.Content, "\n") {
			if line != "" && fileExists(line) {
				paths = append(paths, line)
			}
		}
		if len(paths) == 0 {
			a.SetStatus("❌ 文件条目里的路径都不在了")
			return transfer.Data{}
		}
		data, err := transfer.FileData(paths...)
		if err != nil {
			logf("DRAG files failed: %v", err)
			return transfer.Data{}
		}
		logf("DRAG id=%d files=%d", item.ID, len(paths))
		return data
	case model.KindImage:
		hash := strings.TrimSuffix(item.Content, ".png")
		origin := a.Paths.ImageOrigin(hash)
		if !fileExists(origin) {
			a.SetStatus("❌ 原图文件缺失")
			return transfer.Data{}
		}
		data, err := transfer.FileData(origin)
		if err != nil {
			logf("DRAG image failed: %v", err)
			return transfer.Data{}
		}
		logf("DRAG id=%d image=%s", item.ID, hash)
		return data
	default:
		// 文本：既给纯文本（拖进输入框就是内容），也落一个 .txt 文件（拖进 Finder 就是文件）
		text := transfer.New(transfer.NewItem(transfer.Bytes(transfer.Text, []byte(item.Content))))
		name := fmt.Sprintf("clipnest-%d.txt", item.ID)
		dest := filepathJoin(a.Paths.Root(), "export", name)
		if err := writeFile(dest, []byte(item.Content)); err != nil {
			logf("DRAG text file failed: %v", err)
			return text
		}
		files, err := transfer.FileData(dest)
		if err != nil {
			return text
		}
		logf("DRAG id=%d text=%d runes file=%s", item.ID, len([]rune(item.Content)), dest)
		return transfer.New(append(text.Items(), files.Items()...)...)
	}
}

// ExportTo 让用户先选位置再导出，并把结果写到底栏（右键菜单/⌘O/右栏按钮共用）。
func (a *App) ExportTo(row Row) {
	// 保存对话框会把前台抢走，关掉之后把键盘焦点还给搜索框（方向键继续选）
	defer a.refocusSearch()
	msg, err := a.ExportWithDialog(row)
	switch {
	case err != nil:
		a.SetStatus("❌ " + err.Error())
	case msg != "":
		a.SetStatus(msg)
	}
}

// ExportWithDialog 让用户先选位置再导出，返回一句能放进底栏的说明（取消时为空）。
//
// 文本/图片选「保存到哪个文件」，文件条目选「复制到哪个目录」。
func (a *App) ExportWithDialog(row Row) (string, error) {
	item, ok, err := db.Get(a.Store.DB, row.ID)
	if err != nil || !ok {
		return "", fmt.Errorf("条目不存在")
	}
	dir := a.exportDir()

	switch item.Kind {
	case model.KindFiles:
		targets, err := mygo.Dialog.Open(mygo.OpenDialogOptions{
			Title:       "把文件复制到…",
			DefaultPath: dir,
			ButtonLabel: "复制到这里",
			Directory:   true,
		})
		if err != nil {
			return "", err
		}
		if len(targets) == 0 {
			return "", nil // 取消
		}
		dest := targets[0]
		n := 0
		for _, line := range strings.Split(item.Content, "\n") {
			if line == "" || !fileExists(line) {
				continue
			}
			if err := copyFile(line, filepathJoin(dest, baseName(line))); err != nil {
				return "", err
			}
			n++
		}
		if n == 0 {
			return "", fmt.Errorf("文件条目里的路径都不在了")
		}
		logf("EXPORT dialog files=%d to=%s", n, dest)
		return fmt.Sprintf("已复制 %d 个文件到 %s", n, baseName(dest)), nil

	case model.KindImage:
		hash := strings.TrimSuffix(item.Content, ".png")
		origin := a.Paths.ImageOrigin(hash)
		path, err := mygo.Dialog.Save(mygo.SaveDialogOptions{
			Title:       "导出图片",
			DefaultPath: filepathJoin(dir, hash+".png"),
			ButtonLabel: "导出",
		})
		if err != nil {
			return "", err
		}
		if path == "" {
			return "", nil // 取消
		}
		if err := copyFile(origin, path); err != nil {
			return "", err
		}
		logf("EXPORT dialog image=%s", path)
		return "已导出 " + baseName(path), nil

	default:
		path, err := mygo.Dialog.Save(mygo.SaveDialogOptions{
			Title:       "导出文本",
			DefaultPath: filepathJoin(dir, fmt.Sprintf("clipnest-%d.txt", item.ID)),
			ButtonLabel: "导出",
		})
		if err != nil {
			return "", err
		}
		if path == "" {
			return "", nil // 取消
		}
		if err := writeFile(path, []byte(item.Content)); err != nil {
			return "", err
		}
		logf("EXPORT dialog text=%s", path)
		return "已导出 " + baseName(path), nil
	}
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

// pickBackupFile 让用户挑一个 .cnbak；取消返回空串（默认落在数据根 backups/）。
func (a *App) pickBackupFile() string {
	paths, err := mygo.Dialog.Open(mygo.OpenDialogOptions{
		Title:       "选择要导入的备份",
		DefaultPath: a.Paths.BackupsDir(),
		ButtonLabel: "导入",
		Filters: []mygo.FileFilter{
			{Name: "clipnest 备份", Extensions: []string{"cnbak"}},
		},
	})
	if err != nil {
		logf("BACKUP pick failed: %v", err)
		return ""
	}
	if len(paths) == 0 {
		return ""
	}
	return paths[0]
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

// ResetPanelState 把面板清回初始状态。
//
// 面板只是 Hide 不是销毁，所以搜索词、选中行、筛选、滚动位置、备注草稿这些
// 都会跨「收起→再唤出」留着；不清的话再次呼出看到的就是上次退出时的样子。
func (a *App) ResetPanelState() {
	a.mu.Lock()
	a.query = ""
	a.sel = 0
	a.limit = PageSize
	a.hasMore = false
	a.visible = true
	a.status = ""
	a.syncMsg = ""
	a.kindFilter = 0
	a.groupFilter = ""
	// 备注编辑器回到「没有正在编辑的条目」
	a.noteFor = 0
	a.noteDraft = ""
	// 上一轮排在帧末尾的动作（点击/菜单回调）没跑完就不会跑了，
	// 留着会在下一次渲染时冒出来（症状：刚唤出就选中/粘贴了一条）
	a.pending = nil
	a.scrollToSel = true
	a.focusSearchNext = true
	a.ListScroll = ui.ScrollState{}
	win := a.imageWin
	a.mu.Unlock()
	// 收起时没关的放大预览窗，唤出前先关掉
	if win != nil && win.IsVisible() {
		win.Hide()
	}
}

// Show 打开面板：把界面恢复成初始状态、记下前台应用、摆位置、显示。
func (a *App) Show() {
	w := a.panelWindow()
	if w == nil {
		return
	}
	logf("SHOW ax=%v", paste.Trusted())
	// 面板不激活 app，但仍要记住「刚才在哪个应用」以便粘贴后切回去。
	paste.RememberForeground()

	a.ResetPanelState()
	a.Refresh()

	// 位置：光标所在屏幕，居中或贴底（运行时就能算，不像 nsh 只能创建时定）
	d := mygo.Screen.DisplayNearestPoint(mygo.Screen.CursorScreenPoint())
	width, height := w.Size()
	y := d.WorkArea.Y + (d.WorkArea.Height-height)/2
	if a.Settings.PanelPosition == settings.PositionBottom {
		y = d.WorkArea.Y + d.WorkArea.Height - height - 24
	}
	w.SetPosition(d.WorkArea.X+(d.WorkArea.Width-width)/2, y)
	// 唤出时置顶；1.2 秒后降回普通层级，这样它不会一直压着别的窗口，
	// 但下次按快捷键唤出又会重新置顶。
	w.SetAlwaysOnTop(true)
	w.Show()
	time.AfterFunc(1200*time.Millisecond, func() {
		if w.IsVisible() {
			w.SetAlwaysOnTop(false)
		}
	})

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

// StartSync 在后台跑一轮同步（UI 不卡）。
func (a *App) StartSync() {
	clipsync.ResetEndpointCache() // 手动同步：重新探一次局域网/Tailscale
	go a.runSync(true)
}

// CheckSyncStuck 看当前这轮是不是卡太久了；卡住就放开标志，让下一轮重试。
//
// 定时循环每轮前调它。这里宁可「可能重入」也不能永久卡死：数据库是单连接串行的，
// 两轮同时跑最多重复推送一次，合并侧幂等。
func (a *App) CheckSyncStuck() bool {
	started := a.syncStartedAt.Load()
	if started == 0 {
		return false
	}
	elapsed := time.Since(time.Unix(0, started))
	if elapsed < SyncStuckAfter {
		return false
	}
	a.syncBusy.Store(false)
	a.syncStartedAt.Store(0)
	a.syncPending.Store(false)
	logf("SYNC stuck %s，已重置（常见原因：钥匙串授权弹窗没人点）", elapsed.Round(time.Second))
	a.setSyncStatus("⚠️ 同步卡住了，已重置：可能有个钥匙串授权弹窗在等确认")
	return true
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

func (a *App) runSync(loud bool) {
	if !a.syncBusy.CompareAndSwap(false, true) {
		a.syncPending.Store(true)
		return
	}
	a.syncStartedAt.Store(time.Now().UnixNano())
	defer func() {
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
			a.CheckSyncStuck() // 上一轮卡死（比如在等钥匙串授权）就放开，别永久静默
			if clipsync.LoadConfig(a.Paths).Enabled {
				// 必须另起 goroutine：这一轮如果卡在钥匙串弹窗上，
				// 直接在这里同步调用会把 ticker 循环本身堵死，看门狗跟着一起停摆。
				go a.runSync(false)
			}
		}
	}()
}

// SortRowsStable 让行顺序稳定（调试用）。
func SortRowsStable(rows []Row) {
	sort.SliceStable(rows, func(i, j int) bool { return rows[i].ID < rows[j].ID })
}

// ShortcutDraft 是设置窗里快捷键输入框的草稿（跨帧保留）。
func (a *App) ShortcutDraft() *string {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.shortcutDraft == "" {
		a.shortcutDraft = a.Settings.Shortcut
	}
	return &a.shortcutDraft
}

// ShortcutMessage 是上次改快捷键的结果提示。
func (a *App) ShortcutMessage() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.shortcutMsg
}

// ApplyShortcutDraft 应用输入框里的快捷键，并把结果记成提示。
func (a *App) ApplyShortcutDraft(acc string) {
	err := a.ApplyShortcut(acc)
	a.mu.Lock()
	defer a.mu.Unlock()
	if err != nil {
		a.shortcutMsg = "❌ " + err.Error()
		return
	}
	a.shortcutDraft = a.Settings.Shortcut
	a.shortcutMsg = "✅ 已改为 " + HotkeyText(a.Settings.Shortcut) + "（立即生效）"
}

// PruneDaysDraft 是「清理多少天前」输入框的草稿。
func (a *App) PruneDaysDraft() *float64 {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.pruneDays <= 0 {
		a.pruneDays = 30
	}
	return &a.pruneDays
}

// PruneOlderThanDays 删掉 N 天前的条目（收藏与置顶受保护）。
func (a *App) PruneOlderThanDays(days int) {
	if days <= 0 {
		a.SetStatus("⚠️ 天数要大于 0")
		return
	}
	cutoff := time.Now().AddDate(0, 0, -days).UTC().Format(time.RFC3339)
	n, err := a.Store.PruneOlderThan(cutoff)
	if err != nil {
		logf("PRUNE AGE ERROR %v", err)
		a.SetStatus("❌ 清理失败：" + err.Error())
		return
	}
	logf("PRUNE AGE days=%d deleted=%d", days, n)
	a.SetStatus(fmt.Sprintf("✅ 清理了 %d 条（%d 天前）", n, days))
	a.Refresh()
}

// AskClearAll / ClearConfirming / CancelClearAll 是「清空全部」的两步确认。
func (a *App) AskClearAll() {
	a.mu.Lock()
	a.clearConfirm = true
	a.mu.Unlock()
	a.invalidate()
}

func (a *App) ClearConfirming() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.clearConfirm
}

func (a *App) CancelClearAll() {
	a.mu.Lock()
	a.clearConfirm = false
	a.mu.Unlock()
	a.invalidate()
}

// ClearAllHistory 真的清空（含收藏与置顶）。
func (a *App) ClearAllHistory() {
	n, err := a.Store.ClearAll()
	a.mu.Lock()
	a.clearConfirm = false
	a.mu.Unlock()
	if err != nil {
		logf("CLEAR ALL ERROR %v", err)
		a.SetStatus("❌ 清空失败：" + err.Error())
		return
	}
	logf("CLEAR ALL deleted=%d", n)
	a.SetStatus(fmt.Sprintf("✅ 已清空 %d 条历史", n))
	a.Refresh()
}

// Hotkey 是当前生效的唤出快捷键展示文案（⌘⇧V 这类）。
func (a *App) Hotkey() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return HotkeyText(a.Settings.Shortcut)
}

// ShortcutAccelerator 是当前生效的 accelerator 原文。
func (a *App) ShortcutAccelerator() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.Settings.Shortcut
}

// ApplyShortcut 换唤出快捷键：注销旧的、注册新的，失败就回滚到旧的。
func (a *App) ApplyShortcut(acc string) error {
	acc = strings.TrimSpace(acc)
	if acc == "" {
		return errors.New("快捷键不能为空")
	}
	a.mu.Lock()
	old := a.Settings.Shortcut
	a.mu.Unlock()
	if acc == old {
		return nil
	}
	mygo.GlobalShortcut.Unregister(old)
	if err := mygo.GlobalShortcut.Register(acc, a.TogglePanel); err != nil {
		// 回滚，别让用户丢了原来的快捷键
		_ = mygo.GlobalShortcut.Register(old, a.TogglePanel)
		return fmt.Errorf("注册 %s 失败（可能被别的应用占用）：%w", acc, err)
	}
	next := a.Settings
	next.Shortcut = acc
	if err := settings.Save(next, a.Paths); err != nil {
		return fmt.Errorf("保存设置失败：%w", err)
	}
	a.mu.Lock()
	a.Settings = next
	a.mu.Unlock()
	a.refreshTrayMenu()
	logf("SHORTCUT now %s (%s)", acc, HotkeyText(acc))
	return nil
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
