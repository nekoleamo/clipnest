package sync

import (
	"runtime"

	"github.com/egoist/mygo"
)

// SecretStoreName 是「口令存放处」的平台说法，只用于给用户看的文案。
//
// macOS 是钥匙串（有按 App 的 ACL，会弹授权）；Windows 是凭据管理器里的通用凭据
// （DPAPI 按用户加密，读不弹框）；Linux 是 libsecret（GNOME Keyring / KWallet）。
func SecretStoreName() string {
	switch runtime.GOOS {
	case "darwin":
		return "钥匙串"
	case "windows":
		return "Windows 凭据管理器"
	default:
		return "系统密钥环"
	}
}

// secretGet 从系统凭据里取一个口令（macOS Keychain / Windows Credential Manager / Linux libsecret）。
func secretGet(service, key string) (string, error) {
	data, err := mygo.Secrets.Service(service).Get(key)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

// SecretSet 写口令进钥匙串。
func SecretSet(service, key, value string) error {
	return mygo.Secrets.Service(service).Set(key, []byte(value))
}

// SecretDelete 删掉钥匙串条目。
func SecretDelete(service, key string) error {
	return mygo.Secrets.Service(service).Delete(key)
}

// SecretGet 读口令（导出给 UI 用）。
func SecretGet(service, key string) (string, error) { return secretGet(service, key) }
