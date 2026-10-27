# REST API（rest-api.md）

Base URL：`http://localhost:8080`（`-addr` 可改）。
所有 POST 都接受 JSON 请求体；`Content-Type: application/json`。

- [`GET /healthz`、`/readyz`](#健康检查)
- [`GET /api/v1/meta`](#元信息)
- [`POST /api/v1/sources/validate`](#校验书源配置)
- [`POST /api/v1/instances`](#创建实例)
- [`GET /api/v1/instances`](#列出实例)
- [`GET /api/v1/instances/{id}`](#查询单个实例)
- [`POST /api/v1/instances/{id}/touch`](#续期)
- [`DELETE /api/v1/instances/{id}`](#删除实例)
- [`POST /api/v1/instances/{id}/search`](#搜索)
- [`POST /api/v1/instances/{id}/explore`](#发现)
- [`POST /api/v1/instances/{id}/bookinfo`](#详情)
- [`POST /api/v1/instances/{id}/chapters`](#目录)
- [`POST /api/v1/instances/{id}/content`](#正文)
- [`POST /api/v1/instances/{id}/eval`](#执行-js)
- [`POST /mcp`](#mcp)（见 [mcp.md](mcp.md)）
- [错误格式与错误码](#错误格式)

> 除 `GET/POST /api/v1/instances*` 中明确标注的“不续期”接口外，**每次成功调用都会刷新实例的 lastUsed**（滑动空闲 TTL）。

## 鉴权

启动时 `-token` / `API_TOKEN` 非空则启用：

```
Authorization: Bearer <token>
X-API-Token: <token>
```

`/api/v1/*` 与 `/mcp` 都受保护；`/healthz`、`/readyz` 不鉴权（给探针用）。失败返回 `401`。

## 限流

`-rate-limit N`（每分钟每 IP）> 0 时启用；超限返回 `429`，带 `Retry-After` 头。

---

## 健康检查

```http
GET /healthz
GET /readyz
```

```json
{"status":"ok","stats":{"instances":1,"sweeps":12,"evicted":0,"created":1},"time":"2025-01-01T00:00:00Z"}
```

## 元信息

```http
GET /api/v1/meta
```

```json
{
  "name": "legado-booksource",
  "version": "1.0.0",
  "mcpProtocol": "2026-07-28",
  "defaultTTLSec": 1800,
  "instanceCount": 0,
  "endpoints": ["POST /api/v1/instances", "..."]
}
```

> `endpoints` 里的前缀是 `api/v1`，实际路由不带 `api`（历史遗留文案），请以本文档为准。

## 校验书源配置

```http
POST /api/v1/sources/validate
```

请求体同创建实例（`{"config": {...}}` 或裸书源对象）：

```json
{"valid":true,"name":"本地假书源","url":"http://127.0.0.1:18081","group":"示例","type":0,
 "hasSearch":true,"exploreEntries":[{"title":"全部","url":"/list?page={{page}}"}],"variables":1}
```

失败 `400` + `INVALID_CONFIG`。

## 创建实例

```http
POST /api/v1/instances
```

| 字段 | 类型 | 说明 |
| --- | --- | --- |
| `config` | object/array | **必填**。书源 JSON；也接受阅读 App 导出的数组（取第一个） |
| `id` | string | 可选，自定义 ID（`[A-Za-z0-9._-]{1,64}`）。重复则 `400`，删除后可用同 ID 重建 |
| `ttl` | string/number | `"10m"`、`"90s"`、`600`（秒）；缺省用服务端 `DEFAULT_TTL`，上限受 `MAX_TTL` 封顶 |
| `ttlSeconds` | number | 与 `ttl` 二选一（`ttl` 优先） |
| `name` | string | 覆盖 `bookSourceName` |
| `owner` | string | 调用方标识，仅用于排查 |

也可以直接 POST 一个书源对象（不带 `config` 包装）：

```bash
curl -X POST localhost:8080/api/v1/instances -H 'Content-Type: application/json' \
     -d @examples/book-source.json
```

响应 `201`：

```json
{
  "instance": {
    "id": "src-1f0a...",
    "name": "本地假书源",
    "url": "http://127.0.0.1:18081",
    "group": "示例",
    "owner": "cli",
    "createdAt": "2025-01-01T00:00:00Z",
    "lastUsed": "2025-01-01T00:00:00Z",
    "ttlSeconds": 600,
    "remainingSeconds": 600,
    "expiresAt": "2025-01-01T00:10:00Z"
  }
}
```

超过 `MAX_INSTANCES` 返回 `429 INSTANCE_LIMIT`。

## 列出实例

```http
GET /api/v1/instances            # 不续期
```

```json
{"instances":[{"id":"src-1f0a...","name":"本地假书源","ttlSeconds":600,"remainingSeconds":598,"expiresAt":"..."}],
 "stats":{"instances":1,"sweeps":3,"evicted":0,"created":1}}
```

## 查询单个实例

```http
GET /api/v1/instances/{id}       # 不续期（只读取）
```

```json
{
  "instance": {"id":"src-1f0a...","name":"本地假书源","remainingSeconds":598,"expiresAt":"..."},
  "source": {
    "name":"本地假书源","url":"http://127.0.0.1:18081","group":"示例","type":0,
    "exploreEntries":[{"title":"全部","url":"/list?page={{page}}"}],
    "variables":{"site":"fixture"}
  }
}
```

## 续期

```http
POST /api/v1/instances/{id}/touch
```

```json
{"ttl":"30m"}        // 可选：顺带改 TTL
```

```json
{"instance":{"id":"src-1f0a...","ttlSeconds":1800,"remainingSeconds":1800,"expiresAt":"..."}}
```

## 删除实例

```http
DELETE /api/v1/instances/{id}
```

`204 No Content`。删除后进入墓碑期（`GRACE_PERIOD`），期内再访问返回 `410 EXPIRED`。

## 搜索

```http
POST /api/v1/instances/{id}/search
```

```json
{"key":"剑来","page":1}
```

```json
{
  "books":[{"name":"剑来","author":"烽火戏诸侯","kind":"玄幻","intro":"...","coverUrl":"http://...",
            "bookUrl":"http://site/book/1","tocUrl":"","lastChapter":"...","originName":"本地假书源"}],
  "count":1,
  "elapsedMs":123
}
```

`key` 为空返回 `400`；规则没配 `searchUrl` 返回 `400 INVALID_CONFIG`。
结果会按 `ruleSearch.checkKeyWord` 过滤，并缓存到 `CACHE_TTL`。

## 发现

```http
POST /api/v1/instances/{id}/explore
```

```json
{"url":"/list/xuanhuan?page={{page}}","page":1}
```

`url` 留空时用 `exploreUrl` 的第一个入口；响应结构同搜索。

## 详情

```http
POST /api/v1/instances/{id}/bookinfo
```

```json
{"bookUrl":"http://site/book/1","name":"剑来","author":"烽火戏诸侯"}
```

只给 `bookUrl` 就够；也可以直接传一个完整 `book` 对象：

```json
{"book":{"name":"剑来","author":"烽火戏诸侯","bookUrl":"http://site/book/1"}}
```

```json
{"book":{"name":"剑来","author":"烽火戏诸侯","intro":"...","coverUrl":"http://...",
         "tocUrl":"http://site/toc/1","lastChapter":"第1200章 ...","bookUrl":"http://site/book/1"},
 "elapsedMs":88}
```

## 目录

```http
POST /api/v1/instances/{id}/chapters
```

请求体同详情（`bookUrl` 或 `book`）。响应：

```json
{"chapters":[
   {"title":"第一章","url":"http://site/content/1","baseUrl":"http://site/toc/1","index":0,"bookUrl":"http://site/book/1"},
   {"title":"第二章","url":"http://site/content/2","baseUrl":"http://site/toc/1","index":1,"bookUrl":"http://site/book/1"}],
 "count":2,"elapsedMs":210}
```

自动跟随 `ruleToc.nextTocUrl` 翻页（最多 20 页，遇重复 URL 停止）。

## 正文

```http
POST /api/v1/instances/{id}/content
```

```json
{"chapterUrl":"http://site/content/1","title":"第一章","bookUrl":"http://site/book/1"}
```

也支持 `{"chapter":{"title":"第一章","url":"..."},"book":{...}}`。

```json
{"content":{"title":"第一章","content":"第一页第一段。\n\n第一页第二段 带空格。\n\n第二页内容…"},
 "elapsedMs":320}
```

- 自动跟随 `ruleContent.nextContentUrl` 合并多页（最多 20 页）；
- 应用 `ruleContent.replaceRegex` 与 `FormatContent`（去零宽字符、压缩空行、统一缩进）。

## 执行 JS

```http
POST /api/v1/instances/{id}/eval
```

调试规则的神器——它跑在**该实例的完整 JS 上下文**里（`java.*`、`source.*`、`book`、`chapter`、`result` 都可用）：

```json
{
  "code": "java.ajax(baseUrl + '/search?q=' + key)",
  "key": "剑来",
  "page": 1,
  "baseUrl": "http://127.0.0.1:18081",
  "result": "可选：注入 result"
}
```

```json
{"result":"<!doctype html>…"}
```

`script` 与 `code` 等价（兼容 MCP/旧调用方）。JS 抛错 → `422`（`PARSE_FAILED`）或 `500`。

## 错误格式

```json
{"error":{"kind":"EXPIRED","message":"instance src-1f0a expired"}}
```

| kind | HTTP | 含义 |
| --- | --- | --- |
| `INVALID_CONFIG` | 400 | 配置/参数不合法 |
| `NOT_FOUND` | 404 | 实例不存在（从未创建或墓碑已清理） |
| `EXPIRED` | 410 | 实例被 TTL 回收（墓碑期） |
| `CLOSED` | 410 | 实例已 `Close()` |
| `INSTANCE_LIMIT` | 429 | 超过 `MAX_INSTANCES` |
| `NETWORK` / `HTTP` | 502 | 站点不可达 / 非 2xx |
| `TIMEOUT` | 504 | 抓取超时 |
| `PARSE_FAILED` / `PARSE_EMPTY` | 422 | 规则报错 / 规则没取到内容 |
| `UNAUTHORIZED` | 401 | 令牌缺失或不匹配 |
| `RATE_LIMITED` | 429 | 触发 `RATE_LIMIT` 限流 |
| `INTERNAL` | 500 | 其它 |

## 完整 curl 流程

见 [quickstart.md](quickstart.md#4-调用功能) 与 `scripts/smoke.sh`（可直接跑的端到端脚本）。
