package app

import "testing"

// TestToggleAction 快捷键三态：不可见就唤出、被盖住就拿到最前、在前面就收起。
func TestToggleAction(t *testing.T) {
	cases := []struct {
		visible, hidden bool
		want            panelAction
	}{
		{false, false, actShow},   // 收着：唤出
		{false, true, actShow},    // 收着且材质停画：仍是唤出
		{true, true, actRaise},    // 在屏上但被别的 app 盖住：拿到最前（这里以前误判成收起）
		{true, false, actDismiss}, // 就在前面：收起
	}
	for _, c := range cases {
		if got := toggleAction(c.visible, c.hidden); got != c.want {
			t.Fatalf("visible=%v hidden=%v：想要 %d，得到 %d", c.visible, c.hidden, c.want, got)
		}
	}
}
