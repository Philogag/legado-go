# 排错速查（troubleshooting.md）

按**症状**查。每条给：可能原因 → 怎么确认 → 怎么处置。

## 启动与连通

### `listen failed: listen tcp :8080: bind: address already in use`
另一个进程占了端口。`ss -lntp | grep 8080` 找到它，或换 `-addr :18080`。

### 请求连不上 / `connection refused`
服务没起来或只监听在 `127.0.0.1`。检查 `journalctl -u legado-booksource -n 50`，
确认 `listening on ...` 日志行；容器里 `-addr` 必须是 `0.0.0.0:8080` 或 `:8080`。

### `401` + `{"error":{"kind":"UNAUTHORIZED"}}`
启用了 `API_TOKEN` 但请求没带令牌。三种写法任选：

```bash
curl -H "Authorization: Bearer $API_TOKEN" ...
curl -H "X-API-Token: $API_TOKEN" ...
```

只接受请求头携带令牌（**不支持** `?token=` 查询参数，避免令牌进入访问日志）。

`/healthz`、`/readyz` 豁免鉴权，`/mcp` 不免。

### `429 rate limited`
超过 `RATE_LIMIT`（每分钟每 IP）。反向代理后若要按真实客户端限流，
**必须**让 nginx 传 `X-Forwarded-For`，否则所有请求共用一个桶。

## 实例与 TTL

### `404 NOT_FOUND: instance xxx not found`
实例从来没建过，或**墓碑已被清理**（删除/回收超过 `GRACE_PERIOD`）。
处置：重新 `POST /v1/instances`。客户端应把 `404` 当成"重建"信号。

### `410 EXPIRED: instance xxx 已过期`
`now - lastUsed > TTL`，实例被回收了（墓碑期内还会返回这个而不是 404）。原因通常是：

| 原因 | 确认 | 处置 |
| --- | --- | --- |
| 客户端两次调用间隔 > TTL | 看响应 `ttlSeconds` 与调用时间差 | 提高 `ttl`（创建时传 `ttl`/`DEFAULT_TTL`）或每次调用前 `touch` |
| 只调了"不续期"接口 | 只用了 `GET /v1/instances`、`source_list`、`source_get` | 改用实际业务接口，或显式 `touch` |
| 超过 `MAX_TTL` 绝对寿命 | 服务端配了 `-max-ttl`（默认 6h），实例活满即回收 | 重建；确需长活则调大 `MAX_TTL` |
| 服务重启 | 日志有 `listening on` | 实例是内存状态，重启即丢；客户端按 404 重建 |

### `429 INSTANCE_LIMIT: instance limit 64 reached`
超过 `MAX_INSTANCES`。要么调大，要么检查**是不是漏删**（`GET /v1/instances` 看列表，
很多 `remainingSeconds` 很大说明客户端没设 TTL）。也可以调小 `DEFAULT_TTL`。

### 实例被回收但内存没降
`MAX_INSTANCES` 只限数量不限内存；大页面缓存会占内存。看 `CACHE_TTL` 是否过长，
并用 `stats.evicted` 确认回收在跑（`GET /healthz`）。

## 书源配置

### `400 INVALID_CONFIG: 书源已禁用`
配置里 `enabled: false`。改成 `true` 或删掉该字段。

### `400 INVALID_CONFIG: bookSourceUrl 不能为空`
书源缺 `bookSourceUrl`。它同时是相对 URL 的基准，必须给。

### 校验通过但搜索返回空 `books: []`
按顺序排查：

1. **站点返回的是登录页/验证页**：`source_eval` 里 `java.ajax(searchUrl)` 看原始 HTML。
2. **列表选择器不对**：`bookList` 必须选出**多个**元素。用 `source_eval` 试
   `java.ajax(baseUrl + '/search?q=xxx')` + 手工看结构，或把 `bookList` 换成 `@js:` 打印元素数。
3. **`checkKeyWord` 过滤掉了**：结果的 `name` 不含关键字。确认该字段规则取到的是书名（不是整块文本）。
4. **相对 URL 没绝对化**：本实现会把 `bookUrl`/`coverUrl` 相对路径按**请求 URL** 绝对化；
   若书源返回的是 `//cdn/...` 也支持。若书源给的是裸 ID，规则里要用 `@js:` 拼。

### `422 PARSE_EMPTY: 规则没取到内容`
规则命中但结果为空。常见原因：

- 选择器用了上游 JSoup 支持、但 Go 侧不支持的写法（见 [compatibility.md](compatibility.md)）；
- `##` 正则替换把内容替换成空（替换串写错）；
- XPath 返回了属性节点而非文本（需要显式取 `text()`）。

### `422 PARSE_FAILED: rule error: ...`
规则本身报错（JS 抛异常、正则不合法、JSONPath 语法错）。
`source_eval` 跑同一段 JS 可复现真实堆栈；JS 异常信息会带行号。

## 抓取

### `502 HTTP: status 403`
站点反爬。处置：补 `header`（UA/Referer/Cookie）、降低频率，
或改用 `java.ajax` 带上完整头。**不要**指望本服务绕过验证码（不支持）。

### `504 TIMEOUT`
`HTTP_TIMEOUT` 太短或站点太慢。调大 `-http-timeout`；`nextContentUrl` 多页合并更耗时，
注意 nginx `proxy_read_timeout` 也要放宽（默认 60s 常见踩坑点）。

### `502 NETWORK: dial tcp: lookup ... no such host`
DNS/网络不通。容器里注意 DNS 配置；书源域名可能被墙或有区域限制。

### 章节正文只有第一页
`ruleContent.nextContentUrl` 为空或匹配不到。用 `source_eval` 验证该规则在**摘要 URL** 上能否取到链接；
注意多页合并上限 20 页，且会按 URL 去重（同页重复会提前停止）。

### 正文里混入导航/广告
用 `replaceRegex` 清洗（`##正则##替换`），或把 `content` 规则收窄到正文容器。
`FormatContent` 只处理零宽字符、空行与缩进，不识别广告。

## JS 与规则调试

### `TypeError: Object has no member 'xxx' at ...`
用了未实现的 `java.*` 方法。自查 [compatibility.md](compatibility.md#对齐情况总览) 的能力表；
可用 `Object.keys(java).join(',')` 打印支持清单。

### JS 里 `java.ajax` 返回空串
1. 站点失败但没抛错（非 2xx 会原样返回）；用 `java.connect(url).body` 看状态码。
2. URL 缺协议：`java.ajax` 不会自动补 `bookSourceUrl`，需要用 `baseUrl + '/path'`。
3. 需要头/Cookie：`java.ajax(url, {headers:{...}})` 或先 `cookie.setCookie`。

### `console.log` 没输出
服务默认不打印书源日志。用 `VERBOSE_JS=1` 启动；或在 `POST /v1/instances/{id}/eval` 里直接把值返回。

### 变量取不到（`{{myVar}}` 是空）
1. `variable` 字段格式：JSON 对象或 `k=v` 行；确认 `source_get`/`source.get('myVar')` 能取到。
2. 变量赋值要用 `@put:{k:v}` 或 `source.put(k,v)`；`java.put` 是**宿主级**变量（跨实例共享，少用）。
3. 变量名大小写敏感。

## MCP

### `-32600 invalid Accept header`
请求带了 `Accept` 但不含 `application/json`。去掉该头或设为 `application/json, */*`。

### `-32600 unsupported protocol version`
`Mcp-Protocol-Version` 头写了别的版本。本服务只支持 `2026-07-28`，去掉该头即可。

### `tools/call` 返回 `isError: true`
工具的 `structuredContent` 里带 `error.kind`，对照 [rest-api.md#错误格式](rest-api.md#错误格式) 处置。
常见：`EXPIRED`（实例过期，重建设）、`INVALID_CONFIG`（传给 `source_create` 的 JSON 不合法）。

### 客户端一直在等 `initialize` 的响应流
本服务无状态、纯 JSON、没有 SSE。若客户端坚持先开 GET 流，请改用支持 streamable-http 无状态模式的客户端配置。

## 性能

### 单次搜索很慢（>5s）
1. 站点本身慢：`curl -o /dev/null -w '%{time_total}'` 直接测站点；
2. 规则里有多余的 `java.ajax`（每个结果项都请求一次详情）——改成详情按需拉；
3. `nextTocUrl` 翻了很多页：目录页数多时会串行抓取，考虑只取前 N 页。

### 内存持续增长
`GET /healthz` 看 `instances` 是否持续增长（漏删），或 `MAX_INSTANCES` 过大。
`CACHE_TTL` 过长也会让页面缓存堆积。

### 大量 429 但限流没开
`429` 也可能来自**目标站点**，会以 `502 HTTP: status 429` 出现（注意 kind 是 `HTTP` 不是 `INSTANCE_LIMIT`）。
处置：降低并发、加大 `HTTP_TIMEOUT`、给书源加 `header`。

## 还是解决不了

1. `VERBOSE_JS=1 QUIET=0` 重启，复现一次；
2. `source_eval` 逐步缩小到最小失败规则；
3. 对照 `internal/*/..._test.go` 里的等价用例（测试就是行为文档）；
4. 若怀疑是上游语义差异，查 [compatibility.md](compatibility.md) 的差异清单。
