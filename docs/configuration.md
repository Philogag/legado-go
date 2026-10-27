# 书源配置（configuration.md）

书源 JSON 与**阅读 App（Legado）导出的格式完全一致**——你可以直接从阅读里
「书源管理 → 导出」拿到 JSON，原样 POST 给本服务即可，字段名不需要改。

服务端接受的三种提交形态：

```jsonc
{ "config": { ...单个书源... } }     // 推荐
{ ...单个书源... }                    // 裸对象
[ {...}, {...} ]                      // 书源数组：只取第一个
```

> 数组形态是为了兼容阅读 App 的整包导出；批量管理请逐个创建实例。

---

## 1. 顶层字段

| 字段 | 类型 | 说明 |
| --- | --- | --- |
| `bookSourceUrl` | string | **必填**。书源站点根地址，同时作为相对 URL 的基准 |
| `bookSourceName` | string | 书源名；缺省用 `origin`，再缺用 URL |
| `bookSourceGroup` | string | 分组，仅展示 |
| `bookSourceType` | int | `0` 文本 / `1` 音频 / `2` 图片 / `3` 文件（缺省 0） |
| `bookUrlPattern` | string | 详情页 URL 正则；命中时 `BookInfo` 直接以该 URL 为结果，不再抓取 |
| `enabled` | bool | 显式 `false` 时 `Normalize` 直接报 `INVALID_CONFIG: 书源已禁用` |
| `enabledExplore` | bool | 是否启用发现页（`exploreUrl` 为空时无意义） |
| `searchUrl` | string | 搜索地址模板，支持 `{{key}}` `{{page}}` 与 `,{...}` 选项块 |
| `exploreUrl` | string | 发现页入口：JSON 数组或文本行，见 §4 |
| `header` | string | 请求头：JSON 对象或 `k:v` 逐行 |
| `loginUrl` / `loginUi` / `loginCheckJs` | string | 登录相关。本服务**不提供 WebView**，只保留字段与 `source.getLoginInfo*` 读取能力 |
| `jsLib` | string | 公共 JS，实例内共享（`eval` 前注入一次） |
| `bookSourceComment` | string | 备注 |
| `variable` | string | 书源变量，JSON 对象或 `k=v` 逐行，见 §3 |
| `weight` / `customOrder` / `lastUpdateTime` / `respondTime` | int | 排序与统计，仅透传 |
| `concurrentRate` | string | 并发/限速声明（如 `"3/s"`）。**当前不做强制**，见 compatibility.md |
| `ruleSearch` | object | 搜索规则，见 §2 |
| `ruleExplore` | object | 发现规则，见 §2 |
| `ruleBookInfo` | object | 详情规则，见 §2 |
| `ruleToc` | object | 目录规则，见 §2 |
| `ruleContent` | object | 正文规则，见 §2 |

## 2. 规则字段

### ruleSearch

| 字段 | 说明 |
| --- | --- |
| `checkKeyWord` | 检查关键字；搜索结果的 `name` 必须匹配它，否则丢弃 |
| `bookList` | 结果列表选择器（必须能选出多个元素） |
| `name` `author` `intro` `kind` `lastChapter` `updateTime` `bookUrl` `coverUrl` `wordCount` | 相对 `bookList` 每项的取值规则 |

### ruleExplore

与 `ruleSearch` 相同，但没有 `checkKeyWord`。

### ruleBookInfo

| 字段 | 说明 |
| --- | --- |
| `init` | 抓取前的初始化规则（可写变量、可改 URL） |
| `name` `author` `intro` `kind` `lastChapter` `updateTime` `coverUrl` `wordCount` | 详情字段 |
| `tocUrl` | 目录页地址 |
| `canReName` | 是否允许改名（仅透传） |
| `downloadUrls` | `json:"downloadUrls"`，下载地址（仅透传） |

### ruleToc

| 字段 | 说明 |
| --- | --- |
| `preUpdateJs` | 解析目录前执行的 JS |
| `chapterList` | 章节列表选择器 |
| `chapterName` / `chapterUrl` | 相对每章元素的取值规则 |
| `formatJs` | 章节名后处理 JS（`result` 为章节名数组） |
| `isVolume` / `isVip` / `isPay` | 布尔标记规则 |
| `updateTime` | 章节更新时间 |
| `nextTocUrl` | 下一页目录（**已实现**，会循环抓取直到为空或重复） |

### ruleContent

| 字段 | 说明 |
| --- | --- |
| `title` | 正文页标题 |
| `content` | 正文选择器 |
| `nextContentUrl` | 下一页正文；非空时自动翻页合并（默认上限 20 页，防环） |
| `webJs` | 抓取正文前在页面上下文执行的 JS（返回正文 HTML/文本） |
| `sourceRegex` | 从页面源码里抽取目标内容的预览正则 |
| `replaceRegex` | 正文清洗正则，形如 `##正则##替换##是否替换首次`，多条用 `\n` 分隔 |
| `imageStyle` | 图片样式（仅透传） |
| `payAction` | 付费动作（本服务忽略） |

## 3. header 与 variable 的两种写法

```jsonc
// header：JSON
"header": "{\"User-Agent\":\"Mozilla/5.0\",\"Referer\":\"https://x.com/\"}"

// header：逐行（冒号分隔，常用于阅读 App 里手填）
"header": "User-Agent: Mozilla/5.0\nReferer: https://x.com/"
```

```jsonc
// variable：JSON 对象（推荐，与阅读一致）
"variable": "{\"token\":\"abc\",\"vip\":1}"

// variable：逐行 k=v
"variable": "token=abc\nvip=1"
```

读取方式：

- 规则里用 `@get:{token}` 取变量（`variable` 表与 `@put` 共用同一张表）；
- JS 里 `source.getVariable()` 拿整包 JSON，`source.get("token")` 拿单个；
- `{{...}}` 里是 **JS 表达式**，不是变量名查找：`{{page}}` `{{key}}` `{{book.name}}` 能用是因为它们是 JS 绑定；想读书源变量要写 `@get:{token}` 或 `{{source.get('token')}}`。

## 4. exploreUrl 的三种形态

```jsonc
// ① JSON 数组（阅读 App 导出格式）
"exploreUrl": "[{\"title\":\"玄幻\",\"url\":\"/list/xuanhuan?page={{page}}\"}]"

// ② 文本行，一行一个入口，`名称::url`
"exploreUrl": "玄幻::/list/xuanhuan?page={{page}}\n都市::/list/dushi?page={{page}}"

// ③ 纯 URL（全部用「默认」入口）
"exploreUrl": "/list/all?page={{page}}"
```

`Explore` 调用时可用 `url` 参数直接指定目标地址，跳过入口表。

## 5. searchUrl 的选项块

```
https://site.com/search?q={{key}},{"charset":"gbk","method":"POST","body":"q={{key}}","headers":{"X-Token":"t"}}
```

支持的选项：`charset` `method` `headers` `body` `retry` `js` `type` `cookies` `sourceRegex` `webView`（忽略） `webJs`。详见 [rules.md](rules.md#6-选项块)。

## 6. 一份完整可跑的示例

`examples/book-source.json`（针对内置假书站）：

```json
{
  "bookSourceUrl": "http://127.0.0.1:18081",
  "bookSourceName": "本地假书源",
  "bookSourceGroup": "示例",
  "header": "{\"User-Agent\":\"legado-go-test\"}",
  "variable": "{\"site\":\"fixture\"}",
  "searchUrl": "/search?key={{key}}&page={{page}}",
  "exploreUrl": "[{\"title\":\"全部\",\"url\":\"/list?page={{page}}\"}]",
  "ruleSearch": {
    "bookList": ".result .item",
    "name": "h3@a@text",
    "author": ".author@text",
    "kind": ".kind@text",
    "bookUrl": "h3@a@href",
    "coverUrl": "img@src",
    "intro": ".intro@text"
  },
  "ruleExplore": {
    "bookList": ".result .item",
    "name": "h3@a@text",
    "bookUrl": "h3@a@href"
  },
  "ruleBookInfo": {
    "name": "h1@text",
    "author": ".author@text",
    "intro": ".intro@text",
    "coverUrl": "img.cover@src",
    "tocUrl": ".toc@a@href",
    "lastChapter": ".last@text"
  },
  "ruleToc": {
    "chapterList": ".chapters li",
    "chapterName": "a@text",
    "chapterUrl": "a@href"
  },
  "ruleContent": {
    "title": "h1@text",
    "content": "#content@html",
    "nextContentUrl": ".next@a@href",
    "replaceRegex": "##<script[^>]*>[\\s\\S]*?</script>##"
  }
}
```

## 7. 编码与 URL 约定

- **相对 URL** 一律以 `bookSourceUrl` 为基准补全（`bookUrl`/`chapterUrl`/`coverUrl`/`tocUrl`/`nextContentUrl` 同理，基准是**当前页面地址**）。
- 站点编码不是 UTF-8 时，在 `searchUrl` 的选项块里写 `"charset":"gbk"`；响应头的 `Content-Type` 已声明 charset 时会优先采用。
- `data:` / `file:` / `http(s)://` 开头的地址不会被改写。

## 8. 校验

创建前想先验证配置能否解析：

```bash
curl -s -X POST localhost:8080/api/v1/sources/validate \
  -H 'Content-Type: application/json' \
  -d "{\"config\": $(cat examples/book-source.json)}"
```

返回：

```json
{"valid":true,"name":"本地假书源","url":"http://127.0.0.1:18081","group":"示例",
 "type":0,"hasSearch":true,"exploreEntries":[{"title":"全部","url":"/list?page={{page}}"}],"variables":1}
```
配置有错时返回 `400` + `{"error":{"kind":"INVALID_CONFIG","message":"..."}}`。

---

# 服务端参数（命令行 / 环境变量）

两者等价，**命令行优先**。`legado-server -h` 可看全部。

| 命令行 | 环境变量 | 默认 | 说明 |
| --- | --- | --- | --- |
| `-addr` | `ADDR` | `:8080` | 监听地址 |
| `-ttl` | `DEFAULT_TTL` | `30m` | 新实例默认空闲 TTL（滑动窗口） |
| `-max-ttl` | `MAX_TTL` | `6h` | 实例绝对寿命上限，`0` 表示不限 |
| `-max-instances` | `MAX_INSTANCES` | `64` | 最大存活实例数，超限返回 `429`，`0` 表示不限 |
| `-sweep` | `SWEEP_INTERVAL` | `5s` | 后台过期扫描间隔 |
| `-http-timeout` | `HTTP_TIMEOUT` | `30s` | 单次抓取超时（含 `java.ajax`） |
| `-cache-ttl` | `CACHE_TTL` | `1h` | 页面级 HTTP 缓存 TTL |
| `-grace` | `GRACE_PERIOD` | `10m` | 实例回收后墓碑保留时长（期间返回 `410`） |
| `-token` | `API_TOKEN` | 空 | 非空则启用 Bearer 鉴权（`/api/v1` 与 `/mcp`） |
| `-rate-limit` | `RATE_LIMIT` | `0` | 每 IP 每分钟请求上限，`0` 表示不限 |
| `-quiet` | `QUIET` | `1` | `1` 关闭访问日志 |
| `-verbose-js` | `VERBOSE_JS` | `0` | `1` 打印书源 `console.log` |
| `-shutdown-timeout` | `SHUTDOWN_TIMEOUT` | `10s` | 优雅停机宽限时长 |
| `-healthcheck` | — | 空 | 只做健康检查后退出：`-healthcheck http://127.0.0.1:8080/healthz` |

示例：

```bash
# 本地开发：短 TTL，方便观察回收
DEFAULT_TTL=30s SWEEP_INTERVAL=1s ./bin/legado-server -addr 127.0.0.1:8080

# 生产：令牌 + 限流 + 关闭访问日志
API_TOKEN=$(openssl rand -hex 24) RATE_LIMIT=600 MAX_INSTANCES=512 \
HTTP_TIMEOUT=15s QUIET=1 ./bin/legado-server -addr 0.0.0.0:8080

# 容器健康检查（镜像内无 shell 时用）
./legado-server -healthcheck http://127.0.0.1:8080/healthz
```

非法值（如 `DEFAULT_TTL=abc`）不会启动失败：打印一行 `invalid X=..., using <默认>` 后回落默认值。
