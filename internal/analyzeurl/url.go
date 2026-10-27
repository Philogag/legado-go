// Package analyzeurl 解析书源的 url 规则（移植 Legado `AnalyzeUrl` / `UrlOption`）。
//
// 规则形态：
//
//	https://a.com/search?q={{key}}&p={{page}}
//	https://a.com/search,{"method":"POST","body":"searchkey={{key}}","charset":"gbk"}
//	<js>一段返回 url 的脚本</js>
//
// `{{}}` 内是 JS 表达式，可用变量：key / page / baseUrl / title / src 以及 java、source、cookie、cache。
package analyzeurl

import (
	"encoding/json"
	"net/url"
	"strconv"
	"strings"
)

// Option 是 url 规则里 `,{...}` 部分的选项
type Option struct {
	Method      string            `json:"method"`
	Headers     map[string]string `json:"headers"`
	Body        string            `json:"body"`
	Charset     string            `json:"charset"`
	Retry       int               `json:"retry"`
	JS          string            `json:"js"`
	Type        string            `json:"type"`
	Cookies     string            `json:"cookies"`
	SourceRegex string            `json:"sourceRegex"`
	WebView     bool              `json:"webView"`
	WebJS       string            `json:"webJs"`
}

// Parsed 是解析结果
type Parsed struct {
	URL         string
	Method      string
	Body        string
	Headers     map[string]string
	Charset     string
	Retry       int
	Cookies     string
	SourceRegex string
}

// Context 是 URL 规则解析所需的宿主能力
type Context interface {
	// EvalJS 执行 `{{}}` 里的 JS
	EvalJS(js string) (any, error)
	// DefaultHeaders 书源默认请求头（含登录头）
	DefaultHeaders() map[string]string
	// CookieGet 读取某 url 的 cookie
	CookieGet(url string) string
	// CookiesEnabled 是否启用 cookie
	CookiesEnabled() bool
}

// SplitURLRule 把规则串拆成「url 模板 + 选项」
func SplitURLRule(urlStr string) (string, Option) {
	text := strings.TrimSpace(urlStr)
	cut := findOptionStart(text)
	if cut < 0 {
		return text, Option{}
	}
	template := strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(text[:cut]), ","))
	var opt Option
	if err := json.Unmarshal([]byte(strings.TrimSpace(text[cut:])), &opt); err != nil {
		return text, Option{}
	}
	return template, opt
}

// findOptionStart 返回选项 JSON 的起始位置（`{` 的下标）。
// 支持两种写法：legado 的 `url,{"method":"POST"}` 与省略逗号的 `url{"method":"POST"}`。
func findOptionStart(text string) int {
	// 1) legado 标准写法：`,{...}`
	for i := 0; i+1 < len(text); i++ {
		if text[i] != ',' || text[i+1] != '{' {
			continue
		}
		if isOptionJSON(text[i+1:]) {
			return i + 1
		}
	}
	// 2) 宽松写法：URL 后直接跟 `{"...` （选项块在末尾）
	for i := 1; i+1 < len(text); i++ {
		if text[i] != '{' || text[i+1] != '"' {
			continue
		}
		if isOptionJSON(text[i:]) && looksLikeOption(text[i:]) {
			return i
		}
	}
	return -1
}

// optionKeys 是 URL 选项块支持的字段名
var optionKeys = map[string]bool{
	"method": true, "headers": true, "body": true, "charset": true, "retry": true,
	"js": true, "type": true, "cookies": true, "sourceregex": true, "webview": true, "webjs": true,
}

// looksLikeOption 判断 JSON 对象的 key 至少命中一个选项字段
func looksLikeOption(candidate string) bool {
	var raw map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(candidate)), &raw); err != nil {
		return false
	}
	for k := range raw {
		if optionKeys[strings.ToLower(k)] {
			return true
		}
	}
	return false
}

// isOptionJSON 判断整段文本能否解析为 URL 选项对象。
func isOptionJSON(candidate string) bool {
	trimmed := strings.TrimSpace(candidate)
	if !strings.HasPrefix(trimmed, "{") || !json.Valid([]byte(trimmed)) {
		return false
	}
	var opt Option
	return json.Unmarshal([]byte(trimmed), &opt) == nil
}

// ReplaceTemplate 替换 `{{expr}}`（允许嵌套一层花括号）
func ReplaceTemplate(text string, eval func(string) (any, error)) (string, error) {
	if text == "" || !strings.Contains(text, "{{") {
		return text, nil
	}
	var b strings.Builder
	i := 0
	for i < len(text) {
		start := strings.Index(text[i:], "{{")
		if start < 0 {
			b.WriteString(text[i:])
			break
		}
		start += i
		b.WriteString(text[i:start])
		depth := 0
		end := -1
		for j := start + 2; j < len(text); j++ {
			ch := text[j]
			if ch == '{' {
				depth++
				continue
			}
			if ch == '}' {
				if depth == 0 && j+1 < len(text) && text[j+1] == '}' {
					end = j
					break
				}
				depth--
			}
		}
		if end < 0 {
			b.WriteString(text[start:])
			break
		}
		expr := strings.TrimSpace(text[start+2 : end])
		value, err := eval(expr)
		if err != nil {
			return "", err
		}
		b.WriteString(stringify(value))
		i = end + 2
	}
	return b.String(), nil
}

// Analyze 解析 url 规则，得到最终请求描述
func Analyze(ctx Context, urlStr string, bindings map[string]any) (*Parsed, error) {
	raw := strings.TrimSpace(urlStr)
	eval := func(js string) (any, error) { return ctx.EvalJS(js) }

	template := raw
	opt := Option{}
	jsOnly := ""
	if m := regexpJSOnly(raw); m != "" {
		jsOnly = m
	}
	if jsOnly != "" {
		out, err := eval(jsOnly)
		if err != nil {
			return nil, err
		}
		template = stringify(out)
	} else {
		template, opt = SplitURLRule(raw)
	}

	finalURL, err := ReplaceTemplate(template, eval)
	if err != nil {
		return nil, err
	}
	if base, ok := bindings["baseUrl"].(string); ok && base != "" {
		finalURL = resolveURL(base, finalURL)
	}

	headers := map[string]string{}
	for k, v := range ctx.DefaultHeaders() {
		headers[k] = v
	}
	for k, v := range opt.Headers {
		headers[k] = v
	}

	body := ""
	if opt.Body != "" {
		body, err = ReplaceTemplate(opt.Body, eval)
		if err != nil {
			return nil, err
		}
	}

	// 选项里的 js：可返回 url
	if opt.JS != "" {
		out, err := eval(opt.JS)
		if err != nil {
			return nil, err
		}
		if s, ok := out.(string); ok && strings.TrimSpace(s) != "" {
			finalURL = strings.TrimSpace(s)
		}
	}

	cookieHeader := ""
	if v, ok := headers["Cookie"]; ok {
		cookieHeader = v
	} else if v, ok := headers["cookie"]; ok {
		cookieHeader = v
	}
	switch {
	case opt.Cookies != "":
		cookieHeader = opt.Cookies
	case ctx.CookiesEnabled():
		if stored := ctx.CookieGet(finalURL); stored != "" {
			if cookieHeader != "" {
				cookieHeader = cookieHeader + "; " + stored
			} else {
				cookieHeader = stored
			}
		}
	}
	if cookieHeader != "" {
		delete(headers, "cookie")
		headers["Cookie"] = cookieHeader
	}

	method := strings.ToUpper(strings.TrimSpace(opt.Method))
	if method == "" {
		method = "GET"
	}
	retry := opt.Retry
	if retry < 0 {
		retry = 0
	}
	return &Parsed{
		URL:         finalURL,
		Method:      method,
		Body:        body,
		Headers:     headers,
		Charset:     opt.Charset,
		Retry:       retry,
		Cookies:     opt.Cookies,
		SourceRegex: opt.SourceRegex,
	}, nil
}

// resolveURL 用 base 解析相对地址（绝对地址原样返回）
func resolveURL(base, ref string) string {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return ref
	}
	ru, err := url.Parse(ref)
	if err != nil {
		return ref
	}
	if ru.IsAbs() {
		return ru.String()
	}
	bu, err := url.Parse(strings.TrimSpace(base))
	if err != nil {
		return ref
	}
	return bu.ResolveReference(ru).String()
}

// regexpJSOnly 识别整串就是 JS 的 url 规则（`<js>...</js>` / `@js:...`）
func regexpJSOnly(raw string) string {
	lower := strings.ToLower(raw)
	if strings.HasPrefix(lower, "<js>") && strings.HasSuffix(lower, "</js>") {
		return raw[4 : len(raw)-5]
	}
	if strings.HasPrefix(lower, "@js:") {
		return raw[4:]
	}
	return ""
}

func stringify(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return t
	case bool:
		if t {
			return "true"
		}
		return "false"
	case int:
		return strconv.Itoa(t)
	case int64:
		return strconv.FormatInt(t, 10)
	case float64:
		if t == float64(int64(t)) {
			return strconv.FormatInt(int64(t), 10)
		}
		return strconv.FormatFloat(t, 'f', -1, 64)
	}
	b, err := json.Marshal(v)
	if err != nil {
		return ""
	}
	return string(b)
}
