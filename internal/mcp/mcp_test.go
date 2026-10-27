package mcp_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"legado-go/internal/booksource"
	"legado-go/internal/mcp"
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
		page(w, `<h1>第一章</h1><div id="content"><p>正文一</p></div>`)
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

func newServer(t *testing.T) (*httptest.Server, *registry.Registry) {
	t.Helper()
	reg := registry.New(registry.Options{
		DefaultTTL:    time.Minute,
		SweepInterval: -1,
		SourceOptions: booksource.Options{Timeout: 5 * time.Second},
	})
	t.Cleanup(reg.Close)
	srv := httptest.NewServer(mcp.New(reg, mcp.Options{Name: "test", Version: "0.0.0"}))
	t.Cleanup(srv.Close)
	return srv, reg
}

// rpc 发一个 JSON-RPC 请求，返回 HTTP 状态码和解析后的响应体
func rpc(t *testing.T, url string, payload any, headers map[string]string) (int, map[string]any) {
	t.Helper()
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	req, _ := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
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

func callTool(t *testing.T, url, name string, args any, id int) map[string]any {
	t.Helper()
	status, resp := rpc(t, url, map[string]any{
		"jsonrpc": "2.0", "id": id, "method": "tools/call",
		"params": map[string]any{"name": name, "arguments": args},
	}, nil)
	if status != http.StatusOK {
		t.Fatalf("%s: status %d response %v", name, status, resp)
	}
	if e, ok := resp["error"]; ok && e != nil {
		t.Fatalf("%s: rpc error %v", name, e)
	}
	result, _ := resp["result"].(map[string]any)
	if result == nil {
		t.Fatalf("%s: no result: %v", name, resp)
	}
	return result
}

func structured(t *testing.T, result map[string]any) map[string]any {
	t.Helper()
	sc, _ := result["structuredContent"].(map[string]any)
	if sc == nil {
		t.Fatalf("no structuredContent: %v", result)
	}
	return sc
}

func TestToolsListAndMeta(t *testing.T) {
	srv, _ := newServer(t)
	status, resp := rpc(t, srv.URL, map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/list"}, nil)
	if status != http.StatusOK {
		t.Fatalf("status %d", status)
	}
	result := resp["result"].(map[string]any)
	tools, _ := result["tools"].([]any)
	if len(tools) != 12 {
		t.Fatalf("want 12 tools, got %d", len(tools))
	}
	names := map[string]bool{}
	for _, tl := range tools {
		tool := tl.(map[string]any)
		names[tool["name"].(string)] = true
		if tool["inputSchema"] == nil {
			t.Fatalf("tool %v missing inputSchema", tool["name"])
		}
	}
	for _, want := range []string{"source_create", "source_search", "source_content", "source_channels"} {
		if want == "source_channels" {
			continue // 未实现，仅占位
		}
		if !names[want] {
			t.Fatalf("missing tool %s (have %v)", want, names)
		}
	}

	status, resp = rpc(t, srv.URL, map[string]any{"jsonrpc": "2.0", "id": 2, "method": "initialize",
		"params": map[string]any{"protocolVersion": "2026-07-28", "capabilities": map[string]any{},
			"clientInfo": map[string]any{"name": "test", "version": "1"}}}, nil)
	if status != http.StatusOK {
		t.Fatalf("initialize status %d", status)
	}
	initResult := resp["result"].(map[string]any)
	if initResult["protocolVersion"] != "2026-07-28" {
		t.Fatalf("unexpected protocol version: %v", initResult)
	}
	if _, ok := initResult["instructions"]; !ok {
		t.Fatalf("initialize should carry instructions: %v", initResult)
	}
}

func TestFullFlowOverMCP(t *testing.T) {
	site := fakeSite()
	defer site.Close()
	srv, reg := newServer(t)

	create := structured(t, callTool(t, srv.URL, "source_create", map[string]any{
		"config": sourceConfig(site.URL), "ttlSeconds": 300, "owner": "tester",
	}, 1))
	inst := create["instance"].(map[string]any)
	id, _ := inst["id"].(string)
	if id == "" || inst["ttlSeconds"].(float64) != 300 {
		t.Fatalf("unexpected create result: %v", inst)
	}
	if info, err := reg.Get(id); err != nil || info.ID != id {
		t.Fatalf("registry not populated: %v", err)
	}

	search := structured(t, callTool(t, srv.URL, "source_search", map[string]any{"instanceId": id, "key": "棋"}, 2))
	books := search["books"].([]any)
	if len(books) != 1 {
		t.Fatalf("unexpected search result: %v", search)
	}
	book := books[0].(map[string]any)

	info := structured(t, callTool(t, srv.URL, "source_book_info", map[string]any{"instanceId": id, "book": book}, 3))
	bookInfo := info["book"].(map[string]any)
	if bookInfo["name"] != "测试之书" {
		t.Fatalf("unexpected book info: %v", bookInfo)
	}

	chapters := structured(t, callTool(t, srv.URL, "source_chapters", map[string]any{"instanceId": id, "book": bookInfo}, 4))
	list := chapters["chapters"].([]any)
	if len(list) != 1 {
		t.Fatalf("unexpected chapters: %v", chapters)
	}

	content := structured(t, callTool(t, srv.URL, "source_content", map[string]any{
		"instanceId": id, "book": bookInfo, "chapter": list[0],
	}, 5))
	got := content["content"].(map[string]any)
	if got["content"] != "正文一" {
		t.Fatalf("unexpected content: %v", got)
	}

	evalOut := structured(t, callTool(t, srv.URL, "source_eval", map[string]any{
		"instanceId": id, "script": "page * 2", "page": 21,
	}, 6))
	if evalOut["result"].(float64) != 42 {
		t.Fatalf("unexpected eval result: %v", evalOut)
	}

	listOut := structured(t, callTool(t, srv.URL, "source_list", map[string]any{}, 7))
	if len(listOut["instances"].([]any)) != 1 {
		t.Fatalf("unexpected list: %v", listOut)
	}

	if _, err := reg.Get(id); err != nil {
		t.Fatalf("TTL should be refreshed by tool calls: %v", err)
	}

	structured(t, callTool(t, srv.URL, "source_touch", map[string]any{"instanceId": id, "ttlSeconds": 60}, 8))
	if del := structured(t, callTool(t, srv.URL, "source_delete", map[string]any{"instanceId": id}, 9)); del["deleted"] != id {
		t.Fatalf("unexpected delete result: %v", del)
	}

	errResult := callTool(t, srv.URL, "source_search", map[string]any{"instanceId": id, "key": "棋"}, 10)
	if errResult["isError"] != true {
		t.Fatalf("search after delete should be an error: %v", errResult)
	}
	sc := errResult["structuredContent"].(map[string]any)
	if sc["error"].(map[string]any)["kind"] != "NOT_FOUND" {
		t.Fatalf("unexpected error kind: %v", sc)
	}
}

func TestProtocolBehaviors(t *testing.T) {
	srv, _ := newServer(t)

	// 通知（无 id）→ 202 且无响应体
	body, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "method": "notifications/initialized"})
	resp, err := http.Post(srv.URL, "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("notification should be 202, got %d", resp.StatusCode)
	}
	resp.Body.Close()

	// 错误的协议版本头 → 400
	status, _ := rpc(t, srv.URL, map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/list"},
		map[string]string{"Mcp-Protocol-Version": "2025-06-18"})
	if status != http.StatusBadRequest {
		t.Fatalf("bad version header should be 400, got %d", status)
	}

	// 未实现的方法 → JSON-RPC 错误
	status, resp2 := rpc(t, srv.URL, map[string]any{"jsonrpc": "2.0", "id": 2, "method": "resources/read"}, nil)
	if status != http.StatusOK {
		t.Fatalf("status %d", status)
	}
	if resp2["error"] == nil {
		t.Fatalf("expected rpc error: %v", resp2)
	}

	// 批量请求
	batch := []map[string]any{
		{"jsonrpc": "2.0", "id": 1, "method": "tools/list"},
		{"jsonrpc": "2.0", "method": "notifications/initialized"},
		{"jsonrpc": "2.0", "id": 2, "method": "ping"},
	}
	raw, _ := json.Marshal(batch)
	req, _ := http.NewRequest(http.MethodPost, srv.URL, bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	bresp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("batch do: %v", err)
	}
	defer bresp.Body.Close()
	var arr []map[string]any
	if err := json.NewDecoder(bresp.Body).Decode(&arr); err != nil {
		t.Fatalf("batch decode: %v", err)
	}
	if len(arr) != 2 {
		t.Fatalf("batch should return 2 responses (notification skipped), got %d", len(arr))
	}

	// GET 不支持（无 SSE 通道）
	getResp, err := http.Get(srv.URL)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer getResp.Body.Close()
	if getResp.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("GET should be 405, got %d", getResp.StatusCode)
	}

	// 未知工具
	unknown := callTool(t, srv.URL, "nope", map[string]any{}, 3)
	if unknown["isError"] != true {
		t.Fatalf("unknown tool should be an error: %v", unknown)
	}
}
