package app

import (
	"testing"

	"github.com/egoist/mygo/ui"

	"clipnest/internal/settings"
	"clipnest/internal/sync"
	"clipnest/internal/update"
)

// 设置窗的控件必须跨帧保留编辑：以前每帧都从 settings.json 重新取一遍值绑到局部变量上，
// 于是 + 只能跳一次、− 卡住不动、复选框点了不回勾、输入框打不进字、选项卡选不中。
// 这几个用例按症状逐条钉住。

// newPrefsTester 起一个设置窗的无头渲染器（620×720，与真实窗口一致）。
func newPrefsTester(t *testing.T) (*App, *ui.Tester) {
	t.Helper()
	a := newTestApp(t)
	tt := ui.NewTester(a.PrefsView, 620, 720)
	tt.Frame()
	tt.Frame()
	return a, tt
}

// fieldValues 收集当前所有单行输入框里显示的文字。
func fieldValues(tt *ui.Tester) []string {
	var out []string
	for _, n := range tt.AccessAll() {
		if n.Role == ui.RoleTextField {
			out = append(out, n.Value)
		}
	}
	return out
}

// TestPrefsNumberInputStepsAccumulate 点 + 三次要变成 +300，点 − 一次要退回来。
func TestPrefsNumberInputStepsAccumulate(t *testing.T) {
	a, tt := newPrefsTester(t)
	start := a.PrefsForm().maxItems
	if start != 5000 {
		t.Fatalf("默认历史条数应当是 5000，实际 %v", start)
	}
	for i := 1; i <= 3; i++ {
		if err := tt.Click("Increase"); err != nil {
			t.Fatalf("第 %d 次点 + 失败：%v", i, err)
		}
		tt.Frame()
		if want := start + float64(i*100); a.PrefsForm().maxItems != want {
			t.Fatalf("点 %d 次 + 后应当是 %v，实际 %v（每帧都被旧值盖回去了）", i, want, a.PrefsForm().maxItems)
		}
	}
	// 界面上显示的也必须跟着走（不是只有内存里变了）
	if got := fieldValues(tt)[0]; got != "5300" {
		t.Fatalf("输入框里应当显示 5300，实际 %q", got)
	}
	if err := tt.Click("Decrease"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if got := a.PrefsForm().maxItems; got != 5200 {
		t.Fatalf("点一次 − 后应当是 5200，实际 %v", got)
	}
}

// TestPrefsSegmentedKeepsChoice 面板位置与同步方式两个选项卡都要选得中。
func TestPrefsSegmentedKeepsChoice(t *testing.T) {
	a, tt := newPrefsTester(t)

	if err := tt.Click("贴屏幕底部"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if got := a.PrefsForm().position; got != 1 {
		t.Fatalf("面板位置应当是贴底（1），实际 %d", got)
	}
	if !checked(tt, "贴屏幕底部") || checked(tt, "屏幕居中") {
		t.Fatal("选项卡的选中状态没跟着变")
	}

	// 同步方式在滚动区下面，先滚下去
	tt.Scroll(300, 300, 0, 500)
	tt.Frame()
	if err := tt.Click("WebDAV"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if got := a.PrefsForm().backendKind; got != 1 {
		t.Fatalf("同步方式应当是 WebDAV（1），实际 %d", got)
	}
}

// TestPrefsCheckboxToggles 复选框点一下要真的翻转（并且下一帧还在）。
func TestPrefsCheckboxToggles(t *testing.T) {
	a, tt := newPrefsTester(t)
	tt.Scroll(300, 300, 0, 300)
	tt.Frame()

	name := "按应用过滤敏感内容（关掉则不拦）"
	before := a.PrefsForm().ignoreSensitive
	if err := tt.Click(name); err != nil {
		t.Fatalf("点复选框失败：%v", err)
	}
	tt.Frame()
	if got := a.PrefsForm().ignoreSensitive; got == before {
		t.Fatalf("复选框没翻转：%v → %v", before, got)
	}
	if got := checked(tt, name); got == before {
		t.Fatal("界面上的勾选状态没变")
	}
	tt.Frame() // 再多跑一帧，确认不会被盖回去
	if got := a.PrefsForm().ignoreSensitive; got == before {
		t.Fatal("下一帧又被盖回去了")
	}
}

// TestPrefsTextInputKeepsTyping 输入框里打的字要留下来（不能下一帧就被 settings 里的旧值盖回去）。
func TestPrefsTextInputKeepsTyping(t *testing.T) {
	a, tt := newPrefsTester(t)
	r, ok := tt.Find("历史保留条数（0 = 不限）")
	if !ok {
		t.Fatal("找不到历史保留条数的标签")
	}
	tt.ClickAt(r.X+20, r.Y+28) // 点进它下面的输入框
	tt.Frame()
	tt.Key(ui.Cmd, ui.KeyA) // 全选
	tt.Frame()
	tt.Type("123")
	tt.Frame()
	if got := a.PrefsForm().maxItems; got != 123 {
		t.Fatalf("打的字没留在输入框里：%v", got)
	}
	tt.Frame()
	if got := a.PrefsForm().maxItems; got != 123 {
		t.Fatalf("下一帧又被盖回去了：%v", got)
	}
}

// TestPrefsSaveWritesSettings 点「保存」把整张草稿写回磁盘（并且面板高度立刻生效）。
func TestPrefsSaveWritesSettings(t *testing.T) {
	a, tt := newPrefsTester(t)
	if err := tt.Click("Increase"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if err := tt.Click("保存"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()

	if got := a.Settings.MaxItems; got != 5100 {
		t.Fatalf("内存里的设置没更新：%d", got)
	}
	if got := settings.Load(a.Paths).MaxItems; got != 5100 {
		t.Fatalf("设置没落盘：%d", got)
	}
	if got := a.PrefsForm().status; got != "✅ 已保存" {
		t.Fatalf("保存提示应当留在窗口上，实际 %q", got)
	}
}

// TestPrefsSaveKeepsPassphraseIndependentOfBackend 本地目录模式也要能设口令：
// 加密与备份都靠它，以前这个输入框只在 WebDAV 分支里出现。
func TestPrefsSaveKeepsPassphraseIndependentOfBackend(t *testing.T) {
	a, tt := newPrefsTester(t)
	f := a.PrefsForm()
	if f.backendKind != 0 {
		t.Fatal("默认应当是本地目录")
	}
	f.passphrase = "s3cret"
	f.encrypt = true
	if err := tt.Click("保存"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if got := sync.LoadConfig(a.Paths).Encryption.Enabled; !got {
		t.Fatal("端到端加密没落盘")
	}
	if got := a.PrefsForm().passphrase; got != "" {
		t.Fatalf("保存后口令应当从草稿里清掉，实际 %q", got)
	}
}

// TestPrefsUpdateSourceSwitches 更新源下拉要能切换：它排的活是 a.later，
// 而 a.later 只有 runPending 会跑，以前只有面板视图跑过 runPending。
func TestPrefsUpdateSourceSwitches(t *testing.T) {
	a := newTestApp(t)
	a.Settings.ReleaseSource = update.SourceGitee
	a.Settings.ReleaseRepo = "null_593_5354/clipnest"
	tt := ui.NewTester(a.PrefsView, 620, 720)
	tt.Frame()
	tt.Frame()

	if err := tt.Click("Gitee"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if err := tt.Click("GitHub"); err != nil {
		t.Fatalf("下拉里点不到 GitHub：%v", err)
	}
	tt.Frame()
	tt.Frame()
	if got := a.Settings.ReleaseSource; got != update.SourceGitHub {
		t.Fatalf("源没切过去：%q", got)
	}
	if got := settings.Load(a.Paths).ReleaseSource; got != update.SourceGitHub {
		t.Fatalf("切源没落盘：%q", got)
	}
}

// TestPrefsResetDropsUnsavedEdits 重开设置窗要丢掉上次没保存的编辑。
func TestPrefsResetDropsUnsavedEdits(t *testing.T) {
	a, tt := newPrefsTester(t)
	if err := tt.Click("Increase"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	a.ResetPrefsForm()
	tt.Frame()
	if got := a.PrefsForm().maxItems; got != float64(a.Settings.MaxItems) {
		t.Fatalf("重开应当回到已保存的值 %d，实际 %v", a.Settings.MaxItems, got)
	}
}

// checked 读某个名字的无头控件的勾选状态（找不到返回 false）。
func checked(tt *ui.Tester, name string) bool {
	for _, n := range tt.AccessAll() {
		if n.Name == name {
			return n.Checked
		}
	}
	return false
}
