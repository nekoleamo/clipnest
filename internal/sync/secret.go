package sync

import (
	"github.com/egoist/mygo"
)

// secretGet 从系统钥匙串取一个口令（macOS Keychain / Windows DPAPI / Linux libsecret）。
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
