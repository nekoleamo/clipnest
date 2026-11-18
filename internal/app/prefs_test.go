package app

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"

	"clipnest/internal/backup"
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

// fakeShortcuts 替掉真实的全局快捷键注册（headless 下调 mygo 会等主线程，直接卡死）。
func fakeShortcuts(t *testing.T) *[]string {
	t.Helper()
	var calls []string
	oldReg, oldUnreg := registerShortcut, unregisterShortcut
	registerShortcut = func(acc string, _ func()) error {
		calls = append(calls, "register "+acc)
		return nil
	}
	unregisterShortcut = func(acc string) { calls = append(calls, "unregister "+acc) }
	t.Cleanup(func() { registerShortcut, unregisterShortcut = oldReg, oldUnreg })
	return &calls
}

// TestShortcutRecorderTakesKeyCombo 点一下录制框、再按 ⌘⇧K，快捷键就改成它（不用手打字符串）。
func TestShortcutRecorderTakesKeyCombo(t *testing.T) {
	calls := fakeShortcuts(t)
	a, tt := newPrefsTester(t)

	if err := tt.Click(HotkeyText(a.Settings.Shortcut)); err != nil {
		t.Fatalf("点不到快捷键框：%v", err)
	}
	tt.Frame()
	if !a.PrefsForm().recording {
		t.Fatal("点一下应当进入录制态")
	}
	// 录制一开始就要把旧键摘掉，否则按到它面板会被唤出、录制框收不到键
	if got := (*calls)[len(*calls)-1]; got != "unregister "+a.Settings.Shortcut {
		t.Fatalf("录制开始时应当先摘掉旧快捷键，最后一步是 %q", got)
	}

	tt.Key(ui.Shift|ui.Super, ui.KeyK)
	tt.Frame()
	if got := a.Settings.Shortcut; got != "Super+Shift+K" {
		t.Fatalf("录制没生效：%q", got)
	}
	if settings.Load(a.Paths).Shortcut != "Super+Shift+K" {
		t.Fatal("新快捷键没落盘")
	}
	if a.PrefsForm().recording {
		t.Fatal("录完应当退出录制态")
	}
	if got := a.PrefsForm().shortcut; got != "Super+Shift+K" {
		t.Fatalf("框里应当显示新组合，实际 %q", got)
	}
}

// TestShortcutRecorderEscapeCancels 录制时按 Esc 取消，快捷键保持原样并重新挂上。
func TestShortcutRecorderEscapeCancels(t *testing.T) {
	calls := fakeShortcuts(t)
	a, tt := newPrefsTester(t)
	before := a.Settings.Shortcut

	if err := tt.Click(HotkeyText(before)); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	tt.Key(0, ui.KeyEscape)
	tt.Frame()

	if a.PrefsForm().recording {
		t.Fatal("Esc 应当退出录制态")
	}
	if got := a.Settings.Shortcut; got != before {
		t.Fatalf("Esc 不该改快捷键：%q", got)
	}
	if got := (*calls)[len(*calls)-1]; got != "register "+before {
		t.Fatalf("取消后应当把原快捷键挂回去，最后一步是 %q", got)
	}
}

// TestShortcutRecorderNeedsModifier 光按一个字母不算组合键：继续等，并给出提示。
func TestShortcutRecorderNeedsModifier(t *testing.T) {
	fakeShortcuts(t)
	a, tt := newPrefsTester(t)
	before := a.Settings.Shortcut

	if err := tt.Click(HotkeyText(before)); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	tt.Key(0, ui.KeyK)
	tt.Frame()
	if got := a.Settings.Shortcut; got != before {
		t.Fatalf("光一个字母不该改快捷键：%q", got)
	}
	if !a.PrefsForm().recording {
		t.Fatal("应当还在录制，等一个带修饰键的组合")
	}
	if a.PrefsForm().recordHint == "" {
		t.Fatal("应当提示要按住修饰键")
	}
}

// TestApplyShortcutSameAccReRegisters 录到的还是原来那个组合时也要重新挂上
// （录制开始时它已经被摘掉了，不能因为「没变化」就直接返回）。
func TestApplyShortcutSameAccReRegisters(t *testing.T) {
	calls := fakeShortcuts(t)
	a := newTestApp(t)
	cur := a.Settings.Shortcut

	a.StartShortcutRecord()
	if err := a.ApplyShortcut(cur); err != nil {
		t.Fatalf("同一个快捷键再应用一次不该报错：%v", err)
	}
	if got := (*calls)[len(*calls)-1]; got != "register "+cur {
		t.Fatalf("录到同一个键也要重新注册，最后一步是 %q", got)
	}
	if settings.Load(a.Paths).Shortcut != cur {
		t.Fatal("不该把设置写坏")
	}
	a.StopShortcutRecord()
}

// TestAcceleratorFromInput 按键到 accelerator 的翻译：字母/数字/功能键/方向键、必须带修饰键。
func TestAcceleratorFromInput(t *testing.T) {
	cases := []struct {
		ev   ui.InputEvent
		want string
		ok   bool
	}{
		{ui.InputEvent{Kind: ui.InputKeyDown, PhysicalKey: ui.KeyK, Mods: ui.Super | ui.Shift}, "Super+Shift+K", true},
		{ui.InputEvent{Kind: ui.InputKeyDown, PhysicalKey: ui.KeySpace, Mods: ui.Alt}, "Alt+Space", true},
		{ui.InputEvent{Kind: ui.InputKeyDown, PhysicalKey: ui.Key1, Mods: ui.Ctrl | ui.Alt}, "Ctrl+Alt+1", true},
		{ui.InputEvent{Kind: ui.InputKeyDown, PhysicalKey: ui.KeyF5}, "F5", true}, // 功能键不必带修饰键
		{ui.InputEvent{Kind: ui.InputKeyDown, PhysicalKey: ui.KeyUp, Mods: ui.Super}, "Super+Up", true},
		{ui.InputEvent{Kind: ui.InputKeyDown, PhysicalKey: ui.KeySlash, Mods: ui.Super}, "Super+/", true},
		{ui.InputEvent{Kind: ui.InputKeyDown, PhysicalKey: ui.KeyK}, "", false},
		// 光 ⇧ 不行：把 ⇧K 注册成全局快捷键会吃掉所有大写 K
		{ui.InputEvent{Kind: ui.InputKeyDown, PhysicalKey: ui.KeyK, Mods: ui.Shift}, "", false},
		{ui.InputEvent{Kind: ui.InputKeyDown, PhysicalKey: ui.KeyUnknown, Key: ui.KeyUnknown, Mods: ui.Shift}, "", false},
	}
	for _, c := range cases {
		got, ok := AcceleratorFromInput(c.ev)
		if ok != c.ok || got != c.want {
			t.Errorf("%+v → %q,%v；期望 %q,%v", c.ev, got, ok, c.want, c.ok)
		}
	}
}

// TestShortcutRecorderCancelsOnClickElsewhere 录制时点到别处就当取消：
// 否则焦点走了、旧快捷键却还没挂回去，用户等于没了唤出键。
func TestShortcutRecorderCancelsOnClickElsewhere(t *testing.T) {
	calls := fakeShortcuts(t)
	a, tt := newPrefsTester(t)
	before := a.Settings.Shortcut

	if err := tt.Click(HotkeyText(before)); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if !a.PrefsForm().recording {
		t.Fatal("应当进入录制态")
	}
	if r, ok := tt.Find("历史保留条数（0 = 不限）"); ok {
		tt.ClickAt(r.X+10, r.Y+5) // 点别处的标签
	}
	tt.Frame()
	if a.PrefsForm().recording {
		t.Fatal("点到别处应当退出录制态")
	}
	if got := (*calls)[len(*calls)-1]; got != "register "+before {
		t.Fatalf("取消后应当把原快捷键挂回去，最后一步是 %q", got)
	}
}

// TestClearAllBacksUpFirst 清空前必须先导一份加密备份；没口令就**不清**。
//
// 清空不可撤销，墓碑还会随同步推到别的设备，所以留一份能导回来的备份是底线。
func TestClearAllBacksUpFirst(t *testing.T) {
	stubSecrets(t, "") // 系统凭据里也取不到口令
	a := newTestApp(t)
	record(t, a, "要清掉的条目")

	if msg := a.ClearAllHistoryWithBackup(""); !strings.Contains(msg, "没有清空") {
		t.Fatalf("没口令应当拒绝清空，实际提示 %q", msg)
	}
	if len(a.Rows()) != 1 {
		t.Fatal("没口令时不该清掉任何条目")
	}
	if files, _ := filepath.Glob(filepath.Join(a.Paths.BackupsDir(), "*.cnbak")); len(files) != 0 {
		t.Fatalf("没口令时不该产生备份，实际 %v", files)
	}

	msg := a.ClearAllHistoryWithBackup("pw-123456")
	if len(a.Rows()) != 0 {
		t.Fatalf("应当清空，实际还剩 %d 条（%s）", len(a.Rows()), msg)
	}
	files, _ := filepath.Glob(filepath.Join(a.Paths.BackupsDir(), "*.cnbak"))
	if len(files) != 1 {
		t.Fatalf("应当产出一份 .cnbak，实际 %v（提示 %q）", files, msg)
	}
	// 备份要真能导回来，否则「兜底」只是句空话
	report, err := backup.Import(a.Store.DB, files[0], "pw-123456")
	if err != nil {
		t.Fatalf("备份导不回来：%v", err)
	}
	if report.Inserted != 1 {
		t.Fatalf("备份里应当有那 1 条，实际 %+v", report)
	}
	a.Refresh()
	if len(a.Rows()) != 1 {
		t.Fatalf("导回来之后列表应当有 1 条，实际 %d", len(a.Rows()))
	}
}

// TestPrefsClearAllNeedsWord 清空要点两下、输入「清空」、且先填口令，确认按钮才可点。
func TestPrefsClearAllNeedsWord(t *testing.T) {
	a := newTestApp(t)
	record(t, a, "别手滑清掉的条目")
	tt := ui.NewTester(a.PrefsView, 620, 720)
	tt.Frame()
	tt.Frame()

	if err := tt.Click("清空全部历史…"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if !a.ClearConfirming() {
		t.Fatal("点第一下应当进入确认态")
	}
	if !disabled(tt, "确认清空（先自动备份）") {
		t.Fatal("没输入解锁词、也没口令时确认按钮应当禁用")
	}

	a.PrefsForm().clearWord = "清空"
	tt.Frame()
	if disabled(tt, "确认清空（先自动备份）") {
		t.Fatal("输入解锁词后确认按钮应当可点（口令在点下去时才解析）")
	}
	if len(a.Rows()) != 1 {
		t.Fatal("还没点确认就清了")
	}
}

// disabled 读某个名字的无头控件的禁用状态（找不到就当没禁用）。
func disabled(tt *ui.Tester, name string) bool {
	for _, n := range tt.AccessAll() {
		if n.Name == name {
			return n.Disabled
		}
	}
	return false
}

// TestClearAllUsesSavedPassphrase 设置窗里没填口令时，用系统凭据里存着的那个来备份。
func TestClearAllUsesSavedPassphrase(t *testing.T) {
	stubSecrets(t, "stored-pw-123")
	a := newTestApp(t)
	record(t, a, "靠存着的口令备份")

	msg := a.ClearAllHistoryWithBackup("")
	if len(a.Rows()) != 0 {
		t.Fatalf("有存着的口令就该备份后清空，实际还剩 %d 条（%s）", len(a.Rows()), msg)
	}
	files, _ := filepath.Glob(filepath.Join(a.Paths.BackupsDir(), "*.cnbak"))
	if len(files) != 1 {
		t.Fatalf("应当产出备份，实际 %v（%s）", files, msg)
	}
	if _, err := backup.Import(a.Store.DB, files[0], "stored-pw-123"); err != nil {
		t.Fatalf("用存着的口令应当能导回来：%v", err)
	}
}

// stubSecrets 替掉系统凭据读取（headless 下读钥匙串会等主线程）。
func stubSecrets(t *testing.T, passphrase string) {
	t.Helper()
	old := secretGet
	secretGet = func(string, string) (string, error) {
		if passphrase == "" {
			return "", errors.New("没存过")
		}
		return passphrase, nil
	}
	t.Cleanup(func() { secretGet = old })
}
