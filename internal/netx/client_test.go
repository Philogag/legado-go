package netx_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"legado-go/internal/netx"
)

func TestFetchDecodesGBK(t *testing.T) {
	// "中文" in GBK
	gbk := []byte{0xD6, 0xD0, 0xCE, 0xC4}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=gbk")
		w.Write(gbk)
	}))
	defer srv.Close()

	c := netx.New(5 * time.Second)
	resp, err := c.Do(netx.Request{URL: srv.URL, Method: "GET"})
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if resp.Body != "中文" {
		t.Fatalf("charset decode failed: %q", resp.Body)
	}
}

func TestPostHeadersAndCookies(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body := make([]byte, 64)
		n, _ := r.Body.Read(body)
		w.Header().Set("Set-Cookie", "sid=abc; Path=/")
		w.Header().Set("Content-Type", "text/plain")
		w.Write([]byte(r.Method + "|" + r.Header.Get("X-Test") + "|" + string(body[:n])))
	}))
	defer srv.Close()

	c := netx.New(5 * time.Second)
	resp, err := c.Do(netx.Request{
		URL:     srv.URL,
		Method:  "POST",
		Body:    "k=v",
		Headers: map[string]string{"X-Test": "1"},
	})
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	if resp.Body != "POST|1|k=v" {
		t.Fatalf("unexpected body: %q", resp.Body)
	}
	if got := c.Cookies.Get(srv.URL); got != "sid=abc" {
		t.Fatalf("cookie store: %q", got)
	}
}

func TestClientErrorStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "nope", http.StatusTeapot)
	}))
	defer srv.Close()

	// 上游语义：非 2xx 不抛错，原样返回（由调用方/规则决定如何处理）
	c := netx.New(5 * time.Second)
	resp, err := c.Do(netx.Request{URL: srv.URL, Method: "GET"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Code != http.StatusTeapot {
		t.Fatalf("want 418, got %d", resp.Code)
	}
	if !strings.Contains(resp.Body, "nope") {
		t.Fatalf("want body passthrough, got %q", resp.Body)
	}
}

func TestCookieStoreDomainScope(t *testing.T) {
	s := netx.NewCookieStore()
	s.Set("http://a.example/path", "x=1; y=2")
	if got := s.Get("http://a.example/other"); got != "x=1; y=2" {
		t.Fatalf("cross-path cookie: %q", got)
	}
	if got := s.Get("http://b.example/"); got != "" {
		t.Fatalf("cookie leaked to other domain: %q", got)
	}
	s.Replace("http://a.example/", "x=9")
	if got := s.Get("http://a.example/"); got != "x=9; y=2" && got != "x=9" {
		t.Fatalf("replace: %q", got)
	}
	s.Remove("http://a.example/", "x")
	if got := s.Get("http://a.example/"); got == "x=9" {
		t.Fatalf("remove failed: %q", got)
	}
}
