//go:build windows

// Package paste 负责「粘贴」这一步（Windows 侧：SendInput 合成 Ctrl+V）。
package paste

import (
	"fmt"
	"sync"
	"sync/atomic"
	"syscall"
	"unsafe"
)

var (
	once                         sync.Once
	initErr                      error
	user32                       = syscall.NewLazyDLL("user32.dll")
	procSendInput                = user32.NewProc("SendInput")
	procGetForegroundWindow      = user32.NewProc("GetForegroundWindow")
	procSetForegroundWindow      = user32.NewProc("SetForegroundWindow")
	procIsWindow                 = user32.NewProc("IsWindow")
	procEnumWindows              = user32.NewProc("EnumWindows")
	procGetWindowThreadProcessID = user32.NewProc("GetWindowThreadProcessId")
	procIsWindowVisible          = user32.NewProc("IsWindowVisible")
)

const (
	inputKeyboard  = 1
	keyeventfKeyup = 0x0002
	vkControl      = 0x11
	vkV            = 0x56
)

// keyboardInput 是 INPUT 里键盘那半截（Win32 的 INPUT 是 union，这里只用到 ki）。
type keyboardInput struct {
	vk    uint16
	scan  uint16
	flags uint32
	time  uint32
	extra uintptr
}

type input struct {
	typ uint32
	_   uint32 // 对齐：x64 上 union 起始偏移 8
	ki  keyboardInput
}

// Trusted 在 Windows 上没有授权门槛。
func Trusted() bool { return true }

// SendCmdV 发一次 Ctrl+V（四个 INPUT：Ctrl 按下、V 按下、V 抬起、Ctrl 抬起）。
func SendCmdV() bool {
	once.Do(func() {
		if err := procSendInput.Find(); err != nil {
			initErr = err
		}
	})
	if initErr != nil {
		return false
	}
	inputs := []input{
		{typ: inputKeyboard, ki: keyboardInput{vk: vkControl}},
		{typ: inputKeyboard, ki: keyboardInput{vk: vkV}},
		{typ: inputKeyboard, ki: keyboardInput{vk: vkV, flags: keyeventfKeyup}},
		{typ: inputKeyboard, ki: keyboardInput{vk: vkControl, flags: keyeventfKeyup}},
	}
	sent, _, err := procSendInput.Call(
		uintptr(len(inputs)),
		uintptr(unsafe.Pointer(&inputs[0])),
		unsafe.Sizeof(inputs[0]),
	)
	if int(sent) != len(inputs) {
		_ = err
		return false
	}
	return true
}

// 前台目标：Windows 存 HWND（0 = 还没记过）。
var frontTarget atomic.Int64

// RememberForeground 记下当前前台窗口（面板打开之前调用）。
func RememberForeground() {
	hwnd, _, _ := procGetForegroundWindow.Call()
	if hwnd == 0 {
		return
	}
	var pid uint32
	procGetWindowThreadProcessID.Call(hwnd, uintptr(unsafe.Pointer(&pid)))
	if int(pid) == selfPID {
		return
	}
	frontTarget.Store(int64(hwnd))
}

// FrontTarget 是记下的前台窗口句柄。
func FrontTarget() int64 { return frontTarget.Load() }

// ReturnToPreviousApp 把前台还给刚才那个窗口；被前台锁拒绝时返回 false。
func ReturnToPreviousApp() bool {
	raw := frontTarget.Load()
	if raw == 0 {
		return false
	}
	hwnd := uintptr(raw)
	if alive, _, _ := procIsWindow.Call(hwnd); alive == 0 {
		return false
	}
	ok, _, _ := procSetForegroundWindow.Call(hwnd)
	return ok != 0
}

// ClaimForeground 把自己的可见顶层窗口抢回前台。
//
// Windows 上「能收键盘」= 必须是前台窗口；系统可能因为前台锁拒绝，那就只返回 false。
var (
	selfPID   = syscall.Getpid()
	foundHWND atomic.Int64
)

func ClaimForeground() bool {
	once.Do(func() {})
	foundHWND.Store(0)
	cb := syscall.NewCallback(func(hwnd uintptr, _ uintptr) uintptr {
		var pid uint32
		procGetWindowThreadProcessID.Call(hwnd, uintptr(unsafe.Pointer(&pid)))
		if int(pid) == selfPID {
			if vis, _, _ := procIsWindowVisible.Call(hwnd); vis != 0 {
				foundHWND.Store(int64(hwnd))
				return 0 // 找到就停
			}
		}
		return 1
	})
	procEnumWindows.Call(cb, 0)
	hwnd := foundHWND.Load()
	if hwnd == 0 {
		return false
	}
	ok, _, _ := procSetForegroundWindow.Call(uintptr(hwnd))
	return ok != 0
}

var _ = fmt.Sprintf
