package sync

import (
	"bytes"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Transport 是传输入口：一台设备一个文件，文件名 <device>.json。
type Transport interface {
	List() ([]string, error)
	Get(name string) ([]byte, error)
	Put(name string, data []byte) error
	Remove(name string) error
}

// LocalTransport 用本地目录当「远端」：测试与「网盘同步目录」的兜底。
type LocalTransport struct{ root string }

// NewLocalTransport 建一个本地目录传输。
func NewLocalTransport(root string) *LocalTransport { return &LocalTransport{root: root} }

// Root 是远端目录。
func (t *LocalTransport) Root() string { return t.root }

// List 列出 .json 文件。
func (t *LocalTransport) List() ([]string, error) {
	entries, err := os.ReadDir(t.root)
	if err != nil {
		return nil, fmt.Errorf("读目录失败：%w", err)
	}
	var out []string
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".json") {
			out = append(out, e.Name())
		}
	}
	sort.Strings(out)
	return out, nil
}

// Get 读一个文件。
func (t *LocalTransport) Get(name string) ([]byte, error) {
	data, err := os.ReadFile(filepath.Join(t.root, name))
	if err != nil {
		return nil, fmt.Errorf("读 %s 失败：%w", name, err)
	}
	return data, nil
}

// Put 原子写一个文件。
func (t *LocalTransport) Put(name string, data []byte) error {
	target := filepath.Join(t.root, name)
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return fmt.Errorf("建目录失败：%w", err)
	}
	tmp := target + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return fmt.Errorf("写 %s 失败：%w", name, err)
	}
	return os.Rename(tmp, target)
}

// Remove 删一个文件；不存在不算错。
func (t *LocalTransport) Remove(name string) error {
	err := os.Remove(filepath.Join(t.root, name))
	if err == nil || errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return fmt.Errorf("删 %s 失败：%w", name, err)
}

// WebDavTransport 是 WebDAV 传输（PROPFIND / MKCOL / PUT / GET / DELETE）。
type WebDavTransport struct {
	base     string // 结尾不带 /
	user     string
	password string
	client   *http.Client
}

// NewWebDavTransport 建一个 WebDAV 传输。
//
// 必须显式禁代理：本机 HTTPS_PROXY 会把局域网 NAS 也送进代理，每个请求卡到超时。
// 连接超时单独设短（3 秒）：出门在外局域网地址不可达时，快速失败才能尽快切到
// Tailscale 备用地址（整体请求仍留 30 秒，慢的 PROPFIND 不受影响）。
func NewWebDavTransport(url, remoteDir, user, password string, insecureTLS bool) *WebDavTransport {
	dialer := &net.Dialer{Timeout: 3 * time.Second, KeepAlive: 30 * time.Second}
	transport := &http.Transport{
		Proxy:                 nil,
		DialContext:           dialer.DialContext,
		TLSClientConfig:       &tls.Config{InsecureSkipVerify: insecureTLS},
		TLSHandshakeTimeout:   6 * time.Second,
		MaxIdleConnsPerHost:   4,
		IdleConnTimeout:       30 * time.Second,
		ResponseHeaderTimeout: 15 * time.Second,
	}
	return &WebDavTransport{
		base:     strings.TrimRight(url, "/") + "/" + strings.Trim(remoteDir, "/"),
		user:     user,
		password: password,
		client:   &http.Client{Transport: transport, Timeout: 30 * time.Second},
	}
}

// maxRemoteBytes 是单个远端响应的读取上限（正常增量包几十 KB，图片原图最多几 MB）。
//
// 没有上限的话，一个被写坏/被塞满的远端文件会把常驻进程直接撑爆。
const maxRemoteBytes = 64 << 20

// ErrRemoteTooLarge 表示远端响应超过上限。
var ErrRemoteTooLarge = errors.New("远端响应超过上限")

// readLimited 读 body 并检查上限。
func readLimited(r io.Reader) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(r, maxRemoteBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > maxRemoteBytes {
		return nil, fmt.Errorf("%w（%d 字节以上）", ErrRemoteTooLarge, maxRemoteBytes)
	}
	return data, nil
}

// do 发一个请求（WebDAV 的自定义方法需要自己拼）。
func (t *WebDavTransport) do(method, target string, body []byte) (*http.Response, error) {
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequest(method, target, reader)
	if err != nil {
		return nil, err
	}
	if t.user != "" || t.password != "" {
		req.SetBasicAuth(t.user, t.password)
	}
	if body != nil {
		req.ContentLength = int64(len(body))
	}
	return t.client.Do(req)
}

// ensureDir 确保远端目录存在（根目录只读的 NAS 会 405/403，能忽略的就忽略）。
func (t *WebDavTransport) ensureDir(dir string) error {
	resp, err := t.do("MKCOL", dir, nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case 201, 301, 405, 403, 409, 200:
		return nil
	default:
		return fmt.Errorf("MKCOL %s: %s", dir, resp.Status)
	}
}

// List 列远端目录里的 .json 文件（PROPFIND）。
func (t *WebDavTransport) List() ([]string, error) {
	body := []byte(`<?xml version="1.0" encoding="utf-8"?>
<d:propfind xmlns:d="DAV:"><d:prop><d:resourcetype/></d:prop></d:propfind>`)
	resp, err := t.do("PROPFIND", t.base+"/", body)
	if err != nil {
		return nil, fmt.Errorf("PROPFIND 失败：%w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == 404 {
		// 目录还不存在：先建，当作空列表
		if err := t.ensureDir(t.base); err != nil {
			return nil, err
		}
		return nil, nil
	}
	if resp.StatusCode != 207 && resp.StatusCode != 200 {
		return nil, fmt.Errorf("PROPFIND: %s", resp.Status)
	}
	raw, err := readLimited(resp.Body)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, href := range extractHrefs(string(raw)) {
		name := remoteName(href)
		if strings.HasSuffix(name, ".json") {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out, nil
}

// extractHrefs 从 PROPFIND 的 XML 里抠出 <d:href> 内容（够用，不引 XML 解析）。
func extractHrefs(xml string) []string {
	var out []string
	for {
		i := strings.Index(xml, "href>")
		if i < 0 {
			break
		}
		rest := xml[i+len("href>"):]
		j := strings.Index(rest, "</")
		if j < 0 {
			break
		}
		out = append(out, strings.TrimSpace(rest[:j]))
		xml = rest[j:]
	}
	return out
}

// Get 读远端一个文件。
func (t *WebDavTransport) Get(name string) ([]byte, error) {
	resp, err := t.do("GET", t.base+"/"+name, nil)
	if err != nil {
		return nil, fmt.Errorf("GET %s 失败：%w", name, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("GET %s: %s", name, resp.Status)
	}
	return readLimited(resp.Body)
}

// remoteName 从 PROPFIND 的 href 里取文件名。
//
// href 是 URL 编码过的（空格是 %20、中文是 %XX），不解码的话带空格/中文的
// 包名永远认不出来，那一轮就白拉了。
func remoteName(href string) string {
	if u, err := url.Parse(href); err == nil && u.Path != "" {
		if strings.HasSuffix(u.Path, "/") {
			return "" // 目录（含根目录）不是包
		}
		if unescaped, err := url.PathUnescape(u.Path); err == nil {
			return path.Base(unescaped)
		}
		return path.Base(u.Path)
	}
	if unescaped, err := url.PathUnescape(href); err == nil {
		return path.Base(unescaped)
	}
	return path.Base(href)
}

// Put 上传一个文件（父目录不存在时先建：飞牛等 NAS 会回 403 而不是 409）。
func (t *WebDavTransport) Put(name string, data []byte) error {
	target := t.base + "/" + name
	resp, err := t.do("PUT", target, data)
	if err != nil {
		return fmt.Errorf("PUT %s 失败：%w", name, err)
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case 200, 201, 204:
		return nil
	case 403, 404, 409:
		// 父目录不存在：建好再试一次
		if err := t.ensureDir(path.Dir(target)); err != nil {
			return err
		}
		again, err := t.do("PUT", target, data)
		if err != nil {
			return fmt.Errorf("PUT %s 失败：%w", name, err)
		}
		defer again.Body.Close()
		if again.StatusCode == 200 || again.StatusCode == 201 || again.StatusCode == 204 {
			return nil
		}
		return fmt.Errorf("PUT %s: %s", name, again.Status)
	default:
		return fmt.Errorf("PUT %s: %s", name, resp.Status)
	}
}

// Remove 删远端一个文件；不存在不算错。
func (t *WebDavTransport) Remove(name string) error {
	resp, err := t.do("DELETE", t.base+"/"+name, nil)
	if err != nil {
		return fmt.Errorf("DELETE %s 失败：%w", name, err)
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case 200, 204, 404:
		return nil
	default:
		return fmt.Errorf("DELETE %s: %s", name, resp.Status)
	}
}

// OpenTransport 按配置建传输层，并返回实际用的入口（WebDAV 会在局域网/Tailscale 间选）。
func OpenTransport(c Config) (Transport, Endpoint, error) {
	switch c.Backend.Kind {
	case BackendLocalDir:
		if err := os.MkdirAll(c.Backend.Path, 0o755); err != nil {
			return nil, Endpoint{}, fmt.Errorf("建同步目录失败：%w", err)
		}
		return NewLocalTransport(c.Backend.Path), Endpoint{Label: "本地目录", URL: c.Backend.Path}, nil
	case BackendWebDav:
		ep, err := ResolveEndpoint(c.Backend, ProbeTimeout)
		if err != nil {
			return nil, Endpoint{}, err
		}
		return NewWebDavTransport(ep.URL, c.Backend.RemoteDir, c.Backend.Username,
			webdavPassword(c), c.Backend.InsecureTLS), ep, nil
	default:
		return nil, Endpoint{}, errors.New("没有可用的同步方式")
	}
}

// webdavPassword 从钥匙串取口令。
//
// 先按设备名找（现在的写法）；早期版本按用户名存，找不到时兼容回退。
func webdavPassword(c Config) string {
	if pw, err := secretGet(WebDavSecretService, c.DeviceName); err == nil && pw != "" {
		return pw
	}
	if c.Backend.Username != "" {
		if pw, err := secretGet(WebDavSecretService, c.Backend.Username); err == nil {
			return pw
		}
	}
	return ""
}
