// Package netx 提供带 cookie、字符集识别与重试的 HTTP 客户端。
package netx

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"golang.org/x/text/encoding"
	"golang.org/x/text/encoding/htmlindex"
	"golang.org/x/text/transform"
)

// DefaultUA 是默认 User-Agent（与阅读 App 一致）
const DefaultUA = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/122.0.0.0 Safari/537.36"

// Request 是一次请求描述
type Request struct {
	URL     string
	Method  string
	Headers map[string]string
	Body    string
	Charset string
	Retry   int
}

// Response 是响应结果
type Response struct {
	URL         string
	FinalURL    string
	Body        string
	Bytes       []byte
	Code        int
	Headers     map[string]string
	Charset     string
	ContentType string
	Elapsed     time.Duration
}

// CookieStore 是极简的按域名存储的 cookie 仓库（对齐 Legado 的 CookieStore 语义：
// 不处理 path/expires，直接按域名保存整串 cookie，便于 `cookie.*` API 读写）。
type CookieStore struct {
	mu       sync.Mutex
	byDomain map[string]map[string]string
}

// NewCookieStore 创建空仓库
func NewCookieStore() *CookieStore {
	return &CookieStore{byDomain: map[string]map[string]string{}}
}

// DomainOf 取 URL 对应的 cookie 域名（host 去掉端口）
func DomainOf(rawURL string) string {
	u, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || u.Host == "" {
		return strings.TrimSpace(rawURL)
	}
	return u.Hostname()
}

// Get 返回该域名下的 cookie 串（`k=v; k2=v2`）
func (s *CookieStore) Get(rawURL string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	domain := DomainOf(rawURL)
	jar, ok := s.byDomain[domain]
	if !ok || len(jar) == 0 {
		return ""
	}
	var parts []string
	for _, k := range sortedKeys(jar) {
		if v := jar[k]; v != "" {
			parts = append(parts, k+"="+v)
		}
	}
	return strings.Join(parts, "; ")
}

// Set 合并写入 cookie 串
func (s *CookieStore) Set(rawURL, cookieStr string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	domain := DomainOf(rawURL)
	jar := s.byDomain[domain]
	if jar == nil {
		jar = map[string]string{}
		s.byDomain[domain] = jar
	}
	for _, pair := range cookiePairs(cookieStr) {
		jar[pair.k] = pair.v
	}
}

// Replace 只替换已存在键的值（对齐 Legado replaceCookie：保留其它键）
func (s *CookieStore) Replace(rawURL, cookieStr string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	domain := DomainOf(rawURL)
	jar := s.byDomain[domain]
	if jar == nil {
		jar = map[string]string{}
		s.byDomain[domain] = jar
	}
	for _, pair := range cookiePairs(cookieStr) {
		if _, ok := jar[pair.k]; ok {
			jar[pair.k] = pair.v
		}
	}
}

// Remove 删除某个键（key 为空时清空该域名）
func (s *CookieStore) Remove(rawURL, key string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	domain := DomainOf(rawURL)
	if key == "" {
		delete(s.byDomain, domain)
		return
	}
	if jar := s.byDomain[domain]; jar != nil {
		delete(jar, key)
	}
}

// AddFromResponse 吸收响应里的 Set-Cookie
func (s *CookieStore) AddFromResponse(rawURL string, header http.Header) {
	for _, c := range header.Values("Set-Cookie") {
		s.Set(rawURL, c)
	}
}

// cookieAttribute 是 Set-Cookie 里的属性名，不属于键值对
var cookieAttributes = map[string]bool{
	"path": true, "domain": true, "expires": true, "max-age": true,
	"secure": true, "httponly": true, "samesite": true, "priority": true,
	"version": true, "comment": true, "partitioned": true,
}

type cookiePair struct{ k, v string }

// cookiePairs 把 `a=1; Path=/; b=2` 拆成 {a:1, b:2}，忽略属性项
func cookiePairs(cookieStr string) []cookiePair {
	var out []cookiePair
	for _, raw := range strings.Split(cookieStr, ";") {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		k, v, _ := strings.Cut(raw, "=")
		k, v = strings.TrimSpace(k), strings.TrimSpace(v)
		if k == "" || cookieAttributes[strings.ToLower(k)] {
			continue
		}
		out = append(out, cookiePair{k: k, v: v})
	}
	return out
}

// Client 是共享的 HTTP 客户端
type Client struct {
	http      *http.Client
	Cookies   *CookieStore
	Timeout   time.Duration
	UserAgent string
	mu        sync.Mutex
}

// New 创建客户端
func New(timeout time.Duration) *Client {
	if timeout <= 0 {
		timeout = 20 * time.Second
	}
	return &Client{
		http:      &http.Client{Timeout: timeout},
		Cookies:   NewCookieStore(),
		Timeout:   timeout,
		UserAgent: DefaultUA,
	}
}

var charsetFromMeta = regexp.MustCompile(`(?i)<meta[^>]+charset\s*=\s*["']?\s*([a-zA-Z0-9_-]+)`)
var charsetFromContentType = regexp.MustCompile(`(?i)charset\s*=\s*["']?\s*([a-zA-Z0-9_-]+)`)

// Do 执行请求（含重试、cookie、字符集解码）
func (c *Client) Do(req Request) (*Response, error) {
	method := strings.ToUpper(strings.TrimSpace(req.Method))
	if method == "" {
		method = http.MethodGet
	}
	retry := req.Retry
	var lastErr error
	var lastResp *Response
	for attempt := 0; attempt <= retry; attempt++ {
		resp, err := c.doOnce(req, method)
		if err == nil && resp.Code < 500 {
			return resp, nil
		}
		if err != nil {
			lastErr = err
		} else {
			lastResp = resp
		}
		if attempt < retry {
			time.Sleep(time.Duration(200*(attempt+1)) * time.Millisecond)
		}
	}
	if lastResp != nil {
		return lastResp, nil
	}
	return nil, lastErr
}

func (c *Client) doOnce(req Request, method string) (*Response, error) {
	start := time.Now()
	var body io.Reader
	if req.Body != "" && method != http.MethodGet && method != http.MethodHead {
		body = strings.NewReader(req.Body)
	}
	httpReq, err := http.NewRequest(method, req.URL, body)
	if err != nil {
		return nil, err
	}
	for k, v := range req.Headers {
		if strings.EqualFold(k, "cookie") {
			continue
		}
		httpReq.Header.Set(k, v)
	}
	if httpReq.Header.Get("User-Agent") == "" {
		httpReq.Header.Set("User-Agent", c.UserAgent)
	}
	if c.Cookies != nil {
		cookie := ""
		for k, v := range req.Headers {
			if strings.EqualFold(k, "cookie") {
				cookie = v
			}
		}
		if stored := c.Cookies.Get(req.URL); stored != "" {
			if cookie == "" {
				cookie = stored
			} else {
				cookie = cookie + "; " + stored
			}
		}
		if cookie != "" {
			httpReq.Header.Set("Cookie", cookie)
		}
	}

	resp, err := c.http.Do(httpReq)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if c.Cookies != nil {
		c.Cookies.AddFromResponse(req.URL, resp.Header)
	}

	contentType := resp.Header.Get("Content-Type")
	charsetName := detectCharset(req.Charset, contentType, raw)
	text := decode(raw, charsetName)
	headers := map[string]string{}
	for k, v := range resp.Header {
		if len(v) > 0 {
			headers[strings.ToLower(k)] = v[0]
		}
	}
	finalURL := req.URL
	if resp.Request != nil && resp.Request.URL != nil {
		finalURL = resp.Request.URL.String()
	}
	return &Response{
		URL:         req.URL,
		FinalURL:    finalURL,
		Body:        text,
		Bytes:       raw,
		Code:        resp.StatusCode,
		Headers:     headers,
		Charset:     charsetName,
		ContentType: contentType,
		Elapsed:     time.Since(start),
	}, nil
}

// detectCharset 依次按：显式指定 → Content-Type → meta 标签 → UTF-8 合法性 → UTF-8
func detectCharset(explicit, contentType string, raw []byte) string {
	if e := normalizeCharset(explicit); e != "" {
		return e
	}
	if m := charsetFromContentType.FindStringSubmatch(contentType); m != nil {
		if e := normalizeCharset(m[1]); e != "" {
			return e
		}
	}
	head := raw
	if len(head) > 2048 {
		head = head[:2048]
	}
	if m := charsetFromMeta.FindSubmatch(head); m != nil {
		if e := normalizeCharset(string(m[1])); e != "" {
			return e
		}
	}
	if !utf8.Valid(raw) {
		return "gbk"
	}
	return "utf-8"
}

func normalizeCharset(name string) string {
	name = strings.ToLower(strings.TrimSpace(name))
	switch name {
	case "", "auto":
		return ""
	case "utf8":
		return "utf-8"
	case "gb2312", "gb-2312", "gb18030":
		return "gbk"
	}
	return name
}

func lookupEncoding(name string) encoding.Encoding {
	enc, err := htmlindex.Get(name)
	if err != nil || enc == nil {
		return nil
	}
	return enc
}

// decode 按字符集解码；无法解码时原样返回
func decode(raw []byte, charsetName string) string {
	name := normalizeCharset(charsetName)
	if name == "" || name == "utf-8" || name == "unicode-1-1-utf-8" {
		return string(raw)
	}
	enc := lookupEncoding(name)
	if enc == nil || enc == encoding.Nop {
		return string(raw)
	}
	out, err := io.ReadAll(transform.NewReader(bytes.NewReader(raw), enc.NewDecoder()))
	if err != nil {
		return string(raw)
	}
	return string(out)
}

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	for i := 1; i < len(keys); i++ {
		for j := i; j > 0 && keys[j] < keys[j-1]; j-- {
			keys[j], keys[j-1] = keys[j-1], keys[j]
		}
	}
	return keys
}

// Errorf 便于上层包装错误
func Errorf(format string, args ...any) error { return fmt.Errorf(format, args...) }
