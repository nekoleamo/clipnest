// clipnest：跨平台剪贴板历史管理器（Go + mygo）。
package main

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"io"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"

	"clipnest/internal/app"
	"clipnest/internal/clip"
	"clipnest/internal/logfile"
	"clipnest/internal/paste"
	"clipnest/internal/paths"
	"clipnest/internal/settings"
	"clipnest/internal/store"
	clipsync "clipnest/internal/sync"
)

func main() {
	log.SetFlags(log.Ltime | log.Lmicroseconds)

	p := paths.Platform()
	a, err := app.New(p)
	if err != nil {
		log.Fatalf("打开数据库失败（%s）：%v", p.DB(), err)
	}
	defer a.Store.Close()

	// 日志出口：stdout + 数据目录里的 clipnest.log（LaunchServices 启动时 stdout 去不了任何地方）
	setupLogFile(p)
	store.Logf = log.Printf
	settings.Logf = log.Printf
	clipsync.Logf = log.Printf
	clipsync.SetThumbnailWriter(store.WriteThumbnail)

	// 单实例：第二个实例只把参数转交给第一个，然后退出（否则会出现两个托盘图标）
	if !mygo.App.RequestSingleInstanceLock() {
		return
	}

	// 常驻应用：没有 Dock 图标，也没有菜单栏（macOS accessory）
	mygo.App.SetActivationPolicy(mygo.ActivationPolicyAccessory)
	mygo.App.OnWindowAllClosed(func() {}) // 关掉最后一个窗口不退出
	// 退出流程要先把「正在退出」标上：面板的 OnClose 靠它决定要不要 PreventDefault
	mygo.App.OnBeforeQuit(func(e *mygo.QuitEvent) { a.MarkQuitting() })

	mygo.App.WhenReady(func() {
		// 面板：NSPanel 非激活样式，抢键盘但不抢前台
		panel := mygo.NewWindow(mygo.WindowOptions{
			Panel:       true,
			Hidden:      true,
			Frameless:   true,
			Transparent: true,
			Vibrancy:    mygo.VibrancyHUD,
			Width:       settings.PanelWidth,
			Height:      int(a.Settings.PanelHeight),
			// 缩到比这更小就排不开了（列表 + 右栏预览 + 底栏）
			MinWidth:  settings.PanelWidth,
			MinHeight: 300,
			Content:   ui.View(a.PanelView),
		})
		// 关闭（Esc / ⌘W）当隐藏，下次唤起不用重建
		panel.OnClose(func(e *mygo.CloseEvent) {
			if a.Quitting() {
				return // 退出流程里要真的关掉，否则 Quit 会被取消
			}
			e.PreventDefault()
			panel.Hide()
		})
		a.SetPanel(panel)

		// 设置窗
		prefs := mygo.NewWindow(mygo.WindowOptions{
			Title:     "clipnest 设置",
			Width:     620,
			Height:    720,
			MinWidth:  520,
			MinHeight: 420,
			Hidden:    true,
			Content:   ui.View(a.PrefsView),
		})
		prefs.OnClose(func(e *mygo.CloseEvent) {
			if a.Quitting() {
				return // 退出流程里要真的关掉
			}
			e.PreventDefault()
			prefs.Hide() // 关掉设置窗只是隐藏，下次还能打开
		})
		a.SetPrefsWindow(prefs)

		// 大图预览窗口：点右栏图片时显示（窗口只在启动阶段建，帧回调里建不安全）
		preview := mygo.NewWindow(mygo.WindowOptions{
			Title:     "clipnest 预览",
			Width:     600,
			Height:    480,
			MinWidth:  200,
			MinHeight: 160,
			Hidden:    true,
			Content:   ui.View(a.DetailView),
		})
		preview.OnClose(func(e *mygo.CloseEvent) {
			if a.Quitting() {
				return // 退出流程里要真的关掉，否则 Quit 会被取消
			}
			e.PreventDefault()
			preview.Hide()
		})
		a.SetImageWindow(preview)

		// 托盘
		if _, err := a.AttachTray(trayIcon()); err != nil {
			log.Printf("托盘创建失败：%v", err)
		}

		// 全局快捷键：唤出/收起面板（用户可在设置里改）
		log.Printf("SHORTCUT register %s", a.Settings.Shortcut)
		if err := mygo.GlobalShortcut.Register(a.Settings.Shortcut, a.TogglePanel); err != nil {
			log.Printf("快捷键 %s 注册失败（可能被占用），回退默认：%v", a.Settings.Shortcut, err)
			if err := mygo.GlobalShortcut.Register(settings.DefaultShortcut, a.TogglePanel); err != nil {
				log.Printf("默认快捷键也注册不了：%v", err)
			} else {
				log.Printf("SHORTCUT fell back to %s", settings.DefaultShortcut)
			}
		}

		// 窗口都建好了：之后才允许 Invalidate（之前调用会等主线程而死锁）
		a.SetReady()

		// 剪贴板变化 → 采集落库
		clip.Watch(a.Capture)

		// 启动清理：孤儿图片与超限历史
		if n, err := a.Store.CollectGarbage(); err == nil && n > 0 {
			log.Printf("启动清理孤儿图片 %d 个", n)
		}
		a.Prune()
		a.Refresh()

		// 定时同步
		a.StartAutoSync()

		// 第二个实例被唤起时（用户又点了图标）把面板亮出来
		mygo.App.OnSecondInstance(func(_ []string, _ string) { a.Show() })

		// 自检钩子：`open -a clipnest.app --args -prefs` 启动就打开设置窗
		if hasArg("-prefs") {
			time.AfterFunc(1500*time.Millisecond, func() { a.OpenPrefs() })
		}

		// 自检钩子：`open -a clipnest.app --args -ax-prompt` 直接弹辅助功能授权提示
		// （tccutil reset 之后重新登记用，不必拿鼠标去点面板里的按钮）
		if hasArg("-ax-prompt") {
			go func() {
				log.Printf("AXP exec prompt trusted=%v", paste.PromptTrust())
			}()
		}

		// 自检钩子：CLIPNEST_AUTOLOGIN=1 注册登录项，=0 取消（macOS 走 SMAppService）
		switch os.Getenv("CLIPNEST_AUTOLOGIN") {
		case "1", "true":
			if err := mygo.App.SetOpenAtLogin(true); err != nil {
				log.Printf("AUTOLOGIN on err=%v", err)
			} else {
				log.Printf("AUTOLOGIN on now=%v", mygo.App.OpenAtLogin())
			}
		case "0", "false":
			if err := mygo.App.SetOpenAtLogin(false); err != nil {
				log.Printf("AUTOLOGIN off err=%v", err)
			} else {
				log.Printf("AUTOLOGIN off now=%v", mygo.App.OpenAtLogin())
			}
		}

		// 自检钩子：CLIPNEST_AUTOOPEN=1 启动 2 秒后自动弹面板；CLIPNEST_AUTOQUIT=<秒> 到点退出
		if os.Getenv("CLIPNEST_AUTOOPEN") != "" {
			time.AfterFunc(2*time.Second, func() {
				log.Printf("AUTOOPEN fire")
				a.Show()
				log.Printf("AUTOOPEN done")
			})
		}
		if secs := os.Getenv("CLIPNEST_AUTOQUIT"); secs != "" {
			if n, err := strconv.Atoi(secs); err == nil && n > 0 {
				time.AfterFunc(time.Duration(n)*time.Second, func() {
					log.Printf("AUTOQUIT fire")
					mygo.App.Quit()
					log.Printf("AUTOQUIT done")
				})
			}
		}

		total, _ := a.Store.Count()
		log.Printf("BOOT 数据=%s 历史=%d 登录项=%v 设置=%+v", p.Root(), total, mygo.App.OpenAtLogin(), a.Settings)
	})

	log.Printf("ENTER App.Run")
	if err := mygo.App.Run(); err != nil {
		log.Fatal(err)
	}
	log.Printf("EXIT App.Run")
}

// hasArg 判断命令行里有没有某个参数（-ax-prompt 这类自检钩子用）。
func hasArg(want string) bool {
	for _, a := range os.Args[1:] {
		if a == want {
			return true
		}
	}
	return false
}

// setupLogFile 把日志同时写到 <data>/clipnest.log（超过 2 MB 就轮转一份 .1）。
// setupLogFile 把日志写到 stderr + <数据根>/clipnest.log，超过 2MB 自动轮转成 .1。
//
// 常驻进程一跑好几天，轮转必须发生在运行期（logfile.Writer），不能只在启动查一次。
func setupLogFile(p paths.Paths) {
	w, err := logfile.Open(filepath.Join(p.Root(), "clipnest.log"), 2<<20)
	if err != nil {
		return
	}
	log.SetOutput(io.MultiWriter(os.Stderr, w))
}

// trayIcon 生成 32×32 的模板图标（黑+透明，macOS 会按菜单栏配色染）。
func trayIcon() []byte {
	img := image.NewRGBA(image.Rect(0, 0, 32, 32))
	black := color.RGBA{0, 0, 0, 255}
	// 剪贴板外形：一块板 + 顶部夹子
	for y := 8; y < 27; y++ {
		for x := 7; x < 25; x++ {
			img.SetRGBA(x, y, black)
		}
	}
	// 挖出中间
	for y := 12; y < 23; y++ {
		for x := 11; x < 21; x++ {
			img.SetRGBA(x, y, color.RGBA{0, 0, 0, 0})
		}
	}
	// 夹子
	for y := 4; y < 8; y++ {
		for x := 12; x < 20; x++ {
			img.SetRGBA(x, y, black)
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return nil
	}
	return buf.Bytes()
}
