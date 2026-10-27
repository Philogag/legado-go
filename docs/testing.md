# 测试（testing.md）

```bash
make test          # go test ./...（约 3s，无外部网络）
make vet           # go vet ./...
make cover         # 覆盖率 + coverage.html
make smoke         # 端到端：起假书站 + 服务，REST/MCP/TTL 全流程
```

测试**不访问真实站点**：所有抓取都打到 `tools/fixtureserver`（本地假书站）或 `httptest`。

## 测试矩阵

| 包 | 文件 | 覆盖内容 |
| --- | --- | --- |
| `internal/model` | `model_test.go` | 配置解析/默认值/`Normalize`/禁用报错/`variable` 两种写法/`Error` kind |
| `internal/jsonpath` | `jsonpath_test.go` | `$.a.b`、`$..b`、`$.a[0]`、`$.a[*]`、`$['a-b']`、`length()`/`keys()`、缺省前缀 |
| `internal/rules` | `rules_test.go` | 前缀切分（`@@`/`-`/`:`/`@css:`/`@js:`）、`##`/`###`、`$n` 分组引用、`@put/@get`、JS 结果类型转换 |
| `internal/analyze` | `rule_test.go` | JSoup/XPath/JSONPath/正则/JS 执行、`@text`/`@html`、链式 `@`、`{{}}` 插值 |
| `internal/analyzeurl` | `url_test.go` | `,{...}` 选项块（js/post/body/charset/headers/cookie/retry/webView）、`{{key}}`/`{{page}}` 替换、相对→绝对 |
| `internal/netx` | `client_test.go` | GET/POST、Cookie 往返、GBK 解码、非 2xx 透传、超时、Cookie 域名隔离 |
| `internal/jsengine` | `engine_test.go` | 绑定（`result`/`key`/`page`/`baseUrl`/`book`/`chapter`）、jsLib 只注入一次、`@@` 转义、`java.*`（ajax/putVar/cache/base64/md5/aes）、`source.*`、`cookie.*` |
| `internal/booksource` | `source_test.go` | 搜索/详情/目录/正文全链路、`nextContentUrl` 多页、`##` 清洗、相对 URL 绝对化、`@js:` 规则、实例 `Close` |
| `internal/registry` | `registry_test.go` | 创建/去重/自定义 ID、滑动续期、TTL 过期转墓碑、墓碑期 `410`、墓碑清理后 `404`、`MaxTTL` 绝对寿命与封顶、`Touch` 改 TTL、`Sweep` 计数、并发创建 |
| `internal/httpapi` | `httpapi_test.go` | 全部端点、错误映射（400/404/410/422/429/502/504）、鉴权、限流、请求体校验 |
| `internal/mcp` | `mcp_test.go` | `initialize`/`tools.list`/`tools.call` 全 12 工具、批量请求、通知 `202`、版本头、`isError`、`Accept` 校验 |
| 端到端 | `scripts/smoke.sh` | 真起进程：REST 全流程 → TTL 到期 `410` → 删除 `404` → MCP 全流程 |

合计约 2.2K 行测试代码 / 11.4K 行源码。

## 假书站（fixtureserver）

`tools/fixtureserver/main.go` 是一个 88 行的 `net/http` 服务，提供：

| 路径 | 内容 |
| --- | --- |
| `/search?q=&page=` | 搜索页，2 条结果（`.result .item`，含书名/作者/封面/链接） |
| `/book/1`、`/book/2` | 详情页（`h1` 书名、`.intro` 简介、`.toc a` 目录链接） |
| `/toc/1`、`/toc/2` | 目录页（`.chapters li`，`/toc/2` 是第二页目录，验证 `nextTocUrl`） |
| `/content/1-1` | 第一章正文第一页（`#content`，含空行与零宽字符，测 `FormatContent`） |
| `/content/1-1-2` | 第一章第二页（`nextContentUrl` 指向它，验证多页合并） |
| `/content/1-2` | 第二章正文 |
| `/content/2-1` | 第二本书的正文 |

```bash
go run ./tools/fixtureserver -addr 127.0.0.1:18081
curl -s 'http://127.0.0.1:18081/search?q=%E5%89%91&page=1' | head -20
```

配套书源配置：`examples/book-source.json`（`bookSourceUrl` 指向 `127.0.0.1:18081`）。

## 加一个测试：以"新增规则能力"为例

1. **先写失败的用例**（`internal/analyze/rule_test.go`）：

```go
func TestRuleNewPrefix(t *testing.T) {
	r := mustRule(t, "@tpl:name")
	got, err := r.getString(ctxWith(t), `<a>张三</a>`)
	if err != nil || got != "张三" {
		t.Fatalf("got %q err %v", got, err)
	}
}
```

2. `go test ./internal/analyze -run TestRuleNewPrefix` → 失败。
3. 实现（`internal/analyze/rule.go` 前缀表 + 分派）。
4. 再跑，绿；`make test vet`。

需要真实页面结构的用例，往 `fixtureserver` 加一个路由，**不要**在测试里请求外网。

## 调试技巧

```bash
# 只跑某个包、某个用例，带日志
go test ./internal/booksource -run TestContentMultiPage -v

# 竞态检测（注册表并发用例值得常跑）
go test -race ./internal/registry ./internal/netx

# 覆盖率里看哪些分支没测到
go test -coverprofile=coverage.out ./internal/rules && go tool cover -func=coverage.out

# 端到端时保留服务日志（smoke.sh 默认清理临时目录）
bash -x ./scripts/smoke.sh 2>&1 | tail -40
```

## 已知测试约束

- `node --test`-style 的顶层 `await` 与 `go test` 无关；但若在仓库里跑 TS 参考实现，用 `npx tsx --test test/core.test.ts`。
- `jsengine` 测试用真实 goja VM，每个用例 `New`/`Close`，不要在包级复用（实例内 JS 变量会串）。
- `registry` 用例**不用** sleep 等后台 ticker，统一用极短 TTL + 手动 `Sweep()`，避免 flaky。
- 抓取类用例给 `httptest.Server` 设 `Timeout`，避免 CI 卡死。

## CI 建议

```yaml
- run: go vet ./...
- run: go test -race ./...
- run: ./scripts/smoke.sh
```

`-race` 在 `registry`/`netx`/`jsengine` 上会慢一些（约 +40%），但能挡住实例共享状态的回归。
