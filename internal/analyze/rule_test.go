package analyze_test

import (
	"fmt"
	"strings"
	"testing"

	"legado-go/internal/analyze"
)

// host 是 analyze.Host 的最小实现：`{{x}}` / JS 都当作字符串拼接或变量查表。
type host struct {
	vars map[string]string
}

func newHost() *host { return &host{vars: map[string]string{"v": "VAR"}} }

func (h *host) EvalJS(js string, result any) (any, error) {
	switch {
	case strings.HasPrefix(js, "ERR"):
		return nil, fmt.Errorf("js failed")
	case js == "v":
		return h.vars[js], nil
	default:
		return fmt.Sprintf("%v-%s", result, js), nil
	}
}

func (h *host) GetVar(key string) string { return h.vars[key] }

func (h *host) PutVar(key, value string) { h.vars[key] = value }

const bookHTML = `<!doctype html><html><body>
<div class="result">
  <div class="item"><a href="/book/1">书名一</a><span class="author">作者甲</span></div>
  <div class="item"><a href="/book/2">书名二</a><span class="author">作者乙</span></div>
</div>
<div id="intro">简介<b>加粗</b>文字</div>
<ul class="toc"><li><a href="/c/1">第一章</a></li><li><a href="/c/2">第二章</a></li></ul>
</body></html>`

func TestGetStringAndList(t *testing.T) {
	r := analyze.New(newHost(), bookHTML, "http://site.example/dir/page.html")

	if got, err := r.GetString("h1@text"); err != nil || got != "" {
		t.Fatalf("missing selector should be empty, got %q err=%v", got, err)
	}
	if got, err := r.GetString("#intro@text"); err != nil || got != "简介加粗文字" {
		t.Fatalf("intro text: %q err=%v", got, err)
	}
	if got, err := r.GetString("#intro@html"); err != nil || !strings.Contains(got, "<b>加粗</b>") {
		t.Fatalf("intro html: %q err=%v", got, err)
	}

	list, err := r.GetStringList(".result .item")
	if err != nil || len(list) != 2 {
		t.Fatalf("item list: %v err=%v", list, err)
	}

	// 相对地址解析（索引需写在选择器末尾，与上游 findIndexSet 的逆向扫描一致）
	url, err := r.GetStringIsURL(".result .item a.0@href")
	if err != nil || url != "http://site.example/book/1" {
		t.Fatalf("resolved url: %q err=%v", url, err)
	}

	// 倒序 / 排除 / 索引
	last, err := r.GetString(".result .item a.-1@text")
	if err != nil || last != "书名二" {
		t.Fatalf("negative index: %q err=%v", last, err)
	}
	filtered, err := r.GetStringList(".result .item a!0@text")
	if err != nil || len(filtered) != 1 || filtered[0] != "书名二" {
		t.Fatalf("exclude index: %v err=%v", filtered, err)
	}
}

func TestRuleComposition(t *testing.T) {
	r := analyze.New(newHost(), bookHTML, "http://site.example")

	// `||` 取第一个非空
	or, err := r.GetString(".nope@text||#intro@text")
	if err != nil || or != "简介加粗文字" {
		t.Fatalf("|| : %q err=%v", or, err)
	}

	// `&&` 在元素规则链中表示「同一节点上再取一层，结果合并」
	and, err := r.GetString("#intro&&b@text")
	if err != nil || and != "简介加粗文字\n加粗" {
		t.Fatalf("&& : %q err=%v", and, err)
	}

	// `##正则##替换`
	replaced, err := r.GetString("#intro@text##加粗##")
	if err != nil || replaced != "简介文字" {
		t.Fatalf("## replace: %q err=%v", replaced, err)
	}

	// `##@js:` 后处理（trailing `@js:` 不被支持，与上游一致：需写成 `##@js:` 或规则头部 `@js:`）
	js, err := r.GetString("#intro@text##@js:result+result")
	if err != nil || !strings.HasPrefix(js, "简介加粗文字") || js == "简介加粗文字" {
		t.Fatalf("##@js: : %q err=%v", js, err)
	}

	// `@js:` 前缀：result 为当前文档全文
	head, err := r.GetString("@js:1+1")
	if err != nil || !strings.Contains(head, "简介") || !strings.HasSuffix(head, "-1+1") {
		t.Fatalf("@js: prefix: %q err=%v", head, err)
	}

	// `{{}}` 插值：Mode.Regex 下直接把展开后的规则文本作为结果（与上游 getString 的 `else -> rule` 一致）
	tpl, err := r.GetString("{{v}}")
	if err != nil || tpl != "VAR" {
		t.Fatalf("template: %q err=%v", tpl, err)
	}

	if _, err := r.GetString("#intro@text##@js:ERR"); err == nil {
		t.Fatal("expected js error to propagate")
	}
}

func TestXPathAndJSONPath(t *testing.T) {
	x := analyze.New(newHost(), bookHTML, "http://site.example")
	got, err := x.GetString("@XPath://div[@class=\"item\"][1]/a/text()")
	if err != nil || got != "书名一" {
		t.Fatalf("xpath: %q err=%v", got, err)
	}
	href, err := x.GetString("@XPath://div[@class=\"item\"][1]/a/@href")
	if err != nil || href != "/book/1" {
		t.Fatalf("xpath attr: %q err=%v", href, err)
	}
	absHref, err := x.GetStringIsURL("@XPath://div[@class=\"item\"][1]/a/@href")
	if err != nil || absHref != "http://site.example/book/1" {
		t.Fatalf("xpath attr as url: %q err=%v", absHref, err)
	}

	jsonContent := `{"data":{"list":[{"name":"A","id":1},{"name":"B","id":2}]}}`
	j := analyze.New(newHost(), jsonContent, "http://site.example")
	name, err := j.GetString("$.data.list[1].name")
	if err != nil || name != "B" {
		t.Fatalf("jsonpath: %q err=%v", name, err)
	}
	names, err := j.GetStringList("$..name")
	if err != nil || len(names) != 2 || names[0] != "A" {
		t.Fatalf("jsonpath list: %v err=%v", names, err)
	}
}

func TestGetElementsReturnsNodes(t *testing.T) {
	r := analyze.New(newHost(), bookHTML, "http://site.example")
	nodes, err := r.GetElements(".result .item")
	if err != nil || len(nodes) != 2 {
		t.Fatalf("elements: %d err=%v", len(nodes), err)
	}
	sub := analyze.New(newHost(), nodes[1], "http://site.example")
	if got, err := sub.GetString("a@text"); err != nil || got != "书名二" {
		t.Fatalf("sub doc: %q err=%v", got, err)
	}
}

func TestAbsoluteURL(t *testing.T) {
	cases := []struct{ base, ref, want string }{
		{"http://a.example/x/y", "/z", "http://a.example/z"},
		{"http://a.example/x/y", "z", "http://a.example/x/z"},
		{"http://a.example/x/y", "https://b.example/q", "https://b.example/q"},
		{"http://a.example/x/y", "//b.example/q", "http://b.example/q"},
	}
	for _, tc := range cases {
		if got := analyze.AbsoluteURL(tc.base, tc.ref); got != tc.want {
			t.Fatalf("AbsoluteURL(%q,%q): want %q, got %q", tc.base, tc.ref, tc.want, got)
		}
	}
}

func TestUnescapeHTML(t *testing.T) {
	// `&nbsp;` 保留为 U+00A0，与 legado 的 unescape 行为一致
	if got := analyze.UnescapeHTML("a&amp;b&#39;c&nbsp;d"); got != "a&b'c\u00a0d" {
		t.Fatalf("unescape: %q", got)
	}
}
