// Package jsengine 提供书源规则里的 JS 执行环境（基于 goja）。
//
// 暴露给 JS 的全局对象与阅读 App 保持一致：
//
//	java.ajax / ajaxAll / connect / get / post / head / putVar / getVar
//	source.get / put / getVariable / setVariable / getHeaderMap / ...
//	book / chapter（当前上下文，可读写字段）
//	cookie.getCookie / setCookie / replaceCookie / removeCookie
//	cache.put / get / delete
//	result / baseUrl / title / src / nextChapterUrl / page / key
//
// 与 TS 版的差别：Go 侧同步调用，`java.ajax` 直接阻塞当前 goroutine，
// 因此规则里写的同步 JS 可以正常工作；同一个书源实例的 JS 执行串行化。
package jsengine

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/dop251/goja"

	"legado-go/internal/model"
)

// Host 是 JS 侧需要回调的宿主能力（书源实例实现）
type Host interface {
	Ajax(url string) (string, error)
	AjaxAll(urls []string) ([]any, error)
	Connect(url string) (map[string]any, error)
	HTTPGet(url string, headers map[string]string) (string, error)
	HTTPPost(url, body string, headers map[string]string) (string, error)
	HTTPHead(url string) (string, error)

	PutVar(key, value string)
	GetVar(key string) (string, bool)

	SourceVariable() string
	SourceSetVariable(json string)
	SourceGet(key string) string
	SourcePut(key, value string)
	SourceHeaderMap() map[string]string
	SourceLoginHeaderMap() map[string]string
	SourcePutLoginHeader(key, value string)
	SourceRemoveLoginHeader(key string)
	SourceLoginInfo(key string) string
	SourceLoginInfoMap() map[string]string

	BookVariable() string
	BookSetVariable(value string)
	BookGet(key string) any
	BookPut(key string, value any)

	ChapterGet(key string) any
	ChapterPut(key string, value any)

	CookieGet(url string) string
	CookieSet(url, cookie string)
	CookieReplace(url, cookie string)
	CookieRemove(url string)

	CachePut(key, value string, ttl time.Duration)
	CacheGet(key string) (string, bool)
	CacheDelete(key string)

	JsLib() string
	Log(line string)
}

// Bindings 是一次 JS 执行的上下文变量
type Bindings struct {
	BaseURL        string
	Page           int
	Key            string
	Title          string
	Src            string
	NextChapterURL string
	Result         any
	Book           *model.Book
	Chapter        *model.Chapter
}

// Engine 是一个书源实例独占的 JS 运行时
type Engine struct {
	mu        sync.Mutex
	host      Host
	vm        *goja.Runtime
	libLoaded string
	closed    bool
}

// New 创建引擎
func New(host Host) *Engine {
	return &Engine{host: host, vm: goja.New()}
}

// Close 释放运行时
func (e *Engine) Close() error {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.closed = true
	e.vm = nil
	return nil
}

var returnPattern = regexp.MustCompile(`(?m)\breturn\b`)

// NormalizeJS 预处理 JS 片段：`@@` 视为换行；含 return 的片段包进函数体执行。
func NormalizeJS(src string) string {
	code := strings.TrimSpace(src)
	code = strings.ReplaceAll(code, "@@", "\n")
	lower := strings.ToLower(code)
	if returnPattern.MatchString(code) &&
		!strings.HasPrefix(lower, "function") &&
		!strings.HasPrefix(lower, "async") &&
		!strings.HasPrefix(lower, "(") {
		return "(function(){\n" + code + "\n})()"
	}
	return code
}

// Eval 执行一段 JS 并返回结果
func (e *Engine) Eval(code string, b Bindings) (any, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closed || e.vm == nil {
		return nil, model.NewError(model.KindClosed, "JS 引擎已关闭")
	}
	vm := e.vm
	e.installGlobals(vm, b)
	if err := e.ensureLib(vm); err != nil {
		return nil, err
	}
	value, err := vm.RunString(NormalizeJS(code))
	if err != nil {
		return nil, model.WrapError(model.KindParseFailed, err, "JS 执行失败: %s", firstLine(err.Error()))
	}
	if value == nil || goja.IsUndefined(value) || goja.IsNull(value) {
		return nil, nil
	}
	return value.Export(), nil
}

func firstLine(s string) string {
	if idx := strings.IndexByte(s, '\n'); idx > 0 {
		return s[:idx]
	}
	return s
}

// ensureLib 装载 jsLib（同一份 jsLib 只装载一次）
func (e *Engine) ensureLib(vm *goja.Runtime) error {
	lib := e.host.JsLib()
	if lib == "" || lib == e.libLoaded {
		return nil
	}
	e.libLoaded = lib
	if _, err := vm.RunString(lib); err != nil {
		return model.WrapError(model.KindParseFailed, err, "jsLib 装载失败: %s", firstLine(err.Error()))
	}
	return nil
}

// installGlobals 每次执行前刷新绑定（result / book / chapter 等随时变化）
func (e *Engine) installGlobals(vm *goja.Runtime, b Bindings) {
	host := e.host
	set := func(name string, v any) { _ = vm.Set(name, v) }

	set("baseUrl", b.BaseURL)
	page := b.Page
	if page <= 0 {
		page = 1
	}
	set("page", page)
	if b.Key == "" {
		set("key", goja.Null())
	} else {
		set("key", b.Key)
	}
	if b.Title == "" {
		set("title", goja.Null())
	} else {
		set("title", b.Title)
	}
	if b.Src == "" {
		set("src", goja.Null())
	} else {
		set("src", b.Src)
	}
	if b.NextChapterURL == "" {
		set("nextChapterUrl", goja.Null())
	} else {
		set("nextChapterUrl", b.NextChapterURL)
	}
	if b.Result == nil {
		set("result", goja.Null())
	} else {
		set("result", b.Result)
	}

	javaObj := map[string]any{
		"ajax": func(url string) (string, error) { return host.Ajax(url) },
		"ajaxAll": func(urls []string) (any, error) {
			list, err := host.AjaxAll(urls)
			if err != nil {
				return nil, err
			}
			return list, nil
		},
		"connect": func(url string) (any, error) {
			m, err := host.Connect(url)
			if err != nil {
				return nil, err
			}
			return m, nil
		},
		"get": func(url string, headers map[string]string) (string, error) {
			return host.HTTPGet(url, headers)
		},
		"post": func(url, body string, headers map[string]string) (string, error) {
			return host.HTTPPost(url, body, headers)
		},
		"head": func(url string) (string, error) { return host.HTTPHead(url) },
		"putVar": func(key, value string) {
			host.PutVar(key, value)
		},
		"getVar": func(key string) goja.Value {
			if v, ok := host.GetVar(key); ok {
				return vm.ToValue(v)
			}
			return goja.Null()
		},
		"put": func(key string, value any) { host.CachePut(key, cacheString(value), 0) },
		"getFromCache": func(key string) any {
			if v, ok := host.CacheGet(key); ok {
				return v
			}
			return goja.Null()
		},
		"putGlobal": func(key, value string) { host.PutVar(key, value) },
		"getGlobal": func(key string) any {
			if v, ok := host.GetVar(key); ok {
				return v
			}
			return goja.Null()
		},
		"log": func(msg string) { host.Log(msg) },
	}
	for k, v := range javaExtras() {
		javaObj[k] = v
	}
	set("java", javaObj)

	set("source", map[string]any{
		"getVariable":       func() string { return host.SourceVariable() },
		"setVariable":       func(json string) { host.SourceSetVariable(json) },
		"get":               func(key string) string { return host.SourceGet(key) },
		"put":               func(key, value string) { host.SourcePut(key, value) },
		"getHeaderMap":      func() any { return host.SourceHeaderMap() },
		"getLoginHeaderMap": func() any { return host.SourceLoginHeaderMap() },
		"putLoginHeader":    func(key, value string) { host.SourcePutLoginHeader(key, value) },
		"removeLoginHeader": func(key string) { host.SourceRemoveLoginHeader(key) },
		"getLoginInfo":      func(key string) any { return host.SourceLoginInfo(key) },
		"getLoginInfoMap":   func() any { return host.SourceLoginInfoMap() },
	})

	set("cookie", map[string]any{
		"getCookie":     func(url string) string { return host.CookieGet(url) },
		"setCookie":     func(url, cookie string) { host.CookieSet(url, cookie) },
		"replaceCookie": func(url, cookie string) { host.CookieReplace(url, cookie) },
		"removeCookie":  func(url string) { host.CookieRemove(url) },
	})

	set("cache", map[string]any{
		"put": func(key, value string, ttlMs int64) {
			host.CachePut(key, value, time.Duration(ttlMs)*time.Millisecond)
		},
		"get":    func(key string) any { v, _ := host.CacheGet(key); return v },
		"delete": func(key string) { host.CacheDelete(key) },
	})

	set("console", map[string]any{
		"log": func(msg string) { host.Log(msg) },
	})

	if b.Book != nil {
		set("book", bookObject(vm, host, b.Book))
	} else {
		set("book", goja.Null())
	}
	if b.Chapter != nil {
		set("chapter", chapterObject(vm, host, b.Chapter))
	} else {
		set("chapter", goja.Null())
	}
}

func bookObject(vm *goja.Runtime, host Host, book *model.Book) goja.Value {
	obj := vm.NewObject()
	_ = obj.Set("name", book.Name)
	_ = obj.Set("author", book.Author)
	_ = obj.Set("kind", book.Kind)
	_ = obj.Set("intro", book.Intro)
	_ = obj.Set("coverUrl", book.CoverURL)
	_ = obj.Set("tocUrl", book.TocURL)
	_ = obj.Set("bookUrl", book.BookURL)
	_ = obj.Set("origin", book.Origin)
	_ = obj.Set("originName", book.OriginName)
	_ = obj.Set("lastChapter", book.LastChapter)
	_ = obj.Set("updateTime", book.UpdateTime)
	_ = obj.Set("wordCount", book.WordCount)
	_ = obj.Set("getVariable", func() string { return host.BookVariable() })
	_ = obj.Set("setVariable", func(v string) { host.BookSetVariable(v) })
	_ = obj.Set("get", func(key string) any { return host.BookGet(key) })
	_ = obj.Set("put", func(key string, value any) { host.BookPut(key, value) })
	return obj
}

func chapterObject(vm *goja.Runtime, host Host, chapter *model.Chapter) goja.Value {
	obj := vm.NewObject()
	_ = obj.Set("title", chapter.Title)
	_ = obj.Set("url", chapter.URL)
	_ = obj.Set("baseUrl", chapter.BaseURL)
	_ = obj.Set("index", chapter.Index)
	_ = obj.Set("bookUrl", chapter.BookURL)
	_ = obj.Set("isVolume", chapter.IsVolume)
	_ = obj.Set("isVip", chapter.IsVIP)
	_ = obj.Set("isPay", chapter.IsPay)
	_ = obj.Set("tag", chapter.Tag)
	_ = obj.Set("wordCount", chapter.WordCount)
	_ = obj.Set("get", func(key string) any { return host.ChapterGet(key) })
	_ = obj.Set("put", func(key string, value any) { host.ChapterPut(key, value) })
	return obj
}

// cacheString 把任意 JS 值转成字符串存进缓存（对应 legado 的 java.put）。
func cacheString(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return t
	case []byte:
		return string(t)
	}
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprint(v)
	}
	return string(b)
}
