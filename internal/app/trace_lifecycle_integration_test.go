package app_test

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/huangxinxinyu/nano-notebook/internal/agent"
	"github.com/huangxinxinyu/nano-notebook/internal/agentobs"
	"github.com/huangxinxinyu/nano-notebook/internal/agentobs/semconv"
	"github.com/huangxinxinyu/nano-notebook/internal/agentoutbox"
	"github.com/huangxinxinyu/nano-notebook/internal/collector"
	"github.com/huangxinxinyu/nano-notebook/internal/jobs"
	"github.com/huangxinxinyu/nano-notebook/internal/models"
	"github.com/jackc/pgx/v5"
)

func TestAdmissionAtomicallyStartsDurableTraceAndReplayDoesNotDuplicate(t *testing.T) {
	api, sessionCookie, csrfCookie, chatID := newChatFixture(t, "trace-admission@example.com")
	const messageID = "0190cdd2-5f2d-7ad8-b3f5-1b588788c420"
	runID := admitRunForLeaseTest(t, api, sessionCookie, csrfCookie, chatID, messageID)
	ctx := context.Background()

	trace, err := agent.LoadDurableTraceByRun(ctx, api.db.Pool(), runID)
	if err != nil {
		t.Fatalf("Load admitted Trace: %v", err)
	}
	if trace.RunID != runID || trace.TraceID == "" || trace.RootSpanID == "" || len(trace.Records) != 2 {
		t.Fatalf("admitted Trace envelope/records = %#v", trace)
	}
	root, admitted := trace.Records[0], trace.Records[1]
	if root.Kind != agentobs.RecordSpanStarted || root.SpanID != trace.RootSpanID || root.ParentSpanID != "" || root.Name != agent.TraceSpanAgentExecution {
		t.Fatalf("admitted root = %#v", root)
	}
	if admitted.Kind != agentobs.RecordEvent || admitted.SpanID != trace.RootSpanID || admitted.Name != agent.TraceEventRunAdmitted {
		t.Fatalf("admitted Event = %#v", admitted)
	}
	var outboxTraceID string
	var nextSequence, collectorCursor, outboxRecords int
	var deliveryState string
	if err := api.db.Pool().QueryRow(ctx, `
		select trace_id, next_sequence, collector_cursor, delivery_state
		from agent_trace_refs where run_id = $1
	`, runID).Scan(&outboxTraceID, &nextSequence, &collectorCursor, &deliveryState); err != nil {
		t.Fatalf("load admitted Outbox Trace ref: %v", err)
	}
	if err := api.db.Pool().QueryRow(ctx, `
		select count(*) from agentobs_outbox_records where trace_id = $1
	`, trace.TraceID).Scan(&outboxRecords); err != nil {
		t.Fatalf("count admitted Outbox records: %v", err)
	}
	if outboxTraceID != string(trace.TraceID) || nextSequence != 3 || collectorCursor != 0 || deliveryState != "ready" || outboxRecords != 2 {
		t.Fatalf("admitted Outbox ref/records = %s %d/%d %s records=%d", outboxTraceID, nextSequence, collectorCursor, deliveryState, outboxRecords)
	}
	outboxStore, err := agentoutbox.NewPostgresStore(api.db.Pool(), agentoutbox.Config{
		ProducerID: "nano-worker", MaxRecords: 128, MaxEncodedBytes: 512 * 1024,
		MaxTraces: 16, LeaseDuration: 30 * time.Second,
	})
	if err != nil {
		t.Fatalf("NewPostgresStore: %v", err)
	}
	claimed, ok, err := outboxStore.ClaimBatch(ctx)
	if err != nil || !ok {
		t.Fatalf("ClaimBatch = %#v ok=%t err=%v", claimed, ok, err)
	}
	if claimed.LeaseToken == "" || claimed.Batch.ProducerID != "nano-worker" || len(claimed.Batch.Chunks) != 1 {
		t.Fatalf("claimed Batch = %#v", claimed)
	}
	chunk := claimed.Batch.Chunks[0]
	if chunk.Trace.TraceID != trace.TraceID || chunk.Trace.RunID != runID || chunk.FirstSequence != 1 || len(chunk.Records) != 2 {
		t.Fatalf("claimed Trace Chunk = %#v", chunk)
	}
	for index, envelope := range chunk.Records {
		wantHash, hashErr := trace.Records[index].CanonicalHash()
		if hashErr != nil {
			t.Fatalf("record %d CanonicalHash: %v", index, hashErr)
		}
		if envelope.Sequence != index+1 || envelope.Record.IdentityKey != trace.Records[index].IdentityKey || envelope.CanonicalSHA256 != hex.EncodeToString(wantHash[:]) {
			t.Fatalf("claimed record %d = %#v", index, envelope)
		}
	}
	secondStore, err := agentoutbox.NewPostgresStore(api.db.Pool(), agentoutbox.Config{
		ProducerID: "nano-worker-2", MaxRecords: 128, MaxEncodedBytes: 512 * 1024,
		MaxTraces: 16, LeaseDuration: 30 * time.Second,
	})
	if err != nil {
		t.Fatalf("NewPostgresStore second sender: %v", err)
	}
	if duplicate, duplicateOK, duplicateErr := secondStore.ClaimBatch(ctx); duplicateErr != nil || duplicateOK {
		t.Fatalf("concurrent ClaimBatch = %#v ok=%t err=%v", duplicate, duplicateOK, duplicateErr)
	}
	if _, err := api.db.Pool().Exec(ctx, `
		update agent_trace_refs set lease_expires_at = now() - interval '1 second'
		where trace_id = $1 and lease_token = $2
	`, trace.TraceID, claimed.LeaseToken); err != nil {
		t.Fatalf("expire sender lease: %v", err)
	}
	reclaimed, reclaimedOK, err := secondStore.ClaimBatch(ctx)
	if err != nil || !reclaimedOK {
		t.Fatalf("reclaimed ClaimBatch = %#v ok=%t err=%v", reclaimed, reclaimedOK, err)
	}
	if reclaimed.LeaseToken == claimed.LeaseToken || reclaimed.Batch.ProducerID != "nano-worker-2" || len(reclaimed.Batch.Chunks) != 1 || len(reclaimed.Batch.Chunks[0].Records) != 2 {
		t.Fatalf("reclaimed Batch = %#v", reclaimed)
	}
	var senderAttemptCount int
	if err := api.db.Pool().QueryRow(ctx, `
		select attempt_count from agent_trace_refs where trace_id = $1
	`, trace.TraceID).Scan(&senderAttemptCount); err != nil {
		t.Fatalf("load sender attempt count: %v", err)
	}
	if senderAttemptCount != 2 {
		t.Fatalf("sender attempt count = %d, want 2", senderAttemptCount)
	}
	if err := secondStore.ApplyResult(ctx, reclaimed, collector.BatchResult{
		BatchID: reclaimed.Batch.BatchID,
		Chunks: []collector.ChunkResult{{
			TraceID: trace.TraceID, Status: collector.ChunkCommitted, CommittedThrough: 2,
		}},
	}); err != nil {
		t.Fatalf("ApplyResult: %v", err)
	}
	var acknowledgedCursor, retainedRecords int
	var acknowledgedState string
	if err := api.db.Pool().QueryRow(ctx, `
		select r.collector_cursor, r.delivery_state, count(o.sequence_no)
		from agent_trace_refs r
		left join agentobs_outbox_records o on o.trace_id = r.trace_id
		where r.trace_id = $1
		group by r.trace_id
	`, trace.TraceID).Scan(&acknowledgedCursor, &acknowledgedState, &retainedRecords); err != nil {
		t.Fatalf("load acknowledged Outbox Trace: %v", err)
	}
	if acknowledgedCursor != 2 || acknowledgedState != "acknowledged" || retainedRecords != 2 {
		t.Fatalf("acknowledged Outbox = cursor %d state %s records %d", acknowledgedCursor, acknowledgedState, retainedRecords)
	}

	replayedRunID := admitRunForLeaseTest(t, api, sessionCookie, csrfCookie, chatID, messageID)
	if replayedRunID != runID {
		t.Fatalf("replay Run = %q, want %q", replayedRunID, runID)
	}
	replayed, err := agent.LoadDurableTraceByRun(ctx, api.db.Pool(), runID)
	if err != nil {
		t.Fatal(err)
	}
	if replayed.TraceID != trace.TraceID || len(replayed.Records) != 2 {
		t.Fatalf("replay changed Trace = %#v", replayed)
	}
}

func TestOutboxRetryableResultRetainsTraceAndPermanentResultQuarantinesIt(t *testing.T) {
	api, sessionCookie, csrfCookie, chatID := newChatFixture(t, "trace-delivery-result@example.com")
	runID := admitRunForLeaseTest(t, api, sessionCookie, csrfCookie, chatID, "0190cdd2-5f2d-7ad8-b3f5-1b588788c421")
	trace, err := agent.LoadDurableTraceByRun(context.Background(), api.db.Pool(), runID)
	if err != nil {
		t.Fatalf("Load admitted Trace: %v", err)
	}
	store, err := agentoutbox.NewPostgresStore(api.db.Pool(), agentoutbox.Config{
		ProducerID: "nano-worker", MaxRecords: 128, MaxEncodedBytes: 512 * 1024,
		MaxTraces: 16, LeaseDuration: 30 * time.Second,
		BaseBackoff: time.Second, MaxBackoff: time.Minute,
		RetryJitter: func() float64 { return 0 },
	})
	if err != nil {
		t.Fatalf("NewPostgresStore: %v", err)
	}
	ctx := context.Background()
	claimed, ok, err := store.ClaimBatch(ctx)
	if err != nil || !ok {
		t.Fatalf("ClaimBatch = %#v ok=%t err=%v", claimed, ok, err)
	}
	retryStartedAt := time.Now()
	if err := store.ApplyResult(ctx, claimed, collector.BatchResult{
		BatchID: claimed.Batch.BatchID,
		Chunks: []collector.ChunkResult{{
			TraceID: trace.TraceID, Status: collector.ChunkRetryable,
			CommittedThrough: 0, Code: collector.CodeSequenceGap,
		}},
	}); err != nil {
		t.Fatalf("ApplyResult retryable: %v", err)
	}
	var retryState, retryCode string
	var retryCursor, retainedRecords int
	var retryLeaseCleared bool
	var nextAttemptAt time.Time
	if err := api.db.Pool().QueryRow(ctx, `
		select r.delivery_state, r.collector_cursor, r.last_error_code,
			r.lease_token is null, r.next_attempt_at, count(o.sequence_no)
		from agent_trace_refs r
		left join agentobs_outbox_records o on o.trace_id = r.trace_id
		where r.trace_id = $1
		group by r.trace_id
	`, trace.TraceID).Scan(&retryState, &retryCursor, &retryCode, &retryLeaseCleared, &nextAttemptAt, &retainedRecords); err != nil {
		t.Fatalf("load retryable Outbox state: %v", err)
	}
	if retryState != "ready" || retryCursor != 0 || retryCode != collector.CodeSequenceGap || !retryLeaseCleared || nextAttemptAt.Before(retryStartedAt.Add(450*time.Millisecond)) || nextAttemptAt.After(retryStartedAt.Add(900*time.Millisecond)) || retainedRecords != 2 {
		t.Fatalf("retryable Outbox = state %s cursor %d code %s cleared=%t next=%s records=%d", retryState, retryCursor, retryCode, retryLeaseCleared, nextAttemptAt, retainedRecords)
	}
	if _, err := api.db.Pool().Exec(ctx, `update agent_trace_refs set next_attempt_at = now() - interval '1 second' where trace_id = $1`, trace.TraceID); err != nil {
		t.Fatalf("make retry due: %v", err)
	}
	reclaimed, ok, err := store.ClaimBatch(ctx)
	if err != nil || !ok {
		t.Fatalf("reclaim retryable Batch = %#v ok=%t err=%v", reclaimed, ok, err)
	}
	if err := store.ApplyResult(ctx, reclaimed, collector.BatchResult{
		BatchID: reclaimed.Batch.BatchID,
		Chunks: []collector.ChunkResult{{
			TraceID: trace.TraceID, Status: collector.ChunkRejected,
			CommittedThrough: 0, Code: collector.CodeIdentityConflict,
		}},
	}); err != nil {
		t.Fatalf("ApplyResult rejected: %v", err)
	}
	var quarantineState, quarantineCode string
	var quarantineCursor, quarantinedRecords int
	var quarantineLeaseCleared, hasQuarantinedAt bool
	if err := api.db.Pool().QueryRow(ctx, `
		select r.delivery_state, r.collector_cursor, r.last_error_code,
			r.lease_token is null, r.quarantined_at is not null, count(o.sequence_no)
		from agent_trace_refs r
		left join agentobs_outbox_records o on o.trace_id = r.trace_id
		where r.trace_id = $1
		group by r.trace_id
	`, trace.TraceID).Scan(&quarantineState, &quarantineCursor, &quarantineCode, &quarantineLeaseCleared, &hasQuarantinedAt, &quarantinedRecords); err != nil {
		t.Fatalf("load quarantined Outbox state: %v", err)
	}
	if quarantineState != "quarantined" || quarantineCursor != 0 || quarantineCode != collector.CodeIdentityConflict || !quarantineLeaseCleared || !hasQuarantinedAt || quarantinedRecords != 2 {
		t.Fatalf("quarantined Outbox = state %s cursor %d code %s cleared=%t at=%t records=%d", quarantineState, quarantineCursor, quarantineCode, quarantineLeaseCleared, hasQuarantinedAt, quarantinedRecords)
	}
}

func TestOutboxBatchUsesExactCollectorRecordBytes(t *testing.T) {
	api, sessionCookie, csrfCookie, chatID := newChatFixture(t, "trace-batch-bytes@example.com")
	runID := admitRunForLeaseTest(t, api, sessionCookie, csrfCookie, chatID, "0190cdd2-5f2d-7ad8-b3f5-1b588788c425")
	trace, err := agent.LoadDurableTraceByRun(context.Background(), api.db.Pool(), runID)
	if err != nil {
		t.Fatalf("Load admitted Trace: %v", err)
	}
	wantSizes := make([]int, 0, len(trace.Records))
	for index, record := range trace.Records {
		hash, err := record.CanonicalHash()
		if err != nil {
			t.Fatalf("record %d CanonicalHash: %v", index, err)
		}
		encoded, err := json.Marshal(collector.SequencedRecord{
			Sequence: index + 1, Record: record, CanonicalSHA256: hex.EncodeToString(hash[:]),
		})
		if err != nil {
			t.Fatalf("marshal record %d: %v", index, err)
		}
		wantSizes = append(wantSizes, len(encoded))
	}
	rows, err := api.db.Pool().Query(context.Background(), `
		select encoded_bytes from agentobs_outbox_records
		where trace_id = $1 order by sequence_no
	`, trace.TraceID)
	if err != nil {
		t.Fatalf("load Outbox encoded sizes: %v", err)
	}
	defer rows.Close()
	var gotSizes []int
	for rows.Next() {
		var size int
		if err := rows.Scan(&size); err != nil {
			t.Fatalf("scan Outbox encoded size: %v", err)
		}
		gotSizes = append(gotSizes, size)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("load Outbox encoded sizes: %v", err)
	}
	if len(gotSizes) != len(wantSizes) || gotSizes[0] != wantSizes[0] || gotSizes[1] != wantSizes[1] {
		t.Fatalf("Outbox encoded sizes = %v, want %v", gotSizes, wantSizes)
	}
	store, err := agentoutbox.NewPostgresStore(api.db.Pool(), agentoutbox.Config{
		ProducerID: "nano-worker", MaxRecords: 128,
		MaxEncodedBytes: wantSizes[0] + wantSizes[1] - 1,
		MaxTraces:       16, LeaseDuration: 30 * time.Second, MaxDelay: time.Hour,
	})
	if err != nil {
		t.Fatalf("NewPostgresStore: %v", err)
	}
	claimed, ok, err := store.ClaimBatch(context.Background())
	if err != nil || !ok {
		t.Fatalf("ClaimBatch = %#v ok=%t err=%v", claimed, ok, err)
	}
	if len(claimed.Batch.Chunks) != 1 || len(claimed.Batch.Chunks[0].Records) != 1 {
		t.Fatalf("byte-bounded Batch = %#v", claimed.Batch)
	}
}

func TestOutboxBatchFlushesAtRecordCountAndOldestDelayThresholds(t *testing.T) {
	t.Run("record count", func(t *testing.T) {
		api, sessionCookie, csrfCookie, chatID := newChatFixture(t, "trace-batch-count@example.com")
		_ = admitRunForLeaseTest(t, api, sessionCookie, csrfCookie, chatID, "0190cdd2-5f2d-7ad8-b3f5-1b588788c433")
		store, err := agentoutbox.NewPostgresStore(api.db.Pool(), agentoutbox.Config{
			ProducerID: "nano-worker", MaxRecords: 2, MaxEncodedBytes: 512 * 1024,
			MaxTraces: 16, LeaseDuration: 30 * time.Second, MaxDelay: time.Hour,
		})
		if err != nil {
			t.Fatalf("NewPostgresStore: %v", err)
		}
		claimed, ok, err := store.ClaimBatch(context.Background())
		if err != nil || !ok || len(claimed.Batch.Chunks) != 1 || len(claimed.Batch.Chunks[0].Records) != 2 {
			t.Fatalf("record-count Batch = %#v ok=%t err=%v", claimed, ok, err)
		}
	})

	t.Run("oldest delay", func(t *testing.T) {
		api, sessionCookie, csrfCookie, chatID := newChatFixture(t, "trace-batch-oldest@example.com")
		runID := admitRunForLeaseTest(t, api, sessionCookie, csrfCookie, chatID, "0190cdd2-5f2d-7ad8-b3f5-1b588788c434")
		if _, err := api.db.Pool().Exec(context.Background(), `
			update agentobs_outbox_records set created_at = now() - interval '2 minutes'
			where trace_id = (select trace_id from agent_trace_refs where run_id = $1)
		`, runID); err != nil {
			t.Fatalf("age Outbox records: %v", err)
		}
		store, err := agentoutbox.NewPostgresStore(api.db.Pool(), agentoutbox.Config{
			ProducerID: "nano-worker", MaxRecords: 128, MaxEncodedBytes: 512 * 1024,
			MaxTraces: 16, LeaseDuration: 30 * time.Second, MaxDelay: time.Minute,
		})
		if err != nil {
			t.Fatalf("NewPostgresStore: %v", err)
		}
		claimed, ok, err := store.ClaimBatch(context.Background())
		if err != nil || !ok || len(claimed.Batch.Chunks) != 1 || len(claimed.Batch.Chunks[0].Records) != 2 {
			t.Fatalf("oldest-delay Batch = %#v ok=%t err=%v", claimed, ok, err)
		}
	})
}

func TestOutboxBatchWaitsForDelayButFlushesTerminalTraceImmediately(t *testing.T) {
	api, sessionCookie, csrfCookie, chatID := newChatFixture(t, "trace-batch-delay@example.com")
	runID := admitRunForLeaseTest(t, api, sessionCookie, csrfCookie, chatID, "0190cdd2-5f2d-7ad8-b3f5-1b588788c429")
	store, err := agentoutbox.NewPostgresStore(api.db.Pool(), agentoutbox.Config{
		ProducerID: "nano-worker", MaxRecords: 128, MaxEncodedBytes: 512 * 1024,
		MaxTraces: 16, LeaseDuration: 30 * time.Second, MaxDelay: time.Hour,
	})
	if err != nil {
		t.Fatalf("NewPostgresStore: %v", err)
	}
	ctx := context.Background()
	if waiting, ok, err := store.ClaimBatch(ctx); err != nil || ok {
		t.Fatalf("young partial ClaimBatch = %#v ok=%t err=%v", waiting, ok, err)
	}
	response := api.postJSONWithCookieAndCSRF(t, "/api/v1/agent-runs/"+runID+"/cancel", map[string]any{}, sessionCookie, csrfCookie, csrfCookie.Value, "")
	if response.Code != http.StatusOK {
		t.Fatalf("cancel status = %d body=%s", response.Code, response.Body.String())
	}
	terminal, ok, err := store.ClaimBatch(ctx)
	if err != nil || !ok {
		t.Fatalf("terminal ClaimBatch = %#v ok=%t err=%v", terminal, ok, err)
	}
	if len(terminal.Batch.Chunks) != 1 || len(terminal.Batch.Chunks[0].Records) != 5 {
		t.Fatalf("terminal Batch = %#v", terminal.Batch)
	}
}

func TestOutboxMixedBatchCommitsAndQuarantinesTracesIndependently(t *testing.T) {
	api, sessionCookie, csrfCookie, chatID := newChatFixture(t, "trace-mixed-batch@example.com")
	firstRunID := admitRunForLeaseTest(t, api, sessionCookie, csrfCookie, chatID, "0190cdd2-5f2d-7ad8-b3f5-1b588788c431")
	firstTrace, err := agent.LoadDurableTraceByRun(context.Background(), api.db.Pool(), firstRunID)
	if err != nil {
		t.Fatalf("load first Trace: %v", err)
	}
	cancelled := api.postJSONWithCookieAndCSRF(t, "/api/v1/agent-runs/"+firstRunID+"/cancel", map[string]any{}, sessionCookie, csrfCookie, csrfCookie.Value, "")
	if cancelled.Code != http.StatusOK {
		t.Fatalf("cancel first Run = %d body=%s", cancelled.Code, cancelled.Body.String())
	}
	secondRunID := admitRunForLeaseTest(t, api, sessionCookie, csrfCookie, chatID, "0190cdd2-5f2d-7ad8-b3f5-1b588788c432")
	secondTrace, err := agent.LoadDurableTraceByRun(context.Background(), api.db.Pool(), secondRunID)
	if err != nil {
		t.Fatalf("load second Trace: %v", err)
	}
	store, err := agentoutbox.NewPostgresStore(api.db.Pool(), agentoutbox.Config{
		ProducerID: "nano-worker", MaxRecords: 128, MaxEncodedBytes: 512 * 1024,
		MaxTraces: 16, LeaseDuration: 30 * time.Second,
	})
	if err != nil {
		t.Fatalf("NewPostgresStore: %v", err)
	}
	ctx := context.Background()
	claimed, ok, err := store.ClaimBatch(ctx)
	if err != nil || !ok {
		t.Fatalf("ClaimBatch = %#v ok=%t err=%v", claimed, ok, err)
	}
	if len(claimed.Batch.Chunks) != 2 {
		t.Fatalf("mixed Batch = %#v", claimed.Batch)
	}
	lastByTrace := make(map[agentobs.TraceID]int)
	for _, chunk := range claimed.Batch.Chunks {
		lastByTrace[chunk.Trace.TraceID] = chunk.Records[len(chunk.Records)-1].Sequence
	}
	if lastByTrace[firstTrace.TraceID] != 5 || lastByTrace[secondTrace.TraceID] != 2 {
		t.Fatalf("mixed Batch cursors = %#v", lastByTrace)
	}
	if err := store.ApplyResult(ctx, claimed, collector.BatchResult{
		BatchID: claimed.Batch.BatchID,
		Chunks: []collector.ChunkResult{
			{TraceID: secondTrace.TraceID, Status: collector.ChunkRejected, CommittedThrough: 0, Code: collector.CodeIdentityConflict},
			{TraceID: firstTrace.TraceID, Status: collector.ChunkCommitted, CommittedThrough: 5},
		},
	}); err != nil {
		t.Fatalf("ApplyResult mixed: %v", err)
	}
	for _, expectation := range []struct {
		traceID agentobs.TraceID
		state   string
		cursor  int
		records int
	}{
		{traceID: firstTrace.TraceID, state: "acknowledged", cursor: 5, records: 0},
		{traceID: secondTrace.TraceID, state: "quarantined", cursor: 0, records: 2},
	} {
		var state string
		var cursor, records int
		if err := api.db.Pool().QueryRow(ctx, `
			select r.delivery_state, r.collector_cursor, count(o.sequence_no)
			from agent_trace_refs r left join agentobs_outbox_records o on o.trace_id = r.trace_id
			where r.trace_id = $1 group by r.trace_id
		`, expectation.traceID).Scan(&state, &cursor, &records); err != nil {
			t.Fatalf("load mixed result Trace %s: %v", expectation.traceID, err)
		}
		if state != expectation.state || cursor != expectation.cursor || records != expectation.records {
			t.Fatalf("mixed result Trace %s = %s/%d/%d", expectation.traceID, state, cursor, records)
		}
	}
}

func TestOutboxLeaseAllowsTerminalAppendAndCleansOnlyAfterTerminalACK(t *testing.T) {
	api, sessionCookie, csrfCookie, chatID := newChatFixture(t, "trace-terminal-while-leased@example.com")
	runID := admitRunForLeaseTest(t, api, sessionCookie, csrfCookie, chatID, "0190cdd2-5f2d-7ad8-b3f5-1b588788c426")
	trace, err := agent.LoadDurableTraceByRun(context.Background(), api.db.Pool(), runID)
	if err != nil {
		t.Fatalf("Load admitted Trace: %v", err)
	}
	store, err := agentoutbox.NewPostgresStore(api.db.Pool(), agentoutbox.Config{
		ProducerID: "nano-worker", MaxRecords: 128, MaxEncodedBytes: 512 * 1024,
		MaxTraces: 16, LeaseDuration: 30 * time.Second,
	})
	if err != nil {
		t.Fatalf("NewPostgresStore: %v", err)
	}
	ctx := context.Background()
	prefix, ok, err := store.ClaimBatch(ctx)
	if err != nil || !ok {
		t.Fatalf("claim prefix = %#v ok=%t err=%v", prefix, ok, err)
	}
	response := api.postJSONWithCookieAndCSRF(t, "/api/v1/agent-runs/"+runID+"/cancel", map[string]any{}, sessionCookie, csrfCookie, csrfCookie.Value, "")
	if response.Code != http.StatusOK {
		t.Fatalf("cancel while Outbox leased status = %d body=%s", response.Code, response.Body.String())
	}
	var stateWhileLeased string
	var nextSequence int
	var terminalSequence *int
	if err := api.db.Pool().QueryRow(ctx, `
		select delivery_state, next_sequence, terminal_sequence
		from agent_trace_refs where trace_id = $1
	`, trace.TraceID).Scan(&stateWhileLeased, &nextSequence, &terminalSequence); err != nil {
		t.Fatalf("load leased terminal append: %v", err)
	}
	if stateWhileLeased != "leased" || nextSequence != 6 || terminalSequence == nil || *terminalSequence != 5 {
		t.Fatalf("leased terminal append = state %s next %d terminal %v", stateWhileLeased, nextSequence, terminalSequence)
	}
	if err := store.ApplyResult(ctx, prefix, collector.BatchResult{
		BatchID: prefix.Batch.BatchID,
		Chunks: []collector.ChunkResult{{
			TraceID: trace.TraceID, Status: collector.ChunkCommitted, CommittedThrough: 2,
		}},
	}); err != nil {
		t.Fatalf("ACK prefix: %v", err)
	}
	var prefixCursor, prefixRecords int
	var prefixState string
	if err := api.db.Pool().QueryRow(ctx, `
		select r.collector_cursor, r.delivery_state, count(o.sequence_no)
		from agent_trace_refs r left join agentobs_outbox_records o on o.trace_id = r.trace_id
		where r.trace_id = $1 group by r.trace_id
	`, trace.TraceID).Scan(&prefixCursor, &prefixState, &prefixRecords); err != nil {
		t.Fatalf("load prefix ACK: %v", err)
	}
	if prefixCursor != 2 || prefixState != "ready" || prefixRecords != 5 {
		t.Fatalf("prefix ACK = cursor %d state %s records %d", prefixCursor, prefixState, prefixRecords)
	}
	tail, ok, err := store.ClaimBatch(ctx)
	if err != nil || !ok {
		t.Fatalf("claim terminal tail = %#v ok=%t err=%v", tail, ok, err)
	}
	if len(tail.Batch.Chunks) != 1 || tail.Batch.Chunks[0].FirstSequence != 3 || len(tail.Batch.Chunks[0].Records) != 3 {
		t.Fatalf("terminal tail = %#v", tail.Batch)
	}
	if err := store.ApplyResult(ctx, tail, collector.BatchResult{
		BatchID: tail.Batch.BatchID,
		Chunks: []collector.ChunkResult{{
			TraceID: trace.TraceID, Status: collector.ChunkCommitted, CommittedThrough: 5,
		}},
	}); err != nil {
		t.Fatalf("ACK terminal tail: %v", err)
	}
	var finalCursor, finalRecords int
	var finalState, retainedRootSpanID string
	if err := api.db.Pool().QueryRow(ctx, `
		select r.collector_cursor, r.delivery_state, r.root_span_id, count(o.sequence_no)
		from agent_trace_refs r left join agentobs_outbox_records o on o.trace_id = r.trace_id
		where r.trace_id = $1 group by r.trace_id
	`, trace.TraceID).Scan(&finalCursor, &finalState, &retainedRootSpanID, &finalRecords); err != nil {
		t.Fatalf("load terminal ACK: %v", err)
	}
	if finalCursor != 5 || finalState != "acknowledged" || retainedRootSpanID != string(trace.RootSpanID) || finalRecords != 0 {
		t.Fatalf("terminal ACK = cursor %d state %s root %s records %d", finalCursor, finalState, retainedRootSpanID, finalRecords)
	}
}

func TestOutboxRecordCapacityFailsClosedWithoutDeletingBacklog(t *testing.T) {
	api, sessionCookie, csrfCookie, chatID := newChatFixture(t, "trace-outbox-capacity@example.com")
	runID := admitRunForLeaseTest(t, api, sessionCookie, csrfCookie, chatID, "0190cdd2-5f2d-7ad8-b3f5-1b588788c427")
	ctx := context.Background()
	if _, err := api.db.Pool().Exec(ctx, `
		update agentobs_outbox_capacity set max_records = current_records
		where singleton
	`); err != nil {
		t.Fatalf("set Outbox record capacity: %v", err)
	}
	response := api.postJSONWithCookieAndCSRF(t, "/api/v1/agent-runs/"+runID+"/cancel", map[string]any{}, sessionCookie, csrfCookie, csrfCookie.Value, "")
	if response.Code != http.StatusInternalServerError {
		t.Fatalf("capacity cancellation status = %d body=%s", response.Code, response.Body.String())
	}
	var currentRecords, maxRecords, storedRecords int
	if err := api.db.Pool().QueryRow(ctx, `
		select c.current_records, c.max_records, count(o.sequence_no)
		from agentobs_outbox_capacity c
		cross join agentobs_outbox_records o
		where c.singleton
		group by c.singleton
	`).Scan(&currentRecords, &maxRecords, &storedRecords); err != nil {
		t.Fatalf("load Outbox capacity after rejection: %v", err)
	}
	if currentRecords != 2 || maxRecords != 2 || storedRecords != 2 {
		t.Fatalf("Outbox capacity/backlog = current %d max %d rows %d", currentRecords, maxRecords, storedRecords)
	}
	var runStatus, jobStatus string
	if err := api.db.Pool().QueryRow(ctx, `
		select r.status, j.status from agent_runs r join agent_jobs j on j.run_id = r.id
		where r.id = $1
	`, runID).Scan(&runStatus, &jobStatus); err != nil {
		t.Fatalf("load rejected cancellation state: %v", err)
	}
	if runStatus != "queued" || jobStatus != "queued" {
		t.Fatalf("capacity rejection changed Run/Job = %s/%s", runStatus, jobStatus)
	}
}

func TestClaimAndReclaimRecordAttemptTreeAndContinuesLink(t *testing.T) {
	api, sessionCookie, csrfCookie, chatID := newChatFixture(t, "trace-attempts@example.com")
	runID := admitRunForLeaseTest(t, api, sessionCookie, csrfCookie, chatID, "0190cdd2-5f2d-7ad8-b3f5-1b588788c422")
	ctx := context.Background()
	queue := jobs.NewQueue(api.db.Pool())

	first, ok, err := queue.ClaimNext(ctx)
	if err != nil || !ok {
		t.Fatalf("first claim = %#v ok=%t err=%v", first, ok, err)
	}
	firstTrace, err := agent.LoadDurableTraceByRun(ctx, api.db.Pool(), runID)
	if err != nil {
		t.Fatal(err)
	}
	if len(firstTrace.Records) != 3 {
		t.Fatalf("first claim records = %#v", firstTrace.Records)
	}
	firstAttempt := firstTrace.Records[2]
	if firstAttempt.Kind != agentobs.RecordSpanStarted || firstAttempt.Name != agent.TraceSpanJobAttempt || firstAttempt.ParentSpanID != firstTrace.RootSpanID {
		t.Fatalf("first Attempt = %#v", firstAttempt)
	}

	if _, err := api.db.Pool().Exec(ctx, `update agent_jobs set lease_expires_at = now() - interval '1 second' where id = $1`, first.ID); err != nil {
		t.Fatal(err)
	}
	second, ok, err := queue.ClaimNext(ctx)
	if err != nil || !ok || second.AttemptNo != 2 {
		t.Fatalf("second claim = %#v ok=%t err=%v", second, ok, err)
	}
	trace, err := agent.LoadDurableTraceByRun(ctx, api.db.Pool(), runID)
	if err != nil {
		t.Fatal(err)
	}
	if len(trace.Records) != 6 {
		t.Fatalf("reclaim records = %#v", trace.Records)
	}
	leaseExpired, secondAttempt, continues := trace.Records[3], trace.Records[4], trace.Records[5]
	if leaseExpired.Kind != agentobs.RecordEvent || leaseExpired.SpanID != firstAttempt.SpanID || leaseExpired.Name != agent.TraceEventLeaseExpired {
		t.Fatalf("Lease expiry Event = %#v", leaseExpired)
	}
	if secondAttempt.Kind != agentobs.RecordSpanStarted || secondAttempt.ParentSpanID != trace.RootSpanID || secondAttempt.SpanID == firstAttempt.SpanID {
		t.Fatalf("second Attempt = %#v", secondAttempt)
	}
	if continues.Kind != agentobs.RecordLink || continues.Name != semconv.LinkContinues || continues.SpanID != secondAttempt.SpanID || continues.TargetTraceID != trace.TraceID || continues.TargetSpanID != firstAttempt.SpanID {
		t.Fatalf("continues Link = %#v", continues)
	}
	for _, record := range trace.Records {
		if record.Kind == agentobs.RecordSpanEnded && (record.SpanID == firstAttempt.SpanID || record.SpanID == secondAttempt.SpanID) {
			t.Fatalf("claim/reclaim fabricated Attempt completion: %#v", record)
		}
	}
}

func TestCancellationRecordsAuthoritativeEventsAndEndsRoot(t *testing.T) {
	api, sessionCookie, csrfCookie, chatID := newChatFixture(t, "trace-cancel@example.com")
	runID := admitRunForLeaseTest(t, api, sessionCookie, csrfCookie, chatID, "0190cdd2-5f2d-7ad8-b3f5-1b588788c423")
	response := api.postJSONWithCookieAndCSRF(t, "/api/v1/agent-runs/"+runID+"/cancel", map[string]any{}, sessionCookie, csrfCookie, csrfCookie.Value, "")
	if response.Code != http.StatusOK {
		t.Fatalf("cancel status = %d body=%s", response.Code, response.Body.String())
	}
	trace, err := agent.LoadDurableTraceByRun(context.Background(), api.db.Pool(), runID)
	if err != nil {
		t.Fatal(err)
	}
	if len(trace.Records) != 5 {
		t.Fatalf("cancel Trace records = %#v", trace.Records)
	}
	cancellation, terminal, rootEnd := trace.Records[2], trace.Records[3], trace.Records[4]
	if cancellation.Kind != agentobs.RecordEvent || cancellation.Name != agent.TraceEventCancellation || cancellation.SpanID != trace.RootSpanID {
		t.Fatalf("cancellation Event = %#v", cancellation)
	}
	if terminal.Kind != agentobs.RecordEvent || terminal.Name != agent.TraceEventRunTerminal {
		t.Fatalf("terminal Event = %#v", terminal)
	}
	if rootEnd.Kind != agentobs.RecordSpanEnded || rootEnd.Name != agent.TraceSpanAgentExecution || rootEnd.Status != agentobs.StatusCancelled || rootEnd.SpanID != trace.RootSpanID {
		t.Fatalf("cancelled root end = %#v", rootEnd)
	}
}

func TestDeadlineExpiryRecordsAuthoritativeEventsAndEndsRoot(t *testing.T) {
	api, sessionCookie, csrfCookie, chatID := newChatFixture(t, "trace-deadline@example.com")
	runID := admitRunForLeaseTest(t, api, sessionCookie, csrfCookie, chatID, "0190cdd2-5f2d-7ad8-b3f5-1b588788c424")
	ctx := context.Background()
	if _, err := api.db.Pool().Exec(ctx, `update agent_runs set deadline_at = now() - interval '1 second' where id = $1`, runID); err != nil {
		t.Fatal(err)
	}
	if claimed, ok, err := jobs.NewQueue(api.db.Pool()).ClaimNext(ctx); err != nil || ok {
		t.Fatalf("deadline claim = %#v ok=%t err=%v", claimed, ok, err)
	}
	trace, err := agent.LoadDurableTraceByRun(ctx, api.db.Pool(), runID)
	if err != nil {
		t.Fatal(err)
	}
	if len(trace.Records) != 5 {
		t.Fatalf("deadline Trace records = %#v", trace.Records)
	}
	deadline, terminal, rootEnd := trace.Records[2], trace.Records[3], trace.Records[4]
	if deadline.Kind != agentobs.RecordEvent || deadline.Name != agent.TraceEventDeadlineExpired {
		t.Fatalf("deadline Event = %#v", deadline)
	}
	if terminal.Kind != agentobs.RecordEvent || terminal.Name != agent.TraceEventRunTerminal {
		t.Fatalf("deadline terminal Event = %#v", terminal)
	}
	if rootEnd.Kind != agentobs.RecordSpanEnded || rootEnd.Status != agentobs.StatusError {
		t.Fatalf("deadline root end = %#v", rootEnd)
	}
}

func TestRecoveryExhaustionRecordsLastLeaseLossAndEndsRoot(t *testing.T) {
	api, sessionCookie, csrfCookie, chatID := newChatFixture(t, "trace-recovery-exhausted@example.com")
	runID := admitRunForLeaseTest(t, api, sessionCookie, csrfCookie, chatID, "0190cdd2-5f2d-7ad8-b3f5-1b588788c425")
	ctx := context.Background()
	queue := jobs.NewQueue(api.db.Pool())
	for attempt := 1; attempt <= 3; attempt++ {
		claimed, ok, err := queue.ClaimNext(ctx)
		if err != nil || !ok || claimed.AttemptNo != attempt {
			t.Fatalf("attempt %d claim = %#v ok=%t err=%v", attempt, claimed, ok, err)
		}
		if _, err := api.db.Pool().Exec(ctx, `update agent_jobs set lease_expires_at = now() - interval '1 second' where id = $1`, claimed.ID); err != nil {
			t.Fatal(err)
		}
	}
	if claimed, ok, err := queue.ClaimNext(ctx); err != nil || ok {
		t.Fatalf("exhaustion claim = %#v ok=%t err=%v", claimed, ok, err)
	}
	trace, err := agent.LoadDurableTraceByRun(ctx, api.db.Pool(), runID)
	if err != nil {
		t.Fatal(err)
	}
	if len(trace.Records) < 4 {
		t.Fatalf("recovery Trace = %#v", trace.Records)
	}
	tail := trace.Records[len(trace.Records)-4:]
	if tail[0].Kind != agentobs.RecordEvent || tail[0].Name != agent.TraceEventLeaseExpired {
		t.Fatalf("third Lease expiry = %#v", tail[0])
	}
	if tail[1].Kind != agentobs.RecordEvent || tail[1].Name != agent.TraceEventRecoveryExhausted {
		t.Fatalf("recovery exhaustion Event = %#v", tail[1])
	}
	if tail[2].Kind != agentobs.RecordEvent || tail[2].Name != agent.TraceEventRunTerminal {
		t.Fatalf("recovery terminal Event = %#v", tail[2])
	}
	if tail[3].Kind != agentobs.RecordSpanEnded || tail[3].SpanID != trace.RootSpanID || tail[3].Status != agentobs.StatusError {
		t.Fatalf("recovery root end = %#v", tail[3])
	}
	for _, record := range trace.Records {
		if record.Kind == agentobs.RecordSpanEnded && record.SpanID != trace.RootSpanID {
			t.Fatalf("recovery fabricated Attempt terminal: %#v", record)
		}
	}
}

func TestRetryCreatesSeparateTraceLinkedToPriorRootAndReplays(t *testing.T) {
	api, sessionCookie, csrfCookie, chatID := newChatFixture(t, "trace-retry@example.com")
	const messageID = "0190cdd2-5f2d-7ad8-b3f5-1b588788c426"
	sourceRunID := admitRunForLeaseTest(t, api, sessionCookie, csrfCookie, chatID, messageID)
	cancelled := api.postJSONWithCookieAndCSRF(t, "/api/v1/agent-runs/"+sourceRunID+"/cancel", map[string]any{}, sessionCookie, csrfCookie, csrfCookie.Value, "")
	if cancelled.Code != http.StatusOK {
		t.Fatalf("cancel status = %d body=%s", cancelled.Code, cancelled.Body.String())
	}
	path := "/api/v1/agent-runs/" + sourceRunID + "/retry"
	first := api.postJSONWithCookieAndCSRF(t, path, map[string]any{}, sessionCookie, csrfCookie, csrfCookie.Value, "trace-retry-key")
	if first.Code != http.StatusAccepted {
		t.Fatalf("retry status = %d body=%s", first.Code, first.Body.String())
	}
	var firstBody struct {
		Run agent.RunSnapshot `json:"run"`
	}
	decodeBody(t, first, &firstBody)
	ctx := context.Background()
	source, err := agent.LoadDurableTraceByRun(ctx, api.db.Pool(), sourceRunID)
	if err != nil {
		t.Fatal(err)
	}
	retry, err := agent.LoadDurableTraceByRun(ctx, api.db.Pool(), firstBody.Run.ID)
	if err != nil {
		t.Fatalf("load Retry Trace: %v", err)
	}
	if retry.TraceID == source.TraceID || len(retry.Records) != 4 {
		t.Fatalf("Retry Trace = %#v source=%#v", retry, source)
	}
	link, admitted := retry.Records[2], retry.Records[3]
	if link.Kind != agentobs.RecordLink || link.Name != semconv.LinkRetriedFrom || link.SpanID != retry.RootSpanID || link.TargetTraceID != source.TraceID || link.TargetSpanID != source.RootSpanID {
		t.Fatalf("retried_from Link = %#v", link)
	}
	if admitted.Kind != agentobs.RecordEvent || admitted.Name != agent.TraceEventRetryAdmitted {
		t.Fatalf("Retry admitted Event = %#v", admitted)
	}

	replay := api.postJSONWithCookieAndCSRF(t, path, map[string]any{}, sessionCookie, csrfCookie, csrfCookie.Value, "trace-retry-key")
	if replay.Code != http.StatusAccepted {
		t.Fatalf("retry replay status = %d body=%s", replay.Code, replay.Body.String())
	}
	var replayBody struct {
		Run agent.RunSnapshot `json:"run"`
	}
	decodeBody(t, replay, &replayBody)
	if replayBody.Run.ID != firstBody.Run.ID {
		t.Fatalf("Retry replay Run = %q, want %q", replayBody.Run.ID, firstBody.Run.ID)
	}
	replayed, err := agent.LoadDurableTraceByRun(ctx, api.db.Pool(), firstBody.Run.ID)
	if err != nil || len(replayed.Records) != 4 {
		t.Fatalf("Retry replay Trace records = %#v err=%v", replayed.Records, err)
	}
}

func TestAdmissionRollsBackRunJobAndMessageWhenRequiredTraceWriteFails(t *testing.T) {
	api, sessionCookie, csrfCookie, chatID := newChatFixture(t, "trace-admission-failure@example.com")
	ctx := context.Background()
	if _, err := api.db.Pool().Exec(ctx, `drop table agentobs_replay_staging, agentobs_outbox_records`); err != nil {
		t.Fatal(err)
	}
	const messageID = "0190cdd2-5f2d-7ad8-b3f5-1b588788c421"
	response := api.postJSONWithCookieAndCSRF(t, "/api/v1/chats/"+chatID+"/messages", map[string]any{
		"id": messageID, "content": "Required Trace failure must roll back.",
	}, sessionCookie, csrfCookie, csrfCookie.Value, "")
	if response.Code != http.StatusInternalServerError {
		t.Fatalf("failed Trace admission status = %d body=%s", response.Code, response.Body.String())
	}

	var messages, runs, jobs, traces int
	if err := api.db.Pool().QueryRow(ctx, `select count(*) from chat_messages where id = $1`, messageID).Scan(&messages); err != nil {
		t.Fatal(err)
	}
	if err := api.db.Pool().QueryRow(ctx, `select count(*) from agent_runs where input_message_id = $1`, messageID).Scan(&runs); err != nil {
		t.Fatal(err)
	}
	if err := api.db.Pool().QueryRow(ctx, `select count(*) from agent_jobs j join agent_runs r on r.id = j.run_id where r.input_message_id = $1`, messageID).Scan(&jobs); err != nil {
		t.Fatal(err)
	}
	if err := api.db.Pool().QueryRow(ctx, `select count(*) from agent_trace_refs`).Scan(&traces); err != nil {
		t.Fatal(err)
	}
	if messages != 0 || runs != 0 || jobs != 0 || traces != 0 {
		t.Fatalf("failed admission retained message/run/job/Trace = %d/%d/%d/%d", messages, runs, jobs, traces)
	}
}

func TestRuntimeTraceCutoverDoesNotDependOnSprint4TraceTables(t *testing.T) {
	api, sessionCookie, csrfCookie, chatID := newChatFixture(t, "trace-outbox-cutover@example.com")
	ctx := context.Background()
	if _, err := api.db.Pool().Exec(ctx, `drop table agent_trace_records, agent_traces cascade`); err != nil {
		t.Fatalf("remove Sprint 4 Trace tables: %v", err)
	}
	const messageID = "0190cdd2-5f2d-7ad8-b3f5-1b588788c428"
	response := api.postJSONWithCookieAndCSRF(t, "/api/v1/chats/"+chatID+"/messages", map[string]any{
		"id": messageID, "content": "Outbox is the only runtime Trace authority.",
	}, sessionCookie, csrfCookie, csrfCookie.Value, "")
	if response.Code != http.StatusAccepted {
		t.Fatalf("Outbox-only admission status = %d body=%s", response.Code, response.Body.String())
	}
	var admitted struct {
		RunID string `json:"run_id"`
	}
	decodeBody(t, response, &admitted)
	trace, err := agent.LoadDurableTraceByRun(ctx, api.db.Pool(), admitted.RunID)
	if err != nil || len(trace.Records) != 2 {
		t.Fatalf("Outbox-only Trace = %#v err=%v", trace, err)
	}
	claimed, ok, err := jobs.NewQueue(api.db.Pool()).ClaimNext(ctx)
	if err != nil || !ok || claimed.RunID != admitted.RunID {
		t.Fatalf("Outbox-only claim = %#v ok=%t err=%v", claimed, ok, err)
	}
	trace, err = agent.LoadDurableTraceByRun(ctx, api.db.Pool(), admitted.RunID)
	if err != nil || len(trace.Records) != 3 || trace.Records[2].Name != agent.TraceSpanJobAttempt {
		t.Fatalf("Outbox-only Attempt Trace = %#v err=%v", trace, err)
	}
}

func TestControllerRecordsModelCheckpointPublicationAndTerminalTree(t *testing.T) {
	api, sessionCookie, csrfCookie, chatID := newChatFixture(t, "trace-controller@example.com")
	runID := admitRunForLeaseTest(t, api, sessionCookie, csrfCookie, chatID, "0190cdd2-5f2d-7ad8-b3f5-1b588788c427")
	ctx := context.Background()
	claimed, ok, err := jobs.NewQueue(api.db.Pool()).ClaimNext(ctx)
	if err != nil || !ok {
		t.Fatalf("claim = %#v ok=%t err=%v", claimed, ok, err)
	}
	registry, err := agent.NewActionRegistry()
	if err != nil {
		t.Fatal(err)
	}
	model := &recordingModelClient{result: models.ModelDecision{Final: &models.FinalDraft{Text: "Durable traced answer."}}}
	telemetry := &failingTraceExporter{}
	runtime := agent.NewPostgresRuntime(api.db.Pool(), agent.BareSystemPrompt, func() string { return "msg_trace_controller" }, agent.WithBestEffortTraceExporter(telemetry))
	if err := agent.NewController(runtime, model, registry).Execute(ctx, attemptFromClaim(claimed)); err != nil {
		t.Fatalf("Controller Execute: %v", err)
	}
	trace, err := agent.LoadDurableTraceByRun(ctx, api.db.Pool(), runID)
	if err != nil {
		t.Fatal(err)
	}
	wantKindsAndNames := []struct {
		kind agentobs.RecordKind
		name string
	}{
		{agentobs.RecordSpanStarted, agent.TraceSpanAgentExecution},
		{agentobs.RecordEvent, agent.TraceEventRunAdmitted},
		{agentobs.RecordSpanStarted, agent.TraceSpanJobAttempt},
		{agentobs.RecordSpanStarted, semconv.ModelCall},
		{agentobs.RecordSpanEnded, semconv.ModelCall},
		{agentobs.RecordEvent, agent.TraceEventCheckpointAccepted},
		{agentobs.RecordSpanStarted, agent.TraceSpanPublication},
		{agentobs.RecordEvent, agent.TraceEventPublicationPassed},
		{agentobs.RecordSpanEnded, agent.TraceSpanPublication},
		{agentobs.RecordSpanEnded, agent.TraceSpanJobAttempt},
		{agentobs.RecordEvent, agent.TraceEventRunTerminal},
		{agentobs.RecordSpanEnded, agent.TraceSpanAgentExecution},
	}
	if len(trace.Records) != len(wantKindsAndNames) {
		t.Fatalf("complete Trace records = %#v", trace.Records)
	}
	for index, want := range wantKindsAndNames {
		got := trace.Records[index]
		if got.Kind != want.kind || got.Name != want.name {
			t.Fatalf("record %d = %s/%s, want %s/%s", index, got.Kind, got.Name, want.kind, want.name)
		}
	}
	if telemetry.calls < 2 {
		t.Fatalf("best-effort exporter calls = %d, want Model start and terminal", telemetry.calls)
	}
}

func TestReclaimLinksRepeatedPhysicalActionAndKeepsFirstIncomplete(t *testing.T) {
	api, sessionCookie, csrfCookie, chatID := newChatFixture(t, "trace-repeated-action@example.com")
	runID := admitRunForLeaseTest(t, api, sessionCookie, csrfCookie, chatID, "0190cdd2-5f2d-7ad8-b3f5-1b588788c428")
	ctx := context.Background()
	queue := jobs.NewQueue(api.db.Pool())
	first, ok, err := queue.ClaimNext(ctx)
	if err != nil || !ok {
		t.Fatalf("first claim = %#v ok=%t err=%v", first, ok, err)
	}
	runtime := agent.NewPostgresRuntime(api.db.Pool(), agent.BareSystemPrompt, func() string { return "msg_trace_repeated_action" })
	proposal, err := agent.NewProposalCheckpoint(1, models.ActionProposalBatch{Actions: []models.ActionProposal{{
		Name: "recovery_record", Input: json.RawMessage(`{"value":"repeat-me"}`),
	}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.AppendCheckpoint(ctx, attemptFromClaim(first), proposal); err != nil {
		t.Fatal(err)
	}
	firstContext, firstTracer, err := runtime.StartAttemptTrace(ctx, attemptFromClaim(first))
	if err != nil {
		t.Fatal(err)
	}
	_, firstActionSpan, err := firstTracer.StartSpan(firstContext, agentobs.SpanStart{
		IdentityKey: agent.TraceActionStartIdentity(runID, 1, "decision:1/action:0"),
		Name:        semconv.AgentAction,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := api.db.Pool().Exec(ctx, `update agent_jobs set lease_expires_at = now() - interval '1 second' where id = $1`, first.ID); err != nil {
		t.Fatal(err)
	}
	second, ok, err := queue.ClaimNext(ctx)
	if err != nil || !ok || second.AttemptNo != 2 {
		t.Fatalf("second claim = %#v ok=%t err=%v", second, ok, err)
	}
	action := &recoveryRecordingAction{}
	registry, err := agent.NewActionRegistry(action)
	if err != nil {
		t.Fatal(err)
	}
	model := &recordingModelClient{result: models.ModelDecision{Final: &models.FinalDraft{Text: "Repeated work completed."}}}
	if err := agent.NewController(runtime, model, registry).Execute(ctx, attemptFromClaim(second)); err != nil {
		t.Fatal(err)
	}
	trace, err := agent.LoadDurableTraceByRun(ctx, api.db.Pool(), runID)
	if err != nil {
		t.Fatal(err)
	}
	var actionStarts []agentobs.Record
	var retries *agentobs.Record
	ended := make(map[agentobs.SpanID]bool)
	for index := range trace.Records {
		record := trace.Records[index]
		if record.Kind == agentobs.RecordSpanStarted && record.Name == semconv.AgentAction {
			actionStarts = append(actionStarts, record)
		}
		if record.Kind == agentobs.RecordSpanEnded {
			ended[record.SpanID] = true
		}
		if record.Kind == agentobs.RecordLink && record.Name == semconv.LinkRetries {
			retries = &trace.Records[index]
		}
	}
	if len(actionStarts) != 2 || actionStarts[0].SpanID != firstActionSpan.SpanID || ended[firstActionSpan.SpanID] {
		t.Fatalf("physical Action executions = %#v ended=%v", actionStarts, ended)
	}
	if retries == nil || retries.SpanID != actionStarts[1].SpanID || retries.TargetTraceID != trace.TraceID || retries.TargetSpanID != firstActionSpan.SpanID {
		t.Fatalf("retries Link = %#v", retries)
	}
	var resultCheckpoints int
	if err := api.db.Pool().QueryRow(ctx, `select count(*) from agent_run_checkpoints where run_id = $1 and kind = 'action_result'`, runID).Scan(&resultCheckpoints); err != nil {
		t.Fatal(err)
	}
	if resultCheckpoints != 1 || len(action.calls) != 1 {
		t.Fatalf("accepted Result/physical second call = %d/%d", resultCheckpoints, len(action.calls))
	}
}

func TestRequiredModelStartFailureDoesNotCallGateway(t *testing.T) {
	api, sessionCookie, csrfCookie, chatID := newChatFixture(t, "trace-model-start-failure@example.com")
	_ = admitRunForLeaseTest(t, api, sessionCookie, csrfCookie, chatID, "0190cdd2-5f2d-7ad8-b3f5-1b588788c429")
	ctx := context.Background()
	claimed, ok, err := jobs.NewQueue(api.db.Pool()).ClaimNext(ctx)
	if err != nil || !ok {
		t.Fatalf("claim = %#v ok=%t err=%v", claimed, ok, err)
	}
	if _, err := api.db.Pool().Exec(ctx, `drop table agentobs_replay_staging, agentobs_outbox_records`); err != nil {
		t.Fatal(err)
	}
	model := &recordingModelClient{result: models.ModelDecision{Final: &models.FinalDraft{Text: "must not be called"}}}
	registry, err := agent.NewActionRegistry()
	if err != nil {
		t.Fatal(err)
	}
	err = agent.NewController(agent.NewPostgresRuntime(api.db.Pool(), agent.BareSystemPrompt, nil), model, registry).Execute(ctx, attemptFromClaim(claimed))
	if err == nil || model.calls != 0 {
		t.Fatalf("Controller error/model calls = %v/%d, want required start failure before gateway", err, model.calls)
	}
}

func TestStaleAttemptCannotAppendTraceRecordsAfterReclaim(t *testing.T) {
	api, sessionCookie, csrfCookie, chatID := newChatFixture(t, "trace-stale-attempt@example.com")
	runID := admitRunForLeaseTest(t, api, sessionCookie, csrfCookie, chatID, "0190cdd2-5f2d-7ad8-b3f5-1b588788c430")
	ctx := context.Background()
	queue := jobs.NewQueue(api.db.Pool())
	first, ok, err := queue.ClaimNext(ctx)
	if err != nil || !ok {
		t.Fatalf("first claim = %#v ok=%t err=%v", first, ok, err)
	}
	runtime := agent.NewPostgresRuntime(api.db.Pool(), agent.BareSystemPrompt, nil)
	firstContext, firstTracer, err := runtime.StartAttemptTrace(ctx, attemptFromClaim(first))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := api.db.Pool().Exec(ctx, `update agent_jobs set lease_expires_at = now() - interval '1 second' where id = $1`, first.ID); err != nil {
		t.Fatal(err)
	}
	second, ok, err := queue.ClaimNext(ctx)
	if err != nil || !ok || second.AttemptNo != 2 {
		t.Fatalf("second claim = %#v ok=%t err=%v", second, ok, err)
	}
	if err := firstTracer.Event(firstContext, agentobs.Event{
		IdentityKey: "run/" + runID + "/stale-worker-event", Name: "nano.test.stale",
	}); !errors.Is(err, agent.ErrLeaseLost) {
		t.Fatalf("stale append error = %v, want ErrLeaseLost", err)
	}
	trace, err := agent.LoadDurableTraceByRun(ctx, api.db.Pool(), runID)
	if err != nil {
		t.Fatal(err)
	}
	for _, record := range trace.Records {
		if record.IdentityKey == "run/"+runID+"/stale-worker-event" {
			t.Fatalf("stale Event was appended: %#v", record)
		}
	}
}

func TestModelCallRemainsIncompleteWhenResponseIsNeverObserved(t *testing.T) {
	api, sessionCookie, csrfCookie, chatID := newChatFixture(t, "trace-model-incomplete@example.com")
	runID := admitRunForLeaseTest(t, api, sessionCookie, csrfCookie, chatID, "0190cdd2-5f2d-7ad8-b3f5-1b588788c431")
	ctx, cancel := context.WithCancel(context.Background())
	claimed, ok, err := jobs.NewQueue(api.db.Pool()).ClaimNext(ctx)
	if err != nil || !ok {
		t.Fatalf("claim = %#v ok=%t err=%v", claimed, ok, err)
	}
	model := &blockingTraceModel{started: make(chan struct{})}
	registry, err := agent.NewActionRegistry()
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		done <- agent.NewController(agent.NewPostgresRuntime(api.db.Pool(), agent.BareSystemPrompt, nil), model, registry).Execute(ctx, attemptFromClaim(claimed))
	}()
	select {
	case <-model.started:
	case <-time.After(5 * time.Second):
		t.Fatal("Model was not called")
	}
	trace, err := agent.LoadDurableTraceByRun(context.Background(), api.db.Pool(), runID)
	if err != nil {
		t.Fatal(err)
	}
	var modelStarts, modelEnds int
	for _, record := range trace.Records {
		if record.Name == semconv.ModelCall && record.Kind == agentobs.RecordSpanStarted {
			modelStarts++
		}
		if record.Name == semconv.ModelCall && record.Kind == agentobs.RecordSpanEnded {
			modelEnds++
		}
	}
	if modelStarts != 1 || modelEnds != 0 {
		t.Fatalf("in-flight Model records start/end = %d/%d, Trace=%#v", modelStarts, modelEnds, trace.Records)
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancelled Controller error = %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("cancelled Controller did not return")
	}
	trace, err = agent.LoadDurableTraceByRun(context.Background(), api.db.Pool(), runID)
	if err != nil {
		t.Fatal(err)
	}
	for _, record := range trace.Records {
		if record.Name == semconv.ModelCall && record.Kind == agentobs.RecordSpanEnded {
			t.Fatalf("cancelled process fabricated a Model terminal: %#v", record)
		}
	}
}

func TestCompletedModelCallRemainsUnacceptedWhenCheckpointCommitFails(t *testing.T) {
	api, sessionCookie, csrfCookie, chatID := newChatFixture(t, "trace-model-unaccepted@example.com")
	runID := admitRunForLeaseTest(t, api, sessionCookie, csrfCookie, chatID, "0190cdd2-5f2d-7ad8-b3f5-1b588788c432")
	ctx := context.Background()
	claimed, ok, err := jobs.NewQueue(api.db.Pool()).ClaimNext(ctx)
	if err != nil || !ok {
		t.Fatalf("claim = %#v ok=%t err=%v", claimed, ok, err)
	}
	commitCalls := 0
	runtime := agent.NewPostgresRuntime(api.db.Pool(), agent.BareSystemPrompt, nil, agent.WithCommitFunc(func(ctx context.Context, tx pgx.Tx) error {
		commitCalls++
		if commitCalls >= 3 {
			return errors.New("checkpoint storage unavailable")
		}
		return tx.Commit(ctx)
	}))
	model := &recordingModelClient{result: models.ModelDecision{Final: &models.FinalDraft{Text: "observed but unaccepted"}}}
	registry, err := agent.NewActionRegistry()
	if err != nil {
		t.Fatal(err)
	}
	if err := agent.NewController(runtime, model, registry).Execute(ctx, attemptFromClaim(claimed)); err == nil {
		t.Fatal("Checkpoint failure returned nil")
	}
	trace, err := agent.LoadDurableTraceByRun(ctx, api.db.Pool(), runID)
	if err != nil {
		t.Fatal(err)
	}
	var modelEnds, accepted int
	for _, record := range trace.Records {
		if record.Name == semconv.ModelCall && record.Kind == agentobs.RecordSpanEnded {
			modelEnds++
		}
		if record.Name == agent.TraceEventCheckpointAccepted {
			accepted++
		}
	}
	var checkpoints int
	if err := api.db.Pool().QueryRow(ctx, `select count(*) from agent_run_checkpoints where run_id = $1`, runID).Scan(&checkpoints); err != nil {
		t.Fatal(err)
	}
	if model.calls != 1 || modelEnds != 1 || checkpoints != 0 || accepted != 0 {
		t.Fatalf("Model calls/ends/checkpoints/Events = %d/%d/%d/%d", model.calls, modelEnds, checkpoints, accepted)
	}
}

func TestActionLossBoundariesPreservePhysicalWorkWithoutAcceptance(t *testing.T) {
	tests := []struct {
		name      string
		messageID string
		failFrom  int
		wantEnded bool
	}{
		{name: "after execution before terminal", messageID: "0190cdd2-5f2d-7ad8-b3f5-1b588788c433", failFrom: 2, wantEnded: false},
		{name: "after terminal before Result Checkpoint", messageID: "0190cdd2-5f2d-7ad8-b3f5-1b588788c434", failFrom: 3, wantEnded: true},
	}
	for index, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			api, sessionCookie, csrfCookie, chatID := newChatFixture(t, "trace-action-loss-"+string(rune('a'+index))+"@example.com")
			runID := admitRunForLeaseTest(t, api, sessionCookie, csrfCookie, chatID, tt.messageID)
			ctx := context.Background()
			claimed, ok, err := jobs.NewQueue(api.db.Pool()).ClaimNext(ctx)
			if err != nil || !ok {
				t.Fatalf("claim = %#v ok=%t err=%v", claimed, ok, err)
			}
			attempt := attemptFromClaim(claimed)
			normalRuntime := agent.NewPostgresRuntime(api.db.Pool(), agent.BareSystemPrompt, nil)
			proposal, err := agent.NewProposalCheckpoint(1, models.ActionProposalBatch{Actions: []models.ActionProposal{{
				Name: "recovery_record", Input: json.RawMessage(`{"value":"physical-work"}`),
			}}})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := normalRuntime.AppendCheckpoint(ctx, attempt, proposal); err != nil {
				t.Fatal(err)
			}
			commitCalls := 0
			faultyRuntime := agent.NewPostgresRuntime(api.db.Pool(), agent.BareSystemPrompt, nil, agent.WithCommitFunc(func(ctx context.Context, tx pgx.Tx) error {
				commitCalls++
				if commitCalls >= tt.failFrom {
					return errors.New("simulated durable boundary loss")
				}
				return tx.Commit(ctx)
			}))
			action := &recoveryRecordingAction{}
			registry, err := agent.NewActionRegistry(action)
			if err != nil {
				t.Fatal(err)
			}
			model := &recordingModelClient{result: models.ModelDecision{Final: &models.FinalDraft{Text: "must not be reached"}}}
			if err := agent.NewController(faultyRuntime, model, registry).Execute(ctx, attempt); err == nil {
				t.Fatal("faulty Action boundary returned nil")
			}
			trace, err := agent.LoadDurableTraceByRun(ctx, api.db.Pool(), runID)
			if err != nil {
				t.Fatal(err)
			}
			var actionStarts, actionEnds, resultEvents int
			for _, record := range trace.Records {
				if record.Name == semconv.AgentAction && record.Kind == agentobs.RecordSpanStarted {
					actionStarts++
				}
				if record.Name == semconv.AgentAction && record.Kind == agentobs.RecordSpanEnded {
					actionEnds++
				}
				if record.Name == agent.TraceEventCheckpointAccepted && stringAttributeForTest(record, agent.TraceKeyCheckpointKind) == string(agent.CheckpointActionResult) {
					resultEvents++
				}
			}
			var results int
			if err := api.db.Pool().QueryRow(ctx, `select count(*) from agent_run_checkpoints where run_id = $1 and kind = 'action_result'`, runID).Scan(&results); err != nil {
				t.Fatal(err)
			}
			wantEnds := 0
			if tt.wantEnded {
				wantEnds = 1
			}
			if len(action.calls) != 1 || actionStarts != 1 || actionEnds != wantEnds || results != 0 || resultEvents != 0 || model.calls != 0 {
				t.Fatalf("calls/starts/ends/results/Events/model = %d/%d/%d/%d/%d/%d", len(action.calls), actionStarts, actionEnds, results, resultEvents, model.calls)
			}
		})
	}
}

type failingTraceExporter struct{ calls int }

func (e *failingTraceExporter) Export(context.Context, agentobs.Record) error {
	e.calls++
	return errors.New("telemetry unavailable")
}
func (*failingTraceExporter) ForceFlush(context.Context) error { return nil }
func (*failingTraceExporter) Shutdown(context.Context) error   { return nil }

type blockingTraceModel struct{ started chan struct{} }

func (m *blockingTraceModel) Decide(ctx context.Context, request models.ModelRequest) (models.ModelOutcome, error) {
	close(m.started)
	<-ctx.Done()
	return models.ModelOutcome{Metadata: models.ModelCallMetadata{RequestedModel: request.Model, ResultKind: models.ModelResultUnavailable}}, ctx.Err()
}

func stringAttributeForTest(record agentobs.Record, key string) string {
	for _, attribute := range record.Attributes {
		if attribute.Key == key && attribute.Value.Kind == agentobs.ValueString {
			return attribute.Value.String
		}
	}
	return ""
}
