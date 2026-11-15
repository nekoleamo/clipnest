// Package clip 是剪贴板采集层：读一次快照、翻译成入库内容、自身写回抑制。
//
// 与旧 Rust 实现（legacy-rust/src/clipboard.rs）的对应：
//   - 轮询 changeCount 换成 mygo 的 Clipboard.OnChanged（macOS 内部 250ms 轮询）；
//   - flavor 判定换成 Formats()；
//   - 回环抑制（契约 10）从「比对 changeCount」换成「比对内容哈希」——更可靠，
//     也不依赖框架暴露版本号。
package clip

import (
	"strings"
	"sync"

	"clipnest/internal/model"
	"clipnest/internal/store"
)

// Flavor 名按 mygo transfer.Format 的取值，图片一律当 PNG 处理（框架负责 TIFF/DIB 转换）。
const (
	FormatText = "text/plain"
	FormatHTML = "text/html"
	FormatRTF  = "text/rtf"
	FormatPNG  = "image/png"
	FormatFile = "application/x-mygo-file-list"
)

// Source 是剪贴板读取抽象；真实现是 MygoSource，测试用假实现。
type Source interface {
	// Has 判断某个 flavor 是否存在。
	Has(format string) bool
	// Bytes 读某个 flavor 的原始字节。
	Bytes(format string) ([]byte, bool)
	// Files 读文件列表（绝对路径）。
	Files() ([]string, bool)
}

// Snapshot 是一次采集的结果。
type Snapshot struct {
	// Flavors 是 flavor 名单，如 text+html（日志用）。
	Flavors string
	// Excerpt 是摘要（压平换行、按字符截断），用于日志。
	Excerpt string
	Text    string
	HTML    string
	RTF     string
	Files   []string
	Image   *store.ImageBytes
}

// Capture 读一次剪贴板，翻译成要入库的内容与快照。
//
// 优先级与旧实现一致：files > image > html > rtf > text 只决定摘要来源；
// 真正落库时 store.Record 让图片优先（契约：图片优先于文本）。
func Capture(src Source) (store.Captured, Snapshot, bool) {
	var (
		snap    Snapshot
		flavors []string
	)

	if files, ok := src.Files(); ok && len(files) > 0 {
		flavors = append(flavors, "files")
		snap.Files = files
		snap.Excerpt = strings.Join(files, "|")
	}
	if src.Has(FormatPNG) {
		flavors = append(flavors, "image")
		// 编码成 PNG 才能落盘与算内容哈希；解不出尺寸就只记 flavor 不落库。
		if png, ok := src.Bytes(FormatPNG); ok && len(png) > 0 {
			if w, h, err := imageSize(png); err == nil {
				snap.Image = &store.ImageBytes{PNG: png, Width: w, Height: h}
				if snap.Excerpt == "" {
					snap.Excerpt = "<图片 " + itoa(w) + "×" + itoa(h) + ">"
				}
			}
		}
	}
	if src.Has(FormatHTML) {
		flavors = append(flavors, "html")
		if b, ok := src.Bytes(FormatHTML); ok {
			snap.HTML = string(b)
			if snap.Excerpt == "" {
				snap.Excerpt = snap.HTML
			}
		}
	}
	if src.Has(FormatRTF) {
		flavors = append(flavors, "rtf")
		if b, ok := src.Bytes(FormatRTF); ok {
			snap.RTF = string(b)
			if snap.Excerpt == "" {
				snap.Excerpt = snap.RTF
			}
		}
	}
	if src.Has(FormatText) {
		flavors = append(flavors, "text")
		if b, ok := src.Bytes(FormatText); ok {
			snap.Text = string(b)
			if snap.Excerpt == "" {
				snap.Excerpt = snap.Text
			}
		}
	}

	if len(flavors) == 0 {
		return store.Captured{}, Snapshot{}, false
	}
	snap.Flavors = strings.Join(flavors, "+")
	snap.Excerpt = truncateRunes(strings.ReplaceAll(snap.Excerpt, "\n", `\n`), 60)

	cap := store.Captured{Text: snap.Text, Files: snap.Files, Image: snap.Image}
	return cap, snap, true
}

// Hashes 算这次采集会产生的条目哈希（回环抑制用）。
func Hashes(cap store.Captured) []string {
	var out []string
	if cap.Image != nil {
		name := store.ImageFileName(model.HashBytes(cap.Image.PNG))
		out = append(out, model.ContentHash(model.KindImage, name))
	}
	if len(cap.Files) > 0 {
		out = append(out, model.ContentHash(model.KindFiles, strings.Join(cap.Files, "\n")))
	}
	if cap.Text != "" {
		out = append(out, model.ContentHash(model.KindText, cap.Text))
	}
	return out
}

// Guard 是自身写回抑制（契约 10）。
//
// 写回前登记「这次会产生的哈希」，随后 OnChanged 见到其中一个即认为变化来自自己，跳过入库。
// 比对内容哈希而不是剪贴板版本号：框架不暴露版本号，而内容哈希本身就是天然标识。
type Guard struct {
	mu      sync.Mutex
	pending map[string]struct{}
}

// NewGuard 建一个空的抑制器。
func NewGuard() *Guard { return &Guard{pending: make(map[string]struct{})} }

// Expect 登记一次自身写回。
func (g *Guard) Expect(hashes ...string) {
	if len(hashes) == 0 {
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	for _, h := range hashes {
		g.pending[h] = struct{}{}
	}
}

// Consume 判断这组哈希里是否有自己刚写回的；命中则清理登记并返回 true。
func (g *Guard) Consume(hashes ...string) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	for _, h := range hashes {
		if _, ok := g.pending[h]; ok {
			delete(g.pending, h)
			return true
		}
	}
	return false
}

// Pending 是还挂着的登记数（诊断用）。
func (g *Guard) Pending() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return len(g.pending)
}

func truncateRunes(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max])
}

func itoa(v int64) string {
	if v == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for v > 0 {
		i--
		buf[i] = byte('0' + v%10)
		v /= 10
	}
	return string(buf[i:])
}
