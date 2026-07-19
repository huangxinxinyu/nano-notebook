package app_test

import (
	"context"
	"testing"
	"time"

	"github.com/huangxinxinyu/nano-notebook/internal/agentobs"
	"github.com/huangxinxinyu/nano-notebook/internal/agentoutbox"
	"github.com/huangxinxinyu/nano-notebook/internal/collector"
	"github.com/huangxinxinyu/nano-notebook/internal/objectstore"
	"github.com/huangxinxinyu/nano-notebook/internal/replay"
)

func TestPurgeCommandSurvivesRunDeletionWithoutFullTraceOutbox(t *testing.T) {
	api, sessionCookie, csrfCookie, chatID := newChatFixture(t, "purge-only@example.com")
	runID := admitRunForLeaseTest(t, api, sessionCookie, csrfCookie, chatID, "0190cdd2-5f2d-7ad8-b3f5-1b588788d333")
	ctx := context.Background()
	var traceID agentobs.TraceID
	if err := api.db.Pool().QueryRow(ctx, `select trace_id from agent_trace_refs where run_id = $1`, runID).Scan(&traceID); err != nil {
		t.Fatal(err)
	}
	objects := objectstore.NewMemoryStore()
	objectKey := replay.StagingTracePrefix("agent-replay-staging", traceID) + "/payload"
	if err := objects.Put(ctx, objectKey, []byte("ciphertext")); err != nil {
		t.Fatal(err)
	}
	if _, err := api.db.Pool().Exec(ctx, `delete from agent_runs where id = $1`, runID); err != nil {
		t.Fatal(err)
	}
	store, err := agentoutbox.NewPurgeStore(api.db.Pool(), agentoutbox.PurgeStoreConfig{
		ProducerID: "nano-worker", MaxCommands: 4, LeaseDuration: time.Minute,
		BaseBackoff: time.Millisecond, MaxBackoff: time.Second, RetryJitter: func() float64 { return 0 },
		StagingObjects: objects,
	})
	if err != nil {
		t.Fatal(err)
	}
	claimed, ok, err := store.ClaimPurgeBatch(ctx)
	if err != nil || !ok || len(claimed.Batch.Commands) != 1 || claimed.Batch.Commands[0].TraceID != traceID {
		t.Fatalf("ClaimPurgeBatch = %#v ok=%t err=%v", claimed, ok, err)
	}
	if err := store.ApplyPurgeResult(ctx, claimed, collector.PurgeBatchResult{
		BatchID:  claimed.Batch.BatchID,
		Commands: []collector.PurgeCommandResult{{TraceID: traceID, Status: collector.PurgeAcknowledged}},
	}); err != nil {
		t.Fatal(err)
	}
	var state string
	if err := api.db.Pool().QueryRow(ctx, `select delivery_state from agentobs_outbox_commands where trace_id = $1`, traceID).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if state != "acknowledged" || objects.Len() != 0 {
		t.Fatalf("purge state=%q producer_objects=%d", state, objects.Len())
	}
}
