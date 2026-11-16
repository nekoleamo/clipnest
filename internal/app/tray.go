package app

import (
	"github.com/egoist/mygo"
)

// AttachTray 挂菜单栏/托盘图标：显示面板 / 偏好设置 / 立即同步 / 退出。
func (a *App) AttachTray(icon []byte) (*mygo.Tray, error) {
	menu := mygo.NewMenu([]*mygo.MenuItem{
		{Label: "显示面板  " + Hotkey(), Click: func(*mygo.MenuItem, *mygo.Window) { a.TogglePanel() }},
		{Type: mygo.MenuItemSeparator},
		{Label: "辅助功能授权…", Click: func(*mygo.MenuItem, *mygo.Window) { a.RequestAccessibility() }},
		{Label: "显示授权提示", Click: func(*mygo.MenuItem, *mygo.Window) { a.ShowAXHint() }},
		{Label: "偏好设置…", Click: func(*mygo.MenuItem, *mygo.Window) { a.OpenPrefs() }},
		{Label: "立即同步", Click: func(*mygo.MenuItem, *mygo.Window) { a.StartSync() }},
		{Type: mygo.MenuItemSeparator},
		{Label: "退出 clipnest", Click: func(*mygo.MenuItem, *mygo.Window) { mygo.App.Quit() }},
	})
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
