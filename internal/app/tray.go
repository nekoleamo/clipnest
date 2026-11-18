package app

import (
	"github.com/egoist/mygo"

	"clipnest/internal/paste"
)

// AttachTray 挂菜单栏/托盘图标：显示面板 / 偏好设置 / 立即同步 / 退出。
func (a *App) AttachTray(icon []byte) (*mygo.Tray, error) {
	menu := a.buildMenu()
	tray, err := mygo.NewTray(mygo.TrayOptions{
		ID:             "clipnest",
		Icon:           icon,
		IconIsTemplate: true,
		// Title 别填：macOS 会把它当**文字**画在图标旁边（实测菜单栏里就多出
		// 一个像图标的 ❐），看起来就是「重复了一个图标」。只要图标就够。
		ToolTip: "clipnest 剪贴板历史",
		Menu:    menu,
	})
	if err != nil {
		return nil, err
	}
	tray.OnClick(func() { a.TogglePanel() })
	a.tray = tray
	return tray, nil
}

// buildMenu 是托盘菜单（换快捷键后要重建它，标签里带快捷键文案）。
//
// 「辅助功能授权」这两个菜单项只在 macOS 出现：Windows 不需要授权，
// 留着只会让人以为哪里没配好。
func (a *App) buildMenu() *mygo.Menu {
	items := []*mygo.MenuItem{
		{Label: "显示面板  " + a.Hotkey(), Click: func(*mygo.MenuItem, *mygo.Window) { a.TogglePanel() }},
		{Label: "暂停记录", Type: mygo.MenuItemCheckbox, Checked: a.Paused(),
			Click: func(*mygo.MenuItem, *mygo.Window) { a.TogglePause() }},
		{Type: mygo.MenuItemSeparator},
	}
	if paste.NeedsTrust() {
		items = append(items,
			&mygo.MenuItem{Label: "辅助功能授权…", Click: func(*mygo.MenuItem, *mygo.Window) { a.RequestAccessibility() }},
			&mygo.MenuItem{Label: "显示授权提示", Click: func(*mygo.MenuItem, *mygo.Window) { a.ShowAXHint() }},
		)
	}
	items = append(items,
		&mygo.MenuItem{Label: "偏好设置…", Click: func(*mygo.MenuItem, *mygo.Window) { a.OpenPrefs() }},
		&mygo.MenuItem{Label: "立即同步", Click: func(*mygo.MenuItem, *mygo.Window) { a.StartSync() }},
		&mygo.MenuItem{Label: "检查更新", Click: func(*mygo.MenuItem, *mygo.Window) {
			a.OpenPrefs()
			a.CheckUpdate()
		}},
		&mygo.MenuItem{Type: mygo.MenuItemSeparator},
		&mygo.MenuItem{Label: "退出 clipnest", Click: func(*mygo.MenuItem, *mygo.Window) { a.Quit() }},
	)
	return mygo.NewMenu(items)
}

// refreshTrayMenu 换快捷键后更新托盘菜单里的标签。
func (a *App) refreshTrayMenu() {
	a.mu.Lock()
	tray := a.tray
	a.mu.Unlock()
	if tray != nil {
		tray.SetMenu(a.buildMenu())
	}
}
