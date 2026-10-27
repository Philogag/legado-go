package analyze

import (
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"strings"

	"legado-go/internal/rules"
)

// Host 是规则执行所需的宿主能力（书源实例实现它）
type Host interface {
	// EvalJS 执行一段 JS，result 为当前中间结果
	EvalJS(js string, result any) (any, error)
	// GetVar 读取变量（`@get:{key}` / `{{key}}` 兜底）
	GetVar(key string) string
	// PutVar 写入变量（`@put:{}`）
	PutVar(key, value string)
}

// Rule 是统一的规则调度器（移植 Legado `AnalyzeRule`）
type Rule struct {
	host    Host
	Content any
	BaseURL string

	isJSON bool
	jsoup  *Jsoup
	xpath  *XPath
	json   *JsonPath
	cache  map[string][]*rules.SourceRule
	ctx    *ruleCtx
}

type ruleCtx struct{ r *Rule }

func (c *ruleCtx) EvalJS(js string, result any) (any, error) { return c.r.host.EvalJS(js, result) }
func (c *ruleCtx) GetVar(key string) string                  { return c.r.host.GetVar(key) }

// New 构造规则调度器
func New(host Host, content any, baseURL string) *Rule {
	r := &Rule{host: host, BaseURL: baseURL, cache: map[string][]*rules.SourceRule{}}
	r.ctx = &ruleCtx{r: r}
	r.SetContent(content, baseURL)
	return r
}

// SetContent 设置内容（同时重置解析器缓存）
func (r *Rule) SetContent(content any, baseURL string) *Rule {
	r.Content = content
	r.isJSON = isJSONContent(content)
	if baseURL != "" {
		r.BaseURL = baseURL
	}
	r.jsoup, r.xpath, r.json = nil, nil, nil
	return r
}

// SetBaseURL 设置基准 URL
func (r *Rule) SetBaseURL(u string) {
	if u != "" {
		r.BaseURL = u
	}
}

func (r *Rule) byJsoup(content any) *Jsoup {
	if r.jsoup == nil || content != r.Content {
		return NewJsoup(content)
	}
	return r.jsoup
}

func (r *Rule) getJsoup() *Jsoup {
	if r.jsoup == nil {
		r.jsoup = NewJsoup(r.Content)
	}
	return r.jsoup
}

func (r *Rule) getXPath() *XPath {
	if r.xpath == nil {
		r.xpath = NewXPath(r.Content)
	}
	return r.xpath
}

func (r *Rule) getJSON() *JsonPath {
	if r.json == nil {
		r.json = NewJsonPath(r.Content)
	}
	return r.json
}

func (r *Rule) rules(ruleStr string, allInOne bool) []*rules.SourceRule {
	if ruleStr == "" {
		return nil
	}
	key := ruleStr
	if allInOne {
		key = ":" + ruleStr
	}
	if cached, ok := r.cache[key]; ok {
		return cached
	}
	list := rules.SplitSourceRule(r.ctx, ruleStr, allInOne, r.isJSON)
	r.cache[key] = list
	return list
}

func (r *Rule) putRule(list []rules.PutEntry) error {
	for _, entry := range list {
		v, err := r.GetString(entry.Value)
		if err != nil {
			return err
		}
		r.host.PutVar(entry.Key, v)
	}
	return nil
}

// GetString 取文本
func (r *Rule) GetString(ruleStr string) (string, error) {
	return r.GetStringOn(ruleStr, nil, false)
}

// GetStringIsURL 取 URL（相对地址会拼成绝对地址）
func (r *Rule) GetStringIsURL(ruleStr string) (string, error) {
	return r.GetStringOn(ruleStr, nil, true)
}

// GetStringOn 在指定内容上取文本
func (r *Rule) GetStringOn(ruleStr string, content any, isURL bool) (string, error) {
	if ruleStr == "" {
		return "", nil
	}
	list := r.rules(ruleStr, false)
	return r.getString0(list, content, isURL)
}

func (r *Rule) getString0(list []*rules.SourceRule, content any, isURL bool) (string, error) {
	var result any
	src := content
	if src == nil {
		src = r.Content
	}
	if src != nil && len(list) > 0 {
		result = src
		if obj, ok := src.(map[string]any); ok && list[0].Mode == rules.ModeDefault {
			sr := list[0]
			if err := r.putRule(sr.PutMap); err != nil {
				return "", err
			}
			if err := sr.MakeUpRule(result); err != nil {
				return "", err
			}
			if sr.HasParams() {
				result = sr.Rule
			} else if v, hit := obj[sr.Rule]; hit && v != nil {
				result = rules.JsResultToString(v)
			} else {
				result = ""
			}
			if sr.ReplaceRegex != "" {
				s, err := r.replaceRegex(rules.JsResultToString(result), sr)
				if err != nil {
					return "", err
				}
				result = s
			}
		} else {
			for _, sr := range list {
				if err := r.putRule(sr.PutMap); err != nil {
					return "", err
				}
				if err := sr.MakeUpRule(result); err != nil {
					return "", err
				}
				if result == nil {
					continue
				}
				kind := "string"
				if isURL {
					kind = "url"
				}
				if strings.TrimSpace(sr.Rule) != "" || sr.ReplaceRegex == "" {
					var err error
					result, err = r.applyRule(sr, result, kind)
					if err != nil {
						return "", err
					}
				}
				if result != nil && sr.ReplaceRegex != "" {
					s, err := r.replaceRegex(rules.JsResultToString(result), sr)
					if err != nil {
						return "", err
					}
					result = s
				}
			}
		}
	}
	out := rules.JsResultToString(result)
	if strings.Contains(out, "&") {
		out = UnescapeHTML(out)
	}
	if isURL {
		if strings.TrimSpace(out) == "" {
			return r.BaseURL, nil
		}
		return AbsoluteURL(r.BaseURL, out), nil
	}
	return out, nil
}

// applyRule 按规则模式分派到具体解析器
func (r *Rule) applyRule(sr *rules.SourceRule, result any, kind string) (any, error) {
	rule := sr.Rule
	switch sr.Mode {
	case rules.ModeJS:
		return r.host.EvalJS(rule, result)
	case rules.ModeJSON:
		j := r.byJSON(result)
		switch kind {
		case "elements":
			return j.GetList(rule), nil
		case "element":
			return j.GetObject(rule), nil
		case "list":
			return j.GetStringList(rule), nil
		}
		return j.GetString(rule), nil
	case rules.ModeXPath:
		x := r.byXPath(result)
		switch kind {
		case "element", "elements", "list":
			out := make([]any, 0)
			for _, n := range x.GetElements(rule) {
				out = append(out, n)
			}
			if kind == "element" {
				if len(out) == 0 {
					return nil, nil
				}
				return out[0], nil
			}
			return out, nil
		}
		return x.GetString(rule), nil
	case rules.ModeRegex:
		// 上游语义：`getString` / `getStringList` 下 `Mode.Regex` 直接把（已展开插值的）规则文本
		// 当作结果返回（`{{js}}` / `@get:{}` 靠这一步取值）；只有取元素/对象时才用正则抽取。
		switch kind {
		case "element", "elements":
			groups := Regex.GetElements(rules.JsResultToString(result), rules.SplitNotBlank(rule, "&&"))
			out := make([]any, 0, len(groups))
			for _, g := range groups {
				for _, s := range g {
					out = append(out, s)
				}
			}
			return out, nil
		case "object":
			groups := Regex.GetElement(rules.JsResultToString(result), rules.SplitNotBlank(rule, "&&"))
			if groups == nil {
				return nil, nil
			}
			return groups[0], nil
		}
		return rule, nil
	}

	j := r.byJsoup(result)
	switch kind {
	case "url":
		return j.GetString0(rule), nil
	case "list":
		return j.GetStringList(rule), nil
	case "elements":
		out := make([]any, 0)
		for _, n := range j.GetElements(rule) {
			out = append(out, n)
		}
		return out, nil
	case "element":
		nodes := j.GetElements(rule)
		if len(nodes) == 0 {
			return nil, nil
		}
		return nodes[0], nil
	}
	return j.GetString(rule), nil
}

func (r *Rule) byJSON(content any) *JsonPath {
	if !sameContent(content, r.Content) {
		return NewJsonPath(content)
	}
	return r.getJSON()
}

func (r *Rule) byXPath(content any) *XPath {
	if !sameContent(content, r.Content) {
		return NewXPath(content)
	}
	return r.getXPath()
}

// sameContent 安全比较两段解析内容：map/slice 等不可比较类型直接判定为不同。
func sameContent(a, b any) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	switch a.(type) {
	case string, []byte, bool, int, int64, float64:
		return a == b
	}
	return false
}

// GetStringList 取文本列表
func (r *Rule) GetStringList(ruleStr string) ([]string, error) {
	return r.GetStringListOn(ruleStr, nil, false)
}

// GetStringListIsURL 取 URL 列表（去重）
func (r *Rule) GetStringListIsURL(ruleStr string) ([]string, error) {
	return r.GetStringListOn(ruleStr, nil, true)
}

// GetStringListOn 在指定内容上取文本列表
func (r *Rule) GetStringListOn(ruleStr string, content any, isURL bool) ([]string, error) {
	if ruleStr == "" {
		return nil, nil
	}
	list := r.rules(ruleStr, false)
	var result any
	src := content
	if src == nil {
		src = r.Content
	}
	if src != nil && len(list) > 0 {
		result = src
		if obj, ok := src.(map[string]any); ok && list[0].Mode == rules.ModeDefault {
			sr := list[0]
			if err := r.putRule(sr.PutMap); err != nil {
				return nil, err
			}
			if err := sr.MakeUpRule(result); err != nil {
				return nil, err
			}
			if sr.HasParams() {
				result = sr.Rule
			} else {
				result = obj[sr.Rule]
			}
			if sr.ReplaceRegex != "" && result != nil {
				replaced, err := r.replaceEach(result, sr)
				if err != nil {
					return nil, err
				}
				result = replaced
			}
		} else {
			for _, sr := range list {
				if err := r.putRule(sr.PutMap); err != nil {
					return nil, err
				}
				if err := sr.MakeUpRule(result); err != nil {
					return nil, err
				}
				if result == nil {
					continue
				}
				if sr.Rule != "" {
					var err error
					result, err = r.applyRule(sr, result, "list")
					if err != nil {
						return nil, err
					}
				}
				if result != nil && sr.ReplaceRegex != "" {
					replaced, err := r.replaceEach(result, sr)
					if err != nil {
						return nil, err
					}
					result = replaced
				}
			}
		}
	}
	if result == nil {
		return nil, nil
	}
	var out []string
	switch v := result.(type) {
	case string:
		out = strings.Split(v, "\n")
	case []string:
		out = v
	case []any:
		out = make([]string, len(v))
		for i, item := range v {
			out[i] = rules.JsResultToString(item)
		}
	default:
		out = []string{rules.JsResultToString(v)}
	}
	if isURL {
		var urls []string
		for _, u := range out {
			abs := AbsoluteURL(r.BaseURL, u)
			if abs == "" {
				continue
			}
			dup := false
			for _, have := range urls {
				if have == abs {
					dup = true
					break
				}
			}
			if !dup {
				urls = append(urls, abs)
			}
		}
		return urls, nil
	}
	return out, nil
}

func (r *Rule) replaceEach(result any, sr *rules.SourceRule) (any, error) {
	if list, ok := result.([]string); ok {
		out := make([]string, len(list))
		for i, item := range list {
			s, err := r.replaceRegex(item, sr)
			if err != nil {
				return nil, err
			}
			out[i] = s
		}
		return out, nil
	}
	if list, ok := result.([]any); ok {
		out := make([]any, len(list))
		for i, item := range list {
			s, err := r.replaceRegex(rules.JsResultToString(item), sr)
			if err != nil {
				return nil, err
			}
			out[i] = s
		}
		return out, nil
	}
	return r.replaceRegex(rules.JsResultToString(result), sr)
}

// GetElement 取单个元素（列表项节点/对象）
func (r *Rule) GetElement(ruleStr string) (any, error) {
	if ruleStr == "" {
		return nil, nil
	}
	list := r.rules(ruleStr, true)
	result := r.Content
	if result == nil || len(list) == 0 {
		return nil, nil
	}
	for _, sr := range list {
		if err := r.putRule(sr.PutMap); err != nil {
			return nil, err
		}
		if err := sr.MakeUpRule(result); err != nil {
			return nil, err
		}
		if result == nil {
			continue
		}
		var err error
		result, err = r.applyRule(sr, result, "element")
		if err != nil {
			return nil, err
		}
		if sr.ReplaceRegex != "" && result != nil {
			s, err := r.replaceRegex(rules.JsResultToString(result), sr)
			if err != nil {
				return nil, err
			}
			result = s
		}
	}
	return result, nil
}

// GetElements 取元素列表
func (r *Rule) GetElements(ruleStr string) ([]any, error) {
	if ruleStr == "" {
		return nil, nil
	}
	list := r.rules(ruleStr, true)
	result := r.Content
	if result == nil || len(list) == 0 {
		return nil, nil
	}
	for _, sr := range list {
		if err := r.putRule(sr.PutMap); err != nil {
			return nil, err
		}
		if err := sr.MakeUpRule(result); err != nil {
			return nil, err
		}
		if result == nil {
			continue
		}
		var err error
		result, err = r.applyRule(sr, result, "elements")
		if err != nil {
			return nil, err
		}
	}
	if out, ok := result.([]any); ok {
		return out, nil
	}
	return nil, nil
}

var jsReplacePattern = regexp.MustCompile(`(?is)^(?:@js:|<js>)([\s\S]*)$`)

// replaceRegex 执行 `##匹配##替换[###]` 后置替换
func (r *Rule) replaceRegex(result string, sr *rules.SourceRule) (string, error) {
	if sr.ReplaceRegex == "" {
		return result, nil
	}
	// `##@js:...` / `##<js>...</js>`：用已解析结果做 JS 后处理
	if m := jsReplacePattern.FindStringSubmatch(strings.TrimSpace(sr.ReplaceRegex)); m != nil {
		body := strings.TrimSuffix(strings.TrimSpace(m[1]), "</js>")
		body = regexp.MustCompile(`(?i)</js>\s*$`).ReplaceAllString(body, "")
		v, err := r.host.EvalJS(body, result)
		if err != nil {
			return "", err
		}
		return rules.JsResultToString(v), nil
	}
	re, err := compileReplace(sr.ReplaceRegex, !sr.ReplaceFirst)
	if err != nil {
		if sr.ReplaceFirst {
			return sr.Replacement, nil
		}
		return strings.ReplaceAll(result, sr.ReplaceRegex, sr.Replacement), nil
	}
	if sr.ReplaceFirst {
		loc := re.FindStringIndex(result)
		if loc == nil {
			return "", nil
		}
		matched := result[loc[0]:loc[1]]
		one, err := compileReplace(sr.ReplaceRegex, false)
		if err != nil {
			return sr.Replacement, nil
		}
		return one.ReplaceAllString(matched, sr.Replacement), nil
	}
	return re.ReplaceAllString(result, sr.Replacement), nil
}

func compileReplace(pattern string, global bool) (*regexp.Regexp, error) {
	prefix := "(?s)"
	if !global {
		prefix = "(?s)"
	}
	re, err := regexp.Compile(prefix + pattern)
	if err != nil {
		return nil, err
	}
	return re, nil
}

// isJSONContent 只有 JSON 文本、JSON 数组或普通对象才走 JsonPath
func isJSONContent(content any) bool {
	switch v := content.(type) {
	case string:
		return isJSONText(v)
	case []any:
		return true
	case map[string]any:
		return true
	}
	return false
}

func isJSONText(text string) bool {
	t := strings.TrimSpace(text)
	if t == "" {
		return false
	}
	if t[0] != '{' && t[0] != '[' {
		return false
	}
	var v any
	return json.Unmarshal([]byte(t), &v) == nil
}

var namedEntities = map[string]string{
	"amp": "&", "lt": "<", "gt": ">", "quot": "\"", "apos": "'", "nbsp": "\u00a0",
	"copy": "\u00a9", "hellip": "\u2026", "mdash": "\u2014", "ndash": "\u2013",
	"ldquo": "\u201c", "rdquo": "\u201d", "lsquo": "\u2018", "rsquo": "\u2019",
}

var entityPattern = regexp.MustCompile(`&(#x?[0-9a-fA-F]+|[a-zA-Z][a-zA-Z0-9]*);`)

// UnescapeHTML 近似 `StringEscapeUtils.unescapeHtml4`
func UnescapeHTML(text string) string {
	if !strings.Contains(text, "&") {
		return text
	}
	return entityPattern.ReplaceAllStringFunc(text, func(all string) string {
		body := all[1 : len(all)-1]
		lower := strings.ToLower(body)
		if strings.HasPrefix(lower, "#x") {
			var code int64
			if _, err := fmt.Sscanf(lower[2:], "%x", &code); err == nil {
				return string(rune(code))
			}
			return all
		}
		if strings.HasPrefix(lower, "#") {
			var code int64
			if _, err := fmt.Sscanf(lower[1:], "%d", &code); err == nil {
				return string(rune(code))
			}
			return all
		}
		if v, ok := namedEntities[lower]; ok {
			return v
		}
		return all
	})
}

// AbsoluteURL 把相对地址补成绝对地址
func AbsoluteURL(baseURL, ref string) string {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return ""
	}
	lower := strings.ToLower(ref)
	if strings.HasPrefix(lower, "http://") || strings.HasPrefix(lower, "https://") ||
		strings.HasPrefix(lower, "data:") || strings.HasPrefix(lower, "file:") {
		return ref
	}
	base := strings.TrimSpace(baseURL)
	if base == "" {
		return ref
	}
	b, err := url.Parse(base)
	if err != nil {
		return ref
	}
	if strings.HasPrefix(ref, "//") {
		return b.Scheme + ":" + ref
	}
	r, err := url.Parse(ref)
	if err != nil {
		return ref
	}
	return b.ResolveReference(r).String()
}
