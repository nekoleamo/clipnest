package sync

import (
	"runtime"
	"strings"
	"testing"
)

// TestSecretStoreNameIsPlatformSpecific 给用户看的文案要按平台说对话。
func TestSecretStoreNameIsPlatformSpecific(t *testing.T) {
	got := SecretStoreName()
	switch runtime.GOOS {
	case "darwin":
		if got != "钥匙串" {
			t.Fatalf("macOS 应当说「钥匙串」，实际 %q", got)
		}
	case "windows":
		if !strings.Contains(got, "凭据管理器") {
			t.Fatalf("Windows 应当说「凭据管理器」，实际 %q", got)
		}
	default:
		if got == "" {
			t.Fatal("不能为空")
		}
	}
}
