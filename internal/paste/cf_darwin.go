//go:build darwin

// CoreFoundation 最小绑定：只为 AXIsProcessTrustedWithOptions 造一个选项字典。
package paste

import (
	"sync"
	"unsafe"

	"github.com/ebitengine/purego"
)

const cfStringEncodingUTF8 = 0x08000100

var (
	cfOnce sync.Once
	cfErr  error

	cfStringCreate     func(alloc uintptr, cStr *byte, encoding uint32) uintptr
	cfDictionaryCreate func(alloc uintptr, keys, values *uintptr, count int64, keyCB, valueCB uintptr) uintptr
	cfRelease          func(obj uintptr)

	axTrustedWithOptionsFn func(options uintptr) bool

	// 下面三个是导出常量：dlsym 拿到的是变量地址，再解一次引用才是对象指针。
	cfBoolTrue      uintptr
	cfTypeDictKeyCB uintptr
	cfTypeDictValCB uintptr
)

// initCF 惰性绑定 CoreFoundation 与 AXIsProcessTrustedWithOptions。
func initCF() error {
	cfOnce.Do(func() {
		cf, err := purego.Dlopen("/System/Library/Frameworks/CoreFoundation.framework/CoreFoundation",
			purego.RTLD_NOW|purego.RTLD_LOCAL)
		if err != nil {
			cfErr = err
			return
		}
		for _, item := range []struct {
			fn   any
			name string
		}{
			{&cfStringCreate, "CFStringCreateWithCString"},
			{&cfDictionaryCreate, "CFDictionaryCreate"},
			{&cfRelease, "CFRelease"},
		} {
			if err := bind(item.fn, cf, item.name); err != nil {
				cfErr = err
				return
			}
		}
		for _, item := range []struct {
			out  *uintptr
			name string
		}{
			{&cfBoolTrue, "kCFBooleanTrue"},
			{&cfTypeDictKeyCB, "kCFTypeDictionaryKeyCallBacks"},
			{&cfTypeDictValCB, "kCFTypeDictionaryValueCallBacks"},
		} {
			p, err := purego.Dlsym(cf, item.name)
			if err != nil || p == 0 {
				cfErr = err
				return
			}
			*item.out = **(**uintptr)(unsafe.Pointer(&p))
		}
		as, err := purego.Dlopen("/System/Library/Frameworks/ApplicationServices.framework/ApplicationServices",
			purego.RTLD_NOW|purego.RTLD_LOCAL)
		if err != nil {
			return
		}
		if err := bind(&axTrustedWithOptionsFn, as, "AXIsProcessTrustedWithOptions"); err != nil {
			axTrustedWithOptionsFn = nil
		}
	})
	return cfErr
}
