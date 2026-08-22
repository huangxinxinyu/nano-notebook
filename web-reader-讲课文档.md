# web-reader 讲课文档（60 分钟版）

> **听众画像**：后端开发，不熟悉前端
> **涉及代码范围**：`services/web-reader/`（TS 服务本体）+ `internal/webreader/`、`internal/sourceprocessing/`（Go 侧接入，即 a916d2d..4af7604 之间的增强）
> **文档用法**：正文即讲稿，可直接照读；【】内是动作提示（切文件、板书、提问），不用念出来。

---

## 讲课前准备（提前一天）

- [ ] 本地起好服务：`cd services/web-reader && npm install && npm run build && npm start`（或 `docker compose up -d --build web-reader`）
- [ ] 终端预置两条演示命令（见附录 A），先跑一遍确认网络可达
- [ ] 编辑器打开这几个文件并记住路径，讲的时候直接跳：
  - `services/web-reader/src/reader.ts`（解析管线）
  - `services/web-reader/src/engine.ts`（双引擎）
  - `services/web-reader/src/fetcher.ts`（抓取 + SSRF）
  - `services/web-reader/src/browser.ts`（浏览器引擎）
  - `internal/webreader/webreader.go`（Go 适配器）
  - `internal/sourceprocessing/processor.go`（兜底逻辑，搜 `readViaWebReader`）
- [ ] 白板或画图工具（要画两张图：架构图、解析管线图）

---

## 时间表

| # | 章节 | 时长 |
|---|------|------|
| 1 | 开场与背景：为什么要造这个轮子 | 5 min |
| 2 | 前端速成课：后端视角看网页 | 8 min |
| 3 | 整体架构与接口契约 | 7 min |
| 4 | 解析管线：从脏 HTML 到干净 Markdown | 9 min |
| 5 | 双引擎与 auto 升级策略 | 7 min |
| 6 | SSRF 防护：这个服务最"后端"的部分 | 8 min |
| 7 | 本次增强：Go 侧接入 + 一个有趣的 bug | 10 min |
| 8 | 测试与部署 | 3 min |
| 9 | 总结与 Q&A | 3 min |

---

## 第 1 节 开场与背景（5 分钟）

【开场，语速放慢】

大家好，今天用一个小时讲一下咱们仓库里的 web-reader 服务——一个"把网页变成干净正文"的服务。我会假设大家跟我一样是后端出身、前端不太熟，所以前几分钟先补一点前端的概念，后面的内容都是建立在这几个概念上的。

先说这个服务解决什么问题。

我们的产品链路大家熟悉：用户往 notebook 里加一个"来源"（source），系统抓取、解析、归一化，最后变成 LLM 能用的知识。来源里有一大类是网页。问题来了——**把一个 URL 的 HTML 原样塞给大模型，效果是很差的**。

【板书：一个典型网页的构成】

随便打开一个新闻页，按 F12 看，你会看到：正文可能只占页面 20% 的体积，剩下 80% 是导航栏、侧边栏、广告位、评论区、Cookie 提示、埋点脚本……而且全是 HTML 标签的嵌套噪音。直接给模型，一来浪费 token，二来检索质量差，三来正文被淹没在噪音里。

所以我们需要一个"萃取"步骤：**输入 URL，输出干净的正文**。

第二个背景：这个能力我们之前是**调第三方 API 实现的**（jina reader 这类）。用第三方有三个痛点：成本随量走、数据出内网有合规风险、可用性不受自己控制。所以这次我们把它自托管了，实现思路参考了开源的 jina-ai/reader——它就是这个领域的标杆项目，Firefox 阅读模式算法的工业级封装。

第三个背景，也是这次讲解的重点增量：**上一阶段我们把 web-reader 接进了 Go 侧的 worker 流水线**——当确定性解析的质量门不过关时，自动降级到 web-reader 兜底重试。这部分代码是最近两个 commit 加的，第 7 节细讲。

一句话总结今天的路线：**为什么做 → 网页怎么工作的 → 服务怎么拆的 → 正文怎么提炼的 → JS 页面怎么办 → 怎么防止被打 → Go 侧怎么接的**。

---

## 第 2 节 前端速成课（8 分钟）

【这节是地基，宁可超时 1 分钟也要讲透】

我尽量用后端的概念来类比前端的东西。

### 2.1 HTML、DOM、渲染

**HTML 是一份声明式的结构文档**，长得像 XML：

```html
<html>
  <body>
    <nav>导航栏...</nav>
    <article>
      <h1>标题</h1>
      <p>正文第一段...</p>
    </article>
    <aside>侧边栏...</aside>
  </body>
</html>
```

浏览器拿到它之后，会解析成一棵**DOM 树**——可以理解为"运行时的对象树"。类比大家熟悉的：HTML 像是 proto 文件定义的序列化文本，DOM 是反序列化出来的 message 实例，可以用代码增删改查。

浏览器渲染一个页面的大致流程：HTML 解析成 DOM → CSS 计算样式 → JS 随时改写 DOM → 布局 → 绘制。**今天我们只关心前三步，不需要"绘制"**——我们要的是最终那棵 DOM 树的文本内容，不是像素。

两个对我们有害的前端特性，记住这两点就够了：

1. **CSS 可以隐藏内容**：`display: none` 的元素用户看不见，但它还在 DOM 里。爬下来的 HTML 里有一堆这种"隐形垃圾"。
2. **JS 可以改写整个 DOM**——这是重头戏，下面单独说。

### 2.2 SPA：为什么 curl 拿到的 HTML 是空壳

【板书：传统页面 vs SPA】

现在的网站大多是 **SPA（单页应用）**，React/Vue 写的。它的服务端返回的 HTML 长这样：

```html
<!doctype html>
<html>
  <body>
    <div id="root"></div>
    <script src="/bundle.js"></script>
  </body>
</html>
```

看——`<div id="root">` 是**空的**。所有真实内容，是浏览器下载 `bundle.js` 之后、由 JS 执行后动态填进去的。

【提问互动：那我们用 HTTP 客户端直接 GET 这个页面，拿到什么？】

对，一个空壳。这就是为什么"解析网页"这件事光靠 HTTP 抓取是不够的——现代互联网上一大半页面是这种形态。这也是 web-reader 需要**两个引擎**的根本原因：轻量引擎处理服务端渲染的页面，浏览器引擎处理 JS 壳页面。

### 2.3 Readability：Firefox 阅读模式背后的算法

大家用 Safari 或者 Firefox 的时候可能点过"阅读模式"——一点，页面上只剩正文，排版干干净净。它背后就是一个开源算法，叫 **Readability**（Mozilla 出的，npm 包 `@mozilla/readability`）。

原理一句话：**给 DOM 节点打分**。文本密度高的祖先节点（段落多、字数多、链接少）得分高，最后取得分最高的子树当正文。它对"新闻文章、博客"这类页面效果极好。我们直接复用这个库，外面再包一层自己的清洗规则。

### 2.4 为什么输出 Markdown

三个理由：**token 效率高**（比 HTML 少一个量级）；**结构保留**（标题层级、列表、代码块，对下游检索和引用定位很关键）；**LLM 对它最熟**（训练语料里 Markdown 含量极高，模型理解它几乎零成本）。

好，前端补课结束。总共就四个词：**DOM 是对象树、CSS 会藏东西、JS 会造内容、Readability 会打分**。后面全用得上。

---

## 第 3 节 整体架构与接口契约（7 分钟）

### 3.1 在仓库里的位置

【画架构图】

```
┌────────────────────────── nano-notebook (Go) ──────────────────────────┐
│                                                                        │
│  cmd/worker ──▶ internal/sourceprocessing ──▶ internal/normalize       │
│      │                │  HTML 质量门不过时                    │                │
│      │                ▼ 兜底调用                                ▼                │
│      │         internal/webreader (Go HTTP 适配器)              artifact        │
└──────┼──────────────────┬─────────────────────────────────────────┘
       │                  │  POST /v1/parse (HTTP + Bearer token)
       ▼                  ▼
┌────────────────── services/web-reader (TypeScript sidecar) ────────────┐
│  HTTP 服务 ─▶ 引擎编排(engine) ─┬─▶ 轻量引擎: fetcher ─▶ reader(解析)   │
│                                └─▶ 浏览器引擎: browser(puppeteer) ─▶    │
│                                    reader(解析)                         │
└─────────────────────────────────────────────────────────────────────────┘
```

几个架构决策，讲给后端听很自然：

1. **Sidecar 模式**：web-reader 是独立进程，跟 Go 服务同机部署、HTTP 通信。为什么不是 Go 里嵌一个库？因为浏览器引擎要拖着 Chromium 跑——这是个重依赖，动辄几百 MB 内存、还会崩。**独立进程 = 故障隔离**：Chromium 崩了只影响这一个容器，worker 主流程无感。
2. **为什么放 `services/` 而不是 `internal/`**：`internal/` 是 Go 的地盘（Go 工具链的语义：internal 包外部不可 import），web-reader 是 TypeScript，语言生态、构建链、部署形态都不同，单独一个目录互不干扰。
3. **语言选 TS**：不是赶时髦——这个领域的成熟工具链（puppeteer、readability、turndown）全在 Node 生态，属于"站在巨人肩膀上"。

### 3.2 HTTP 契约

接口就两个，非常克制：

```
GET  /health/live      存活检查
POST /v1/parse         解析网页
```

`POST /v1/parse` 的请求体：

```json
{
  "url": "https://example.com/post",
  "format": "markdown",        // markdown | text | html
  "with_links": true,
  "with_images": true,
  "max_chars": 250000
}
```

响应体（节选，完整契约 Go 侧有结构体镜像）：

```json
{
  "schema_version": "1",
  "url": "https://example.com/post",
  "final_url": "https://example.com/post",   // 重定向后的最终地址
  "title": "...",
  "content": "# 标题\n\n正文...",             // 干净正文
  "engine": "lightweight",                    // lightweight | browser
  "upgraded": false,                          // auto 模式是否升级过
  "word_count": 1234,
  "truncated": false,
  "fetch": { "status": 200, "bytes": 88000, "redirects": 1 }
}
```

`engine` 和 `upgraded` 这两个字段是排障利器——调用方一眼就能看出"这个结果是谁产出的、是不是升级重试来的"。

### 3.3 错误契约

【切到 `src/errors.ts`，12 个错误码一屏展示】

所有失败都是稳定结构 `{"error":{"code","message"}}`，code 是**稳定契约**，一共 12 个，每个映射固定 HTTP 状态码。后端同学重点看这几个：

| code | HTTP | 含义 |
|---|---|---|
| `unsafe_destination` | 422 | SSRF 防护拦截（私有地址等） |
| `upstream_failed` | 502 | 上游网页抓取失败 |
| `unsupported_type` | 415 | 响应不是 HTML |
| `parse_failed` | 422 | 解析不出正文 |
| `engine_unavailable` | 503 | 浏览器引擎不可用 |
| `service_busy` | 503 | 并发打满 |

这套风格和仓库里已有的 Go sidecar（source-fetcher、document-renderer）完全对齐——**这是刻意的，降低调用方心智**。

### 3.4 鉴权

生产模式下要带 `Authorization: Bearer <token>`（token 来自环境变量 `NANO_WEB_READER_SERVICE_TOKEN`）。服务端用 SHA-256 + `timingSafeEqual` 比较，防时序攻击。本地裸跑不设 token 就自动关闭鉴权，方便调试。

【过渡句】接口看完了，接下来钻进去：一个 URL 进来，正文是怎么被"洗"出来的。

---

## 第 4 节 解析管线（9 分钟）

【画管线图】

```
HTML 字符串
  │ ① jsdom 解析（不执行脚本、不加载资源）
  ▼
DOM 树
  │ ② 预清洗 preClean（删噪音）
  ▼
干净一点的 DOM
  │ ③ Readability 打分提取
  ▼
正文子树 HTML ──(不足 60 字符)──▶ 回退：清洗后的 <body>
  │ ④ 渲染输出
  ▼
markdown / text / html
```

【切到 `src/reader.ts`，对着 `parsePage` 讲】

### ① jsdom 解析

jsdom 是 Node 里的纯 JS DOM 实现——**不开浏览器、不执行页面脚本、不发网络请求**，只做"HTML 字符串 → DOM 树"这一件事。快，毫秒级，而且安全（页面的恶意 JS 根本没机会跑）。

### ② 预清洗 preClean

这一步是我们自己的规则，在 Readability 之前先把明显的垃圾删掉：

- **整类删除的标签**：`script`、`style`、`noscript`、`iframe`、`form`、`button`、`nav`、`aside`、`footer`……（完整清单在 `reader.ts` 顶部的 `REMOVABLE_TAGS`）
- **隐藏元素**：带 `display:none` / `visibility:hidden` / `aria-hidden` 的——还记得第 2 节说的"CSS 会藏东西"吗？就是删这些
- **噪声容器**：class 或 id 里含 `sidebar`、`advert`、`cookie`、`newsletter`、`related`、`comment` 这类词的 div/section（`NOISE_CLASS_TOKENS`）——这是业界通行做法，前端起 class 名是有"方言"的，广告位就叫 ad、赞助位就叫 sponsor
- **ARIA 角色**：`role=navigation`、`role=banner` 这类无障碍标记，反过来告诉我们"这是导航不是正文"

【经验之谈，可以讲个小段子】规则式清洗的特点：单条规则都不完美，但组合起来对 90% 的页面有效，而且**确定、可测、零成本**。剩下 10% 的疑难杂症交给 Readability 和兜底逻辑。

### ③ Readability 提取 + 回退

```typescript
const article = new Readability(doc).parse();
// article.content = 正文子树的 HTML
```

如果 Readability 返回空、或者提取结果**不足 60 个字符**（`MIN_CONTENT_CHARS`，和 Go 侧 normalize 的质量下限对齐），就回退用清洗后的整个 `<body>`——宁可用次优结果，不用空结果。

### ④ 渲染输出

【切到 `src/markdown.ts`】

Markdown 转换用 turndown（HTML→Markdown 的规则引擎）+ GFM 插件（表格、删除线、任务列表），外面还包了从 jina-ai/reader 移植的 `tidyMarkdown` 后处理：压缩多余空行、规整列表标记、把链接和图片地址**基于 `base_url` 绝对化**（页面里的 `./img/a.png` 变成 `https://site.com/img/a.png`，不然下游拿到的全是死链）。

【小结过渡】到这里，"服务端渲染的页面"就处理完了。但还记得第 2 节的 SPA 空壳吗？这种页面走完整管线，Readability 也只能提出个寂寞——这就是双引擎要解决的问题。

---

## 第 5 节 双引擎与 auto 升级（7 分钟）

【切到 `src/engine.ts`】

配置项 `NANO_WEB_READER_ENGINE` 三选一：

- **lightweight**：只用 HTTP 直取。快、省资源，但搞不定 JS 页面。
- **browser**：一律用浏览器渲染。能搞定一切，但慢（秒级）、贵（一个 Chromium 实例常驻，内存几百 MB 起）。
- **auto（默认）**：先轻量，结果"看起来不对"再升级浏览器重试。**这是我们最推荐的姿势**。

### auto 的决策逻辑

【对着 `readPage` 函数讲，画决策树】

```
轻量尝试
  ├─ 抛错？
  │    ├─ parse_failed ──────────────▶ 可恢复，升级
  │    ├─ upstream_failed(非超时) ───▶ 可恢复（bot wall 403 之类），升级
  │    └─ 超时 / unsafe_destination ─▶ 不可恢复，直接失败（换引擎也救不了）
  ├─ 成功但 word_count < 100 ────────▶ 内容太薄，怀疑是 JS 壳，升级
  └─ 成功且够厚 ────────────────────▶ 直接返回
```

三个细节，体现工程上的克制：

1. **超时不重试**：上游都超时了，换个更慢的引擎只会更糟。
2. **安全判定永不重试**：`unsafe_destination` 是 SSRF 拦截，换引擎结果也一样——安全结论不能被"重试"稀释。
3. **升级后还要比武**：浏览器结果不一定更好（有些站对无头浏览器返回验证页）。所以升级成功后比较两个结果的 `word_count`，**取内容更多的那个**。

### 并发控制

浏览器引擎有独立信号量（`Semaphore`，默认并发 2，配置 `NANO_WEB_READER_BROWSER_MAX_CONCURRENT`）。auto 模式下如果浏览器槽满了，**降级返回轻量结果**而不是报错——尽量给调用方一个答案。

### 浏览器引擎内部

【切到 `src/browser.ts`，快讲，细节第 6 节还会回来】

- puppeteer-core 驱动**系统 Chromium**（容器里装的，不是 npm 下载的那种）——镜像可控、体积可控
- 浏览器实例**全局共享、懒加载**：第一个请求触发启动，之后复用；断了自动重连
- 页面加载策略：`domcontentloaded`（DOM 就绪就够，不等所有图片）→ `waitForNetworkIdle`（等 XHR 静默 800ms，给前端框架 hydrate 的时间）→ 再固定等 300ms → `page.content()` 取最终 DOM 的 HTML 快照
- 拿到的 HTML **复用第 4 节的同一套解析管线**——两个引擎只负责"搞到 HTML"，洗正文是同一条路，行为一致

【过渡句】注意刚才 browser.ts 里我们刻意跳过了一段——每个页面装了一个"请求拦截器"。它是干什么 的？这就要讲今天的重头戏之一：SSRF。

---

## 第 6 节 SSRF 防护（8 分钟）

【这是后端听众最能共鸣的一节，讲深一点】

### 6.1 什么是 SSRF，为什么这个服务首当其冲

SSRF（Server-Side Request Forgery）：**任何"你给我 URL、我替你发请求"的服务，都是一个潜在的 内网跳板**。

web-reader 恰好就是这么一个服务。想象它被恶意调用的场景：

```
POST /v1/parse {"url": "http://169.254.169.254/latest/meta-data/iam/..."}
```

这是**云厂商的 metadata 端点**——如果 web-reader 跑在云主机上、又没做防护，攻击者就能借你的手拿到机器的角色凭证。类似的还有 `http://localhost:8080/admin`（探测内网管理面板）、`http://10.x.x.x/`（内网端口扫描）。

所以对这个服务来说，**SSRF 防护不是安全加分项，是功能的一部分**。

### 6.2 三层防线（轻量引擎）

【切到 `src/fetcher.ts`，画三层漏斗】

**第一层：URL 静态校验**（`validateUrl`）
只允许 http/https、禁止 userinfo（`http://user:pass@host` 的形态）、禁止 fragment、校验端口范围。纯语法层，最快。

**第二层：DNS 解析后校验 IP**（`validatingLookup`）
这是关键设计。我们**接管了 Node HTTP 客户端的 DNS 解析环节**：把自定义 `lookup` 函数注入 `http.request`。流程：

```
要连 example.com
  → 我们自己 dns.lookup，拿到所有 A/AAAA 记录
  → 逐个对照 blocklist 校验
  → 全部是公网地址才放行
  → 把已验证的 IP 交给 TCP 层拨号
```

Blocklist 在 `src/ip.ts`（从 jina-ai/reader 移植并加固）：私网段（10/8、172.16/12、192.168/16）、环回（127/8）、链路本地（169.254/16，**metadata 端点就在这**）、CGNAT（100.64/10）、IPv4-mapped IPv6（`::ffff:127.0.0.1` 这种伪装形态，jina 原版解析有 bug 我们修了）、保留段、文档段。

**第三层：重定向逐跳校验**
跟随重定向时，每一跳的 Location 都重新走一遍 `validateUrl` + IP 校验。不然攻击者用公网域名 302 跳内网，一层校验就白做了。

这个设计有个精妙之处：**拨号的目标就是我们验证过的那个 IP**，中间没有第二次 DNS 解析——不存在"验证时是公网、连接时被换成内网"的 TOCTOU 窗口。后端同学可以把它理解成：**DNS 校验和 TCP connect 绑定在同一个回调里原子完成**。

### 6.3 浏览器引擎的麻烦

轻量引擎能注入 lookup，**Chromium 不行**——它自己解析 DNS，我们插不进手。怎么办？两头夹击：

1. **导航前预检**：页面打开前，先把目标域名按同样规则校验一遍
2. **请求拦截器**：`page.setRequestInterception(true)` 之后，Chromium 发起的**每一个请求**——主文档、每一跳重定向、每个子资源（图片、XHR、字体）——都先经过我们的回调：解析这个请求的域名 → 公网才 `continue()`，否则 `abort()`

【指 `browser.ts` 的 `installSsrfGuard`】

校验结果按 render 缓存在一个 Map 里（`verdicts`），页面关闭缓存即失效——**避免跨请求复用旧 DNS 结论，堵 DNS rebinding 的时间窗**：攻击者第一次解析返回公网 IP、 TTL 过期后换成内网 IP。当然，拦截器校验和 Chromium 实际连接之间理论上仍有极小窗口，这是无头浏览器方案公认的残余风险，生产上由**容器出口网络策略**兜底（ADR-0032 的立场）。

【小结】安全设计的原则就一句话：**纵深防御，每层都假设上一层会被绕过**。

---

## 第 7 节 本次增强：Go 侧接入 + 一个有趣的 bug（10 分钟）

【重点节，对应 commit `4af7604` 和 `4d76b61`】

前面 6 节讲的是 web-reader 这个服务本身。接下来是最近做的增量：**把它接进 Go 侧的 worker 流水线，作为 HTML 解析的兜底**。分三块讲：动机、Go 适配器设计、一个调试了两天的 bug。

### 7.1 动机：质量门与"该重试却没得重试"

【画流水线图】

worker 处理 HTML 来源的路径是 `internal/normalize` 的 `html-primary-v2`——一套**纯确定性的**提取：DOM 树上选主文档、抽文本、组装成 block。它有一道质量门（`ErrHTMLQuality`），以下情况直接判死：

- 找不到主文档
- 正文太短（低于最小 rune 数）
- 疑似登录页/错误页（文本特征匹配）
- 正文几乎全是链接（链接文本占比 ≥ 80%）
- 大量重复内容

问题在哪？**被拒的来源里，很大一部分恰恰是 web-reader 最擅长救的**：

| 被质量门拒掉的形态 | web-reader 为什么能救 |
|---|---|
| JS 壳页面（`<div id="root">` 空 div） | 浏览器引擎渲染后内容就出来了 |
| Bot wall（对无 UA 的请求返回拦截页） | 浏览器引擎带真实 UA + 完整指纹 |
| 服务端 HTML 太薄 | Readability 连同元数据一起榨 |

但当时 Go 侧的处理是：质量门一拒，这个 source 就失败了，**没有第二次机会**。所以这次增强就是补上这条兜底路径。

### 7.2 兜底逻辑：sourceprocessing 里的 40 行

【切到 `internal/sourceprocessing/processor.go`，搜 `readViaWebReader`】

核心改动在 `NativeExtractor.Extract` 的 HTML 分支：

```go
case source.FormatHTML:
    input.ExtractionConfigID = htmlPrimaryExtractionConfigID   // "html-primary-v2"
    artifact, err := normalize.HTML(input)
    if err == nil {
        return artifact, nil
    }
    // 只有质量门拒绝才值得让 web-reader 试；
    // 结构性错误（预算超限、非 UTF-8）重试没有意义
    if !errors.Is(err, normalize.ErrHTMLQuality) {
        return normalize.Artifact{}, err
    }
    return e.readViaWebReader(ctx, item, err)
```

【对着讲四个设计决策，每个都有后端可以带走的通用原则】

**决策一：只重试"可救"的失败**。`ErrHTMLQuality` 才走兜底；非 UTF-8、超预算这类结构性错误直接失败。**重试要区分错误类型**——这是所有 fallback 设计的通用原则。

**决策二：兜底也有下限**。web-reader 返回的内容不足 60 rune，视为"救失败了"，返回原始错误。不能"为了成功而成功"，把一个空壳换个姿势塞进知识库。

**决策三：错误链保留**。所有失败路径都用 `%w` 包装原始的 `ErrHTMLQuality`，调用方 `errors.Is` 依然能判别失败类型。**fallback 失败时，根因永远是第一现场**。

**决策四：证据链与产物分离**。兜底成功后，产物打上新的提取配置 `html-reader-v1`、格式是 markdown，走 `normalize.Text` 重新归一化。原始 HTML 字节和它的 SHA256 **原样保留**——推导内容单独记账，将来审计/回滚有据可查（仓库 ADR-0021 的约定）。

另外注意一个前置条件：兜底要求 `item.FinalURL` 非空（来源抓取时记录的最终 URL，数据库里新加读的字段）。没有 URL 就没法让 web-reader 重抓，保持原失败。

### 7.3 Go 适配器：internal/webreader

【切到 `internal/webreader/webreader.go`】

这个包是标准的**端口-适配器**：对外暴露 6 行的 `Adapter` interface，worker 依赖接口，测试注入 fake——大家天天写的模式，不展开。重点讲 HTTPAdapter 里几个"防坑"细节，都是后端视角的干货：

1. **响应体大小有上限**：`io.LimitReader(body, MaxChars*4 + 1MB)`。为什么 ×4？请求的是 25 万**字符**，UTF-8 最坏情况一个字符 4 **字节**——按字符限流的服务，按字节收就要乘上最坏系数，不然 LimitReader 会把合法长响应误杀。
2. **严格 JSON 解码**：`DisallowUnknownFields()` + 解码第二个值断言 `io.EOF`（拒绝尾随多余 JSON）。这是在**把 sidecar 的响应 schema 当契约管理**——TS 侧多返回一个字段、少返回一个字段，Go 侧立即在解码层报错，而不是让脏数据静默流下去。这跟第 3 节讲的 schema_version 校验是配套的。
3. **语义校验**：schema_version 必须 "1"、format 必须等于请求的 format、engine 非空、content 非空、计数器非负。**解码成功 ≠ 数据合法**。
4. **请求侧同样校验**：`Request.Validate()` 检查 URL 语法、format 固定 markdown、MaxChars 在 1..25 万——纵深防御，Go 侧不依赖 TS 侧替它把关。

装配在 `cmd/worker/main.go`：环境变量 `NANO_WEB_READER_URL`（默认 `http://127.0.0.1:8085`，**置空即整体禁用兜底**，回到旧行为）、`NANO_WEB_READER_SERVICE_TOKEN`、`NANO_WEB_READER_TIMEOUT`（默认 90s——要覆盖浏览器引擎最坏耗时）。HTTP client 挂了 otelhttp Transport，调用链路有 trace。

### 7.4 一个有趣的 bug：Happy Eyeballs（commit `4d76b61`）

最后讲这个，因为它是**典型的"跨层契约"bug**，后端同学以后写 Node 网络代码很可能撞上。

背景：第 6 节说我们把自定义 `validatingLookup` 注入 `http.request`。上线后发现部分站点随机报 `Invalid IP address`，而 DNS 明明解析正常。

排查结论，三层：

1. Node 的 net 模块默认开了 **autoSelectFamily**，也就是 **Happy Eyeballs** 算法——同时向 IPv4 和 IPv6 发起连接，谁先通用谁。这是 RFC 8305，浏览器界用了十几年，Node 20+ 默认开启。
2. 开了 Happy Eyeballs 后，net 调用 lookup 时会传 `{ all: true }`，**期望回调收到"地址数组"**——这是 `dns.lookup` 的标准契约。
3. 而我们的旧实现按"单地址"签名回调：`callback(null, address, family)`。net 拿到一个本该是数组的值，**把地址字符串当成数组逐字符迭代**——`2`, `.`, `0`, `.`, `2`, `1`……每个"元素"都不是合法 IP，于是报 Invalid IP address。

修复很小：尊重调用方的 `options.all`，要列表就给列表：

```typescript
if (options.all) {
  callback(null, addresses);   // 完整地址列表，逐个都已过 SSRF 校验
  return;
}
callback(null, first.address, first.family);  // 单地址调用方
```

【带给听众的 takeaway】**给库写回调，签名不是看文档里"你该怎么写"，而是看调用方实际传了什么 options**。回调的本质是你和框架之间的协议，协议要看双方。这类 bug 的特征也记一下：错误信息驴唇不对马嘴（DNS 好好的却报 Invalid IP）、随机出现（只有走 Happy Eyeballs 路径的请求才触发）。

---

## 第 8 节 测试与部署（3 分钟）

【快速过，给 Interested 的人指路】

**测试**（`services/web-reader/test/`，node:test，无第三方框架）：
- ip / markdown / reader：纯函数单测，秒级
- fetcher / server / engine：注入 fake 依赖的行为测试（server 测试用真端口起 HTTP）
- browser：集成测试，**本机没浏览器自动 skip**，不阻塞 CI
- Go 侧：`webreader_test.go`（请求校验、解码严格性）、`processor_webreader_test.go`（5 个场景：兜底触发/不触发/失败保留根因/无 URL 不兜底/结构性错误不重试——把第 7 节讲的决策每个都钉死）

**部署**（`infra/web-reader/Dockerfile` + compose）：
- 多阶段构建：build（编译 TS）→ prune（裁 devDependencies）→ runtime（`node:22-bookworm-slim` + 系统 Chromium + 字体）
- 运行时加固：非 root 用户、**只读根文件系统**、tmpfs 挂 /tmp（Chromium 的 profile 和缓存在这，`HOME=/tmp`）、`cap_drop: ALL`、`no-new-privileges`
- 资源画像按浏览器进程簇设计：`pids_limit: 256`（Chromium 是一主多子进程）、`mem_limit: 2g`、端口只绑 `127.0.0.1`
- 版本管理：`.nvmrc` 锁 Node 22，Dockerfile 基础镜像同样锁 `node:22`

---

## 第 9 节 总结（3 分钟）

【收尾，语速放慢】

一小时信息量不小，收成三句话：

1. **分层解耦**：抓取（两个引擎）和解析（一条管线）分开；轻量优先、按需升级、处处可降级。性能和覆盖率不是二选一，是分级提供。
2. **安全内建**：SSRF 防护贯穿到 DNS 解析层和浏览器请求层，纵深防御。凡是"替别人发请求"的服务，这类防护都是第一天就要做的。
3. **契约严格**：稳定错误码 + schema_version + Go 侧 DisallowUnknownFields，跨语言边界的每一处都当契约管理，漂移当场暴露而不是静默腐烂。

代码入口就三处：想看解析看 `reader.ts`，想看编排看 `engine.ts`，想看 Go 接入看 `internal/webreader/` 和 `processor.go` 的 `readViaWebReader`。

下面是 Q&A。

---

---

## 附录 A：现场演示脚本

> 提前起服务：`cd services/web-reader && npm run build && npm start`（无 token 模式）。
> PowerShell 注意用 `curl.exe`（不是 `curl` 别名）或 `Invoke-RestMethod`。

**演示 1：健康检查（30 秒）**

```bash
curl http://127.0.0.1:8085/health/live
# {"status":"live","service":"web-reader"}
```

**演示 2：轻量解析（1 分钟）**

```bash
curl http://127.0.0.1:8085/v1/parse \
  -H "Content-Type: application/json" \
  -d '{"url": "https://example.com", "format": "markdown"}'
```

讲点：看响应里的 `engine: "lightweight"`、`word_count`、`content`——对比浏览器里 F12 看到的原始 HTML，噪音全没了。

**演示 3：SPA 页面触发升级（2 分钟）**

```bash
curl http://127.0.0.1:8085/v1/parse \
  -H "Content-Type: application/json" \
  -d '{"url": "https://react.dev", "format": "markdown"}'
```

讲点：观察 `engine` 和 `upgraded` 字段。若本机未配浏览器（`NANO_WEB_READER_BROWSER_EXECUTABLE` 指向 Chrome/Edge），此演示展示降级路径也很直观。

**演示 4：SSRF 拦截（1 分钟，最受欢迎的环节）**

```bash
curl http://127.0.0.1:8085/v1/parse \
  -H "Content-Type: application/json" \
  -d '{"url": "http://127.0.0.1:8085/health/live"}'
# {"error":{"code":"unsafe_destination","message":"..."}}
```

讲点：让它"自己解析自己"都不行——环回地址在 blocklist 里。云 metadata 端点 `169.254.169.254` 同理。

**演示 5（可选）：Go 侧兜底**

讲 `processor_webreader_test.go` 的第一个测试即可，跑 `go test ./internal/sourceprocessing/ -run WebReader -v`，对着输出讲"JS 壳被质量门拒 → web-reader 救回 → 产出 html-reader-v1"。

---

## 附录 B：Q&A 预案

**Q：为什么不用 Go 生态的库（goquery、chromedp）一步到位？**
A：goquery 只解决了"静态解析"，没有等价 Readability 的成熟正文算法（要自己维护打分规则）；chromedp 驱动浏览器可行，但 Readability/turndown 这套工具链在 Node 生态是事实标准、经过海量页面验证。另外独立 sidecar 的故障隔离价值不变。本质是"复用最成熟的实现"。

**Q：和直接用 jina-ai/reader 有什么区别？为什么自建？**
A：实现思路（Readability + 双引擎 + SSRF IP 校验）确实参考并移植了它，差异在：自托管（数据不出内网、成本固定）；错误契约与仓库 Go sidecar 对齐；SSRF 规则更严（修了它的 IPv4-mapped IPv6 解析 bug、补了保留段/文档段）；去掉不需要的平台特性（缓存、SSE、魔法参数），可维护面更小。

**Q：DNS rebinding 能彻底防住吗？**
A：轻量引擎可以——校验和拨号在同一个 lookup 回调里，连接的就是验证过的 IP。浏览器引擎有残余 TOCTOU 窗口（拦截器校验和 Chromium 实际连接之间），业界公认无解，靠容器出口网络策略兜底（拒绝访问内网段）。

**Q：性能怎么样？**
A：轻量引擎典型几百毫秒（受上游和网络支配）；浏览器引擎秒级（domcontentloaded + 网络静默等待）。并发：总并发默认 8，浏览器槽默认 2。auto 模式下大部分流量走轻量，浏览器只在必要时启用。

**Q：为什么 Go 侧响应上限是 MaxChars*4 + 1MB？**
A：MaxChars 限的是字符数，UTF-8 单字符最多 4 字节，所以字节上限要乘 4；+1MB 是 JSON 结构本身的开销余量。不这么放大会误杀中文等多字节内容的合法响应。

**Q：web-reader 挂了会怎样？**
A：Go 侧兜底失败时保留原始 `ErrHTMLQuality` 错误，source 走原有失败路径（重试/标记失败），worker 主流程不受影响——sidecar 的可用性不构成主链路的单点。

**Q：为什么 worker 的兜底超时给到 90 秒？**
A：要覆盖浏览器引擎最坏路径：30s 页面加载 + 重定向 + 网络，加上排队。兜底是"尽力而为"路径，宁可给足时间也不轻易放弃一次可救活的来源。

**Q：怎么判断一个页面该走哪个引擎？能不能提前分类？**
A：可以（特征：script 大小、框架指纹），但 auto 的"事后判断"（先抓、看结果薄不薄）更鲁棒——不依赖对页面类型的预测，直接看产出质量。预测会错，结果不会。

---

## 附录 C：关键文件地图

| 文件 | 职责 | 讲课对应节 |
|---|---|---|
| `services/web-reader/src/server.ts` | HTTP 服务、鉴权、并发闸 | 3 |
| `services/web-reader/src/config.ts` | 全部环境变量与默认值 | 3 |
| `services/web-reader/src/errors.ts` | 12 个稳定错误码 | 3 |
| `services/web-reader/src/fetcher.ts` | 抓取、重定向、SSRF lookup 注入 | 4、6、7.4 |
| `services/web-reader/src/reader.ts` | 解析管线（清洗 + Readability + 回退） | 4 |
| `services/web-reader/src/markdown.ts` | HTML→Markdown 规则引擎 | 4 |
| `services/web-reader/src/engine.ts` | 双引擎编排、auto 升级、降级 | 5 |
| `services/web-reader/src/browser.ts` | puppeteer 渲染、请求拦截 SSRF 防护 | 5、6 |
| `services/web-reader/src/ip.ts` | IP 解析与非公网段 blocklist | 6 |
| `internal/webreader/webreader.go` | Go HTTP 适配器（严格解码） | 7.3 |
| `internal/sourceprocessing/processor.go` | 质量门兜底 `readViaWebReader` | 7.2 |
| `cmd/worker/main.go` | 装配（URL/token/timeout 环境变量） | 7.3 |
| `infra/web-reader/Dockerfile` | 多阶段构建与运行时加固 | 8 |
| `infra/compose/compose.yaml` | 开发编排（含资源限制） | 8 |

**对应 commit**：
- `a916d2d` web-reader 服务本体（双引擎 + SSRF + 解析管线 + 部署）
- `4d76b61` fetcher Happy Eyeballs lookup 兼容修复
- `4af7604` worker/sourceprocessing 接入 web-reader 兜底

---

## 附录 D：术语速查（发给听众）

| 术语 | 一句话解释 |
|---|---|
| DOM | 浏览器把 HTML 解析成的对象树，可被代码读写 |
| SPA | 页面内容由 JS 在浏览器里动态生成的网站形态 |
| jsdom | Node 里纯 JS 的 DOM 实现，不开浏览器 |
| Readability | Firefox 阅读模式的正文提取算法（文本密度打分） |
| turndown | HTML 转 Markdown 的规则引擎 |
| puppeteer | 用代码驱动 Chrome/Chromium 的库（DevTools 协议） |
| SSRF | 诱导服务端向内网发起请求的攻击 |
| DNS rebinding | DNS 结果在"校验时"和"连接时"之间被调包的攻击 |
| Happy Eyeballs | IPv4/IPv6 并发竞速连接（RFC 8305） |
| TOCTOU | Time-Of-Check 到 Time-Of-Use 之间的窗口期风险 |
| Sidecar | 与主服务同机部署、通过本地 HTTP 协作的辅助进程 |
| 质量门 / ErrHTMLQuality | Go 侧确定性 HTML 提取的合格性判定 |
