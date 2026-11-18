// Package update 是「检查新版本」：只查不装。
//
// 为什么不做自动下载替换：签名/公证已决定不做（见 docs/发布.md），自动替换一个未签名的
// 二进制会绕过用户对 Gatekeeper 的确认。所以这里只回答「有没有新版本」，由用户点链接去装。
package update

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Release 是查到的最新发布。
type Release struct {
	Tag         string `json:"tag_name"`
	Name        string `json:"name"`
	URL         string `json:"html_url"`
	Notes       string `json:"body"`
	PublishedAt string `json:"published_at"`
	// CreatedAt 是 Gitee 的发布时间字段（它不是 GitHub 的 published_at）。
	CreatedAt string `json:"created_at"`
}

// 更新源。国内直连 GitHub 常是「API 通、下载断」，所以默认走 gitee。
const (
	SourceGitee  = "gitee"
	SourceGitHub = "github"
	// SourceAuto 是「先 Gitee，不行再 GitHub」，与安装脚本的 auto 一致。
	SourceAuto   = "auto"
	SourceCustom = "custom"
)

// SourceLabels 是设置窗下拉里显示的文案（顺序与 Sources 一致）。
var SourceLabels = []string{"Gitee", "GitHub", "自动", "自定义"}

// Sources 是 SourceLabels 对应的取值。
var Sources = []string{SourceGitee, SourceGitHub, SourceAuto, SourceCustom}

// IndexOf 返回 source 在 Sources 里的下标（不认识的归到第一个，即 Gitee）。
func IndexOf(source string) int {
	source = strings.ToLower(strings.TrimSpace(source))
	for i, s := range Sources {
		if s == source {
			return i
		}
	}
	return 0
}

// Candidates 是「按这个源查一次更新」时要依次尝试的源。
// auto 展开为 [gitee github]（两个都试，前一个失败自动换第二个）；其他只自己一个。
func Candidates(source string) []string {
	if strings.ToLower(strings.TrimSpace(source)) == SourceAuto {
		return []string{SourceGitee, SourceGitHub}
	}
	return []string{source}
}

// SourceOf 是 SourceLabels 里的文案对应的取值（不认识的归到 GitHub）。
func SourceOf(label string) string { return Sources[IndexOfLabel(label)] }

// IndexOfLabel 返回文案在 SourceLabels 里的下标（不认识的归到 0）。
func IndexOfLabel(label string) int {
	for i, l := range SourceLabels {
		if l == label {
			return i
		}
	}
	return 0
}

// notesLimit 是更新说明最多留多少字（摆不进面板的信息不值得留在内存里）。
const notesLimit = 600

// maxBody 是响应体上限。
const maxBody = 256 << 10

// Client 是查更新用的小客户端。
//
// APIBase 与 HTTP 都能注入：测试里指到 httptest 服务器，顺手还能测超时与坏 JSON。
type Client struct {
	APIBase string
	// WebBase 是拼发布页用到的网页根（GitHub/Gitee 的 API 与网页不在同一个域）。
	// 留空就不猜，只认接口返回的 URL。
	WebBase string
	HTTP    *http.Client
	// UserAgent 是请求头里的标识；GitHub 的 API 要求带上它。
	UserAgent string
}

// DefaultClient 用官方 API 与 8 秒超时。
func DefaultClient(version string) Client {
	return Client{
		APIBase:   "https://api.github.com",
		WebBase:   "https://github.com",
		HTTP:      &http.Client{Timeout: 8 * time.Second},
		UserAgent: "clipnest/" + strings.TrimSpace(version),
	}
}

// ClientFor 按更新源给出客户端：gitee / github 用内置根，custom 用 apiBase。
// auto 在这里退到它的第一个候选（实际换源由 Candidates 负责）；
// apiBase 为空的自定义源会退到默认（否则只能报「没有更新源」）。
func ClientFor(source, apiBase, version string) Client {
	c := DefaultClient(version)
	switch strings.ToLower(strings.TrimSpace(source)) {
	case SourceGitee:
		c.APIBase = "https://gitee.com/api/v5"
		c.WebBase = "https://gitee.com"
	case SourceAuto:
		return ClientFor(SourceGitee, apiBase, version)
	case SourceCustom:
		switch base := strings.TrimRight(strings.TrimSpace(apiBase), "/"); {
		case base != "":
			c.APIBase = base
			c.WebBase = webBaseOf(base)
		}
	}
	return c
}

// webBaseOf 从 API 根里取 scheme://host（反代形如 https://host/api.github.com）。
// 取不出来就返回空，让「打开发布页」依赖接口返回的 URL。
func webBaseOf(apiBase string) string {
	u, err := url.Parse(apiBase)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return ""
	}
	return u.Scheme + "://" + u.Host
}

// Latest 取仓库的最新发布。
func (c Client) Latest(ctx context.Context, repo string) (Release, error) {
	repo = strings.Trim(strings.TrimSpace(repo), "/")
	if repo == "" {
		return Release{}, fmt.Errorf("没有填更新源（形如 owner/repo）")
	}
	base := c.APIBase
	if base == "" {
		base = "https://api.github.com"
	}
	httpClient := c.HTTP
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 8 * time.Second}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/repos/"+repo+"/releases/latest", nil)
	if err != nil {
		return Release{}, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	ua := c.UserAgent
	if ua == "" {
		ua = "clipnest"
	}
	req.Header.Set("User-Agent", ua)

	resp, err := httpClient.Do(req)
	if err != nil {
		return Release{}, fmt.Errorf("请求更新信息失败：%w", err)
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusNotFound:
		return Release{}, fmt.Errorf("%s 还没有发布版本", repo)
	case http.StatusForbidden:
		return Release{}, fmt.Errorf("更新源拒绝访问（可能是限流）、稍后再试")
	default:
		return Release{}, fmt.Errorf("更新源返回 %s", resp.Status)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBody+1))
	if err != nil {
		return Release{}, err
	}
	if len(body) > maxBody {
		return Release{}, fmt.Errorf("更新信息过大（超过 %d 字节）", maxBody)
	}
	var rel Release
	if err := json.Unmarshal(body, &rel); err != nil {
		return Release{}, fmt.Errorf("更新信息解析失败：%w", err)
	}
	rel.Tag = strings.TrimSpace(rel.Tag)
	// Gitee 不返回 html_url，用网页根拼发布页；发布日期字段名也不同。
	if rel.URL == "" && rel.Tag != "" && c.WebBase != "" {
		rel.URL = strings.TrimRight(c.WebBase, "/") + "/" + repo + "/releases/tag/" + rel.Tag
	}
	if rel.PublishedAt == "" {
		rel.PublishedAt = rel.CreatedAt
	}
	if got := []rune(rel.Notes); len(got) > notesLimit {
		rel.Notes = string(got[:notesLimit]) + "…"
	}
	return rel, nil
}

// Compare 比较两个版本号：a<b 返回 -1，相等 0，a>b 返回 1。
//
// 只认「点分数字」这种主流写法（`v1.2.3`、`1.2.3`、`1.2`）；解析不了就按字符串比，
// 不会崩——更新检查出错时宁可提示「无法比较」，也不能让面板挂掉。
func Compare(a, b string) int {
	an, aok := parseVersion(a)
	bn, bok := parseVersion(b)
	if !aok || !bok {
		return strings.Compare(strings.TrimSpace(a), strings.TrimSpace(b))
	}
	for i := 0; i < len(an) || i < len(bn); i++ {
		var av, bv int
		if i < len(an) {
			av = an[i]
		}
		if i < len(bn) {
			bv = bn[i]
		}
		switch {
		case av < bv:
			return -1
		case av > bv:
			return 1
		}
	}
	return 0
}

// Newer 报告 latest 是否比 current 新。
func Newer(current, latest string) bool { return Compare(latest, current) > 0 }

// parseVersion 把 `v1.2.3` 拆成 [1 2 3]；遇到非数字段就放弃（返回 ok=false）。
func parseVersion(v string) ([]int, bool) {
	v = strings.TrimSpace(v)
	v = strings.TrimPrefix(v, "v")
	v = strings.TrimPrefix(v, "V")
	if v == "" {
		return nil, false
	}
	// 去掉 -beta.1 / +build 之类的尾巴
	if i := strings.IndexAny(v, "-+"); i >= 0 {
		v = v[:i]
	}
	parts := strings.Split(v, ".")
	out := make([]int, 0, len(parts))
	for _, p := range parts {
		n, err := strconv.Atoi(strings.TrimSpace(p))
		if err != nil {
			return nil, false
		}
		out = append(out, n)
	}
	return out, true
}
