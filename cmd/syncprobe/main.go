// syncprobe 手工排查同步：读真实配置，打印选中的入口并跑一轮，把耗时与报告打出来。
//
//	go run ./cmd/syncprobe
//
// 注意：macOS 上首次读取钥匙串里的口令会弹系统授权框（重打包后签名变了会再弹一次），
// 那个框不点掉，进程就会一直等在那里——看到「卡住不动」先看屏幕上有没有钥匙串弹窗。
package main

import (
	"flag"
	"fmt"
	"log"
	"time"

	"clipnest/internal/paths"
	"clipnest/internal/store"
	clipsync "clipnest/internal/sync"
)

func main() {
	probeOnly := flag.Bool("probe", false, "只探测局域网/Tailscale 选哪个入口，不跑同步（不碰钥匙串）")
	list := flag.Bool("list", false, "列远端文件与大小（诊断用）")
	flag.Parse()

	log.SetFlags(log.Ltime | log.Lmicroseconds)
	p := paths.Platform()

	cfg := clipsync.LoadConfig(p)
	fmt.Printf("配置：%s\n", cfg.Summary())
	fmt.Printf("实时：%v，拉取间隔：%ds\n", cfg.Realtime, cfg.PullIntervalSecs())

	start := time.Now()
	ep, err := clipsync.ResolveEndpoint(cfg.Backend, clipsync.ProbeTimeout)
	fmt.Printf("入口：%s（err=%v，探测用时 %s）\n", ep, err, time.Since(start).Round(time.Millisecond))
	if *probeOnly || err != nil {
		return
	}

	if *list {
		transport, _, err := clipsync.OpenTransport(cfg)
		if err != nil {
			log.Fatalf("建传输失败：%v", err)
		}
		start := time.Now()
		names, err := transport.List()
		fmt.Printf("List 用时 %s，共 %d 个包：%v（err=%v）\n", time.Since(start).Round(time.Millisecond), len(names), names, err)
		for _, name := range names {
			start := time.Now()
			data, err := transport.Get(name)
			fmt.Printf("  %-24s %6d B  %s err=%v\n", name, len(data), time.Since(start).Round(time.Millisecond), err)
		}
		return
	}

	st, err := store.Open(p)
	if err != nil {
		log.Fatalf("打开数据库失败：%v", err)
	}
	defer st.Close()
	clipsync.Logf = log.Printf

	start = time.Now()
	report, err := clipsync.RunOnce(st.DB, cfg, p)
	fmt.Printf("耗时=%s 报告=%+v\n错误=%v\n", time.Since(start).Round(time.Millisecond), report, err)
}
