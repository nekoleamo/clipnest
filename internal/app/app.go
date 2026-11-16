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
	// dragProbe 只在测试里用：拖拽开始时把载荷交出去断言
	dragProbe func(transfer.Data)
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
	filter := a.currentFilterLocked()
	a.mu.Unlock()

	items, err := db.Query(a.Store.DB, q, filter, db.Page{Limit: PageSize})
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
	a.mu.Unlock()
	a.Refresh()
}

// SetGroupFilter 切换分组筛选（"" 全部、groupNone 未分组、其它为组名）。
func (a *App) SetGroupFilter(name string) {
	a.mu.Lock()
	a.groupFilter = name
	a.sel = 0
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
