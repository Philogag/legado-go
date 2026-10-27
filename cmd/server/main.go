// Command server 是书源库的 HTTP 微服务入口（REST + MCP）。
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"legado-go/internal/booksource"
	"legado-go/internal/httpapi"
	"legado-go/internal/mcp"
	"legado-go/internal/registry"
)

func main() {
	addr := flag.String("addr", env("ADDR", ":8080"), "监听地址")
	defaultTTL := flag.Duration("ttl", envDuration("DEFAULT_TTL", 30*time.Minute), "默认实例空闲 TTL")
	maxTTL := flag.Duration("max-ttl", envDuration("MAX_TTL", 6*time.Hour), "实例最长存活时间（0 表示不限）")
	maxInstances := flag.Int("max-instances", envInt("MAX_INSTANCES", 64), "最大实例数（0 表示不限）")
	sweep := flag.Duration("sweep", envDuration("SWEEP_INTERVAL", 5*time.Second), "过期扫描间隔")
	timeout := flag.Duration("http-timeout", envDuration("HTTP_TIMEOUT", 30*time.Second), "单次抓取超时")
	cacheTTL := flag.Duration("cache-ttl", envDuration("CACHE_TTL", time.Hour), "书源缓存 TTL")
	token := flag.String("token", env("API_TOKEN", ""), "API 访问令牌（空表示不鉴权）")
	grace := flag.Duration("grace", envDuration("GRACE_PERIOD", 10*time.Minute), "实例回收后墓碑保留时长（期间返回 410）")
	rateLimit := flag.Int("rate-limit", envInt("RATE_LIMIT", 0), "每 IP 每分钟请求上限（0 表示不限）")
	quiet := flag.Bool("quiet", env("QUIET", "1") == "1", "关闭访问日志")
	verboseJS := flag.Bool("verbose-js", env("VERBOSE_JS", "0") == "1", "打印书源 JS 内的 console.log")
	shutdownTimeout := flag.Duration("shutdown-timeout", envDuration("SHUTDOWN_TIMEOUT", 10*time.Second), "优雅停机宽限时长")
	healthcheck := flag.String("healthcheck", "", "只做健康检查后退出：-healthcheck http://127.0.0.1:8080/healthz")
	flag.Parse()

	if *healthcheck != "" {
		os.Exit(probe(*healthcheck))
	}

	logger := log.New(os.Stdout, "[server] ", log.LstdFlags|log.Lmsgprefix)

	reg := registry.New(registry.Options{
		DefaultTTL:    *defaultTTL,
		MaxTTL:        *maxTTL,
		MaxInstances:  *maxInstances,
		SweepInterval: *sweep,
		GracePeriod:   *grace,
		SourceOptions: booksource.Options{
			Timeout:  *timeout,
			CacheTTL: *cacheTTL,
			OnLog: func(line string) {
				if *verboseJS {
					logger.Printf("[js] %s", line)
				}
			},
		},
		OnEvict: func(info registry.Info) {
			logger.Printf("evicted instance %s (%s) after %s idle", info.ID, info.Name, time.Duration(info.TTL)*time.Second)
		},
	})
	defer reg.Close()

	mcpServer := mcp.New(reg, mcp.Options{
		Name:    "legado-booksource",
		Version: "1.0.0",
		Log:     func(format string, args ...any) { logger.Printf(format, args...) },
	})

	var requestLog func(format string, args ...any)
	if !*quiet {
		requestLog = func(format string, args ...any) { logger.Printf(format, args...) }
	}

	router := httpapi.NewRouter(httpapi.Options{
		Registry:  reg,
		MCP:       mcpServer,
		Token:     *token,
		RateLimit: *rateLimit,
		Log:       requestLog,
	})

	srv := &http.Server{
		Addr:              *addr,
		Handler:           router,
		ReadHeaderTimeout: 10 * time.Second,
	}

	go func() {
		logger.Printf("listening on %s (defaultTTL=%s maxTTL=%s maxInstances=%d)", *addr, *defaultTTL, *maxTTL, *maxInstances)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Fatalf("listen failed: %v", err)
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	<-stop
	logger.Printf("shutting down")
	ctx, cancel := context.WithTimeout(context.Background(), *shutdownTimeout)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		logger.Printf("shutdown error: %v", err)
	}
	logger.Printf("bye")
}

// probe 供容器健康检查使用：GET 目标地址，2xx 视为健康。
func probe(url string) int {
	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		fmt.Fprintf(os.Stderr, "healthcheck: %v\n", err)
		return 1
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		fmt.Fprintf(os.Stderr, "healthcheck: status %d\n", resp.StatusCode)
		return 1
	}
	return 0
}

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func envInt(key string, def int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
		fmt.Fprintf(os.Stderr, "invalid %s=%q, using %d\n", key, v, def)
	}
	return def
}

func envDuration(key string, def time.Duration) time.Duration {
	if v := os.Getenv(key); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			return d
		}
		fmt.Fprintf(os.Stderr, "invalid %s=%q, using %s\n", key, v, def)
	}
	return def
}
