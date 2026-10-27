package httpapi

import (
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/labstack/echo/v4"

	"legado-go/internal/booksource"
	"legado-go/internal/model"
	"legado-go/internal/registry"
)

type api struct {
	reg *registry.Registry
}

// createRequest 支持两种写法：
//  1. {"config": {...书源...}, "ttl": "2m", "name": "别名", "owner": "x"}
//  2. 直接提交书源对象/数组
//
// TTL 可以用 `ttl`（"90s"/"2m" 或纯数字秒）或 `ttlSeconds` 指定。
type createRequest struct {
	ID         string          `json:"id"`
	Config     json.RawMessage `json:"config"`
	TTL        json.RawMessage `json:"ttl"`
	TTLSeconds int             `json:"ttlSeconds"`
	Name       string          `json:"name"`
	Owner      string          `json:"owner"`
}

// parseTTL 接受 {"ttl":"2m"}、{"ttl":120}、{"ttlSeconds":120} 三种形式。
func parseTTL(raw json.RawMessage, seconds int) (time.Duration, error) {
	if len(raw) > 0 && string(raw) != "null" {
		var s string
		if json.Unmarshal(raw, &s) == nil {
			s = strings.TrimSpace(s)
			if s == "" {
				return 0, nil
			}
			if n, err := strconv.Atoi(s); err == nil {
				return time.Duration(n) * time.Second, nil
			}
			d, err := time.ParseDuration(s)
			if err != nil {
				return 0, model.WrapError(model.KindInvalidConfig, err, "invalid ttl: %s", s)
			}
			return d, nil
		}
		var n int
		if json.Unmarshal(raw, &n) == nil {
			return time.Duration(n) * time.Second, nil
		}
		return 0, model.WrapError(model.KindInvalidConfig, nil, "invalid ttl")
	}
	return time.Duration(seconds) * time.Second, nil
}

// bookRequest 支持两种写法：`{"book": {...}}`（推荐，可直接回传上次的响应）或平铺字段。
type bookRequest struct {
	Book     json.RawMessage `json:"book"`
	BookURL  string          `json:"bookUrl"`
	TocURL   string          `json:"tocUrl"`
	Name     string          `json:"name"`
	Author   string          `json:"author"`
	CoverURL string          `json:"coverUrl"`
	Intro    string          `json:"intro"`
}

func (r bookRequest) toBook() *model.Book {
	if len(r.Book) > 0 {
		var b model.Book
		if json.Unmarshal(r.Book, &b) == nil && b.BookURL != "" {
			return &b
		}
	}
	return &model.Book{BookURL: r.BookURL, TocURL: r.TocURL, Name: r.Name, Author: r.Author, CoverURL: r.CoverURL, Intro: r.Intro}
}

// contentRequest 的 chapter 字段直接接受 /chapters 返回的章节对象。
type contentRequest struct {
	bookRequest
	Chapter    json.RawMessage `json:"chapter"`
	ChapterURL string          `json:"chapterUrl"`
	Title      string          `json:"title"`
	Index      int             `json:"index"`
}

func (r contentRequest) toChapter() *model.Chapter {
	if len(r.Chapter) > 0 {
		var ch model.Chapter
		if json.Unmarshal(r.Chapter, &ch) == nil && ch.URL != "" {
			return &ch
		}
	}
	return &model.Chapter{Title: r.Title, URL: r.ChapterURL, Index: r.Index}
}

func readBody(c echo.Context) ([]byte, error) {
	return io.ReadAll(io.LimitReader(c.Request().Body, 8<<20))
}

func (a *api) createInstance(c echo.Context) error {
	raw, err := readBody(c)
	if err != nil {
		return respondError(c, model.WrapError(model.KindInvalidConfig, err, "read body failed"))
	}
	cfgData := raw
	ttl := time.Duration(0)
	owner := ""
	var req createRequest
	if json.Unmarshal(raw, &req) == nil {
		if len(req.Config) > 0 {
			cfgData = req.Config
		}
		ttl, err = parseTTL(req.TTL, req.TTLSeconds)
		if err != nil {
			return respondError(c, err)
		}
		owner = req.Owner
	}
	cfg, err := model.ParseConfig(cfgData)
	if err != nil {
		return respondError(c, err)
	}
	if req.Name != "" {
		cfg.BookSourceName = req.Name
	}
	entry, err := a.reg.CreateWithID(strings.TrimSpace(req.ID), cfg, ttl, owner)
	if err != nil {
		return respondError(c, err)
	}
	return c.JSON(http.StatusCreated, map[string]any{"instance": entry.Info()})
}

func (a *api) listInstances(c echo.Context) error {
	return c.JSON(http.StatusOK, map[string]any{
		"instances": a.reg.List(),
		"stats":     a.reg.Stats(),
	})
}

func (a *api) getInstance(c echo.Context) error {
	entry, err := a.reg.Lookup(c.Param("id"))
	if err != nil {
		return respondError(c, err)
	}
	src := entry.Source
	return c.JSON(http.StatusOK, map[string]any{
		"instance": entry.Info(),
		"source": map[string]any{
			"name":           src.Name(),
			"url":            src.URL(),
			"group":          src.Config().Group,
			"type":           src.Config().Type,
			"exploreEntries": src.ExploreEntries(),
			"variables":      src.Variables(),
		},
	})
}

func (a *api) deleteInstance(c echo.Context) error {
	if err := a.reg.Delete(c.Param("id")); err != nil {
		return respondError(c, err)
	}
	return c.NoContent(http.StatusNoContent)
}

func (a *api) touchInstance(c echo.Context) error {
	var req struct {
		TTL        json.RawMessage `json:"ttl"`
		TTLSeconds int             `json:"ttlSeconds"`
	}
	if err := c.Bind(&req); err != nil && err != io.EOF {
		return respondError(c, model.WrapError(model.KindInvalidConfig, err, "invalid body"))
	}
	ttl, err := parseTTL(req.TTL, req.TTLSeconds)
	if err != nil {
		return respondError(c, err)
	}
	entry, err := a.reg.Touch(c.Param("id"), ttl)
	if err != nil {
		return respondError(c, err)
	}
	return c.JSON(http.StatusOK, map[string]any{"instance": entry.Info()})
}

// source 取出实例并刷新 TTL
func (a *api) source(c echo.Context) (*booksource.Source, error) {
	entry, err := a.reg.Get(c.Param("id"))
	if err != nil {
		return nil, err
	}
	return entry.Source, nil
}

func (a *api) search(c echo.Context) error {
	src, err := a.source(c)
	if err != nil {
		return respondError(c, err)
	}
	var req struct {
		Key  string `json:"key"`
		Page int    `json:"page"`
	}
	if err := c.Bind(&req); err != nil {
		return respondError(c, model.WrapError(model.KindInvalidConfig, err, "invalid body"))
	}
	start := time.Now()
	books, err := src.Search(req.Key, req.Page)
	if err != nil {
		return respondError(c, err)
	}
	return c.JSON(http.StatusOK, map[string]any{
		"books": books, "count": len(books), "elapsedMs": time.Since(start).Milliseconds(),
	})
}

func (a *api) explore(c echo.Context) error {
	src, err := a.source(c)
	if err != nil {
		return respondError(c, err)
	}
	var req struct {
		URL  string `json:"url"`
		Page int    `json:"page"`
	}
	if err := c.Bind(&req); err != nil && err != io.EOF {
		return respondError(c, model.WrapError(model.KindInvalidConfig, err, "invalid body"))
	}
	start := time.Now()
	books, err := src.Explore(req.URL, req.Page)
	if err != nil {
		return respondError(c, err)
	}
	return c.JSON(http.StatusOK, map[string]any{
		"books": books, "count": len(books), "elapsedMs": time.Since(start).Milliseconds(),
	})
}

func (a *api) bookInfo(c echo.Context) error {
	src, err := a.source(c)
	if err != nil {
		return respondError(c, err)
	}
	var req bookRequest
	if err := c.Bind(&req); err != nil {
		return respondError(c, model.WrapError(model.KindInvalidConfig, err, "invalid body"))
	}
	start := time.Now()
	book, err := src.GetBookInfo(req.toBook())
	if err != nil {
		return respondError(c, err)
	}
	return c.JSON(http.StatusOK, map[string]any{"book": book, "elapsedMs": time.Since(start).Milliseconds()})
}

func (a *api) chapters(c echo.Context) error {
	src, err := a.source(c)
	if err != nil {
		return respondError(c, err)
	}
	var req bookRequest
	if err := c.Bind(&req); err != nil {
		return respondError(c, model.WrapError(model.KindInvalidConfig, err, "invalid body"))
	}
	start := time.Now()
	chapters, err := src.GetChapterList(req.toBook())
	if err != nil {
		return respondError(c, err)
	}
	return c.JSON(http.StatusOK, map[string]any{
		"chapters": chapters, "count": len(chapters), "elapsedMs": time.Since(start).Milliseconds(),
	})
}

func (a *api) content(c echo.Context) error {
	src, err := a.source(c)
	if err != nil {
		return respondError(c, err)
	}
	var req contentRequest
	if err := c.Bind(&req); err != nil {
		return respondError(c, model.WrapError(model.KindInvalidConfig, err, "invalid body"))
	}
	start := time.Now()
	chapter := req.toChapter()
	book := req.toBook()
	if book.BookURL == "" {
		book.BookURL = chapter.BookURL
	}
	result, err := src.GetContent(chapter, book)
	if err != nil {
		return respondError(c, err)
	}
	return c.JSON(http.StatusOK, map[string]any{"content": result, "elapsedMs": time.Since(start).Milliseconds()})
}

func (a *api) eval(c echo.Context) error {
	src, err := a.source(c)
	if err != nil {
		return respondError(c, err)
	}
	var req struct {
		Code    string `json:"code"`
		Script  string `json:"script"`
		Key     string `json:"key"`
		Page    int    `json:"page"`
		BaseURL string `json:"baseUrl"`
		Title   string `json:"title"`
		Result  any    `json:"result"`
	}
	if err := c.Bind(&req); err != nil {
		return respondError(c, model.WrapError(model.KindInvalidConfig, err, "invalid body"))
	}
	out, err := src.EvalJS(firstNonEmpty(req.Script, req.Code), booksource.Bindings{
		BaseURL: req.BaseURL,
		Key:     req.Key,
		Page:    req.Page,
		Title:   req.Title,
		Result:  req.Result,
	})
	if err != nil {
		return respondError(c, err)
	}
	return c.JSON(http.StatusOK, map[string]any{"result": out})
}

func (a *api) health(c echo.Context) error {
	return c.JSON(http.StatusOK, map[string]any{
		"status": "ok", "stats": a.reg.Stats(), "time": time.Now(),
	})
}

func (a *api) meta(c echo.Context) error {
	return c.JSON(http.StatusOK, map[string]any{
		"name":          "legado-booksource",
		"version":       "1.0.0",
		"mcpProtocol":   "2026-07-28",
		"defaultTTLSec": int(a.reg.DefaultTTL().Seconds()),
		"instanceCount": a.reg.Stats().Instances,
		"endpoints": []string{
			"POST /api/v1/instances",
			"GET /api/v1/instances",
			"GET /api/v1/instances/:id",
			"DELETE /api/v1/instances/:id",
			"POST /api/v1/instances/:id/touch",
			"POST /api/v1/instances/:id/search",
			"POST /api/v1/instances/:id/explore",
			"POST /api/v1/instances/:id/bookinfo",
			"POST /api/v1/instances/:id/chapters",
			"POST /api/v1/instances/:id/content",
			"POST /api/v1/instances/:id/eval",
			"POST /api/v1/sources/validate",
			"POST /mcp",
		},
	})
}

func (a *api) validate(c echo.Context) error {
	raw, err := readBody(c)
	if err != nil {
		return respondError(c, model.WrapError(model.KindInvalidConfig, err, "read body failed"))
	}
	cfgData := raw
	var req createRequest
	if json.Unmarshal(raw, &req) == nil && len(req.Config) > 0 {
		cfgData = req.Config
	}
	cfg, err := model.ParseConfig(cfgData)
	if err != nil {
		return respondError(c, err)
	}
	normalized, err := model.Normalize(cfg)
	if err != nil {
		return respondError(c, err)
	}
	src, err := booksource.New(cfg, booksource.Options{})
	if err != nil {
		return respondError(c, err)
	}
	defer src.Close()
	return c.JSON(http.StatusOK, map[string]any{
		"valid":          true,
		"name":           normalized.Name,
		"url":            normalized.URL,
		"group":          normalized.Group,
		"type":           normalized.Type,
		"hasSearch":      normalized.SearchURL != "",
		"exploreEntries": normalized.ExploreEntries,
		"variables":      len(normalized.Variable),
	})
}

// firstNonEmpty 返回第一个非空字符串，用于兼容同一语义的多个字段名。
func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}
