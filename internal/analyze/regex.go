package analyze

import (
	"errors"
	"regexp"
	"strings"
)

// Regex 是正则规则解析器（移植 Legado `AnalyzeByRegex`）。
//
// 规则写法：`正则1&&正则2`，`&&` 串起多级正则，上一级的匹配结果作为下一级输入；
// 最后一级的捕获组即结果（组 0 是整段匹配，1..n 为各分组，规则里用 `$1` 引用）。
var Regex = regexAnalyzer{}

type regexAnalyzer struct{}

func compileRegex(pattern string) (*regexp.Regexp, error) {
	if pattern == "" {
		return nil, errors.New("empty pattern")
	}
	flags := ""
	if strings.Contains(pattern, "(?i)") {
		flags = "i"
	}
	re, err := regexp.Compile("(?s" + flags + ")" + pattern)
	if err != nil {
		return nil, err
	}
	return re, nil
}

// GetElement 取第一条匹配的捕获组列表
func (regexAnalyzer) GetElement(res string, regs []string) []string {
	if len(regs) == 0 {
		return nil
	}
	re, err := compileRegex(regs[0])
	if err != nil {
		return nil
	}
	loc := re.FindStringSubmatchIndex(res)
	if loc == nil {
		return nil
	}
	groups := submatchStrings(res, loc)
	if len(regs) == 1 {
		return groups
	}
	var b strings.Builder
	for _, m := range re.FindAllStringIndex(res, -1) {
		if len(m) == 2 {
			b.WriteString(res[m[0]:m[1]])
		}
	}
	return Regex.GetElement(b.String(), regs[1:])
}

// GetElements 取所有匹配的捕获组列表
func (regexAnalyzer) GetElements(res string, regs []string) [][]string {
	if len(regs) == 0 {
		return nil
	}
	re, err := compileRegex(regs[0])
	if err != nil {
		return nil
	}
	all := re.FindAllStringSubmatchIndex(res, -1)
	if len(all) == 0 {
		return nil
	}
	if len(regs) == 1 {
		out := make([][]string, 0, len(all))
		for _, loc := range all {
			out = append(out, submatchStrings(res, loc))
		}
		return out
	}
	var b strings.Builder
	for _, loc := range all {
		b.WriteString(res[loc[0]:loc[1]])
	}
	return Regex.GetElements(b.String(), regs[1:])
}

func submatchStrings(res string, loc []int) []string {
	out := make([]string, 0, len(loc)/2)
	for i := 0; i+1 < len(loc); i += 2 {
		if loc[i] < 0 {
			out = append(out, "")
			continue
		}
		out = append(out, res[loc[i]:loc[i+1]])
	}
	return out
}
