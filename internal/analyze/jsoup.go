// Package analyze 实现书源规则的三种解析器（CSS / XPath / JsonPath / 正则）
// 与统一的规则调度器 AnalyzeRule。
package analyze

import (
	"strconv"
	"strings"

	"github.com/PuerkitoBio/goquery"
	"golang.org/x/net/html"

	"legado-go/internal/rules"
)

// Node 是 jsoup 解析器的工作单元：一个 HTML 元素节点。
type Node struct{ N *html.Node }

// ── 文本抽取（对齐 Jsoup `Element.text()`：块级元素之间补空格并折叠空白）────────

func collectText(n *html.Node, b *strings.Builder) {
	if n == nil {
		return
	}
	switch n.Type {
	case html.TextNode:
		b.WriteString(n.Data)
		return
	case html.ElementNode:
		name := strings.ToLower(n.Data)
		if name == "script" || name == "style" {
			return
		}
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		collectText(c, b)
		if c.Type == html.ElementNode && isBlockTag(strings.ToLower(c.Data)) {
			b.WriteByte(' ')
		}
	}
}

// blockTags 是会在 text() 中产生空白分隔的块级标签
var blockTags = map[string]bool{
	"address": true, "article": true, "aside": true, "blockquote": true, "br": true,
	"dd": true, "div": true, "dl": true, "dt": true, "fieldset": true, "figcaption": true,
	"figure": true, "footer": true, "form": true, "h1": true, "h2": true, "h3": true,
	"h4": true, "h5": true, "h6": true, "header": true, "hr": true, "li": true,
	"main": true, "nav": true, "ol": true, "p": true, "pre": true, "section": true,
	"table": true, "tbody": true, "td": true, "tfoot": true, "th": true, "thead": true,
	"tr": true, "ul": true,
}

func isBlockTag(name string) bool { return blockTags[name] }

func foldSpace(s string) string { return strings.Join(strings.Fields(s), " ") }

// JsoupText 等价 Jsoup 的 Element.text()
func JsoupText(n *html.Node) string {
	var b strings.Builder
	collectText(n, &b)
	return foldSpace(b.String())
}

// JsoupOwnText 只取直属文本节点
func JsoupOwnText(n *html.Node) string {
	if n == nil {
		return ""
	}
	var b strings.Builder
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if c.Type == html.TextNode {
			b.WriteString(c.Data)
		}
	}
	return foldSpace(b.String())
}

func textNodeList(n *html.Node) []string {
	var out []string
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if c.Type == html.TextNode {
			if t := foldSpace(c.Data); t != "" {
				out = append(out, t)
			}
		}
	}
	return out
}

func childrenOf(n *html.Node) []*html.Node {
	var out []*html.Node
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if c.Type == html.ElementNode {
			out = append(out, c)
		}
	}
	return out
}

// ── 索引规则解析 ─────────────────────────────────────────────────────────────

type indexSpec struct {
	single bool
	value  int
	start  *int
	end    *int
	step   int
}

type indexParse struct {
	split  byte // '.', '!' 或 ' '
	before string
	def    []int
	specs  []indexSpec
}

// ParseIndexSpec 移植 Legado `ElementsSingle.findIndexSet`：解析 `tag.div.0:2` / `tag.div!0` / `tag.div[-1,1:3]`
func ParseIndexSpec(rawRule string) indexParse {
	rule := strings.TrimSpace(rawRule)
	res := indexParse{split: ' ', before: rule}
	if rule == "" {
		return res
	}
	length := len(rule)
	bracket := rule[length-1] == ']'
	l := ""
	curMinus := false
	var curList []*int
	var curInt *int
	readInt := func() *int {
		if l == "" {
			return nil
		}
		v, _ := strconv.Atoi(l)
		if curMinus {
			v = -v
		}
		return &v
	}
	ptr := func(v int) *int { return &v }

	if bracket {
		length--
		for {
			length--
			if length < 0 {
				break
			}
			rl := rule[length]
			if rl == ' ' {
				continue
			}
			if rl >= '0' && rl <= '9' {
				l = string(rl) + l
				continue
			}
			if rl == '-' {
				curMinus = true
				continue
			}
			curInt = readInt()
			if rl == ':' {
				curList = append(curList, curInt)
			} else {
				if len(curList) == 0 {
					if curInt == nil {
						break
					}
					res.specs = append(res.specs, indexSpec{single: true, value: *curInt})
				} else {
					var start *int
					if curInt != nil {
						start = curInt
					}
					end := curList[len(curList)-1]
					step := 1
					if len(curList) == 2 && curList[0] != nil {
						step = *curList[0]
					}
					res.specs = append(res.specs, indexSpec{start: start, end: end, step: step})
					curList = nil
				}
				if rl == '!' {
					res.split = '!'
					for length > 0 && rule[length-1] == ' ' {
						length--
					}
				}
				if rl == '[' {
					res.before = rule[:length]
					break
				}
				if rl != ',' {
					break
				}
			}
			l = ""
			curMinus = false
		}
		return res
	}

	done := false
	for {
		length--
		if length < 0 {
			break
		}
		rl := rule[length]
		if rl == ' ' {
			continue
		}
		if rl >= '0' && rl <= '9' {
			l = string(rl) + l
			continue
		}
		if rl == '-' {
			curMinus = true
			continue
		}
		if rl == '!' || rl == '.' || rl == ':' {
			v := 0
			if l != "" {
				v, _ = strconv.Atoi(l)
			}
			if curMinus {
				v = -v
			}
			res.def = append(res.def, v)
			if rl != ':' {
				res.split = rl
				res.before = rule[:length]
				done = true
				break
			}
		} else {
			break
		}
		l = ""
		curMinus = false
	}
	if !done {
		res.split = ' '
		res.before = rule
		res.def = nil
	}
	_ = ptr
	return res
}

// ExpandIndexes 把索引描述展开成下标序列（按规则文本顺序）
func ExpandIndexes(p indexParse, length int) []int {
	type item struct {
		num  *int
		spec *indexSpec
	}
	var ordered []item
	if len(p.specs) == 0 {
		for i := len(p.def) - 1; i >= 0; i-- {
			v := p.def[i]
			ordered = append(ordered, item{num: &v})
		}
	} else {
		for i := len(p.specs) - 1; i >= 0; i-- {
			s := p.specs[i]
			ordered = append(ordered, item{spec: &s})
		}
	}
	var out []int
	push := func(i int) {
		for _, v := range out {
			if v == i {
				return
			}
		}
		out = append(out, i)
	}
	for _, it := range ordered {
		if it.num != nil {
			v := *it.num
			if v >= 0 && v < length {
				push(v)
			} else if v < 0 && length >= -v {
				push(v + length)
			}
			continue
		}
		s := it.spec
		if s.single {
			v := s.value
			if v < 0 {
				v += length
			}
			if v >= 0 && v < length {
				push(v)
			}
			continue
		}
		start := 0
		if s.start != nil {
			start = *s.start
		}
		if start < 0 {
			start += length
		}
		end := length - 1
		if s.end != nil {
			end = *s.end
		}
		if end < 0 {
			end += length
		}
		if (start < 0 && end < 0) || (start >= length && end >= length) {
			continue
		}
		if start >= length {
			start = length - 1
		} else if start < 0 {
			start = 0
		}
		if end >= length {
			end = length - 1
		} else if end < 0 {
			end = 0
		}
		stepX := s.step
		if start == end || stepX >= length {
			push(start)
			continue
		}
		step := stepX
		if stepX > 0 {
			step = stepX
		} else if -stepX < length {
			step = stepX + length
		} else {
			step = 1
		}
		if end > start {
			for i := start; i <= end; i += step {
				push(i)
			}
		} else {
			for i := start; i >= end; i -= step {
				push(i)
			}
		}
	}
	return out
}

// ── CSS / jsoup 解析器 ──────────────────────────────────────────────────────

var suffixRules = map[string]bool{"text": true, "textNodes": true, "ownText": true, "html": true, "all": true}

// Jsoup CSS 选择器解析器
type Jsoup struct {
	root *html.Node
}

// NewJsoup 以字符串（HTML）或元素节点构造解析器
func NewJsoup(content any) *Jsoup {
	switch v := content.(type) {
	case *html.Node:
		return &Jsoup{root: v}
	case Node:
		return &Jsoup{root: v.N}
	case *goquery.Selection:
		if n := firstNode(v); n != nil {
			return &Jsoup{root: n}
		}
		return &Jsoup{root: parseHTML("")}
	case []*html.Node:
		if len(v) > 0 {
			return &Jsoup{root: v[0]}
		}
		return &Jsoup{root: parseHTML("")}
	case nil:
		return &Jsoup{root: parseHTML("")}
	}
	return &Jsoup{root: parseHTML(rules.JsResultToString(content))}
}

func firstNode(sel *goquery.Selection) *html.Node {
	if sel == nil || sel.Length() == 0 {
		return nil
	}
	return sel.Get(0)
}

// parseHTML 容错解析 HTML 片段（补齐 table 包装，取 body 作为根）
func parseHTML(text string) *html.Node {
	trimmed := strings.TrimSpace(text)
	switch {
	case strings.HasSuffix(trimmed, "</td>"):
		text = "<table><tr>" + text + "</tr></table>"
	case strings.HasSuffix(trimmed, "</tr>"), strings.HasSuffix(trimmed, "</tbody>"):
		text = "<table>" + text + "</table>"
	}
	doc, err := html.Parse(strings.NewReader(text))
	if err != nil || doc == nil {
		doc, _ = html.Parse(strings.NewReader(""))
	}
	body := findBody(doc)
	if body != nil {
		return body
	}
	return doc
}

func findBody(n *html.Node) *html.Node {
	if n.Type == html.ElementNode && strings.EqualFold(n.Data, "body") {
		return n
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if got := findBody(c); got != nil {
			return got
		}
	}
	return nil
}

func (j *Jsoup) sel(scope *html.Node) *goquery.Selection {
	return goquery.NewDocumentFromNode(scope).Selection
}

// GetString 合并内容列表（多行用 \n 连接）
func (j *Jsoup) GetString(rule string) string {
	return strings.Join(j.GetStringList(rule), "\n")
}

// GetString0 只取第一个结果
func (j *Jsoup) GetString0(rule string) string {
	list := j.GetStringList(rule)
	if len(list) == 0 {
		return ""
	}
	return list[0]
}

// GetStringList 取内容列表
func (j *Jsoup) GetStringList(rule string) []string {
	if rule == "" {
		return nil
	}
	trimmed := strings.TrimSpace(rule)
	if strings.HasPrefix(strings.ToLower(trimmed), "@css:") {
		parts, typ := splitCombinator(trimmed[5:])
		var results [][]string
		for _, part := range parts {
			idx := strings.LastIndex(part, "@")
			var selector, last string
			if idx < 0 {
				selector = part
			} else {
				selector, last = part[:idx], part[idx+1:]
			}
			elements := j.selectRule(j.root, selector)
			var got []string
			if idx < 0 {
				got = textOf(elements)
			} else {
				got = j.getResultLast(elements, last)
			}
			if len(got) > 0 {
				results = append(results, got)
				if typ == "||" {
					break
				}
			}
		}
		return mergeStrings(results, typ)
	}

	parts, typ := splitCombinator(trimmed)
	var results [][]string
	for _, part := range parts {
		got := j.getResultList(part)
		if len(got) > 0 {
			results = append(results, got)
			if typ == "||" {
				break
			}
		}
	}
	if len(results) == 0 && len(parts) == 1 && trimmed == "" {
		return []string{JsoupText(j.root)}
	}
	return mergeStrings(results, typ)
}

// GetElements 取元素集合（列表规则）
func (j *Jsoup) GetElements(rule string) []*html.Node {
	if rule == "" {
		return nil
	}
	trimmed := strings.TrimSpace(rule)
	isCSS := strings.HasPrefix(strings.ToLower(trimmed), "@css:")
	body := trimmed
	if isCSS {
		body = strings.TrimSpace(trimmed[5:])
	}
	parts, typ := splitCombinator(body)
	var groups [][]*html.Node
	for _, part := range parts {
		var found []*html.Node
		switch {
		case isCSS:
			found = j.selectRule(j.root, part)
		case strings.Contains(part, "@"):
			segments := rules.SplitBalanced(part, "@")
			current := []*html.Node{j.root}
			for _, seg := range segments {
				seg = strings.TrimSpace(seg)
				if seg == "" {
					continue
				}
				var next []*html.Node
				for _, el := range current {
					next = append(next, j.selectSingle(el, seg)...)
				}
				current = dedupeNodes(next)
			}
			found = current
		default:
			found = j.selectSingle(j.root, part)
		}
		kept := make([]*html.Node, 0, len(found))
		for _, n := range found {
			if n != j.root {
				kept = append(kept, n)
			}
		}
		if len(kept) > 0 {
			groups = append(groups, kept)
			if typ == "||" {
				break
			}
		}
	}
	return mergeNodes(groups, typ)
}

// getResultList 处理 `a@b@text`：逐段下钻，最后一段取内容
func (j *Jsoup) getResultList(ruleStr string) []string {
	if ruleStr == "" {
		return nil
	}
	raw := rules.SplitBalanced(ruleStr, "@")
	var segments []string
	for _, s := range raw {
		segments = append(segments, strings.TrimSpace(s))
	}
	if len(segments) > 0 && segments[0] == "" {
		segments = segments[1:]
	}
	if len(segments) == 0 {
		return nil
	}
	last := segments[len(segments)-1]
	if len(segments) == 1 {
		if suffixRules[last] {
			return j.getResultLast([]*html.Node{j.root}, last)
		}
		return textOf(j.selectSingle(j.root, last))
	}
	current := []*html.Node{j.root}
	for _, seg := range segments[:len(segments)-1] {
		var next []*html.Node
		for _, el := range current {
			next = append(next, j.selectSingle(el, seg)...)
		}
		current = next
		if len(current) == 0 {
			return nil
		}
	}
	return j.getResultLast(current, last)
}

// getResultLast 处理最后一段（text / textNodes / ownText / html / all / 属性）
func (j *Jsoup) getResultLast(elements []*html.Node, lastRule string) []string {
	var out []string
	switch lastRule {
	case "text":
		for _, el := range elements {
			if t := JsoupText(el); t != "" {
				out = append(out, t)
			}
		}
		return out
	case "textNodes":
		for _, el := range elements {
			if tn := textNodeList(el); len(tn) > 0 {
				out = append(out, strings.Join(tn, "\n"))
			}
		}
		return out
	case "ownText":
		for _, el := range elements {
			if t := JsoupOwnText(el); t != "" {
				out = append(out, t)
			}
		}
		return out
	case "html":
		if s := outerHTML(elements, true); s != "" {
			out = append(out, s)
		}
		return out
	case "":
		return textOf(elements)
	case "all":
		out = append(out, outerHTML(elements, false))
		return out
	}
	for _, el := range elements {
		v := attrOf(el, lastRule)
		if v == "" {
			continue
		}
		dup := false
		for _, have := range out {
			if have == v {
				dup = true
				break
			}
		}
		if !dup {
			out = append(out, v)
		}
	}
	return out
}

// selectSingle 解析单段规则（可带索引）并选出元素
func (j *Jsoup) selectSingle(scope *html.Node, ruleRaw string) []*html.Node {
	parsed := ParseIndexSpec(ruleRaw)
	base := j.selectRule(scope, parsed.before)
	if len(parsed.def) == 0 && len(parsed.specs) == 0 {
		return base
	}
	indexes := ExpandIndexes(parsed, len(base))
	if parsed.split == '!' {
		out := make([]*html.Node, 0, len(base))
		for i, n := range base {
			skip := false
			for _, x := range indexes {
				if x == i {
					skip = true
					break
				}
			}
			if !skip {
				out = append(out, n)
			}
		}
		return out
	}
	var out []*html.Node
	for _, i := range indexes {
		if i >= 0 && i < len(base) {
			out = append(out, base[i])
		}
	}
	return out
}

// selectRule 按规则选出候选元素（不含索引处理）
func (j *Jsoup) selectRule(scope *html.Node, rule string) []*html.Node {
	if rule == "" {
		return childrenOf(scope)
	}
	parts := strings.Split(rule, ".")
	head := parts[0]
	rest := ""
	if len(parts) > 1 {
		rest = parts[1]
	}
	switch head {
	case "children":
		return childrenOf(scope)
	case "class":
		return j.matchSelfAndDescendants(scope, "."+cssEscape(rest))
	case "tag":
		return j.matchSelfAndDescendants(scope, rest)
	case "id":
		return j.matchSelfAndDescendants(scope, `[id="`+cssEscape(rest)+`"]`)
	case "text":
		var out []*html.Node
		for _, el := range j.matchSelfAndDescendants(scope, "*") {
			if strings.Contains(JsoupOwnText(el), rest) {
				out = append(out, el)
			}
		}
		return out
	}
	return j.matchSelfAndDescendants(scope, rule)
}

func (j *Jsoup) matchSelfAndDescendants(scope *html.Node, selector string) []*html.Node {
	sel := j.sel(scope)
	if scope == j.root {
		var out []*html.Node
		sel.Find(selector).Each(func(_ int, s *goquery.Selection) {
			if n := s.Get(0); n != nil {
				out = append(out, n)
			}
		})
		return out
	}
	var out []*html.Node
	if sel.Is(selector) {
		out = append(out, scope)
	}
	sel.Find(selector).Each(func(_ int, s *goquery.Selection) {
		if n := s.Get(0); n != nil {
			out = append(out, n)
		}
	})
	return dedupeNodes(out)
}

// ── 工具函数 ────────────────────────────────────────────────────────────────

func attrOf(n *html.Node, name string) string {
	if n == nil {
		return ""
	}
	for _, a := range n.Attr {
		if strings.EqualFold(a.Key, name) {
			return a.Val
		}
	}
	return ""
}

func textOf(elements []*html.Node) []string {
	var out []string
	for _, el := range elements {
		if t := JsoupText(el); t != "" {
			out = append(out, t)
		}
	}
	return out
}

// outerHTML 拼接元素的 outerHTML；stripScript 时先删掉 script/style
func outerHTML(elements []*html.Node, stripScript bool) string {
	var b strings.Builder
	for _, el := range elements {
		if el == nil {
			continue
		}
		node := el
		if stripScript {
			node = cloneWithoutScript(el)
		}
		var buf strings.Builder
		if err := html.Render(&buf, node); err == nil {
			b.WriteString(buf.String())
		}
	}
	return b.String()
}

func cloneWithoutScript(n *html.Node) *html.Node {
	clone := shallowClone(n)
	var last *html.Node
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if c.Type == html.ElementNode {
			name := strings.ToLower(c.Data)
			if name == "script" || name == "style" {
				continue
			}
		}
		copied := cloneWithoutScript(c)
		copied.Parent = clone
		if last == nil {
			clone.FirstChild = copied
		} else {
			last.NextSibling = copied
			copied.PrevSibling = last
		}
		last = copied
	}
	clone.LastChild = last
	return clone
}

func shallowClone(n *html.Node) *html.Node {
	attrs := make([]html.Attribute, len(n.Attr))
	copy(attrs, n.Attr)
	return &html.Node{Type: n.Type, DataAtom: n.DataAtom, Data: n.Data, Attr: attrs}
}

func dedupeNodes(list []*html.Node) []*html.Node {
	seen := make(map[*html.Node]bool, len(list))
	out := make([]*html.Node, 0, len(list))
	for _, n := range list {
		if n == nil || seen[n] {
			continue
		}
		seen[n] = true
		out = append(out, n)
	}
	return out
}

func mergeNodes(groups [][]*html.Node, typ string) []*html.Node {
	if len(groups) == 0 {
		return nil
	}
	if typ == "%%" {
		max := 0
		for _, g := range groups {
			if len(g) > max {
				max = len(g)
			}
		}
		var out []*html.Node
		for i := 0; i < max; i++ {
			for _, g := range groups {
				if i < len(g) {
					out = append(out, g[i])
				}
			}
		}
		return out
	}
	var out []*html.Node
	for _, g := range groups {
		out = append(out, g...)
	}
	return out
}

func mergeStrings(results [][]string, typ string) []string {
	if len(results) == 0 {
		return nil
	}
	if typ == "%%" {
		max := 0
		for _, r := range results {
			if len(r) > max {
				max = len(r)
			}
		}
		var out []string
		for i := 0; i < max; i++ {
			for _, r := range results {
				if i < len(r) {
					out = append(out, r[i])
				}
			}
		}
		return out
	}
	var out []string
	for _, r := range results {
		out = append(out, r...)
	}
	return out
}

// splitCombinator 按 `&&` / `||` / `%%` 切分规则
func splitCombinator(rule string) ([]string, string) {
	raw := rules.SplitBalanced(rule, "&&", "||", "%%")
	parts := make([]string, 0, len(raw))
	for _, p := range raw {
		p = strings.TrimSpace(p)
		if p != "" {
			parts = append(parts, p)
		}
	}
	typ := ""
	if len(parts) > 1 {
		switch {
		case strings.Contains(rule, "%%"):
			typ = "%%"
		case strings.Contains(rule, "||"):
			typ = "||"
		default:
			typ = "&&"
		}
	}
	return parts, typ
}

func cssEscape(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `"`, `\"`)
	return s
}
