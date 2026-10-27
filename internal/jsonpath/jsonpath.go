// Package jsonpath 实现 Jayway JsonPath 兼容的表达式求值（无第三方依赖）。
//
// 支持：`$`/`@` 根、`.name`、`['name']`、`[n]`、`[-n]`、`[start:end:step]`、`[*]`、
// `..name`、`..*`、联合 `[a,b]`、过滤 `[?(expr)]`（`== != < <= > >= =~ in nin size empty
// subsetof anyof noneof && || !`）、函数 `length() size() min() max() avg() sum() keys()
// first() last() concat()`。
package jsonpath

import (
	"errors"
	"fmt"
	"math"
	"reflect"
	"regexp"
	"strconv"
	"strings"
)

// ErrSyntax 表示表达式语法错误
var ErrSyntax = errors.New("jsonpath: syntax error")

// ---------- AST ----------

type segKind int

const (
	segName segKind = iota
	segWildcard
	segIndex
	segSlice
	segUnion
	segFilter
	segDeep
	segFunc
)

type seg struct {
	kind   segKind
	name   string
	index  int
	start  *int
	end    *int
	step   *int
	items  []seg  // union 成员或 deep 的子选择器
	filter expr   // 过滤表达式
	fname  string // 函数段名
	fargs  []expr // 函数段参数
}

type exprKind int

const (
	exprLit exprKind = iota
	exprPath
	exprRegex
	exprUnary
	exprBinary
	exprFunc
)

type expr struct {
	kind exprKind
	lit  any
	abs  bool  // pathExpr: 以 $ 开头
	segs []seg // pathExpr
	re   *regexp.Regexp
	op   string
	l, r *expr
	name string
	args []expr
}

type parser struct {
	s   string
	pos int
}

// Path 是编译后的 JsonPath 表达式
type Path struct {
	segs []seg
	raw  string
}

// Compile 编译表达式
func Compile(path string) (*Path, error) {
	p := &parser{s: path}
	p.skipSpace()
	if p.peek() != '$' && p.peek() != '@' {
		return nil, fmt.Errorf("%w: 表达式必须以 $ 或 @ 开头: %q", ErrSyntax, path)
	}
	p.pos++
	segs, err := p.parseSegs()
	if err != nil {
		return nil, err
	}
	p.skipSpace()
	if p.pos != len(p.s) {
		return nil, fmt.Errorf("%w: 无法解析的结尾 %q", ErrSyntax, p.s[p.pos:])
	}
	return &Path{segs: segs, raw: path}, nil
}

// String 返回原始表达式
func (p *Path) String() string { return p.raw }

func (p *parser) peek() byte {
	if p.pos < len(p.s) {
		return p.s[p.pos]
	}
	return 0
}

func (p *parser) skipSpace() {
	for p.pos < len(p.s) {
		switch p.s[p.pos] {
		case ' ', '\t', '\n', '\r':
			p.pos++
		default:
			return
		}
	}
}

func (p *parser) parseSegs() ([]seg, error) {
	var segs []seg
	for {
		save := p.pos
		p.skipSpace()
		if !strings.HasPrefix(p.s[p.pos:], "..") && p.peek() != '.' && p.peek() != '[' {
			p.pos = save // 不吞掉尾随空白，交给上层识别关键字运算符
			return segs, nil
		}
		switch {
		case strings.HasPrefix(p.s[p.pos:], ".."):
			p.pos += 2
			child, err := p.parseDeepChild()
			if err != nil {
				return nil, err
			}
			segs = append(segs, seg{kind: segDeep, items: []seg{child}})
		case p.peek() == '.':
			p.pos++
			name, err := p.parseName()
			if err != nil {
				return nil, err
			}
			if p.peek() == '(' {
				// 路径上的函数段：$.store.book.length()
				args, err := p.parseCallArgs()
				if err != nil {
					return nil, err
				}
				segs = append(segs, seg{kind: segFunc, fname: strings.ToLower(name), fargs: args})
				continue
			}
			segs = append(segs, seg{kind: segName, name: name})
		case p.peek() == '[':
			s, err := p.parseBracket()
			if err != nil {
				return nil, err
			}
			segs = append(segs, s)
		}
	}
}

func (p *parser) parseDeepChild() (seg, error) {
	p.skipSpace()
	if p.peek() == '[' {
		return p.parseBracket()
	}
	if p.peek() == '*' {
		p.pos++
		return seg{kind: segWildcard}, nil
	}
	name, err := p.parseName()
	if err != nil {
		return seg{}, err
	}
	return seg{kind: segName, name: name}, nil
}

func (p *parser) parseName() (string, error) {
	start := p.pos
	for p.pos < len(p.s) {
		c := p.s[p.pos]
		if c == '.' || c == '[' || c == ']' || c == ' ' || c == '\t' || c == '(' || c == ')' || c == ',' || c == '=' || c == '!' || c == '<' || c == '>' || c == '&' || c == '|' {
			break
		}
		p.pos++
	}
	if p.pos == start {
		return "", fmt.Errorf("%w: 期望字段名: %q", ErrSyntax, p.s[p.pos:])
	}
	return p.s[start:p.pos], nil
}

func (p *parser) parseBracket() (seg, error) {
	p.pos++ // '['
	p.skipSpace()
	var result seg
	switch {
	case p.peek() == '*':
		p.pos++
		result = seg{kind: segWildcard}
	case strings.HasPrefix(p.s[p.pos:], "?(") || strings.HasPrefix(p.s[p.pos:], "? ("):
		p.pos += 2
		e, err := p.parseExpr()
		if err != nil {
			return seg{}, err
		}
		p.skipSpace()
		if p.peek() != ')' {
			return seg{}, fmt.Errorf("%w: 过滤表达式缺少 )", ErrSyntax)
		}
		p.pos++
		result = seg{kind: segFilter, filter: e}
	default:
		items, err := p.parseUnionItems()
		if err != nil {
			return seg{}, err
		}
		if len(items) == 1 {
			result = items[0]
		} else {
			result = seg{kind: segUnion, items: items}
		}
	}
	p.skipSpace()
	if p.peek() != ']' {
		return seg{}, fmt.Errorf("%w: 缺少 ]: %q", ErrSyntax, p.s[p.pos:])
	}
	p.pos++
	return result, nil
}

func (p *parser) parseUnionItems() ([]seg, error) {
	var items []seg
	for {
		p.skipSpace()
		var item seg
		switch p.peek() {
		case '\'', '"':
			name, err := p.parseQuoted()
			if err != nil {
				return nil, err
			}
			item = seg{kind: segName, name: name}
		case ':':
			s, err := p.parseSlice(nil)
			if err != nil {
				return nil, err
			}
			return []seg{s}, nil
		default:
			// 数字或 slice
			numStart := p.pos
			if p.peek() == '-' || p.peek() == '+' {
				p.pos++
			}
			for p.pos < len(p.s) && p.s[p.pos] >= '0' && p.s[p.pos] <= '9' {
				p.pos++
			}
			p.skipSpace()
			if p.peek() == ':' {
				first := p.s[numStart:p.pos]
				var f *int
				if fv, err := strconv.Atoi(strings.TrimSpace(first)); err == nil {
					f = &fv
				}
				s, err := p.parseSlice(f)
				if err != nil {
					return nil, err
				}
				return []seg{s}, nil
			}
			idx, err := strconv.Atoi(strings.TrimSpace(p.s[numStart:p.pos]))
			if err != nil {
				return nil, fmt.Errorf("%w: 非法下标 %q", ErrSyntax, p.s[numStart:p.pos])
			}
			item = seg{kind: segIndex, index: idx}
		}
		items = append(items, item)
		p.skipSpace()
		if p.peek() == ',' {
			p.pos++
			continue
		}
		return items, nil
	}
}

func (p *parser) parseSlice(first *int) (seg, error) {
	s := seg{kind: segSlice, start: first}
	p.pos++ // ':'
	p.skipSpace()
	if p.peek() == '-' || (p.peek() >= '0' && p.peek() <= '9') {
		start := p.pos
		if p.peek() == '-' {
			p.pos++
		}
		for p.pos < len(p.s) && p.s[p.pos] >= '0' && p.s[p.pos] <= '9' {
			p.pos++
		}
		if v, err := strconv.Atoi(p.s[start:p.pos]); err == nil {
			s.end = &v
		}
	}
	p.skipSpace()
	if p.peek() == ':' {
		p.pos++
		p.skipSpace()
		start := p.pos
		if p.peek() == '-' {
			p.pos++
		}
		for p.pos < len(p.s) && p.s[p.pos] >= '0' && p.s[p.pos] <= '9' {
			p.pos++
		}
		if v, err := strconv.Atoi(p.s[start:p.pos]); err == nil {
			s.step = &v
		}
	}
	return s, nil
}

func (p *parser) parseQuoted() (string, error) {
	quote := p.s[p.pos]
	p.pos++
	var b strings.Builder
	for p.pos < len(p.s) {
		c := p.s[p.pos]
		if c == '\\' && p.pos+1 < len(p.s) {
			p.pos++
			b.WriteByte(p.s[p.pos])
			p.pos++
			continue
		}
		if c == quote {
			p.pos++
			return b.String(), nil
		}
		b.WriteByte(c)
		p.pos++
	}
	return "", fmt.Errorf("%w: 字符串未闭合", ErrSyntax)
}

// ---------- 表达式 ----------

func (p *parser) parseExpr() (expr, error) { return p.parseOr() }

func (p *parser) parseOr() (expr, error) {
	left, err := p.parseAnd()
	if err != nil {
		return expr{}, err
	}
	for {
		p.skipSpace()
		if strings.HasPrefix(p.s[p.pos:], "||") {
			p.pos += 2
			right, err := p.parseAnd()
			if err != nil {
				return expr{}, err
			}
			l, r := left, right
			left = expr{kind: exprBinary, op: "||", l: &l, r: &r}
			continue
		}
		return left, nil
	}
}

func (p *parser) parseAnd() (expr, error) {
	left, err := p.parseUnary()
	if err != nil {
		return expr{}, err
	}
	for {
		p.skipSpace()
		if strings.HasPrefix(p.s[p.pos:], "&&") {
			p.pos += 2
			right, err := p.parseUnary()
			if err != nil {
				return expr{}, err
			}
			l, r := left, right
			left = expr{kind: exprBinary, op: "&&", l: &l, r: &r}
			continue
		}
		return left, nil
	}
}

// 带右操作数的符号运算符（长的在前）
var symbolOps = []string{"==", "!=", "<=", ">=", "=~", "<", ">"}

// 带右操作数的关键字运算符
var wordBinOps = []string{"subsetof", "noneof", "anyof", "nin", "in"}

// 不带右操作数的关键字运算符
var wordUnaryOps = []string{"size", "empty"}

// matchWordOp 匹配关键字运算符（可选前置空白 + 必须后接空白或表达式结尾）。命中返回消耗长度。
func (p *parser) matchWordOp(op string) int {
	rest := p.s[p.pos:]
	rest = strings.TrimLeft(rest, " \t")
	n := len(p.s[p.pos:]) - len(rest)
	if !strings.HasPrefix(rest, op) {
		return 0
	}
	after := rest[len(op):]
	if after == "" || after[0] == ' ' || after[0] == '\t' {
		return n + len(op)
	}
	return 0
}

func (p *parser) parseUnary() (expr, error) {
	p.skipSpace()
	if p.peek() == '!' && !strings.HasPrefix(p.s[p.pos:], "!=") {
		p.pos++
		x, err := p.parseUnary()
		if err != nil {
			return expr{}, err
		}
		return expr{kind: exprUnary, op: "!", l: &x}, nil
	}
	left, err := p.parsePrimary()
	if err != nil {
		return expr{}, err
	}
	p.skipSpace()
	for _, op := range wordUnaryOps {
		if n := p.matchWordOp(op); n > 0 {
			p.pos += n
			return expr{kind: exprBinary, op: op, l: &left}, nil
		}
	}
	for _, op := range symbolOps {
		if !strings.HasPrefix(p.s[p.pos:], op) {
			continue
		}
		p.pos += len(op)
		if op == "=~" {
			p.skipSpace()
			if p.peek() != '/' {
				return expr{}, fmt.Errorf("%w: =~ 右侧必须是 /regex/", ErrSyntax)
			}
			re, err := p.parseRegex()
			if err != nil {
				return expr{}, err
			}
			r := expr{kind: exprRegex, re: re}
			return expr{kind: exprBinary, op: op, l: &left, r: &r}, nil
		}
		right, err := p.parsePrimary()
		if err != nil {
			return expr{}, err
		}
		l, r := left, right
		return expr{kind: exprBinary, op: op, l: &l, r: &r}, nil
	}
	for _, op := range wordBinOps {
		n := p.matchWordOp(op)
		if n == 0 {
			continue
		}
		p.pos += n
		right, err := p.parsePrimary()
		if err != nil {
			return expr{}, err
		}
		l, r := left, right
		return expr{kind: exprBinary, op: op, l: &l, r: &r}, nil
	}
	return left, nil
}

func (p *parser) parseRegex() (*regexp.Regexp, error) {
	p.pos++ // '/'
	start := p.pos
	for p.pos < len(p.s) {
		if p.s[p.pos] == '\\' {
			p.pos += 2
			continue
		}
		if p.s[p.pos] == '/' {
			break
		}
		p.pos++
	}
	if p.pos >= len(p.s) {
		return nil, fmt.Errorf("%w: 正则未闭合", ErrSyntax)
	}
	pattern := p.s[start:p.pos]
	p.pos++ // '/'
	flags := ""
	for p.pos < len(p.s) {
		c := p.s[p.pos]
		if c == 'i' || c == 'm' || c == 's' || c == 'U' {
			flags += string(c)
			p.pos++
			continue
		}
		break
	}
	if strings.Contains(flags, "i") {
		pattern = "(?i)" + pattern
	}
	if strings.Contains(flags, "s") {
		pattern = "(?s)" + pattern
	}
	if strings.Contains(flags, "m") {
		pattern = "(?m)" + pattern
	}
	return regexp.Compile(pattern)
}

func (p *parser) parsePrimary() (expr, error) {
	p.skipSpace()
	c := p.peek()
	switch {
	case c == '$' || c == '@':
		p.pos++
		segs, err := p.parseSegs()
		if err != nil {
			return expr{}, err
		}
		return expr{kind: exprPath, abs: c == '$', segs: segs}, nil
	case c == '(':
		p.pos++
		e, err := p.parseExpr()
		if err != nil {
			return expr{}, err
		}
		p.skipSpace()
		if p.peek() != ')' {
			return expr{}, fmt.Errorf("%w: 缺少 )", ErrSyntax)
		}
		p.pos++
		return e, nil
	case c == '\'' || c == '"':
		s, err := p.parseQuoted()
		if err != nil {
			return expr{}, err
		}
		return expr{kind: exprLit, lit: s}, nil
	case c == '[':
		// 数组字面量：['a','b'] 或 [1,2]
		p.pos++
		var items []any
		for {
			p.skipSpace()
			if p.peek() == ']' {
				p.pos++
				break
			}
			if p.peek() == '\'' || p.peek() == '"' {
				s, err := p.parseQuoted()
				if err != nil {
					return expr{}, err
				}
				items = append(items, s)
			} else {
				num, err := p.parseNumber()
				if err != nil {
					return expr{}, err
				}
				items = append(items, num)
			}
			p.skipSpace()
			if p.peek() == ',' {
				p.pos++
				continue
			}
			if p.peek() == ']' {
				p.pos++
				break
			}
			return expr{}, fmt.Errorf("%w: 数组字面量格式错误", ErrSyntax)
		}
		return expr{kind: exprLit, lit: items}, nil
	case c == '-' || (c >= '0' && c <= '9') || c == '+':
		v, err := p.parseNumber()
		if err != nil {
			return expr{}, err
		}
		return expr{kind: exprLit, lit: v}, nil
	case c == '/':
		re, err := p.parseRegex()
		if err != nil {
			return expr{}, err
		}
		return expr{kind: exprRegex, re: re}, nil
	default:
		// true / false / null / 函数
		name := p.scanIdent()
		if name == "" {
			return expr{}, fmt.Errorf("%w: 无法解析的表达式 %q", ErrSyntax, p.s[p.pos:])
		}
		switch strings.ToLower(name) {
		case "true":
			return expr{kind: exprLit, lit: true}, nil
		case "false":
			return expr{kind: exprLit, lit: false}, nil
		case "null":
			return expr{kind: exprLit, lit: nil}, nil
		}
		p.skipSpace()
		if p.peek() != '(' {
			return expr{}, fmt.Errorf("%w: 未知标识符 %q", ErrSyntax, name)
		}
		args, err := p.parseCallArgs()
		if err != nil {
			return expr{}, err
		}
		return expr{kind: exprFunc, name: strings.ToLower(name), args: args}, nil
	}
}

// parseCallArgs 解析 "(a, b)" 形式的实参列表（当前指向 '('）。
func (p *parser) parseCallArgs() ([]expr, error) {
	p.pos++ // '('
	var args []expr
	for {
		p.skipSpace()
		if p.peek() == ')' {
			p.pos++
			return args, nil
		}
		arg, err := p.parseExpr()
		if err != nil {
			return nil, err
		}
		args = append(args, arg)
		p.skipSpace()
		if p.peek() == ',' {
			p.pos++
			continue
		}
		if p.peek() == ')' {
			p.pos++
			return args, nil
		}
		return nil, fmt.Errorf("%w: 函数参数格式错误", ErrSyntax)
	}
}

func (p *parser) scanIdent() string {
	start := p.pos
	for p.pos < len(p.s) {
		c := p.s[p.pos]
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '_' {
			p.pos++
			continue
		}
		break
	}
	return p.s[start:p.pos]
}

func (p *parser) parseNumber() (any, error) {
	start := p.pos
	if p.peek() == '-' || p.peek() == '+' {
		p.pos++
	}
	for p.pos < len(p.s) {
		c := p.s[p.pos]
		if (c >= '0' && c <= '9') || c == '.' || c == 'e' || c == 'E' || c == '-' || c == '+' {
			p.pos++
			continue
		}
		break
	}
	return parseNumberLiteral(p.s[start:p.pos])
}

func parseNumberLiteral(s string) (any, error) {
	if strings.ContainsAny(s, ".eE") {
		f, err := strconv.ParseFloat(s, 64)
		if err != nil {
			return nil, fmt.Errorf("%w: 非法数字 %q", ErrSyntax, s)
		}
		return f, nil
	}
	i, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return nil, fmt.Errorf("%w: 非法数字 %q", ErrSyntax, s)
	}
	return float64(i), nil
}

// ---------- 求值 ----------

// Eval 返回所有匹配值。
func Eval(data any, path string) ([]any, error) {
	p, err := Compile(path)
	if err != nil {
		return nil, err
	}
	return p.Eval(data)
}

// Eval 在给定数据上求值。
func (p *Path) Eval(data any) ([]any, error) {
	return applySegs(data, []any{data}, p.segs)
}

// EvalOne 返回第一个匹配值。
func EvalOne(data any, path string) (any, bool, error) {
	vals, err := Eval(data, path)
	if err != nil || len(vals) == 0 {
		return nil, false, err
	}
	return vals[0], true, nil
}

// IsPath 判断一段文本是否像 JsonPath 表达式。
func IsPath(s string) bool {
	s = strings.TrimSpace(s)
	if s == "" {
		return false
	}
	if s[0] != '$' && s[0] != '@' {
		return false
	}
	if len(s) == 1 {
		return true
	}
	switch s[1] {
	case '.', '[':
		return true
	}
	return false
}

func applySegs(root any, in []any, segs []seg) ([]any, error) {
	cur := in
	for _, s := range segs {
		out := make([]any, 0, len(cur))
		for _, node := range cur {
			vals, err := s.apply(root, node)
			if err != nil {
				return nil, err
			}
			out = append(out, vals...)
		}
		cur = out
	}
	return cur, nil
}

func (s seg) apply(root, node any) ([]any, error) {
	switch s.kind {
	case segName:
		if m, ok := node.(map[string]any); ok {
			if v, ok := m[s.name]; ok {
				return []any{v}, nil
			}
		}
		return nil, nil
	case segWildcard:
		return children(node), nil
	case segIndex:
		arr, ok := toSlice(node)
		if !ok {
			return nil, nil
		}
		idx := s.index
		if idx < 0 {
			idx += len(arr)
		}
		if idx < 0 || idx >= len(arr) {
			return nil, nil
		}
		return []any{arr[idx]}, nil
	case segSlice:
		arr, ok := toSlice(node)
		if !ok {
			return nil, nil
		}
		n := len(arr)
		step := 1
		if s.step != nil {
			step = *s.step
		}
		if step == 0 {
			return nil, nil
		}
		start, end := 0, n
		if step < 0 {
			start, end = n-1, -1
		}
		if s.start != nil {
			start = normalizeIndex(*s.start, n, step < 0)
		}
		if s.end != nil {
			end = normalizeIndex(*s.end, n, step < 0)
		}
		var out []any
		if step > 0 {
			for i := clamp(start, 0, n); i < clamp(end, 0, n); i += step {
				out = append(out, arr[i])
			}
		} else {
			for i := clamp(start, -1, n-1); i > clamp(end, -1, n-1); i += step {
				out = append(out, arr[i])
			}
		}
		return out, nil
	case segUnion:
		var out []any
		for _, item := range s.items {
			vals, err := item.apply(root, node)
			if err != nil {
				return nil, err
			}
			out = append(out, vals...)
		}
		return out, nil
	case segFilter:
		var out []any
		candidates, isArr := toSlice(node)
		if !isArr {
			candidates = []any{node}
		}
		for _, cand := range candidates {
			ok, err := evalBool(root, cand, s.filter)
			if err != nil {
				return nil, err
			}
			if ok {
				out = append(out, cand)
			}
		}
		return out, nil
	case segFunc:
		v, err := evalFunc(root, node, expr{kind: exprFunc, name: s.fname, args: s.fargs})
		if err != nil {
			return nil, err
		}
		if arr, ok := toSlice(v); ok {
			return arr, nil
		}
		return []any{v}, nil
	case segDeep:
		var out []any
		for _, child := range s.items {
			walk(node, func(n any) {
				vals, err := child.apply(root, n)
				if err == nil {
					out = append(out, vals...)
				}
			})
		}
		return out, nil
	}
	return nil, nil
}

func normalizeIndex(i, n int, fromEnd bool) int {
	if i < 0 {
		return n + i
	}
	return i
}

func clamp(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func toSlice(node any) ([]any, bool) {
	switch v := node.(type) {
	case []any:
		return v, true
	case []string:
		out := make([]any, len(v))
		for i, s := range v {
			out[i] = s
		}
		return out, true
	}
	rv := reflect.ValueOf(node)
	if rv.IsValid() && (rv.Kind() == reflect.Slice || rv.Kind() == reflect.Array) {
		out := make([]any, rv.Len())
		for i := 0; i < rv.Len(); i++ {
			out[i] = rv.Index(i).Interface()
		}
		return out, true
	}
	return nil, false
}

func children(node any) []any {
	if arr, ok := toSlice(node); ok {
		return arr
	}
	if m, ok := node.(map[string]any); ok {
		out := make([]any, 0, len(m))
		for _, k := range sortedKeys(m) {
			out = append(out, m[k])
		}
		return out
	}
	return nil
}

func sortedKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	// 保持稳定输出
	for i := 1; i < len(keys); i++ {
		for j := i; j > 0 && keys[j] < keys[j-1]; j-- {
			keys[j], keys[j-1] = keys[j-1], keys[j]
		}
	}
	return keys
}

func walk(node any, fn func(any)) {
	fn(node)
	if m, ok := node.(map[string]any); ok {
		for _, v := range m {
			walk(v, fn)
		}
		return
	}
	if arr, ok := toSlice(node); ok {
		for _, v := range arr {
			walk(v, fn)
		}
	}
}

func evalBool(root, cur any, e expr) (bool, error) {
	v, err := evalExpr(root, cur, e)
	if err != nil {
		return false, err
	}
	return truthy(v), nil
}

func evalExpr(root, cur any, e expr) (any, error) {
	switch e.kind {
	case exprLit:
		return e.lit, nil
	case exprRegex:
		return e.re, nil
	case exprPath:
		start := cur
		if e.abs {
			start = root
		}
		vals, err := applySegs(root, []any{start}, e.segs)
		if err != nil {
			return nil, err
		}
		if len(vals) == 1 {
			return vals[0], nil
		}
		if len(vals) == 0 {
			return nil, nil
		}
		return vals, nil
	case exprUnary:
		if e.op == "!" {
			v, err := evalExpr(root, cur, *e.l)
			if err != nil {
				return nil, err
			}
			return !truthy(v), nil
		}
		return nil, fmt.Errorf("%w: 未知一元运算符 %q", ErrSyntax, e.op)
	case exprBinary:
		return evalBinary(root, cur, e)
	case exprFunc:
		return evalFunc(root, cur, e)
	}
	return nil, nil
}

func evalBinary(root, cur any, e expr) (any, error) {
	switch e.op {
	case "&&", "||":
		l, err := evalBool(root, cur, *e.l)
		if err != nil {
			return nil, err
		}
		if e.op == "&&" && !l {
			return false, nil
		}
		if e.op == "||" && l {
			return true, nil
		}
		return evalBool(root, cur, *e.r)
	case "size":
		l, err := evalExpr(root, cur, *e.l)
		if err != nil {
			return nil, err
		}
		return float64(sizeOf(l)), nil
	case "empty":
		l, err := evalExpr(root, cur, *e.l)
		if err != nil {
			return nil, err
		}
		return sizeOf(l) == 0, nil
	}

	lv, err := evalExpr(root, cur, *e.l)
	if err != nil {
		return nil, err
	}
	if e.r == nil {
		return nil, fmt.Errorf("%w: 运算符 %q 缺少右操作数", ErrSyntax, e.op)
	}
	rv, err := evalExpr(root, cur, *e.r)
	if err != nil {
		return nil, err
	}

	switch e.op {
	case "==":
		return anyMatch(lv, func(v any) bool { return looseEqual(v, rv) }), nil
	case "!=":
		return !anyMatch(lv, func(v any) bool { return looseEqual(v, rv) }), nil
	case "=~":
		re, ok := rv.(*regexp.Regexp)
		if !ok {
			return false, nil
		}
		return anyMatch(lv, func(v any) bool {
			s, ok := asString(v)
			return ok && re.MatchString(s)
		}), nil
	case "<", "<=", ">", ">=":
		return anyMatch(lv, func(v any) bool { return compareOp(e.op, v, rv) }), nil
	case "in", "nin":
		list, ok := toSlice(rv)
		if !ok {
			return false, fmt.Errorf("%w: %s 右侧必须是数组", ErrSyntax, e.op)
		}
		found := anyMatch(lv, func(v any) bool {
			for _, item := range list {
				if looseEqual(v, item) {
					return true
				}
			}
			return false
		})
		if e.op == "nin" {
			return !found, nil
		}
		return found, nil
	case "subsetof":
		list, ok := toSlice(rv)
		if !ok {
			return false, nil
		}
		left, ok := toSlice(lv)
		if !ok {
			left = []any{lv}
		}
		for _, v := range left {
			found := false
			for _, item := range list {
				if looseEqual(v, item) {
					found = true
					break
				}
			}
			if !found {
				return false, nil
			}
		}
		return true, nil
	case "anyof":
		list, ok := toSlice(rv)
		if !ok {
			return false, nil
		}
		return anyMatch(lv, func(v any) bool {
			for _, item := range list {
				if looseEqual(v, item) {
					return true
				}
			}
			return false
		}), nil
	case "noneof":
		list, ok := toSlice(rv)
		if !ok {
			return false, nil
		}
		return !anyMatch(lv, func(v any) bool {
			for _, item := range list {
				if looseEqual(v, item) {
					return true
				}
			}
			return false
		}), nil
	}
	return nil, fmt.Errorf("%w: 未知运算符 %q", ErrSyntax, e.op)
}

func anyMatch(lv any, pred func(any) bool) bool {
	if arr, ok := toSlice(lv); ok {
		for _, v := range arr {
			if pred(v) {
				return true
			}
		}
		return false
	}
	return pred(lv)
}

func evalFunc(root, cur any, e expr) (any, error) {
	args := make([]any, 0, len(e.args))
	for _, a := range e.args {
		v, err := evalExpr(root, cur, a)
		if err != nil {
			return nil, err
		}
		args = append(args, v)
	}
	target := cur
	var items []any
	if len(args) > 0 {
		target = args[0]
	}
	if arr, ok := toSlice(target); ok {
		items = arr
	} else if target != nil {
		items = []any{target}
	}
	switch e.name {
	case "length", "size":
		return float64(sizeOf(target)), nil
	case "min", "max", "avg", "sum":
		nums := make([]float64, 0, len(items))
		for _, it := range items {
			if f, ok := toFloat(it); ok {
				nums = append(nums, f)
			}
		}
		if len(nums) == 0 {
			if e.name == "sum" {
				return 0.0, nil
			}
			return nil, nil
		}
		switch e.name {
		case "min":
			m := nums[0]
			for _, v := range nums[1:] {
				m = math.Min(m, v)
			}
			return m, nil
		case "max":
			m := nums[0]
			for _, v := range nums[1:] {
				m = math.Max(m, v)
			}
			return m, nil
		case "avg":
			var s float64
			for _, v := range nums {
				s += v
			}
			return s / float64(len(nums)), nil
		default:
			var s float64
			for _, v := range nums {
				s += v
			}
			return s, nil
		}
	case "keys":
		if m, ok := target.(map[string]any); ok {
			keys := sortedKeys(m)
			out := make([]any, len(keys))
			for i, k := range keys {
				out[i] = k
			}
			return out, nil
		}
		return []any{}, nil
	case "first":
		if len(items) == 0 {
			return nil, nil
		}
		return items[0], nil
	case "last":
		if len(items) == 0 {
			return nil, nil
		}
		return items[len(items)-1], nil
	case "concat":
		var b strings.Builder
		for _, a := range args {
			if s, ok := asString(a); ok {
				b.WriteString(s)
			}
		}
		return b.String(), nil
	}
	return nil, fmt.Errorf("%w: 未知函数 %q", ErrSyntax, e.name)
}

func sizeOf(v any) int {
	if v == nil {
		return 0
	}
	if arr, ok := toSlice(v); ok {
		return len(arr)
	}
	if m, ok := v.(map[string]any); ok {
		return len(m)
	}
	if s, ok := v.(string); ok {
		return len([]rune(s))
	}
	return 1
}

func truthy(v any) bool {
	switch t := v.(type) {
	case nil:
		return false
	case bool:
		return t
	case string:
		return t != ""
	}
	if f, ok := toFloat(v); ok {
		return f != 0
	}
	if arr, ok := toSlice(v); ok {
		return len(arr) > 0
	}
	if m, ok := v.(map[string]any); ok {
		return len(m) > 0
	}
	return true
}

func toFloat(v any) (float64, bool) {
	switch t := v.(type) {
	case float64:
		return t, true
	case float32:
		return float64(t), true
	case int:
		return float64(t), true
	case int64:
		return float64(t), true
	case int32:
		return float64(t), true
	case string:
		f, err := strconv.ParseFloat(strings.TrimSpace(t), 64)
		if err != nil {
			return 0, false
		}
		return f, true
	case bool:
		if t {
			return 1, true
		}
		return 0, true
	}
	return 0, false
}

func asString(v any) (string, bool) {
	switch t := v.(type) {
	case string:
		return t, true
	case nil:
		return "", false
	case bool:
		return strconv.FormatBool(t), true
	}
	if f, ok := toFloat(v); ok {
		if f == math.Trunc(f) && math.Abs(f) < 1e15 {
			return strconv.FormatInt(int64(f), 10), true
		}
		return strconv.FormatFloat(f, 'f', -1, 64), true
	}
	return "", false
}

func looseEqual(a, b any) bool {
	if af, ok := toFloat(a); ok {
		if bf, ok2 := toFloat(b); ok2 {
			return af == bf
		}
	}
	as, aok := asString(a)
	bs, bok := asString(b)
	if aok && bok && !isNumeric(a) && !isNumeric(b) {
		return as == bs
	}
	return reflect.DeepEqual(a, b)
}

func isNumeric(v any) bool {
	switch v.(type) {
	case float64, float32, int, int64, int32:
		return true
	}
	return false
}

func compareOp(op string, a, b any) bool {
	if af, ok := toFloat(a); ok {
		if bf, ok2 := toFloat(b); ok2 {
			switch op {
			case "<":
				return af < bf
			case "<=":
				return af <= bf
			case ">":
				return af > bf
			case ">=":
				return af >= bf
			}
		}
	}
	as, aok := asString(a)
	bs, bok := asString(b)
	if !aok || !bok {
		return false
	}
	switch op {
	case "<":
		return as < bs
	case "<=":
		return as <= bs
	case ">":
		return as > bs
	case ">=":
		return as >= bs
	}
	return false
}
