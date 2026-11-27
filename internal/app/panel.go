// 面板状态与列表：刷新、筛选、选中、翻页、多选、备注与分组。
package app

import (
	"fmt"
	"strings"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"

	"clipnest/internal/db"
	"clipnest/internal/model"
	"clipnest/internal/paste"
	"clipnest/internal/settings"
)

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
	// 总数：一整页装得下时 len 就是准数，不必再查；装不下才去 count。
	//
	// 试过用 COUNT(*) OVER () 把两条合成一条 SQL：COUNT 在 LIMIT 之前算，
	// SQLite 得把整个过滤结果物化+排序，实测 314µs 的页面查询变成 4ms（12 倍），
	// 而单独的 count 只要 3.5µs。所以这里宁可两条，也不合并。
	total := int64(len(items))
	if hasMore {
		n, err := db.CountQuery(a.Store.DB, q, filter)
		if err != nil {
			logf("COUNT ERROR %v", err)
		} else {
			total = n
		}
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
	case 5:
		yes := true
		f.Sensitive = &yes
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

// ClearQuery 清空搜索词并重查（搜索框那个 ✕）。
func (a *App) ClearQuery() {
	a.SetQuery("")
	a.FocusSearch()
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
		CreatedAt: model.DateTime(item.CreatedAt),
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

// MarkedSet 返回标记集合的快照（渲染时用：一帧只取一次，别每行加一次锁）。
func (a *App) MarkedSet() map[int64]bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	if len(a.marked) == 0 {
		return nil
	}
	out := make(map[int64]bool, len(a.marked))
	for id := range a.marked {
		out[id] = true
	}
	return out
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
	// 图片文件先留着：撤销窗口内要能恢复原图，窗口过去才真回收（undoAfter）
	snaps, files, err := a.Store.RemoveKeepingFiles(ids)
	if err != nil {
		logf("REMOVE MANY failed n=%d: %v", len(ids), err)
		a.SetStatus("❌ " + err.Error())
		return 0
	}
	a.setUndo(snaps, files)
	a.mu.Lock()
	a.marked = nil
	a.mu.Unlock()
	a.Refresh()
	switch {
	case len(snaps) == 0:
		a.SetStatus("没有删掉任何条目")
	case multi:
		a.SetStatus(fmt.Sprintf("已删除 %d 条 · 10 秒内可撤销", len(snaps)))
	default:
		a.SetStatus("已删除 · 10 秒内可撤销")
	}
	return len(snaps)
}

// UndoAfter 是撤销删除的窗口（变量而非常量：测试要把它调小）。
var UndoAfter = 10 * time.Second

// undoState 是最近一次删除的快照（只记最近一批）。
type undoState struct {
	snaps []db.RowData
	files []string
	at    time.Time
	gen   int
}

// setUndo 记下这次删除并安排「窗口过了回收图片文件」。
func (a *App) setUndo(snaps []db.RowData, files []string) {
	a.mu.Lock()
	prevFiles := a.undo.files // 上一批的图片文件：只记最近一批，它的窗口到此结束
	a.undoGen++
	gen := a.undoGen
	a.undo = undoState{snaps: snaps, files: files, at: time.Now(), gen: gen}
	a.mu.Unlock()

	// 上一批已经不可撤销，它的图片文件立刻回收
	if len(prevFiles) > 0 {
		a.Store.DeleteImageFiles(prevFiles)
	}

	// 窗口结束时重画一次：撤销按钮/状态文案要跟着消失
	time.AfterFunc(UndoAfter, a.invalidate)
	if len(files) == 0 {
		return
	}
	// 每个批次只清理「自己那一批」的文件：否则连删两次时，先到的定时器会把
	// 后一批还在窗口内的图片删掉（那一批就撤销不回来了）
	own := files
	time.AfterFunc(UndoAfter, func() {
		a.mu.Lock()
		mine := a.undo.gen == gen
		if mine {
			a.undo = undoState{}
		}
		a.mu.Unlock()
		if mine {
			a.Store.DeleteImageFiles(own) // 窗口过了没人撤销：真删
		}
	})
}

// CanUndo 报告现在有没有可撤销的删除。
func (a *App) CanUndo() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return len(a.undo.snaps) > 0 && time.Since(a.undo.at) < UndoAfter
}

// UndoDelete 把最近一次删除恢复回来（⌘Z / 底栏「撤销」按钮）。
func (a *App) UndoDelete() int {
	a.mu.Lock()
	state := a.undo
	a.undo = undoState{}
	a.mu.Unlock()
	if len(state.snaps) == 0 {
		a.SetStatus("没有可撤销的删除")
		return 0
	}
	if time.Since(state.at) >= UndoAfter {
		a.Store.DeleteImageFiles(state.files)
		a.SetStatus("撤销窗口已过（10 秒）")
		return 0
	}
	restored := 0
	for _, snap := range state.snaps {
		if err := db.RestoreRow(a.Store.DB, snap); err != nil {
			logf("UNDO restore failed id=%d: %v", snap.ID, err)
			a.SetStatus("❌ 恢复失败：" + err.Error())
			continue
		}
		restored++
	}
	a.Refresh()
	if restored > 0 {
		a.SetStatus(fmt.Sprintf("已撤销，恢复 %d 条", restored))
	}
	return restored
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

// TogglePanel 显示/收起面板（快捷键与托盘共用）。
func (a *App) TogglePanel() {
	w := a.panelWindow()
	if w == nil {
		return
	}
	act := toggleAction(w.IsVisible(), a.panelHidden.Load())
	logf("TOGGLE visible=%v hidden=%v focused=%v → %d", w.IsVisible(), a.panelHidden.Load(), w.IsFocused(), act)
	switch act {
	case actShow:
		a.Show()
	case actRaise:
		a.Raise()
	default:
		a.Dismiss()
	}
}

// panelAction 是按快捷键要做的事，从 TogglePanel 里拎出来单测。
type panelAction int

const (
	actDismiss panelAction = iota // 面板就在前面：收起
	actRaise                      // 面板还显示着、但被别的窗口盖住了：拿到最前
	actShow                       // 面板本来就是隐藏的：完整唤出
)

// toggleAction 只按「可不可见」「是不是被盖住了」判断。
//
// 不能看 IsFocused：isKeyWindow 是 app 内的状态，面板被别的 app 盖住之后它仍是 true
// （实测：这正是「要按两次才到台前」的原因——第一次按被当成「开着，收起」）。
func toggleAction(visible, hidden bool) panelAction {
	switch {
	case !visible:
		return actShow
	case hidden:
		return actRaise
	default:
		return actDismiss
	}
}

// Raise 把还在屏上、但被别的窗口盖住的面板拿回最前：不重置状态、不重算位置，
// 用户之前打的搜索词和选中的行都留着。
func (a *App) Raise() {
	w := a.panelWindow()
	if w == nil {
		return
	}
	logf("RAISE")
	paste.RememberForeground()
	a.panelHidden.Store(false)
	w.SetAlwaysOnTop(true)
	w.Show()
	w.Focus()
	a.dropTopAfter(w)
	if !paste.ClaimForeground() {
		logf("PANEL claim-foreground refused")
	}
}

// dropTopAfter 排一次「1.2 秒后降回普通层级」：面板不该一直压着别人的窗口，
// 但每次唤出/提到最前都要重新压一会儿。
//
// topSeq 只认最后一次：连续唤出两次时，先前排的定时器不会把最新那次置顶提前撤掉。
func (a *App) dropTopAfter(w *mygo.Window) {
	a.mu.Lock()
	a.topSeq++
	seq := a.topSeq
	a.mu.Unlock()
	time.AfterFunc(1200*time.Millisecond, func() {
		a.mu.Lock()
		cur := a.topSeq
		a.mu.Unlock()
		if cur == seq && w.IsVisible() {
			w.SetAlwaysOnTop(false)
		}
	})
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
	a.panelHidden.Store(false)
	w.SetAlwaysOnTop(true)
	w.Show()
	a.dropTopAfter(w)

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
