package rules_test

import (
	"fmt"
	"testing"

	"legado-go/internal/rules"
)

// fakeCtx 是 rules.Ctx 的最小实现：把 JS 当作「拼接 result」处理。
type fakeCtx struct{ vars map[string]string }

func (c *fakeCtx) EvalJS(js string, result any) (any, error) {
	if js == "boom" {
		return nil, fmt.Errorf("js error")
	}
	return fmt.Sprintf("%v[%s]", result, js), nil
}

func (c *fakeCtx) GetVar(key string) string { return c.vars[key] }

func newCtx() *fakeCtx { return &fakeCtx{vars: map[string]string{"v": "VAR"}} }

func TestSplitBalanced(t *testing.T) {
	// 按任意分隔符切分，分隔符本身被丢弃（调用方按索引奇偶还原含义）
	got := rules.SplitBalanced("a&&b||c&&d", "&&", "||")
	want := []string{"a", "b", "c", "d"}
	if len(got) != len(want) {
		t.Fatalf("want %v, got %v", want, got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("want %v, got %v", want, got)
		}
	}
}

func TestSplitBalancedKeepsQuotedAndBraced(t *testing.T) {
	in := `a&&{"k":"x&&y"}&&b`
	got := rules.SplitBalanced(in, "&&")
	if len(got) != 3 || got[2] != "b" {
		t.Fatalf("brackets should protect delimiters: %v", got)
	}
}

func TestSourceRulePrefixes(t *testing.T) {
	cases := []struct {
		rule string
		want rules.Mode
		body string
	}{
		{`@XPath://div/@href`, rules.ModeXPath, "//div/@href"},
		{`@Json:$.a.b`, rules.ModeJSON, "$.a.b"},
		{`@css:.a b`, rules.ModeDefault, "@css:.a b"}, // 前缀由 jsoup 自己剥
		{`a##b`, rules.ModeDefault, "a##b"},           // `##` 在 MakeUpRule 阶段才切
	}
	for _, tc := range cases {
		r := rules.NewSourceRule(newCtx(), tc.rule, rules.ModeDefault, false)
		if r.Mode != tc.want {
			t.Fatalf("%q: want mode %v, got %v", tc.rule, tc.want, r.Mode)
		}
		if r.Rule != tc.body {
			t.Fatalf("%q: want rule %q, got %q", tc.rule, tc.body, r.Rule)
		}
	}
}

func TestMakeUpRuleSplitsReplace(t *testing.T) {
	r := rules.NewSourceRule(newCtx(), "div#c@text##广告## ", rules.ModeDefault, false)
	if err := r.MakeUpRule(nil); err != nil {
		t.Fatalf("make up: %v", err)
	}
	if r.Rule != "div#c@text" || r.ReplaceRegex != "广告" || r.Replacement != " " {
		t.Fatalf("unexpected rule: %+v", r)
	}

	js := rules.NewSourceRule(newCtx(), `result + "!"`, rules.ModeJS, false)
	if js.Mode != rules.ModeJS {
		t.Fatalf("want js mode, got %v", js.Mode)
	}
}

func TestMakeUpRuleExpandsParams(t *testing.T) {
	// `{{page}}` 走 JS 求值，`@get:{v}` 走变量
	r := rules.NewSourceRule(newCtx(), "p{{page}}-@get:{v}", rules.ModeDefault, false)
	if !r.HasParams() {
		t.Fatal("expected params")
	}
	if err := r.MakeUpRule(nil); err != nil {
		t.Fatalf("make up: %v", err)
	}
	if r.Rule != "p<nil>[page]-VAR" {
		t.Fatalf("unexpected expanded rule: %q", r.Rule)
	}

	// `@put:{}` 抽出但不参与展开
	put := rules.NewSourceRule(newCtx(), `@put:{"k":"v"}div`, rules.ModeDefault, false)
	if len(put.PutMap) != 1 || put.PutMap[0].Key != "k" || put.PutMap[0].Value != "v" {
		t.Fatalf("unexpected put map: %+v", put.PutMap)
	}
	if put.Rule != "div" {
		t.Fatalf("put should be stripped from rule, got %q", put.Rule)
	}
}

func TestSplitSourceRule(t *testing.T) {
	// `||` / `&&` 的拆解在 analyze 层做，这里只切 `<js>` 与 `@js:` 段
	if rs := rules.SplitSourceRule(newCtx(), "a||b", false, false); len(rs) != 1 || rs[0].Rule != "a||b" {
		t.Fatalf("unexpected: %+v", rs)
	}

	rs := rules.SplitSourceRule(newCtx(), `a@text<js>result+"x"</js>b`, false, false)
	if len(rs) != 3 || rs[0].Rule != "a@text" || rs[1].Mode != rules.ModeJS || rs[2].Rule != "b" {
		t.Fatalf("unexpected split: %+v", rs)
	}

	pure := rules.SplitSourceRule(newCtx(), `@js:result`, false, false)
	if len(pure) != 1 || pure[0].Mode != rules.ModeJS {
		t.Fatalf("unexpected pure js: %+v", pure)
	}
	if rules.SplitSourceRule(newCtx(), "", false, false) != nil {
		t.Fatal("empty rule should yield nil")
	}
}

func TestJsResultToString(t *testing.T) {
	cases := []struct {
		in   any
		want string
	}{
		{nil, ""},
		{"x", "x"},
		{12, "12"},
		{12.5, "12.5"},
		{[]any{"a", "b"}, "a\nb"},
		{[]string{"a", "", "b"}, "a\n\nb"}, // 空元素保留
		{map[string]any{"a": 1}, `{"a":1}`},
	}
	for _, tc := range cases {
		if got := rules.JsResultToString(tc.in); got != tc.want {
			t.Fatalf("%#v: want %q, got %q", tc.in, tc.want, got)
		}
	}
}

func TestIsRuleString(t *testing.T) {
	for _, ok := range []string{"@js:1", "$.a", "$[0]", "//div"} {
		if !rules.IsRuleString(ok) {
			t.Fatalf("%q should be a rule string", ok)
		}
	}
	if rules.IsRuleString(`a@href&&b`) {
		t.Fatal("plain selector is not a rule string")
	}
	if rules.IsRuleString("http://example.com/path") {
		t.Fatal("url is not a rule string")
	}
	if rules.IsRuleString("") {
		t.Fatal("empty is not a rule string")
	}
}

func TestReplaceInnerRule(t *testing.T) {
	out := rules.ReplaceInnerRule("x{{a}}y{{b}}z", "{{", "}}", func(inner string) string {
		return "<" + inner + ">"
	})
	if out != "x<a>y<b>z" {
		t.Fatalf("unexpected: %q", out)
	}
}
