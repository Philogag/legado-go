package jsonpath

import (
	"encoding/json"
	"reflect"
	"testing"
)

const doc = `{
  "store": {
    "book": [
      {"category":"reference","author":"Nigel Rees","title":"Sayings of the Century","price":8.95},
      {"category":"fiction","author":"Evelyn Waugh","title":"Sword of Honour","price":12.99},
      {"category":"fiction","author":"Herman Melville","title":"Moby Dick","isbn":"0-553-21311-3","price":8.99},
      {"category":"fiction","author":"J. R. R. Tolkien","title":"The Lord of the Rings","isbn":"0-395-19395-8","price":22.99}
    ],
    "bicycle": {"color":"red","price":19.95}
  },
  "expensive": 10
}`

func load(t *testing.T) any {
	t.Helper()
	var v any
	if err := json.Unmarshal([]byte(doc), &v); err != nil {
		t.Fatal(err)
	}
	return v
}

func TestJaywayExamples(t *testing.T) {
	data := load(t)
	cases := []struct {
		path string
		want []any
	}{
		{"$.store.book[*].author", []any{"Nigel Rees", "Evelyn Waugh", "Herman Melville", "J. R. R. Tolkien"}},
		{"$..author", []any{"Nigel Rees", "Evelyn Waugh", "Herman Melville", "J. R. R. Tolkien"}},
		{"$.store..price", []any{8.95, 12.99, 8.99, 22.99, 19.95}},
		{"$..book[2].title", []any{"Moby Dick"}},
		{"$..book[-1].title", []any{"The Lord of the Rings"}},
		{"$..book[0,1].title", []any{"Sayings of the Century", "Sword of Honour"}},
		{"$..book[:2].title", []any{"Sayings of the Century", "Sword of Honour"}},
		{"$..book[?(@.isbn)].title", []any{"Moby Dick", "The Lord of the Rings"}},
		{"$..book[?(@.price<10)].title", []any{"Sayings of the Century", "Moby Dick"}},
		{"$..book[?(@.price<10 && @.category=='fiction')].title", []any{"Moby Dick"}},
		{"$..book[?(@.author =~ /.*REES/i)].title", []any{"Sayings of the Century"}},
		{"$..book[?(@.price>$.store.bicycle.price)].title", []any{"The Lord of the Rings"}},
		{"$.store.book[?(@.title in ['Moby Dick'])].title", []any{"Moby Dick"}},
		{"$.store.book.length()", []any{float64(4)}},
		{"$.store.bicycle.color", []any{"red"}},
		{"$.store.book[?(@.category nin ['fiction'])].title", []any{"Sayings of the Century"}},
		{"$.expensive", []any{float64(10)}},
		{"$..book[1:3].category", []any{"fiction", "fiction"}},
		{"$.store.keys()", []any{"bicycle", "book"}},
		{"$.store.book[?(!@.isbn)].title", []any{"Sayings of the Century", "Sword of Honour"}},
		{"$.store.book[?(@.price > 9 && @.price < 13)].title", []any{"Sword of Honour"}},
		{"$['store']['bicycle']['color']", []any{"red"}},
		{"$.store.book[*].title.size()", []any{float64(22), float64(15), float64(9), float64(21)}},
	}
	for _, c := range cases {
		got, err := Eval(data, c.path)
		if err != nil {
			t.Fatalf("%s: %v", c.path, err)
		}
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s:\n got %#v\nwant %#v", c.path, got, c.want)
		}
	}
}

func TestChineseAndHTML(t *testing.T) {
	var v any
	_ = json.Unmarshal([]byte(`{"data":{"list":[{"name":"诡秘之主","wordCount":"1100万字"},{"name":"大奉打更人","wordCount":"300万字"}]}}`), &v)
	got, err := Eval(v, "$.data.list[?(@.name=='大奉打更人')].wordCount")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0] != "300万字" {
		t.Fatalf("got %#v", got)
	}
}

func TestSyntaxError(t *testing.T) {
	if _, err := Eval(load(t), "$.store.book["); err == nil {
		t.Fatal("期望语法错误")
	}
	if IsPath("tag.a@text") {
		t.Fatal("普通规则不应被识别为 JsonPath")
	}
	if !IsPath("$..book") {
		t.Fatal("$..book 应被识别为 JsonPath")
	}
}
