//go:build !darwin && !windows

// Package paste 在其它平台上不做事：面板能出，但不会自动粘贴。
package paste

// Trusted 报告是否可以自动粘贴。
// NeedsTrust 报告是否需要「辅助功能授权」（非 macOS 平台没有这道门）。
func NeedsTrust() bool { return false }

func Trusted() bool { return false }

// SendCmdV 在这个平台上没有实现。
func SendCmdV() bool { return false }

// RememberForeground 不做事。
func RememberForeground() {}

// FrontTarget 恒为 0。
func FrontTarget() int64 { return 0 }

// ReturnToPreviousApp 不做事。
func ReturnToPreviousApp() bool { return false }

// ClaimForeground 不做事。
func ClaimForeground() bool { return true }

// PromptTrust 在这个平台上没有实现。
func PromptTrust() bool { return Trusted() }
