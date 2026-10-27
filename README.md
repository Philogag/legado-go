# legado-go

[![Docker](https://github.com/Philogag/legado-go/actions/workflows/docker-publish.yml/badge.svg)](https://github.com/Philogag/legado-go/actions/workflows/docker-publish.yml)
[![ghcr](https://img.shields.io/badge/ghcr.io-philogag%2Flegado--go-blue)](https://github.com/Philogag/legado-go/pkgs/container/legado-go)

把 [legado](https://github.com/gedoor/legado) 的**书源调用引擎**从 Android 应用里拆解出来，
重写为一个独立的 Go 库 + HTTP 微服务 + MCP 服务端。

- **库**：给一份书源配置（JSON），得到一个可调用实例，然后 `Search / BookInfo / ChapterList / Content / Explore / EvalJS`。
- **微服务**：`POST /api/v1/instances` 创建实例并返回 `id`，后续所有调用只带 `id`；实例按 **TTL** 自动回收（滑动空闲 + 绝对上限 + 墓碑）。
- **MCP**：同一进程内提供 `POST /mcp`（无状态，协议版本 `2026-07-28`），把同一套能力暴露为 12 个 tool，可直接接入 Claude / ChatGPT / 各类 Agent。

> 上游 `upstream/legado/` 只作为**语义参考**，本仓库不包含任何 Kotlin 代码，也不依赖 JVM。

---

## 1. 为什么重写而不是复用

上游引擎深度绑定 Android：`okhttp` + `CookieManager` + `android.webkit.WebView` + Kotlin 协程 + 全局 `CacheManager`，
且 JS 规则跑在 QuickJS（`jsLib` 里塞了 Maven 解析、AES、GM 脚本等大量安卓库）。

本仓库保留**规则语义**（书源 JSON 格式、`@js:` / `##` / `&&` / `||` / `{{}}` 模板、变量、分页、字数统计等），
替换掉**运行时基础设施**：

| 上游依赖 | 本仓库替代 | 说明 |
| --- | --- | --- |
| OkHttp | `net/http` + `netx` | 连接池、超时、重定向、代理、字符集嗅探（GBK/Big5 等） |
| Jsoup / `AnalyzeByJSoup` | `goquery` + `cascadia` | `class.a.0@text`、`@href`、`@html`、`@textNodes`、索引、`!` 排除、`-` 倒序 |
| `AnalyzeByXPath` | `antchfx/htmlquery` / `antchfx/xpath` | 支持 `//`、`@attr`、`text()`、索引 |
| `AnalyzeByJSonPath` | `github.com/ohler55/ojg/jp` | `$.a.b`、`$..c`、`$[?(@.x>1)]` |
| QuickJS + jsLib | `goja` | 书源脚本 API：`java.*`、`cookie.*`、`cache.*`、`source.*`、`book.*`、`chapter.*`、`base64/md5/aes/digest` 等 |
| `WebView` 登录/发现页 | **未实现**（降级为普通 HTTP） | 见「已知差异」 |

---

## 2. 架构

```
cmd/server            HTTP 服务入口（echo）
  ├── internal/httpapi     REST 路由 / 校验 / 错误映射 / 鉴权 / 限流
  ├── internal/mcp         MCP 2026-07-28 无状态服务端（JSON-RPC over HTTP）
  └── internal/registry    实例注册表：Create / Get / Touch / List / Sweep（TTL）
        └── internal/booksource   书源实例：把配置 + 规则串成一条调用管线
              ├── internal/model        配置归一化（header/variable/exploreUrl/默认值）
              ├── internal/analyze      规则解析器（JSoup / XPath / JSONPath / 正则）
              ├── internal/rules        规则切分（`##` `&&` `||` `@js:` `{{}}`、PUT/GET 变量）
              ├── internal/analyzeurl   URL 规则（`url,{"method":"POST","body":"..."}` / `{{page}}`）
              ├── internal/netx         HTTP 客户端 + CookieJar + 字符集 + 缓存
              └── internal/jsengine     goja 沙箱 + 书源 JS API
```

数据流（以「正文」为例）：

```
chapterUrl ──analyzeurl──> 真实 URL + 方法/头/体
   └──netx.Fetch──> 响应体（自动 charset）
        └──analyze.Rule(#content p)──> 文本
             ├── nextContentUrl 循环（翻页拼接）
             ├── replaceRegex / @js: 后处理
             └── formatContent（去广告行、压缩空行、全角转半角）
```

---

## 3. 作为库使用

```go
package main

import (
	"fmt"

	"legado-go/internal/booksource"
	"legado-go/internal/model"
)

func main() {
	cfg, err := model.ParseConfig([]byte(`{
	  "bookSourceName": "示例源",
	  "bookSourceUrl": "https://example.com",
	  "searchUrl": "/search?q={{key}}&page={{page}}",
	  "ruleSearch": {"bookList": ".result .item", "name": "a@text", "author": ".author@text", "bookUrl": "a@href"},
	  "ruleBookInfo": {"name": "h1@text", "author": ".author@text", "tocUrl": "a.toc@href"},
	  "ruleToc": {"chapterList": "ul.chapters > li", "chapterName": "a@text", "chapterUrl": "a@href"},
	  "ruleContent": {"content": "#content p"}
	}`))
	if err != nil {
		panic(err)
	}

	src, err := booksource.New(cfg, booksource.Options{}) // 也可 booksource.NewFromJSON(raw, opts)
	if err != nil {
		panic(err)
	}
	defer src.Close()

	books, err := src.Search("关键词", 1)
	if err != nil {
		panic(err)
	}
	book, err := src.GetBookInfo(books[0])
	if err != nil {
		panic(err)
	}
	chapters, err := src.GetChapterList(book)
	if err != nil {
		panic(err)
	}
	content, err := src.GetContent(book, chapters[0])
	if err != nil {
		panic(err)
	}
	fmt.Println(content.Title, len(content.Content))
}
```

`booksource.Options` 可注入 HTTP 超时、UA、代理、缓存、是否允许 WebView 类规则等；
`Source` 暴露 `EvalJS / Variables / SourceGet / PutVar / Config / Name / URL / Close`，便于把状态在多次调用间保留（变量、Cookie）。

---

## 4. 作为微服务使用

```bash
go build -o legado-server ./cmd/server
ADDR=:8080 DEFAULT_TTL=30m MAX_INSTANCES=64 ./legado-server
```

或用容器（多架构镜像 `linux/amd64,linux/arm64`，distroless + 非 root，约 15 MB）：

```bash
docker compose up -d --build                                   # 本地构建并启动
# 或拉已发布镜像
docker run --rm -p 8080:8080 ghcr.io/philogag/legado-go:latest
curl -s localhost:8080/healthz; echo
```

### 4.1 实例生命周期（TTL）

实例有三种「死法」，语义对齐通用 TTL 缓存：

| 机制 | 触发 | 行为 |
| --- | --- | --- |
| 滑动空闲 TTL | `now - lastUsed > ttl` | 回收；之后访问返回 `410 EXPIRED` |
| 绝对上限 `MAX_TTL` | `now - created > maxTTL` | 回收（即使一直在用），防止无限续期 |
| 手动删除 | `DELETE /instances/:id` | 立即关闭，之后返回 `404 NOT_FOUND` |

- 每次**成功取用**（`GET /instances/:id`、任意业务调用）都会刷新 `lastUsed`；
- 后台 `SWEEP_INTERVAL`（默认 5s）扫描回收，回收后留下**墓碑**（默认 10 分钟），
  所以「刚过期」返回 `410 Gone`，「从没存在过」返回 `404`，客户端可据此区分「重试（重建实例）」与「参数错了」；
- `POST /instances/:id/touch` 可显式续期（也可改 TTL）；
- `MAX_INSTANCES` 限制并发实例数，超出返回 `429 LIMIT`。

### 4.2 REST API

| 方法 | 路径 | 说明 |
| --- | --- | --- |
| GET | `/healthz`, `/readyz` | 健康检查（含 `stats`） |
| GET | `/api/v1/meta` | 版本、端点清单、默认 TTL |
| POST | `/api/v1/sources/validate` | 只校验配置，不建实例 |
| POST | `/api/v1/instances` | 建实例：`{"config":{...},"ttl":"10m","name":"x"}` → `201 {instance}` |
| GET | `/api/v1/instances` | 列出存活实例 + 统计 |
| GET | `/api/v1/instances/:id` | 实例详情（同时续期） |
| DELETE | `/api/v1/instances/:id` | 删除实例 |
| POST | `/api/v1/instances/:id/touch` | 续期 / 改 TTL |
| POST | `/api/v1/instances/:id/search` | `{"key":"x","page":1}` → `{books:[...], count}` |
| POST | `/api/v1/instances/:id/explore` | `{"url":"分类::/list/1","page":2}`（省略 url 用第一个入口） |
| POST | `/api/v1/instances/:id/bookinfo` | `{"book":{...}}` → 补全详情 |
| POST | `/api/v1/instances/:id/chapters` | `{"book":{...}}` → 章节列表 |
| POST | `/api/v1/instances/:id/content` | `{"book":{...},"chapter":{...}}` → 正文（含翻页拼接/格式清理） |
| POST | `/api/v1/instances/:id/eval` | `{"script":"key","key":"x"}` 直接跑书源 JS |

错误统一为 `{"error":{"kind":"EXPIRED","message":"..."}}`，`kind` ∈
`INVALID_CONFIG`(400) / `NETWORK`,`HTTP`(502) / `TIMEOUT`(504) / `PARSE_FAILED`,`PARSE_EMPTY`(422) /
`NOT_FOUND`(404) / `EXPIRED`,`CLOSED`(410) / `INSTANCE_LIMIT`,`RATE_LIMITED`(429) / `INTERNAL`(500)。

```bash
# 建实例
ID=$(curl -s -X POST localhost:8080/api/v1/instances \
  -H 'Content-Type: application/json' \
  -d "{\"config\":$(cat examples/book-source.json),\"ttl\":\"10m\"}" | jq -r .instance.id)

# 搜索
curl -s -X POST localhost:8080/api/v1/instances/$ID/search \
  -H 'Content-Type: application/json' -d '{"key":"测试","page":1}' | jq '.books[] | {name, author}'
```

鉴权：设置 `API_TOKEN` 后，`/api/v1/*` 需带 `Authorization: Bearer <token>`（或 `X-API-Token`）；
`/healthz`、`/readyz` 与 `/mcp` 不鉴权，MCP 走独立部署或网关鉴权。
限流：`RATE_LIMIT`（每 IP 每分钟请求数，默认 0 关闭）在 /api/v1/* 上生效，超限返回 `429 RATE_LIMITED`。

---

### 4.3 容器与 CI/CD

| 文件 | 作用 |
| --- | --- |
| `Dockerfile` | 多阶段构建（`golang:1.26-alpine` → `distroless/static:nonroot`）；`--platform=$BUILDPLATFORM` 交叉编译；内置 `HEALTHCHECK` |
| `docker-compose.yml` | 只读根文件系统、`cap_drop: ALL`、资源上限、日志轮转、`.env` 注入 |
| `.env.example` | 全部可调参数模板（含 `API_TOKEN`） |
| `.github/workflows/docker-publish.yml` | `go vet` + `go test -race` 门禁 → 用 [`push-to-ghcr`](https://github.com/marketplace/actions/push-to-ghcr) 多架构推送到 ghcr.io |

镜像标签：`main`/`master` → `latest` + `sha-<7位>`；tag `v1.2.3` → `1.2.3` + `latest`；release → `1.2.3`；PR → 只构建并 curl `/healthz` `/readyz` `/mcp` 冒烟，不推送。

本地等价命令：`make docker-build` / `make docker-multi` / `make docker-smoke` / `make compose-up`。
完整说明见 [docs/deployment.md §4](docs/deployment.md#4-容器)。

## 5. MCP（无状态，协议 `2026-07-28`）
端点：`POST /mcp`，`Content-Type: application/json`，单次 JSON-RPC 消息，**无 `initialize`、无 `Mcp-Session-Id`**，
每个请求自包含（详见 `docs/mcp-2026-07-28.md`）。能力通过 header 声明：

```
MCP-Protocol-Version: 2026-07-28
Mcp-Method: tools/call
Mcp-Name: source_search
```

12 个 tool：

| Tool | 作用 |
| --- | --- |
| `source_validate` | 校验书源 JSON（返回名称/分组/发现入口数） |
| `source_create` | 建实例（`ttlSeconds` 可指定）→ 返回 `instanceId` |
| `source_list` / `source_get` / `source_touch` / `source_delete` | 实例管理 |
| `source_search` | 搜索：`{instanceId, key, page}` |
| `source_explore` | 发现页：`{instanceId, url?, page}` |
| `source_book_info` | 详情：`{instanceId, book}` |
| `source_chapters` | 目录：`{instanceId, book}` |
| `source_content` | 正文：`{instanceId, book, chapter}` |
| `source_eval` | 跑书源 JS：`{instanceId, script, key?, page?}` |

`tools/call` 同时返回 `content[].text`（人类可读 JSON）与 `structuredContent`（机器可读），
`errors` 用 JSON-RPC 错误码（`-32601` 未知方法、`-32602` 参数非法）。

接入示例（Claude Code / 任意 MCP 客户端，走 HTTP）：

```json
{
  "mcpServers": {
    "legado": {
      "type": "http",
      "url": "http://127.0.0.1:8080/mcp",
      "headers": { "Authorization": "Bearer $API_TOKEN" }
    }
  }
}
```

---

## 6. 规则支持矩阵

| 能力 | 支持情况 |
| --- | --- |
| 选择器 | `class@attr`、`@text`、`@ownText`、`@html`、`@textNodes`、`@href`、`@src`、`tag.class`、`tag[attr=val]`、`.a.b`、`#id`、`索引`、`!排除`、`-倒序` |
| 多规则组合 | `&&`（顺序取）、`||`（取第一个非空）、`%%`（正则）、`##正则##替换`、`##@js:` |
| 变量/模板 | `{{key}}`、`{{page}}`、`{{book.name}}`、`$.变量`（JSONPath 取值）、`@put:{k:v}`、`@get:{k}` |
| 语义要点 | `{{js}}` / `@get:{k}` 这类纯插值规则在字符串模式下直接返回展开后的值（对应上游 `Mode.Regex → else -> rule`）；元素/对象模式才用正则抽取 |
| JS | `@js:` 前缀、`##@js:`、`<js>...</js>`、`java.ajax/get/post/head/connect/ajaxAll`、`java.putVar/getVar`、`java.put/getFromCache`、`java.base64Encode/base64Decode`、`java.md5Encode/md5Encode16/sha1Encode/sha256Encode`、`java.hexEncodeToString/hexDecodeToString`、`java.aesEncodeToString/aesDecodeToString`、`java.encodeURI/decodeURI`、`java.randomUUID/timeFormat`、`source.*`、`book.*`、`chapter.*`、`cookie.*`、`cache.*`、`console.log` |
| 请求 | GET/POST/PUT、`headers`、`body`、`charset`、`retry`、`type`（json/html）、`webJs`（降级）、`sourceRegex`（降级） |
| 内容处理 | `nextContentUrl` 翻页拼接、`replaceRegex`、`formatContent`（去广告/空行/全角半角）、`style` 类型（CSS 样式还原为文本） |

参考实现：`internal/rules`（切分）、`internal/analyze`（解析器）、`internal/jsengine`（JS API）。

---

## 7. 配置项

| 环境变量 / flag | 默认 | 说明 |
| --- | --- | --- |
| `ADDR` / `-addr` | `:8080` | 监听地址 |
| `DEFAULT_TTL` / `-ttl` | `30m` | 未指定 TTL 时的默认空闲 TTL |
| `MAX_TTL` / `-max-ttl` | `6h` | 实例绝对上限（0 不限） |
| `MAX_INSTANCES` / `-max-instances` | `64` | 实例上限（0 不限） |
| `SWEEP_INTERVAL` / `-sweep` | `5s` | 过期扫描间隔 |
| `GRACE_PERIOD` / `-grace` | `10m` | 墓碑保留时长（期间返回 410） |
| `HTTP_TIMEOUT` / `-http-timeout` | `30s` | 单次抓取超时 |
| `CACHE_TTL` / `-cache-ttl` | `1h` | 书源缓存（`cache.*`、页面缓存）TTL |
| `API_TOKEN` / `-token` | 空 | 非空则启用 Bearer 鉴权 |
| `RATE_LIMIT` / `-rate-limit` | `0` | 每 IP 每分钟请求上限（0 关闭） |
| `VERBOSE_JS` / `-verbose-js` | `0` | 打印书源 JS 的 `console.log` |
| `QUIET` / `-quiet` | `1` | 关闭访问日志 |

---

## 8. 测试与冒烟

```bash
go vet ./...
go test ./...                 # 单元测试：规则切分/解析器/URL/规则引擎/实例/HTTP/MCP
./scripts/smoke.sh            # 起本地假书站 + 服务，跑 REST 全流程、MCP 全流程、TTL 回收
```

`tools/fixtureserver` 是一个可独立运行的假书站（搜索/详情/目录/正文/翻页），
`examples/book-source.json` 是指向它的书源配置，可用于联调。

---

## 9. 已知差异与安全提示

**差异**（相对上游 legado）：

1. 未实现 `WebView` 类规则（`webView:true`、`webJs`、`sourceRegex` 的浏览器执行路径）：会走普通 HTTP 并记录告警；
2. `jsLib` 只提供 Java 兼容层（`Packages.java.*` 常用类），不含上游 Maven 解析、GM 脚本、图片处理等；
3. 音频/图片/漫画书源（`bookSourceType != 0`）未做专门处理，正文按文本返回；
4. Cookie 为进程内 `CookieJar`（支持 `loginUrl` 的纯 HTTP 登录，不支持交互式验证码）。

**安全**：

- 书源 JS 在 `goja` 沙箱里跑，**没有**文件系统/网络宿主 API，只能通过 `java.ajax` 等白名单方法访问网络；
  但仍建议只加载可信书源，并给服务加 `API_TOKEN` + 反代限流；
- 抓取目标为不可信站点：`netx` 默认限制响应体大小、禁止 `file://` 协议、限制重定向次数；
- 生产部署建议：`MAX_INSTANCES` + `MAX_TTL` 一起收紧，避免被滥用为通用抓取代理。

---

## 10. 文档

| 文档 | 内容 |
| --- | --- |
| [docs/README.md](docs/README.md) | **文档总目录**（推荐入口） |
| [docs/quickstart.md](docs/quickstart.md) | 5 分钟跑通：构建、起假书站、curl、MCP |
| [docs/configuration.md](docs/configuration.md) | 书源 JSON 全字段 + 服务端参数/环境变量 |
| [docs/rules.md](docs/rules.md) | 规则语法参考（含示例与调试技巧） |
| [docs/library.md](docs/library.md) | 当库用：`booksource` / `registry` 公开 API |
| [docs/rest-api.md](docs/rest-api.md) | REST 端点、请求/响应、错误码 |
| [docs/mcp.md](docs/mcp.md) | MCP 接入（12 个工具、会话示例、安全） |
| [docs/ttl.md](docs/ttl.md) | TTL 实例生命周期、并发与时钟细节 |
| [docs/architecture.md](docs/architecture.md) | 分层架构、请求流程、关键设计决策 |
| [docs/compatibility.md](docs/compatibility.md) | 与 upstream/legado 的逐项差异（含未实现清单） |
| [docs/deployment.md](docs/deployment.md) | 部署与运维（容器、反代、调参、备份） |
| [docs/troubleshooting.md](docs/troubleshooting.md) | 症状 → 原因 → 处置 速查 |
| [docs/testing.md](docs/testing.md) | 测试矩阵与如何自己加测试 |
| [docs/mcp-2026-07-28.md](docs/mcp-2026-07-28.md) | MCP 协议摘要与实现映射（规范备忘） |

示例：
- `examples/book-source.json` — 最小可用的书源配置
- `examples/library_usage.go.txt` — 完整可跑的库用法（7 步：实例 → 搜索 → 详情 → 目录 → 正文 → JS → TTL）
- `scripts/smoke.sh` — 端到端冒烟（起服务、REST 全流程、TTL 到期、MCP 全流程）

目录一览

```
cmd/server            服务入口
internal/httpapi      REST（echo）路由、鉴权、错误映射
internal/mcp          MCP 2026-07-28 无状态服务端（12 tools）
internal/registry     TTL 实例注册表（滑动过期 / 绝对上限 / 墓碑）
internal/booksource   书源实例与调用管线（Search/Explore/BookInfo/Chapters/Content/EvalJS）
internal/model        配置模型与归一化
internal/analyze      规则解析（JSoup/XPath/JSONPath/正则）
internal/analyzeurl   URL 规则解析（选项块 / 模板变量）
internal/rules        规则切分与 JS 结果转换
internal/netx         HTTP 客户端、CookieJar、缓存、字符集
internal/jsengine     goja 沙箱与书源 JS API
internal/jsonpath     JSONPath 引擎封装
docs/                 文档（从 docs/README.md 进）
examples/             示例书源配置 + 库用法示例
scripts/smoke.sh      端到端冒烟
tools/fixtureserver   本地假书站
```
