// Package settings 是应用设置：<数据根>/settings.json。
//
// 只存「改了立刻能看出效果」的项；未知字段解析时忽略（向前兼容），
// 文件坏了就退回默认值并记一条日志，不让应用起不来。
package settings

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"

	"clipnest/internal/paths"
)

// 面板尺寸（点）。宽度固定：左边列表 + 右边图片预览。
const (
	PanelHeightMin = 240
	PanelHeightMax = 1200
	PanelWidth     = 720
)

// 面板位置取值。
const (
	PositionCenter = "center"
	PositionBottom = "bottom"
)

// Settings 是用户设置。
type Settings struct {
	// MaxItems 是历史保留条数上限（0 = 不限）；清理时收藏与置顶受保护（契约 9）。
	MaxItems int64 `json:"max_items"`
	// PanelHeight 是面板高度（点）。
	PanelHeight int32 `json:"panel_height"`
	// PanelPosition 是 center（屏幕正中）或 bottom（贴屏幕底部，靠近输入位置）。
	PanelPosition string `json:"panel_position"`
	// SensitiveApps 里复制的内容不进历史（bundle id / 进程名）。
	// 空 = 用内置白名单；填了就以这里为准。
	SensitiveApps []string `json:"sensitive_apps"`
	// IgnoreSensitiveApps 为 false 就不按应用过滤。
	IgnoreSensitiveApps bool `json:"ignore_sensitive_apps"`
	// ExportDir 是导出目录（空 = ~/Downloads）。
	ExportDir string `json:"export_dir"`
	// HideAccessibilityHint 关掉面板顶部那条「没有辅助功能授权」提示（用户自己知道就行）。
	// 授权状态真变了会自动恢复正常显示（见 tray 里的菜单项）。
	HideAccessibilityHint bool `json:"hide_accessibility_hint"`
}

// Default 是默认设置。
func Default() Settings {
	return Settings{
		MaxItems:            5000,
		PanelHeight:         560,
		PanelPosition:       PositionCenter,
		SensitiveApps:       nil,
		IgnoreSensitiveApps: true,
		ExportDir:           "",
	}
}

// Normalize 把越界的值拉回合法范围（手改过 json 也安全）。
func Normalize(s Settings) Settings {
	if s.MaxItems < 0 {
		s.MaxItems = 0
	}
	if s.MaxItems > 1_000_000 {
		s.MaxItems = 1_000_000
	}
	if s.PanelPosition != PositionCenter && s.PanelPosition != PositionBottom {
		s.PanelPosition = PositionCenter
	}
	if s.PanelHeight < PanelHeightMin {
		s.PanelHeight = PanelHeightMin
	}
	if s.PanelHeight > PanelHeightMax {
		s.PanelHeight = PanelHeightMax
	}
	return s
}

// Load 读设置；文件不存在或解析失败时给默认值。
func Load(p paths.Paths) Settings {
	data, err := os.ReadFile(p.Settings())
	switch {
	case errors.Is(err, os.ErrNotExist):
		return Default()
	case err != nil:
		Logf("SETTINGS 读取失败（用默认值）：%v", err)
		return Default()
	}
	// 先铺默认值再覆盖，未知字段忽略（等价于 serde 的 #[serde(default)]）
	s := Default()
	if err := json.Unmarshal(data, &s); err != nil {
		Logf("SETTINGS 解析失败（用默认值）：%v", err)
		return Default()
	}
	return Normalize(s)
}

// Save 原子写（先写 .tmp 再改名）。
func Save(s Settings, p paths.Paths) error {
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	return writeAtomic(p.Settings(), data)
}

// writeAtomic 先写临时文件再改名，避免半截文件。
func writeAtomic(path string, data []byte) error {
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// Logf 是日志出口，宿主可替换（默认丢弃，测试不吵）。
var Logf = func(format string, args ...any) {}
