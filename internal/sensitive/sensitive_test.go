package sensitive

import (
	"strings"
	"testing"
)

func apps(list ...string) []string { return list }

func TestBundleIDsMatchExactlyAndIgnoreCase(t *testing.T) {
	list := apps("com.1password.1password", "com.bitwarden.desktop")
	for _, app := range []string{"com.1password.1password", "COM.1PASSWORD.1PASSWORD", "com.bitwarden.desktop"} {
		if !Matches(app, list) {
			t.Fatalf("%s 应命中", app)
		}
	}
	for _, app := range []string{"com.apple.safari", ""} {
		if Matches(app, list) {
			t.Fatalf("%s 不该命中", app)
		}
	}
}

func TestWindowsProcessNamesIgnoreExeSuffix(t *testing.T) {
	list := apps("keepassxc", "bitwarden")
	for _, app := range []string{"KeePassXC.exe", "keepassxc", "Bitwarden.exe"} {
		if !Matches(app, list) {
			t.Fatalf("%s 应命中", app)
		}
	}
	if Matches("explorer.exe", list) {
		t.Fatal("explorer 不该命中")
	}
}

func TestHelperProcessesOfAListedAppAlsoMatch(t *testing.T) {
	// 1Password 有 com.1password.1password.helper 之类的子进程
	if !Matches("com.1password.1password.helper", apps("com.1password.1password")) {
		t.Fatal("子进程应命中")
	}
	// 不做裸 endsWith
	if Matches("notkeepassxc", apps("keepassxc")) {
		t.Fatal("notkeepassxc 不该命中")
	}
}

func TestConfigOverridesTheBuiltinList(t *testing.T) {
	cfg := Config{IgnoreSensitiveApps: true}
	found := false
	for _, app := range List(cfg) {
		if app == "com.bitwarden.desktop" {
			found = true
		}
	}
	if !found {
		t.Fatal("默认应使用内置表")
	}

	cfg.SensitiveApps = apps("com.my.pass")
	got := List(cfg)
	if len(got) != 1 || got[0] != "com.my.pass" {
		t.Fatalf("覆盖失败：%v", got)
	}
}

func TestCanBeTurnedOffAndHandlesNoApp(t *testing.T) {
	cfg := Config{IgnoreSensitiveApps: true}
	if !ShouldSkip(cfg, "com.bitwarden.desktop") {
		t.Fatal("应拦下 Bitwarden")
	}
	if ShouldSkip(cfg, "com.apple.mail") {
		t.Fatal("邮件不该拦")
	}
	if ShouldSkip(cfg, "") {
		t.Fatal("拿不到应用就不拦")
	}
	cfg.IgnoreSensitiveApps = false
	if ShouldSkip(cfg, "com.bitwarden.desktop") {
		t.Fatal("关掉后不该拦")
	}
}

func TestBuiltinListIsLowercaseAndDeduped(t *testing.T) {
	seen := map[string]bool{}
	for _, app := range DefaultApps {
		if app != strings.ToLower(app) {
			t.Fatalf("%s 要小写", app)
		}
		if seen[app] {
			t.Fatalf("%s 重复了", app)
		}
		seen[app] = true
	}
}
