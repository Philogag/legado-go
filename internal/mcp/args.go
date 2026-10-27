package mcp

import (
	"encoding/json"

	"legado-go/internal/model"
)

// instanceArgs 是「操作某个实例」的工具通用入参。
// `instanceId` 是推荐写法，`id` 作为兼容别名保留。
type instanceArgs struct {
	ID         string `json:"id"`
	InstanceID string `json:"instanceId"`
}

// instance 返回实例 id（instanceId 优先）。
func (a instanceArgs) instance() string {
	if a.InstanceID != "" {
		return a.InstanceID
	}
	return a.ID
}

// bookArgs 支持 `book` 对象（推荐，直接回传 source_search / source_book_info 的结果）
// 与平铺的 bookUrl/tocUrl/name/author。
type bookArgs struct {
	ID         string          `json:"id"`
	InstanceID string          `json:"instanceId"`
	Book       json.RawMessage `json:"book"`
	BookURL    string          `json:"bookUrl"`
	TocURL     string          `json:"tocUrl"`
	Name       string          `json:"name"`
	Author     string          `json:"author"`
}

func (a bookArgs) instance() string {
	if a.InstanceID != "" {
		return a.InstanceID
	}
	return a.ID
}

func (a bookArgs) toBook() *model.Book {
	if len(a.Book) > 0 && string(a.Book) != "null" {
		var b model.Book
		if json.Unmarshal(a.Book, &b) == nil && b.BookURL != "" {
			return &b
		}
	}
	return &model.Book{BookURL: a.BookURL, TocURL: a.TocURL, Name: a.Name, Author: a.Author}
}

// chapterArgs 支持 `chapter` 对象（直接回传 source_chapters 的元素）与平铺字段。
type chapterArgs struct {
	bookArgs
	Chapter    json.RawMessage `json:"chapter"`
	ChapterURL string          `json:"chapterUrl"`
	Title      string          `json:"title"`
	Index      int             `json:"index"`
}

func (a chapterArgs) toChapter() *model.Chapter {
	if len(a.Chapter) > 0 && string(a.Chapter) != "null" {
		var ch model.Chapter
		if json.Unmarshal(a.Chapter, &ch) == nil && ch.URL != "" {
			return &ch
		}
	}
	return &model.Chapter{URL: a.ChapterURL, Title: a.Title, Index: a.Index}
}
