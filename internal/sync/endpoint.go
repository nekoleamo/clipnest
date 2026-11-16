// 端点选择：局域网优先，不通就走 Tailscale 备用地址。
//
// NAS 同时有局域网地址和 Tailscale 地址（MagicDNS 名或 100.x IP）。出门在外局域网
// 不通，直连会卡满一个 HTTP 超时；这里先做 1 秒级 TCP 探测，探到谁用谁，结果缓存
// 5 分钟，避免每轮都多花探测时间。
package sync

import (
	"fmt"
	"net"
	"net/url"
	"strings"
	"sync"
	"time"
)

// 探测与缓存参数。
const (
	ProbeTimeout    = 1200 * time.Millisecond
	endpointTTL     = 5 * time.Minute
	EndpointLAN     = "局域网"
	EndpointTailnet = "Tailscale"
)

// Endpoint 是选中的同步入口。
type Endpoint struct {
	Label string // 局域网 / Tailscale / 本地目录
	URL   string
}

// String 给日志与状态行用。
func (e Endpoint) String() string { return e.Label + " " + e.URL }

// endpoints 把后端配置里的地址展开成候选列表（主地址在前）。
func endpoints(b Backend) []Endpoint {
	out := make([]Endpoint, 0, 2)
	if b.URL != "" {
		out = append(out, Endpoint{Label: EndpointLAN, URL: b.URL})
	}
	if b.FallbackURL != "" {
		out = append(out, Endpoint{Label: EndpointTailnet, URL: b.FallbackURL})
	}
	return out
}

// hostPort 从 URL 取可拨号地址（补默认端口）。
func hostPort(raw string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return "", err
	}
	host := u.Hostname()
	if host == "" {
		return "", fmt.Errorf("地址里没有主机名：%s", raw)
	}
	if port := u.Port(); port != "" {
		return net.JoinHostPort(host, port), nil
	}
	switch u.Scheme {
	case "https":
		return net.JoinHostPort(host, "443"), nil
	case "http":
		return net.JoinHostPort(host, "80"), nil
	}
	return "", fmt.Errorf("认不出协议：%s", raw)
}

// Reachable 用 TCP 拨一次，判断地址是否活着（1.2 秒超时）。
//
// 不做 WebDAV 认证：端口能连上就说明这条链路通，认证由真正的同步去谈。
func Reachable(raw string, timeout time.Duration) bool {
	addr, err := hostPort(raw)
	if err != nil {
		return false
	}
	conn, err := net.DialTimeout("tcp", addr, timeout)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

// 端点缓存：同一组候选地址 5 分钟内不重复探测。
var (
	endpointMu    sync.Mutex
	endpointCache = map[string]cachedEndpoint{}
)

type cachedEndpoint struct {
	ep    Endpoint
	until time.Time
}

// ResetEndpointCache 清掉缓存（测试与「手动立即同步」用）。
func ResetEndpointCache() {
	endpointMu.Lock()
	endpointCache = map[string]cachedEndpoint{}
	endpointMu.Unlock()
}

// ResolveEndpoint 选一个可用入口：缓存的优先，其次主地址，最后备用地址。
//
// 全都不通时返回错误，错误里带两个地址，方便直接贴进设置窗排查。
func ResolveEndpoint(b Backend, probe time.Duration) (Endpoint, error) {
	candidates := endpoints(b)
	if len(candidates) == 0 {
		return Endpoint{}, fmt.Errorf("没有填同步地址")
	}
	if len(candidates) == 1 {
		// 只有一个地址就没有「切换」可言，直接用它（失败让同步自己报错）。
		return candidates[0], nil
	}
	key := candidates[0].URL + "\x00" + candidates[1].URL

	endpointMu.Lock()
	if c, ok := endpointCache[key]; ok && time.Now().Before(c.until) {
		endpointMu.Unlock()
		return c.ep, nil
	}
	endpointMu.Unlock()

	for _, cand := range candidates {
		if Reachable(cand.URL, probe) {
			endpointMu.Lock()
			endpointCache[key] = cachedEndpoint{ep: cand, until: time.Now().Add(endpointTTL)}
			endpointMu.Unlock()
			return cand, nil
		}
		Logf("SYNC endpoint down: %s", cand.URL)
	}
	return Endpoint{}, fmt.Errorf("%s 和 %s 都连不上（局域网与 Tailscale 都不通）",
		candidates[0].URL, candidates[1].URL)
}

// ForgetEndpoint 在同步失败时清掉缓存，下一轮重新探测。
func ForgetEndpoint(b Backend) {
	candidates := endpoints(b)
	if len(candidates) < 2 {
		return
	}
	key := candidates[0].URL + "\x00" + candidates[1].URL
	endpointMu.Lock()
	delete(endpointCache, key)
	endpointMu.Unlock()
}
