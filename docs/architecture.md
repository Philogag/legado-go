# 架构（architecture.md）

```
┌──────────────────────────── 接入层 ────────────────────────────┐
│  REST (echo)  /api/v1/*          MCP 2026-07-28  /mcp          │
│  internal/httpapi                internal/mcp                  │
│  鉴权 · 限流 · 错误映射           12 tools · JSON-RPC 2.0        │
└───────────────────────────┬───────────────────────────────────┘
                            │ 都用 registry 拿实例
┌───────────────────────────▼───────────────────────────────────┐
│  TTL 实例注册表  internal/registry                              │
│  滑动过期 · 绝对寿命 · 墓碑 · 后台 Sweep · Stats                  │
└───────────────────────────┬───────────────────────────────────┘
┌───────────────────────────▼───────────────────────────────────┐
│  书源实例  internal/booksource                                  │
│  Search / Explore / GetBookInfo / GetChapterList / GetContent   │
│  EvalJS · Variables · Close       （实现 rules.Engine 接口）     │
└──────┬───────────────────┬───────────────────┬────────────────┘
       │                   │                   │
┌──────▼──────┐  ┌─────────▼────────┐  ┌───────▼────────┐
│ 规则引擎     │  │ JS 沙箱           │  │ HTTP 客户端     │
│ analyze/    │  │ jsengine (goja)  │  │ netx           │
│ rules/      │  │ java/source/     │  │ CookieJar ·    │
│ analyzeurl/ │  │ book/chapter/    │  │ 字符集 · 缓存   │
│ jsonpath/   │  │ cookie/cache     │  │ 重试 · 代理     │
└─────────────┘  └──────────────────┘  └────────────────┘
                    model（配置模型 + 错误类型）
```

单进程、纯 Go、无 CGO。每个实例持有：`*netx.Client`（含 Cookie/缓存/连接池）、
`*jsengine.Engine`（一个 goja VM + 一个同步 RPC 通道）。

## 一次搜索请求的完整链路

```
POST /v1/instances/{id}/search {"key":"剑","page":1}
 │
 ├─ httpapi: 取 token（可选）→ registry.Get(id)            ← 刷新 lastUsed
 │            失败 → 410 EXPIRED / 404 NOT_FOUND
 │
 ├─ booksource.Search(key, page)
 │   ├─ 1) analyzeurl.Analyze(searchUrl, ctx)               "…/search?q={{key}}&page={{page}}"
 │   │     └─ ReplaceTemplate: {{key}} → urlEncode(key)、{{page}} → 1
 │   │        解析 <,>,js,post,body,charset,headers,cookie,proxy,retry,webView…
 │   ├─ 2) netx.Client.Fetch(parsed)  → StrResponse         字符集探测/解码、Cookie、缓存
 │   ├─ 3) rules.ApplyResult (ruleSearch.bookList)          AnalyzeRule → 元素列表
 │   ├─ 4) 逐元素跑 name/author/bookUrl/cover… 规则          每字段：切分 → 解析器 → @js:
 │   ├─ 5) 相对 URL 绝对化（analyze.AbsoluteURL）             bookUrl/coverUrl 以请求 URL 为 base
 │   └─ 6) checkKeyWord 过滤 + sortUrl/sortName              （配置了才生效）
 │
 └─ 200 {"books":[...],"count":N,"elapsedMs":123}
```

`GetContent` 多两条路径：

```
chapterUrl → 规则 content（可能含 `-` 反向 / `##` 正则替换）
           → 若 nextContentUrl 非空：循环抓下一页（≤20 页，URL 去重）追加正文
           → replaceRegex（可多条）→ FormatContent（零宽字符、空行、缩进）
```

`GetChapterList` 同理，`nextTocUrl` 最多跟随 20 页。

## 规则求值：三层结构

```
rule 字符串
  │ splitSourceRule（内部类 SourceRule）
  ├── 1. 分离 put 字段      @put:{k:v} / @get:{k}
  ├── 2. 分离 eval 片段     ##js… / {{…}} / <js>…</js>   （evalJS / evalJSAll）
  ├── 3. 分离 groupRef      $1 $2 …                       （后续 `###` 的输入）
  └── 4. 解析前缀链         @@ - : @css: @json: @xpath: @js: <js> @html @text
        ↓
    Analyzer（analyze 包，接口 GetString/GetElements/GetStringList）
        ↓
    JS 结果 → JsValueToString（对象取 string/list/map 的第一个…… 与上游对齐）
```

`AnalyzeRule` 负责编排：字符串模式返回 `${…}` 展开值，元素/列表模式走 `AnalyzeByRegex` 抽 `$n`。

## 为什么这样设计（关键决策）

| 决策 | 理由 |
| --- | --- |
| **纯 Go 复刻，而非桥接 upstream** | upstream 是 Android/Kotlin，`WebView`、`Gson`、`Coroutines` 不可移植；但**规则语义**必须逐条对齐才能复用现成书源 |
| **自研 JSONPath 而非第三方** | 第三方库不保留 `$.` 前缀语义（上游 `$.book.name` 是"从根取 `book.name`"，而非"取字段 `$`"），且要求"取首个/取列表"可切换 → `internal/jsonpath` 1400 行封装 |
| **goja + 同步 RPC 通道** | goja 无 goroutine 概念，JS 里 `java.ajax` 必须**同步**返回；用 `runtime.LockOSThread` + 请求/响应 channel 桥接 Go 的网络与缓存 |
| **REST 用 echo** | 曾选 gin，但 `gin → ugorji/go/codec` 在本机编译 OOM 被杀；echo 依赖轻、路由/中间件/错误处理够用，且我们自写错误映射的形态与框架无关 |
| **MCP 无状态化** | 2026-07-28 规范删除 `initialize`/`Mcp-Session-Id`，与"实例 ID 即会话"的 TTL 注册表天然契合 |
| **实例持有 Cookie 与 source 变量** | 上游书源依赖登录态（`source.put/get`、`cookie.*`）；注册表因此成为必要组件，而不是可选缓存 |
| **不缓存整章正文到磁盘** | 正文缓存命中率低、体积大；只缓存**页面级** HTTP 响应（`CACHE_TTL`） |

## 包职责与依赖方向

| 包 | 职责 | 依赖 |
| --- | --- | --- |
| `model` | 配置模型、归一化、错误类型 | 无 |
| `jsonpath` | JSONPath 引擎 | 无 |
| `rules` | 规则切分/前缀解析/JS 结果转换 | model |
| `analyze` | 规则执行（JSoup/XPath/JSONPath/正则/JS） | model, rules, jsonpath, goquery, htmlquery, goja |
| `analyzeurl` | URL 选项块 + 模板变量 | analyze? 否，仅 model |
| `netx` | HTTP、Cookie、字符集、缓存 | model |
| `jsengine` | goja VM + `java/source/book/chapter/cookie/cache` | model, netx(接口) |
| `booksource` | 实例与调用管线（唯一"知道全部规则字段"的地方） | 以上全部 |
| `registry` | TTL 生命周期 | booksource, model |
| `httpapi` / `mcp` | 传输层 | registry, model |

依赖是**单向**的：`model ← rules ← analyze ← booksource ← registry ← httpapi/mcp`。
`analyzeurl` 只依赖 `model`，因此可以被单独复用。

## 扩展点

| 想扩展 | 改哪里 |
| --- | --- |
| 新增规则前缀（例如 `@tpl:`） | `internal/analyze/rule.go` 的前缀表 + `analyze-rule` 分派 |
| 新增 JS API（例如 `java.aesEncode`） | `internal/jsengine/javaext.go`（纯函数）或 `engine.go` 的 `java`/`source` 对象 + `booksource.Options` 接口实现 |
| 新增输出格式（如 OPDS） | `internal/httpapi` 加 handler，复用 `registry` + `booksource` |
| 换存储后端（Redis 存实例） | 抽 `registry` 的 `Store` 接口；当前是内存 map |
| 支持 `webView` 抓取 | `netx.Fetch` 前判断 `parsed.WebView`，接 headless 浏览器（当前直接忽略，见 [compatibility.md](compatibility.md)） |
| 多协议书源（`bookSourceType=1` 音频） | `booksource` 新增 pipeline，规则复用 |

## 关键文件导航

| 文件 | 行数 | 内容 |
| --- | --- | --- |
| `internal/jsonpath/jsonpath.go` | 1356 | 自研 JSONPath |
| `internal/mcp/server.go` | 500 | JSON-RPC 分发、工具调用、批量、限额 |
| `internal/rules/rules.go` | 479 | 规则切分（上游 `AnalyzeRule` 的等价物） |
| `internal/model/model.go` | 418 | 配置结构体 + `Normalize` + `Error` |
| `internal/booksource/source.go` | 400+ | 实例与 `Search`/`GetContent` 等 |
| `internal/registry/registry.go` | 384 | TTL 注册表 |
| `internal/httpapi/handlers.go` | 406 | REST handler |
| `internal/netx/client.go` | 374 | HTTP + Cookie + 字符集 |
| `internal/jsengine/engine.go` | 354 | goja 沙箱与同步 RPC |
| `internal/analyze/jsoup.go` | 300+ | JSoup 选择器兼容（含 `@text`/`@html`/`!`/`-`） |

完整行数：`find . -name '*.go' | xargs wc -l | tail -1` → 约 11.9K 行（含测试 ~2.2K）。
