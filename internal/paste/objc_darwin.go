//go:build darwin

package paste

import (
	"os"
	"runtime"
	"sync"
	"time"
	"unsafe"

	"github.com/ebitengine/purego"
)

// noAX 支持 SPIKE_NO_AX=1 的自检开关。
func noAX() bool { return os.Getenv("SPIKE_NO_AX") != "" }

func sleepMS(ms int) { time.Sleep(time.Duration(ms) * time.Millisecond) }

func selfPID() int { return os.Getpid() }

// activatePID 让指定 pid 的应用回到前台（NSRunningApplication.activateWithOptions）。
func activatePID(pid int64) bool {
	objcOnce.Do(initObjC)
	if objcErr != nil {
		return false
	}
	cls := objcClass("NSRunningApplication")
	if cls == 0 {
		return false
	}
	app := msgSend4(cls, objcSel("runningApplicationWithProcessIdentifier:"), uintptr(pid), 0, 0)
	if app == 0 {
		return false
	}
	// NSApplicationActivateIgnoringOtherApps = 1 << 1 = 2
	return msgSend3(app, objcSel("activateWithOptions:"), 2, 0) != 0
}

var (
	objcOnce sync.Once
	objcErr  error
	classFn  func(name uintptr) uintptr
	selFn    func(name uintptr) uintptr
	msgSend2 func(id, sel uintptr) uintptr
	msgSend3 func(id, sel, a, b uintptr) uintptr
	msgSend4 func(id, sel, a, b, c uintptr) uintptr
)

func initWithCString(fn func(uintptr) uintptr, s string) uintptr {
	b := append([]byte(s), 0)
	v := fn(uintptr(unsafe.Pointer(&b[0])))
	runtime.KeepAlive(b)
	return v
}

func initObjC() {
	for _, framework := range []string{
		"/System/Library/Frameworks/Foundation.framework/Foundation",
		"/System/Library/Frameworks/AppKit.framework/AppKit",
	} {
		if h, err := purego.Dlopen(framework, purego.RTLD_NOW|purego.RTLD_GLOBAL); err == nil {
			keepAlive = append(keepAlive, h)
		}
	}
	lib, err := purego.Dlopen("/usr/lib/libobjc.A.dylib", purego.RTLD_NOW|purego.RTLD_LOCAL)
	if err != nil {
		objcErr = err
		return
	}
	if err := bind(&classFn, lib, "objc_getClass"); err != nil {
		objcErr = err
		return
	}
	if err := bind(&selFn, lib, "sel_registerName"); err != nil {
		objcErr = err
		return
	}
	if err := bind(&msgSend2, lib, "objc_msgSend"); err != nil {
		objcErr = err
		return
	}
	if err := bind(&msgSend3, lib, "objc_msgSend"); err != nil {
		objcErr = err
		return
	}
	if err := bind(&msgSend4, lib, "objc_msgSend"); err != nil {
		objcErr = err
	}
}

var keepAlive []uintptr

func objcClass(name string) uintptr { return initWithCString(classFn, name) }
func objcSel(name string) uintptr   { return initWithCString(selFn, name) }

// frontPID 是当前前台应用的 pid（拿不到返回 0）。
func frontPID() int64 {
	objcOnce.Do(initObjC)
	if objcErr != nil {
		return 0
	}
	ws := msgSend2(objcClass("NSWorkspace"), objcSel("sharedWorkspace"))
	if ws == 0 {
		return 0
	}
	app := msgSend2(ws, objcSel("frontmostApplication"))
	if app == 0 {
		return 0
	}
	return int64(int32(msgSend2(app, objcSel("processIdentifier"))))
}
