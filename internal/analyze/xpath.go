package analyze

import (
	"encoding/json"
	"regexp"
	"strings"

	"github.com/antchfx/htmlquery"
	"golang.org/x/net/html"

	"legado-go/internal/jsonpath"
	"legado-go/internal/rules"
)

// XPath 是 XPath 规则解析器（移植 Legado `AnalyzeByXPath`）。
type XPath struct {
	doc *html.Node
}

var attrTailPattern = regexp.MustCompile(`^([\s\S]*)/@([A-Za-z_][-\w:.]*)$`)

// NewXPath 以字符串（HTML）或元素节点构造解析器
func NewXPath(content any) *XPath {
	switch v := content.(type) {
	case *html.Node:
		return &XPath{doc: v}
	case Node:
		return &XPath{doc: v.N}
	case nil:
		return &XPath{doc: parseHTML("")}
	}
	return &XPath{doc: parseHTML(rules.JsResultToString(content))}
}

// nodes 执行一条 XPath，返回匹配到的节点
func (x *XPath) nodes(rule string) []*html.Node {
	rule = strings.TrimSpace(rule)
	if rule == "" {
		return nil
	}
	// `//a/@href` 这类属性轴：htmlquery 会丢掉属性值，这里单独处理
	if m := attrTailPattern.FindStringSubmatch(rule); m != nil && strings.Contains(m[1], "/") {
		return attrPseudoNodes(x.nodes(m[1]), m[2])
	}
	list, err := htmlquery.QueryAll(x.doc, rule)
	if err != nil {
		return nil
	}
	return list
}

// attrPseudoNodes 把「取属性」表达成文本节点，便于统一转字符串
func attrPseudoNodes(nodes []*html.Node, attr string) []*html.Node {
	out := make([]*html.Node, 0, len(nodes))
	for _, n := range nodes {
		v := attrOf(n, attr)
		if v == "" {
			continue
		}
		out = append(out, &html.Node{Type: html.TextNode, Data: v})
	}
	return out
}

func nodeToString(n *html.Node) string {
	if n == nil {
		return ""
	}
	switch n.Type {
	case html.TextNode:
		return n.Data
	case html.DocumentNode:
		var b strings.Builder
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			if err := html.Render(&b, c); err == nil {
				continue
			}
		}
		return b.String()
	}
	return JsoupText(n)
}

// GetString 取规则结果文本（多个节点用 \n 连接）
func (x *XPath) GetString(rule string) string {
	if rule == "" {
		return ""
	}
	parts, typ := splitCombinator(rule)
	if len(parts) <= 1 {
		nodes := x.nodes(rule)
		if len(nodes) == 0 {
			return ""
		}
		out := make([]string, 0, len(nodes))
		for _, n := range nodes {
			out = append(out, nodeToString(n))
		}
		return strings.Join(out, "\n")
	}
	var texts []string
	for _, part := range parts {
		if got := x.GetString(part); got != "" {
			texts = append(texts, got)
			if typ == "||" {
				break
			}
		}
	}
	return strings.Join(texts, "\n")
}

// GetStringList 取规则结果列表
func (x *XPath) GetStringList(rule string) []string {
	if rule == "" {
		return nil
	}
	parts, typ := splitCombinator(rule)
	if len(parts) <= 1 {
		var out []string
		for _, n := range x.nodes(rule) {
			out = append(out, nodeToString(n))
		}
		return out
	}
	var results [][]string
	for _, part := range parts {
		if got := x.GetStringList(part); len(got) > 0 {
			results = append(results, got)
			if typ == "||" {
				break
			}
		}
	}
	return mergeStrings(results, typ)
}

// GetElements 取规则匹配的元素
func (x *XPath) GetElements(rule string) []*html.Node {
	if rule == "" {
		return nil
	}
	parts, typ := splitCombinator(rule)
	var groups [][]*html.Node
	for _, part := range parts {
		got := x.nodes(part)
		if len(got) > 0 {
			groups = append(groups, got)
			if typ == "||" {
				break
			}
		}
	}
	return mergeNodes(groups, typ)
}

// JsonPath 是 JsonPath 规则解析器（移植 Legado `AnalyzeByJSonPath`）
type JsonPath struct {
	json any
}

// NewJsonPath 构造解析器：字符串会被尝试解析成 JSON
func NewJsonPath(content any) *JsonPath {
	if s, ok := content.(string); ok {
		var v any
		if err := json.Unmarshal([]byte(strings.TrimSpace(s)), &v); err == nil {
			return &JsonPath{json: v}
		}
		return &JsonPath{json: s}
	}
	return &JsonPath{json: content}
}

// splitUnion 拆开顶层多路径并集（`$.a,$.b` / `$.a|$.b`）
func splitUnion(rule string) []string {
	parts := rules.SplitBalanced(rule, ",", "|")
	if len(parts) <= 1 {
		return []string{rule}
	}
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	if len(out) <= 1 {
		return []string{rule}
	}
	return out
}

// query 执行一条（可能含并集的）jsonpath，返回展开后的所有结果
func (j *JsonPath) query(rule string) []any {
	var values []any
	for _, part := range splitUnion(rule) {
		got, err := jsonpath.Eval(j.json, part)
		if err != nil {
			continue
		}
		values = append(values, got...)
	}
	return values
}

func stringify(v any) string {
	return rules.JsResultToString(v)
}

// GetString 取单值文本
func (j *JsonPath) GetString(rule string) string {
	if rule == "" {
		return ""
	}
	parts, typ := splitCombinator(rule)
	if len(parts) <= 1 {
		values := j.query(rule)
		if len(values) == 0 {
			return ""
		}
		if len(values) == 1 {
			if list, ok := values[0].([]any); ok {
				out := make([]string, len(list))
				for i, v := range list {
					out[i] = stringify(v)
				}
				return strings.Join(out, "\n")
			}
			return stringify(values[0])
		}
		out := make([]string, len(values))
		for i, v := range values {
			out[i] = stringify(v)
		}
		return strings.Join(out, "\n")
	}
	var texts []string
	for _, part := range parts {
		if got := j.GetString(part); got != "" {
			texts = append(texts, got)
			if typ == "||" {
				break
			}
		}
	}
	return strings.Join(texts, "\n")
}

// GetStringList 取列表
func (j *JsonPath) GetStringList(rule string) []string {
	if rule == "" {
		return nil
	}
	parts, typ := splitCombinator(rule)
	if len(parts) <= 1 {
		var out []string
		for _, v := range j.query(rule) {
			if list, ok := v.([]any); ok {
				for _, item := range list {
					out = append(out, stringify(item))
				}
				continue
			}
			out = append(out, stringify(v))
		}
		return out
	}
	var results [][]string
	for _, part := range parts {
		if got := j.GetStringList(part); len(got) > 0 {
			results = append(results, got)
			if typ == "||" {
				break
			}
		}
	}
	return mergeStrings(results, typ)
}

// GetObject 取单个对象（Jayway 基数语义：0 个 → nil，1 个 → 值本身，多个 → 数组）
func (j *JsonPath) GetObject(rule string) any {
	values := j.query(rule)
	switch len(values) {
	case 0:
		return nil
	case 1:
		return values[0]
	}
	return values
}

// GetList 取对象列表
func (j *JsonPath) GetList(rule string) []any {
	values := j.query(rule)
	if len(values) == 1 {
		if list, ok := values[0].([]any); ok {
			return list
		}
	}
	return values
}
