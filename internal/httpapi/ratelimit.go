package httpapi

import (
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/labstack/echo/v4"
)

// rateLimiter 是每 IP 的固定窗口限流器（窗口 1 分钟）。
type rateLimiter struct {
	limit  int
	window time.Duration

	mu      sync.Mutex
	buckets map[string]*bucket
	lastGC  time.Time
}

type bucket struct {
	count int
	reset time.Time
}

func newRateLimiter(perMinute int) *rateLimiter {
	return &rateLimiter{
		limit:   perMinute,
		window:  time.Minute,
		buckets: map[string]*bucket{},
		lastGC:  time.Now(),
	}
}

// allow 返回该 IP 是否还有配额
func (l *rateLimiter) allow(ip string, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()

	if now.Sub(l.lastGC) > 5*time.Minute {
		for key, b := range l.buckets {
			if now.After(b.reset) {
				delete(l.buckets, key)
			}
		}
		l.lastGC = now
	}

	b, ok := l.buckets[ip]
	if !ok || now.After(b.reset) {
		l.buckets[ip] = &bucket{count: 1, reset: now.Add(l.window)}
		return true
	}
	if b.count >= l.limit {
		return false
	}
	b.count++
	return true
}

// rateLimitMiddleware 生成 echo 中间件；limit<=0 时原样放行。
func rateLimitMiddleware(limit int) echo.MiddlewareFunc {
	if limit <= 0 {
		return func(next echo.HandlerFunc) echo.HandlerFunc { return next }
	}
	l := newRateLimiter(limit)
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			if !l.allow(clientIP(c), time.Now()) {
				return echo.NewHTTPError(http.StatusTooManyRequests, ErrorBody{Error: Detail{Kind: "RATE_LIMITED", Message: "rate limit exceeded"}})
			}
			return next(c)
		}
	}
}

func clientIP(c echo.Context) string {
	if ip := c.RealIP(); ip != "" {
		if host, _, err := net.SplitHostPort(ip); err == nil {
			return host
		}
		return ip
	}
	return "unknown"
}
