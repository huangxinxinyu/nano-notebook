#!/usr/bin/env python3
"""Generate the deterministic AtlasDesk Markdown/TXT evaluation corpus."""

from __future__ import annotations

from dataclasses import dataclass
from pathlib import Path


ROOT = Path(__file__).resolve().parent


@dataclass(frozen=True)
class Document:
    filename: str
    title: str
    owner: str
    status: str
    summary: str
    facts: tuple[str, ...]
    procedure: tuple[str, ...]
    caveats: tuple[str, ...]
    related: tuple[str, ...]


DOCUMENTS = (
    Document("01-project-overview.md", "AtlasDesk 项目概览", "产品平台组", "现行", "AtlasDesk 是面向中型企业的多租户协作平台，覆盖工位、访客、会议室与内部公告。", ("生产环境只部署当前受支持版本，实验功能默认关闭。", "每个租户拥有独立的逻辑数据边界，用户必须隶属于至少一个租户。", "浏览器端、管理端和开放 API 共用同一套身份与权限模型。"), ("新成员先创建租户，再邀请管理员，最后由管理员配置身份源。", "上线新模块前必须补充运行手册、告警和回滚说明。"), ("本文是总览，不是具体参数的权威来源。", "时间、容量和保留期应以对应专题文档为准。"), ("02-system-architecture.md", "04-authentication-overview.md", "20-tenant-isolation.md")),
    Document("02-system-architecture.md", "系统架构", "架构组", "现行", "系统采用 API、异步 Worker、PostgreSQL、Redis 和对象存储组成的模块化服务架构。", ("API 节点无状态，可以水平扩容。", "PostgreSQL 保存业务权威数据，Redis 只保存可重建缓存和短期协调状态。", "异步任务通过队列交给 Worker，失败任务按任务类型执行有界重试。"), ("外部请求先经过负载均衡，再进入 API。", "写请求先提交数据库事务，再发布需要异步处理的任务。", "读取缓存未命中时回源 PostgreSQL，并按缓存策略回填。"), ("Redis 不得作为订单、权限或审计记录的唯一存储。", "对象存储故障时不得伪造附件已上传成功。"), ("10-cache-overview.md", "13-database.md", "27-worker-jobs.md")),
    Document("03-production-deployment.md", "生产部署手册", "平台工程组", "现行", "生产发布使用 Docker Compose 运行经过签名的镜像，部署过程包含备份、迁移、健康检查和流量切换。", ("最低要求为 Docker 26、Compose v2.27、4 核 CPU、16 GiB 内存和 100 GiB 可用磁盘。", "发布命令是 docker compose -f deploy/compose.prod.yaml up -d。", "数据库迁移命令是 ./atlasctl migrate --mode=expand。", "健康门禁要求 /health/ready 连续 3 次返回 200。", "默认回滚窗口为新版本切流后的 30 分钟。"), ("确认备份任务最近一次成功且校验通过。", "拉取并校验镜像签名。", "先执行 expand 迁移，再启动新容器。", "等待三个健康采样全部通过后切换流量。", "观察错误率和延迟 30 分钟，再执行 contract 迁移。"), ("不得先执行破坏性 contract 迁移。", "开发环境的 docker compose up 不能作为生产发布命令。", "回滚应用版本时不得回滚已提交的用户数据。"), ("14-database-migrations.md", "18-release-operations.md", "24-disaster-recovery.md")),
    Document("04-authentication-overview.md", "认证方式总览", "身份平台组", "现行", "AtlasDesk 当前支持本地账号密码登录和企业 OIDC 单点登录，两种方式最终都会签发同一种 Access Token 与 Refresh Token。", ("本地登录需要邮箱、密码和租户标识。", "企业单点登录使用 OIDC Authorization Code Flow，并启用 PKCE。", "认证成功后签发 Access Token 和 Refresh Token。", "服务账号使用独立的 Client Credentials 流程，不代表终端用户。"), ("根据租户配置选择本地登录或跳转 OIDC 身份提供方。", "回调完成后校验 state、nonce 和 PKCE verifier。", "建立会话并签发令牌。"), ("旧版 API Key 不能用于用户登录。", "本文不定义令牌有效期，具体数值见令牌专题文档。"), ("05-access-token.md", "06-refresh-token.md", "07-oidc-sso.md", "08-legacy-authentication.md")),
    Document("05-access-token.md", "Access Token", "身份平台组", "现行", "Access Token 是调用 AtlasDesk API 的短期 Bearer 凭证。", ("Access Token 默认有效期为 15 分钟。", "令牌使用 RS256 签名，并包含 tenant_id、subject、roles 和 expires_at。", "API 节点只接受当前或仍在轮换宽限期内的签名密钥。", "权限变更不会延长已经签发令牌的生命周期。"), ("客户端把令牌放在 Authorization: Bearer 请求头。", "收到 401 token_expired 后使用 Refresh Token 换取新令牌。", "服务端校验签名、受众、签发方、过期时间和租户。"), ("2024 年旧版本曾使用 30 分钟有效期，该配置已经废弃。", "不得把 Access Token 写入 URL、日志或前端持久化存储。"), ("06-refresh-token.md", "08-legacy-authentication.md", "17-security-baseline.md")),
    Document("06-refresh-token.md", "Refresh Token", "身份平台组", "现行", "Refresh Token 用于在用户不重新输入凭据的情况下轮换短期 Access Token。", ("Refresh Token 默认有效期为 30 天。", "每次刷新都执行 rotation，旧 Refresh Token 在成功使用后立即失效。", "检测到旧令牌重放时撤销整个 token family。", "退出登录会撤销当前设备对应的 token family。"), ("客户端向 /v1/auth/refresh 提交 Refresh Token。", "服务端锁定 token family，校验哈希和状态。", "事务内撤销旧令牌并签发新的 Access Token 与 Refresh Token。"), ("30 天是绝对有效期，不会因频繁刷新无限延长。", "不要把 30 天与 Access Token 的 15 分钟混淆。"), ("05-access-token.md", "09-session-management.md", "17-security-baseline.md")),
    Document("07-oidc-sso.md", "企业 OIDC 单点登录", "身份平台组", "现行", "企业租户可以配置一个 OIDC 身份提供方，并通过 Authorization Code Flow 登录。", ("必须使用 PKCE S256。", "回调地址必须精确匹配控制台登记值。", "系统校验 issuer、audience、state 和 nonce。", "默认以 email claim 匹配成员，也可以配置不可变 subject 映射。"), ("管理员录入 issuer、client_id 和加密后的 client_secret。", "系统读取 discovery document 并验证 JWKS。", "测试登录成功后才能启用该身份源。"), ("不接受隐式流程返回的 access_token。", "身份提供方的组信息只有经过显式映射才会转成 AtlasDesk 角色。"), ("04-authentication-overview.md", "17-security-baseline.md", "20-tenant-isolation.md")),
    Document("08-legacy-authentication.md", "历史认证方案说明", "身份平台组", "已废弃", "本文保留 2024 年认证方案，供排查旧客户端，不代表当前配置。", ("旧版 Access Token 有效期为 30 分钟。", "旧版 Refresh Token 有效期为 14 天。", "旧版内部工具曾接受静态 API Key。", "这些参数在 2025.3 版本后全部停止签发。"), ("发现旧客户端时记录版本和租户。", "要求客户端升级并重新登录。", "确认新会话使用 15 分钟 Access Token 和 30 天 Refresh Token。"), ("禁止复制本文参数到新环境。", "题目询问当前行为时应以现行文档为准。"), ("05-access-token.md", "06-refresh-token.md", "31-release-notes-2025.md")),
    Document("09-session-management.md", "会话管理", "身份平台组", "现行", "会话记录设备、最近活动、Refresh Token family 和撤销状态。", ("单个用户每个租户最多保留 10 个活动设备会话。", "连续 90 天无活动的设备会话会被清理。", "管理员强制下线会撤销目标用户在该租户下的所有 token family。"), ("用户可以在安全中心查看设备列表。", "撤销操作先写数据库，再异步删除相关缓存。", "缓存删除失败不会恢复已经撤销的数据库状态。"), ("设备会话的 90 天清理周期不是 Refresh Token 的有效期。", "浏览器标签页关闭不会自动撤销会话。"), ("06-refresh-token.md", "10-cache-overview.md", "17-security-baseline.md")),
    Document("10-cache-overview.md", "缓存策略总览", "平台工程组", "现行", "AtlasDesk 使用多个用途不同的缓存层，任何 TTL 都必须连同缓存对象名称一起说明。", ("用户资料缓存、目录查询缓存、负缓存和边缘静态资源缓存使用不同 TTL。", "所有缓存均可从权威数据重新构建。", "写操作提交后通过版本号或事件使相关缓存失效。"), ("先识别缓存对象和调用链。", "确认权威数据来源。", "选择主动失效或 TTL 兜底。", "监控命中率、陈旧读取和回源压力。"), ("不得只回答“缓存是 20 分钟”而不说明对象。", "具体 TTL 以 11、12 和 29 号文档为准。"), ("11-redis-cache.md", "12-edge-cache.md", "29-directory-search.md")),
    Document("11-redis-cache.md", "Redis 应用缓存", "平台工程组", "现行", "Redis 保存用户资料、权限摘要和目录查询结果等可重建数据。", ("用户资料缓存默认 TTL 为 20 分钟。", "目录查询结果缓存默认 TTL 为 5 分钟，即 300 秒。", "查无结果的负缓存 TTL 为 30 秒。", "权限摘要最大缓存 2 分钟，并在角色修改后主动失效。"), ("缓存键必须包含 tenant_id 和数据版本。", "读取未命中时从 PostgreSQL 加载。", "回填时加入最多 10% 的随机抖动，避免同时过期。"), ("禁止使用通配符批量删除生产缓存。", "20 分钟不能用于回答目录查询缓存的 TTL。", "Redis 故障时允许回源，但必须受并发保护。"), ("10-cache-overview.md", "13-database.md", "29-directory-search.md")),
    Document("12-edge-cache.md", "边缘静态资源缓存", "Web 平台组", "现行", "CDN 缓存带内容哈希的 JavaScript、CSS、字体和公开图片。", ("带内容哈希的静态资源 Cache-Control max-age 为 86400 秒。", "HTML 入口页 max-age 为 60 秒。", "认证后的 API 响应不得进入共享 CDN 缓存。"), ("构建阶段生成内容哈希文件名。", "发布新版本后先上传静态资源，再切换 HTML。", "紧急回滚时恢复上一版 HTML 引用。"), ("86400 秒只适用于不可变静态资源。", "该 TTL 与 Redis 用户资料缓存和目录查询缓存无关。"), ("10-cache-overview.md", "18-release-operations.md", "28-web-frontend.md")),
    Document("13-database.md", "数据库运行参数", "数据平台组", "现行", "PostgreSQL 是 AtlasDesk 业务数据、权限和审计记录的权威存储。", ("API 进程默认数据库连接池上限为 40。", "Worker 进程默认连接池上限为 20。", "单条交互式查询超时为 3 秒。", "事务空闲超时为 10 秒。"), ("启动时校验数据库版本和必需扩展。", "写事务保持短小，并按稳定顺序锁定资源。", "慢查询超过 500 毫秒会进入采样日志。"), ("连接池上限按单个进程计算，不是整个集群总数。", "Redis 中的数据不能替代 PostgreSQL 约束。"), ("02-system-architecture.md", "14-database-migrations.md", "15-backup-retention.md")),
    Document("14-database-migrations.md", "数据库迁移", "数据平台组", "现行", "数据库变更采用 expand、migrate、contract 三阶段，确保滚动发布期间新旧应用兼容。", ("expand 阶段只增加向后兼容结构。", "数据回填由可恢复 Worker 分批执行。", "contract 阶段至少等待一个完整回滚窗口后才能执行。", "迁移记录保存在 schema_migrations 表。"), ("先在快照副本验证迁移耗时。", "发布前执行 expand。", "观察新版本稳定后执行回填。", "确认旧版本不再运行后执行 contract。"), ("不得在应用启动请求中执行长时间回填。", "contract 迁移完成后通常不能仅靠回滚镜像恢复。"), ("03-production-deployment.md", "15-backup-retention.md", "18-release-operations.md")),
    Document("15-backup-retention.md", "备份与保留", "数据平台组", "现行", "生产 PostgreSQL 使用每日全量备份、连续 WAL 归档和定期恢复演练。", ("每日全量备份保留 35 天。", "WAL 归档支持最近 7 天内的时间点恢复。", "对象存储版本保留 30 天。", "每季度至少完成一次隔离环境恢复演练。"), ("备份完成后校验清单和对象哈希。", "恢复演练使用独立网络和临时凭据。", "记录恢复点、耗时、缺口和清理结果。"), ("备份任务显示成功不等于可恢复，必须验证恢复。", "35 天只适用于数据库全量备份。"), ("13-database.md", "24-disaster-recovery.md", "25-file-storage.md")),
    Document("16-api-conventions.md", "开放 API 约定", "API 平台组", "现行", "开放 API 使用 JSON、版本化路径和幂等写入约定。", ("当前稳定前缀为 /v1。", "创建类请求使用 Idempotency-Key，请求键在同一租户内保留 24 小时。", "列表接口使用不透明 cursor，默认 page_size 为 50，最大为 200。", "错误响应包含 code、message 和 request_id。"), ("客户端生成随机幂等键。", "重试时保持请求体和幂等键不变。", "遇到 429 时按 Retry-After 退避。"), ("幂等键不是认证凭证。", "不要根据 cursor 内容推断数据库主键。"), ("04-authentication-overview.md", "19-rate-limits.md", "30-webhooks.md")),
    Document("17-security-baseline.md", "安全基线", "安全工程组", "现行", "安全基线覆盖传输加密、凭据、审计、依赖和最小权限。", ("公网流量只允许 TLS 1.2 或 TLS 1.3。", "服务间凭据通过 Secret Store 注入，不写入镜像。", "高风险管理操作必须记录操作者、租户、对象和 request_id。", "严重依赖漏洞必须在 72 小时内完成评估。"), ("每季度审查生产权限。", "轮换密钥时保留有界验签宽限期。", "发现泄露后先吊销，再调查影响范围。"), ("文档示例不得包含真实密钥。", "日志脱敏不能依赖开发人员手工处理。"), ("05-access-token.md", "21-secret-management.md", "22-audit-logging.md")),
    Document("18-release-operations.md", "发布与回滚操作", "平台工程组", "现行", "正式版本先进入预发布环境，通过冒烟测试后再进入生产灰度。", ("生产先向 10% 实例发布。", "灰度观察至少 15 分钟。", "全量切流后保留 30 分钟应用回滚窗口。", "版本号遵循 YYYY.MINOR.PATCH。"), ("冻结镜像摘要和配置摘要。", "运行迁移前检查兼容性。", "灰度期间比较错误率、p95 延迟和队列积压。", "满足门禁后扩大流量。"), ("回滚镜像不自动回滚数据库 contract 迁移。", "不得使用 latest 标签部署生产。"), ("03-production-deployment.md", "14-database-migrations.md", "23-monitoring.md")),
    Document("19-rate-limits.md", "限流策略", "API 平台组", "现行", "限流以租户和调用方身份为基本维度，并为登录等敏感端点设置更严格策略。", ("普通开放 API 默认每租户每分钟 600 次。", "登录失败尝试按账号和来源地址共同限制。", "导出任务每租户最多同时运行 3 个。", "超过限制返回 429 和 Retry-After。"), ("网关执行快速计数。", "应用层对高成本任务再做并发配额。", "告警观察持续限流而不是单个 429。"), ("限流额度不是性能承诺。", "不同端点可能有更低的独立额度。"), ("16-api-conventions.md", "23-monitoring.md", "27-worker-jobs.md")),
    Document("20-tenant-isolation.md", "租户隔离", "安全工程组", "现行", "所有业务访问都必须显式携带服务端解析的 tenant_id，并在数据库查询中执行租户过滤。", ("tenant_id 来自验证后的会话或服务账号，不信任请求体覆盖。", "缓存键必须包含 tenant_id。", "对象存储路径以租户不可猜测标识分区。", "后台任务持久化其授权租户，执行时重新验证。"), ("入口层解析身份。", "领域层传递租户上下文。", "存储层执行租户条件并记录审计。"), ("管理员角色也不能跨越未授权租户。", "只依赖前端隐藏不是隔离措施。"), ("04-authentication-overview.md", "11-redis-cache.md", "25-file-storage.md")),
    Document("21-secret-management.md", "密钥管理", "安全工程组", "现行", "数据库密码、OIDC client secret 和第三方令牌存放在 Secret Store。", ("应用通过短期工作负载身份读取密钥。", "生产密钥每 90 天轮换一次，泄露事件立即轮换。", "配置文件只保存 secret reference。", "本地开发使用单独的占位凭据。"), ("创建新 secret version。", "部署读取新版本的实例。", "确认健康后撤销旧版本。", "检查审计记录和异常访问。"), ("禁止把真实密钥写入 README、日志、Trace 或压缩包。", "Base64 编码不等于加密。"), ("07-oidc-sso.md", "17-security-baseline.md", "32-configuration-reference.md")),
    Document("22-audit-logging.md", "审计日志", "安全工程组", "现行", "审计日志记录会改变权限、身份、配置或数据可见性的高价值操作。", ("审计日志在线保留 180 天。", "记录 actor、tenant、action、target、result、request_id 和时间。", "审计写入失败会阻止高风险管理操作成功。", "普通读取不全部进入审计日志。"), ("操作开始时生成 request_id。", "事务内写入业务状态和审计事件。", "导出审计记录时再次校验管理员权限。"), ("应用调试日志不能替代审计日志。", "不得记录密码、完整令牌和 Secret 值。"), ("17-security-baseline.md", "26-logging.md", "33-admin-guide.md")),
    Document("23-monitoring.md", "监控指标与服务目标", "可靠性工程组", "现行", "监控覆盖请求、数据库、缓存、队列和业务流程，并使用统一 request_id 与 trace_id 关联。", ("API 可用性月度目标为 99.9%。", "API p95 延迟目标低于 400 毫秒。", "5xx 比例连续 5 分钟超过 2% 触发严重告警。", "Worker 最老任务等待时间超过 120 秒触发告警。"), ("指标先进入 Prometheus。", "告警规则在满足持续窗口后发送到 Alertmanager。", "Alertmanager 路由到值班系统并创建事件。", "值班人员按运行手册确认影响范围。"), ("单个瞬时尖峰不会立即呼叫值班人员。", "SLO 是服务目标，不是每个请求的硬保证。"), ("24-alerting.md", "26-logging.md", "27-worker-jobs.md")),
    Document("24-alerting.md", "告警路由", "可靠性工程组", "现行", "告警根据严重级别、持续时间和服务归属发送到不同处理渠道。", ("P1 告警同时发送 PagerDuty 和 #incident-critical。", "P2 告警发送 #service-alerts，并在工作时间由服务负责人处理。", "同一告警按 fingerprint 去重 30 分钟。", "事件结束后两个工作日内完成复盘初稿。"), ("Alertmanager 附加服务、环境、runbook 和 dashboard 标签。", "PagerDuty 呼叫主值班，5 分钟未确认则升级到备值班。", "事件指挥官建立事件频道并维护时间线。"), ("只有达到规则持续窗口的告警才进入路由。", "聊天频道消息本身不是最终事件记录。"), ("23-monitoring.md", "26-logging.md", "34-incident-response.md")),
    Document("25-file-storage.md", "文件与对象存储", "存储平台组", "现行", "用户附件通过短期上传凭证直接写入对象存储，应用只在验证完成后发布文件记录。", ("单文件上限为 100 MiB。", "上传凭证有效期为 10 分钟。", "对象存储版本保留 30 天。", "下载链接有效期为 5 分钟。"), ("客户端申请上传意图。", "对象上传完成后调用 finalize。", "服务端校验大小、哈希和媒体类型。", "验证通过后文件才对成员可见。"), ("上传成功但未 finalize 的对象不会成为业务文件。", "下载链接有效期不能当作对象保留期。"), ("15-backup-retention.md", "20-tenant-isolation.md", "33-admin-guide.md")),
    Document("26-logging.md", "应用日志", "可靠性工程组", "现行", "应用输出结构化 JSON 日志，用于排障而不是保存业务权威事实。", ("默认生产日志保留 14 天。", "每条请求日志包含 request_id、service、route、status 和 duration_ms。", "错误日志可以包含稳定错误码，但不能包含完整凭据。", "Trace 采样不影响错误日志写入。"), ("入口生成或接受合法 request_id。", "跨服务调用传递 request_id 和 trace context。", "排障完成后把稳定结论写入运行手册。"), ("不要依赖自由文本解析权限变化。", "日志保留期与审计日志 180 天不同。"), ("22-audit-logging.md", "23-monitoring.md", "34-incident-response.md")),
    Document("27-worker-jobs.md", "异步任务与重试", "平台工程组", "现行", "邮件、导出、索引和清理任务由 Worker 从持久化队列领取。", ("普通任务最多执行 5 次。", "退避从 2 秒开始并加入随机抖动，最大 60 秒。", "每个任务拥有稳定幂等键。", "超过次数的任务进入 dead-letter 状态。"), ("事务内创建业务记录和待处理任务。", "Worker 领取任务并设置租约。", "成功时原子记录结果。", "失败时按分类决定重试或终止。"), ("重试保证的是至少一次处理，不代表外部副作用天然只发生一次。", "无法确认结果的写操作必须依靠下游幂等键。"), ("02-system-architecture.md", "19-rate-limits.md", "23-monitoring.md")),
    Document("28-web-frontend.md", "Web 前端运行约定", "Web 平台组", "现行", "Web 客户端使用 React 和 TypeScript，通过 HTTPS 调用 /v1 API。", ("浏览器不持久化保存 Access Token。", "CSRF 防护使用 SameSite Cookie 与请求令牌组合。", "前端资源使用内容哈希文件名。", "用户可见错误展示稳定错误码和 request_id。"), ("页面启动时加载当前会话。", "接口失败按错误类型决定重试或提示。", "收到未授权响应时只执行一次刷新协商。"), ("前端状态不是权限权威。", "静态资源 TTL 见边缘缓存文档。"), ("05-access-token.md", "12-edge-cache.md", "16-api-conventions.md")),
    Document("29-directory-search.md", "成员目录搜索", "搜索平台组", "现行", "成员目录搜索从 PostgreSQL 权威数据构建轻量索引，并对常见查询使用 Redis 缓存。", ("目录查询结果缓存 5 分钟，即 300 秒。", "空结果负缓存 30 秒。", "搜索最多返回 100 条成员记录。", "成员权限变化后发布失效事件。"), ("规范化查询中的空格和大小写。", "按 tenant_id 限定候选成员。", "缓存未命中时查询索引并回填。"), ("用户资料详情缓存 20 分钟，不是目录查询结果 TTL。", "空结果的 30 秒也不是查询结果的正常 TTL。"), ("10-cache-overview.md", "11-redis-cache.md", "20-tenant-isolation.md")),
    Document("30-webhooks.md", "Webhook", "API 平台组", "现行", "Webhook 用于把成员、会议和访客事件推送到租户登记的 HTTPS 端点。", ("请求使用 HMAC-SHA256 签名。", "接收方应在 5 分钟时间窗内校验时间戳。", "非 2xx 响应最多重试 8 次。", "事件 ID 可作为消费端幂等键。"), ("管理员登记 HTTPS URL 和 secret。", "系统发送签名头、时间戳和事件 ID。", "接收方先验签再处理。"), ("Webhook 重试可能造成重复投递。", "签名 secret 不会再次以明文展示。"), ("16-api-conventions.md", "17-security-baseline.md", "27-worker-jobs.md")),
    Document("31-release-notes-2025.md", "2025 年发布说明", "发布管理组", "历史", "2025 年版本完成认证令牌缩短、Refresh rotation 和目录缓存优化。", ("2025.3 将 Access Token 从 30 分钟缩短到 15 分钟。", "2025.3 将 Refresh Token 从 14 天调整为 30 天并启用 rotation。", "2025.6 将目录查询缓存统一为 5 分钟。", "2025.8 增加 OIDC PKCE 强制校验。"), ("升级前通知仍在使用旧 SDK 的租户。", "升级后要求用户重新登录以获得新 token family。", "观察认证失败率和刷新重放告警。"), ("发布说明记录变化，不替代现行专题文档。", "旧客户端显示的过期时间可能来自本地缓存。"), ("05-access-token.md", "06-refresh-token.md", "08-legacy-authentication.md")),
    Document("32-configuration-reference.md", "配置参考", "平台工程组", "现行", "配置分为非敏感环境变量、Secret reference 和数据库动态配置。", ("ATLAS_HTTP_PORT 默认 8080。", "ATLAS_DB_POOL_MAX 默认 40，仅用于 API 进程。", "ATLAS_REDIS_PROFILE_TTL 默认 20m。", "ATLAS_DIRECTORY_CACHE_TTL 默认 5m。"), ("复制 config/example.env。", "填写非敏感地址和 Secret reference。", "运行 atlasctl config validate。", "启动时记录配置摘要而不是 Secret 值。"), ("环境变量名称相似时必须核对具体缓存对象。", "生产不得使用 example.env 中的占位凭据。"), ("11-redis-cache.md", "13-database.md", "21-secret-management.md")),
    Document("33-admin-guide.md", "管理员指南", "客户成功组", "现行", "租户管理员负责成员邀请、角色、身份源和安全策略。", ("成员邀请链接有效期为 24 小时。", "管理员可以撤销尚未使用的邀请。", "角色修改立即写入数据库，并异步清除权限缓存。", "删除成员会撤销其当前租户的会话。"), ("先确认操作者具备 tenant_admin。", "输入成员邮箱并选择最小角色。", "发送邀请并通过审计日志确认结果。"), ("邀请链接 24 小时与 Access Token 15 分钟、Refresh Token 30 天无关。", "管理员不能查看其他租户成员。"), ("04-authentication-overview.md", "09-session-management.md", "22-audit-logging.md")),
    Document("34-incident-response.md", "故障响应", "可靠性工程组", "现行", "故障响应以降低用户影响、保留时间线和恢复可验证服务为优先。", ("P1 事件要求 5 分钟内确认。", "事件指挥官负责分工和状态更新。", "恢复后先验证用户路径，再关闭告警。", "两个工作日内提交复盘初稿。"), ("值班人员确认告警并创建事件频道。", "指定事件指挥官、沟通负责人和操作负责人。", "记录假设、操作和结果。", "恢复后创建长期修复项。"), ("不要在没有验证的情况下宣布恢复。", "事件频道不是审计日志或最终复盘。"), ("23-monitoring.md", "24-alerting.md", "26-logging.md")),
    Document("35-troubleshooting.md", "常见故障排查", "支持工程组", "现行", "排障从用户影响、request_id 和最近变更开始，逐层检查入口、API、数据库、缓存和 Worker。", ("登录循环首先检查 OIDC 回调地址、state 和 Cookie 域。", "目录结果陈旧时检查失效事件和 5 分钟缓存。", "导出长时间等待时检查最老任务等待时间和 Worker 租约。", "数据库连接耗尽时按进程核对 40/20 的池上限。"), ("收集 request_id、租户、时间窗和复现步骤。", "查看对应 dashboard 和结构化日志。", "验证最小影响修复，再形成运行手册更新。"), ("不要通过清空全部 Redis 作为第一步。", "历史文档参数不能覆盖现行配置。"), ("11-redis-cache.md", "13-database.md", "23-monitoring.md", "27-worker-jobs.md")),
)


MAINTENANCE = """
## 维护与验证约定

本文中的数值、状态和步骤必须和主题名称一起引用，不能脱离对象只保留一个数字。变更负责人需要在合并前检查同主题的历史文档、配置参考和运行手册，确认现行与已废弃内容有明确标识。生产变更必须留下 request_id 或变更单号，并在完成后验证用户可见路径，而不能只依据命令退出码判断成功。

检索或问答系统使用本文时，应优先返回能够直接支持问题的段落；如果问题要求的信息没有出现在任何文档中，应明确说明知识库没有提供答案。相似术语、旧版本参数以及其他子系统的同名设置只能作为区分依据，不能拼接成未经文档支持的新结论。跨文档回答必须分别说明每份文档贡献的事实。

本知识库是用于检索评测的虚构材料，不包含真实用户、生产地址、账号或密钥。文档之间刻意保留少量重复背景，以模拟长期维护中的信息重叠；专题文档的现行明确陈述优先于总览和历史发布说明。若文档状态为“历史”或“已废弃”，只能用于解释变化，不能作为当前操作依据。
""".strip()


def render(doc: Document) -> str:
    lines = [
        f"# {doc.title}",
        "",
        f"- 文档状态：{doc.status}",
        f"- 维护团队：{doc.owner}",
        "- 项目：AtlasDesk",
        "",
        "## 适用范围",
        "",
        doc.summary,
        "",
        "## 关键事实",
        "",
    ]
    lines.extend(f"- {item}" for item in doc.facts)
    lines.extend(["", "## 操作流程", ""])
    lines.extend(f"{index}. {item}" for index, item in enumerate(doc.procedure, start=1))
    lines.extend(["", "## 易混淆点与限制", ""])
    lines.extend(f"- {item}" for item in doc.caveats)
    lines.extend(["", "## 相关文档", ""])
    lines.extend(f"- `{item}`" for item in doc.related)
    lines.extend(["", MAINTENANCE, ""])
    return "\n".join(lines)


def main() -> None:
    expected = {doc.filename for doc in DOCUMENTS}
    for old in ROOT.glob("*.md"):
        if old.name != "GENERATION_PROMPT.md" and old.name not in expected:
            old.unlink()
    for doc in DOCUMENTS:
        (ROOT / doc.filename).write_text(render(doc), encoding="utf-8")
    total_chars = sum(len((ROOT / doc.filename).read_text(encoding="utf-8")) for doc in DOCUMENTS)
    print(f"generated {len(DOCUMENTS)} documents, {total_chars} characters")


if __name__ == "__main__":
    main()
