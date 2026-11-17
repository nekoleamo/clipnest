// 平台相关的键位展示。
//
// 注册用的 accelerator 一直是 CmdOrCtrl+Shift+V（macOS 是 Command、其它平台是 Control），
// 但显示必须跟系统一致：macOS 用 ⌘⇧V，Windows 用 Ctrl+Shift+V。
// 面板底部提示、行内序号、设置窗都从这里取，别在 UI 里写死符号。
package app

import (
	"fmt"
	"runtime"
	"strings"
)

// macOS 的展示符号。
const (
	macHotkey     = "⌘⇧V"
	macSlotPrefix = "⌘"
	macHints      = "⏎ 粘贴 · ⌥⏎ 纯文本 · ⌘1-9 直贴 · ⌘D 删 · ⌘P 置顶 · ⌘S 收藏 · ⌘E 敏感 · ⌘O 导出 · ⌘Y 复制"
	macHintsShort = "⏎ 粘贴 · ⌥⏎ 纯文本 · ⌘1-9 直贴 · ⌘D 删 · ⌘S 收藏"
)

// 其它平台的展示符号。
const (
	winHotkey     = "Ctrl+Shift+V"
	winSlotPrefix = "Ctrl+"
	winHints      = "⏎ 粘贴 · Alt+⏎ 纯文本 · Ctrl+1-9 直贴 · Ctrl+D 删 · Ctrl+P 置顶 · Ctrl+S 收藏 · Ctrl+E 敏感 · Ctrl+O 导出 · Ctrl+Y 复制"
	winHintsShort = "⏎ 粘贴 · Alt+⏎ 纯文本 · Ctrl+1-9 直贴 · Ctrl+D 删 · Ctrl+S 收藏"
)

// Hotkey 是默认快捷键的展示文案（没有自定义时用）。
func Hotkey() string {
	if runtime.GOOS == "darwin" {
		return macHotkey
	}
	return winHotkey
}

// HotkeyText 把 accelerator 转成展示文案：
// CmdOrCtrl+Shift+V → ⌘⇧V（macOS）/ Ctrl+Shift+V（其它平台）。
func HotkeyText(acc string) string {
	parts := strings.Split(strings.TrimSpace(acc), "+")
	mods := ""
	key := ""
	for _, raw := range parts {
		p := strings.TrimSpace(raw)
		if p == "" {
			continue
		}
		switch strings.ToLower(p) {
		case "cmdorctrl", "commandorcontrol", "cmd", "command", "super", "meta":
			mods += "⌘"
		case "ctrl", "control":
			if runtime.GOOS == "darwin" {
				mods += "⌃"
			} else {
				mods += "Ctrl+"
			}
		case "shift":
			mods += "⇧"
		case "alt", "option", "opt":
			mods += "⌥"
		default:
			key = p
		}
	}
	if key == "" {
		return acc
	}
	if runtime.GOOS != "darwin" {
		// 非 macOS 用朴素写法：Ctrl+Shift+V
		flat := ""
		for _, raw := range parts {
			p := strings.TrimSpace(raw)
			switch strings.ToLower(p) {
			case "cmdorctrl", "commandorcontrol", "cmd", "command", "super", "meta", "ctrl", "control":
				flat += "Ctrl+"
			case "shift":
				flat += "Shift+"
			case "alt", "option", "opt":
				flat += "Alt+"
			}
		}
		return flat + key
	}
	return mods + strings.ToUpper(key)
}

// Hints 是面板底部的操作提示。
func Hints() string {
	if runtime.GOOS == "darwin" {
		return macHints
	}
	return winHints
}

// CompactHints 是窄面板时的短提示。
func CompactHints() string {
	if runtime.GOOS == "darwin" {
		return macHintsShort
	}
	return winHintsShort
}

// Slot 是行内序号标签：⌘1 / Ctrl+1。
func Slot(index int) string {
	prefix := winSlotPrefix
	if runtime.GOOS == "darwin" {
		prefix = macSlotPrefix
	}
	return fmt.Sprintf("%s%d", prefix, index+1)
}
