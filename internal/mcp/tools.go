package mcp

// toolDefinitions 返回 MCP tools/list 的工具清单（JSON Schema 2020-12）。
// 约定：实例入参统一用 `instanceId`（兼容 `id`）；book/chapter 既可用对象也可用平铺字段。
func toolDefinitions() []map[string]any {
	idProp := map[string]any{"type": "string", "description": "书源实例 id（source_create 返回）"}
	configProp := map[string]any{
		"description": "Legado 书源配置（对象或数组，字段与阅读 App 导出的 bookSource 一致）",
		"type":        []any{"object", "array"},
	}
	bookProp := map[string]any{
		"type":        "object",
		"description": "书籍对象，可直接回传 source_search / source_book_info 的响应",
	}
	chapterProp := map[string]any{
		"type":        "object",
		"description": "章节对象，可直接回传 source_chapters 的元素",
	}
	ttlProp := map[string]any{
		"type":        []any{"string", "integer"},
		"description": "空闲存活时间：\"90s\"/\"30m\" 或秒数；缺省用服务端默认值",
	}
	pageProp := map[string]any{"type": "integer", "minimum": 1, "default": 1}

	return []map[string]any{
		{
			"name":        "source_create",
			"title":       "创建书源实例",
			"description": "用一份 Legado 书源配置创建实例，返回实例 id；实例在 TTL 内无访问会被自动回收。",
			"inputSchema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"config": configProp,
					"ttl":    ttlProp,
					"id":     map[string]any{"type": "string", "description": "自定义实例 id（[A-Za-z0-9._-]，1-64 字符）；留空自动生成"},
					"name":   map[string]any{"type": "string", "description": "覆盖书源名称（便于区分同类实例）"},
					"owner":  map[string]any{"type": "string", "description": "调用方标识，便于排查"},
				},
				"required":             []any{"config"},
				"additionalProperties": false,
			},
		},
		{
			"name":        "source_validate",
			"title":       "校验书源配置",
			"description": "解析并归一化书源配置，返回是否可用及其能力（搜索/发现/变量数），不会创建实例。",
			"inputSchema": map[string]any{
				"type":                 "object",
				"properties":           map[string]any{"config": configProp},
				"required":             []any{"config"},
				"additionalProperties": false,
			},
		},
		{
			"name":        "source_list",
			"title":       "列出实例",
			"description": "列出当前存活的书源实例及其剩余 TTL。",
			"inputSchema": map[string]any{"type": "object", "properties": map[string]any{}, "additionalProperties": false},
		},
		{
			"name":        "source_get",
			"title":       "查看实例",
			"description": "查看单个实例的元信息、变量与发现页入口，不会刷新 TTL。",
			"inputSchema": map[string]any{
				"type":                 "object",
				"properties":           map[string]any{"instanceId": idProp},
				"required":             []any{"instanceId"},
				"additionalProperties": false,
			},
		},
		{
			"name":        "source_delete",
			"title":       "销毁实例",
			"description": "立即销毁实例并释放其 JS 运行时与缓存。",
			"inputSchema": map[string]any{
				"type":                 "object",
				"properties":           map[string]any{"instanceId": idProp},
				"required":             []any{"instanceId"},
				"additionalProperties": false,
			},
		},
		{
			"name":        "source_touch",
			"title":       "续期实例",
			"description": "手动续期：重置空闲计时，可选同时修改 TTL。",
			"inputSchema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"instanceId": idProp,
					"ttl":        ttlProp,
				},
				"required":             []any{"instanceId"},
				"additionalProperties": false,
			},
		},
		{
			"name":        "source_search",
			"title":       "搜索书籍",
			"description": "用实例的 ruleSearch 搜索关键词，返回书名/作者/详情页地址等。",
			"inputSchema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"instanceId": idProp,
					"key":        map[string]any{"type": "string"},
					"page":       pageProp,
				},
				"required":             []any{"instanceId", "key"},
				"additionalProperties": false,
			},
		},
		{
			"name":        "source_explore",
			"title":       "发现页列表",
			"description": "访问发现页入口（不传 url 时用书源的第一个入口），返回书籍列表。",
			"inputSchema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"instanceId": idProp,
					"url":        map[string]any{"type": "string", "description": "发现页 url 规则，可含 {{page}}"},
					"page":       pageProp,
				},
				"required":             []any{"instanceId"},
				"additionalProperties": false,
			},
		},
		{
			"name":        "source_book_info",
			"title":       "书籍详情",
			"description": "抓取详情页，返回补全后的书名、作者、简介、目录地址等。",
			"inputSchema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"instanceId": idProp,
					"book":       bookProp,
					"bookUrl":    map[string]any{"type": "string"},
					"name":       map[string]any{"type": "string"},
				},
				"required":             []any{"instanceId"},
				"additionalProperties": false,
			},
		},
		{
			"name":        "source_chapters",
			"title":       "目录列表",
			"description": "抓取目录页（含 nextTocUrl 翻页），返回章节列表。",
			"inputSchema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"instanceId": idProp,
					"book":       bookProp,
					"bookUrl":    map[string]any{"type": "string"},
					"tocUrl":     map[string]any{"type": "string"},
					"name":       map[string]any{"type": "string"},
					"author":     map[string]any{"type": "string"},
				},
				"required":             []any{"instanceId"},
				"additionalProperties": false,
			},
		},
		{
			"name":        "source_content",
			"title":       "章节正文",
			"description": "抓取单章正文（含 nextContentUrl 翻页与 replaceRegex），返回纯文本。",
			"inputSchema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"instanceId": idProp,
					"chapter":    chapterProp,
					"chapterUrl": map[string]any{"type": "string"},
					"title":      map[string]any{"type": "string"},
					"book":       bookProp,
					"bookUrl":    map[string]any{"type": "string"},
				},
				"required":             []any{"instanceId"},
				"additionalProperties": false,
			},
		},
		{
			"name":        "source_eval",
			"title":       "执行 JS 调试",
			"description": "在书源 JS 沙箱里执行一段代码（可用 java/source/cookie/cache/result 等变量），用于调试规则。",
			"inputSchema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"instanceId": idProp,
					"script":     map[string]any{"type": "string", "description": "要执行的 JS（别名 code）"},
					"result":     map[string]any{"description": "注入 result 变量"},
					"baseUrl":    map[string]any{"type": "string"},
					"key":        map[string]any{"type": "string"},
					"page":       pageProp,
					"title":      map[string]any{"type": "string"},
				},
				"required":             []any{"instanceId", "script"},
				"additionalProperties": false,
			},
		},
	}
}
