package app

import (
	"github.com/egoist/mygo"
)

// AttachTray 挂菜单栏/托盘图标：显示面板 / 偏好设置 / 立即同步 / 退出。
func (a *App) AttachTray(icon []byte) (*mygo.Tray, error) {
	menu := a.buildMenu()
	tray, err := mygo.NewTray(mygo.TrayOptions{
		ID:             "clipnest",
		Icon:           icon,
		IconIsTemplate: true,
		Title:          "❐",
		Menu:           menu,
	})
	if err != nil {
		return nil, err
	}
	tray.OnClick(func() { a.TogglePanel() })
	a.tray = tray
	return tray, nil
}

// buildMenu 是托盘菜单（换快捷键后要重建它，标签里带快捷键文案）。
func (a *App) buildMenu() *mygo.Menu {
	return mygo.NewMenu([]*mygo.MenuItem{
		{Label: "显示面板  " + a.Hotkey(), Click: func(*mygo.MenuItem, *mygo.Window) { a.TogglePanel() }},
		{Label: "暂停记录", Type: mygo.MenuItemCheckbox, Checked: a.Paused(),
			Click: func(*mygo.MenuItem, *mygo.Window) { a.TogglePause() }},
		{Type: mygo.MenuItemSeparator},
		{Label: "辅助功能授权…", Click: func(*mygo.MenuItem, *mygo.Window) { a.RequestAccessibility() }},
		{Label: "显示授权提示", Click: func(*mygo.MenuItem, *mygo.Window) { a.ShowAXHint() }},
		{Label: "偏好设置…", Click: func(*mygo.MenuItem, *mygo.Window) { a.OpenPrefs() }},
		{Label: "立即同步", Click: func(*mygo.MenuItem, *mygo.Window) { a.StartSync() }},
		{Type: mygo.MenuItemSeparator},
		{Label: "退出 clipnest", Click: func(*mygo.MenuItem, *mygo.Window) { a.Quit() }},
	})
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
