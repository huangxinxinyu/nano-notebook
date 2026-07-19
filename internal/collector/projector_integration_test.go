package collector_test

import (
	"context"
	"testing"
	"time"

	"github.com/huangxinxinyu/nano-notebook/internal/agentobs"
	"github.com/huangxinxinyu/nano-notebook/internal/collector"
	"github.com/huangxinxinyu/nano-notebook/internal/replay"
)

func TestProjectorPersistsDeterministicViewsAndAdvancesWatermark(t *testing.T) {
	ctx := context.Background()
	pool := openObservabilityTestPool(t, ctx)
	t.Cleanup(pool.Close)
	resetObservabilityTestSchema(t, ctx, pool)
	stored := projectionStoredTrace(t, true, true)
	store := collector.NewPostgresStore(pool)
	if _, err := store.CommitTraceChunk(ctx, collector.TraceChunk{
		Trace: stored.Trace, FirstSequence: 1, Records: stored.Records,
	}); err != nil {
		t.Fatalf("CommitTraceChunk: %v", err)
	}

	projector, err := collector.NewProjector(pool, collector.ProjectorConfig{RetryDelay: time.Millisecond})
	if err != nil {
		t.Fatalf("NewProjector: %v", err)
	}
	projected, err := projector.RunOnce(ctx)
	if err != nil || !projected {
		t.Fatalf("RunOnce projected=%t error=%v", projected, err)
	}
	var projectedSequence, spanCount, eventCount, linkCount, totalTokens int
	var active, costKnown bool
	if err := pool.QueryRow(ctx, `
		select t.projected_sequence,
			(select count(*) from obs_spans where trace_id = t.trace_id),
			(select count(*) from obs_events where trace_id = t.trace_id),
			(select count(*) from obs_links where trace_id = t.trace_id),
			s.active, s.total_tokens, s.cost_known
		from obs_traces t join obs_trace_summaries s using (trace_id)
		where t.trace_id = $1
	`, stored.Trace.TraceID).Scan(&projectedSequence, &spanCount, &eventCount, &linkCount, &active, &totalTokens, &costKnown); err != nil {
		t.Fatalf("load projection: %v", err)
	}
	if projectedSequence != len(stored.Records) || spanCount != 4 || eventCount != 1 || linkCount != 1 || active || totalTokens != 27 || !costKnown {
		t.Fatalf("projection cursor=%d spans=%d events=%d links=%d active=%t tokens=%d costKnown=%t",
			projectedSequence, spanCount, eventCount, linkCount, active, totalTokens, costKnown)
	}

	first, err := collector.LoadProjectedTrace(ctx, pool, stored.Trace.TraceID)
	if err != nil {
		t.Fatalf("LoadProjectedTrace first: %v", err)
	}
	if err := projector.RebuildTrace(ctx, stored.Trace.TraceID); err != nil {
		t.Fatalf("RebuildTrace: %v", err)
	}
	second, err := collector.LoadProjectedTrace(ctx, pool, stored.Trace.TraceID)
	if err != nil {
		t.Fatalf("LoadProjectedTrace second: %v", err)
	}
	if first.CanonicalJSON != second.CanonicalJSON {
		t.Fatalf("projection rebuild changed view\nfirst: %s\nsecond: %s", first.CanonicalJSON, second.CanonicalJSON)
	}
}

func collectorInvalidReplayReference() agentobs.Attribute {
	return agentobs.String(replay.ModelRequestAttachmentKey, "not-an-attachment-id")
}

func TestProjectorFailureLeavesRawCursorAndDiagnostic(t *testing.T) {
	ctx := context.Background()
	pool := openObservabilityTestPool(t, ctx)
	t.Cleanup(pool.Close)
	resetObservabilityTestSchema(t, ctx, pool)
	stored := projectionStoredTrace(t, true, true)
	if _, err := collector.NewPostgresStore(pool).CommitTraceChunk(ctx, collector.TraceChunk{
		Trace: stored.Trace, FirstSequence: 1, Records: stored.Records,
	}); err != nil {
		t.Fatalf("CommitTraceChunk: %v", err)
	}
	malformed := stored.Records[3].Record
	malformed.Attributes = append(malformed.Attributes, collectorInvalidReplayReference())
	malformedEnvelope := collectorEnvelope(t, 4, malformed)
	payload, err := malformed.CanonicalPayload()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `drop trigger obs_trace_records_immutable_update on obs_trace_records`); err != nil {
		t.Fatalf("drop fixture immutability trigger: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		update obs_trace_records set canonical_payload = $3, canonical_sha256 = $4
		where trace_id = $1 and sequence = $2
	`, stored.Trace.TraceID, 4, payload, malformedEnvelope.CanonicalSHA256); err != nil {
		t.Fatalf("seed malformed historical record: %v", err)
	}
	if err := collector.RunMigrations(ctx, pool); err != nil {
		t.Fatalf("restore Collector invariants: %v", err)
	}
	projector, _ := collector.NewProjector(pool, collector.ProjectorConfig{RetryDelay: time.Millisecond})
	projected, err := projector.RunOnce(ctx)
	if err == nil || projected {
		t.Fatalf("RunOnce projected=%t error=%v", projected, err)
	}
	var committed, projectedSequence, rawCount int
	var diagnostic string
	if err := pool.QueryRow(ctx, `
		select committed_sequence, projected_sequence,
			(select count(*) from obs_trace_records where trace_id = obs_traces.trace_id),
			(select last_error_code from obs_projection_queue where trace_id = obs_traces.trace_id)
		from obs_traces where trace_id = $1
	`, stored.Trace.TraceID).Scan(&committed, &projectedSequence, &rawCount, &diagnostic); err != nil {
		t.Fatalf("load failure state: %v", err)
	}
	if committed != len(stored.Records) || projectedSequence != 0 || rawCount != len(stored.Records) || diagnostic == "" {
		t.Fatalf("failure state committed=%d projected=%d raw=%d diagnostic=%q", committed, projectedSequence, rawCount, diagnostic)
	}
}
