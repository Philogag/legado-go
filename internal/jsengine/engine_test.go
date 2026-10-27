package jsengine_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"legado-go/internal/jsengine"
	"legado-go/internal/model"
)

// fakeHost 实现 jsengine.Host，只覆盖测试用到的部分。
type fakeHost struct {
	vars  map[string]string
	cache map[string]string
	urls  []string
}

func newHost() *fakeHost {
	return &fakeHost{vars: map[string]string{"k": "v"}, cache: map[string]string{}}
}

func (h *fakeHost) Ajax(url string) (string, error) {
	h.urls = append(h.urls, url)
	if strings.Contains(url, "fail") {
		return "", errors.New("ajax failed")
	}
	return "BODY:" + url, nil
}

func (h *fakeHost) AjaxAll(urls []string) ([]any, error) {
	out := make([]any, 0, len(urls))
	for _, u := range urls {
		b, _ := h.Ajax(u)
		out = append(out, b)
	}
	return out, nil
}

func (h *fakeHost) Connect(url string) (map[string]any, error) {
	return map[string]any{"url": url, "code": 200, "body": "BODY:" + url}, nil
}

func (h *fakeHost) HTTPGet(url string, headers map[string]string) (string, error) {
	return h.Ajax(url)
}

func (h *fakeHost) HTTPPost(url, body string, headers map[string]string) (string, error) {
	return "POST:" + body, nil
}

func (h *fakeHost) HTTPHead(url string) (string, error) { return "HEAD:" + url, nil }

func (h *fakeHost) PutVar(key, value string)         { h.vars[key] = value }
func (h *fakeHost) GetVar(key string) (string, bool) { v, ok := h.vars[key]; return v, ok }

func (h *fakeHost) SourceVariable() string             { return `{"a":1}` }
func (h *fakeHost) SourceSetVariable(json string)      {}
func (h *fakeHost) SourceGet(key string) string        { return "sv:" + key }
func (h *fakeHost) SourcePut(key, value string)        {}
func (h *fakeHost) SourceHeaderMap() map[string]string { return map[string]string{"UA": "x"} }
func (h *fakeHost) SourceLoginHeaderMap() map[string]string {
	return map[string]string{"L": "1"}
}
func (h *fakeHost) SourcePutLoginHeader(key, value string) {}
func (h *fakeHost) SourceRemoveLoginHeader(key string)     {}
func (h *fakeHost) SourceLoginInfo(key string) string      { return "li:" + key }
func (h *fakeHost) SourceLoginInfoMap() map[string]string  { return map[string]string{"u": "n"} }

func (h *fakeHost) BookVariable() string          { return `{"name":"b"}` }
func (h *fakeHost) BookSetVariable(value string)  {}
func (h *fakeHost) BookGet(key string) any        { return "bv:" + key }
func (h *fakeHost) BookPut(key string, value any) {}

func (h *fakeHost) ChapterGet(key string) any        { return "cv:" + key }
func (h *fakeHost) ChapterPut(key string, value any) {}

func (h *fakeHost) CookieGet(url string) string      { return "c=1" }
func (h *fakeHost) CookieSet(url, cookie string)     {}
func (h *fakeHost) CookieReplace(url, cookie string) {}
func (h *fakeHost) CookieRemove(url string)          {}

func (h *fakeHost) CachePut(key, value string, ttl time.Duration) { h.cache[key] = value }
func (h *fakeHost) CacheGet(key string) (string, bool)            { v, ok := h.cache[key]; return v, ok }
func (h *fakeHost) CacheDelete(key string)                        { delete(h.cache, key) }

func (h *fakeHost) JsLib() string   { return "var libValue = 42;" }
func (h *fakeHost) Log(line string) {}

func TestEvalBasics(t *testing.T) {
	e := jsengine.New(newHost())
	defer e.Close()

	out, err := e.Eval("1+1", jsengine.Bindings{})
	if err != nil || out.(int64) != 2 {
		t.Fatalf("arith: %v %v", out, err)
	}

	out, err = e.Eval("result + '!'", jsengine.Bindings{Result: "abc"})
	if err != nil || out != "abc!" {
		t.Fatalf("result binding: %v %v", out, err)
	}

	out, err = e.Eval("baseUrl + '|' + key + '|' + page", jsengine.Bindings{BaseURL: "http://a/", Key: "kw", Page: 3})
	if err != nil || out != "http://a/|kw|3" {
		t.Fatalf("bindings: %v %v", out, err)
	}

	// jsLib 只安装一次，且变量可见
	out, err = e.Eval("libValue", jsengine.Bindings{})
	if err != nil || out.(int64) != 42 {
		t.Fatalf("jsLib: %v %v", out, err)
	}

	// `@@` 在脚本里表示换行（多语句分隔）
	out, err = e.Eval("var a = 1@@var b = 2@@a + b", jsengine.Bindings{})
	if err != nil || out.(int64) != 3 {
		t.Fatalf("@@ separator: %v %v", out, err)
	}
	if _, err := e.Eval("throw new Error('boom')", jsengine.Bindings{}); err == nil {
		t.Fatal("expected js error")
	}
}

func TestJavaAPI(t *testing.T) {
	h := newHost()
	e := jsengine.New(h)
	defer e.Close()

	out, err := e.Eval("java.ajax('http://x/1')", jsengine.Bindings{})
	if err != nil || out != "BODY:http://x/1" {
		t.Fatalf("java.ajax: %v %v", out, err)
	}

	// java.get(url, headers) 是 HTTP GET（与上游一致），变量用 putVar/getVar
	out, err = e.Eval("java.get('http://x/2', {A:'1'})", jsengine.Bindings{})
	if err != nil || out != "BODY:http://x/2" {
		t.Fatalf("java.get: %v %v", out, err)
	}
	out, err = e.Eval("java.putVar('kk','vv'); java.getVar('kk')", jsengine.Bindings{})
	if err != nil || out != "vv" {
		t.Fatalf("java.putVar/getVar: %v %v", out, err)
	}
	if h.vars["kk"] != "vv" {
		t.Fatalf("var not persisted: %+v", h.vars)
	}

	// java.put/getFromCache 走缓存
	out, err = e.Eval("java.put('ck','cv'); java.getFromCache('ck')", jsengine.Bindings{})
	if err != nil || out != "cv" {
		t.Fatalf("java cache: %v %v", out, err)
	}

	// base64 / md5 / hex
	out, err = e.Eval("java.base64Encode('中文')", jsengine.Bindings{})
	if err != nil || out != "5Lit5paH" {
		t.Fatalf("base64: %v %v", out, err)
	}
	out, err = e.Eval("java.base64Decode('5Lit5paH')", jsengine.Bindings{})
	if err != nil || out != "中文" {
		t.Fatalf("base64 decode: %v %v", out, err)
	}
	out, err = e.Eval("java.md5Encode('abc')", jsengine.Bindings{})
	if err != nil || out != "900150983cd24fb0d6963f7d28e17f72" {
		t.Fatalf("md5: %v %v", out, err)
	}

	// 分页/变量
	out, err = e.Eval("java.putVar('p', String(page)); java.getVar('p')", jsengine.Bindings{Page: 2})
	if err != nil || out != "2" {
		t.Fatalf("page var: %v %v", out, err)
	}
}

func TestSourceBookChapterAndCache(t *testing.T) {
	e := jsengine.New(newHost())
	defer e.Close()

	if _, err := e.Eval("source.put('x','1')", jsengine.Bindings{}); err != nil {
		t.Fatalf("source.put: %v", err)
	}
	out, err := e.Eval("source.get('x')", jsengine.Bindings{})
	if err != nil || out != "sv:x" {
		t.Fatalf("source.get: %v %v", out, err)
	}
	out, err = e.Eval("source.getVariable()", jsengine.Bindings{})
	if err != nil || !strings.Contains(out.(string), "a") {
		t.Fatalf("source.getVariable: %v %v", out, err)
	}

	e2 := jsengine.New(newHost())
	defer e2.Close()
	out, err = e2.Eval("cache.put('k','v',60); cache.get('k')", jsengine.Bindings{})
	if err != nil || out != "v" {
		t.Fatalf("cache: %v %v", out, err)
	}

	book := &model.Book{Name: "书名", Author: "作者", BookURL: "http://b/1"}
	out, err = e.Eval("book.name", jsengine.Bindings{Book: book})
	if err != nil || out != "书名" {
		t.Fatalf("book binding: %v %v", out, err)
	}

	ch := &model.Chapter{Title: "第一章", URL: "http://c/1"}
	out, err = e.Eval("chapter.title", jsengine.Bindings{Chapter: ch})
	if err != nil || out != "第一章" {
		t.Fatalf("chapter binding: %v %v", out, err)
	}
}
