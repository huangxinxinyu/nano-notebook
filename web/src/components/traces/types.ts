export type Cost = { known: boolean; amount: number | null; currency: string; source: string };

export type TraceSummary = {
  trace_id: string;
  run_id: string;
  chat_id: string;
  notebook_id: string;
  root_span_id: string;
  agent_name: string;
  started_at_unix_nano: number;
  last_observed_unix_nano: number;
  ended_at_unix_nano: number | null;
  duration_nanoseconds: number | null;
  status: string;
  active: boolean;
  models: string[];
  input_tokens: number | null;
  output_tokens: number | null;
  total_tokens: number | null;
  cost: Cost;
  attempt_count: number;
};

export type TraceListItem = {
  summary: TraceSummary;
  committed_sequence: number;
  projected_sequence: number;
  projection_lagged: boolean;
};

export type Attribute = {
  Key?: string;
  key?: string;
  Value?: { Kind?: string; String?: string; Int64?: number; Float64?: number; Bool?: boolean };
  value?: { kind?: string; string?: string; int64?: number; float64?: number; bool?: boolean };
};

export type ReplayReference = { attachment_id: string; class: string; record_sequence: number };

export type ModelAnalysis = {
  requested_model: string;
  selected_model: string;
  provider: string;
  input_tokens: number | null;
  output_tokens: number | null;
  total_tokens: number | null;
  cached_tokens: number | null;
  reasoning_tokens: number | null;
  gateway_retries: number | null;
  gateway_fallbacks: number | null;
  duration_nanoseconds: number | null;
  cost: Cost;
};

export type Span = {
  trace_id: string;
  span_id: string;
  parent_span_id: string;
  name: string;
  start_sequence: number;
  end_sequence: number | null;
  started_at_unix_nano: number;
  ended_at_unix_nano: number | null;
  duration_nanoseconds: number | null;
  status: string;
  start_attributes: Attribute[];
  end_attributes: Attribute[];
  replay: ReplayReference[];
  model: ModelAnalysis | null;
};

export type TraceEvent = {
  trace_id: string;
  sequence: number;
  span_id: string;
  name: string;
  occurred_at_unix_nano: number;
  attributes: Attribute[];
};

export type TraceLink = {
  trace_id: string;
  sequence: number;
  span_id: string;
  name: string;
  target_trace_id: string;
  target_span_id: string;
  occurred_at_unix_nano: number;
  attributes: Attribute[];
};

export type TraceDetail = {
  projection: { summary: TraceSummary; spans: Span[]; events: TraceEvent[]; links: TraceLink[] };
  committed_sequence: number;
  projected_sequence: number;
};
