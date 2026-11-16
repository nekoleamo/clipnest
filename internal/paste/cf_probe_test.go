//go:build darwin

package paste

import "testing"

// TestTrustOptionsBuilt 确认授权提示用的选项字典造得出来（只造字典，不触发系统弹框）。
func TestTrustOptionsBuilt(t *testing.T) {
	if err := initCF(); err != nil {
		t.Fatalf("initCF: %v", err)
	}
	if axTrustedWithOptionsFn == nil {
		t.Fatal("AXIsProcessTrustedWithOptions 没绑上")
	}
	if d := trustOptions(); d == 0 {
		t.Fatal("选项字典为 0（NSDictionary 造失败）")
	}
}
