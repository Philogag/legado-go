package booksource

import (
	"regexp"
	"strings"

	"legado-go/internal/analyze"
	"legado-go/internal/model"
	"legado-go/internal/netx"
	"legado-go/internal/rules"
)

// Bindings 是一次规则执行/抓取的上下文变量
type Bindings struct {
	BaseURL        string
	Page           int
	Key            string
	Title          string
	Src            string
	NextChapterURL string
	Result         any
	Book           *model.Book
	Chapter        *model.Chapter
}

// Engine 是取书链路依赖的最小书源能力
type Engine interface {
	MakeRule(content any, baseURL string, b Bindings) *analyze.Rule
	Fetch(urlRule string, b Bindings) (*netx.Response, error)
	EvalJS(js string, b Bindings) (any, error)
}

// ListRule 是搜索页/发现页规则的统一形态
type ListRule struct {
	BookList     string
	Name         string
	Author       string
	Intro        string
	Kind         string
	LastChapter  string
	UpdateTime   string
	BookURL      string
	CoverURL     string
	WordCount    string
	CheckKeyWord string
}

// ListOptions 是解析列表时的附加参数
type ListOptions struct {
	IsSearch     bool
	Key          string
	Page         int
	CheckKeyWord string
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

// ParseBookList 解析搜索结果 / 发现列表
func ParseBookList(engine Engine, content, baseURL string, rule ListRule, opts ListOptions) ([]model.Book, error) {
	page := opts.Page
	if page <= 0 {
		page = 1
	}
	root := engine.MakeRule(content, baseURL, Bindings{Page: page, Key: opts.Key})
	elements, err := root.GetElements(rule.BookList)
	if err != nil {
		return nil, err
	}

	var books []model.Book
	seen := map[string]bool{}
	for _, item := range elements {
		ir := engine.MakeRule(item, baseURL, Bindings{Page: page, Key: opts.Key})
		name, err := ir.GetString(rule.Name)
		if err != nil {
			return nil, err
		}
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		bookURL, err := ir.GetStringIsURL(rule.BookURL)
		if err != nil {
			return nil, err
		}
		if strings.TrimSpace(bookURL) == "" {
			continue
		}
		if seen[bookURL] {
			continue
		}
		author, err := ir.GetString(rule.Author)
		if err != nil {
			return nil, err
		}
		author = strings.TrimSpace(author)

		if opts.IsSearch && opts.Key != "" && opts.CheckKeyWord != "" {
			check, err := ir.GetString(opts.CheckKeyWord)
			if err != nil {
				return nil, err
			}
			if !CheckKeyWord(check, opts.Key, name, author) {
				continue
			}
		}

		kinds, err := ir.GetStringList(rule.Kind)
		if err != nil {
			return nil, err
		}
		intro, err := ir.GetString(rule.Intro)
		if err != nil {
			return nil, err
		}
		cover, err := ir.GetStringIsURL(rule.CoverURL)
		if err != nil {
			return nil, err
		}
		lastChapter, err := ir.GetString(rule.LastChapter)
		if err != nil {
			return nil, err
		}
		updateTime, err := ir.GetString(rule.UpdateTime)
		if err != nil {
			return nil, err
		}
		wordCount, err := ir.GetString(rule.WordCount)
		if err != nil {
			return nil, err
		}

		seen[bookURL] = true
		books = append(books, model.Book{
			Name:        name,
			Author:      author,
			Kind:        strings.Join(kinds, ","),
			Intro:       intro,
			CoverURL:    cover,
			BookURL:     bookURL,
			LastChapter: lastChapter,
			UpdateTime:  updateTime,
			WordCount:   wordCount,
		})
	}
	return books, nil
}

// CheckKeyWord 关键词校验：规则结果为空表示按书名/作者判断；结果含关键词视为通过
func CheckKeyWord(result, key, name, author string) bool {
	text := strings.TrimSpace(result)
	if text == "" {
		return strings.Contains(name, key) || strings.Contains(author, key)
	}
	switch strings.ToLower(text) {
	case "true", "1":
		return true
	case "false", "0":
		return false
	}
	return strings.Contains(text, key)
}

// ParseBookInfo 解析详情页
func ParseBookInfo(engine Engine, book *model.Book, body, baseURL string, rule model.BookInfoRules, fallbackName string) (*model.Book, error) {
	html := body
	if rule.Init != "" {
		out, err := engine.EvalJS(rule.Init, Bindings{Book: book, Result: html, BaseURL: baseURL})
		if err != nil {
			return nil, err
		}
		if s := strings.TrimSpace(rules.JsResultToString(out)); s != "" {
			html = s
		}
	}
	if book.BookURL == "" {
		book.BookURL = baseURL
	}
	r := engine.MakeRule(html, baseURL, Bindings{Book: book})

	get := func(ruleStr string, isURL bool) (string, error) {
		if isURL {
			return r.GetStringIsURL(ruleStr)
		}
		return r.GetString(ruleStr)
	}

	var err error
	if v, e := get(rule.Name, false); e == nil {
		book.Name = firstNonEmpty(v, fallbackName, book.Name)
	} else {
		err = e
	}
	if v, e := get(rule.Author, false); e == nil {
		book.Author = firstNonEmpty(v, book.Author)
	} else if err == nil {
		err = e
	}
	if v, e := get(rule.Intro, false); e == nil {
		book.Intro = firstNonEmpty(v, book.Intro)
	} else if err == nil {
		err = e
	}
	if v, e := get(rule.Kind, false); e == nil {
		book.Kind = firstNonEmpty(v, book.Kind)
	} else if err == nil {
		err = e
	}
	if v, e := get(rule.LastChapter, false); e == nil {
		book.LastChapter = firstNonEmpty(v, book.LastChapter)
	} else if err == nil {
		err = e
	}
	if v, e := get(rule.UpdateTime, false); e == nil {
		book.UpdateTime = firstNonEmpty(v, book.UpdateTime)
	} else if err == nil {
		err = e
	}
	if v, e := get(rule.CoverURL, true); e == nil {
		book.CoverURL = firstNonEmpty(v, book.CoverURL)
	} else if err == nil {
		err = e
	}
	if v, e := get(rule.WordCount, false); e == nil {
		book.WordCount = firstNonEmpty(v, book.WordCount)
	} else if err == nil {
		err = e
	}
	tocURL, e := get(rule.TocURL, true)
	if err != nil {
		return nil, err
	}
	if e != nil {
		return nil, e
	}
	book.TocURL = firstNonEmpty(tocURL, book.TocURL, book.BookURL)
	return book, nil
}

// ParseChapterList 解析目录（含 nextTocUrl 翻页）
func ParseChapterList(engine Engine, book *model.Book, body, baseURL string, rule model.TocRules) ([]model.Chapter, error) {
	html := body
	if rule.PreUpdateJs != "" {
		out, err := engine.EvalJS(rule.PreUpdateJs, Bindings{Book: book, Result: html, BaseURL: baseURL})
		if err != nil {
			return nil, err
		}
		if s := strings.TrimSpace(rules.JsResultToString(out)); s != "" {
			html = s
		}
	}

	var chapters []model.Chapter
	seenChapter := map[string]bool{}
	seenPage := map[string]bool{}
	var pending []string

	collect := func(r *analyze.Rule) error {
		elements, err := r.GetElements(rule.ChapterList)
		if err != nil {
			return err
		}
		for _, item := range elements {
			ir := engine.MakeRule(item, baseURL, Bindings{Book: book})
			title, err := ir.GetString(rule.ChapterName)
			if err != nil {
				return err
			}
			title = strings.TrimSpace(title)
			if title == "" {
				continue
			}
			u, err := ir.GetStringIsURL(rule.ChapterURL)
			if err != nil {
				return err
			}
			chapterURL := firstNonEmpty(u, baseURL)
			if seenChapter[chapterURL] {
				continue
			}
			seenChapter[chapterURL] = true

			isVolume := strings.EqualFold(strings.TrimSpace(mustString(ir.GetString(rule.IsVolume))), "true")
			isVIP := strings.EqualFold(strings.TrimSpace(mustString(ir.GetString(rule.IsVIP))), "true")
			isPay := strings.EqualFold(strings.TrimSpace(mustString(ir.GetString(rule.IsPay))), "true")
			updateTime := mustString(ir.GetString(rule.UpdateTime))

			chapter := model.Chapter{
				BookURL:  book.BookURL,
				Index:    len(chapters),
				Title:    title,
				URL:      chapterURL,
				BaseURL:  baseURL,
				IsVolume: isVolume,
				IsVIP:    isVIP,
				IsPay:    isPay,
				Tag:      updateTime,
			}
			if rule.FormatJS != "" {
				out, err := engine.EvalJS(rule.FormatJS, Bindings{Book: book, Chapter: &chapter, Title: title, BaseURL: baseURL})
				if err != nil {
					return err
				}
				if out != nil {
					chapter.Title = rules.JsResultToString(out)
				}
			}
			chapters = append(chapters, chapter)
		}
		if rule.NextTocURL == "" {
			return nil
		}
		nextURLs, err := r.GetStringListIsURL(rule.NextTocURL)
		if err != nil {
			return err
		}
		for _, u := range nextURLs {
			if u == "" || seenPage[u] {
				continue
			}
			seenPage[u] = true
			pending = append(pending, u)
		}
		return nil
	}

	if err := collect(engine.MakeRule(html, baseURL, Bindings{Book: book})); err != nil {
		return nil, err
	}

	guard := 0
	for len(pending) > 0 && guard < 20 {
		guard++
		nextURL := pending[0]
		pending = pending[1:]
		resp, err := engine.Fetch(nextURL, Bindings{Book: book})
		if err != nil {
			return nil, err
		}
		if err := collect(engine.MakeRule(resp.Body, resp.FinalURL, Bindings{Book: book})); err != nil {
			return nil, err
		}
	}
	return chapters, nil
}

// ParseContent 解析正文（含 nextContentUrl 翻页与替换规则）
func ParseContent(engine Engine, book *model.Book, chapter *model.Chapter, body, baseURL string, rule model.ContentRules) (*model.Content, error) {
	html := body
	currentURL := baseURL
	title := ""

	var texts []string
	guard := 0
	for {
		r := engine.MakeRule(html, currentURL, Bindings{Book: book, Chapter: chapter, Title: chapter.Title})
		if title == "" && rule.Title != "" {
			got, err := r.GetString(rule.Title)
			if err != nil {
				return nil, err
			}
			title = strings.TrimSpace(got)
		}
		list, err := r.GetStringList(rule.Content)
		if err != nil {
			return nil, err
		}
		texts = append(texts, list...)

		if rule.NextContentURL == "" || guard >= 20 {
			break
		}
		guard++
		nextURL, err := r.GetStringIsURL(rule.NextContentURL)
		if err != nil {
			return nil, err
		}
		nextURL = strings.TrimSpace(nextURL)
		if nextURL == "" || nextURL == currentURL {
			break
		}
		resp, err := engine.Fetch(nextURL, Bindings{Book: book, Chapter: chapter, NextChapterURL: nextURL})
		if err != nil {
			return nil, err
		}
		html = resp.Body
		currentURL = resp.FinalURL
	}

	content := strings.Join(texts, "\n")
	content, err := ApplyReplaceRules(engine, content, rule.ReplaceRegex, Bindings{Book: book, Chapter: chapter})
	if err != nil {
		return nil, err
	}
	if title == "" {
		title = chapter.Title
	}
	return &model.Content{Title: title, Content: FormatContent(content)}, nil
}

func mustString(s string, _ error) string { return s }

// ApplyReplaceRules 执行正文替换规则（多行规则集，`##匹配##替换`；`<js>`/`@js:` 走 JS）
func ApplyReplaceRules(engine Engine, text, ruleSet string, b Bindings) (string, error) {
	if ruleSet == "" {
		return text, nil
	}
	out := text
	for _, raw := range splitReplaceRules(ruleSet) {
		rule := strings.TrimSpace(raw)
		if rule == "" {
			continue
		}
		if js := jsOnlyRule(rule); js != nil {
			b.Result = out
			res, err := engine.EvalJS(*js, b)
			if err != nil {
				return "", err
			}
			out = rules.JsResultToString(res)
			continue
		}
		parts := strings.Split(rule, "##")
		if len(parts) < 2 {
			continue
		}
		pattern := strings.TrimSpace(parts[0])
		replacement := parts[1]
		firstOnly := len(parts) > 3
		re, err := regexp.Compile("(?s)" + pattern)
		if err != nil {
			if !firstOnly {
				out = strings.ReplaceAll(out, pattern, replacement)
			}
			continue
		}
		if firstOnly {
			loc := re.FindStringIndex(out)
			if loc != nil {
				one, err := regexp.Compile("(?s)" + pattern)
				if err == nil {
					out = out[:loc[0]] + one.ReplaceAllString(out[loc[0]:loc[1]], replacement) + out[loc[1]:]
				}
			}
			continue
		}
		out = re.ReplaceAllString(out, replacement)
	}
	return out, nil
}

func jsOnlyRule(rule string) *string {
	lower := strings.ToLower(rule)
	if strings.HasPrefix(lower, "<js>") && strings.HasSuffix(lower, "</js>") {
		body := rule[4 : len(rule)-5]
		return &body
	}
	if strings.HasPrefix(lower, "@js:") {
		body := rule[4:]
		return &body
	}
	return nil
}

// splitReplaceRules 按换行拆分替换规则；`<js>...</js>` 内部允许换行
func splitReplaceRules(rulesText string) []string {
	var out []string
	buf := ""
	inJS := false
	for _, line := range strings.Split(rulesText, "\n") {
		if !inJS && strings.Contains(strings.ToLower(line), "<js>") {
			inJS = true
		}
		if buf != "" {
			buf += "\n"
		}
		buf += line
		if inJS && strings.Contains(strings.ToLower(line), "</js>") {
			inJS = false
		}
		if !inJS {
			out = append(out, buf)
			buf = ""
		}
	}
	if strings.TrimSpace(buf) != "" {
		out = append(out, buf)
	}
	return out
}

var blockTagPattern = regexp.MustCompile(`(?i)</?(?:br|p|div|section|article|li|ul|ol|h[1-6]|tr|td|blockquote|pre)\b[^>]*>`)
var scriptStylePattern = regexp.MustCompile(`(?is)<script[^>]*>[\s\S]*?</script>|<style[^>]*>[\s\S]*?</style>`)
var commentPattern = regexp.MustCompile(`(?s)<!--[\s\S]*?-->`)
var tagPattern = regexp.MustCompile(`<[^>]+>`)
var htmlTagHint = regexp.MustCompile(`[<][a-zA-Z!/]`)
var zeroWidthPattern = regexp.MustCompile(`[\x{200b}-\x{200d}\x{feff}]`)

// HTMLToText 把 HTML 片段转成带换行的纯文本（对齐 Legado 的 HtmlFormatter）
func HTMLToText(text string) string {
	if !htmlTagHint.MatchString(text) {
		return text
	}
	out := scriptStylePattern.ReplaceAllString(text, "")
	out = commentPattern.ReplaceAllString(out, "")
	out = blockTagPattern.ReplaceAllString(out, "\n")
	return tagPattern.ReplaceAllString(out, "")
}

// FormatContent 正文格式化：去标签、统一换行、去零宽字符、去空行
func FormatContent(content string) string {
	text := analyze.UnescapeHTML(HTMLToText(content))
	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")
	text = strings.ReplaceAll(text, "\u00a0", " ")
	var lines []string
	for _, line := range strings.Split(text, "\n") {
		line = zeroWidthPattern.ReplaceAllString(line, "")
		line = strings.TrimSpace(line)
		if line != "" {
			lines = append(lines, line)
		}
	}
	return strings.Join(lines, "\n")
}
