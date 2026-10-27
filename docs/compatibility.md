# 与 upstream/legado 的兼容性（compatibility.md）

目标：**尽可能复用现有书源**。规则语义按 upstream 逐条对齐，宿主环境相关能力按 Go 生态替代。
本文档说明"哪些对齐了、哪些没做、差异在哪"，避免拿不支持的书源去排查规则 bug。

对比基准：`upstream/legado`（Kotlin/Android，提交见仓库根 `upstream/legado`）。

## 对齐情况总览

| 能力 | 状态 | 说明 |
| --- | --- | --- |
| 书源 JSON 字段 | ✅ 全量 | `bookSourceUrl/Name/Group/Type/Enabled/Comment/Variables/Header/LoginUrl/LastUpdateTime` |
| 搜索 `ruleSearch` | ✅ 全量 | 含 `checkKeyWord`、`bookList/name/author/kind/wordCount/intro/lastChapter/coverUrl/bookUrl/tocUrl` |
| 发现 `exploreUrl` | ✅ 全量 | 支持 `title::url` 多行、`{{page}}`、分类列表 |
| 详情 `ruleBookInfo` | ✅ 全量 | 含 `init`/`name`/`author`/`intro`/`coverUrl`/`tocUrl`/`lastChapter`/`wordCount`/`canReName` |
| 目录 `ruleToc` | ✅ 全量 | 含 `nextTocUrl`（≤20 页）、`chapterLazyLoad`、`preUpdateJs` |
| 正文 `ruleContent` | ✅ 全量 | 含 `nextContentUrl`（≤20 页合并）、`replaceRegex`、`webJs`、`sourceRegex`、`imageStyle` |
| 规则前缀 | ✅ 全量 | `@@` `-` `+` `:` `!` `@css:` `@json:` `@xpath:` `@js:` `<js>` `@html` `@text` `@put/@get` |
| 规则链 | ✅ 全量 | `@` 选择器、`##` 正则替换、`###` 分组引用、`&&`/`\|\|` 组合、`$1..$n`、`{{}}` 插值 |
| JS 运行时 | ⚠️ ES5+ 子集 | goja 支持 ES5.1 全量 + 大部分 ES6（箭头函数/模板字符串/let/const/Promise）；**无** `async/await`、`Proxy` 完整语义、DOM API |
| `java.*` | ⚠️ 常用子集 | `ajax/ajaxAll/connect/get/post/head`、`putVar/getVar`、`put/getFromCache/cache`、base64/md5/sha1/sha256/hex/aes/URI/UUID/timeFormat；**无**文件、图片、`androidId`、`log` 之外的系统能力 |
| `source.*` / `book.*` / `chapter.*` / `cookie.cache` | ✅ | 见 [library.md](library.md#js-上下文) |
| `webView` 抓取 | ❌ 忽略 | 纯 Go 无法跑 WebView；`webView:true` 的步骤会退化为普通 HTTP 抓取（可能失败） |
| `bookSourceType=1` 音频 / `2` 图片 / `3` 文件 | ❌ 未实现 | 只实现文本（类型 0）的完整管线；其它类型会被接受但只用文本规则 |
| 阅读进度 / 书架 / 书签 | — | 不在"书源调用库"范围（那是阅读器职责） |
| 导入书源（`json`/`txt` 批量、订阅 URL） | ⚠️ 部分 | 接受单对象或数组（取第一个）；不做订阅抓取与"分享书源"格式 |
| 登录流程（`loginUrl` + 登录 UI） | ⚠️ 部分 | 支持 `loginUrl` 的请求与 `loginHeader` 存取；**不做**交互式登录 UI/验证码 |
| 拼音/繁简转换 | ❌ | `ruleSearch.name` 里的 `{{key}}` 不做拼音处理；上游的 `pinyin` 变量未提供 |
| 章节去重/排序 | ✅ | 按 URL 去重（保持首次出现顺序）+ `sortUrl`/`sortName` |
| `canReName`（正文里替换书名） | ✅ | 见 `formatContent` |

## 明确的已知差异

1. **`Mode.Regex` 的 `:` 前缀**
   上游 `SplitSourceRule` 里 `:` 仅作用于 all-in-one 分支；本实现同样只在 `SplitSourceRule` 的 allInOne 场景生效。
   `{{k}}`、`@get:{k}` 这类插值规则在字符串模式直接返回展开值（对齐上游 `AnalyzeRule.getString` 的 `else -> rule`）。

2. **`java.get` 是 HTTP GET**
   对齐上游 `JsExtensions.kt:378`：`java.get(url, headers)` 发 HTTP 请求并返回响应体；
   变量请用 `java.putVar/getVar`（不是 `java.get`）。

3. **非 2xx 不抛错**
   上游重试后仍非 2xx 会返回原响应体而不抛异常；本实现相同（`StrResponse.StatusCode` 保留状态码）。
   需要严格失败时用 `java.connect` 或自行判断。

4. **`@js:` 与 `##@js:` 等价**
   上游两种都支持（`##` 后接 `@js:` 视为 eval 段）；本实现两种都识别。
   但**必须带 `@js:` 前缀**，裸 JS 不会被当成脚本（`<js>...</js>` 除外）。

5. **XPath 引擎差异**
   Go 用 `antchfx/htmlquery`，Android 用 Jsoup 的 XPath 兼容层。常见表达式一致；
   `//td[contains(@class,'x')]/following-sibling::td[1]` 等轴表达式都支持，但**非标准逗号选择器**（`//a/@href|//b`）行为可能不同。

6. **JSONPath 自研**
   逐条对齐上游 `JsonPath.read` 的语义（`$.a.b`、`$..b`、`$.a[0].b`、`$.a[*].b`、`$['a-b']`、函数 `length()`/`keys()`），
   但与第三方 JSONPath 实现的扩展语法（过滤器 `?()`、脚本表达式）不完全一致——**不支持** `?(@.price<10)` 这类过滤表达式。

7. **字符集**
   响应头有 charset 用之；否则从 HTML `<meta charset>` / `http-equiv` 嗅探；再否则按 GB18030 兜底解 UTF-8 失败的情况（`iconv-lite` 等价的 `golang.org/x/text/encoding`）。

8. **缓存**
   只缓存页面级响应（`CACHE_TTL`，默认 60s，按 URL+方法+body 做 key）。
   上游的 `AnalyzeUrl` 缓存还有"书源级变量缓存"；本实现用 `cache.*` JS API + 进程内 TTL。

## 迁移现有书源的建议流程

1. `POST /v1/sources/validate` 先看能否解析（字段是否认识）。
2. `source_eval` 逐步验证：先 `java.ajax(searchUrl)` 看能否拿到页面；
   再 `AnalyzeRule` 里的选择器（可用 `@js:` 包一段 `java.ajax` + 打印）。
3. 用 `POST /v1/instances/{id}/search` 验证最小闭环。
4. 若失败点集中在 `webView`、图片/音频类型、验证码登录 → 该能力本实现不支持，需改书源或放弃。

## 未实现清单（按优先级）

| 优先级 | 项 | 影响 |
| --- | --- | --- |
| P1 | `webView` 抓取（headless 浏览器） | 少数强反爬站点的书源不可用 |
| P1 | 交互式登录（表单/验证码） | 需要登录的书源只能手工把 Cookie 塞进 `header` |
| P2 | `bookSourceType` 1/2/3 管线 | 音频/图片/文件书源 |
| P2 | 拼音/繁简/`{{key}}` 变体 | 搜索关键词归一化 |
| P3 | JSONPath 过滤表达式 `?()` | 复杂 JSON 站点 |
| P3 | 书源订阅与批量导入 | 运维便利性 |

## 如何验证某个上游行为

```bash
# 在 upstream 里定位规则实现（示例：找 nextContentUrl 的循环）
rg -n "nextContentUrl|nextTocUrl" upstream/legado/app/src/main/java/io/legado/app/model/
rg -n "fun getString" upstream/legado/app/src/main/java/io/legado/app/model/analyzeRule/AnalyzeRule.kt
```

本仓库对应文件：
`internal/booksource/rules_runner.go`（`nextContentUrl` 循环在 `contentPages` 附近的 `for ... guard < 20`）、
`internal/analyze/rule.go`（`applyRule`）、`internal/rules/rules.go`（切分）。
