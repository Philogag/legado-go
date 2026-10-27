package booksource_test

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"legado-go/internal/booksource"
	"legado-go/internal/model"
)

// jsonSite 是一个 JSON API 风格的书站，用来验证 @json / POST 选项 / 变量 / 正则替换。
func jsonSite(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()

	writeJSON := func(w http.ResponseWriter, v any) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(v)
	}

	mux.HandleFunc("/api/search", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "want POST", http.StatusMethodNotAllowed)
			return
		}
		body, _ := io.ReadAll(r.Body)
		key := strings.TrimSpace(strings.TrimPrefix(string(body), "q="))
		writeJSON(w, map[string]any{
			"items": []map[string]any{
				{"t": key + "-JSON", "u": "/api/book?id=1", "a": "JSON作者"},
				{"t": key + "-二号", "u": "/api/book?id=2", "a": "JSON作者"},
			},
		})
	})

	mux.HandleFunc("/api/book", func(w http.ResponseWriter, r *http.Request) {
		id := r.URL.Query().Get("id")
		writeJSON(w, map[string]any{"data": map[string]any{
			"title":    "JSON之书 " + id,
			"author":   "JSON作者",
			"intro":    "这是<b>JSON</b>简介",
			"toc":      "/api/toc?id=" + id,
			"chapters": "/api/toc?id=" + id,
		}})
	})

	mux.HandleFunc("/api/toc", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{"list": []map[string]any{
			{"name": "第1章", "url": "/api/content?c=1"},
			{"name": "第2章", "url": "/api/content?c=2"},
		}})
	})

	mux.HandleFunc("/api/content", func(w http.ResponseWriter, r *http.Request) {
		c := r.URL.Query().Get("c")
		writeJSON(w, map[string]any{"content": fmt.Sprintf("正文第%s段\n广告插播广告插播\n结尾%s", c, c)})
	})

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func jsonSourceConfig(base string) *model.Config {
	return &model.Config{
		BookSourceURL:  base,
		BookSourceName: "JSON源",
		SearchURL:      `/api/search{"method":"POST","body":"q={{key}}"}`,
		RuleSearch: model.SearchRules{
			BookList: `@json:$.items[*]`,
			Name:     `@json:$.t##-JSON$##`,
			Author:   `@json:$.a`,
			BookURL:  `@json:$.u`,
		},
		RuleBookInfo: model.BookInfoRules{
			Name:   `@json:$.data.title`,
			Author: `@json:$.data.author`,
			Intro:  `@json:$.data.intro`,
			TocURL: `@json:$.data.toc`,
		},
		RuleToc: model.TocRules{
			ChapterList: `@json:$.list[*]`,
			ChapterName: `@json:$.name`,
			ChapterURL:  `@json:$.url`,
		},
		RuleContent: model.ContentRules{
			Content:      `@json:$.content`,
			ReplaceRegex: `广告.*?广告##`,
		},
	}
}

func TestJSONAPISource(t *testing.T) {
	site := jsonSite(t)
	src, err := booksource.New(jsonSourceConfig(site.URL), booksource.Options{Timeout: 5 * time.Second})
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	defer src.Close()

	books, err := src.Search("测试", 1)
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(books) != 2 {
		t.Fatalf("want 2 books, got %+v", books)
	}
	if books[0].Name != "测试" {
		t.Fatalf("regex replace after ## failed: %q", books[0].Name)
	}
	if !strings.Contains(books[0].BookURL, "/api/book?id=1") {
		t.Fatalf("bookUrl: %s", books[0].BookURL)
	}

	info, err := src.GetBookInfo(&books[0])
	if err != nil {
		t.Fatalf("book info: %v", err)
	}
	if info.Name != "JSON之书 1" || info.Author != "JSON作者" {
		t.Fatalf("unexpected info: %+v", info)
	}
	if info.Intro != "这是JSON简介" {
		t.Fatalf("intro should be rendered text: %q", info.Intro)
	}

	chapters, err := src.GetChapterList(info)
	if err != nil {
		t.Fatalf("chapters: %v", err)
	}
	if len(chapters) != 2 || chapters[1].Title != "第2章" {
		t.Fatalf("unexpected chapters: %+v", chapters)
	}

	content, err := src.GetContent(&chapters[1], info)
	if err != nil {
		t.Fatalf("content: %v", err)
	}
	if strings.Contains(content.Content, "广告") {
		t.Fatalf("replaceRegex not applied: %q", content.Content)
	}
	if !strings.Contains(content.Content, "正文第2段") || !strings.Contains(content.Content, "结尾2") {
		t.Fatalf("unexpected content: %q", content.Content)
	}
}

func TestSourceVariablesAndJsRule(t *testing.T) {
	site := jsonSite(t)
	cfg := jsonSourceConfig(site.URL)
	// 搜索时用 JS 规则把书名写进书源变量，作者规则再读回来
	cfg.RuleSearch.Name = `@js:source.put("firstName", result); result`
	cfg.RuleSearch.Author = `@get:{firstName}`
	src, err := booksource.New(cfg, booksource.Options{Timeout: 5 * time.Second})
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	defer src.Close()

	books, err := src.Search("变量", 1)
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(books) == 0 {
		t.Fatal("no books")
	}
	if src.Variables()["firstName"] == "" {
		t.Fatalf("variable not stored: %+v", src.Variables())
	}

	// 直接跑一段 JS，验证沙箱里有全书源变量与工具对象
	out, err := src.EvalJS(`JSON.stringify({hasJava: typeof java === "object", cookie: typeof cookie, cache: typeof cache, src: typeof source})`, booksource.Bindings{})
	if err != nil {
		t.Fatalf("eval: %v", err)
	}
	s, _ := out.(string)
	for _, want := range []string{`"hasJava":true`, `"cookie":"object"`, `"cache":"object"`, `"src":"object"`} {
		if !strings.Contains(s, want) {
			t.Fatalf("sandbox missing %s: %s", want, s)
		}
	}
}
