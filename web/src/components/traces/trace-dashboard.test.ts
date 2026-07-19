import { expect, test } from "vitest";
import { tracePollingInterval } from "./polling";
import type { TraceDetail } from "./types";

test("polls only while a Trace is active or its projection is stale", () => {
  expect(tracePollingInterval(detail(true, 4, 4))).toBe(2000);
  expect(tracePollingInterval(detail(false, 3, 4))).toBe(2000);
  expect(tracePollingInterval(detail(false, 4, 4))).toBe(false);
});

function detail(active: boolean, projected: number, committed: number): TraceDetail {
  return {
    committed_sequence: committed,
    projected_sequence: projected,
    projection: { summary: { active } }
  } as TraceDetail;
}
