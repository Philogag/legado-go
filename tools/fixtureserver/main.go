// Command fixtureserver 启动一个本地假书站，用于 demo 与冒烟测试。
//
//	go run ./tools/fixtureserver -addr :18081
//
// 站点结构：/search?q=&page=、/book/1、/toc/1、/content/1-1（含下一页）
package main

import (
	"flag"
	"fmt"
	"html"
	"log"
	"net/http"
	"strconv"
)

func main() {
	addr := flag.String("addr", ":18081", "监听地址")
	flag.Parse()

	mux := http.NewServeMux()
	page := func(w http.ResponseWriter, body string) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, body)
	}

	mux.HandleFunc("/search", func(w http.ResponseWriter, r *http.Request) {
		key := html.EscapeString(r.URL.Query().Get("q"))
		pageNum, _ := strconv.Atoi(r.URL.Query().Get("page"))
		if pageNum <= 0 {
			pageNum = 1
		}
		page(w, fmt.Sprintf(`<!doctype html><html><body>
<h2>"%s" 的搜索结果（第 %d 页）</h2>
<div class="result">
  <div class="item"><a href="/book/1">%s 奇谭</a><span class="author">测试作者</span><span class="kind">玄幻</span></div>
  <div class="item"><a href="/book/2">%s 后传</a><span class="author">另一位作者</span><span class="kind">都市</span></div>
</div></body></html>`, key, pageNum, key, key))
	})

	mux.HandleFunc("/book/1", func(w http.ResponseWriter, r *http.Request) {
		page(w, `<!doctype html><html><body>
<h1>测试奇谭</h1>
<span class="author">测试作者</span>
<span class="kind">玄幻</span>
<div id="intro">这是一本<b>用于验证</b>书源管线的小说。</div>
<a class="toc" href="/toc/1">查看目录</a>
</body></html>`)
	})

	mux.HandleFunc("/book/2", func(w http.ResponseWriter, r *http.Request) {
		page(w, `<!doctype html><html><body><h1>测试后传</h1><span class="author">另一位作者</span>
<a class="toc" href="/toc/2">查看目录</a></body></html>`)
	})

	toc := func(w http.ResponseWriter, chapters string) {
		page(w, `<!doctype html><html><body><ul class="chapters">`+chapters+`</ul></body></html>`)
	}
	mux.HandleFunc("/toc/1", func(w http.ResponseWriter, r *http.Request) {
		toc(w, `<li><a href="/content/1-1">第一章 开端</a></li><li><a href="/content/1-2">第二章 发展</a></li>`)
	})
	mux.HandleFunc("/toc/2", func(w http.ResponseWriter, r *http.Request) {
		toc(w, `<li><a href="/content/2-1">外传 第一章</a></li>`)
	})

	mux.HandleFunc("/content/1-1", func(w http.ResponseWriter, r *http.Request) {
		page(w, `<!doctype html><html><body>
<h1>第一章 开端</h1>
<div id="content"><p>第一页第一段。</p><p>第一页第二段&nbsp;带空格。</p></div>
<a class="next" href="/content/1-1-2">下一页</a></body></html>`)
	})
	mux.HandleFunc("/content/1-1-2", func(w http.ResponseWriter, r *http.Request) {
		page(w, `<!doctype html><html><body><div id="content"><p>第二页内容。</p></div></body></html>`)
	})
	mux.HandleFunc("/content/1-2", func(w http.ResponseWriter, r *http.Request) {
		page(w, `<!doctype html><html><body><h1>第二章 发展</h1><div id="content"><p>第二章正文。</p></div></body></html>`)
	})
	mux.HandleFunc("/content/2-1", func(w http.ResponseWriter, r *http.Request) {
		page(w, `<!doctype html><html><body><h1>外传 第一章</h1><div id="content"><p>外传正文。</p></div></body></html>`)
	})

	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		page(w, `<html><body><a href="/search?q=测试">搜索</a></body></html>`)
	})

	log.Printf("fixture site listening on %s", *addr)
	log.Fatal(http.ListenAndServe(*addr, mux))
}
