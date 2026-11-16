// Package sync 是端到端加密的增量同步。
//
// 设计：
//   - 载荷是增量包：自上次导出以来变更的条目 + 删除墓碑
//   - 合并键是 content_hash（契约 1），重复合并结果不变（幂等）
//   - use_count 取两边的最大值：既不会因为多次拉取而膨胀，也不会丢使用次数
//   - 冲突一律「updated_at 更新的赢」；删除用墓碑，墓碑时间晚于条目更新就删本地
//   - 分组按名字对齐（id 是各自的自增值，不能照搬）
package sync

import (
	"encoding/json"
	"errors"
	"os"
	"strings"

	"clipnest/internal/paths"
)

// ConfigVersion 是配置格式版本。
const ConfigVersion = 1

// 钥匙串里存口令的条目名。
const (
	SecretService       = "clipnest-sync"
	WebDavSecretService = "clipnest-webdav"
)

// BackendKind 是同步后端类型。
type BackendKind string

// 同步方式。
const (
	BackendOff      BackendKind = "off"
	BackendWebDav   BackendKind = "webdav"
	BackendLocalDir BackendKind = "local_dir"
)

// Backend 是同步后端（对应旧的 internally-tagged 枚举）。
//
// WebDAV 支持两个地址：URL 是局域网（快），FallbackURL 是 Tailscale / 外网。
// 每轮同步前先探局域网，不通再走备用（探测结果缓存 5 分钟）。
type Backend struct {
	Kind        BackendKind `json:"kind"`
	URL         string      `json:"url,omitempty"`
	FallbackURL string      `json:"fallback_url,omitempty"`
	Username    string      `json:"username,omitempty"`
	RemoteDir   string      `json:"remote_dir,omitempty"`
	InsecureTLS bool        `json:"insecure_tls,omitempty"`
	Path        string      `json:"path,omitempty"`
}

// Encryption 是端到端加密开关。
type Encryption struct {
	Enabled bool `json:"enabled"`
}

// Config 是 sync.json。
type Config struct {
	Version    int        `json:"version"`
	Enabled    bool       `json:"enabled"`
	DeviceName string     `json:"device_name"`
	Backend    Backend    `json:"backend"`
	Encryption Encryption `json:"encryption"`
	// IntervalSecs 是普通模式的定时同步间隔（<60 不自动跑）。
	IntervalSecs int64 `json:"interval_secs"`
	// Realtime 打开时：采集后 1.2 秒推到远端，并按 PullSecs 短间隔拉取。
	Realtime bool `json:"realtime"`
	// PullSecs 是实时模式下两轮拉取的间隔（秒），默认 5。
	PullSecs int64 `json:"pull_secs"`
}

// PullInterval 是实际使用的实时拉取间隔（夹紧到 2–300 秒）。
const PullDefaultSecs = 5

func (c Config) PullIntervalSecs() int64 {
	if c.PullSecs < 2 {
		return PullDefaultSecs
	}
	if c.PullSecs > 300 {
		return 300
	}
	return c.PullSecs
}

// DefaultConfig 是默认配置（关掉、开加密）。
func DefaultConfig() Config {
	return Config{
		Version:      ConfigVersion,
		Enabled:      false,
		DeviceName:   defaultDeviceName(),
		Backend:      Backend{Kind: BackendOff},
		Encryption:   Encryption{Enabled: true},
		IntervalSecs: 1800,
		Realtime:     true,
		PullSecs:     PullDefaultSecs,
	}
}

// Summary 是给 UI 用的一行摘要。
func (c Config) Summary() string {
	switch c.Backend.Kind {
	case BackendOff:
		return "未启用"
	case BackendWebDav:
		s := "WebDAV " + c.Backend.URL + c.Backend.RemoteDir
		if c.Backend.FallbackURL != "" {
			s += "（备用 " + c.Backend.FallbackURL + "）"
		}
		return s
	case BackendLocalDir:
		return "本地目录 " + c.Backend.Path
	default:
		return string(c.Backend.Kind)
	}
}

// Normalize 规范化：版本号、设备名、空字段兜底。
func (c Config) Normalize() Config {
	c.Version = ConfigVersion
	c.DeviceName = sanitizeDeviceName(c.DeviceName)
	if c.DeviceName == "" {
		c.DeviceName = defaultDeviceName()
	}
	switch c.Backend.Kind {
	case BackendWebDav:
		c.Backend.URL = strings.TrimRight(strings.TrimSpace(c.Backend.URL), "/")
		c.Backend.FallbackURL = strings.TrimRight(strings.TrimSpace(c.Backend.FallbackURL), "/")
		if c.Backend.FallbackURL == c.Backend.URL {
			c.Backend.FallbackURL = "" // 两个写一样就不算备用
		}
		dir := strings.TrimSpace(c.Backend.RemoteDir)
		switch {
		case dir == "":
			c.Backend.RemoteDir = "/clipnest"
		case strings.HasPrefix(dir, "/"):
			c.Backend.RemoteDir = dir
		default:
			c.Backend.RemoteDir = "/" + dir
		}
	case BackendLocalDir:
		c.Backend.Path = strings.TrimSpace(c.Backend.Path)
	case BackendOff:
	default:
		c.Backend.Kind = BackendOff
	}
	return c
}

// Ready 判断配置填完整没（能建后端才会去同步）。
func (c Config) Ready() error {
	if !c.Enabled {
		return errors.New("同步未启用")
	}
	switch c.Backend.Kind {
	case BackendOff:
		return errors.New("没有选择同步方式")
	case BackendWebDav:
		if c.Backend.URL == "" {
			return errors.New("还没填 WebDAV 地址")
		}
		if c.Backend.Username == "" {
			return errors.New("还没填 WebDAV 用户名")
		}
	case BackendLocalDir:
		if c.Backend.Path == "" {
			return errors.New("还没填本地目录")
		}
	}
	return nil
}

// LoadConfig 读配置；坏了就给默认值并记日志，别让应用起不来。
func LoadConfig(p paths.Paths) Config {
	data, err := os.ReadFile(p.SyncConfig())
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			Logf("SYNC 配置读取失败（用默认值）：%v", err)
		}
		return DefaultConfig()
	}
	c := DefaultConfig()
	if err := json.Unmarshal(data, &c); err != nil {
		Logf("SYNC 配置解析失败（用默认值）：%v", err)
		return DefaultConfig()
	}
	return c.Normalize()
}

// SaveConfig 原子写。
func SaveConfig(c Config, p paths.Paths) error {
	c = c.Normalize()
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(p.Root(), 0o755); err != nil {
		return err
	}
	tmp := p.SyncConfig() + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, p.SyncConfig())
}

// sanitizeDeviceName 去掉不能当文件名的字符。
func sanitizeDeviceName(name string) string {
	var b strings.Builder
	for _, c := range strings.TrimSpace(name) {
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '-' || c == '_' {
			b.WriteRune(c)
		} else {
			b.WriteByte('-')
		}
	}
	return b.String()
}

// defaultDeviceName 取主机名里能当文件名的部分。
func defaultDeviceName() string {
	host := os.Getenv("COMPUTERNAME")
	if host == "" {
		host = os.Getenv("HOSTNAME")
	}
	if host == "" {
		if h, err := os.Hostname(); err == nil {
			host = h
		}
	}
	if host == "" {
		host = "device"
	}
	name := sanitizeDeviceName(host)
	if len(name) > 32 {
		name = name[:32]
	}
	return name
}

// Logf 是日志出口，宿主可替换（默认丢弃，测试不吵）。
var Logf = func(format string, args ...any) {}
