# MCP 接入（mcp.md）

服务在 `/mcp` 同时暴露 **MCP（Model Context Protocol）2026-07-28 无状态 JSON-RPC**。
任一本服务实例本身就是一台「书源 MCP server」：LLM 可以通过工具调用创建书源实例、
搜索、取目录、取正文，而实例生命周期由同一套 TTL 机制管理。

- 协议：JSON-RPC 2.0 over HTTP，**POST only**，无 `initialize` 握手、无 `Mcp-Session-Id`、无 SSE。
- 批量请求（JSON 数组）支持；纯通知（无 `id`）返回 `202 Accepted`。
- 响应带 `Mcp-Protocol-Version: 2026-07-28`；请求可带同名头，版本不符返回 `400`。
- `Accept` 若提供则必须包含 `application/json` 或 `*/*`。
- 请求体上限 4 MiB。

## 挂载到客户端

```json
{
  "mcpServers": {
    "legado": { "type": "http", "url": "http://localhost:8080/mcp" }
  }
}
```

带令牌时：

```json
{
  "mcpServers": {
    "legado": {
      "type": "http",
      "url": "http://localhost:8080/mcp",
      "headers": { "Authorization": "Bearer <API_TOKEN>" }
    }
  }
}
```

## 支持的方法

| method | 说明 |
| --- | --- |
| `initialize` | 返回 `protocolVersion` / `capabilities.tools` / `serverInfo` / `instructions`（无状态，可省略） |
| `notifications/initialized`、`notifications/cancelled` | 直接 `202`，无响应体 |
| `ping` | `{}` |
| `tools/list` | 12 个工具定义 |
| `tools/call` | 调用工具 |
| `resources/list`、`prompts/list` | 空列表 |

## 12 个工具

| 工具 | 作用 | 主要参数 |
| --- | --- | --- |
| `source_create` | 用书源 JSON 创建实例 | `config`（必填）、`id`、`ttl`、`name`、`owner` |
| `source_validate` | 只校验配置，不创建 | `config` |
| `source_list` | 列出存活实例 + 统计 | — |
| `source_get` | 查看实例与书源信息（不续期） | `id` |
| `source_delete` | 立即销毁实例 | `id` |
| `source_touch` | 续期（可顺带改 TTL） | `id` / `instanceId`、`ttl` |
| `source_search` | 搜索 | `id`、`key`、`page` |
| `source_explore` | 发现页列表 | `id`、`url`、`page` |
| `source_book_info` | 书籍详情 | `id`、`bookUrl` / `book`、`name`、`author` |
| `source_chapters` | 目录 | `id`、`bookUrl` / `book` |
| `source_content` | 章节正文（自动翻页合并） | `id`、`chapterUrl` / `chapter`、`title` |
| `source_eval` | 在该实例上下文里跑 JS（调试规则） | `id`、`code` / `script`、`key`、`page`、`baseUrl`、`result` |

工具返回同时带 `content[0].text`（人类可读 JSON 文本）与 `structuredContent`（结构化数据）；
失败时 `isError: true`。

## 一次完整会话

```bash
# 1) 握手（可省略）
curl -s localhost:8080/mcp -H 'Content-Type: application/json' -d \
 '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}'

# 2) 看工具
curl -s localhost:8080/mcp -H 'Content-Type: application/json' -d \
 '{"jsonrpc":"2.0","id":2,"method":"tools/list"}'

# 3) 创建实例
curl -s localhost:8080/mcp -H 'Content-Type: application/json' -d '{
  "jsonrpc":"2.0","id":3,"method":"tools/call",
  "params":{"name":"source_create","arguments":{
     "id":"demo","ttl":"30m",
     "config":{"bookSourceUrl":"http://127.0.0.1:18081","bookSourceName":"本地假书源",
               "searchUrl":"/search?q={{key}}&page={{page}}",
               "ruleSearch":{"bookList":".result .item","name":"a@text","bookUrl":"a@href"}}
  }}}'

# 4) 搜索
curl -s localhost:8080/mcp -H 'Content-Type: application/json' -d '{
  "jsonrpc":"2.0","id":4,"method":"tools/call",
  "params":{"name":"source_search","arguments":{"id":"demo","key":"剑","page":1}}}'
```

响应（节选）：

```json
{"jsonrpc":"2.0","id":4,"result":{
  "content":[{"type":"text","text":"{\"books\":[...],\"count\":2}"}],
  "structuredContent":{"books":[{"name":"剑 奇谭","bookUrl":"http://127.0.0.1:18081/book/1"}],"count":2},
  "isError":false}}
```

## 为什么适合 LLM

- **无状态**：每个请求自带全部上下文，可放在任意负载均衡后面，不需要粘性会话。
- **TTL 即「工具内存」**：LLM 创建实例 → 后续多轮调用复用 → 长期不用自动回收，不会泄漏资源。
- **`source_eval` 是逃生舱**：规则写不出来时，直接让模型写 JS 抓页面。

## 安全

- `API_TOKEN` 非空时 `/mcp` 与 `/api/v1` 一起受保护（`Authorization: Bearer` 或 `X-API-Token`）。
- `INSTANCE_LIMIT`（`MAX_INSTANCES`）对 MCP 同样生效，避免被无限建实例。
- `RATE_LIMIT` 按 IP 限流，对 `/mcp` 同样生效。
- JS 沙箱无文件与直连网络能力，只能通过 `java.ajax` 等宿主 API 出网（仍受 `HTTP_TIMEOUT` 约束）。

## 自测

```bash
go test ./internal/mcp -v      # 覆盖 initialize/tools.list/tools.call/批量/错误
./scripts/smoke.sh             # 端到端包含 MCP 全流程
```
