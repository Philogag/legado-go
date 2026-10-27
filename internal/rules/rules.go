// Package rules 移植 Legado 的 SourceRule 规则串切分器。
//
// 一条规则串可以同时包含：模式前缀（`@@` / `@XPath:` / `@Json:` / `:` 正则）、
// JS 段（`<js></js>` 或行首 `@js:`）、内嵌插值（`{{...}}` / `@get:{key}`）、
// 正则捕获组引用（`$1`）、后置替换（`##匹配##替换[###]`）、变量暂存（`@put:{}`）。
package rules

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// Mode 规则模式
type Mode int

const (
	// ModeDefault 默认：CSS 选择器（jsoup）
	ModeDefault Mode = iota
	// ModeJSON JsonPath
	ModeJSON
	// ModeXPath XPath
	ModeXPath
	// ModeJS 纯 JS
	ModeJS
	// ModeRegex 正则
	ModeRegex
)

func (m Mode) String() string {
	switch m {
	case ModeJSON:
		return "json"
	case ModeXPath:
		return "xpath"
	case ModeJS:
		return "js"
	case ModeRegex:
		return "regex"
	}
	return "default"
}

// Ctx 是规则展开所需的宿主能力
type Ctx interface {
	// EvalJS 执行 JS，result 为当前中间结果
	EvalJS(js string, result any) (any, error)
	// GetVar 读取变量（`@get:{key}`）
	GetVar(key string) string
}

var (
	jsTagPattern    = regexp.MustCompile(`(?is)<js>([\w\W]*?)</js>`)
	jsPrefixPattern = regexp.MustCompile(`(?i)^@js:`)
	evalPattern     = regexp.MustCompile(`(?i)@get:\{[^}]+?\}|\{\{[\w\W]*?\}\}`)
	putPattern      = regexp.MustCompile(`(?i)@put:(\{[^}]+?\})`)
	groupRefPattern = regexp.MustCompile(`\$\d{1,2}`)

	cssPrefixPattern   = regexp.MustCompile(`(?i)^@css:`)
	xpathPrefixPattern = regexp.MustCompile(`(?i)^@xpath:`)
	jsonPrefixPattern  = regexp.MustCompile(`(?i)^@json:`)
	groupRefExact      = regexp.MustCompile(`^\$\d{1,2}$`)
)

const (
	paramText = 0
	paramJS   = -1
	paramGet  = -2
)

type param struct {
	kind  int
	value string
}

// PutEntry 是一条 `@put:{}` 记录
type PutEntry struct {
	Key   string
	Value string
}

// SourceRule 是一条解析后的规则
type SourceRule struct {
	ctx          Ctx
	Mode         Mode
	Rule         string
	ReplaceRegex string
	Replacement  string
	ReplaceFirst bool
	PutMap       []PutEntry

	params        []param
	isJSONContent bool
	parsedRule    string
}

// NewSourceRule 解析一条规则串
func NewSourceRule(ctx Ctx, ruleStr string, mode Mode, isJSONContent bool) *SourceRule {
	r := &SourceRule{ctx: ctx, Mode: mode, isJSONContent: isJSONContent}
	r.parsedRule = r.parse(ruleStr)
	r.Rule = r.parsedRule
	return r
}

func (r *SourceRule) parse(ruleStr string) string {
	var rule string
	switch {
	case r.Mode == ModeJS || r.Mode == ModeRegex:
		rule = ruleStr
	case cssPrefixPattern.MatchString(ruleStr):
		r.Mode = ModeDefault
		rule = ruleStr
	case strings.HasPrefix(ruleStr, "@@"):
		r.Mode = ModeDefault
		rule = strings.TrimPrefix(ruleStr, "@@")
	case xpathPrefixPattern.MatchString(ruleStr):
		r.Mode = ModeXPath
		rule = ruleStr[7:]
	case jsonPrefixPattern.MatchString(ruleStr):
		r.Mode = ModeJSON
		rule = ruleStr[6:]
	case r.isJSONContent || strings.HasPrefix(ruleStr, "$.") || strings.HasPrefix(ruleStr, "$["):
		r.Mode = ModeJSON
		rule = ruleStr
	case strings.HasPrefix(ruleStr, "/"):
		r.Mode = ModeXPath
		rule = ruleStr
	default:
		rule = ruleStr
	}
	rule = r.splitPutRule(rule)
	r.splitEval(rule)
	return rule
}

// splitPutRule 抽出 `@put:{...}` 并从规则串中删除
func (r *SourceRule) splitPutRule(ruleStr string) string {
	out := ruleStr
	for _, m := range putPattern.FindAllStringSubmatch(ruleStr, -1) {
		out = strings.ReplaceAll(out, m[0], "")
		var obj map[string]any
		if err := json.Unmarshal([]byte(m[1]), &obj); err != nil {
			continue
		}
		keys := make([]string, 0, len(obj))
		for k := range obj {
			keys = append(keys, k)
		}
		sortStrings(keys)
		for _, k := range keys {
			r.PutMap = append(r.PutMap, PutEntry{Key: k, Value: toString(obj[k])})
		}
	}
	return out
}

// splitEval 切出 `{{}}` / `@get:{}` / `$n` 段
func (r *SourceRule) splitEval(rule string) {
	locs := evalPattern.FindAllStringIndex(rule, -1)
	start := 0
	for _, loc := range locs {
		head := rule[start:loc[0]]
		if r.Mode != ModeJS && r.Mode != ModeRegex && (loc[0] == 0 || !strings.Contains(head, "##")) {
			r.Mode = ModeRegex
		}
		if loc[0] > start {
			r.splitGroupRef(head)
		}
		token := rule[loc[0]:loc[1]]
		if strings.HasPrefix(strings.ToLower(token), "@get:") {
			r.params = append(r.params, param{kind: paramGet, value: token[6 : len(token)-1]})
		} else {
			r.params = append(r.params, param{kind: paramJS, value: token[2 : len(token)-2]})
		}
		start = loc[1]
	}
	if start < len(rule) {
		r.splitGroupRef(rule[start:])
	}
}

func (r *SourceRule) splitGroupRef(text string) {
	head := strings.SplitN(text, "##", 2)[0]
	locs := groupRefPattern.FindAllStringIndex(head, -1)
	if len(locs) == 0 {
		if text != "" {
			r.params = append(r.params, param{kind: paramText, value: text})
		}
		return
	}
	start := 0
	for _, loc := range locs {
		if loc[0] > start {
			r.params = append(r.params, param{kind: paramText, value: text[start:loc[0]]})
		}
		r.params = append(r.params, param{kind: paramText, value: text[loc[0]:loc[1]]})
		start = loc[1]
	}
	if start < len(text) {
		r.params = append(r.params, param{kind: paramText, value: text[start:]})
	}
}

// HasParams 是否需要展开（有插值或分组引用）
func (r *SourceRule) HasParams() bool { return len(r.params) > 0 }

// MakeUpRule 用当前中间结果展开规则串（等价 Legado 的 makeUpRule）
func (r *SourceRule) MakeUpRule(result any) error {
	if len(r.params) > 0 {
		var b strings.Builder
		for _, p := range r.params {
			switch p.kind {
			case paramJS:
				v, err := r.ctx.EvalJS(p.value, result)
				if err != nil {
					return err
				}
				b.WriteString(JsResultToString(v))
			case paramGet:
				b.WriteString(r.ctx.GetVar(p.value))
			default:
				if groupRefExact.MatchString(p.value) {
					idx, _ := strconv.Atoi(p.value[1:])
					if list, ok := result.([]any); ok && len(list) > idx && list[idx] != nil {
						b.WriteString(JsResultToString(list[idx]))
					} else if list, ok := result.([]string); ok && len(list) > idx {
						b.WriteString(list[idx])
					} else {
						b.WriteString(p.value)
					}
				} else {
					b.WriteString(p.value)
				}
			}
		}
		r.Rule = b.String()
	}
	parts := strings.Split(r.Rule, "##")
	r.Rule = strings.TrimSpace(parts[0])
	if len(parts) > 1 {
		r.ReplaceRegex = parts[1]
	}
	if len(parts) > 2 {
		r.Replacement = parts[2]
	}
	if len(parts) > 3 {
		r.ReplaceFirst = true
	}
	return nil
}

// SplitSourceRule 把一条完整规则拆成「规则段 / JS 段」序列。
// allInOne 为 true 时（getElement/getElements），规则以 `:` 开头表示整串都是正则。
func SplitSourceRule(ctx Ctx, ruleStr string, allInOne, isJSONContent bool) []*SourceRule {
	if ruleStr == "" {
		return nil
	}
	var out []*SourceRule
	mode := ModeDefault
	start := 0
	if allInOne && strings.HasPrefix(ruleStr, ":") {
		mode = ModeRegex
		start = 1
	}
	if jsPrefixPattern.MatchString(ruleStr) {
		return []*SourceRule{NewSourceRule(ctx, jsPrefixPattern.ReplaceAllString(ruleStr, ""), ModeJS, isJSONContent)}
	}
	for _, loc := range jsTagPattern.FindAllStringSubmatchIndex(ruleStr, -1) {
		if loc[0] > start {
			text := strings.TrimSpace(ruleStr[start:loc[0]])
			if text != "" {
				out = append(out, NewSourceRule(ctx, text, mode, isJSONContent))
			}
		}
		out = append(out, NewSourceRule(ctx, ruleStr[loc[2]:loc[3]], ModeJS, isJSONContent))
		start = loc[1]
	}
	if len(ruleStr) > start {
		text := strings.TrimSpace(ruleStr[start:])
		if text != "" {
			out = append(out, NewSourceRule(ctx, text, mode, isJSONContent))
		}
	}
	return out
}

// IsRuleString 判断规则串是否自带规则标志。
func IsRuleString(ruleStr string) bool {
	return strings.HasPrefix(ruleStr, "@") ||
		strings.HasPrefix(ruleStr, "$.") ||
		strings.HasPrefix(ruleStr, "$[") ||
		strings.HasPrefix(ruleStr, "//")
}

// JsResultToString 把 JS 求值结果转成规则串文本（整数值的浮点去掉小数位）。
func JsResultToString(v any) string {
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
	case float64:
		if t == float64(int64(t)) {
			return strconv.FormatInt(int64(t), 10)
		}
		return strconv.FormatFloat(t, 'f', -1, 64)
	case float32:
		return JsResultToString(float64(t))
	case int:
		return strconv.Itoa(t)
	case int64:
		return strconv.FormatInt(t, 10)
	case []any:
		parts := make([]string, len(t))
		for i, item := range t {
			parts[i] = JsResultToString(item)
		}
		return strings.Join(parts, "\n")
	case []string:
		return strings.Join(t, "\n")
	}
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprint(v)
	}
	return string(b)
}

// SplitBalanced 按顶层分隔符切分，跳过引号与配对括号内部。
func SplitBalanced(text string, delimiters ...string) []string {
	if text == "" {
		return nil
	}
	if len(delimiters) == 0 {
		return []string{text}
	}
	const openers = "([{"
	const closers = ")]}"
	var out []string
	var stack []byte
	var quote byte
	start := 0
	for i := 0; i < len(text); i++ {
		ch := text[i]
		if quote != 0 {
			if ch == '\\' {
				i++
				continue
			}
			if ch == quote {
				quote = 0
			}
			continue
		}
		if ch == '"' || ch == '\'' {
			quote = ch
			continue
		}
		if idx := strings.IndexByte(openers, ch); idx >= 0 {
			stack = append(stack, closers[idx])
			continue
		}
		if strings.IndexByte(closers, ch) >= 0 {
			if len(stack) > 0 && stack[len(stack)-1] == ch {
				stack = stack[:len(stack)-1]
			}
			continue
		}
		if len(stack) == 0 {
			hit := ""
			for _, d := range delimiters {
				if strings.HasPrefix(text[i:], d) {
					hit = d
					break
				}
			}
			if hit != "" {
				out = append(out, text[start:i])
				i += len(hit) - 1
				start = i + 1
			}
		}
	}
	out = append(out, text[start:])
	return out
}

// SplitNotBlank 按分隔符切分并去掉空段与空白。
func SplitNotBlank(text, delim string) []string {
	parts := strings.Split(text, delim)
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

// ReplaceInnerRule 替换 `{{...}}` 这类内嵌规则（允许嵌套花括号）。
func ReplaceInnerRule(text, startStr, endStr string, fn func(inner string) string) string {
	if !strings.Contains(text, startStr) {
		return text
	}
	var b strings.Builder
	i := 0
	for i < len(text) {
		open := strings.Index(text[i:], startStr)
		if open < 0 {
			b.WriteString(text[i:])
			break
		}
		open += i
		b.WriteString(text[i:open])
		depth := 0
		var quote byte
		closed := -1
		for j := open + len(startStr); j < len(text); j++ {
			ch := text[j]
			if quote != 0 {
				if ch == '\\' {
					j++
				} else if ch == quote {
					quote = 0
				}
				continue
			}
			if ch == '"' || ch == '\'' {
				quote = ch
				continue
			}
			if ch == startStr[len(startStr)-1] {
				depth++
				continue
			}
			if strings.HasPrefix(text[j:], endStr) {
				if depth == 0 {
					closed = j
					break
				}
				depth--
			}
		}
		if closed < 0 {
			b.WriteString(text[open : open+len(startStr)])
			i = open + len(startStr)
			continue
		}
		b.WriteString(fn(text[open+len(startStr) : closed]))
		i = closed + len(endStr)
	}
	return b.String()
}

func toString(v any) string {
	if v == nil {
		return ""
	}
	if s, ok := v.(string); ok {
		return s
	}
	return JsResultToString(v)
}

func sortStrings(list []string) {
	for i := 1; i < len(list); i++ {
		for j := i; j > 0 && list[j] < list[j-1]; j-- {
			list[j], list[j-1] = list[j-1], list[j]
		}
	}
}
