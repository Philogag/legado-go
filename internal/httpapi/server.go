package httpapi

import (
	"net/http"
	"strings"
	"time"

	"github.com/labstack/echo/v4"

	"legado-go/internal/mcp"
	"legado-go/internal/registry"
)

// Options 是 HTTP 服务配置
type Options struct {
	Registry *registry.Registry
	// MCP 为 nil 时不挂载 /mcp
	MCP *mcp.Server
	// Token 非空时要求 Authorization: Bearer <token> 或 X-API-Token
	Token string
	// RateLimit 每 IP 每分钟请求上限（0 表示不限）
	RateLimit int
	// Log 请求日志回调（不传则不打印）
	Log func(format string, args ...any)
}

// NewRouter 构建 echo 路由
func NewRouter(opts Options) *echo.Echo {
	e := echo.New()
	e.HideBanner = true
	e.HidePort = true
	e.HTTPErrorHandler = errorHandler

	a := &api{reg: opts.Registry}

	if opts.Log != nil {
		e.Use(requestLogger(opts.Log))
	}
	if opts.RateLimit > 0 {
		e.Use(rateLimitMiddleware(opts.RateLimit))
	}

	e.GET("/healthz", a.health)
	e.GET("/readyz", a.health)

	// /mcp 与 REST 共用同一套令牌（令牌为空时不鉴权）
	mcpAuth := []echo.MiddlewareFunc{}
	if opts.Token != "" {
		mcpAuth = append(mcpAuth, tokenAuth(opts.Token))
	}
	if opts.MCP != nil {
		e.Any("/mcp", echo.WrapHandler(opts.MCP), mcpAuth...)
	}

	v1 := e.Group("/api/v1")
	if opts.Token != "" {
		v1.Use(tokenAuth(opts.Token))
	}
	v1.GET("/meta", a.meta)
	v1.POST("/sources/validate", a.validate)
	v1.POST("/instances", a.createInstance)
	v1.GET("/instances", a.listInstances)
	v1.GET("/instances/:id", a.getInstance)
	v1.DELETE("/instances/:id", a.deleteInstance)
	v1.POST("/instances/:id/touch", a.touchInstance)
	v1.POST("/instances/:id/search", a.search)
	v1.POST("/instances/:id/explore", a.explore)
	v1.POST("/instances/:id/bookinfo", a.bookInfo)
	v1.POST("/instances/:id/chapters", a.chapters)
	v1.POST("/instances/:id/content", a.content)
	v1.POST("/instances/:id/eval", a.eval)
	return e
}

// errorHandler 把 HTTPError 的载荷原样写出
func errorHandler(err error, c echo.Context) {
	if c.Response().Committed {
		return
	}
	status := http.StatusInternalServerError
	payload := any(ErrorBody{Error: Detail{Kind: "INTERNAL", Message: err.Error()}})
	if he, ok := err.(*echo.HTTPError); ok {
		status = he.Code
		if body, ok := he.Message.(ErrorBody); ok {
			payload = body
		} else {
			payload = map[string]any{"error": map[string]any{"kind": "HTTP", "message": he.Message}}
		}
	}
	if c.Request().Method == http.MethodHead {
		_ = c.NoContent(status)
		return
	}
	_ = c.JSON(status, payload)
}

func requestLogger(log func(format string, args ...any)) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			start := time.Now()
			err := next(c)
			if err != nil {
				c.Error(err)
			}
			log("%s %s %d %s", c.Request().Method, c.Request().URL.Path,
				c.Response().Status, time.Since(start).Round(time.Millisecond))
			return nil
		}
	}
}

func tokenAuth(token string) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			got := strings.TrimSpace(c.Request().Header.Get("X-API-Token"))
			if got == "" {
				auth := c.Request().Header.Get("Authorization")
				if strings.HasPrefix(strings.ToLower(auth), "bearer ") {
					got = strings.TrimSpace(auth[7:])
				}
			}
			if got != token {
				return echo.NewHTTPError(http.StatusUnauthorized, ErrorBody{
					Error: Detail{Kind: "UNAUTHORIZED", Message: "invalid or missing token"},
				})
			}
			return next(c)
		}
	}
}
