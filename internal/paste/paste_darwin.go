//go:build darwin

// Package paste 负责「粘贴」这一步：辅助功能授权检查、⌘V 合成、前台应用记录/归还。
package paste

import (
	"fmt"
	"sync"
	"sync/atomic"

	"github.com/ebitengine/purego"
)

var (
	once     sync.Once
	initErr  error
	cgHandle uintptr

	axTrustedFn func() bool
	cgSourceNew func(state uint32) uintptr
	cgEventNew  func(source uintptr, key uint16, down bool) uintptr
	cgSetFlags  func(event uintptr, flags uint64) uintptr
	cgPost      func(tap uint32, event uintptr)
)

const (
	cgEventFlagMaskCommand      = 1 << 20
	cgHIDEventTap               = 0
	cgEventSourceHIDSystemState = 1
	keyCodeV                    = 9
)

func bind(fn any, lib uintptr, name string) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("绑定 %s: %v", name, r)
		}
	}()
	purego.RegisterLibFunc(fn, lib, name)
	return nil
}

// init 惰性载入 CoreGraphics / ApplicationServices。
func init_() error {
	once.Do(func() {
		cg, err := purego.Dlopen("/System/Library/Frameworks/CoreGraphics.framework/CoreGraphics",
			purego.RTLD_NOW|purego.RTLD_LOCAL)
		if err != nil {
			initErr = err
			return
		}
		cgHandle = cg
		for _, item := range []struct {
			fn   any
			name string
		}{
			{&cgSourceNew, "CGEventSourceCreate"},
			{&cgEventNew, "CGEventCreateKeyboardEvent"},
			{&cgSetFlags, "CGEventSetFlags"},
			{&cgPost, "CGEventPost"},
		} {
			if err := bind(item.fn, cg, item.name); err != nil {
				initErr = err
				return
			}
		}
		as, err := purego.Dlopen("/System/Library/Frameworks/ApplicationServices.framework/ApplicationServices",
			purego.RTLD_NOW|purego.RTLD_LOCAL)
		if err == nil {
			if err := bind(&axTrustedFn, as, "AXIsProcessTrusted"); err != nil {
				axTrustedFn = nil
			}
		}
	})
	return initErr
}

// Trusted 报告是否已获得辅助功能授权（自动粘贴的前置条件）。
//
// 自检：SPIKE_NO_AX=1 假装没授权，用来验面板的权限提示条。
func Trusted() bool {
	if noAX() {
		return false
	}
	if err := init_(); err != nil || axTrustedFn == nil {
		return false
	}
	return axTrustedFn()
}

// PromptTrust 弹系统的「辅助功能」授权提示（把 app 加进名单），返回当前是否已授权。
//
// 否则用户得自己去设置里点「+」找到这个 .app，体验差一截。
// 等价于 AXIsProcessTrustedWithOptions({kAXTrustedCheckOptionPrompt: true})。
func PromptTrust() bool {
	if noAX() {
		return false
	}
	if err := initCF(); err != nil || axTrustedWithOptionsFn == nil {
		return Trusted()
	}
	name := []byte("AXTrustedCheckOptionPrompt")
	key := cfStringCreate(0, &name[0], cfStringEncodingUTF8)
	if key == 0 {
		return Trusted()
	}
	defer cfRelease(key)
	keys := [1]uintptr{key}
	values := [1]uintptr{cfBoolTrue}
	dict := cfDictionaryCreate(0, &keys[0], &values[0], 1, cfTypeDictKeyCB, cfTypeDictValCB)
	if dict == 0 {
		return Trusted()
	}
	defer cfRelease(dict)
	return axTrustedWithOptionsFn(dict)
}

// SendCmdV 发一次 ⌘V（进程内 CGEvent）。
func SendCmdV() bool {
	if err := init_(); err != nil {
		return false
	}
	source := cgSourceNew(cgEventSourceHIDSystemState)
	down := cgEventNew(source, keyCodeV, true)
	if down == 0 {
		return false
	}
	cgSetFlags(down, cgEventFlagMaskCommand)
	cgPost(cgHIDEventTap, down)
	sleepMS(40)
	up := cgEventNew(source, keyCodeV, false)
	if up == 0 {
		return false
	}
	cgSetFlags(up, cgEventFlagMaskCommand)
	cgPost(cgHIDEventTap, up)
	return true
}

// 前台目标：macOS 存 pid（0 = 还没记过）。
var frontTarget atomic.Int64

// RememberForeground 记下当前前台应用（面板打开之前调用）。
func RememberForeground() {
	if pid := frontPID(); pid > 0 && pid != int64(selfPID()) {
		frontTarget.Store(pid)
	}
}

// FrontTarget 是记下的前台目标。
func FrontTarget() int64 { return frontTarget.Load() }

// ReturnToPreviousApp 切回面板出现前的应用；粘贴的 ⌘V 要发给它。
//
// activate 通过 NSRunningApplication 完成：objc 的调用放在 sensitive 包，
// 这里用同样的 purego 手法。
func ReturnToPreviousApp() bool {
	pid := frontTarget.Load()
	if pid <= 0 || pid == int64(selfPID()) {
		return false
	}
	return activatePID(pid)
}

// ClaimForeground 在 macOS 上无需额外操作。
func ClaimForeground() bool { return true }
