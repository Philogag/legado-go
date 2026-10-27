package model_test

import (
	"testing"

	"legado-go/internal/model"
)

func TestNormalizeExploreEntries(t *testing.T) {
	cases := []struct {
		name   string
		raw    string
		titles []string
		urls   []string
	}{
		{
			name:   "单条无标题",
			raw:    "/search?q=分类&page={{page}}",
			titles: []string{"默认"},
			urls:   []string{"/search?q=分类&page={{page}}"},
		},
		{
			name:   "多行 :: 分隔",
			raw:    "玄幻::/list/xuanhuan?page={{page}}\n都市::/list/dushi?page={{page}}",
			titles: []string{"玄幻", "都市"},
			urls:   []string{"/list/xuanhuan?page={{page}}", "/list/dushi?page={{page}}"},
		},
		{
			name:   "## 分隔",
			raw:    "分类##/search?q=分类&page={{page}}",
			titles: []string{"分类"},
			urls:   []string{"/search?q=分类&page={{page}}"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			src, err := model.Normalize(&model.Config{BookSourceName: "t", BookSourceURL: "http://x.example", ExploreURL: tc.raw})
			if err != nil {
				t.Fatalf("normalize: %v", err)
			}
			if len(src.ExploreEntries) != len(tc.titles) {
				t.Fatalf("want %d entries, got %+v", len(tc.titles), src.ExploreEntries)
			}
			for i := range tc.titles {
				if src.ExploreEntries[i].Title != tc.titles[i] || src.ExploreEntries[i].URL != tc.urls[i] {
					t.Fatalf("entry %d: want %s/%s, got %+v", i, tc.titles[i], tc.urls[i], src.ExploreEntries[i])
				}
			}
		})
	}
}

func TestSplitVariables(t *testing.T) {
	src, err := model.Normalize(&model.Config{
		BookSourceName: "t",
		BookSourceURL:  "http://x.example",
		Variable:       "{\"a\":\"1\",\"b\":2,\"c\":true}",
	})
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	if got := src.Variable; got["a"] != "1" || got["b"] != "2" || got["c"] != "true" {
		t.Fatalf("unexpected variables: %+v", got)
	}

	src2, err := model.Normalize(&model.Config{
		BookSourceName: "t2",
		BookSourceURL:  "http://x.example",
		Variable:       "k=v\nk2 = v2",
	})
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	if got := src2.Variable; got["k"] != "v" || got["k2"] != "v2" {
		t.Fatalf("unexpected variables: %+v", got)
	}
}

func TestParseHeaderAndValidate(t *testing.T) {
	cfg := &model.Config{BookSourceName: "h", BookSourceURL: "http://x.example", Header: "{\"User-Agent\":\"ua\",\"Referer\":\"http://r\"}"}
	src, err := model.Normalize(cfg)
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	if src.HeaderMap["User-Agent"] != "ua" || src.HeaderMap["Referer"] != "http://r" {
		t.Fatalf("unexpected headers: %+v", src.HeaderMap)
	}

	if _, err := model.Normalize(&model.Config{BookSourceName: "x"}); err == nil {
		t.Fatal("missing bookSourceUrl should fail")
	}
	// 缺少 bookSourceName 时回退到 host
	fallback, err := model.Normalize(&model.Config{BookSourceURL: "http://x.example"})
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	if fallback.Name != "http://x.example" {
		t.Fatalf("want name fallback to origin, got %q", fallback.Name)
	}
}
