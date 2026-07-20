import { useQuery } from "@tanstack/react-query";
import { useState, type FormEvent } from "react";
import { MaterialSymbol } from "../icons/material-symbol";
import { Alert, AlertDescription } from "../ui/alert";
import { Button } from "../ui/button";
import { tracePollingInterval } from "./polling";
import type { Attribute, ReplayReference, Span, TraceDetail, TraceListItem, TraceSummary } from "./types";

type Locale = "en" | "zh";

const copy = {
  en: {
    explorer: "Trace Explorer", restricted: "Trace access restricted", restrictedBody: "A platform.trace.read grant is required. Notebook roles do not grant observability access.",
    library: "Back to Library", identity: "Trace, Run, or Chat prefix", agent: "Agent", model: "Model", status: "Status", state: "State", timeRange: "Time range", all: "All", lastHour: "Last hour", last24Hours: "Last 24 hours", last7Days: "Last 7 days", active: "Active", terminal: "Terminal", apply: "Apply filters", clear: "Clear", retry: "Retry",
    loading: "Loading Traces…", empty: "No Traces match these filters.", unavailable: "Trace data is temporarily unavailable.",
    started: "Started", ended: "Ended", lastObserved: "Last observed", attempts: "Attempts", summary: "Trace summary", runChat: "Workload", agentModel: "Component / Model", workload: "Workload", agentRun: "Agent run", sourceProcessing: "Source processing", latency: "Latency", tokens: "Tokens", cost: "Cost", open: "Open Trace", lagged: "Projection lagged", incomplete: "Incomplete", next: "Next page", previous: "Previous page",
    tree: "Trace Tree", timeline: "Trace Timeline", inspector: "Inspector", overview: "Overview", replay: "Replay", attributes: "Attributes", eventsLinks: "Events & Links", analysis: "Trace analysis", kind: "Kind", errorKind: "Error kind", modelCall: "Model call", actionCall: "Action", jobAttempt: "Job attempt", agentExecution: "Agent execution", spanKind: "Span",
    unfinished: "Unfinished", selectTimeline: "Select", openLink: "Open", expand: "Expand", collapse: "Collapse", zoomIn: "Zoom in", zoomOut: "Zoom out", resetZoom: "Reset zoom",
    loadReplay: "Load sensitive Replay", sensitive: "This action accesses sensitive user content and will be audited.", replayForbidden: "Replay capability is not granted.", replayLoading: "Loading Replay…", replayExpired: "Replay has expired.", replayCorrupt: "Replay failed its integrity check.", replayUnavailable: "Replay is unavailable.",
    unknown: "Unknown", unknownCost: "Unknown cost", total: "Total", modelCalls: "Model calls", actions: "Actions", other: "Other", duration: "Duration", input: "Input", output: "Output", cached: "Cached", reasoning: "Reasoning", provider: "Provider", noSelection: "Select a Span to inspect it.", noReplay: "This Span has no Replay payload.", noEvents: "No Events or Links for this Span.",
    ragExecution: "RAG execution", searchPurpose: "Search purpose", candidates: "Dense / BM25 candidates", rankFlow: "RRF order", reranked: "Rerank selection", degradation: "Degradation", claimSupport: "Claim support", publicationOutcome: "Publication outcome", stageLatency: "Stage latency", supported: "supported", unsupported: "unsupported", healthy: "None"
  },
  zh: {
    explorer: "Trace 调试台", restricted: "Trace 访问受限", restrictedBody: "需要 platform.trace.read 授权；Notebook 角色不会授予可观测性访问。",
    library: "返回笔记库", identity: "Trace、Run 或 Chat 前缀", agent: "Agent", model: "模型", status: "状态", state: "生命周期", timeRange: "时间范围", all: "全部", lastHour: "最近 1 小时", last24Hours: "最近 24 小时", last7Days: "最近 7 天", active: "进行中", terminal: "已终止", apply: "应用筛选", clear: "清除", retry: "重试",
    loading: "正在加载 Trace…", empty: "没有符合筛选条件的 Trace。", unavailable: "Trace 数据暂时不可用。",
    started: "开始时间", ended: "结束时间", lastObserved: "最近观测", attempts: "尝试次数", summary: "Trace 摘要", runChat: "工作负载", agentModel: "组件 / 模型", workload: "工作负载", agentRun: "Agent 运行", sourceProcessing: "Source 处理", latency: "耗时", tokens: "Token", cost: "成本", open: "打开 Trace", lagged: "投影滞后", incomplete: "未完成", next: "下一页", previous: "上一页",
    tree: "Trace 树", timeline: "Trace 时间线", inspector: "检查器", overview: "概览", replay: "Replay", attributes: "属性", eventsLinks: "事件与链接", analysis: "Trace 分析", kind: "类型", errorKind: "错误类型", modelCall: "模型调用", actionCall: "Action", jobAttempt: "任务尝试", agentExecution: "Agent 执行", spanKind: "Span",
    unfinished: "未闭合", selectTimeline: "在时间线选择", openLink: "打开", expand: "展开", collapse: "收起", zoomIn: "放大", zoomOut: "缩小", resetZoom: "重置缩放",
    loadReplay: "加载敏感 Replay", sensitive: "此操作会访问敏感用户内容并被审计。", replayForbidden: "未授予 Replay 权限。", replayLoading: "正在加载 Replay…", replayExpired: "Replay 已过期。", replayCorrupt: "Replay 完整性校验失败。", replayUnavailable: "Replay 不可用。",
    unknown: "未知", unknownCost: "成本未知", total: "总计", modelCalls: "模型调用", actions: "Action", other: "其他", duration: "耗时", input: "输入", output: "输出", cached: "缓存", reasoning: "推理", provider: "Provider", noSelection: "请选择一个 Span。", noReplay: "这个 Span 没有 Replay 数据。", noEvents: "这个 Span 没有事件或链接。",
    ragExecution: "RAG 执行", searchPurpose: "检索目的", candidates: "Dense / BM25 候选", rankFlow: "RRF 排序", reranked: "重排入选", degradation: "降级", claimSupport: "主张支持", publicationOutcome: "发布结果", stageLatency: "阶段耗时", supported: "支持", unsupported: "不支持", healthy: "无"
  }
} satisfies Record<Locale, Record<string, string>>;

type TraceCopy = { [Key in keyof typeof copy.en]: string };

type TraceDashboardProps = {
  locale: Locale;
  routePath: string;
  canRead: boolean;
  canReplay: boolean;
  onNavigate: (path: string) => void;
  onLibrary: () => void;
};

export function TraceDashboard(props: TraceDashboardProps) {
  const t = copy[props.locale];
  if (!props.canRead) {
    return <main className="trace-shell trace-restricted"><Button variant="ghost" onClick={props.onLibrary}><MaterialSymbol name="arrow_back" size={18} />{t.library}</Button><section><h1>{t.restricted}</h1><p>{t.restrictedBody}</p></section></main>;
  }
  const detailID = props.routePath.startsWith("/admin/traces/") ? decodeURIComponent(props.routePath.slice("/admin/traces/".length)) : "";
  return detailID ? <TraceDetailView key={detailID} {...props} traceID={detailID} /> : <TraceExplorer {...props} />;
}

type Filters = { identity: string; agent: string; model: string; status: string; active: string; timeRange: string };
const emptyFilters: Filters = { identity: "", agent: "", model: "", status: "", active: "", timeRange: "" };

function TraceExplorer(props: TraceDashboardProps) {
  const t = copy[props.locale];
  const [draft, setDraft] = useState<Filters>(emptyFilters);
  const [filters, setFilters] = useState<Filters>(emptyFilters);
  const [cursorStack, setCursorStack] = useState<string[]>([""]);
  const cursor = cursorStack[cursorStack.length - 1] ?? "";
  const traces = useQuery({
    queryKey: ["admin-traces", filters, cursor],
    queryFn: async () => {
      const parameters = new URLSearchParams({ page_size: "50" });
      if (filters.identity) parameters.set("identity_prefix", filters.identity);
      if (filters.agent) parameters.set("agent", filters.agent);
      if (filters.model) parameters.set("model", filters.model);
      if (filters.status) parameters.set("status", filters.status);
      if (filters.active) parameters.set("active", filters.active);
      const startedAfter = timeRangeStart(filters.timeRange);
      if (startedAfter) parameters.set("started_after", startedAfter);
      if (cursor) parameters.set("cursor", cursor);
      return adminJSON<{ items: TraceListItem[]; next_cursor?: string }>(`/api/admin/traces?${parameters}`);
    },
    retry: false
  });
  const forbidden = adminErrorCode(traces.error) === "trace_forbidden";

  function apply(event: FormEvent) {
    event.preventDefault();
    setFilters(draft);
    setCursorStack([""]);
  }

  return (
    <main className="trace-shell">
      <TraceTopbar title={t.explorer} onBack={props.onLibrary} backLabel={t.library} />
      <form className="trace-filters" onSubmit={apply} aria-label="Trace filters">
        <label><span>{t.identity}</span><input value={draft.identity} onChange={(event) => setDraft({ ...draft, identity: event.target.value })} /></label>
        <label><span>{t.agent}</span><input value={draft.agent} onChange={(event) => setDraft({ ...draft, agent: event.target.value })} /></label>
        <label><span>{t.model}</span><input value={draft.model} onChange={(event) => setDraft({ ...draft, model: event.target.value })} /></label>
        <label><span>{t.status}</span><select value={draft.status} onChange={(event) => setDraft({ ...draft, status: event.target.value })}><option value="">{t.all}</option><option value="ok">OK</option><option value="error">Error</option><option value="cancelled">Cancelled</option></select></label>
        <label><span>{t.state}</span><select value={draft.active} onChange={(event) => setDraft({ ...draft, active: event.target.value })}><option value="">{t.all}</option><option value="true">{t.active}</option><option value="false">{t.terminal}</option></select></label>
        <label><span>{t.timeRange}</span><select value={draft.timeRange} onChange={(event) => setDraft({ ...draft, timeRange: event.target.value })}><option value="">{t.all}</option><option value="1h">{t.lastHour}</option><option value="24h">{t.last24Hours}</option><option value="168h">{t.last7Days}</option></select></label>
        <div className="trace-filter-actions"><Button>{t.apply}</Button><Button type="button" variant="secondary" onClick={() => { setDraft(emptyFilters); setFilters(emptyFilters); setCursorStack([""]); }}>{t.clear}</Button></div>
      </form>
      {traces.isLoading ? <div className="trace-state" role="status">{t.loading}</div> : null}
      {traces.isError ? <Alert variant="destructive" className="trace-state"><AlertDescription>{forbidden ? t.restricted : t.unavailable}</AlertDescription>{forbidden ? null : <Button variant="secondary" onClick={() => void traces.refetch()}>{t.retry}</Button>}</Alert> : null}
      {traces.data && traces.data.items.length === 0 ? <div className="trace-state">{t.empty}</div> : null}
      {traces.data && traces.data.items.length > 0 ? (
        <div className="trace-table-wrap"><table className="trace-table"><thead><tr><th>{t.started}</th><th>{t.runChat}</th><th>{t.status}</th><th>{t.agentModel}</th><th>{t.latency}</th><th>{t.tokens}</th><th>{t.cost}</th><th /></tr></thead><tbody>{traces.data.items.map((item) => <TraceRow key={item.summary.trace_id} item={item} t={t} onOpen={() => props.onNavigate(`/admin/traces/${encodeURIComponent(item.summary.trace_id)}`)} />)}</tbody></table></div>
      ) : null}
      {traces.data ? <nav className="trace-pagination" aria-label="Trace pages"><Button variant="secondary" disabled={cursorStack.length === 1} onClick={() => setCursorStack((current) => current.slice(0, -1))}>{t.previous}</Button><Button variant="secondary" disabled={!traces.data.next_cursor} onClick={() => traces.data?.next_cursor && setCursorStack((current) => [...current, traces.data!.next_cursor!])}>{t.next}</Button></nav> : null}
    </main>
  );
}

function TraceRow({ item, t, onOpen }: { item: TraceListItem; t: TraceCopy; onOpen: () => void }) {
  const summary = item.summary;
  const identity = workloadIdentity(summary);
  return <tr><td>{formatTime(summary.started_at_unix_nano)}</td><td><strong>{identity}</strong><small>{workloadKindLabel(summary, t)}</small></td><td><StatusPill status={summary.status} active={summary.active} />{item.projection_lagged ? <span className="trace-warning">{t.lagged}</span> : null}{summary.active ? <span className="trace-warning">{t.incomplete}</span> : null}</td><td>{summary.agent_name}<small>{summary.models.join(", ") || "—"}</small></td><td>{formatDuration(summary.duration_nanoseconds, t.unknown)}</td><td>{formatKnown(summary.total_tokens, t.unknown)}</td><td>{formatCost(summary, t.unknownCost)}</td><td><Button variant="ghost" aria-label={`Open Trace ${identity}`} onClick={onOpen}><MaterialSymbol name="chevron_right" size={20} /></Button></td></tr>;
}

function TraceDetailView(props: TraceDashboardProps & { traceID: string }) {
  const t = copy[props.locale];
  const initialSpan = new URLSearchParams(window.location.search).get("span") ?? "";
  const [selectedID, setSelectedID] = useState(initialSpan);
  const [collapsed, setCollapsed] = useState<Set<string>>(new Set());
  const [tab, setTab] = useState("overview");
  const [zoom, setZoom] = useState(1);
  const detail = useQuery({
    queryKey: ["admin-trace", props.traceID],
    queryFn: async () => normalizeTraceDetail(await adminJSON<TraceDetail>(`/api/admin/traces/${encodeURIComponent(props.traceID)}`)),
    retry: false,
    refetchInterval: (query) => tracePollingInterval(query.state.data)
  });
  if (detail.isLoading) return <main className="trace-shell"><TraceTopbar title={props.traceID} onBack={() => props.onNavigate("/admin/traces")} backLabel={t.explorer} /><div className="trace-state" role="status">{t.loading}</div></main>;
  if (detail.isError || !detail.data) { const forbidden = adminErrorCode(detail.error) === "trace_forbidden"; return <main className="trace-shell"><TraceTopbar title={props.traceID} onBack={() => props.onNavigate("/admin/traces")} backLabel={t.explorer} /><Alert variant="destructive" className="trace-state"><AlertDescription>{forbidden ? t.restricted : t.unavailable}</AlertDescription>{forbidden ? null : <Button variant="secondary" onClick={() => void detail.refetch()}>{t.retry}</Button>}</Alert></main>; }
  const data = detail.data;
  const { summary, spans, events, links } = data.projection;
  const selected = spans.find((span) => span.span_id === selectedID) ?? spans[0] ?? null;

  function select(spanID: string) {
    setSelectedID(spanID);
    setCollapsed((current) => {
      const next = new Set(current);
      for (const ancestor of ancestorSpanIDs(spanID, spans)) next.delete(ancestor);
      return next;
    });
    const url = new URL(window.location.href);
    url.searchParams.set("span", spanID);
    window.history.replaceState(null, "", url);
  }

  return (
    <main className="trace-shell trace-detail">
      <TraceTopbar title={workloadIdentity(summary)} onBack={() => props.onNavigate("/admin/traces")} backLabel={t.explorer} />
      <TraceSummaryHeader summary={summary} lagged={data.projected_sequence < data.committed_sequence} t={t} />
      <section className="trace-timeline-panel" aria-label={t.timeline}>
        <div className="trace-section-heading"><h2>{t.timeline}</h2><div><Button variant="ghost" disabled={zoom <= 1} onClick={() => setZoom(Math.max(1, zoom - 0.5))}>{t.zoomOut}</Button><Button variant="ghost" disabled={zoom >= 4} onClick={() => setZoom(Math.min(4, zoom + 0.5))}>{t.zoomIn}</Button><Button variant="ghost" onClick={() => setZoom(1)}>{t.resetZoom}</Button></div></div>
        <Timeline spans={spans} links={links} summary={summary} selectedID={selected?.span_id ?? ""} zoom={zoom} unfinished={t.unfinished} selectLabel={t.selectTimeline} openLinkLabel={t.openLink} onSelect={select} onOpenLink={(link) => link.target_trace_id === summary.trace_id ? select(link.target_span_id) : props.onNavigate(`/admin/traces/${encodeURIComponent(link.target_trace_id)}?span=${encodeURIComponent(link.target_span_id)}`)} />
      </section>
      <div className="trace-workspace">
        <section className="trace-tree-panel"><h2>{t.tree}</h2><div role="tree" aria-label={t.tree}>{spans.filter((span) => !span.parent_span_id).map((root) => <TreeNode key={root.span_id} span={root} spans={spans} depth={0} selectedID={selected?.span_id ?? ""} collapsed={collapsed} t={t} onSelect={select} onToggle={(spanID) => setCollapsed((current) => toggleSet(current, spanID))} />)}</div></section>
        <section className="trace-inspector" aria-label={t.inspector}><h2>{t.inspector}</h2><div className="trace-tabs" role="tablist">{[["overview", t.overview], ["replay", t.replay], ["attributes", t.attributes], ["events", t.eventsLinks]].map(([value, label]) => <button key={value} role="tab" aria-selected={tab === value} onClick={() => setTab(value)}>{label}</button>)}</div>{selected ? <InspectorTab tab={tab} span={selected} events={events} links={links} canReplay={props.canReplay} t={t} onNavigate={props.onNavigate} /> : <p>{t.noSelection}</p>}</section>
      </div>
      <RAGExecution spans={spans} t={t} />
      <TraceAnalysis summary={summary} spans={spans} t={t} />
    </main>
  );
}

function RAGExecution({ spans, t }: { spans: Span[]; t: TraceCopy }) {
  const searches = spans.filter((span) => spanAttribute(span, "agent.action.name") === "search_evidence" || spanAttribute(span, "nano.rag.search.purpose"));
  const verifier = spans.find((span) => span.name === "nano.claim_support");
  const publication = spans.find((span) => span.name === "nano.publication");
  if (!searches.length && !verifier && !publication) return null;
  const search = searches[searches.length - 1];
  const dense = search ? spanAttribute(search, "nano.rag.dense.candidate_count") : "";
  const bm25 = search ? spanAttribute(search, "nano.rag.bm25.candidate_count") : "";
  const rrf = search ? parseAttributeList(spanAttribute(search, "nano.rag.rrf.candidate_ids")) : [];
  const rerank = search ? parseAttributeList(spanAttribute(search, "nano.rag.rerank.candidate_ids")) : [];
  const degradations = searches.flatMap((span) => parseAttributeList(spanAttribute(span, "nano.rag.retrieval.degradations")));
  const supported = verifier ? spanAttribute(verifier, "nano.rag.verifier.supported_count") : "";
  const unsupported = verifier ? spanAttribute(verifier, "nano.rag.verifier.unsupported_count") : "";
  const latencies = [
    ...searches.map((span, index) => [`search_evidence ${index + 1}`, span.duration_nanoseconds] as const),
    ...(verifier ? [["claim_support", verifier.duration_nanoseconds] as const] : []),
    ...(publication ? [["publication", publication.duration_nanoseconds] as const] : [])
  ];
  return <section className="trace-rag" aria-label={t.ragExecution}><h2>{t.ragExecution}</h2><div className="trace-rag-grid"><article><h3>{t.searchPurpose}</h3><strong>{search ? spanAttribute(search, "nano.rag.search.purpose") || t.unknown : t.unknown}</strong><dl><dt>{t.candidates}</dt><dd>{dense || t.unknown} → {bm25 || t.unknown}</dd><dt>{t.rankFlow}</dt><dd>{rrf.join(" → ") || t.unknown}</dd><dt>{t.reranked}</dt><dd>{rerank.join(" → ") || t.unknown}</dd><dt>{t.degradation}</dt><dd>{degradations.join(", ") || t.healthy}</dd></dl></article><article><h3>{t.claimSupport}</h3><strong>{supported || t.unknown} {t.supported} / {unsupported || t.unknown} {t.unsupported}</strong><dl><dt>{t.publicationOutcome}</dt><dd>{publication ? spanAttribute(publication, "nano.rag.grounding.outcome") || t.unknown : t.unknown}</dd></dl></article><article><h3>{t.stageLatency}</h3><dl>{latencies.map(([label, duration]) => <div key={label}><dt>{label}</dt><dd>{formatDuration(duration, t.unknown)}</dd></div>)}</dl></article></div></section>;
}

function TraceTopbar({ title, backLabel, onBack }: { title: string; backLabel: string; onBack: () => void }) {
  return <header className="trace-topbar"><Button variant="ghost" onClick={onBack}><MaterialSymbol name="arrow_back" size={19} />{backLabel}</Button><h1>{title}</h1><span className="trace-live-dot" aria-hidden="true" /></header>;
}

function TraceSummaryHeader({ summary, lagged, t }: { summary: TraceSummary; lagged: boolean; t: TraceCopy }) {
  return <section className="trace-summary" aria-label={t.summary}><div><span>{t.workload}</span><strong>{workloadIdentity(summary)}</strong><small>{workloadKindLabel(summary, t)}</small></div><div><span>Trace</span><strong>{summary.trace_id}</strong></div>{summary.run_id ? <div><span>Run</span><strong>{summary.run_id}</strong></div> : null}{summary.chat_id ? <div><span>Chat</span><strong>{summary.chat_id}</strong></div> : null}<div><span>{t.agent}</span><strong>{summary.agent_name}</strong></div><div><span>{t.model}</span><strong>{summary.models.join(", ") || "—"}</strong></div><div><span>{t.status}</span><StatusPill status={summary.status} active={summary.active} />{lagged ? <span className="trace-warning">{t.lagged}</span> : null}</div><div><span>{t.started}</span><strong>{formatTime(summary.started_at_unix_nano)}</strong></div><div><span>{t.lastObserved}</span><strong>{formatTime(summary.last_observed_unix_nano)}</strong></div><div><span>{t.ended}</span><strong>{summary.ended_at_unix_nano === null ? t.unfinished : formatTime(summary.ended_at_unix_nano)}</strong></div><div><span>{t.duration}</span><strong>{formatDuration(summary.duration_nanoseconds, t.unfinished)}</strong></div><div><span>{t.attempts}</span><strong>{formatKnown(summary.attempt_count, t.unknown)}</strong></div><div><span>{t.tokens}</span><strong>{formatKnown(summary.total_tokens, t.unknown)}</strong></div><div><span>{t.cost}</span><strong>{formatCost(summary, t.unknownCost)}</strong></div></section>;
}

function Timeline({ spans, links, summary, selectedID, zoom, unfinished, selectLabel, openLinkLabel, onSelect, onOpenLink }: { spans: Span[]; links: TraceDetail["projection"]["links"]; summary: TraceSummary; selectedID: string; zoom: number; unfinished: string; selectLabel: string; openLinkLabel: string; onSelect: (id: string) => void; onOpenLink: (link: TraceDetail["projection"]["links"][number]) => void }) {
  const start = summary.started_at_unix_nano;
  const end = Math.max(summary.ended_at_unix_nano ?? summary.last_observed_unix_nano, start + 1);
  const range = end - start;
  return <div className="trace-timeline-scroll"><div className="trace-timeline" style={{ width: `${zoom * 100}%` }}>{spans.map((span) => {
    const left = Math.max(0, ((span.started_at_unix_nano - start) / range) * 100);
    const observedEnd = span.ended_at_unix_nano ?? summary.last_observed_unix_nano;
    const width = Math.max(1.5, ((observedEnd - span.started_at_unix_nano) / range) * 100);
    return <div className="trace-timeline-row" key={span.span_id} style={{ paddingLeft: `${depthOf(span, spans) * 18}px` }}><span>{span.name}</span><button aria-label={`${selectLabel} ${span.name} in Timeline`} className={`trace-timeline-track${span.span_id === selectedID ? " selected" : ""}${span.ended_at_unix_nano === null ? " unfinished" : ""}`} onClick={() => onSelect(span.span_id)}><i style={{ left: `${left}%`, width: `${Math.min(width, 100 - left)}%` }} /><em>{span.ended_at_unix_nano === null ? unfinished : formatDuration(span.duration_nanoseconds, "—")}</em></button></div>;
  })}{links.map((link) => {
    const source = spans.find((span) => span.span_id === link.span_id);
    const left = Math.max(0, Math.min(100, ((link.occurred_at_unix_nano - start) / range) * 100));
    return <div className="trace-timeline-row trace-timeline-link-row" key={`link-${link.sequence}`} style={{ paddingLeft: `${source ? depthOf(source, spans) * 18 : 0}px` }}><span>↗ {link.name}</span><div className="trace-timeline-link-track"><button style={{ left: `${left}%` }} aria-label={`${openLinkLabel} ${link.name} link to ${link.target_trace_id}`} onClick={() => onOpenLink(link)}>{link.target_trace_id}</button></div></div>;
  })}</div></div>;
}

function TreeNode({ span, spans, depth, selectedID, collapsed, t, onSelect, onToggle }: { span: Span; spans: Span[]; depth: number; selectedID: string; collapsed: Set<string>; t: TraceCopy; onSelect: (id: string) => void; onToggle: (id: string) => void }) {
  const children = spans.filter((candidate) => candidate.parent_span_id === span.span_id).sort((a, b) => a.start_sequence - b.start_sequence);
  const isExpanded = !collapsed.has(span.span_id);
  return <div className="trace-tree-branch"><div role="treeitem" aria-selected={selectedID === span.span_id} aria-expanded={children.length ? isExpanded : undefined} className={`trace-tree-item${selectedID === span.span_id ? " selected" : ""}`} style={{ paddingLeft: `${depth * 18 + 8}px` }}>{children.length ? <button aria-label={`${isExpanded ? t.collapse : t.expand} ${span.name}`} onClick={() => onToggle(span.span_id)}><MaterialSymbol name={isExpanded ? "expand_more" : "chevron_right"} size={18} /></button> : <span className="tree-spacer" />}<button onClick={() => onSelect(span.span_id)}><StatusDot span={span} /><span>{span.name}</span><small>{span.ended_at_unix_nano === null ? t.unfinished : formatDuration(span.duration_nanoseconds, "—")}</small></button></div>{isExpanded ? children.map((child) => <TreeNode key={child.span_id} span={child} spans={spans} depth={depth + 1} selectedID={selectedID} collapsed={collapsed} t={t} onSelect={onSelect} onToggle={onToggle} />) : null}</div>;
}

function InspectorTab({ tab, span, events, links, canReplay, t, onNavigate }: { tab: string; span: Span; events: TraceDetail["projection"]["events"]; links: TraceDetail["projection"]["links"]; canReplay: boolean; t: TraceCopy; onNavigate: (path: string) => void }) {
  if (tab === "replay") return <ReplayPanel key={span.span_id} span={span} canReplay={canReplay} t={t} />;
  if (tab === "attributes") return <AttributeList attributes={[...span.start_attributes, ...span.end_attributes]} unknown={t.unknown} />;
  if (tab === "events") {
    const ownEvents = events.filter((event) => event.span_id === span.span_id);
    const ownLinks = links.filter((link) => link.span_id === span.span_id);
    if (!ownEvents.length && !ownLinks.length) return <p>{t.noEvents}</p>;
    return <div className="trace-facts">{ownEvents.map((event) => <article key={`event-${event.sequence}`}><span>Event</span><strong>{event.name}</strong><time>{formatTime(event.occurred_at_unix_nano)}</time></article>)}{ownLinks.map((link) => <article key={`link-${link.sequence}`}><span>Link · {link.name}</span><button onClick={() => onNavigate(`/admin/traces/${encodeURIComponent(link.target_trace_id)}?span=${encodeURIComponent(link.target_span_id)}`)}>{link.target_trace_id} / {link.target_span_id}</button></article>)}</div>;
  }
  const errorKind = spanAttribute(span, "agent.error.kind");
  return <dl className="trace-overview"><dt>Name</dt><dd>{span.name}</dd><dt>{t.kind}</dt><dd>{spanKind(span, t)}</dd><dt>Status</dt><dd>{span.status || t.unfinished}</dd><dt>{t.started}</dt><dd>{formatTime(span.started_at_unix_nano)}</dd><dt>{t.ended}</dt><dd>{span.ended_at_unix_nano === null ? t.unfinished : formatTime(span.ended_at_unix_nano)}</dd><dt>{t.duration}</dt><dd>{formatDuration(span.duration_nanoseconds, t.unfinished)}</dd><dt>Span ID</dt><dd>{span.span_id}</dd>{errorKind ? <><dt>{t.errorKind}</dt><dd>{errorKind}</dd></> : null}{span.model ? <><dt>{t.provider}</dt><dd>{span.model.provider || "—"}</dd><dt>{t.tokens}</dt><dd>{formatKnown(span.model.total_tokens, t.unknown)}</dd><dt>{t.cost}</dt><dd>{span.model.cost.known && span.model.cost.amount !== null ? `${span.model.cost.amount} ${span.model.cost.currency}` : t.unknownCost}</dd></> : null}</dl>;
}

function ReplayPanel({ span, canReplay, t }: { span: Span; canReplay: boolean; t: TraceCopy }) {
  const [reference, setReference] = useState<ReplayReference | null>(span.replay[0] ?? null);
  const [state, setState] = useState<{ status: "idle" | "loading" | "error" | "success"; payload?: unknown; errorCode?: string }>({ status: "idle" });
  if (!canReplay) return <Alert variant="destructive"><AlertDescription>{t.replayForbidden}</AlertDescription></Alert>;
  if (!span.replay.length || !reference) return <p>{t.noReplay}</p>;
  async function load() {
    if (!reference) return;
    setState({ status: "loading" });
    try {
      const payload = await adminJSON<{ payload: unknown }>(`/api/admin/traces/${encodeURIComponent(span.trace_id)}/replay/${encodeURIComponent(reference.attachment_id)}?span_id=${encodeURIComponent(span.span_id)}`);
      setState({ status: "success", payload: payload.payload });
    } catch (error) {
      setState({ status: "error", errorCode: error instanceof AdminAPIError ? error.code : "replay_unavailable" });
    }
  }
  const retryable = state.errorCode !== "replay_expired" && state.errorCode !== "replay_forbidden";
  return <div className="trace-replay"><label>Payload<select value={reference.attachment_id} onChange={(event) => { setReference(span.replay.find((item) => item.attachment_id === event.target.value) ?? null); setState({ status: "idle" }); }}>{span.replay.map((item) => <option key={item.attachment_id} value={item.attachment_id}>{item.class}</option>)}</select></label><p>{t.sensitive}</p>{state.status === "idle" ? <Button onClick={() => void load()}>{t.loadReplay}</Button> : null}{state.status === "loading" ? <p role="status">{t.replayLoading}</p> : null}{state.status === "error" ? <Alert variant="destructive"><AlertDescription>{replayError(state.errorCode, t)}</AlertDescription>{retryable ? <Button variant="secondary" onClick={() => void load()}>{t.retry}</Button> : null}</Alert> : null}{state.status === "success" ? <div className="trace-replay-content"><pre>{JSON.stringify(state.payload, null, 2)}</pre>{collectStrings(state.payload).map((value, index) => <span className="sr-only" key={`${value}-${index}`}>{value}</span>)}</div> : null}</div>;
}

function TraceAnalysis({ summary, spans, t }: { summary: TraceSummary; spans: Span[]; t: TraceCopy }) {
  const modelSpans = spans.filter((span) => span.model);
  const actionSpans = spans.filter((span) => span.name.includes("action"));
  const sum = (items: Span[]) => items.reduce((total, span) => total + (span.duration_nanoseconds ?? 0), 0);
  const modelDuration = sum(modelSpans);
  const actionDuration = sum(actionSpans);
  const totalDuration = summary.duration_nanoseconds;
  const otherDuration = totalDuration === null ? null : Math.max(0, totalDuration - modelDuration - actionDuration);
  return <section className="trace-analysis" aria-label={t.analysis}><h2>{t.analysis}</h2><div className="trace-analysis-grid"><article><h3>{t.latency}</h3><MetricBars rows={[[t.total, totalDuration], [t.modelCalls, modelDuration], [t.actions, actionDuration], [t.other, otherDuration]]} formatter={(value) => formatDuration(value, t.unknown)} /></article><article><h3>{t.tokens}</h3><table><thead><tr><th>Model</th><th>{t.input}</th><th>{t.output}</th><th>{t.cached}</th><th>{t.reasoning}</th></tr></thead><tbody>{modelSpans.map((span) => <tr key={span.span_id}><td>{span.model?.selected_model || span.model?.requested_model || span.name}</td><td>{formatKnown(span.model?.input_tokens ?? null, t.unknown)}</td><td>{formatKnown(span.model?.output_tokens ?? null, t.unknown)}</td><td>{formatKnown(span.model?.cached_tokens ?? null, t.unknown)}</td><td>{formatKnown(span.model?.reasoning_tokens ?? null, t.unknown)}</td></tr>)}</tbody></table></article><article><h3>{t.cost}</h3>{modelSpans.map((span) => <div className="trace-cost-row" key={span.span_id}><span>{span.model?.selected_model || span.name}</span><strong>{span.model?.cost.known && span.model.cost.amount !== null ? <>{span.model.cost.amount} {span.model.cost.currency}{span.model.cost.source ? <small>{span.model.cost.source}</small> : null}</> : t.unknownCost}</strong></div>)}{!modelSpans.length || !summary.cost.known ? <p>{t.unknownCost}</p> : null}</article></div></section>;
}

function MetricBars({ rows, formatter }: { rows: Array<[string, number | null]>; formatter: (value: number | null) => string }) {
  const maximum = Math.max(1, ...rows.map(([, value]) => value ?? 0));
  return <div className="metric-bars">{rows.map(([label, value]) => <div key={label}><span>{label}</span><i><b style={{ width: `${((value ?? 0) / maximum) * 100}%` }} /></i><strong>{formatter(value)}</strong></div>)}</div>;
}

function AttributeList({ attributes, unknown }: { attributes: Attribute[]; unknown: string }) {
  if (!attributes.length) return <p>{unknown}</p>;
  return <dl className="trace-attributes">{attributes.map((attribute, index) => <div key={`${attribute.Key ?? attribute.key}-${index}`}><dt>{attribute.Key ?? attribute.key}</dt><dd>{attributeValue(attribute, unknown)}</dd></div>)}</dl>;
}

function StatusPill({ status, active }: { status: string; active: boolean }) { return <span className={`trace-status ${active ? "active" : status || "unknown"}`}>{active ? "active" : status || "unknown"}</span>; }
function StatusDot({ span }: { span: Span }) { return <i className={`trace-status-dot ${span.ended_at_unix_nano === null ? "active" : span.status || "unknown"}`} />; }
function formatKnown(value: number | null, unknown: string) { return value === null ? unknown : new Intl.NumberFormat().format(value); }
function formatDuration(value: number | null, unknown: string) { if (value === null) return unknown; const ms = value / 1_000_000; return ms < 1000 ? `${ms.toFixed(ms < 10 ? 2 : 0)} ms` : `${(ms / 1000).toFixed(2)} s`; }
function formatTime(value: number) { return new Date(value / 1_000_000).toLocaleString(); }
function formatCost(summary: TraceSummary, unknown: string) { return summary.cost.known && summary.cost.amount !== null ? `${summary.cost.amount} ${summary.cost.currency}` : unknown; }
function workloadIdentity(summary: TraceSummary) { return summary.workload_id || summary.run_id || summary.trace_id; }
function workloadKindLabel(summary: TraceSummary, t: TraceCopy) { return summary.workload_kind === "source_processing" ? t.sourceProcessing : t.agentRun; }
function depthOf(span: Span, spans: Span[]) { let depth = 0; let parent = span.parent_span_id; while (parent && depth < 20) { depth++; parent = spans.find((item) => item.span_id === parent)?.parent_span_id ?? ""; } return depth; }
function ancestorSpanIDs(spanID: string, spans: Span[]) { const ancestors: string[] = []; let parent = spans.find((item) => item.span_id === spanID)?.parent_span_id ?? ""; while (parent && ancestors.length < 20) { ancestors.push(parent); parent = spans.find((item) => item.span_id === parent)?.parent_span_id ?? ""; } return ancestors; }
function toggleSet(current: Set<string>, value: string) { const next = new Set(current); if (next.has(value)) next.delete(value); else next.add(value); return next; }
function attributeValue(attribute: Attribute, unknown: string) { const value = { ...(attribute.Value ?? {}), ...(attribute.value ?? {}) } as Record<string, unknown>; const kind = String(value.Kind ?? value.kind ?? ""); if (kind === "string") return String(value.String ?? value.string ?? ""); if (kind === "int64") return String(value.Int64 ?? value.int64 ?? 0); if (kind === "float64") return String(value.Float64 ?? value.float64 ?? 0); if (kind === "bool") return String(value.Bool ?? value.bool ?? false); return unknown; }
function spanAttribute(span: Span, key: string) { const attribute = [...span.end_attributes, ...span.start_attributes].find((item) => (item.Key ?? item.key) === key); return attribute ? attributeValue(attribute, "") : ""; }
function parseAttributeList(value: string) { if (!value) return [] as string[]; try { const parsed: unknown = JSON.parse(value); return Array.isArray(parsed) ? parsed.filter((item): item is string => typeof item === "string").slice(0, 64) : []; } catch { return []; } }
function spanKind(span: Span, t: TraceCopy) { if (span.name === "agent.execution") return t.agentExecution; if (span.name === "nano.job.attempt") return t.jobAttempt; if (span.name === "agent.model.call" || span.name === "gen_ai.model.call") return t.modelCall; if (span.name === "agent.action" || span.name.includes("action")) return t.actionCall; return t.spanKind; }
function collectStrings(value: unknown): string[] { if (typeof value === "string") return [value]; if (Array.isArray(value)) return value.flatMap(collectStrings); if (value && typeof value === "object") return Object.values(value).flatMap(collectStrings); return []; }
function timeRangeStart(value: string) { const hours = Number.parseInt(value, 10); return Number.isFinite(hours) && hours > 0 ? new Date(Date.now() - hours * 60 * 60 * 1000).toISOString() : ""; }
function replayError(code: string | undefined, t: TraceCopy) { if (code === "replay_forbidden") return t.replayForbidden; if (code === "replay_expired") return t.replayExpired; if (code === "replay_corrupt") return t.replayCorrupt; return t.replayUnavailable; }

function normalizeTraceDetail(detail: TraceDetail): TraceDetail {
  const projection = detail.projection;
  const spans = Array.isArray(projection.spans) ? projection.spans : [];
  const events = Array.isArray(projection.events) ? projection.events : [];
  const links = Array.isArray(projection.links) ? projection.links : [];
  return {
    ...detail,
    projection: {
      ...projection,
      summary: { ...projection.summary, models: Array.isArray(projection.summary.models) ? projection.summary.models : [] },
      spans: spans.map((span) => ({
        ...span,
        start_attributes: Array.isArray(span.start_attributes) ? span.start_attributes : [],
        end_attributes: Array.isArray(span.end_attributes) ? span.end_attributes : [],
        replay: Array.isArray(span.replay) ? span.replay : []
      })),
      events: events.map((event) => ({ ...event, attributes: Array.isArray(event.attributes) ? event.attributes : [] })),
      links: links.map((link) => ({ ...link, attributes: Array.isArray(link.attributes) ? link.attributes : [] }))
    }
  };
}

class AdminAPIError extends Error {
  constructor(readonly code: string) { super(code); }
}
function adminErrorCode(error: unknown) { return error instanceof AdminAPIError ? error.code : ""; }

async function adminJSON<T>(path: string): Promise<T> {
  const response = await fetch(path, { credentials: "include" });
  if (!response.ok) throw new AdminAPIError((await responseErrorCode(response)) || "trace_unavailable");
  return ((await response.json()) as { data: T }).data;
}

async function responseErrorCode(response: Response) { try { return ((await response.json()) as { error?: { code?: string } }).error?.code ?? ""; } catch { return ""; } }
