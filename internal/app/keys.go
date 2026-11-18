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

	"github.com/egoist/mygo/ui"
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

// ModPrefix 是「命令键」的展示前缀：macOS 是 ⌘，其它平台是 Ctrl+。
//
// 状态文案、底栏提示里不要写死 ⌘，否则 Windows 上会显示成 Mac 的键位。
func ModPrefix() string {
	if runtime.GOOS == "darwin" {
		return "⌘"
	}
	return "Ctrl+"
}

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
			case "cmdorctrl", "commandorcontrol", "ctrl", "control":
				flat += "Ctrl+"
			case "cmd", "command", "super", "meta":
				flat += "Win+" // 录制时按下的 Win 键，别显示成 Ctrl
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

// AcceleratorFromInput 把设置窗里录到的一次按键翻成 accelerator（"Super+Shift+K"）。
//
// 只认能注册的组合：带 ⌘/⌃/⌥ 之一（光 ⇧ 不行，会把打大写字母吃掉），或 F1–F12。
// 键名优先用「物理键位」（PhysicalKey）：mygo 的全局快捷键是按 ANSI 硬编码数字键码注册的，
// 跟布局无关，用布局相关的 Key 在非美式键盘上会注册成另一个键。
func AcceleratorFromInput(ev ui.InputEvent) (string, bool) {
	k := ev.PhysicalKey
	if k == ui.KeyUnknown {
		k = ev.Key
	}
	name, ok := acceleratorKeyName(k)
	if !ok {
		return "", false
	}
	var parts []string
	if ev.Mods&ui.Super != 0 {
		parts = append(parts, "Super")
	}
	if ev.Mods&ui.Ctrl != 0 {
		parts = append(parts, "Ctrl")
	}
	if ev.Mods&ui.Alt != 0 {
		parts = append(parts, "Alt")
	}
	if ev.Mods&ui.Shift != 0 {
		parts = append(parts, "Shift")
	}
	// 必须有 ⌘/⌃/⌥ 之一（⇧ 不算）：把 ⇧K 注册成全局快捷键的话，
	// 用户在任何地方打大写 K 都会被吃掉。
	strong := ev.Mods&(ui.Super|ui.Ctrl|ui.Alt) != 0
	if !strong && !(len(name) == 2 && name[0] == 'F') {
		return "", false
	}
	return strings.Join(append(parts, name), "+"), true
}

// acceleratorKeyName 是 mygo 全局快捷键认的键名（accelerator 语法）。
func acceleratorKeyName(k ui.Key) (string, bool) {
	switch {
	case k >= ui.KeyA && k <= ui.KeyZ:
		return string(rune('A' + (k - ui.KeyA))), true
	case k >= ui.Key0 && k <= ui.Key9:
		return string(rune('0' + (k - ui.Key0))), true
	case k >= ui.KeyF1 && k <= ui.KeyF12:
		return fmt.Sprintf("F%d", int(k-ui.KeyF1)+1), true
	}
	switch k {
	case ui.KeySpace:
		return "Space", true
	case ui.KeyEnter:
		return "Enter", true
	case ui.KeyTab:
		return "Tab", true
	case ui.KeyBackspace:
		return "Backspace", true
	case ui.KeyEscape:
		return "Escape", true
	case ui.KeyDelete:
		return "Delete", true
	case ui.KeyInsert:
		return "Insert", true
	case ui.KeyHome:
		return "Home", true
	case ui.KeyEnd:
		return "End", true
	case ui.KeyPageUp:
		return "PageUp", true
	case ui.KeyPageDown:
		return "PageDown", true
	case ui.KeyUp:
		return "Up", true
	case ui.KeyDown:
		return "Down", true
	case ui.KeyLeft:
		return "Left", true
	case ui.KeyRight:
		return "Right", true
	case ui.KeyMinus:
		return "-", true
	case ui.KeyEqual:
		return "=", true
	case ui.KeyComma:
		return ",", true
	case ui.KeyPeriod:
		return ".", true
	case ui.KeySlash:
		return "/", true
	case ui.KeySemicolon:
		return ";", true
	case ui.KeyQuote:
		return "'", true
	case ui.KeyBracketLeft:
		return "[", true
	case ui.KeyBracketRight:
		return "]", true
	case ui.KeyBackslash:
		return "\\", true
	case ui.KeyBackquote:
		return "`", true
	}
	return "", false
}