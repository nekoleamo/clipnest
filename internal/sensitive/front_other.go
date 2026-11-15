//go:build !darwin && !windows

// 其它平台没有实现：返回空串，敏感过滤自然跳过。
package sensitive

// FrontApp 在非 macOS/Windows 上不可用。
func FrontApp() string { return "" }
