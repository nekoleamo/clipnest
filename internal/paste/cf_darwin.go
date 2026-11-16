//go:build darwin

// 只为 AXIsProcessTrustedWithOptions 绑定一个函数指针。
package paste

import (
	"sync"

	"github.com/ebitengine/purego"
)

var (
	cfOnce                 sync.Once
	cfErr                  error
	axTrustedWithOptionsFn func(options uintptr) bool
)

// initCF 惰性绑定 AXIsProcessTrustedWithOptions。
//
// 选项字典不在这里造：CoreFoundation 的 kCFBooleanTrue 经过 dlsym 拿到的值
// 不是可用对象，塞进 CFDictionaryCreate 会段错，改用 NSDictionary（见 trustOptions）。
func initCF() error {
	cfOnce.Do(func() {
		as, err := purego.Dlopen("/System/Library/Frameworks/ApplicationServices.framework/ApplicationServices",
			purego.RTLD_NOW|purego.RTLD_LOCAL)
		if err != nil {
			cfErr = err
			return
		}
		if err := bind(&axTrustedWithOptionsFn, as, "AXIsProcessTrustedWithOptions"); err != nil {
			axTrustedWithOptionsFn = nil
			cfErr = err
		}
	})
	return cfErr
}
