# 规则语法（rules.md）

书源的每条字段值都是一条**规则串**。规则串由四层语义叠成：

```
规则 = [模式前缀] [选择器链] [##替换正则[##替换文本[##只替换首次]]] [@put:{...}]
                          ↑ 链内可嵌 <js>…</js> / @js:… / {{…}} / @get:{…} / $1..$99
```

一次取值 = **展开插值 → 按 `##` 拆出替换规则 → 按模式执行选择器 → 应用替换**。

---

## 1. 模式前缀（决定用哪条取数链路）

| 写法 | 模式 | 说明 |
| --- | --- | --- |
| `h3@a@text` | 默认 CSS（jsoup） | 不加前缀即 CSS 选择器 |
| `@css:h3@a@text` | 强制 CSS | 内容本身是 JSON 时用它可以压过自动判定 |
| `@@h3` | 字面量 CSS | 剥掉开头一个 `@`，用于选择器本身以 `@` 开头的场景 |
| `@xpath://div[@id='x']/a/text()` | XPath | 也接受以单个 `/` 开头（自动识别为 XPath） |
| `@json:$.data.list[*].name` | JsonPath | 也接受以 `$.` / `$[` 开头；**内容是 JSON 时默认走这条** |
| `@js:'x' + result` | 纯 JS | 整串交给 goja，`result` 为当前中间结果 |
| `<js>…</js>` | JS 片段 | 可**嵌在链中间**，输出字符串继续参与后续选择器 |
| `:正则` | 正则 | 只在「取元素/取列表」（allInOne）场景生效，整串当正则处理 |

判定顺序（`internal/rules/rules.go:107` `parse`）：`@js:` → `@css:` → `@@` → `@xpath:` → `@json:` → JSON 内容/`$.` → `/` → 默认 CSS。
JSON 内容的自动判定只对**取字符串/列表**生效；取元素时仍按规则前缀走。

## 2. 选择器链 `@`

`h3@a@href` 在 jsoup 模式下逐段下钻：先在所有 `h3` 里选 `a`，最后一段若是下列**后缀关键字**就按语义取值，否则当**属性名**：

| 最后一段 | 结果 |
| --- | --- |
| `text` | 元素文本（块级元素之间补换行，`&nbsp;` 归一为空格，去首尾空白） |
| `ownText` | 只要直接子文本，不含后代文本 |
| `textNodes` | 所有文本节点，用 `\n` 连接 |
| `html` | 元素内部 HTML（会剔除 `<script>`/`<style>`） |
| `all` | 元素自身的 outerHTML（保留 script/style） |
| `href` / `src` / `data-original` / … | 取同名属性（结果自动去重、空值丢弃） |
| （无最后一段） | 等同于 `text` |

列表取值时每一段都**只取第一个匹配**再向下钻（与上游 `getStringList` 一致）。

## 3. 插值

| 语法 | 语义 |
| --- | --- |
| `{{key}}` / `{{page}}` / `{{book.name}}` | 展开为变量；找不到同名变量时保留原文本 |
| `{{(page+1)*20}}` | 括号内以 JS 求值 |
| `@get:{token}` | 取变量（`@put` 或书源 `variable` 写入） |
| `@put:{k:规则}` | 把子规则的结果存进变量 `k`，规则串其余部分照常执行 |
| `$1` … `$99` | 在取元素/列表的 allInOne 场景引用正则捕获组 |

`@put` 的值本身可以是一条完整规则（含 JS）：

```
.bookList@href@put:{firstUrl:$0}
```

## 4. `##` 替换段

```
选择器 ## 正则 ## 替换文本 ## 任意字符
```

- 第 2 段是正则，第 3 段是替换文本（支持 `$1` 反向引用），**第 4 段存在即「只替换第一次命中」**。
- 正文清洗（`ruleContent.replaceRegex`）用 `\n` 分隔多条规则，每条都可以是 `正则##替换` 或裸正则。

示例：去掉小广告，并统一空白

```
#content@html##<script[\s\S]*?</script>##
```

## 5. JS 上下文

JS 里可直接用的绑定：

| 名字 | 说明 |
| --- | --- |
| `result` | 当前中间结果（字符串或字符串数组） |
| `baseUrl` | 当前页面地址 |
| `key` / `page` / `title` | 搜索关键字 / 页码 / 章节标题（按场景注入） |
| `book` / `chapter` | 当前书/章节对象（`book.name`、`chapter.title`…） |
| `source` | 书源句柄：`getVariable()` `setVariable()` `get()` `put()` `getHeaderMap()` `getLoginInfo()` … |
| `java` | `ajax/connect/get/post/head/ajaxAll`、`putVar/getVar`、`put/getFromCache`、`base64Encode/base64Decode`、`md5Encode/md5Encode16`、`sha1Encode/sha256Encode`、`hexEncodeToString/hexDecodeToString`、`aesEncodeToString/aesDecodeToString`、`encodeURI/decodeURI`、`randomUUID`、`timeFormat` |
| `cookie` | `getCookie(url)` `setCookie(url, c)` `replaceCookie(url, c)` `removeCookie(url)` |
| `cache` | `put(key, value, ttlSeconds)` `get(key)` `delete(key)` |
| `console` | `log/info/warn/error`（`-verbose-js` 打开时打到 stderr） |

注意事项：

- 脚本里的 `@js:` 前缀会被剥掉；`@@` 会被还原成 `\n`（阅读的换行转义）。
- **不带 `return`** 的脚本会被自动包一层 `return (...)`；带 `return` 的原样执行。
- `java.ajax` 失败抛 JS 异常，可用 `try/catch`。
- 沙箱无文件、无网络直连（网络只能走 `java.ajax` 等宿主 API），单次执行有超时保护。

## 6. 选项块

`searchUrl` 末尾可接 JSON 选项：

```
/search?q={{key}},{"charset":"gbk","method":"POST","body":"q={{key}}&p={{page}}","headers":{"X-Token":"{{token}}"},"retry":2}
```

| 选项 | 生效 | 说明 |
| --- | --- | --- |
| `method` | ✅ | `GET`（默认）/`POST` 等 |
| `charset` | ✅ | 响应解码字符集，如 `gbk`、`gb2312`、`big5` |
| `headers` | ✅ | 追加/覆盖请求头 |
| `body` | ✅ | POST 体，内部同样会做 `{{}}` 插值 |
| `retry` | ✅ | 额外重试次数（总次数 = retry+1） |
| `js` | ✅ | 请求前后可改 URL/头的 JS 字符串，返回值可替换 URL |
| `type` / `cookies` / `sourceRegex` / `webView` / `webJs` | ⛔ 解析但未使用 | 见 compatibility.md |

## 7. 语义边界（容易踩的坑）

- **`&&` / `||` 多查询组合未实现**：上游支持 `规则A&&规则B` 合并列表、`规则A||规则B` 取首个非空；本实现只取第一条规则（遇到 `&&` 会被当成普通选择器文本）。
- **`{{}}` 与 `@get:{}` 在字符串模式下是「直接输出」**：`@get:{k}` 不会再去页面里正则抽取，这与上游 `AnalyzeRule.getString` 的 `else -> rule` 分支一致。
- **`@js:` 只能出现在整条规则开头或作为 `##@js:` 替换段**；中间位置请用 `<js>…</js>`。
- **空结果不报错**：选择器没命中得到空串/空列表，`ParseEmpty` 只在整份结果为空时由上层给出。
