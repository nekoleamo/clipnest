// 粘贴为纯文本：把富文本条目降级成纯文本再写回剪贴板。
package clip

import (
	"fmt"
	"html"
	"regexp"
	"strings"

	"github.com/egoist/mygo"

	"clipnest/internal/model"
	"clipnest/internal/paths"
)

// 块级标签去成换行，行内标签直接抹掉（不然「<b>粗</b>体」会粘成「粗 体」）。
var (
	reDropBody = regexp.MustCompile(`(?is)<(script|style)\b.*?</(script|style)>`)
	reBlockTag = regexp.MustCompile(`(?is)</?(br|p|div|li|tr|h[1-6]|blockquote|pre|section|article|header|footer|table)\b[^>]*>`)
	reAnyTag   = regexp.MustCompile(`(?s)<[^>]*>`)
	reSpaces   = regexp.MustCompile(`[ \t]+`)
	reNewlines = regexp.MustCompile(`\n{2,}`)
)

// PlainText 把任意条目降级成纯文本。
//
// 文本原样返回；HTML 剥标签并解实体；RTF 去壳。图片与文件没有纯文本形态。
func PlainText(item model.Item) (string, error) {
	switch item.Kind {
	case model.KindText:
		return item.Content, nil
	case model.KindHtml:
		return stripHTML(item.Content), nil
	case model.KindRtf:
		return stripRTF(item.Content), nil
	case model.KindImage:
		return "", fmt.Errorf("图片条目没有纯文本形态（试试导出成文件）")
	case model.KindFiles:
		// 文件条目的「纯文本」就是路径，粘到输入框里正合适
		return strings.TrimSpace(item.Content), nil
	default:
		return "", fmt.Errorf("不认识的条目类型：%s", item.Kind)
	}
}

// WriteBackPlain 把条目的纯文本写回剪贴板（只放纯文本，不带 HTML/RTF 风味）。
func WriteBackPlain(item model.Item, p paths.Paths) ([]string, error) {
	text, err := PlainText(item)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(text) == "" {
		return nil, fmt.Errorf("这条没有可粘的纯文本")
	}
	mygo.Clipboard.WriteText(text)
	return []string{model.ContentHash(model.KindText, text)}, nil
}

// stripHTML 去掉标签、脚本与样式，解常见 HTML 实体。
func stripHTML(s string) string {
	s = reDropBody.ReplaceAllString(s, "")
	s = reBlockTag.ReplaceAllString(s, "\n")
	s = reAnyTag.ReplaceAllString(s, "")
	s = html.UnescapeString(s)
	s = strings.ReplaceAll(s, "\u00a0", " ")
	s = reSpaces.ReplaceAllString(s, " ")
	lines := strings.Split(s, "\n")
	for i, line := range lines {
		lines[i] = strings.TrimSpace(line)
	}
	s = strings.Join(lines, "\n")
	// 开闭块标签各给一个换行，折叠成单换行（空段落不留空行）
	s = reNewlines.ReplaceAllString(s, "\n")
	return strings.TrimSpace(s)
}
