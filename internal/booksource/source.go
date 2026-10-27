// Package booksource 把一个 Legado 书源 JSON 配置封装成可调用的书源实例。
//
// 实例自带规则链路所需的全部宿主能力（JS 求值、HTTP、变量、cookie、缓存），
// 对外只需 New(cfg, opts) 然后调用 Search / GetBookInfo / GetChapterList / GetContent。
package booksource

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"legado-go/internal/analyze"
	"legado-go/internal/analyzeurl"
	"legado-go/internal/jsengine"
	"legado-go/internal/model"
	"legado-go/internal/netx"
	"legado-go/internal/rules"
)

// Cache 是可替换的缓存实现
type Cache interface {
	Put(ctx context.Context, key, value string, ttl time.Duration)
	Get(ctx context.Context, key string) (string, bool)
	Delete(ctx context.Context, key string)
}

// Options 是创建书源实例的可选参数
type Options struct {
	// Timeout 单次 HTTP 请求超时（默认 30s）
	Timeout time.Duration
	// CacheTTL 默认缓存 TTL（默认 1h）
	CacheTTL time.Duration
	// Cache 自定义缓存实现（不传则用内置内存缓存）
	Cache Cache
	// Client 共享 HTTP 客户端（用于跨实例共享 cookie）
	Client *netx.Client
	// OnLog 接收 JS 内的 console.log / java.log
	OnLog func(line string)
}

type cacheItem struct {
	value   string
	expires time.Time
}

// MemCache 是默认的内存缓存
type MemCache struct {
	mu    sync.Mutex
	items map[string]cacheItem
	ttl   time.Duration
}

// NewMemCache 创建内存缓存
func NewMemCache(ttl time.Duration) *MemCache {
	if ttl <= 0 {
		ttl = time.Hour
	}
	return &MemCache{items: map[string]cacheItem{}, ttl: ttl}
}

// Put 写入缓存
func (c *MemCache) Put(_ context.Context, key, value string, ttl time.Duration) {
	if key == "" {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if ttl <= 0 {
		ttl = c.ttl
	}
	c.items[key] = cacheItem{value: value, expires: time.Now().Add(ttl)}
}

// Get 读取缓存
func (c *MemCache) Get(_ context.Context, key string) (string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	item, ok := c.items[key]
	if !ok {
		return "", false
	}
	if time.Now().After(item.expires) {
		delete(c.items, key)
		return "", false
	}
	return item.value, true
}

// Delete 删除缓存
func (c *MemCache) Delete(_ context.Context, key string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.items, key)
}

// Sweep 清理过期键
func (c *MemCache) Sweep() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := time.Now()
	n := 0
	for k, v := range c.items {
		if now.After(v.expires) {
			delete(c.items, k)
			n++
		}
	}
	return n
}

// Source 是一个书源实例
type Source struct {
	cfg      *model.Source
	client   *netx.Client
	engine   *jsengine.Engine
	cache    Cache
	cacheTTL time.Duration
	onLog    func(string)

	dataMu       sync.Mutex
	vars         map[string]string
	loginHeaders map[string]string
	loginInfo    map[string]string
	bookVars     map[string]string
	rateNext     time.Time
	closed       bool

	runMu sync.Mutex // 串行化 JS 执行
	ctxMu sync.RWMutex
	book  *model.Book
	chap  *model.Chapter
}

// New 由书源配置创建实例
func New(cfg *model.Config, opts Options) (*Source, error) {
	normalized, err := model.Normalize(cfg)
	if err != nil {
		return nil, err
	}
	if opts.Timeout <= 0 {
		opts.Timeout = 30 * time.Second
	}
	if opts.CacheTTL <= 0 {
		opts.CacheTTL = time.Hour
	}
	cache := opts.Cache
	if cache == nil {
		cache = NewMemCache(opts.CacheTTL)
	}
	client := opts.Client
	if client == nil {
		client = netx.New(opts.Timeout)
	}
	src := &Source{
		cfg:          normalized,
		client:       client,
		cache:        cache,
		cacheTTL:     opts.CacheTTL,
		onLog:        opts.OnLog,
		vars:         map[string]string{},
		loginHeaders: map[string]string{},
		loginInfo:    map[string]string{},
		bookVars:     map[string]string{},
	}
	for k, v := range normalized.Variable {
		src.vars[k] = v
	}
	src.engine = jsengine.New(src)
	return src, nil
}

// NewFromJSON 由书源 JSON（对象或数组）创建实例
func NewFromJSON(data []byte, opts Options) (*Source, error) {
	cfg, err := model.ParseConfig(data)
	if err != nil {
		return nil, err
	}
	return New(cfg, opts)
}

// Close 释放 JS 运行时
func (s *Source) Close() error {
	s.dataMu.Lock()
	if s.closed {
		s.dataMu.Unlock()
		return nil
	}
	s.closed = true
	s.dataMu.Unlock()
	return s.engine.Close()
}

// Config 返回归一化后的书源配置
func (s *Source) Config() *model.Source { return s.cfg }

// Name 书源名称
func (s *Source) Name() string { return s.cfg.Name }

// URL 书源地址
func (s *Source) URL() string { return s.cfg.URL }

// ExploreEntries 返回发现页入口
func (s *Source) ExploreEntries() []model.ExploreEntry { return s.cfg.ExploreEntries }

// ---------------- 解析宿主：analyze.Host ----------------

type ruleHost struct {
	src *Source
	b   Bindings
}

func (h *ruleHost) EvalJS(js string, result any) (any, error) {
	b := h.b
	b.Result = result
	return h.src.EvalJS(js, b)
}

func (h *ruleHost) GetVar(key string) string { return h.src.SourceGet(key) }

func (h *ruleHost) PutVar(key, value string) { h.src.PutVar(key, value) }

// MakeRule 用给定正文与基准地址构造规则解析器
func (s *Source) MakeRule(content any, baseURL string, b Bindings) *analyze.Rule {
	if b.BaseURL == "" {
		b.BaseURL = baseURL
	}
	return analyze.New(&ruleHost{src: s, b: b}, content, baseURL)
}

// ---------------- JS 求值 ----------------

// EvalJS 在书源 JS 沙箱中执行代码
func (s *Source) EvalJS(js string, b Bindings) (any, error) {
	code := strings.TrimSpace(js)
	if code == "" {
		return nil, nil
	}
	bindings := jsengine.Bindings{
		BaseURL:        b.BaseURL,
		Page:           b.Page,
		Key:            b.Key,
		Title:          b.Title,
		Src:            b.Src,
		NextChapterURL: b.NextChapterURL,
		Result:         b.Result,
		Book:           b.Book,
		Chapter:        b.Chapter,
	}
	if bindings.Page <= 0 {
		bindings.Page = 1
	}
	if bindings.BaseURL == "" && b.Book != nil {
		bindings.BaseURL = b.Book.BookURL
	}

	s.runMu.Lock()
	defer s.runMu.Unlock()
	s.ctxMu.Lock()
	s.book, s.chap = b.Book, b.Chapter
	s.ctxMu.Unlock()
	defer func() {
		s.ctxMu.Lock()
		s.book, s.chap = nil, nil
		s.ctxMu.Unlock()
	}()
	return s.engine.Eval(code, bindings)
}

// ---------------- jsengine.Host ----------------

// Ajax 抓取 url 返回正文
func (s *Source) Ajax(url string) (string, error) {
	resp, err := s.Fetch(url, Bindings{})
	if err != nil {
		return "", err
	}
	return resp.Body, nil
}

// AjaxAll 并发抓取多个 url
func (s *Source) AjaxAll(urls []string) ([]any, error) {
	out := make([]any, len(urls))
	var wg sync.WaitGroup
	var firstErr error
	var mu sync.Mutex
	for i, u := range urls {
		wg.Add(1)
		go func(i int, u string) {
			defer wg.Done()
			body, err := s.Ajax(u)
			if err != nil {
				mu.Lock()
				if firstErr == nil {
					firstErr = err
				}
				mu.Unlock()
				return
			}
			out[i] = body
		}(i, u)
	}
	wg.Wait()
	if firstErr != nil {
		return nil, firstErr
	}
	return out, nil
}

// Connect 抓取 url 并返回结构化结果
func (s *Source) Connect(url string) (map[string]any, error) {
	resp, err := s.Fetch(url, Bindings{})
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"body":        resp.Body,
		"bytes":       resp.Bytes,
		"url":         resp.FinalURL,
		"code":        resp.Code,
		"charset":     resp.Charset,
		"contentType": resp.ContentType,
		"headers":     resp.Headers,
	}, nil
}

// HTTPGet GET 请求并返回正文
func (s *Source) HTTPGet(url string, headers map[string]string) (string, error) {
	return s.rawRequest("GET", url, "", headers)
}

// HTTPPost POST 请求并返回正文
func (s *Source) HTTPPost(url, body string, headers map[string]string) (string, error) {
	return s.rawRequest("POST", url, body, headers)
}

// HTTPHead HEAD 请求并返回响应头描述
func (s *Source) HTTPHead(url string) (string, error) {
	merged := s.HeaderMap()
	for k, v := range map[string]string{} {
		merged[k] = v
	}
	resp, err := s.client.Do(netx.Request{URL: url, Method: "HEAD", Headers: merged})
	if err != nil {
		return "", err
	}
	var b strings.Builder
	for k, v := range resp.Headers {
		b.WriteString(k + ": " + v + "\n")
	}
	return b.String(), nil
}

func (s *Source) rawRequest(method, url, body string, headers map[string]string) (string, error) {
	merged := s.HeaderMap()
	for k, v := range headers {
		merged[k] = v
	}
	resp, err := s.client.Do(netx.Request{URL: url, Method: method, Body: body, Headers: merged})
	if err != nil {
		return "", err
	}
	return resp.Body, nil
}

// PutVar 写入全局变量
func (s *Source) PutVar(key, value string) {
	s.dataMu.Lock()
	defer s.dataMu.Unlock()
	if key == "" {
		return
	}
	if len(value) > 1<<20 {
		value = value[:1<<20]
	}
	if value == "" {
		delete(s.vars, key)
		return
	}
	s.vars[key] = value
}

// GetVar 读取全局变量
func (s *Source) GetVar(key string) (string, bool) {
	s.dataMu.Lock()
	defer s.dataMu.Unlock()
	v, ok := s.vars[key]
	return v, ok
}

// SourceVariable 返回变量 JSON
func (s *Source) SourceVariable() string {
	s.dataMu.Lock()
	defer s.dataMu.Unlock()
	if len(s.vars) == 0 {
		return ""
	}
	b, err := json.Marshal(s.vars)
	if err != nil {
		return ""
	}
	return string(b)
}

// SourceSetVariable 用 JSON 覆盖变量
func (s *Source) SourceSetVariable(raw string) {
	vars := map[string]string{}
	if strings.TrimSpace(raw) != "" {
		if err := json.Unmarshal([]byte(raw), &vars); err != nil {
			return
		}
	}
	s.dataMu.Lock()
	defer s.dataMu.Unlock()
	s.vars = vars
}

// Variables 返回变量快照
func (s *Source) Variables() map[string]string {
	s.dataMu.Lock()
	defer s.dataMu.Unlock()
	out := make(map[string]string, len(s.vars))
	for k, v := range s.vars {
		out[k] = v
	}
	return out
}

// SourceGet 读取变量（不存在返回空串）
func (s *Source) SourceGet(key string) string {
	v, _ := s.GetVar(key)
	return v
}

// SourcePut 写入变量
func (s *Source) SourcePut(key, value string) { s.PutVar(key, value) }

// SourceHeaderMap 返回生效请求头
func (s *Source) SourceHeaderMap() map[string]string { return s.HeaderMap() }

// HeaderMap 返回默认头 + 登录头
func (s *Source) HeaderMap() map[string]string {
	headers := map[string]string{}
	for k, v := range s.cfg.HeaderMap {
		headers[k] = v
	}
	s.dataMu.Lock()
	for k, v := range s.loginHeaders {
		headers[k] = v
	}
	s.dataMu.Unlock()
	return headers
}

// SourceLoginHeaderMap 返回登录态请求头
func (s *Source) SourceLoginHeaderMap() map[string]string {
	s.dataMu.Lock()
	defer s.dataMu.Unlock()
	out := make(map[string]string, len(s.loginHeaders))
	for k, v := range s.loginHeaders {
		out[k] = v
	}
	return out
}

// SourcePutLoginHeader 写入登录态请求头
func (s *Source) SourcePutLoginHeader(key, value string) {
	s.dataMu.Lock()
	defer s.dataMu.Unlock()
	if key == "" {
		return
	}
	if value == "" {
		delete(s.loginHeaders, key)
		return
	}
	s.loginHeaders[key] = value
}

// SourceRemoveLoginHeader 移除登录态请求头
func (s *Source) SourceRemoveLoginHeader(key string) {
	s.dataMu.Lock()
	defer s.dataMu.Unlock()
	delete(s.loginHeaders, key)
}

// SourceLoginInfo 读取登录信息
func (s *Source) SourceLoginInfo(key string) string {
	s.dataMu.Lock()
	defer s.dataMu.Unlock()
	return s.loginInfo[key]
}

// SourceLoginInfoMap 返回登录信息快照
func (s *Source) SourceLoginInfoMap() map[string]string {
	s.dataMu.Lock()
	defer s.dataMu.Unlock()
	out := make(map[string]string, len(s.loginInfo))
	for k, v := range s.loginInfo {
		out[k] = v
	}
	return out
}

// SetLoginInfo 写入登录信息
func (s *Source) SetLoginInfo(key, value string) {
	s.dataMu.Lock()
	defer s.dataMu.Unlock()
	if key == "" {
		return
	}
	if value == "" {
		delete(s.loginInfo, key)
		return
	}
	s.loginInfo[key] = value
}

// BookVariable 返回当前书籍变量
func (s *Source) BookVariable() string {
	book := s.currentBook()
	if book == nil {
		return ""
	}
	s.dataMu.Lock()
	defer s.dataMu.Unlock()
	return s.bookVars[book.BookURL]
}

// BookSetVariable 写入当前书籍变量
func (s *Source) BookSetVariable(value string) {
	book := s.currentBook()
	if book == nil {
		return
	}
	s.dataMu.Lock()
	defer s.dataMu.Unlock()
	s.bookVars[book.BookURL] = value
}

// BookGet 读取当前书籍变量
func (s *Source) BookGet(key string) any {
	raw := s.BookVariable()
	if raw == "" {
		return nil
	}
	var vars map[string]any
	if err := json.Unmarshal([]byte(raw), &vars); err != nil {
		return nil
	}
	return vars[key]
}

// BookPut 写入当前书籍变量
func (s *Source) BookPut(key string, value any) {
	if key == "" {
		return
	}
	vars := map[string]any{}
	if raw := s.BookVariable(); raw != "" {
		_ = json.Unmarshal([]byte(raw), &vars)
	}
	vars[key] = value
	b, err := json.Marshal(vars)
	if err != nil {
		return
	}
	s.BookSetVariable(string(b))
}

// ChapterGet 读取当前章节变量
func (s *Source) ChapterGet(key string) any {
	chapter := s.currentChapter()
	if chapter == nil {
		return nil
	}
	s.dataMu.Lock()
	raw := s.bookVars["chapter:"+chapter.URL]
	s.dataMu.Unlock()
	if raw == "" {
		return nil
	}
	var vars map[string]any
	if err := json.Unmarshal([]byte(raw), &vars); err != nil {
		return nil
	}
	return vars[key]
}

// ChapterPut 写入当前章节变量
func (s *Source) ChapterPut(key string, value any) {
	chapter := s.currentChapter()
	if chapter == nil || key == "" {
		return
	}
	vars := map[string]any{}
	s.dataMu.Lock()
	raw := s.bookVars["chapter:"+chapter.URL]
	s.dataMu.Unlock()
	if raw != "" {
		_ = json.Unmarshal([]byte(raw), &vars)
	}
	vars[key] = value
	b, err := json.Marshal(vars)
	if err != nil {
		return
	}
	s.dataMu.Lock()
	s.bookVars["chapter:"+chapter.URL] = string(b)
	s.dataMu.Unlock()
}

func (s *Source) currentBook() *model.Book {
	s.ctxMu.RLock()
	defer s.ctxMu.RUnlock()
	return s.book
}

func (s *Source) currentChapter() *model.Chapter {
	s.ctxMu.RLock()
	defer s.ctxMu.RUnlock()
	return s.chap
}

// CookieGet 读取 cookie
func (s *Source) CookieGet(urlStr string) string {
	if s.client == nil || s.client.Cookies == nil {
		return ""
	}
	return s.client.Cookies.Get(urlStr)
}

// CookieSet 写入 cookie
func (s *Source) CookieSet(url, cookie string) {
	if s.client != nil && s.client.Cookies != nil {
		s.client.Cookies.Set(url, cookie)
	}
}

// CookieReplace 覆盖 cookie
func (s *Source) CookieReplace(url, cookie string) {
	if s.client != nil && s.client.Cookies != nil {
		s.client.Cookies.Replace(url, cookie)
	}
}

// CookieRemove 删除 cookie
func (s *Source) CookieRemove(url string) {
	if s.client != nil && s.client.Cookies != nil {
		s.client.Cookies.Remove(url, "")
	}
}

// CachePut 写入缓存
func (s *Source) CachePut(key, value string, ttl time.Duration) {
	s.cache.Put(context.Background(), key, value, ttl)
}

// CacheGet 读取缓存
func (s *Source) CacheGet(key string) (string, bool) {
	return s.cache.Get(context.Background(), key)
}

// CacheDelete 删除缓存
func (s *Source) CacheDelete(key string) { s.cache.Delete(context.Background(), key) }

// JsLib 书源 JS 库
func (s *Source) JsLib() string { return s.cfg.JSLib }

// Log 输出日志
func (s *Source) Log(line string) {
	if s.onLog != nil {
		s.onLog(line)
	}
}

// ---------------- HTTP ----------------

func (s *Source) waitRate() {
	interval := parseConcurrentRate(s.cfg.ConcurrentRate)
	if interval <= 0 {
		return
	}
	s.dataMu.Lock()
	now := time.Now()
	next := s.rateNext
	if next.Before(now) {
		next = now
	}
	s.rateNext = next.Add(interval)
	s.dataMu.Unlock()
	if wait := time.Until(next); wait > 0 {
		time.Sleep(wait)
	}
}

// parseConcurrentRate 解析 "次数/周期毫秒" 形式的限速配置
func parseConcurrentRate(raw string) time.Duration {
	parts := strings.SplitN(strings.TrimSpace(raw), "/", 2)
	if len(parts) != 2 {
		return 0
	}
	count, err1 := strconv.Atoi(strings.TrimSpace(parts[0]))
	periodMS, err2 := strconv.Atoi(strings.TrimSpace(parts[1]))
	if err1 != nil || err2 != nil || count <= 0 || periodMS <= 0 {
		return 0
	}
	return time.Duration(periodMS) * time.Millisecond / time.Duration(count)
}

type urlCtx struct {
	src *Source
	b   Bindings
}

func (c *urlCtx) EvalJS(js string) (any, error) { return c.src.EvalJS(js, c.b) }

func (c *urlCtx) DefaultHeaders() map[string]string { return c.src.HeaderMap() }

func (c *urlCtx) CookieGet(url string) string { return c.src.CookieGet(url) }

func (c *urlCtx) CookiesEnabled() bool { return true }

// Fetch 按 url 规则发起请求
func (s *Source) Fetch(urlRule string, b Bindings) (*netx.Response, error) {
	if strings.TrimSpace(urlRule) == "" {
		return nil, model.NewError(model.KindInvalidConfig, "url rule is empty")
	}
	if b.BaseURL == "" {
		b.BaseURL = s.cfg.URL
	}
	parsed, err := analyzeurl.Analyze(&urlCtx{src: s, b: b}, urlRule, nil)
	if err != nil {
		return nil, err
	}
	parsed.URL = analyze.AbsoluteURL(b.BaseURL, parsed.URL)
	headers := parsed.Headers
	if headers == nil {
		headers = map[string]string{}
	}
	for k, v := range headers {
		if js, ok := inlineJS(v); ok {
			out, err := s.EvalJS(js, b)
			if err != nil {
				return nil, err
			}
			headers[k] = rules.JsResultToString(out)
		}
	}
	if parsed.Body != "" && headers["Content-Type"] == "" && headers["content-type"] == "" {
		ct := "application/x-www-form-urlencoded"
		if strings.HasPrefix(strings.TrimSpace(parsed.Body), "{") {
			ct = "application/json"
		}
		headers["Content-Type"] = ct
	}
	body, err := analyzeurl.ReplaceTemplate(parsed.Body, func(expr string) (any, error) {
		return s.EvalJS(expr, b)
	})
	if err != nil {
		return nil, err
	}

	s.waitRate()

	req := netx.Request{
		URL:     parsed.URL,
		Method:  parsed.Method,
		Headers: headers,
		Body:    body,
		Charset: parsed.Charset,
		Retry:   0,
	}
	attempts := parsed.Retry + 1
	var lastErr error
	for i := 0; i < attempts; i++ {
		resp, err := s.client.Do(req)
		if err == nil {
			return resp, nil
		}
		lastErr = err
		if i < attempts-1 {
			time.Sleep(time.Duration(150*(i+1)) * time.Millisecond)
		}
	}
	if lastErr == nil {
		lastErr = model.NewError(model.KindNetwork, "request failed: %s", parsed.URL)
	}
	return nil, lastErr
}

func inlineJS(v string) (string, bool) {
	trimmed := strings.TrimSpace(v)
	lower := strings.ToLower(trimmed)
	if strings.HasPrefix(lower, "@js:") {
		return trimmed[4:], true
	}
	if strings.HasPrefix(lower, "<js>") && strings.HasSuffix(lower, "</js>") {
		return trimmed[4 : len(trimmed)-5], true
	}
	return "", false
}

// ReplaceTemplate 替换 `{{expr}}`，expr 在书源 JS 环境求值
func ReplaceTemplate(text string, eval func(string) (any, error)) (string, error) {
	return analyzeurl.ReplaceTemplate(text, eval)
}

// ---------------- 业务入口 ----------------

// Search 关键词搜索
func (s *Source) Search(key string, page int) ([]model.Book, error) {
	key = strings.TrimSpace(key)
	if key == "" {
		return nil, model.NewError(model.KindInvalidConfig, "search key is required")
	}
	if strings.TrimSpace(s.cfg.SearchURL) == "" {
		return nil, model.NewError(model.KindInvalidConfig, "source has no searchUrl")
	}
	if page <= 0 {
		page = 1
	}
	b := Bindings{Key: key, Page: page, BaseURL: s.cfg.URL}
	resp, err := s.Fetch(s.cfg.SearchURL, b)
	if err != nil {
		return nil, err
	}
	rule := searchListRule(s.cfg)
	books, err := ParseBookList(s, resp.Body, resp.FinalURL, rule, ListOptions{
		IsSearch:     true,
		Key:          key,
		Page:         page,
		CheckKeyWord: s.cfg.RuleSearch.CheckKeyWord,
	})
	if err != nil {
		return nil, err
	}
	return s.attachOrigin(books), nil
}

func searchListRule(cfg *model.Source) ListRule {
	return ListRule{
		BookList:     cfg.RuleSearch.BookList,
		Name:         cfg.RuleSearch.Name,
		Author:       cfg.RuleSearch.Author,
		Intro:        cfg.RuleSearch.Intro,
		Kind:         cfg.RuleSearch.Kind,
		LastChapter:  cfg.RuleSearch.LastChapter,
		UpdateTime:   cfg.RuleSearch.UpdateTime,
		BookURL:      cfg.RuleSearch.BookURL,
		CoverURL:     cfg.RuleSearch.CoverURL,
		WordCount:    cfg.RuleSearch.WordCount,
		CheckKeyWord: cfg.RuleSearch.CheckKeyWord,
	}
}

// Explore 发现页
func (s *Source) Explore(exploreURL string, page int) ([]model.Book, error) {
	if page <= 0 {
		page = 1
	}
	urlStr := strings.TrimSpace(exploreURL)
	if urlStr == "" {
		if len(s.cfg.ExploreEntries) == 0 {
			return nil, model.NewError(model.KindInvalidConfig, "source has no exploreUrl")
		}
		urlStr = s.cfg.ExploreEntries[0].URL
	}
	urlStr = strings.ReplaceAll(urlStr, "{{page}}", strconv.Itoa(page))
	b := Bindings{Page: page, BaseURL: s.cfg.URL}
	resp, err := s.Fetch(urlStr, b)
	if err != nil {
		return nil, err
	}
	books, err := ParseBookList(s, resp.Body, resp.FinalURL, exploreListRule(s.cfg), ListOptions{Page: page})
	if err != nil {
		return nil, err
	}
	return s.attachOrigin(books), nil
}

func exploreListRule(cfg *model.Source) ListRule {
	return ListRule{
		BookList:    cfg.RuleExplore.BookList,
		Name:        cfg.RuleExplore.Name,
		Author:      cfg.RuleExplore.Author,
		Intro:       cfg.RuleExplore.Intro,
		Kind:        cfg.RuleExplore.Kind,
		LastChapter: cfg.RuleExplore.LastChapter,
		UpdateTime:  cfg.RuleExplore.UpdateTime,
		BookURL:     cfg.RuleExplore.BookURL,
		CoverURL:    cfg.RuleExplore.CoverURL,
		WordCount:   cfg.RuleExplore.WordCount,
	}
}

func (s *Source) attachOrigin(books []model.Book) []model.Book {
	for i := range books {
		books[i].Origin = s.cfg.URL
		books[i].OriginName = s.cfg.Name
	}
	return books
}

// GetBookInfoByURL 抓取详情页
func (s *Source) GetBookInfoByURL(bookURL, name string) (*model.Book, error) {
	bookURL = strings.TrimSpace(bookURL)
	if bookURL == "" {
		return nil, model.NewError(model.KindInvalidConfig, "bookUrl is required")
	}
	book := &model.Book{Name: name, BookURL: bookURL, Origin: s.cfg.URL, OriginName: s.cfg.Name}
	return s.GetBookInfo(book)
}

// GetBookInfo 抓取详情页
func (s *Source) GetBookInfo(book *model.Book) (*model.Book, error) {
	if book == nil || strings.TrimSpace(book.BookURL) == "" {
		return nil, model.NewError(model.KindInvalidConfig, "book.bookUrl is required")
	}
	if book.Origin == "" {
		book.Origin = s.cfg.URL
	}
	resp, err := s.Fetch(book.BookURL, Bindings{BaseURL: book.BookURL, Book: book})
	if err != nil {
		return nil, err
	}
	info, err := ParseBookInfo(s, book, resp.Body, resp.FinalURL, s.cfg.RuleBookInfo, book.Name)
	if err != nil {
		return nil, err
	}
	if info.Intro != "" {
		info.Intro = FormatContent(info.Intro)
	}
	info.Origin = s.cfg.URL
	info.OriginName = s.cfg.Name
	return info, nil
}

// GetChapterList 抓取目录
func (s *Source) GetChapterList(book *model.Book) ([]model.Chapter, error) {
	if book == nil {
		return nil, model.NewError(model.KindInvalidConfig, "book is required")
	}
	tocURL := firstNonEmpty(book.TocURL, book.BookURL)
	if tocURL == "" {
		return nil, model.NewError(model.KindInvalidConfig, "book.tocUrl or book.bookUrl is required")
	}
	resp, err := s.Fetch(tocURL, Bindings{BaseURL: tocURL, Book: book})
	if err != nil {
		return nil, err
	}
	chapters, err := ParseChapterList(s, book, resp.Body, resp.FinalURL, s.cfg.RuleToc)
	if err != nil {
		return nil, err
	}
	for i := range chapters {
		chapters[i].BookURL = book.BookURL
	}
	return chapters, nil
}

// GetContentByURL 单章正文（不依赖目录）
func (s *Source) GetContentByURL(chapterURL, title string) (*model.Content, error) {
	chapterURL = strings.TrimSpace(chapterURL)
	if chapterURL == "" {
		return nil, model.NewError(model.KindInvalidConfig, "chapterUrl is required")
	}
	chapter := &model.Chapter{URL: chapterURL, Title: title}
	return s.GetContent(chapter, &model.Book{BookURL: chapterURL, Origin: s.cfg.URL})
}

// GetContent 抓取正文
func (s *Source) GetContent(chapter *model.Chapter, book *model.Book) (*model.Content, error) {
	if chapter == nil || strings.TrimSpace(chapter.URL) == "" {
		return nil, model.NewError(model.KindInvalidConfig, "chapter.url is required")
	}
	if book == nil {
		book = &model.Book{BookURL: chapter.BookURL, Origin: s.cfg.URL}
	}
	resp, err := s.Fetch(chapter.URL, Bindings{BaseURL: chapter.URL, Book: book, Chapter: chapter})
	if err != nil {
		return nil, err
	}
	content, err := ParseContent(s, book, chapter, resp.Body, resp.FinalURL, s.cfg.RuleContent)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(content.Content) == "" {
		return nil, model.NewError(model.KindParseEmpty, "content rule produced empty text")
	}
	return content, nil
}

// SearchCacheKey 生成搜索缓存键
func SearchCacheKey(sourceURL, key string, page int) string {
	return fmt.Sprintf("legado:search:%s:%d:%s", sourceURL, page, key)
}

// ExtractCacheKeyFromURL 从请求 url 提取缓存键（对齐 Legado 的 cacheKey）
func ExtractCacheKeyFromURL(rawURL string) string {
	if m := cacheKeyPattern.FindStringSubmatch(rawURL); m != nil {
		return m[1]
	}
	return ""
}

var cacheKeyPattern = regexp.MustCompile(`(?i)[?&](?:key|searchkey|q|query|wd|word|kw)=([^&]*)`)
