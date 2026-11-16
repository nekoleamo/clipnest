// 右键菜单：行上点右键（或按菜单键 / Shift+F10）弹出。
package app

import (
	"github.com/egoist/mygo/ui"
)

// rowMenuBuilds 统计菜单构建次数（测试用）。
var rowMenuBuilds int

// rowMenu 是列表行的右键菜单，条目与面板快捷键一一对应。
func (a *App) rowMenu(m *ui.Menu, row Row) {
	rowMenuBuilds++
	if m.Item("粘贴").Shortcut(0, ui.KeyEnter).Chosen() {
		a.Paste(row)
	}
	if m.Item("复制").Shortcut(ui.Cmd, ui.KeyY).Chosen() {
		a.Copy(row)
	}
	if m.Item("导出为文件…").Shortcut(ui.Cmd, ui.KeyO).Chosen() {
		if err := a.ExportWithDialog(row); err != nil {
			a.SetStatus("❌ " + err.Error())
		} else {
			a.SetStatus("已导出")
		}
	}

	m.Separator()
	pinLabel := "置顶"
	if row.Pinned {
		pinLabel = "取消置顶"
	}
	if m.Item(pinLabel).Shortcut(ui.Cmd, ui.KeyP).Checked(row.Pinned).Chosen() {
		a.ToggleFlag(row, FlagPinned)
	}
	favLabel := "收藏"
	if row.Favorite {
		favLabel = "取消收藏"
	}
	if m.Item(favLabel).Shortcut(ui.Cmd, ui.KeyS).Checked(row.Favorite).Chosen() {
		a.ToggleFlag(row, FlagFavorite)
	}
	sensLabel := "标记为敏感"
	if row.Sensitive {
		sensLabel = "取消敏感标记"
	}
	if m.Item(sensLabel).Shortcut(ui.Cmd, ui.KeyE).Checked(row.Sensitive).Chosen() {
		a.ToggleFlag(row, FlagSensitive)
	}

	m.Separator()
	m.Submenu("移到分组", func(sub *ui.Menu) {
		if sub.Item("未分组").Checked(row.GroupID == nil).Chosen() {
			a.MoveToGroup(row.ID, nil)
		}
		for _, g := range a.Groups() {
			id := g.ID
			checked := row.GroupID != nil && *row.GroupID == id
			if sub.Item(g.Name).Checked(checked).Chosen() {
				a.MoveToGroup(row.ID, &id)
			}
		}
	})

	m.Separator()
	if m.Item("删除").Shortcut(ui.Cmd, ui.KeyD).Danger().Chosen() {
		a.Remove(row)
	}
}
