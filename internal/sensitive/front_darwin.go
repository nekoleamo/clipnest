//go:build darwin

// 前台应用标识：purego 直接问 NSWorkspace，走不通再退到 lsappinfo。
package sensitive

import (
	"bytes"
	"fmt"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"unsafe"

	"github.com/ebitengine/purego"
)

var (
	// keepAlive 保住 dlopen 出来的框架句柄。
	keepAlive []uintptr
	objcOnce  sync.Once
	objcErr   error
	// 两参数版 objc_msgSend：NSWorkspace/NSRunningApplication 的这些查询都不带实参。
	msgSend2 func(id, sel uintptr) uintptr
	// 五参数版：getCString:maxLength:encoding:（self, sel, buf, len, encoding）。
	msgSend5 func(id, sel, a, b, c uintptr) uintptr
	classFn  func(name uintptr) uintptr
	selFn    func(name uintptr) uintptr
)

// withCString 把 Go 字符串转成 C 字符串指针，调用期间保活。
func withCString[T any](s string, fn func(uintptr) T) T {
	b := append([]byte(s), 0)
	v := fn(uintptr(unsafe.Pointer(&b[0])))
	runtime.KeepAlive(b)
	return v
}

// initObjC 只做一次 dlopen + dlsym；失败时 objcErr 非 nil，调用方退到 lsappinfo。
func initObjC() {
	// AppKit 必须真的载入进程，否则 objc_getClass("NSWorkspace") 返回 0（纯 Go 程序默认不链 AppKit）。
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
	if err = bind(&msgSend2, lib, "objc_msgSend"); err != nil {
		objcErr = err
		return
	}
	if err = bind(&msgSend5, lib, "objc_msgSend"); err != nil {
		objcErr = err
		return
	}
	if err = bind(&classFn, lib, "objc_getClass"); err != nil {
		objcErr = err
		return
	}
	if err = bind(&selFn, lib, "sel_registerName"); err != nil {
		objcErr = err
	}
}

// bind 绑定符号；purego 用 panic 报错，这里转成 error。
func bind(fn any, lib uintptr, name string) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("绑定 %s: %v", name, r)
		}
	}()
	purego.RegisterLibFunc(fn, lib, name)
	return nil
}

// sel 取 selector；objc_getClass 取类。
func sel(name string) uintptr   { return withCString(name, selFn) }
func class(name string) uintptr { return withCString(name, classFn) }

// objcString 读 NSString 的内容。
//
// 用 getCString:maxLength:encoding: 让系统写进 Go 的缓冲区，不去解引用外来指针
// （既躲开 uintptr→unsafe.Pointer 的越界风险，go vet 也干净）。
func objcString(obj uintptr) string {
	if obj == 0 {
		return ""
	}
	const nsUTF8StringEncoding = 4
	buf := make([]byte, 1024)
	ok := msgSend5(obj, sel("getCString:maxLength:encoding:"),
		uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)-1), nsUTF8StringEncoding)
	runtime.KeepAlive(buf)
	if ok == 0 {
		return ""
	}
	if i := bytes.IndexByte(buf, 0); i >= 0 {
		buf = buf[:i]
	}
	return string(buf)
}

// FrontApp 是当前前台应用的标识（bundle id 优先、退化为可执行名）。
func FrontApp() string {
	objcOnce.Do(initObjC)
	if objcErr == nil {
		if app := frontAppObjC(); app != "" {
			return app
		}
	}
	return frontAppLsappinfo()
}

func frontAppObjC() string {
	wsClass := class("NSWorkspace")
	if wsClass == 0 {
		return ""
	}
	ws := msgSend2(wsClass, sel("sharedWorkspace"))
	if ws == 0 {
		return ""
	}
	app := msgSend2(ws, sel("frontmostApplication"))
	if app == 0 {
		return ""
	}
	if id := objcString(msgSend2(app, sel("bundleIdentifier"))); id != "" {
		return id
	}
	return objcString(msgSend2(app, sel("localizedName")))
}

// frontAppLsappinfo 是兜底：purego 走不通时问系统命令（约 20ms，每次复制跑一次）。
func frontAppLsappinfo() string {
	front, err := exec.Command("lsappinfo", "front").Output()
	if err != nil {
		return ""
	}
	asn := strings.TrimSpace(string(front))
	if asn == "" {
		return ""
	}
	out, err := exec.Command("lsappinfo", "info", "-only", "bundleid", asn).Output()
	if err != nil {
		return ""
	}
	// 形如：CFBundleIdentifier"="com.apple.Safari
	line := strings.TrimSpace(strings.SplitN(string(out), "\n", 2)[0])
	if i := strings.Index(line, "="); i >= 0 {
		return strings.Trim(strings.TrimSpace(line[i+1:]), `"`)
	}
	return ""
}
