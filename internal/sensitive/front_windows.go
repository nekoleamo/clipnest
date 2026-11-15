//go:build windows

// 前台应用标识：取前台窗口的进程名（不带 .exe，小写）。
package sensitive

import (
	"strings"
	"sync"
	"syscall"
	"unsafe"
)

var (
	winOnce                        sync.Once
	user32                         = syscall.NewLazyDLL("user32.dll")
	kernel32                       = syscall.NewLazyDLL("kernel32.dll")
	procGetForegroundWindow        = user32.NewProc("GetForegroundWindow")
	procGetWindowThreadProcessID   = user32.NewProc("GetWindowThreadProcessId")
	procOpenProcess                = kernel32.NewProc("OpenProcess")
	procQueryFullProcessImageNameW = kernel32.NewProc("QueryFullProcessImageNameW")
	procCloseHandle                = kernel32.NewProc("CloseHandle")
)

const processQueryLimitedInformation = 0x1000

// FrontApp 是当前前台应用的进程名（小写、去掉 .exe）。
func FrontApp() string {
	winOnce.Do(func() {}) // 仅为了让符号表初始化一次，失败也不 panic

	hwnd, _, _ := procGetForegroundWindow.Call()
	if hwnd == 0 {
		return ""
	}
	var pid uint32
	procGetWindowThreadProcessID.Call(hwnd, uintptr(unsafe.Pointer(&pid)))
	if pid == 0 {
		return ""
	}
	process, _, _ := procOpenProcess.Call(processQueryLimitedInformation, 0, uintptr(pid))
	if process == 0 {
		return ""
	}
	defer procCloseHandle.Call(process)

	buf := make([]uint16, 1024)
	size := uint32(len(buf))
	ok, _, _ := procQueryFullProcessImageNameW.Call(
		process, 0, uintptr(unsafe.Pointer(&buf[0])), uintptr(unsafe.Pointer(&size)))
	if ok == 0 {
		return ""
	}
	path := syscall.UTF16ToString(buf[:size])
	if i := strings.LastIndexAny(path, `\/`); i >= 0 {
		path = path[i+1:]
	}
	return strings.ToLower(path)
}
