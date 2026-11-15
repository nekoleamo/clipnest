package clip

import (
	"fmt"
	"os"
	"strings"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/transfer"

	"clipnest/internal/model"
	"clipnest/internal/paths"
)

// MygoSource 是 Source 的框架实现。
type MygoSource struct{}

// Has 判断某个 flavor 是否存在。
func (MygoSource) Has(format string) bool {
	for _, f := range mygo.Clipboard.Formats() {
		if string(f) == format {
			return true
		}
	}
	return false
}

// Bytes 读某个 flavor 的原始字节；不存在或读失败返回 false。
func (MygoSource) Bytes(format string) ([]byte, bool) {
	b, err := mygo.Clipboard.ReadFormat(transfer.Format(format))
	if err != nil || len(b) == 0 {
		return nil, false
	}
	return b, true
}

// Files 读文件列表。
func (MygoSource) Files() ([]string, bool) {
	files, err := mygo.Clipboard.ReadFiles()
	if err != nil || len(files) == 0 {
		return nil, false
	}
	return files, true
}

// Watch 注册剪贴板变化回调；返回的 off 取消监听。
//
// 框架在 macOS 上每 250ms 查一次 changeCount，所以回调不是「复制瞬间」触发的。
func Watch(fn func()) func() { return mygo.Clipboard.OnChanged(fn) }

// WriteBack 把一条历史写回剪贴板，并返回它登记给 Guard 的哈希。
//
// 图片条目读 images/origin/<hh>/<hash>.png；文件没了要给清楚的错，而不是 panic。
func WriteBack(item model.Item, p paths.Paths) ([]string, error) {
	switch item.Kind {
	case model.KindText:
		mygo.Clipboard.WriteText(item.Content)
	case model.KindHtml:
		mygo.Clipboard.WriteHTML(item.Content)
	case model.KindRtf:
		// RTF 走自定义 MIME + 纯文本兜底（旧实现同样保证有 plain 文本）
		if err := mygo.Clipboard.Write(transfer.New(transfer.NewItem(
			transfer.Bytes(transfer.Format(FormatRTF), []byte(item.Content)),
			transfer.Bytes(transfer.Text, []byte(stripRTF(item.Content))),
		))); err != nil {
			return nil, fmt.Errorf("写回 RTF 失败: %w", err)
		}
	case model.KindImage:
		hash := strings.TrimSuffix(item.Content, ".png")
		path := p.ImageOrigin(hash)
		png, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("图片文件不见了：%s", path)
		}
		if err := mygo.Clipboard.WriteImage(png); err != nil {
			return nil, fmt.Errorf("写回图片失败: %w", err)
		}
	case model.KindFiles:
		var files []string
		for _, line := range strings.Split(item.Content, "\n") {
			if line != "" {
				files = append(files, line)
			}
		}
		if len(files) == 0 {
			return nil, fmt.Errorf("文件条目没有路径")
		}
		if err := mygo.Clipboard.WriteFiles(files...); err != nil {
			return nil, fmt.Errorf("写回文件失败: %w", err)
		}
	default:
		return nil, fmt.Errorf("未知的条目类型：%s", item.Kind)
	}

	return []string{model.ContentHash(item.Kind, item.Content)}, nil
}

// PutFiles 把一批文件放到剪贴板（导出后调用：⌘V 到 Finder/微信就是粘文件）。
func PutFiles(paths []string) ([]string, error) {
	if len(paths) == 0 {
		return nil, fmt.Errorf("没有文件可放")
	}
	if err := mygo.Clipboard.WriteFiles(paths...); err != nil {
		return nil, fmt.Errorf("放文件失败: %w", err)
	}
	return []string{model.ContentHash(model.KindFiles, strings.Join(paths, "\n"))}, nil
}

// stripRTF 是极简的 RTF 去壳，只为给 plain 文本一个合理兜底。
func stripRTF(s string) string {
	var b strings.Builder
	depth := 0
	for i := 0; i < len(s); i++ {
		switch c := s[i]; {
		case c == '{':
			depth++
		case c == '}':
			if depth > 0 {
				depth--
			}
		case c == '\\':
			j := i + 1
			if j < len(s) && (s[j] == '\\' || s[j] == '{' || s[j] == '}') {
				i = j // \\{ 之类：原样吞掉
				continue
			}
			if j < len(s) && s[j] == '\'' {
				// \'hh 十六进制字符
				i += 3
				continue
			}
			word := ""
			for j < len(s) && (isAlpha(s[j]) || (s[j] >= '0' && s[j] <= '9')) {
				word += string(s[j])
				j++
			}
			if word == "par" || word == "line" || word == "tab" {
				b.WriteByte(' ')
			}
			if j < len(s) && s[j] == ' ' {
				j++ // 控制词后的分隔空格不属于正文
			}
			i = j - 1
		case depth > 0:
			b.WriteByte(c)
		}
	}
	return strings.TrimSpace(b.String())
}

func isAlpha(c byte) bool {
	return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}
