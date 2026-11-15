// 平台相关的键位展示。
//
// 注册用的 accelerator 一直是 CmdOrCtrl+Shift+V（macOS 是 Command、其它平台是 Control），
// 但显示必须跟系统一致：macOS 用 ⌘⇧V，Windows 用 Ctrl+Shift+V。
// 面板底部提示、行内序号、设置窗都从这里取，别在 UI 里写死符号。
package app

import (
	"fmt"
	"runtime"
)

// macOS 的展示符号。
const (
	macHotkey     = "⌘⇧V"
	macSlotPrefix = "⌘"
	macHints      = "⏎ 粘贴 · ⌘1-9 直贴 · ⌘D 删 · ⌘P 置顶 · ⌘S 收藏 · ⌘E 敏感 · ⌘O 导出 · ⌘Y 复制"
)

// 其它平台的展示符号。
const (
	winHotkey     = "Ctrl+Shift+V"
	winSlotPrefix = "Ctrl+"
	winHints      = "⏎ 粘贴 · Ctrl+1-9 直贴 · Ctrl+D 删 · Ctrl+P 置顶 · Ctrl+S 收藏 · Ctrl+E 敏感 · Ctrl+O 导出 · Ctrl+Y 复制"
)

// Hotkey 是唤出面板的快捷键展示文案。
func Hotkey() string {
	if runtime.GOOS == "darwin" {
		return macHotkey
	}
	return winHotkey
}

// Hints 是面板底部的操作提示。
func Hints() string {
	if runtime.GOOS == "darwin" {
		return macHints
	}
	return winHints
}

// Slot 是行内序号标签：⌘1 / Ctrl+1。
func Slot(index int) string {
	prefix := winSlotPrefix
	if runtime.GOOS == "darwin" {
		prefix = macSlotPrefix
	}
	return fmt.Sprintf("%s%d", prefix, index+1)
}
