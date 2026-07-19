import type { TraceDetail } from "./types";

export function tracePollingInterval(data: TraceDetail | undefined) {
  return data && (data.projection.summary.active || data.projected_sequence < data.committed_sequence) ? 2000 : false;
}
