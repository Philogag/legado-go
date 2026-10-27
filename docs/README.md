# legado-go 文档

把 [Legado（阅读）](https://github.com/gedoor/legado) 的**书源调用引擎**抽成独立 Go 库，
再用 **REST + MCP 双入口 + TTL 实例注册表** 包成常驻微服务。

## 阅读路线

| 我想…… | 看这里 |
| --- | --- |
| 5 分钟跑起来 | [quickstart.md](quickstart.md) |
| 知道服务怎么启动、有哪些环境变量 | [deployment.md](deployment.md) |
| 用 HTTP 创建书源实例、搜索、取正文 | [rest-api.md](rest-api.md) |
| 让 LLM / Claude / ChatGPT 直接调书源 | [mcp.md](mcp.md) |
| 在 Go 代码里把书源当库用 | [library.md](library.md) |
| 写一份自己的书源 JSON | [configuration.md](configuration.md) + [rules.md](rules.md) |
| 搞懂 TTL 实例生命周期（过期 / 墓碑 / 上限） | [ttl.md](ttl.md) |
| 搞懂引擎内部结构与 JS 沙箱 | [architecture.md](architecture.md) |
| 确认与阅读 App 的差异 | [compatibility.md](compatibility.md) |
| 排错 | [troubleshooting.md](troubleshooting.md) |
| 改代码 / 跑测试 | [testing.md](testing.md) |

## 一句话架构

```
书源 JSON ──model.Normalize──▶ model.Source
                                 │
                    registry.Entry（TTL 生命周期）
                                 │
              ┌──────────────────┴───────────────────┐
        REST (echo)                            MCP (JSON-RPC 2026-07-28)
              │                                      │
              └───────────┬──────────────────────────┘
                          ▼
                 booksource.Source
      Search / Explore / BookInfo / Chapters / Content / Eval
                          │
       ┌──────────┬───────┼──────────┬──────────┬──────────┐
     rules     analyze  analyzeurl   netx    jsengine (goja)
   （切分/组装）（jsoup/xpath/    （选项块/  （HTTP/    （java.*/**
                regex/jsonpath）  模板/绝对化） cookies）  source.*/book.*）
```

## 目录

```
projects/legado-go
├── cmd/server/           HTTP 服务入口（echo + TTL 注册表 + MCP）
├── internal/model/       书源配置模型、错误类型、Book/Chapter
├── internal/rules/       规则切分、前缀匹配、{{}} 插值、列表解析
├── internal/analyze/     jsoup / xpath / 正则 / jsonpath 四条取数链路
├── internal/analyzeurl/  searchUrl 选项块解析（,{...}）、模板替换、URL 绝对化
├── internal/netx/        HTTP 客户端、字符集解码、Cookie 存储
├── internal/jsengine/    goja 沙箱 + java/source/book/chapter/cookie/cache 绑定
├── internal/booksource/  书源实例：Search/Explore/BookInfo/Chapters/Content/Eval
├── internal/registry/    TTL 实例注册表（续期、墓碑、上限、后台回收）
├── internal/httpapi/     echo 路由、错误映射、限流
├── internal/mcp/         MCP 2026-07-28 无状态 JSON-RPC 服务
├── tools/fixtureserver/  测试用假书源站点
├── examples/             示例书源配置
├── scripts/              冒烟脚本
└── docs/                 本目录
```

## 与 TS 参考实现的关系

`projects/legado-core-js` 是同一套引擎的 TypeScript 版本（17 个单测），
用于对照验证规则语义；`projects/legado-go` 是生产实现。
两者的规则行为以 [compatibility.md](compatibility.md) 记录的语义为准。

## 能力边界速查

| 能力 | 支持 | 备注 |
| --- | --- | --- |
| 文本类书源（`bookSourceType=0`）全流程 | ✅ | 搜索/发现/详情/目录/正文/翻页合并 |
| 规则语法（`@css` `@xpath` `@json` `@js` `##` `###` `&&` `\|\|` `@@`） | ✅ | 见 [rules.md](rules.md) |
| JS 规则与 `java.*` 常用 API | ✅ | base64/md5/aes/ajax/cookie/cache，见 [library.md](library.md#js-上下文) |
| REST 建实例 + 调用 + TTL | ✅ | 见 [rest-api.md](rest-api.md) / [ttl.md](ttl.md) |
| MCP 2026-07-28（12 工具） | ✅ | 无状态，见 [mcp.md](mcp.md) |
| 当 Go 库嵌入 | ✅ | 见 [library.md](library.md) |
| `webView` 抓取 / 交互式登录 / 音频图片书源 / JSONPath 过滤表达式 | ❌ | 见 [compatibility.md](compatibility.md#未实现清单按优先级) |

## 质量现状

| 项 | 状态 |
| --- | --- |
| `go vet ./...` | 无告警 |
| `go test ./...` | 11 个包全绿（约 2.2K 行测试） |
| `go test -race ./internal/registry ./internal/netx` | 通过 |
| `./scripts/smoke.sh` | REST + MCP + TTL 全流程通过 |
| `make example` | 库用法示例可跑通（对着假书站） |
| TS 参考实现 `projects/legado-core-js` | `npx tsc --noEmit` 干净，17 个测试通过 |

## 文档维护约定

- 代码改行为必须同步改文档：**规则语义** → `rules.md`；**接口** → `rest-api.md` / `mcp.md`；
  **能力边界** → `compatibility.md`（这条最重要，用户靠它判断"是不是我的问题"）。
- 文档里的命令都要能直接复制执行；示例请求体用真实字段名。
- 新增端点/工具时，同步 `docs/README.md` 的能力表与 `rest-api.md` 的错误码表。
