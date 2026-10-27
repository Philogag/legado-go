# 作为 Go 库使用（library.md）

服务端只是 `booksource` + `registry` 的一层薄封装。你也可以在自己的 Go 程序里直接用这两个包
（`internal/` 下的包同仓库内可直接 import；对外发布时改 `go.mod` 的 module path 或加一层 `pkg/` 转发即可）。

```go
import (
    "legado-go/internal/booksource"
    "legado-go/internal/model"
    "legado-go/internal/registry"
)
```

---

## 1. 最短路径：配置 → 实例 → 调用

```go
cfg, err := model.ParseConfig(rawJSON)   // 单个对象 / 数组首个元素都可以
if err != nil { /* INVALID_CONFIG */ }

src, err := booksource.New(cfg, booksource.Options{
    Timeout:  20 * time.Second,
    CacheTTL: time.Hour,
    OnLog:    func(line string) { log.Print(line) },
})
if err != nil { return err }
defer src.Close()                        // 关闭 goja 运行时

books, err := src.Search("剑来", 1)       // 搜索
info, err  := src.GetBookInfo(books[0]) // 详情
chaps, err := src.GetChapterList(info)   // 目录
ch, err    := src.GetContent(chaps[0], info) // 正文（含翻页合并）
fmt.Println(ch.Title, len(ch.Content))
```

`booksource.NewFromJSON(data, opts)` 是 `ParseConfig` + `New` 的合并版。

## 2. `booksource.Source` 对外方法

### 业务方法

| 方法 | 说明 |
| --- | --- |
| `Search(key string, page int) ([]model.Book, error)` | 搜索；结果按 `checkKeyWord` 过滤，命中缓存（key = 书源 URL + 关键字 + 页码） |
| `Explore(exploreURL string, page int) ([]model.Book, error)` | 发现页；`exploreURL` 传空时用 `exploreUrl` 的第一个入口 |
| `GetBookInfoByURL(bookURL, name string) (*model.Book, error)` | 直接按 URL 取详情（会先做 `bookUrlPattern` 快捷判定） |
| `GetBookInfo(book *model.Book) (*model.Book, error)` | 用已有 Book 的 `BookURL` 取详情 |
| `GetChapterList(book *model.Book) ([]model.Chapter, error)` | 目录，自动跟随 `nextTocUrl` 翻页（上限 20 页） |
| `GetContentByURL(chapterURL, title string) (*model.Content, error)` | 按 URL 取正文 |
| `GetContent(chapter *model.Chapter, book *model.Book) (*model.Content, error)` | 取正文，自动跟随 `nextContentUrl` 合并（上限 20 页）、应用 `replaceRegex` |
| `EvalJS(js string, b Bindings) (any, error)` | 在书源上下文里跑一段 JS（调试规则用） |
| `Fetch(urlRule string, b Bindings) (*netx.Response, error)` | 走书源的 header/cookie/charset/retry 抓一个地址 |
| `MakeRule(content any, baseURL string, b Bindings) *analyze.Rule` | 拿一个绑定好宿主的规则调度器，自己组合取值 |

### 元信息与生命周期

| 方法 | 说明 |
| --- | --- |
| `Config() *model.Source` / `Name()` / `URL()` / `ExploreEntries()` / `Variables()` | 归一化后的配置视图 |
| `HeaderMap()` / `SetLoginInfo(k,v)` | 请求头与登录信息 |
| `Close() error` | 释放 goja 运行时；之后调用返回 `CLOSED` |

### `Bindings`

```go
type Bindings struct {
    BaseURL string   // 相对 URL 基准（一般传当前页面地址）
    Page    int      // 页码（<=0 时按 1 处理）
    Key     string   // 搜索关键字
    Title   string   // 章节标题
    Src     string   // 页面源码
    Result  any      // 当前中间结果（给 JS 用）
    Book    *model.Book
    Chapter *model.Chapter
}
```

## 3. `booksource.Options`

```go
type Options struct {
    Timeout  time.Duration                         // 单次 HTTP 超时，默认 30s
    CacheTTL time.Duration                         // 缓存默认 TTL，默认 1h
    Cache    Cache                                 // 自定义缓存（默认内存实现）
    Client   *netx.Client                          // 共享 HTTP 客户端（可跨实例共享 cookie）
    OnLog    func(line string)                     // 接收 console.log / java.log
}
```

`Cache` 接口只有三个方法，接 Redis 很容易：

```go
type Cache interface {
    Put(ctx context.Context, key, value string, ttl time.Duration)
    Get(ctx context.Context, key string) (string, bool)
    Delete(ctx context.Context, key string)
}
```

## 4. `registry`：TTL 实例管理

```go
reg := registry.New(registry.Options{
    DefaultTTL:    30 * time.Minute,
    MaxTTL:        6 * time.Hour,
    MaxInstances:  64,
    SweepInterval: 5 * time.Second,
    GracePeriod:   10 * time.Minute,
    SourceOptions: booksource.Options{Timeout: 20 * time.Second},
    OnEvict:       func(info registry.Info) { log.Printf("evicted %s", info.ID) },
})
defer reg.Close()
```

| 方法 | 说明 |
| --- | --- |
| `Create(cfg *model.Config, ttl time.Duration, owner string) (*Entry, error)` | 创建实例，自动生成 `src-<hex>` ID |
| `CreateWithID(id string, cfg, ttl, owner) (*Entry, error)` | 指定 ID（`[A-Za-z0-9._-]{1,64}`），重复则报错；可幂等重建 |
| `Get(id string) (*Entry, error)` | 取实例并**续期**（滑动空闲 TTL）；过期返回 `EXPIRED`，不存在返回 `NOT_FOUND` |
| `Lookup(id string) (*Entry, error)` | 取实例但**不续期** |
| `Touch(id string, ttl time.Duration) (*Entry, error)` | 手动续期，`ttl>0` 时顺带改 TTL |
| `Delete(id string) error` | 立即回收（留下墓碑） |
| `List() []Info` | 当前存活实例 |
| `Sweep() int` | 立即扫一次过期实例，返回回收数（后台扫描默认就在跑） |
| `Stats() Stats` | created / expired / evicted / 当前数量等计数 |
| `DefaultTTL() time.Duration` | 默认 TTL（服务端 `/api/v1/meta` 会用它） |

`Entry` 上还有 `Info()` 和 `TTLOf()`。完整语义见 [ttl.md](ttl.md)。

## 5. 其它包（需要更底层时）

### `internal/model`

```go
model.ParseConfig([]byte) (*Config, error)   // 兼容数组/对象
model.Normalize(*Config) (*Source, error)    // 补默认值、解析 header/variable/exploreUrl
model.NewError(kind, format, args...) *Error // kind 取 KindXxx 常量
model.KindOf(err) string                     // 取错误码
```

`Book` / `Chapter` / `Content` / `ExploreEntry` 都是纯数据结构（JSON tag 已对齐 REST 输出）。

### `internal/netx`

```go
c := netx.New(30 * time.Second)              // 带 cookie 存储的客户端
resp, err := c.Do(netx.Request{
    URL: "https://x/", Method: "GET",
    Headers: map[string]string{"User-Agent": "..."},
    Charset: "gbk", Retry: 1,
})
// resp: StatusCode, Body(string, 已按 charset 解码), Header, FinalURL, Elapsed
```

`netx.NewCookieStore()` 可单独使用；cookie 按域名隔离。

### `internal/analyze`

```go
rule := analyze.New(host, body, baseURL)     // host 实现 EvalJS/GetVar/PutVar
s, _   := rule.GetString("h3@a@text")
list, _ := rule.GetStringList(".item")        // 或 GetStringListIsURL
node, _ := rule.GetElement("//div[@id='x']")
nodes, _ := rule.GetElements(".item")
```
另有 `analyze.NewJsoup / NewXPath / NewJsonPath`、`AbsoluteURL`、`UnescapeHTML`。

### `internal/jsonpath`

```go
vals, err := jsonpath.Eval(data, "$.data.list[*].name")
one, ok, err := jsonpath.EvalOne(data, "$.data.total")
p, err := jsonpath.Compile("$..book[?(@.price < 10)]")  // 可复用
```

### `internal/rules`

规则切分层：`NewSourceRule` / `SplitSourceRule` / `SplitBalanced` / `SourceRule.MakeUpRule` / `JsResultToString`。
自己写引擎时用得上，一般不需要直接碰。

### `internal/jsengine`

```go
e := jsengine.New(host)        // host 需实现 jsengine.Host（java/source/book/chapter/cookie/cache/jsLib/log）
v, err := e.Eval("java.ajax(baseUrl)", jsengine.Bindings{BaseURL: "https://x/"})
e.Close()
```

### JS 上下文

`EvalJS` / 规则里的 `@js:` / `<js>` 都跑在同一个沙箱里，可用绑定：

| 绑定 | 类型 | 说明 |
| --- | --- | --- |
| `result` | any | 上一步规则的结果（字符串/元素/列表） |
| `baseUrl` | string | 当前页面 URL（`Bindings.BaseURL`） |
| `key` | string | 搜索关键字（`Bindings.Key`） |
| `page` | number | 页码（`Bindings.Page`） |
| `book` | object | `{name, author, bookUrl, …}`（`Bindings.Book`） |
| `chapter` | object | `{title, url, …}`（`Bindings.Chapter`） |
| `source` | object | `get/set/getVariable/setVariable/getKey/put/getLoginInfo…` |
| `book` 的变量 | object | `book.getVariable()/put(k,v)/get(k)` |
| `chapter` 的变量 | object | `chapter.put(k,v)/get(k)` |
| `cookie` | object | `getCookie/setCookie/replaceCookie/removeCookie` |
| `cache` | object | `put(k,v,ttl)/get(k)/delete(k)`（进程内 TTL） |
| `java` | object | `ajax/ajaxAll/connect/get/post/head`、`putVar/getVar`、`put/getFromCache`、`base64*/md5*/sha1/sha256/hex*/aes*/encodeURI/randomUUID/timeFormat` |
| `console` | object | `log/info/warn/error`（`VERBOSE_JS=1` 时输出到服务日志） |

差异与未实现项见 [compatibility.md](compatibility.md#对齐情况总览)。

## 6. 并发与资源

- `Source` 内部按 `runMu` 串行化 JS 执行，**方法可并发调用**，不会交叉污染 `book/chapter` 上下文。
- 每个实例一个 goja 运行时；实例很多时注意内存，建议配合 `registry.MaxInstances`。
- 想跨实例共享 cookie，把同一个 `*netx.Client` 塞进 `Options.Client`。
- 缓存默认是实例内存缓存（`MemCache`，带 TTL 与后台 `Sweep`）；多副本部署时接 Redis 实现 `Cache`。

## 7. 完整可编译示例

见 `examples/library_usage.go.txt`（复制到你的包里改成 `package main` 即可运行；
其中书源指向 `tools/fixtureserver`，先 `go run ./tools/fixtureserver` 起假站点）。
