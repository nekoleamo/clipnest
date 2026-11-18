// 条目动作与系统集成：粘贴/复制/删除、导出、快捷键、辅助功能提示、清理。
package app

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/transfer"

	"clipnest/internal/backup"
	"clipnest/internal/clip"
	"clipnest/internal/db"
	"clipnest/internal/model"
	"clipnest/internal/paste"
	"clipnest/internal/settings"
	clipsync "clipnest/internal/sync"
)

// Paste 粘贴一行：写回剪贴板 → 计一次使用 → 收起面板 → 发 ⌘V。
func (a *App) Paste(row Row) bool {
	if a.probe.paste != nil {
		a.probe.paste(row)
		return true
	}
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
	if a.probe.plain != nil {
		a.probe.plain(row)
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

// DragData 是拖拽导出用的载荷：图片与文件给文件 URL，文本给纯文本。
//
// 图片给的是 origin 里的真文件（拖出去默认是复制，原文件不会被动）。
func (a *App) DragData(id int64) transfer.Data {
	data := a.dragData(id)
	if a.probe.drag != nil {
		a.probe.drag(data)
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

// ShortcutMessage 是上次改快捷键的结果提示。
func (a *App) ShortcutMessage() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.shortcutMsg
}

// ApplyShortcutDraft 应用输入框里的快捷键，并把结果记成提示。
func (a *App) ApplyShortcutDraft(acc string) {
	err := a.ApplyShortcut(acc)
	f := a.PrefsForm() // 先拿草稿：它自己会加 a.mu
	a.mu.Lock()
	defer a.mu.Unlock()
	if err != nil {
		a.shortcutMsg = "❌ " + err.Error()
		return
	}
	f.shortcut = a.Settings.Shortcut
	a.shortcutMsg = "✅ 已改为 " + HotkeyText(a.Settings.Shortcut) + "（立即生效）"
}

// PruneOlderThanDays 删掉 N 天前的条目（收藏与置顶受保护），返回结果文案。
func (a *App) PruneOlderThanDays(days int) string {
	if days <= 0 {
		return a.reportStatus("⚠️ 天数要大于 0")
	}
	cutoff := time.Now().AddDate(0, 0, -days).UTC().Format(time.RFC3339)
	n, err := a.Store.PruneOlderThan(cutoff)
	if err != nil {
		logf("PRUNE AGE ERROR %v", err)
		return a.reportStatus("❌ 清理失败：" + err.Error())
	}
	logf("PRUNE AGE days=%d deleted=%d", days, n)
	return a.reportStatus(fmt.Sprintf("✅ 清理了 %d 条（%d 天前）", n, days))
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

// ClearAllHistory 真的清空（含收藏与置顶），返回结果文案。
func (a *App) ClearAllHistory() string {
	n, err := a.Store.ClearAll()
	a.mu.Lock()
	a.clearConfirm = false
	a.mu.Unlock()
	if err != nil {
		logf("CLEAR ALL ERROR %v", err)
		return a.reportStatus("❌ 清空失败：" + err.Error())
	}
	logf("CLEAR ALL deleted=%d", n)
	return a.reportStatus(fmt.Sprintf("✅ 已清空 %d 条历史", n))
}

// ClearAllHistoryWithBackup 先导一份加密备份到 <数据目录>/backups/，再清空全部历史。
//
// 清空是不可撤销的（墓碑还会随同步推给别的设备，别的机器也跟着没），所以没口令
// 就不备份、也不清空：宁可让用户先填个口令，也不要一次手滑把整库抹掉。
// 设置窗里没填的话，用系统凭据里存着的那个同步口令（只在点确认这一下才去读）。
func (a *App) ClearAllHistoryWithBackup(passphrase string) string {
	if passphrase == "" {
		passphrase = a.savedPassphrase()
	}
	if passphrase == "" {
		return "⚠️ 没有加密口令，没法先备份，所以没有清空（在「同步」里填一次加密口令再来）"
	}
	path, err := backup.ExportTo(a.Store.DB, a.Paths, passphrase)
	if err != nil {
		logf("CLEAR ALL backup failed: %v", err)
		return "❌ 备份失败，没有清空：" + err.Error()
	}
	return "已备份 " + baseName(path) + " · " + a.ClearAllHistory()
}

// 系统凭据的读写口子，可注入：测试里必须替掉——macOS 那份直接调 Security 框架、
// 不看有没有事件循环，测试会**真的往登录钥匙串里写条目**（踩过：留下一条
// clipnest-sync/<主机名>-local）。
var (
	secretGet = func(service, key string) (string, error) { return clipsync.SecretGet(service, key) }
	secretSet = func(service, key, value string) error { return clipsync.SecretSet(service, key, value) }
)

// savedPassphrase 是系统凭据里存着的**加密口令**（备份也用这一份），取不到给空串。
// 不拿 WebDAV 账号口令兜底：那是 NAS 的密码，拿它加密出来的备份以后没人记得住。
func (a *App) savedPassphrase() string {
	cfg := clipsync.LoadConfig(a.Paths)
	if pw, err := secretGet(clipsync.SecretService, cfg.DeviceName); err == nil {
		return pw
	}
	return ""
}

// reportStatus 把文案写进底栏并原样返回（设置窗自己那行提示要用）。
// 先写状态再刷新列表：刷新失败时会自己盖一条错，不要把它盖回去。
func (a *App) reportStatus(text string) string {
	a.SetStatus(text)
	a.Refresh()
	return text
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
	// 先摘再挂：录制时旧键可能已经被摘掉，重新注册一遍也没关系
	unregisterShortcut(old)
	if err := registerShortcut(acc, a.TogglePanel); err != nil {
		// 回滚，别让用户丢了原来的快捷键
		_ = registerShortcut(old, a.TogglePanel)
		return fmt.Errorf("注册 %s 失败（可能被别的应用占用）：%w", acc, err)
	}
	if acc == old {
		return nil // 还是同一个键：已经重新挂好，不用落盘
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

// 全局快捷键的注册口子。
//
// 测试里必须替掉：mygo 的 Register 会往主线程投递消息并**一直等**，
// headless 测试没有主线程在跑事件循环，直接卡死。
var (
	registerShortcut   = func(acc string, fn func()) error { return mygo.GlobalShortcut.Register(acc, fn) }
	unregisterShortcut = func(acc string) { mygo.GlobalShortcut.Unregister(acc) }
)

// StartShortcutRecord 进入录制态。
//
// 必须先把当前快捷键摘掉：Carbon 的全局热键不看窗口焦点，用户按到旧组合时
// 面板会被唤出/收起，录制框根本收不到那个键。
func (a *App) StartShortcutRecord() {
	f := a.PrefsForm()
	f.recording = true
	f.recordHint = ""
	a.mu.Lock()
	cur := a.Settings.Shortcut
	a.mu.Unlock()
	unregisterShortcut(cur)
	logf("SHORTCUT record start (paused %s)", cur)
	a.invalidatePrefs()
}

// StopShortcutRecord 结束录制：不管是因为录到了新组合还是 Esc 取消，
// 都把「当前该生效的那个」重新挂回去（录制开始时它被摘掉了）。
func (a *App) StopShortcutRecord() {
	f := a.PrefsForm()
	if !f.recording {
		return
	}
	f.recording = false
	a.restoreShortcut()
	a.invalidatePrefs()
}

// restoreShortcut 把设置里当前那个快捷键重新挂上。
func (a *App) restoreShortcut() {
	a.mu.Lock()
	cur := a.Settings.Shortcut
	a.mu.Unlock()
	unregisterShortcut(cur)
	if err := registerShortcut(cur, a.TogglePanel); err != nil {
		logf("SHORTCUT re-register %s failed: %v", cur, err)
	}
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
