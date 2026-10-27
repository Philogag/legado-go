package booksource_test

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"legado-go/internal/booksource"
	"legado-go/internal/model"
)

// fakeSite 是一个最小可用的书站（搜索 / 详情 / 目录 / 正文含分页）
func fakeSite(t *testing.T) (*httptest.Server, *int64) {
	t.Helper()
	var hits int64
	mux := http.NewServeMux()

	mux.HandleFunc("/search", func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&hits, 1)
		key := r.URL.Query().Get("q")
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprintf(w, `<html><body><div class="item"><a href="/book/1">%s 之书</a><span class="author">作者甲</span></div>
<div class="item"><a href="/book/2">%s 之二</a><span class="author">作者乙</span></div></body></html>`, key, key)
	})

	mux.HandleFunc("/book/1", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, `<html><body><h1>测试之书</h1><span class="author">作者甲</span>
<div id="intro">这是一本<b>测试</b>书</div><a class="toc" href="/toc/1">目录</a></body></html>`)
	})

	mux.HandleFunc("/toc/1", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, `<html><body><ul class="chapters">
<li><a href="/content/1-1">第一章 开端</a></li>
<li><a href="/content/1-2">第二章 发展</a></li>
</ul></body></html>`)
	})

	mux.HandleFunc("/content/1-1", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, `<html><body><h1>第一章 开端</h1><div id="content"><p>第一段 &nbsp;文字</p><p>第二段文字</p></div>
<a class="next" href="/content/1-1-2">下一页</a></body></html>`)
	})

	mux.HandleFunc("/content/1-1-2", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, `<html><body><div id="content"><p>第三段文字</p></div></body></html>`)
	})

	mux.HandleFunc("/content/1-2", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, `<html><body><h1>第二章 发展</h1><div id="content"><p>只有一段</p></div></body></html>`)
	})

	mux.HandleFunc("/explore", func(w http.ResponseWriter, r *http.Request) {
		page := r.URL.Query().Get("page")
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprintf(w, `<html><body><div class="item"><a href="/book/%s">第 %s 页书</a></div></body></html>`, page, page)
	})

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, &hits
}

func fixtureConfig(base string) *model.Config {
	return &model.Config{
		BookSourceURL:  base,
		BookSourceName: "测试源",
		SearchURL:      "/search?q={{key}}&page={{page}}",
		ExploreURL:     `/explore?page={{page}}`,
		RuleSearch: model.SearchRules{
			BookList: ".item",
			Name:     "a@text",
			Author:   ".author@text",
			BookURL:  "a@href",
		},
		RuleExplore: model.ExploreRules{
			BookList: ".item",
			Name:     "a@text",
			BookURL:  "a@href",
		},
		RuleBookInfo: model.BookInfoRules{
			Name:   "h1@text",
			Author: ".author@text",
			Intro:  "#intro@text",
			TocURL: "a.toc@href",
		},
		RuleToc: model.TocRules{
			ChapterList: "ul.chapters li",
			ChapterName: "a@text",
			ChapterURL:  "a@href",
		},
		RuleContent: model.ContentRules{
			Title:          "h1@text",
			Content:        "#content p",
			NextContentURL: "a.next@href",
		},
	}
}

func TestSearchAndPipeline(t *testing.T) {
	srv, _ := fakeSite(t)
	src, err := booksource.New(fixtureConfig(srv.URL), booksource.Options{Timeout: 5 * time.Second})
	if err != nil {
		t.Fatalf("new source: %v", err)
	}
	defer src.Close()

	books, err := src.Search("棋", 1)
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(books) != 2 {
		t.Fatalf("want 2 books, got %d (%+v)", len(books), books)
	}
	if books[0].Name != "棋 之书" || books[0].Author != "作者甲" {
		t.Fatalf("unexpected first book: %+v", books[0])
	}
	if !strings.HasSuffix(books[0].BookURL, "/book/1") {
		t.Fatalf("bookUrl not absolute: %s", books[0].BookURL)
	}
	if books[0].Origin != srv.URL {
		t.Fatalf("origin not set: %+v", books[0])
	}

	info, err := src.GetBookInfo(&books[0])
	if err != nil {
		t.Fatalf("book info: %v", err)
	}
	if info.Name != "测试之书" || info.Author != "作者甲" {
		t.Fatalf("unexpected info: %+v", info)
	}
	if info.Intro != "这是一本测试书" {
		t.Fatalf("intro not formatted: %q", info.Intro)
	}
	if !strings.HasSuffix(info.TocURL, "/toc/1") {
		t.Fatalf("tocUrl not resolved: %s", info.TocURL)
	}

	chapters, err := src.GetChapterList(info)
	if err != nil {
		t.Fatalf("chapters: %v", err)
	}
	if len(chapters) != 2 || chapters[0].Title != "第一章 开端" {
		t.Fatalf("unexpected chapters: %+v", chapters)
	}
	if chapters[1].Index != 1 || chapters[1].BookURL != info.BookURL {
		t.Fatalf("chapter index/bookUrl wrong: %+v", chapters[1])
	}

	content, err := src.GetContent(&chapters[0], info)
	if err != nil {
		t.Fatalf("content: %v", err)
	}
	if content.Title != "第一章 开端" {
		t.Fatalf("content title: %q", content.Title)
	}
	want := "第一段 文字\n第二段文字\n第三段文字"
	if content.Content != want {
		t.Fatalf("content mismatch:\n got %q\nwant %q", content.Content, want)
	}
}

func TestExploreAndRenderedJS(t *testing.T) {
	srv, _ := fakeSite(t)
	src, err := booksource.New(fixtureConfig(srv.URL), booksource.Options{})
	if err != nil {
		t.Fatalf("new source: %v", err)
	}
	defer src.Close()

	books, err := src.Explore("", 3)
	if err != nil {
		t.Fatalf("explore: %v", err)
	}
	if len(books) != 1 || books[0].Name != "第 3 页书" {
		t.Fatalf("unexpected explore result: %+v", books)
	}

	out, err := src.EvalJS(`key + "|" + page + "|" + baseUrl`, booksource.Bindings{Key: "棋", Page: 2, BaseURL: srv.URL})
	if err != nil {
		t.Fatalf("eval: %v", err)
	}
	if out != fmt.Sprintf("棋|2|%s", srv.URL) {
		t.Fatalf("unexpected js output: %v", out)
	}
}

func TestURLTemplateAndVariables(t *testing.T) {
	srv, hits := fakeSite(t)
	cfg := fixtureConfig(srv.URL)
	cfg.Variable = `{"token":"abc"}`
	src, err := booksource.New(cfg, booksource.Options{})
	if err != nil {
		t.Fatalf("new source: %v", err)
	}
	defer src.Close()

	if got := src.SourceGet("token"); got != "abc" {
		t.Fatalf("variable not seeded: %q", got)
	}
	src.PutVar("token", "xyz")
	if got := src.Variables()["token"]; got != "xyz" {
		t.Fatalf("variable not updated: %q", got)
	}

	// {{key}} 未编码时直接拼进 query，验证模板替换生效
	if _, err := src.Search("丙", 1); err != nil {
		t.Fatalf("search: %v", err)
	}
	if atomic.LoadInt64(hits) == 0 {
		t.Fatal("fake site never hit")
	}
}

func TestParseConfigFromJSONArray(t *testing.T) {
	srv, _ := fakeSite(t)
	cfg := fmt.Sprintf(`[{"bookSourceName":"数组源","bookSourceUrl":%q,"searchUrl":"/search?q={{key}}","ruleSearch":{"bookList":".item","name":"a@text","bookUrl":"a@href"}}]`, srv.URL)
	src, err := booksource.NewFromJSON([]byte(cfg), booksource.Options{})
	if err != nil {
		t.Fatalf("new from json: %v", err)
	}
	defer src.Close()
	if src.Name() != "数组源" {
		t.Fatalf("unexpected name: %s", src.Name())
	}
	if _, err := src.Search("甲", 1); err != nil {
		t.Fatalf("search: %v", err)
	}
}
