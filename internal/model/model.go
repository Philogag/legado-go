// Package model 定义书源配置、书籍/章节实体与统一错误类型。
package model

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
)

// 书源类型，与阅读 App 保持一致
const (
	SourceTypeText  = 0
	SourceTypeAudio = 1
	SourceTypeImage = 2
	SourceTypeFile  = 3
)

// SearchRules 搜索页规则
type SearchRules struct {
	CheckKeyWord string `json:"checkKeyWord"`
	BookList     string `json:"bookList"`
	Name         string `json:"name"`
	Author       string `json:"author"`
	Intro        string `json:"intro"`
	Kind         string `json:"kind"`
	LastChapter  string `json:"lastChapter"`
	UpdateTime   string `json:"updateTime"`
	BookURL      string `json:"bookUrl"`
	CoverURL     string `json:"coverUrl"`
	WordCount    string `json:"wordCount"`
}

// ExploreRules 发现页规则
type ExploreRules struct {
	BookList    string `json:"bookList"`
	Name        string `json:"name"`
	Author      string `json:"author"`
	Intro       string `json:"intro"`
	Kind        string `json:"kind"`
	LastChapter string `json:"lastChapter"`
	UpdateTime  string `json:"updateTime"`
	BookURL     string `json:"bookUrl"`
	CoverURL    string `json:"coverUrl"`
	WordCount   string `json:"wordCount"`
}

// BookInfoRules 详情页规则
type BookInfoRules struct {
	Init        string `json:"init"`
	Name        string `json:"name"`
	Author      string `json:"author"`
	Intro       string `json:"intro"`
	Kind        string `json:"kind"`
	LastChapter string `json:"lastChapter"`
	UpdateTime  string `json:"updateTime"`
	CoverURL    string `json:"coverUrl"`
	TocURL      string `json:"tocUrl"`
	WordCount   string `json:"wordCount"`
	CanReName   string `json:"canReName"`
	DownloadURL string `json:"downloadUrls"`
}

// TocRules 目录页规则
type TocRules struct {
	PreUpdateJs string `json:"preUpdateJs"`
	ChapterList string `json:"chapterList"`
	ChapterName string `json:"chapterName"`
	ChapterURL  string `json:"chapterUrl"`
	FormatJS    string `json:"formatJs"`
	IsVolume    string `json:"isVolume"`
	IsVIP       string `json:"isVip"`
	IsPay       string `json:"isPay"`
	UpdateTime  string `json:"updateTime"`
	NextTocURL  string `json:"nextTocUrl"`
}

// ContentRules 正文页规则
type ContentRules struct {
	Title          string `json:"title"`
	Content        string `json:"content"`
	NextContentURL string `json:"nextContentUrl"`
	WebJs          string `json:"webJs"`
	SourceRegex    string `json:"sourceRegex"`
	ReplaceRegex   string `json:"replaceRegex"`
	ImageStyle     string `json:"imageStyle"`
	PayAction      string `json:"payAction"`
}

// Config 是书源 JSON 的原始映射（字段名与阅读 App 导出的书源一致）。
type Config struct {
	BookSourceURL     string `json:"bookSourceUrl"`
	BookSourceName    string `json:"bookSourceName"`
	BookSourceGroup   string `json:"bookSourceGroup"`
	BookSourceType    *int   `json:"bookSourceType"`
	BookURLPattern    string `json:"bookUrlPattern"`
	CustomOrder       int    `json:"customOrder"`
	Enabled           *bool  `json:"enabled"`
	EnabledExplore    *bool  `json:"enabledExplore"`
	SearchURL         string `json:"searchUrl"`
	ExploreURL        string `json:"exploreUrl"`
	Header            string `json:"header"`
	LoginURL          string `json:"loginUrl"`
	LoginUI           string `json:"loginUi"`
	LoginCheckJS      string `json:"loginCheckJs"`
	JSLib             string `json:"jsLib"`
	BookSourceComment string `json:"bookSourceComment"`
	Variable          string `json:"variable"`
	LastUpdateTime    int64  `json:"lastUpdateTime"`
	RespondTime       int64  `json:"respondTime"`
	Weight            int    `json:"weight"`
	ConcurrentRate    string `json:"concurrentRate"`

	RuleSearch   SearchRules   `json:"ruleSearch"`
	RuleExplore  ExploreRules  `json:"ruleExplore"`
	RuleBookInfo BookInfoRules `json:"ruleBookInfo"`
	RuleToc      TocRules      `json:"ruleToc"`
	RuleContent  ContentRules  `json:"ruleContent"`
}

// ParseConfig 解析书源 JSON；同时接受"书源数组"的第一个元素与纯配置对象。
func ParseConfig(data []byte) (*Config, error) {
	trimmed := strings.TrimSpace(string(data))
	if trimmed == "" {
		return nil, NewError(KindInvalidConfig, "书源配置为空")
	}
	if strings.HasPrefix(trimmed, "[") {
		var list []Config
		if err := json.Unmarshal([]byte(trimmed), &list); err != nil {
			return nil, NewError(KindInvalidConfig, "书源配置解析失败: %v", err)
		}
		if len(list) == 0 {
			return nil, NewError(KindInvalidConfig, "书源数组为空")
		}
		return &list[0], nil
	}
	var cfg Config
	if err := json.Unmarshal([]byte(trimmed), &cfg); err != nil {
		return nil, NewError(KindInvalidConfig, "书源配置解析失败: %v", err)
	}
	return &cfg, nil
}

// Source 是归一化之后、不可变的书源配置。
type Source struct {
	Config

	Name           string
	Type           int
	Group          string
	URL            string
	Origin         string
	HeaderMap      map[string]string
	LoginHeaderMap map[string]string
	SearchURL      string
	ExploreURL     string
	Variable       map[string]string
	ExploreEntries []ExploreEntry
}

// ExploreEntry 是发现页的一个入口（exploreUrl 数组元素）。
type ExploreEntry struct {
	Title string `json:"title"`
	URL   string `json:"url"`
	Style *struct {
		LayoutFlex    string `json:"layout_flexBasisPercent"`
		LayoutGravity string `json:"layout_gravity"`
	} `json:"style,omitempty"`
}

// Normalize 归一化配置：补齐默认值、解析 header / variable / exploreUrl。
func Normalize(cfg *Config) (*Source, error) {
	if cfg == nil {
		return nil, NewError(KindInvalidConfig, "书源配置为空")
	}
	if strings.TrimSpace(cfg.BookSourceURL) == "" {
		return nil, NewError(KindInvalidConfig, "书源配置缺少 bookSourceUrl")
	}
	src := &Source{Config: *cfg}
	src.URL = strings.TrimSpace(cfg.BookSourceURL)
	src.Origin = originOf(src.URL)
	src.Name = strings.TrimSpace(cfg.BookSourceName)
	if src.Name == "" {
		src.Name = src.Origin
	}
	if src.Name == "" {
		src.Name = src.URL
	}
	src.Group = cfg.BookSourceGroup
	if cfg.BookSourceType != nil {
		src.Type = *cfg.BookSourceType
	}
	if cfg.Enabled != nil && !*cfg.Enabled {
		return nil, NewError(KindInvalidConfig, "书源已禁用")
	}
	src.HeaderMap = parseHeaderJSON(cfg.Header)
	src.LoginHeaderMap = map[string]string{}
	src.SearchURL = strings.TrimSpace(cfg.SearchURL)
	src.ExploreURL = strings.TrimSpace(cfg.ExploreURL)
	src.Variable = splitVariables(cfg.Variable)
	if err := src.parseExploreEntries(); err != nil {
		return nil, err
	}
	return src, nil
}

func (s *Source) parseExploreEntries() error {
	if s.ExploreURL == "" {
		return nil
	}
	raw := strings.TrimSpace(s.ExploreURL)
	if !strings.HasPrefix(raw, "[") {
		// 文本形式：每行一个入口，支持 `名称::url`、`名称##url`，缺名称时用默认
		var entries []ExploreEntry
		for _, line := range strings.Split(raw, "\n") {
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			title, target, ok := cutEntry(line)
			if !ok {
				title, target = "默认", line
			}
			entries = append(entries, ExploreEntry{Title: title, URL: target})
		}
		if len(entries) == 0 {
			entries = []ExploreEntry{{Title: "默认", URL: raw}}
		}
		s.ExploreEntries = entries
		return nil
	}
	var entries []ExploreEntry
	if err := json.Unmarshal([]byte(raw), &entries); err != nil {
		return NewError(KindInvalidConfig, "exploreUrl 解析失败: %v", err)
	}
	s.ExploreEntries = entries
	return nil
}

// cutEntry 拆分 `名称::url` / `名称##url` 形式的条目
func cutEntry(line string) (title, target string, ok bool) {
	for _, sep := range []string{"::", "##"} {
		if i := strings.Index(line, sep); i > 0 {
			title = strings.TrimSpace(line[:i])
			target = strings.TrimSpace(line[i+len(sep):])
			if title != "" && target != "" {
				return title, target, true
			}
		}
	}
	return "", "", false
}

func originOf(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return ""
	}
	return u.Scheme + "://" + u.Host
}

func parseHeaderJSON(raw string) map[string]string {
	out := map[string]string{}
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return out
	}
	if !strings.HasPrefix(raw, "{") {
		// "k:v\nk2:v2" 形式
		for _, line := range strings.Split(raw, "\n") {
			if k, v, ok := strings.Cut(line, ":"); ok {
				out[strings.TrimSpace(k)] = strings.TrimSpace(v)
			}
		}
		return out
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		return out
	}
	for k, v := range m {
		if v == nil {
			continue
		}
		if str, ok := v.(string); ok {
			out[k] = str
		} else {
			out[k] = fmt.Sprint(v)
		}
	}
	return out
}

// splitVariables 解析 "k=v\nk2=v2" 形式的书源变量。
func splitVariables(raw string) map[string]string {
	out := map[string]string{}
	trimmed := strings.TrimSpace(raw)
	if strings.HasPrefix(trimmed, "{") {
		// Legado 的 variable 字段是 JSON 对象
		var vars map[string]any
		if err := json.Unmarshal([]byte(trimmed), &vars); err == nil {
			for k, v := range vars {
				switch t := v.(type) {
				case string:
					out[k] = t
				case nil:
					out[k] = ""
				default:
					b, err := json.Marshal(t)
					if err != nil {
						continue
					}
					out[k] = string(b)
				}
			}
			return out
		}
	}
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		out[strings.TrimSpace(k)] = strings.TrimSpace(v)
	}
	return out
}

// Book 是一本书的基础信息
type Book struct {
	Name        string `json:"name"`
	Author      string `json:"author"`
	Kind        string `json:"kind,omitempty"`
	Intro       string `json:"intro,omitempty"`
	CoverURL    string `json:"coverUrl,omitempty"`
	TocURL      string `json:"tocUrl,omitempty"`
	BookURL     string `json:"bookUrl"`
	Origin      string `json:"origin,omitempty"`
	OriginName  string `json:"originName,omitempty"`
	LastChapter string `json:"lastChapter,omitempty"`
	UpdateTime  string `json:"updateTime,omitempty"`
	WordCount   string `json:"wordCount,omitempty"`
}

// Chapter 是一章目录信息
type Chapter struct {
	Title     string `json:"title"`
	URL       string `json:"url"`
	BaseURL   string `json:"baseUrl,omitempty"`
	Index     int    `json:"index"`
	BookURL   string `json:"bookUrl,omitempty"`
	IsVolume  bool   `json:"isVolume,omitempty"`
	IsVIP     bool   `json:"isVip,omitempty"`
	IsPay     bool   `json:"isPay,omitempty"`
	Tag       string `json:"tag,omitempty"`
	WordCount string `json:"wordCount,omitempty"`
}

// Content 是正文结果
type Content struct {
	Title   string `json:"title,omitempty"`
	Content string `json:"content"`
}

// 错误种类
const (
	KindInvalidConfig = "INVALID_CONFIG"
	KindNetwork       = "NETWORK"
	KindHTTP          = "HTTP"
	KindParseFailed   = "PARSE_FAILED"
	KindParseEmpty    = "PARSE_EMPTY"
	KindTimeout       = "TIMEOUT"
	KindClosed        = "CLOSED"
	KindNotFound      = "NOT_FOUND"
	KindExpired       = "EXPIRED"
	KindLimit         = "INSTANCE_LIMIT"
	KindInternal      = "INTERNAL"
)

// Error 是书源库的统一错误类型。
type Error struct {
	Kind  string
	Msg   string
	Cause error
}

func (e *Error) Error() string {
	if e.Cause != nil {
		return fmt.Sprintf("%s: %s: %v", e.Kind, e.Msg, e.Cause)
	}
	return fmt.Sprintf("%s: %s", e.Kind, e.Msg)
}

func (e *Error) Unwrap() error { return e.Cause }

// NewError 构造错误
func NewError(kind, format string, args ...any) *Error {
	return &Error{Kind: kind, Msg: fmt.Sprintf(format, args...)}
}

// WrapError 包装底层错误
func WrapError(kind string, cause error, format string, args ...any) *Error {
	return &Error{Kind: kind, Msg: fmt.Sprintf(format, args...), Cause: cause}
}

// KindOf 返回错误种类；非 *Error 返回 KindInternal。
func KindOf(err error) string {
	var e *Error
	if errors.As(err, &e) {
		return e.Kind
	}
	return KindInternal
}
