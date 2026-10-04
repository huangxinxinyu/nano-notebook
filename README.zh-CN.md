# Nano Notebook

[English](README.md) | [中文](README.zh-CN.md)

面向独立研究者与深度学习者的、以证据为依据的研究工作台。在一个有边界的 Notebook 中收集可信资料,针对你的 Source 提出多步问题,并让每一个重要结论都能对照原始证据加以核实——而不是依赖模型自身的知识。

Nano Notebook 专注于严谨的、以证据为依据的研究,而不是通用聊天。

## 功能特性

- **多格式 Source 摄取** — 支持上传 PDF、DOCX、PPTX、TXT、Markdown、HTML、YouTube 字幕、音频和图片。每个 Source 都会经过一套具备类型化 Evidence Coverage(证据覆盖度)的持久化处理流水线。
- **私有的、以证据为依据的 Chat** — 每一个回答都严格基于你在 Notebook 中选定的 Source。行内 Citation(引用)会将关键论断链接到其支撑证据。
- **Studio 产物生成** — 从你选定的一组 Source 出发,生成一份 Notebook 内共享的产物(Studio Output):Report(报告)、Flashcards(记忆卡片组)、Mind Map(思维导图)或 Data Table(数据表)四种类型之一。这里的"有依据"是结构性强制的,不只是靠 Prompt 约束:产物中的每一个 section、卡片、节点或行都必须引用来自一个不可变、已固定(pinned)的 Evidence Set 中的 Source ID,一旦被引用的 Source 在此之后发生变化或不再属于该集合,生成会直接被拒绝。与 Chat 不同,Studio Output 是对所有 Notebook 成员可见的共享产物,通过与其他功能相同的持久化 Agent/job 流水线生成,并通过 SSE 向客户端实时推送生成进度。
- **可持久化的 Research Agent** — 支持多步、只读的 Agent 运行(Agent Run),即使刷新页面也不会丢失进度;可以随时停止、重试,或离开页面后继续。这种持久性依赖 Checkpoint(检查点)机制来实现:运行时只在一个 Action Proposal(行动提案)、一个已完成的 Action Result(行动结果)或一个 Final Draft(最终草稿)被接受之后才写入 Checkpoint,每个 Checkpoint 由一个稳定的标识 key 和内容的 SHA-256 规范化哈希唯一确定。Checkpoint 的写入是幂等的:即使在"数据库已提交"和"确认已收到"之间发生崩溃,系统也会通过重新比对哈希来判断状态,而不是重复插入;恢复运行时会直接从第一个尚未被接受的步骤继续。
- **运行时研究子 Agent** — 主研究 Agent 通过 `spawn_agent` 自行确定任务。子 Agent 继承主 Agent 固定的模型、工具、Skill 和证据范围，共享预算与截止时间，拥有独立的上下文、TODO 和工作文件。主 Agent 可以继续工作，通过 `list_agents` 或 `wait_agent` 获取终态结果；等待时会释放 worker。结果保存在 PostgreSQL 中，以工具结果返回。子 Agent 不能创建下一层 Agent，也不能互相发消息。
- **Prompt Catalog 与版本化(Prompt Versioning)** — 每一条面向模型的指令都是一个不可变的 `PromptVersion`(由 identity、version、contract、content 组成,并以内容的 SHA-256 做内容寻址)。一旦某个版本被某次 Agent Run 采用,数据库层面就会拒绝对它的任何修改或删除,同时该次运行实际使用的版本会被固定记录下来——这样即便 Prompt 在持续演进,每一次运行的行为依然可复现、可审计。 当前提示词位于 `internal/promptcatalog/prompts`，历史版本和旧资料发现 Agent 的提示词位于 `internal/promptcatalog/archive/prompts`，仍可供绑定旧版本的运行恢复使用。
- **带角色的 Notebook 共享** — 邀请 Viewer(查看者)和 Editor(编辑者),权限模型类似企业级产品。Source 是共享的,但每个成员的 Chat 历史相互独立、保持私密。
- **带段落级 Citation 的有依据回答** — 事实性论断和综合结论都必须附带行内 Citation,点击即可跳转到原始 Source 中对应的段落位置。

## 技术栈

| 层 | 技术 |
| --- | --- |
| 后端 | Go,模块化单体架构,配合 PostgreSQL、MinIO、Qdrant |
| 前端 | React 19、TypeScript、Vite、Tailwind CSS 4、shadcn/ui |
| 模型网关 | Bifrost(OpenAI / Gemini / Qwen) |
| 可观测性 | Durable Agent Trace(自研 Collector)、OpenTelemetry(Trace)、Prometheus + Grafana(Metrics) |

## 可观测性(Observability)

Nano 提供三个可观测性平面,分别回答三个不同的问题:"这一次 Agent Run 里到底发生了什么"(Durable Agent Trace)、"这一次请求在各服务间发生了什么"(OpenTelemetry Trace),以及"整个系统是否健康"(Metrics)。

### Durable Agent Trace(持久化 Agent 追踪)

每一次 Agent Run 都对应恰好一条 Trace 和恰好一个根 Trace Span,并还原出该 Run 的每一次执行尝试(Attempt)——Model 调用、Action(工具调用 / 检索调用)、Job attempt、Agent execution 都作为子 Span 嵌套在其下。与普通链路追踪不同的是,这条 Trace **是强制记录、永不采样、永不过期的**:它是产品自身依赖的持久化记录,而不只是一个调试辅助手段。

- **Exporter(导出器)** — `internal/agentobs/otelbridge` 将 Agent 运行时内部的 span/event 记录桥接为 OpenTelemetry SDK 的 Span,并通过 OTLP 发送给 Jaeger,用于与系统其余部分一致的、可采样的通用请求链路追踪。
- **Collector(采集器)** — 一个独立的、专门构建的 `nano-collector` 服务(`cmd/collector`、`internal/collector`)通过 HTTP 接收同一份运行时记录,写入一个独立的 `nano_observability` Postgres 数据库,与应用的业务数据库完全隔离。应用内的 Trace Dashboard 正是从这里读取数据——也正因为有这一份独立记录,Durable Research Agent"刷新后可续跑"的行为才能够在事后被独立核查。
- **Trace Dashboard(追踪看板)** — 一个应用内、按权限门控(`platform.trace.read`)的视图,包含 Overview(概览)、**Trace Tree(调用树)**(一棵可折叠的嵌套 Span 树——Model call / Action / Job attempt / Agent execution,每个节点带状态和耗时)、Timeline(时间线)、Inspector(检查器)、Attributes(属性)、Events & Links(事件与关联),以及一个用于查看敏感 Span 内容、且带审计的 Replay(回放)视图(`platform.trace.replay`)。

### OpenTelemetry Trace 与 Metrics(Prometheus + Grafana)

- **Trace**(OpenTelemetry,经由上文的 Exporter 输出)— 完整还原一次 Agent Run 内部发生的一切:model 调用、tool 调用、检索步骤和决策过程,可按请求逐条回放。
- **Metrics**(Prometheus + Grafana)— 汇总整个系统的运行健康状况,让性能回归能在看板上直接被发现,而不是等用户反馈才知道。覆盖以下维度:
  - **任务成功率** — 按任务类型区分完成 / 失败 / 取消的比例
  - **错误归因** — 每一次失败都被归入一个具体的类型化原因(模型、工具、检索、存储等),而不只是一个笼统的错误计数
  - **延迟** — 将请求耗时拆分为多个阶段(排队等待、模型调用、工具执行、检索、端到端),便于把一次慢请求定位到具体的责任阶段
  - **内存 / goroutine 泄漏检测** — 对进程的实时堆内存和 goroutine 数量设置运行时指标和告警,让泄漏在告警阶段就被发现,而不是等到 OOM 崩溃

本地和生产两套 Compose 环境都将 Prometheus 和 Grafana 作为标准拓扑的一部分预先配置好,无需额外的安装步骤。本地开发时,`make start` 会随其他服务一起启动它们,看板地址为 `http://localhost:53000`。

## 快速开始

### 前置依赖

- Go 1.25+
- Node.js 22+
- Docker

### 环境搭建

```bash
# 引导依赖服务(PostgreSQL、Kafka、ClickHouse、MinIO、Qdrant、Bifrost 等)
make bootstrap

# 执行数据库迁移并写入种子数据
make migrate
make seed

# 启动 Control Plane、Workers 和 Web 开发服务器
make start
```

其他常用命令:

```bash
make stop       # 停止所有服务
make reset      # 重置到干净状态
make health     # 检查服务健康状况
make test-go    # 运行 Go 测试
make test-web   # 运行前端测试
```

## 项目结构

```
cmd/            应用入口(Control Plane、Workers、Collector 等)
internal/       Go 模块 —— Agent、Chat、Source、Retrieval、Notebook、Studio 等
web/            React + TypeScript 单页应用
docs/           产品调研、技术架构与 sprint runbook
infra/          基础设施定义
evals/          评测用例与评测框架
scripts/        引导、迁移与运维脚本
```

## 文档

- [产品需求](docs/product-discovery/REQUIREMENTS.md)
- [产品调研与决策](docs/product-discovery/DISCOVERY.md)
- [技术架构](docs/technical-architecture/ARCHITECTURE.md)
- [架构术语表](docs/technical-architecture/CONTEXT.md)
- [Sprint runbook](docs/implementation/)
- [第三方声明](docs/third-party-notices.md)

## License

MIT © 2026 Xinyu Huang — 详见 [LICENSE](LICENSE)。
