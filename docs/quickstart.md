# 快速开始

> 需要 Go 1.26+（`goja` 依赖新版运行时）。

## 1. 构建与启动

```bash
cd projects/legado-go
go build -o bin/legado ./cmd/server
DEFAULT_TTL=30m ./bin/legado -addr :8080
```

零依赖启动后健康检查：

```bash
curl -s localhost:8080/healthz
curl -s localhost:8080/api/v1/meta
```

## 2. 起一个本地假书源（可选）

仓库自带一个假书站，方便在没有真实书源时验证全链路：

```bash
go run ./tools/fixtureserver -addr 127.0.0.1:18081
```

它提供 `/search`、`/book/1`、`/toc/1`、`/content/1-1`、`/content/1-1-2`（正文第二页）等页面，
`examples/book-source.json` 就是针对它写的书源配置。

## 3. 创建书源实例

把书源 JSON 直接 POST 进来（`config` 可以是单个对象，也可以是阅读 App 导出的书源数组）：

```bash
curl -s -X POST localhost:8080/api/v1/instances \
  -H 'Content-Type: application/json' \
  -d '{
    "config": {
      "bookSourceUrl": "http://127.0.0.1:18081",
      "bookSourceName": "本地假书源",
      "searchUrl": "/search?key={{key}}&page={{page}}",
      "ruleSearch": {
        "bookList": ".result .item",
        "name": "h3@a@text",
        "author": ".author@text",
        "bookUrl": "h3@a@href",
        "coverUrl": "img@src"
      },
      "ruleBookInfo": { "name": "h1@text", "author": ".author@text", "tocUrl": ".toc@a@href" },
      "ruleToc": { "chapterList": ".chapters li", "chapterName": "a@text", "chapterUrl": "a@href" },
      "ruleContent": { "content": "#content@html", "nextContentUrl": ".next@a@href" }
    },
    "ttl": "10m"
  }'
```

响应（HTTP 201）里拿到 `id`；也可以用 `"id": "my-source"` 自定义（幂等创建，格式 `[A-Za-z0-9._-]{1,64}`）：

```json
{"instance":{"id":"src-3f9c...","name":"本地假书源","url":"http://127.0.0.1:18081","ttlSeconds":600,"remainingSeconds":600,"expiresAt":"2025-01-01T00:10:00Z","createdAt":"...","lastUsed":"..."}}
```

也可以用文件直接提交：

```bash
curl -s -X POST localhost:8080/api/v1/instances \
  -H 'Content-Type: application/json' \
  -d "{\"config\": $(cat examples/book-source.json), \"ttlSeconds\": 600}"
```

## 4. 调用功能

```bash
ID=$(curl -s -X POST localhost:8080/api/v1/instances \
  -H 'Content-Type: application/json' \
  -d "{\"config\": $(cat examples/book-source.json), \"ttl\": \"10m\"}" \
  | python3 -c 'import json,sys;print(json.load(sys.stdin)["instance"]["id"])')

# 搜索
curl -s -X POST localhost:8080/api/v1/instances/$ID/search \
  -H 'Content-Type: application/json' -d '{"key":"剑","page":1}'
curl -s -X POST localhost:8080/api/v1/instances/$ID/search -d '{"key":"剑","page":1}' -H 'Content-Type: application/json'
# 详情
curl -s -X POST localhost:8080/api/v1/instances/$ID/bookinfo -d '{"bookUrl":"http://127.0.0.1:18081/book/1"}' -H 'Content-Type: application/json'
# 目录
curl -s -X POST localhost:8080/api/v1/instances/$ID/chapters -d '{"tocUrl":"http://127.0.0.1:18081/toc/1"}' -H 'Content-Type: application/json'
# 正文（自动跟随 nextContentUrl 合并多页）
curl -s -X POST localhost:8080/api/v1/instances/$ID/content -d '{"chapterUrl":"http://127.0.0.1:18081/content/1-1"}' -H 'Content-Type: application/json'
```

每次调用都会**续期**该实例的 TTL；闲置超过 TTL 会自动回收（`ttl.md`）。

## 5. 在 Go 里当库用

```go
import (
    "context"
    "legado-go/internal/booksource"
    "legado-go/internal/model"
)

cfg, _ := model.ParseConfig(data)      // 书源 JSON
src, _ := model.Normalize(cfg)         // 归一化（header/variable/exploreUrl）
inst := booksource.New(src, booksource.Options{})
defer inst.Close()

books, _ := inst.Search(context.Background(), "剑来", 1)
```

完整 API 见 [library.md](library.md)。

## 6. 给 LLM 用（MCP）

服务同时在 `/mcp` 暴露 **MCP 2026-07-28 无状态 JSON-RPC**，Claude / ChatGPT / Cursor 等可直接挂载：

```json
{
  "mcpServers": {
    "legado": { "type": "http", "url": "http://localhost:8080/mcp" }
  }
}
```

12 个工具（`source_create` / `source_search` / `source_content` …）见 [mcp.md](mcp.md)。

## 7. 跑测试与冒烟

```bash
go test ./...            # 单元测试
./scripts/smoke.sh       # 端到端：假书站 + 服务 + REST + MCP + TTL 回收
```

## 常见坑

- **`ttl` 传了没用**：确认字段名是 `ttl`（`"10m"`）或 `ttlSeconds`（`600`），且服务端 `MAX_TTL` 不小于它。
- **实例突然 410**：被 TTL 回收了，墓碑期内返回 `410 EXPIRED`；重新创建即可，或调大 `DEFAULT_TTL`。
- **搜不到书**：先用 `POST /api/v1/instances/{id}/eval` 单独试某条规则，见 [troubleshooting.md](troubleshooting.md)。
