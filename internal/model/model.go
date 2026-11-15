// Package model 是数据模型：条目类型、哈希、入库输入与数据库行。
//
// 行为契约（见 docs/路线图.md 第四节）在这里落成代码：
//   - 契约 1：content_hash = blake3("<kind>:<content>")
//   - 契约 3：文件条目 content 为换行分隔的路径，file_types 为 d/f 串
//   - 契约 4：图片 content 是 PNG 文件名（像素尺寸另存 width/height）
package model

import (
	"strings"
	"time"

	"lukechampine.com/blake3"
)

// ItemKind 是条目类型，对应数据库 items.kind。
type ItemKind string

// 条目类型。
const (
	KindText  ItemKind = "text"
	KindHtml  ItemKind = "html"
	KindRtf   ItemKind = "rtf"
	KindImage ItemKind = "image"
	KindFiles ItemKind = "files"
)

// ParseKind 从库里的字符串还原；未知值返回 false。
func ParseKind(s string) (ItemKind, bool) {
	switch k := ItemKind(s); k {
	case KindText, KindHtml, KindRtf, KindImage, KindFiles:
		return k, true
	}
	return "", false
}

// ContentHash 是契约 1：blake3("<kind>:<content>")，十六进制小写。
//
// 唯一键就是它：同一内容不论入库多少次都只有一行。
func ContentHash(kind ItemKind, content string) string {
	h := blake3.New(32, nil)
	h.Write([]byte(kind))
	h.Write([]byte(":"))
	h.Write([]byte(content))
	return hexLower(h.Sum(nil))
}

// HashBytes 对任意字节求 blake3（图片文件名用，见契约 4）。
func HashBytes(data []byte) string {
	sum := blake3.Sum256(data)
	return hexLower(sum[:])
}

func hexLower(b []byte) string {
	const digits = "0123456789abcdef"
	out := make([]byte, 0, len(b)*2)
	for _, v := range b {
		out = append(out, digits[v>>4], digits[v&0x0f])
	}
	return string(out)
}

// Now 是当前时间戳（UTC、毫秒精度 ISO-8601），用于 created_at / updated_at。
//
// 字符串字典序即时间序，排序可以直接交给 SQLite。
func Now() string {
	return time.Now().UTC().Format("2006-01-02T15:04:05.000Z")
}

// NewItem 是待入库的条目；id 与时间戳由数据库层补。
type NewItem struct {
	Kind      ItemKind
	Content   string
	FileTypes string // 仅文件条目有值
	Width     int64  // 仅图片条目有值
	Height    int64
	Sensitive bool
}

// NewText 建一条文本条目。
func NewText(content string) NewItem { return plain(KindText, content) }

// NewHTML 建一条 HTML 条目。
func NewHTML(content string) NewItem { return plain(KindHtml, content) }

// NewRTF 建一条 RTF 条目。
func NewRTF(content string) NewItem { return plain(KindRtf, content) }

func plain(kind ItemKind, content string) NewItem {
	return NewItem{Kind: kind, Content: content}
}

// NewFiles 是契约 3：路径换行拼接，file_types 按同一顺序给 d/f。
func NewFiles(paths []string, fileTypes string) NewItem {
	return NewItem{Kind: KindFiles, Content: strings.Join(paths, "\n"), FileTypes: fileTypes}
}

// NewImage 是契约 4：content 存 PNG 文件名，尺寸单独存。
func NewImage(fileName string, width, height int64) NewItem {
	return NewItem{Kind: KindImage, Content: fileName, Width: width, Height: height}
}

// WithSensitive 标记条目是否来自敏感应用。
func (n NewItem) WithSensitive(on bool) NewItem {
	n.Sensitive = on
	return n
}

// Hash 是本条目在库里的唯一键。
func (n NewItem) Hash() string { return ContentHash(n.Kind, n.Content) }

// Item 是一条剪贴板历史（数据库行）。
type Item struct {
	ID          int64
	ContentHash string
	Kind        ItemKind
	Content     string
	FileTypes   string
	Width       int64
	Height      int64
	UseCount    int64
	Pinned      bool
	Favorite    bool
	Note        string
	GroupID     *int64
	Sensitive   bool
	CreatedAt   string
	UpdatedAt   string
}

// Excerpt 是面板里显示的一行摘要（换行压平、按字符截断，不劈开中文）。
func (i Item) Excerpt(maxChars int) string {
	flat := strings.NewReplacer("\n", " ", "\r", " ").Replace(i.Content)
	runes := []rune(flat)
	if len(runes) <= maxChars {
		return flat
	}
	return string(runes[:maxChars]) + "…"
}

// RelativeTime 是面板里显示的相对时间：一分钟内「刚刚」，之后按分钟/小时/天数，超过一周给日期。
func RelativeTime(ts string) string {
	at, err := time.Parse(time.RFC3339, ts)
	if err != nil {
		return ts
	}
	secs := int64(time.Since(at).Seconds())
	switch {
	case secs < 60:
		return "刚刚"
	case secs < 3600:
		return itoa(secs/60) + " 分钟前"
	case secs < 86400:
		return itoa(secs/3600) + " 小时前"
	case secs < 7*86400:
		return itoa(secs/86400) + " 天前"
	default:
		return at.Local().Format("2006-01-02")
	}
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

// Group 是分组（groups 表）。
type Group struct {
	ID   int64
	Name string
	Sort int64
}
