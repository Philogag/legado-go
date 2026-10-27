package analyzeurl_test

import (
	"fmt"
	"testing"

	"legado-go/internal/analyzeurl"
)

type fakeCtx struct {
	vars map[string]string
	ua   string
}

func (c *fakeCtx) EvalJS(js string) (any, error) {
	if v, ok := c.vars[js]; ok {
		return v, nil
	}
	return nil, fmt.Errorf("unknown binding %q", js)
}

func (c *fakeCtx) DefaultHeaders() map[string]string { return map[string]string{"User-Agent": c.ua} }
func (c *fakeCtx) CookieGet(url string) string       { return "sid=1" }
func (c *fakeCtx) CookiesEnabled() bool              { return true }

func newCtx() *fakeCtx {
	return &fakeCtx{vars: map[string]string{"key": "测试", "page": "2"}, ua: "ua"}
}

func TestSplitURLRule(t *testing.T) {
	cases := []struct {
		in     string
		url    string
		method string
		body   string
	}{
		{`/search?q=1`, "/search?q=1", "", ""},
		{`/api,{"method":"POST","body":"k={{key}}"}`, "/api", "POST", "k={{key}}"},
		{`/api{"method":"POST","body":"k={{key}}"}`, "/api", "POST", "k={{key}}"},
		{`/x,{"headers":{"A":"B"}}`, "/x", "", ""},
	}
	for _, tc := range cases {
		u, opt := analyzeurl.SplitURLRule(tc.in)
		if u != tc.url {
			t.Fatalf("%q: want url %q, got %q", tc.in, tc.url, u)
		}
		if opt.Method != tc.method {
			t.Fatalf("%q: want method %q, got %q", tc.in, tc.method, opt.Method)
		}
		if tc.body != "" && opt.Body != tc.body {
			t.Fatalf("%q: want body %q, got %q", tc.in, tc.body, opt.Body)
		}
	}

	// 带 `{"` 的查询串不应被误判成选项块
	u, opt := analyzeurl.SplitURLRule(`/search?q={"a":1}`)
	if u != `/search?q={"a":1}` || opt.Method != "" {
		t.Fatalf("false positive option parse: %q %+v", u, opt)
	}
}

func TestReplaceTemplate(t *testing.T) {
	got, err := analyzeurl.ReplaceTemplate("q={{key}}&p={{page}}&raw=1", func(name string) (any, error) {
		return newCtx().EvalJS(name)
	})
	if err != nil {
		t.Fatalf("replace: %v", err)
	}
	if got != "q=测试&p=2&raw=1" {
		t.Fatalf("unexpected: %q", got)
	}
}

func TestAnalyzeResolvesURLAndOptions(t *testing.T) {
	parsed, err := analyzeurl.Analyze(newCtx(), `/api/search?q={{key}},{"method":"POST","headers":{"X-Test":"1"},"body":"k={{key}}&p={{page}}","charset":"gbk","retry":2}`, map[string]any{"baseUrl": "http://site.example"})
	if err != nil {
		t.Fatalf("analyze: %v", err)
	}
	if parsed.URL != "http://site.example/api/search?q=测试" {
		t.Fatalf("url not resolved: %q", parsed.URL)
	}
	if parsed.Method != "POST" || parsed.Body != "k=测试&p=2" {
		t.Fatalf("unexpected method/body: %+v", parsed)
	}
	if parsed.Headers["X-Test"] != "1" || parsed.Headers["User-Agent"] != "ua" {
		t.Fatalf("unexpected headers: %+v", parsed.Headers)
	}
	if parsed.Charset != "gbk" || parsed.Retry != 2 {
		t.Fatalf("unexpected charset/retry: %+v", parsed)
	}
}

func TestAnalyzeKeepAbsoluteURL(t *testing.T) {
	parsed, err := analyzeurl.Analyze(newCtx(), "https://other.example/x?q={{key}}", nil)
	if err != nil {
		t.Fatalf("analyze: %v", err)
	}
	if parsed.URL != "https://other.example/x?q=测试" {
		t.Fatalf("absolute url should be kept: %q", parsed.URL)
	}
}
