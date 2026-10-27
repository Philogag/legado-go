package httpapi_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"legado-go/internal/httpapi"
	"legado-go/internal/registry"
)

func TestRateLimitRejects(t *testing.T) {
	reg := registry.New(registry.Options{SweepInterval: -1})
	defer reg.Close()
	router := httpapi.NewRouter(httpapi.Options{Registry: reg, RateLimit: 2})

	do := func() int {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/instances", nil)
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		return rec.Code
	}

	if code := do(); code != http.StatusOK {
		t.Fatalf("first request: %d", code)
	}
	if code := do(); code != http.StatusOK {
		t.Fatalf("second request: %d", code)
	}
	if code := do(); code != http.StatusTooManyRequests {
		t.Fatalf("third request should be limited, got %d", code)
	}
}

func TestTokenAuth(t *testing.T) {
	reg := registry.New(registry.Options{SweepInterval: -1})
	defer reg.Close()
	router := httpapi.NewRouter(httpapi.Options{Registry: reg, Token: "s3cret"})

	req := httptest.NewRequest(http.MethodGet, "/api/v1/instances", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("want 401 without token, got %d", rec.Code)
	}

	req = httptest.NewRequest(http.MethodGet, "/api/v1/instances", nil)
	req.Header.Set("Authorization", "Bearer s3cret")
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200 with token, got %d: %s", rec.Code, rec.Body.String())
	}

	// 健康检查不需要鉴权
	req = httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "ok") {
		t.Fatalf("healthz should be public, got %d: %s", rec.Code, rec.Body.String())
	}
}
