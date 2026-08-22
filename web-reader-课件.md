---
marp: true
theme: default
paginate: true
headingDivider: false
style: |
  section { font-size: 24px; }
  section h1 { font-size: 40px; color: #1a5276; }
  section h2 { font-size: 32px; color: #1a5276; }
  code { font-size: 18px; }
  pre { font-size: 17px; }
  table { font-size: 20px; }
---

<!-- _class: lead -->

# web-reader：网页正文提取服务

## 从一个 URL 到 LLM 可用的干净正文

项目实战课，约 70 分钟

代码在 `services/web-reader/`（TypeScript）和 `internal/webreader/`（Go）

---

# 这节课讲什么

一个把网页变成干净正文的服务，怎么从零设计出来。

1. 为什么 LLM 应用需要正文提取这一层
2. web-reader 的整体架构：两个抓取引擎，一条解析管线
3. 几个关键设计决策：为什么是独立进程、为什么引擎分级、错误码为什么定得这么死
4. 正文提取的难点，和"规则 + 算法 + 兜底"的处理思路
5. SSRF 攻击是怎么回事，这个服务怎么防
6. 两个真实案例：Go 侧兜底接入，和一个排查了很久的 bug

函数名不用记。记住的是每个决策背后的原因。

---

<!-- _class: lead -->

# Part 1

## 问题背景：为什么需要这个服务

约 7 分钟

---

# 让 AI 读懂一个网页，难在哪

nano-notebook 的核心链路：

```
用户添加来源(URL) → 抓取 → 解析提取 → 归一化 → 知识库 → LLM 检索问答
```

最直接的做法是把抓到的 HTML 原样给大模型。问题：

| 问题 | 后果 |
|---|---|
| HTML 里大部分是噪音（导航/广告/脚本） | 浪费 token |
| 正文淹没在标签嵌套里 | 检索命中率下降 |
| 页面结构千奇百怪 | 下游没法统一处理 |

所以中间要加一层：输入 URL，输出干净正文。

---

# 看看真实网页长什么样

随便打开一个新闻页，右键"查看源代码"：

```html
<body>
  <nav>首页 | 新闻 | 财经 | 体育 | ...（40 个链接）</nav>
  <div class="ad-banner">广告...</div>
  <aside class="sidebar related-posts">推荐阅读...</aside>
  <div class="cookie-consent">我们使用 Cookie...</div>
  <article>                      ← 正文在这
    <h1>标题</h1>
    <p>（可能只占页面体积的 10%~20%）...</p>
  </article>
  <footer>版权所有 © ...</footer>
  <script>统计埋点、广告SDK、A/B实验...</script>
</body>
```

web-reader 做的事：把 `<article>` 里的东西留下来，其余扔掉。

---

# 为什么自建，不继续调第三方 API

项目早期用的是 jina reader 的云 API，三个原因促成了自建：

1. 成本：按量计费，量上去之后很贵
2. 合规：用户数据要出内网，过不了安全审查
3. 可控性：第三方故障就是我们的功能故障

自建的路线是参考开源的 jina-ai/reader（这个领域的标杆项目），
移植它的核心思路，按自己仓库的规范重写。

复用现成的算法（Readability、SSRF 规则），但每一行代码都要自己
看得懂、改得动、负得起责。

---

<!-- _class: lead -->

# Part 2

## 前置知识：后端视角看网页

约 8 分钟。这节是后面所有内容的基础。

---

# HTML 和 DOM

HTML 是声明式的结构文档，可以类比成 proto 定义的序列化文本：

```html
<article>
  <h1>标题</h1>
  <p>正文第一段</p>
</article>
```

DOM 是浏览器解析 HTML 之后得到的运行时对象树，类比成反序列化出来的
message 实例，可以用代码增删改查：

```
Document
 └─ body
     └─ article
         ├─ h1  → "标题"
         └─ p   → "正文第一段"
```

提取正文，本质就是在这棵树上找到对的那棵子树。

---

# 两个会影响我们的前端特性

特性一：CSS 会隐藏内容

```html
<div style="display:none">用户看不见，但 DOM 里有</div>
```

爬下来一堆这种"隐形垃圾"，解析时得删掉。

特性二：JS 会凭空造内容。这个下一页单独说，它直接决定了架构。

---

# SPA：为什么 curl 拿到的是空壳

现代网站大多是 SPA（单页应用），服务端返回的 HTML 长这样：

```html
<!doctype html>
<html>
  <body>
    <div id="root"></div>          <!-- 空的 -->
    <script src="/bundle.js"></script>
  </body>
</html>
```

所有真实内容，都是浏览器执行 JS 之后动态填进 `#root` 的。

### 思考题

用 HTTP 客户端直接 GET 这种页面，拿到的是什么？这说明光靠 HTTP
抓网页够不够？

（空壳。这就是这个服务需要两个引擎的原因。）

---

# Readability：Firefox 阅读模式背后的算法

Firefox / Safari 的阅读模式大家都用过——点一下，页面只剩正文。

背后的开源算法叫 Readability（Mozilla 出品，`@mozilla/readability`），
思路是给 DOM 节点打分：

```
文本密度高（段落多、字数多）
+ 链接密度低（不是导航列表）
+ 语义标签加分（<article>、<main>、<p>）
+ class/id 暗示加分（"content"、"post"）
─────────────────────────────
得分最高的祖先子树 = 正文
```

对新闻、文章、博客这类页面效果很好，我们直接用这个库。

---

# 为什么输出 Markdown

| 候选 | 问题 |
|---|---|
| 纯文本 | 丢失结构——标题层级、列表、代码块全没了，检索和引用定位做不了 |
| HTML | token 开销大，噪音残留 |
| Markdown | 结构保留，token 高效，LLM 训练语料里含量高 |

这节的四个词后面会反复用到：

**DOM 是树，CSS 藏东西，JS 造内容，Readability 打分。**

---

<!-- _class: lead -->

# Part 3

## 整体架构

约 12 分钟

---

# 架构总览

```
┌────────────────── nano-notebook 主仓库 (Go) ──────────────────┐
│                                                               │
│  worker ──▶ sourceprocessing ──▶ normalize（确定性提取+质量门）│
│                 │ quality 不过时兜底                            │
│                 ▼                                             │
│          internal/webreader（Go HTTP 适配器）                  │
└───────────────────┬───────────────────────────────────────────┘
                    │ POST /v1/parse  (HTTP + Bearer Token)
┌───────────────────▼───────────────────────────────────────────┐
│           services/web-reader（TypeScript sidecar）           │
│                                                               │
│  HTTP 服务 ──▶ engine 引擎编排 ─┬─▶ 轻量引擎 fetcher ──┐        │
│                                │                      ├─▶ 同一条│
│                                └─▶ 浏览器引擎 browser ─┘   解析管线│
│                                    (puppeteer+Chromium)  reader │
└───────────────────────────────────────────────────────────────┘
```

先记住：2 个抓取引擎，1 条解析管线。

---

# 为什么是独立进程，不做进 Go 里

web-reader 没做成 Go 里的一个库，而是独立的 sidecar 进程。
主要原因是浏览器引擎要拖着 Chromium 跑：

- 内存几百 MB 起，随时可能崩溃
- 崩了如果和 worker 同进程，整个 worker 跟着挂
- 崩了如果是独立容器，只有这个容器重启，主流程无感

重依赖用进程边界隔离，代价是跨进程序列化（HTTP + JSON），
换来的是故障域收敛。这个取舍对浏览器、媒体处理这类不稳定依赖
一般都划算。

目录位置顺带说下：`internal/` 是 Go 工具链语义（外部不能 import），
TS 服务放 `services/`，语言生态和构建部署链不同，物理分开。

---

# 引擎为什么分两级

两种抓取方式的成本差异很大：

| | 轻量引擎（HTTP 直取） | 浏览器引擎（Chromium 渲染） |
|---|---|---|
| 延迟 | 百毫秒级 | 秒级 |
| 资源 | 几乎为零 | 常驻进程 + 几百 MB |
| 覆盖 | 服务端渲染页面 | 一切页面（含 JS 壳） |
| 并发能力 | 高（默认 8） | 低（默认 2） |

全用浏览器，大部分请求在烧钱等渲染；全用轻量，SPA 页面全军覆没。

解法是 auto 模式：轻量优先，结果可疑再升级浏览器重试。Part 5 细讲。

---

# 接口和错误码为什么定得这么死

接口只有两个：

```
GET  /health/live   存活检查
POST /v1/parse      解析网页
```

但失败契约定义得很严格——12 个稳定错误码（`src/errors.ts`）：

```typescript
export type ErrorCode =
  | 'invalid_request'      // 400 参数错误
  | 'unauthorized'         // 401 鉴权失败
  | 'unsafe_destination'   // 422 SSRF 拦截
  | 'response_too_large'   // 413
  | 'unsupported_type'     // 415 非 HTML
  | 'upstream_failed'      // 502 上游失败
  | 'parse_failed'         // 422 提取不出正文
  | 'engine_unavailable'   // 503 浏览器不可用
  | 'service_busy'         // 503 并发打满
  | ...                    // 共 12 个，每个固定映射一个 HTTP 状态码
```

错误码是跨语言协作的公共协议。这套契约和仓库里已有的 Go sidecar
（source-fetcher 等）完全对齐，调用方不用学新东西，监控报警可以
按 code 直接分类。

---

# 契约要机器强制，不能只写在文档里

Go 适配器解码时做了三层检查：

```go
decoder := json.NewDecoder(bytes.NewReader(payload))
decoder.DisallowUnknownFields()          // TS 侧多返回字段 → 立刻报错
var decoded parseResponse
decoder.Decode(&decoded)
decoder.Decode(&struct{}{})              // 尾随多余 JSON → 报错
if decoded.SchemaVersion != "1" || ...   // 语义校验
```

### 思考题

为什么 TS 侧新增返回字段要让 Go 侧报错，而不是静默忽略？
"宽容解析"不是美德吗？

（schema 漂移是渐进发生的。今天静默放过一个字段，半年后没人知道
契约长什么样。长期演进的系统里，Postel 法则是负债。）

---

# 安全这个事，提前说一句

web-reader 的本质是"你给我 URL，我替你发请求"，天然是 SSRF
（服务端请求伪造）的高危目标。

SSRF 防护在这个服务里不是附加的安全项，是功能设计的一部分，
两个引擎的实现里都有。Part 6 详细讲。

到这里，架构讲完了。总结成四句话：

- 重依赖用进程隔离（失败别扩散）
- 引擎分级，按需升级（成本和覆盖率要兼顾）
- 错误码当公共协议管理（且要机器强制）
- 替人发请求的服务，SSRF 防护第一天就要做

---

<!-- _class: lead -->

# Part 4

## 解析管线：从脏 HTML 到干净 Markdown

约 10 分钟。代码：`src/reader.ts`

---

# 难点：如何定义"正文"

这是这个项目第一个本质难点：

- 没有标准：`<article>` 标签？很多页面根本不用
- 没有边界：评论区算不算正文？相关推荐呢？
- 没有唯一解：同一页面，不同算法提取结果不同

工程上的答案是三层组合，各管一段：

```
规则清洗（快、确定、覆盖大部分明显噪音）
  + Readability 打分（算法解决剩下的模糊地带）
  + 质量门 + 兜底（最后的保险丝）
```

---

# 管线全景（`parsePage` 函数）

```
HTML 字符串
  │ ① jsdom 解析（不开浏览器、不执行页面 JS、不发请求）
  ▼
DOM 树
  │ ② preClean 预清洗（删噪音）
  ▼
较干净的 DOM
  │ ③ Readability 打分提取
  ▼
正文子树 HTML ──(正文 < 60 字符)──▶ 回退：用清洗后的 <body>
  │                                        │
  │ ④ 渲染输出                     (仍 < 60 字符 → 抛 parse_failed)
  ▼
markdown / text / html
```

①为什么用 jsdom：纯 JS 的 DOM 实现，毫秒级，而且页面的恶意脚本
根本没机会执行。

---

# ② 预清洗：四类噪音

```typescript
// 一、整类删除无语义标签（reader.ts REMOVABLE_TAGS）
script, style, noscript, iframe, form, button, nav, aside, footer...

// 二、删隐藏元素（CSS 藏的东西不是给人看的）
[hidden], [aria-hidden="true"], style~="display:none"...

// 三、按 class/id 语义删容器
// NOISE_CLASS_TOKENS:
advert, ads, sidebar, cookie, newsletter, related,
share, social, sponsor, subscribe, popup...

// 四、按 ARIA 无障碍角色删
role="navigation" | "banner" | "search" | "menu"...
```

第三条值得说一下：前端给广告位、赞助位起 class 名是有规律的，
ad、sponsor、related 这些词就是"方言"。规则式清洗单条都不完美，
但组合起来对大部分页面有效，而且确定、可测试、零运行成本。

---

# ③ Readability + 回退：宁要次优，不要空手

```typescript
let contentHtml = '';
try {
  const article = new Readability(doc).parse();
  contentHtml = article?.content ?? '';
} catch { /* Readability 拒绝本文档 */ }

// 质量门：60 字符（和 Go 侧 normalize 的下限对齐）
if (plainText.length < MIN_CONTENT_CHARS) {
  extraction = 'fallback-body';        // 回退用清洗后的整个 <body>
  contentHtml = cleanedHtml;
}
if (仍然不够 60 字符) {
  throw new ReaderError('parse_failed', ...);   // 真没救了
}
```

`extraction` 字段会进 API 响应（`readability` / `fallback-body`），
调用方可以知道结果是怎么来的。

---

# ④ Markdown 渲染

turndown（HTML→MD 规则引擎）+ GFM 插件（表格、任务列表）：

```typescript
const service = new TurndownService({
  headingStyle: 'atx',      // # 风格标题
  codeBlockStyle: 'fenced', // ``` 围栏代码块
  bulletListMarker: '-',
});
service.use(gfm);
```

关键的后处理（移植自 jina-ai/reader 的 `tidyMarkdown`）：

- 相对链接绝对化：`./img/a.png` → `https://site.com/img/a.png`，
  不处理的话下游拿到的全是死链
- 压缩多余空行、规整列表缩进

---

<!-- _class: lead -->

# Part 5

## 双引擎与 auto 升级策略

约 8 分钟。代码：`src/engine.ts` / `src/browser.ts`

---

# 浏览器的代价

Chromium 能渲染一切，但：

1. 慢：秒级（domcontentloaded + 等网络静默）
2. 贵：常驻进程，几百 MB 内存
3. 并发低：进程簇（1 主 + N 子），槽位有限
4. 会崩：Chromium 崩溃是日常
5. 会被识破：不少站点对无头浏览器返回验证页

所以不能全用浏览器，也不能不用。需要调度策略。

---

# auto 模式的决策逻辑（`readPage`）

```
轻量引擎尝试
 ├─ 抛错？
 │   ├─ parse_failed ────────────▶ 可恢复 → 升级浏览器重试
 │   ├─ upstream_failed(非超时) ─▶ 可恢复(bot wall 403等) → 升级
 │   └─ 超时 / unsafe_destination ▶ 不可恢复 → 直接失败
 ├─ 成功但 word_count < 100 ─────▶ 内容太薄(疑似JS壳) → 升级
 └─ 成功且够厚 ─────────────────▶ 直接返回
```

升级之后还要比一下两个结果：

```typescript
const browserOutcome = await runBrowser(url, options);
if (browserOutcome.page.wordCount > light.page.wordCount) {
  return browserOutcome;   // 浏览器结果内容更多 → 用它
}
return light;              // 否则仍用轻量结果
```

浏览器结果不一定更好——有些站对无头浏览器返回的是验证页。

---

# 三个容易忽略的细节

**超时不重试。** 上游都超时了，换个更慢的引擎只会更糟。

**安全判定不重试。** `unsafe_destination` 是 SSRF 拦截，换引擎
结果也一样。安全结论不能靠重试碰运气。

**浏览器槽满了就降级，不报错。**

```typescript
if (!browserGate.tryAcquire()) {
  return light;   // 降级：有轻量结果总比报错好
}
```

auto 模式的原则就是尽量给调用方一个答案。

---

# 浏览器引擎内部（`src/browser.ts`）

- puppeteer-core 驱动系统 Chromium（容器里装的，不是 npm 下载的），
  镜像可控
- 浏览器实例全局共享、懒启动、断线自动重启
- 加载策略，每一步都有原因：

```typescript
await page.goto(url, { waitUntil: 'domcontentloaded' });  // DOM就绪即返回
await page.waitForNetworkIdle({ idleTime: 800 })   // 等XHR静默：前端框架
  .catch(() => {});                                 // hydration需要时间
await sleep(300);                                   // 最后的settle
const html = await page.content();                  // 快照最终DOM
```

拿到的 HTML 复用 Part 4 的同一条解析管线。两个引擎只负责"搞到 HTML"，
洗正文是同一条路，行为一致。

---

<!-- _class: lead -->

# Part 6

## SSRF 防护

约 12 分钟。代码：`src/fetcher.ts` / `src/ip.ts`

---

# 什么是 SSRF

SSRF（Server-Side Request Forgery）：诱导服务端替攻击者发请求。

web-reader 就是"替人发请求"的服务。看个攻击例子：

```bash
# 攻击者调用我们的接口，目标是云厂商的 metadata 端点：
POST /v1/parse
{"url": "http://169.254.169.254/latest/meta-data/iam/security-credentials/"}

# 若无防护，攻击者借我们的手拿到云主机的角色凭证
```

其他玩法：`http://localhost:8080/admin` 探测内网管理面板，
`http://10.0.0.5:3306/` 扫内网端口。

---

# 思考题：哪个方案能防住

以下校验方案，哪些能防住 SSRF，哪些防不住？

**方案 A**：校验 URL 里不能出现 `localhost`、`127.0.0.1` 字符串

**方案 B**：校验域名解析出的 IP 不在私网段

**方案 C**：B 的基础上，跟随重定向时每一跳都重新校验

---

（答案：A 可以被 `0x7f000001`——127.0.0.1 的十六进制写法——或
`localtest.me` 这种解析到 127.0.0.1 的公网域名绕过；B 思路对但
重定向可绕；C 才完整。）

**关键认知：校验"域名"没有意义，必须校验最终连接的 IP。**

---

# 轻量引擎：把校验注入 DNS 解析层

关键设计是接管 Node HTTP 客户端的 DNS lookup 环节：

```typescript
const req = transport.request(url, {
  lookup: (hostname, options, callback) => {
    validatingLookup(hostname, options, config, callback);  // 注入
  },
  ...
});
```

`validatingLookup` 内部：

```
要连 example.com
 → 自己做 dns.lookup，拿到所有 A/AAAA 记录
 → 逐个 IP 对照 blocklist 校验（ip.ts）
 → 全部是公网地址 → 放行，把已验证的 IP 交给 TCP 层拨号
 → 任一是私网/保留地址 → 拒绝
```

这样校验和拨号在同一个回调里完成，连接的就是验证过的那个 IP，
不存在"校验时是公网、连接时被换"的窗口。

---

# Blocklist：什么算"非公网"

`src/ip.ts`，从 jina-ai/reader 移植并加固：

| 类别 | 例子 | 为什么要挡 |
|---|---|---|
| 私网段 | `10/8`, `172.16/12`, `192.168/16` | 内网本身 |
| 环回 | `127/8`, `::1` | 本机服务 |
| 链路本地 | `169.254/16` | 云 metadata 端点在这 |
| CGNAT | `100.64/10` | 运营商级 NAT 段 |
| IPv4-mapped IPv6 | `::ffff:127.0.0.1` | 伪装形态绕过 |
| 保留/文档段 | `240/4`, `2001:db8::/32` | 不该出现在公网 |

移植时发现 jina 原版解析 `::ffff:127.0.0.1` 这类地址有 bug——
填充位数错误导致解析错位。我们修了，并按 IPv4 blocklist 二次校验。
移植开源代码不能盲信，该测的地方要测。

---

# 浏览器引擎的麻烦：DNS 插不进手

轻量引擎能注入 lookup，但 Chromium 自己解析 DNS，插不进去。
解法是两头夹击（`src/browser.ts`）：

```typescript
// 第一道：导航前预检目标域名
await assertPublicHost(target.hostname);

// 第二道：请求拦截器——Chromium 发起的每一个请求
//（主文档、每一跳重定向、每个子资源）都先过审
await page.setRequestInterception(true);
page.on('request', (req) => {
  const allowed = await checkRequestUrl(req.url(), config, verdicts);
  allowed ? req.continue() : req.abort('accessdenied');
});
```

---

# DNS Rebinding：更隐蔽的攻击

攻击时间线：

```
t0  攻击者域名 evil.com TTL=0
t1  第一次解析 → 8.8.8.8（公网）   ← 通过校验
t2  校验结束后的下一毫秒，再次解析 → 127.0.0.1（内网）← 实际连接
```

防御：校验结论的生命周期与页面绑定：

```typescript
// verdicts 缓存在 page 上，页面关闭缓存即死
const verdicts = new Map<string, boolean>();
// 一次 render 内复用结论；下一次 render 重新校验
// 堵死"上一次请求的旧结论"被复用的窗口
```

也要诚实地说：拦截器校验和 Chromium 实际连接之间仍有极小的
TOCTOU 窗口，这是无头浏览器方案的公认极限，生产上由容器出口
网络策略兜底。

整体思路就是纵深防御，每层都假设上一层会被绕过：
URL 校验、DNS 校验、重定向逐跳校验、请求拦截、容器网络策略。

---

<!-- _class: lead -->

# Part 7

## 工程案例：Go 侧接入 + 一个跨层 Bug

约 8 分钟。对应 commit `4af7604` 和 `4d76b61`

---

# 背景：确定性提取的质量门

Go 侧 worker 处理 HTML 的主路径是 `normalize.HTML`（html-primary-v2），
纯确定性提取加一道质量门，以下情况判死：

```go
// internal/normalize/html.go
root := selectHTMLPrimaryV2(document)
if root == nil {
    return Artifact{}, fmt.Errorf("%w: no primary document", ErrHTMLQuality)
}
if textRunes < minHTMLV2Runes {
    return Artifact{}, fmt.Errorf("%w: content below useful bound", ErrHTMLQuality)
}
if loginOrErrorPage(text) { ... }        // 疑似登录页/错误页
if 链接文本占比 >= 80% { ... }            // 正文几乎全是链接
```

问题是：被拒的来源里，很大一块恰恰是 web-reader 擅长救的——
JS 壳、bot wall、服务端 HTML 太薄。但质量门一拒就直接失败，
没有第二次机会。这次增强就是补这条兜底路径。

---

# 兜底逻辑：40 行代码，四个决策

```go
// internal/sourceprocessing/processor.go
case source.FormatHTML:
    artifact, err := normalize.HTML(input)
    if err == nil { return artifact, nil }
    if !errors.Is(err, normalize.ErrHTMLQuality) {
        return normalize.Artifact{}, err        // 决策一
    }
    return e.readViaWebReader(ctx, item, err)
```

**决策一：只重试"可救"的失败。** 质量门拒绝才值得让 web-reader 试；
非 UTF-8、超预算这类结构性错误重试没有意义。做 fallback 必须区分
错误类型。

**决策二：兜底也有下限。** web-reader 返回不足 60 rune 就算救失败。
不能为了成功而成功，把空壳换个姿势塞进知识库。

---

# 兜底逻辑（续）：决策三、四

**决策三：保留第一现场。**

```go
return normalize.Artifact{}, fmt.Errorf("%w; web-reader fallback failed: %v",
    cause, err)   // %w 包装原始 ErrHTMLQuality
// 调用方 errors.Is(err, normalize.ErrHTMLQuality) 依然成立
```

兜底失败时，根因还是原来那个错，不能被兜底的错误覆盖掉。

**决策四：证据和产物分开记。**
兜底成功的产物打上新配置 `html-reader-v1`、格式 markdown；
原始 HTML 字节和它的 SHA256 原样保留。推导内容单独记账（ADR-0021），
将来审计和回滚有据可查。

---

# 一个真实的 bug：Happy Eyeballs

**现象**：上线后部分站点随机报 `Invalid IP address`，但 DNS 一切正常。

排查下来三层原因：

1. Node 20+ 默认开启 autoSelectFamily（Happy Eyeballs，RFC 8305）：
   IPv4/IPv6 并发竞速连接，谁先通用谁
2. Happy Eyeballs 调用 lookup 时传 `{ all: true }`，期望回调收到
   地址数组——这是 `dns.lookup` 的标准契约
3. 我们的旧实现按"单地址"签名回调，net 把地址字符串当成数组逐字符
   迭代：`'2'`, `'.'`, `'0'`, `'.'`, `'2'`, `'1'`... 全不是合法 IP

---

# 修复：尊重调用方的契约

```diff
+ if (options.all) {
+   callback(null, addresses);        // 要列表就给完整列表
+   return;
+ }
  callback(null, first.address, first.family);  // 单地址调用方
```

### 思考题

这个 bug 有什么特征，能让下次快速定位同类问题？

（两点：错误信息驴唇不对马嘴——DNS 正常却报 IP 无效；随机出现——
只有走 Happy Eyeballs 路径的请求才触发。）

教训：给框架写回调，签名要看调用方传了什么 options，不是自己想
怎么写。回调是和框架之间的双向协议。

---

<!-- _class: lead -->

# Part 8

## 总结与展望

约 6 分钟

---

# 回顾

```
为什么 ──▶ LLM 需要干净正文，第三方 API 不可控
   │
怎么做 ──▶ 双引擎(轻量/浏览器) + 单解析管线(jsdom→清洗→Readability→MD)
   │
关键决策 ▶ 进程隔离 · 引擎分级 · 错误码当协议 · SSRF 第一天就做
   │
四个难点 ▶ 正文无标准：规则+算法+兜底
           浏览器又慢又贵：auto 按需升级、处处可降级
           SSRF：校验绑定拨号、拦截器、防 rebinding
           跨层契约：Happy Eyeballs 的教训
   │
工程收尾 ▶ Go 兜底接入(4个决策) + 严格契约解码
```

---

# 测试与部署

测试（node:test，零第三方框架）：

- 纯函数单测：`ip` / `markdown` / `reader`，秒级
- 行为测试：`fetcher` / `server` / `engine`，注入 fake 依赖
- 集成测试：`browser`，本机没浏览器自动 skip，不阻塞 CI
- Go 侧：`processor_webreader_test.go` 5 个场景，把"何时兜底、
  何时不兜底"的每个决策钉死

部署（Docker 多阶段 + compose）：

- 运行时：非 root、只读根文件系统、tmpfs 挂 /tmp、`cap_drop: ALL`
- 资源：`pids_limit: 256`（Chromium 是进程簇）、`mem_limit: 2g`、
  只绑 127.0.0.1
- 版本：`.nvmrc` 锁 Node 22，基础镜像锁 `node:22-bookworm-slim`

---

# 后面可以做的事（一）

**结果缓存。** 相同 URL 短期内重复解析很常见（重试、多消费者）。
要想的几个问题：缓存 key 怎么定（URL？带不带格式参数）、TTL 和
内容新鲜度怎么权衡。有个坑：缓存必须放在 SSRF 校验之后，否则旧
结论复用会打开 DNS rebinding 的时间窗。

**分页合并。** 很多文章有"下一页"（`?page=2`），目前一页一结果。
可以检测分页模式，抓全之后合并成完整正文。

**可观测性。** 现在靠 `engine`/`upgraded`/`extraction` 字段事后
排障，可以加 Prometheus 指标：升级率、各错误码占比、两引擎耗时
分布。升级率异常升高往往说明上游生态变了。

---

# 后面可以做的事（二）

**无头浏览器对抗。** 站点对无头浏览器的检测一直在升级（CDP 指纹、
UA 特征）。可选手段有指纹伪装、住宅代理出口池。原则是对抗成本要
和业务价值匹配，多数场景够用就好。

**LLM 辅助提取。** 对 Readability 也搞不定的怪异页面（表格型、
嵌套 iframe），可以用多模态模型直接从渲染截图提取——jina 商业版
已经在做。定位是最后一级兜底，成本决定了只用于高价值来源。

**协议层演进。** 现在是同步 HTTP，大量并发解析可以演进成异步任务
队列；也可以做 SSE 流式返回，边解析边推送，改善大文档的首字延迟。

这些方向共同的背景：在覆盖率、成本、新鲜度这个三角里，按业务
需要选位置，没有全赢的方案。

---

# 结束语

几句话收尾：

1. 正文提取、安全防护、错误处理，都没有一招制敌的解法，
   都是多层组合，各管一段
2. 每个设计决策都要能回答 why——引擎为什么分两级、SSRF 为什么
   校验 IP、重试为什么有选择
3. 跨语言协作靠契约：错误码、schema、回调签名，漂移要当场暴露

代码看三个入口就够了：

- 解析 → `services/web-reader/src/reader.ts`
- 编排 → `services/web-reader/src/engine.ts`
- Go 接入 → `internal/sourceprocessing/processor.go`（搜 `readViaWebReader`）

有问题可以聊，谢谢。

---

# 附录：术语表

| 术语 | 解释 |
|---|---|
| DOM | 浏览器把 HTML 解析成的对象树，可被代码读写 |
| SPA | 内容由 JS 在浏览器里动态生成的网站形态 |
| jsdom | Node 里纯 JS 的 DOM 实现，不开浏览器 |
| Readability | Firefox 阅读模式的正文提取算法（文本密度打分） |
| turndown | HTML 转 Markdown 的规则引擎 |
| puppeteer | 用代码驱动 Chrome/Chromium 的库（DevTools 协议） |
| sidecar | 与主服务同机部署、本地 HTTP 协作的辅助进程 |
| SSRF | 诱导服务端向内网发起请求的攻击 |
| DNS rebinding | DNS 结果在"校验时"与"连接时"之间被调包 |
| Happy Eyeballs | IPv4/IPv6 并发竞速连接（RFC 8305） |
| TOCTOU | Time-Of-Check 到 Time-Of-Use 之间的窗口期风险 |
| 质量门 | 确定性提取结果的合格性判定（ErrHTMLQuality） |
