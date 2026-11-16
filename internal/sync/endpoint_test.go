package sync

import (
	"net"
	"strings"
	"testing"
	"time"
)

// live 起一个只监听的端口，返回 URL 与关闭函数（端口已占用 => 探测能连上）。
func live(t *testing.T) (string, func()) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("监听失败：%v", err)
	}
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			_ = conn.Close()
		}
	}()
	return "https://" + ln.Addr().String(), func() { _ = ln.Close() }
}

// dead 拿一个已经释放的端口，探测必然失败。
func dead(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("监听失败：%v", err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()
	return "https://" + addr
}

func TestHostPortFillsDefaultPort(t *testing.T) {
	cases := map[string]string{
		"https://192.168.1.2":            "192.168.1.2:443",
		"http://nas.local":               "nas.local:80",
		"https://nas.tail.ts.net:5006":   "nas.tail.ts.net:5006",
		"https://nas.tail.ts.net:5006/x": "nas.tail.ts.net:5006",
	}
	for in, want := range cases {
		got, err := hostPort(in)
		if err != nil || got != want {
			t.Fatalf("hostPort(%q)=%q err=%v，想要 %q", in, got, err, want)
		}
	}
	if _, err := hostPort("ftp://x"); err == nil {
		t.Fatal("不认识的协议应当报错")
	}
}

func TestResolveEndpointPrefersLAN(t *testing.T) {
	ResetEndpointCache()
	lan, closeLAN := live(t)
	defer closeLAN()
	b := Backend{Kind: BackendWebDav, URL: lan, FallbackURL: dead(t)}

	ep, err := ResolveEndpoint(b, ProbeTimeout)
	if err != nil {
		t.Fatalf("解析失败：%v", err)
	}
	if ep.Label != EndpointLAN || ep.URL != lan {
		t.Fatalf("应当选局域网，实际 %+v", ep)
	}
}

func TestResolveEndpointFallsBackToTailscale(t *testing.T) {
	ResetEndpointCache()
	ts, closeTS := live(t)
	defer closeTS()
	b := Backend{Kind: BackendWebDav, URL: dead(t), FallbackURL: ts}

	ep, err := ResolveEndpoint(b, ProbeTimeout)
	if err != nil {
		t.Fatalf("解析失败：%v", err)
	}
	if ep.Label != EndpointTailnet || ep.URL != ts {
		t.Fatalf("应当选 Tailscale，实际 %+v", ep)
	}
}

func TestResolveEndpointBothDown(t *testing.T) {
	ResetEndpointCache()
	b := Backend{Kind: BackendWebDav, URL: dead(t), FallbackURL: dead(t)}
	if _, err := ResolveEndpoint(b, 200*time.Millisecond); err == nil {
		t.Fatal("两边都不通应当报错")
	} else if !strings.Contains(err.Error(), "都连不上") {
		t.Fatalf("错误文案不对：%v", err)
	}
}

func TestResolveEndpointSingleURLNeverProbes(t *testing.T) {
	ResetEndpointCache()
	url := dead(t) // 不通也直接用它，让同步自己去报错
	ep, err := ResolveEndpoint(Backend{Kind: BackendWebDav, URL: url}, ProbeTimeout)
	if err != nil {
		t.Fatalf("单地址不该报错：%v", err)
	}
	if ep.URL != url || ep.Label != EndpointLAN {
		t.Fatalf("单地址应当原样返回，实际 %+v", ep)
	}
}

func TestEndpointCacheSkipsReprobe(t *testing.T) {
	ResetEndpointCache()
	lan, closeLAN := live(t)
	b := Backend{Kind: BackendWebDav, URL: lan, FallbackURL: dead(t)}
	if _, err := ResolveEndpoint(b, ProbeTimeout); err != nil {
		t.Fatalf("首次解析失败：%v", err)
	}
	closeLAN() // 局域网断了，但 5 分钟内缓存还说它通
	ep, err := ResolveEndpoint(b, ProbeTimeout)
	if err != nil || ep.URL != lan {
		t.Fatalf("应当命中缓存，实际 %+v err=%v", ep, err)
	}
	ResetEndpointCache()
	if _, err := ResolveEndpoint(b, 200*time.Millisecond); err == nil {
		t.Fatal("清缓存后应当重新探测并失败")
	}
}

func TestConfigNormalizesFallback(t *testing.T) {
	c := Config{Enabled: true, Backend: Backend{
		Kind:        BackendWebDav,
		URL:         " https://192.168.1.2:5006/ ",
		FallbackURL: " https://nas.tail.ts.net:5006/ ",
		Username:    "u",
	}}.Normalize()
	if c.Backend.URL != "https://192.168.1.2:5006" {
		t.Fatalf("主地址未修剪：%q", c.Backend.URL)
	}
	if c.Backend.FallbackURL != "https://nas.tail.ts.net:5006" {
		t.Fatalf("备用地址未修剪：%q", c.Backend.FallbackURL)
	}

	// 两个地址写成一样时没有「切换」可言，清掉备用
	same := Config{Enabled: true, Backend: Backend{
		Kind: BackendWebDav, URL: "https://a:1", FallbackURL: "https://a:1/", Username: "u",
	}}.Normalize()
	if same.Backend.FallbackURL != "" {
		t.Fatalf("重复地址应当清空，实际 %q", same.Backend.FallbackURL)
	}
}

func TestPullIntervalClamps(t *testing.T) {
	if got := (Config{}).PullIntervalSecs(); got != PullDefaultSecs {
		t.Fatalf("默认拉取间隔应为 %d，实际 %d", PullDefaultSecs, got)
	}
	if got := (Config{PullSecs: 1}).PullIntervalSecs(); got != PullDefaultSecs {
		t.Fatalf("小于 2 秒应回落到默认值，实际 %d", got)
	}
	if got := (Config{PullSecs: 30}).PullIntervalSecs(); got != 30 {
		t.Fatalf("应当保留 30，实际 %d", got)
	}
	if got := (Config{PullSecs: 9999}).PullIntervalSecs(); got != 300 {
		t.Fatalf("应当夹到 300，实际 %d", got)
	}
}

func TestSummaryMentionsFallback(t *testing.T) {
	c := Config{Backend: Backend{
		Kind: BackendWebDav, URL: "https://lan:5006", FallbackURL: "https://ts:5006", RemoteDir: "/d",
	}}
	if s := c.Summary(); !strings.Contains(s, "备用") || !strings.Contains(s, "ts:5006") {
		t.Fatalf("摘要里应带备用地址：%q", s)
	}
}
