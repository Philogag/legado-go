package httpapi_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"legado-go/internal/booksource"
	"legado-go/internal/httpapi"
	"legado-go/internal/model"
	"legado-go/internal/registry"
)

func fakeSite() *httptest.Server {
	mux := http.NewServeMux()
	page := func(w http.ResponseWriter, body string) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, body)
	}
	mux.HandleFunc("/search", func(w http.ResponseWriter, r *http.Request) {
		page(w, `<div class="item"><a href="/book/1">棋 之书</a><span class="author">作者甲</span></div>`)
	})
	mux.HandleFunc("/book/1", func(w http.ResponseWriter, r *http.Request) {
		page(w, `<h1>测试之书</h1><span class="author">作者甲</span><a class="toc" href="/toc/1">目录</a>`)
	})
	mux.HandleFunc("/toc/1", func(w http.ResponseWriter, r *http.Request) {
		page(w, `<ul class="chapters"><li><a href="/content/1-1">第一章</a></li></ul>`)
	})
	mux.HandleFunc("/content/1-1", func(w http.ResponseWriter, r *http.Request) {
		page(w, `<h1>第一章</h1><div id="content"><p>正文一</p><p>正文二</p></div>`)
	})
	return httptest.NewServer(mux)
}

func sourceConfig(base string) map[string]any {
	return map[string]any{
		"bookSourceName": "测试源",
		"bookSourceUrl":  base,
		"searchUrl":      "/search?q={{key}}",
		"ruleSearch": map[string]any{
			"bookList": ".item", "name": "a@text", "author": ".author@text", "bookUrl": "a@href",
		},
		"ruleBookInfo": map[string]any{"name": "h1@text", "author": ".author@text", "tocUrl": "a.toc@href"},
		"ruleToc": map[string]any{
			"chapterList": "ul.chapters li", "chapterName": "a@text", "chapterUrl": "a@href",
		},
		"ruleContent": map[string]any{"title": "h1@text", "content": "#content p"},
	}
}

func newTestServer(t *testing.T, token string) (*httptest.Server, *registry.Registry) {
	t.Helper()
	reg := registry.New(registry.Options{
		DefaultTTL:    time.Minute,
		MaxTTL:        time.Hour,
		MaxInstances:  8,
		SweepInterval: -1,
		SourceOptions: booksource.Options{Timeout: 5 * time.Second},
	})
	t.Cleanup(reg.Close)
	router := httpapi.NewRouter(httpapi.Options{Registry: reg, Token: token})
	srv := httptest.NewServer(router)
	t.Cleanup(srv.Close)
	return srv, reg
}

func doJSON(t *testing.T, method, url string, body any, headers map[string]string) (int, map[string]any) {
	t.Helper()
	var buf bytes.Buffer
	if body != nil {
		if err := json.NewEncoder(&buf).Encode(body); err != nil {
			t.Fatalf("encode: %v", err)
		}
	}
	req, err := http.NewRequest(method, url, &buf)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	defer resp.Body.Close()
	out := map[string]any{}
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

func TestInstanceLifecycle(t *testing.T) {
	site := fakeSite()
	defer site.Close()
	srv, _ := newTestServer(t, "")

	status, created := doJSON(t, http.MethodPost, srv.URL+"/api/v1/instances", map[string]any{
		"config": sourceConfig(site.URL),
		"ttl":    "2m",
		"name":   "棋源",
	}, nil)
	if status != http.StatusCreated {
		t.Fatalf("create: status %d body %v", status, created)
	}
	inst, _ := created["instance"].(map[string]any)
	id, _ := inst["id"].(string)
	if id == "" || inst["name"] != "棋源" || inst["ttlSeconds"].(float64) != 120 {
		t.Fatalf("unexpected instance: %v", inst)
	}

	// 搜索
	status, search := doJSON(t, http.MethodPost, srv.URL+"/api/v1/instances/"+id+"/search", map[string]any{
		"key": "棋", "page": 1,
	}, nil)
	if status != http.StatusOK {
		t.Fatalf("search: status %d body %v", status, search)
	}
	books, _ := search["books"].([]any)
	if len(books) != 1 {
		t.Fatalf("unexpected books: %v", books)
	}
	first, _ := books[0].(map[string]any)
	bookURL, _ := first["bookUrl"].(string)
	if first["name"] != "棋 之书" || bookURL == "" {
		t.Fatalf("unexpected book: %v", first)
	}

	// 详情
	status, infoResp := doJSON(t, http.MethodPost, srv.URL+"/api/v1/instances/"+id+"/bookinfo", map[string]any{
		"book": map[string]any{"name": "棋 之书", "bookUrl": bookURL},
	}, nil)
	if status != http.StatusOK {
		t.Fatalf("bookinfo: status %d body %v", status, infoResp)
	}
	info, _ := infoResp["book"].(map[string]any)
	if info["name"] != "测试之书" {
		t.Fatalf("unexpected info: %v", info)
	}
	tocURL, _ := info["tocUrl"].(string)

	// 目录
	status, tocResp := doJSON(t, http.MethodPost, srv.URL+"/api/v1/instances/"+id+"/chapters", map[string]any{
		"book": map[string]any{"name": "测试之书", "bookUrl": bookURL, "tocUrl": tocURL},
	}, nil)
	if status != http.StatusOK {
		t.Fatalf("chapters: status %d body %v", status, tocResp)
	}
	chapters, _ := tocResp["chapters"].([]any)
	if len(chapters) != 1 {
		t.Fatalf("unexpected chapters: %v", chapters)
	}
	chapter := chapters[0].(map[string]any)

	// 正文
	status, contentResp := doJSON(t, http.MethodPost, srv.URL+"/api/v1/instances/"+id+"/content", map[string]any{
		"book":    map[string]any{"name": "测试之书", "bookUrl": bookURL, "tocUrl": tocURL},
		"chapter": chapter,
	}, nil)
	if status != http.StatusOK {
		t.Fatalf("content: status %d body %v", status, contentResp)
	}
	content, _ := contentResp["content"].(map[string]any)
	if content["content"] != "正文一\n正文二" {
		t.Fatalf("unexpected content: %v", content)
	}

	// JS 求值
	status, evalResp := doJSON(t, http.MethodPost, srv.URL+"/api/v1/instances/"+id+"/eval", map[string]any{
		"script": `key.toUpperCase()`, "key": "棋",
	}, nil)
	if status != http.StatusOK || evalResp["result"] != "棋" {
		t.Fatalf("eval: status %d body %v", status, evalResp)
	}

	// touch
	status, touched := doJSON(t, http.MethodPost, srv.URL+"/api/v1/instances/"+id+"/touch", map[string]any{"ttl": "10m"}, nil)
	if status != http.StatusOK {
		t.Fatalf("touch: status %d body %v", status, touched)
	}
	if touched["instance"].(map[string]any)["ttlSeconds"].(float64) != 600 {
		t.Fatalf("touch did not update ttl: %v", touched)
	}

	// list / get
	status, list := doJSON(t, http.MethodGet, srv.URL+"/api/v1/instances", nil, nil)
	if status != http.StatusOK || len(list["instances"].([]any)) != 1 {
		t.Fatalf("list: status %d body %v", status, list)
	}
	status, got := doJSON(t, http.MethodGet, srv.URL+"/api/v1/instances/"+id, nil, nil)
	if status != http.StatusOK || got["instance"].(map[string]any)["id"] != id {
		t.Fatalf("get: status %d body %v", status, got)
	}

	// delete
	status, _ = doJSON(t, http.MethodDelete, srv.URL+"/api/v1/instances/"+id, nil, nil)
	if status != http.StatusNoContent {
		t.Fatalf("delete: status %d", status)
	}
	status, missing := doJSON(t, http.MethodGet, srv.URL+"/api/v1/instances/"+id, nil, nil)
	if status != http.StatusNotFound || missing["error"].(map[string]any)["kind"] != "NOT_FOUND" {
		t.Fatalf("after delete: status %d body %v", status, missing)
	}
}

func TestExpiredInstanceReturns410(t *testing.T) {
	site := fakeSite()
	defer site.Close()
	reg := registry.New(registry.Options{DefaultTTL: 30 * time.Millisecond, SweepInterval: -1,
		SourceOptions: booksource.Options{Timeout: 5 * time.Second}})
	defer reg.Close()
	srv := httptest.NewServer(httpapi.NewRouter(httpapi.Options{Registry: reg}))
	defer srv.Close()

	entry, err := reg.Create(configFor(site.URL), 0, "")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	time.Sleep(60 * time.Millisecond)
	status, body := doJSON(t, http.MethodGet, srv.URL+"/api/v1/instances/"+entry.ID, nil, nil)
	if status != http.StatusGone || body["error"].(map[string]any)["kind"] != "EXPIRED" {
		t.Fatalf("want 410 EXPIRED, got %d %v", status, body)
	}
}

func TestAuthAndValidation(t *testing.T) {
	site := fakeSite()
	defer site.Close()
	srv, _ := newTestServer(t, "s3cret")

	if status, _ := doJSON(t, http.MethodGet, srv.URL+"/api/v1/instances", nil, nil); status != http.StatusUnauthorized {
		t.Fatalf("want 401 without token, got %d", status)
	}
	status, list := doJSON(t, http.MethodGet, srv.URL+"/api/v1/instances", nil, map[string]string{"Authorization": "Bearer s3cret"})
	if status != http.StatusOK {
		t.Fatalf("want 200 with token, got %d %v", status, list)
	}

	status, validated := doJSON(t, http.MethodPost, srv.URL+"/api/v1/sources/validate", map[string]any{
		"config": sourceConfig(site.URL),
	}, map[string]string{"Authorization": "Bearer s3cret"})
	if status != http.StatusOK || validated["valid"] != true || validated["name"] != "测试源" {
		t.Fatalf("validate: status %d body %v", status, validated)
	}

	status, invalid := doJSON(t, http.MethodPost, srv.URL+"/api/v1/instances", map[string]any{"config": map[string]any{}}, map[string]string{"Authorization": "Bearer s3cret"})
	if status != http.StatusBadRequest || invalid["error"].(map[string]any)["kind"] != "INVALID_CONFIG" {
		t.Fatalf("want 400 INVALID_CONFIG, got %d %v", status, invalid)
	}

	status, health := doJSON(t, http.MethodGet, srv.URL+"/healthz", nil, nil)
	if status != http.StatusOK || health["status"] != "ok" {
		t.Fatalf("healthz: %d %v", status, health)
	}
}

func TestParseFromJSONArray(t *testing.T) {
	site := fakeSite()
	defer site.Close()
	srv, _ := newTestServer(t, "")

	array := fmt.Sprintf(`[%s]`, mustJSON(sourceConfig(site.URL)))
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/api/v1/instances", bytes.NewBufferString(array))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("array create: status %d", resp.StatusCode)
	}
}

// configFor 把 map 形式的书源配置转成 model.Config。
func configFor(base string) *model.Config {
	cfg, err := model.ParseConfig([]byte(mustJSON(sourceConfig(base))))
	if err != nil {
		panic(err)
	}
	return cfg
}

func mustJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return string(b)
}
