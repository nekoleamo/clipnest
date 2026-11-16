package app

import (
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// fileExists 判断路径存在且是文件/目录。
func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// baseName 取路径最后一段（Windows 的 \\ 也认）。
func baseName(path string) string {
	if i := strings.LastIndexAny(path, `/\`); i >= 0 {
		return path[i+1:]
	}
	return path
}

// filepathJoin 是 filepath.Join 的短别名。
func filepathJoin(parts ...string) string { return filepath.Join(parts...) }

// copyFile 复制文件（先截断再写）。
func copyFile(src, dst string) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

// writeFile 写文件（自动建目录）。
func writeFile(dst string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	return os.WriteFile(dst, data, 0o644)
}

// homeDir 取用户主目录。
func homeDir() (string, error) {
	if h := os.Getenv("HOME"); h != "" {
		return h, nil
	}
	return os.UserHomeDir()
}

// openAccessibilitySettings 打开系统设置的「辅助功能」授权页。
func openAccessibilitySettings() {
	if runtime.GOOS != "darwin" {
		return
	}
	if err := exec.Command("open",
		"x-apple.systempreferences:com.apple.preference.security?Privacy_Accessibility").Run(); err != nil {
		logf("AXP open settings failed: %v", err)
		return
	}
	logf("AXP opened settings")
}

// max 返回较大值。
func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
