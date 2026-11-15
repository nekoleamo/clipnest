// Package sensitive 是敏感应用过滤：密码管理器里复制的东西默认不进历史。
//
// 规则按应用身份（macOS 的 bundle id、Windows 的进程名）判断，不看内容——
// 猜密码长得像什么既不可靠也没必要。
//
// 三道口子：
//  1. 内置白名单（DefaultApps），可在 settings.json 的 sensitive_apps 覆盖；
//  2. cfg.IgnoreSensitiveApps = true 时整体关掉（零值即「开着」，见 ShouldSkip）；
//  3. 面板里可以把单条标记/取消标记，标记的条目显示 🔒。
package sensitive

import "strings"

// DefaultApps 是内置白名单：密码管理器与系统钥匙串相关进程。
//
// macOS 用 bundle id（大小写不敏感地比对），Windows 用进程名（不带 .exe）。
var DefaultApps = []string{
	// 1Password
	"com.1password.1password",
	"com.agilebits.onepassword7",
	"1password",
	// Bitwarden
	"com.bitwarden.desktop",
	"bitwarden",
	// KeePassXC / KeePass
	"org.keepassxc.keepassxc",
	"keepassxc",
	"keepass",
	// LastPass / Dashlane / NordPass / Enpass / Proton Pass
	"com.lastpass.lastpass",
	"lastpass",
	"com.dashlane.dashlane",
	"dashlane",
	"com.nordpass.nordpass",
	"nordpass",
	"com.enpass.enpass",
	"enpass",
	"me.proton.pass.electron",
	"protonpass",
	// 系统钥匙串（注意：不要放 com.apple.safari —— Safari 里复制普通文本很常见）
	"com.apple.keychainaccess",
	"com.apple.securityagent",
	"securityagent",
}

// Config 是过滤相关设置（settings.json 的子集）。
type Config struct {
	// SensitiveApps 非空时覆盖内置表。
	SensitiveApps []string
	// IgnoreSensitiveApps 为 true 时不做敏感过滤（默认 true，与旧实现一致）。
	IgnoreSensitiveApps bool
}

// List 是生效的白名单：配置里给了就用它，否则用内置表。
func List(cfg Config) []string {
	if len(cfg.SensitiveApps) > 0 {
		return append([]string(nil), cfg.SensitiveApps...)
	}
	return append([]string(nil), DefaultApps...)
}

// Matches 判断这个应用要不要拦。
//
// 比对规则：都转小写、去掉 .exe，然后
//   - 相等；
//   - 以「名单项.」开头（macOS 的 com.1password.1password.helper 这类子进程）；
//   - 以「.名单项」结尾。
//
// 不做裸 endsWith：notkeepassxc 不该命中 keepassxc。
func Matches(app string, apps []string) bool {
	app = strings.ToLower(strings.TrimSpace(app))
	if app == "" {
		return false
	}
	app = strings.TrimSuffix(app, ".exe")
	for _, candidate := range apps {
		c := strings.ToLower(strings.TrimSpace(candidate))
		if c == "" {
			continue
		}
		if app == c || strings.HasPrefix(app, c+".") || strings.HasSuffix(app, "."+c) {
			return true
		}
	}
	return false
}

// ShouldSkip 按当前设置判断：这个前台应用要不要跳过。
func ShouldSkip(cfg Config, app string) bool {
	if !cfg.IgnoreSensitiveApps {
		return false
	}
	if app == "" {
		return false
	}
	return Matches(app, List(cfg))
}
