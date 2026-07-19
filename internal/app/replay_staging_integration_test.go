package app_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/huangxinxinyu/nano-notebook/internal/agent"
	"github.com/huangxinxinyu/nano-notebook/internal/agentobs"
	"github.com/huangxinxinyu/nano-notebook/internal/agentoutbox"
	"github.com/huangxinxinyu/nano-notebook/internal/collector"
	"github.com/huangxinxinyu/nano-notebook/internal/jobs"
	"github.com/huangxinxinyu/nano-notebook/internal/models"
	"github.com/huangxinxinyu/nano-notebook/internal/objectstore"
	"github.com/huangxinxinyu/nano-notebook/internal/replay"
)

func TestControllerStagesAndBindsNormalizedReplayAcrossModelAndActionBoundaries(t *testing.T) {
	api, sessionCookie, csrfCookie, chatID := newChatFixture(t, "replay-runtime@example.com")
	runID := admitRunForLeaseTest(t, api, sessionCookie, csrfCookie, chatID, "0190cdd2-5f2d-7ad8-b3f5-1b588788c440")
	ctx := context.Background()
	claimed, ok, err := jobs.NewQueue(api.db.Pool()).ClaimNext(ctx)
	if err != nil || !ok {
		t.Fatalf("ClaimNext = %#v, %t, %v", claimed, ok, err)
	}
	keys, _ := replay.NewDevelopmentKeyProvider("dev-key-v1", bytes.Repeat([]byte{0x41}, 32))
	sealer, _ := replay.NewSealer(keys)
	objects := objectstore.NewMemoryStore()
	stager, err := replay.NewPostgresStager(api.db.Pool(), sealer, objects, replay.StagerConfig{ObjectPrefix: "producer-staging"})
	if err != nil {
		t.Fatal(err)
	}
	registry, err := agent.NewActionRegistry(agent.NewCalculateAction())
	if err != nil {
		t.Fatal(err)
	}
	runtime := agent.NewPostgresRuntime(api.db.Pool(), agent.BareSystemPrompt, func() string { return "msg_replay_runtime" }, agent.WithReplayStager(stager))
	if err := agent.NewController(runtime, &replayFlowModel{}, registry).Execute(ctx, attemptFromClaim(claimed)); err != nil {
		t.Fatalf("Controller Execute: %v", err)
	}

	var traceID agentobs.TraceID
	var attachmentCount, attachedCount int
	var classes []string
	if err := api.db.Pool().QueryRow(ctx, `select trace_id from agent_trace_refs where run_id = $1`, runID).Scan(&traceID); err != nil {
		t.Fatal(err)
	}
	if err := api.db.Pool().QueryRow(ctx, `
		select count(*), count(*) filter (where state = 'attached'), array_agg(class order by class, identity_key)
		from agentobs_replay_staging where trace_id = $1
	`, traceID).Scan(&attachmentCount, &attachedCount, &classes); err != nil {
		t.Fatal(err)
	}
	wantClasses := []string{"action_input", "action_result", "model_decision", "model_decision", "model_request", "model_request"}
	if attachmentCount != 6 || attachedCount != 6 || objects.Len() != 6 || len(classes) != len(wantClasses) {
		t.Fatalf("Replay attachments=%d attached=%d objects=%d classes=%v", attachmentCount, attachedCount, objects.Len(), classes)
	}
	for index := range wantClasses {
		if classes[index] != wantClasses[index] {
			t.Fatalf("Replay classes = %v, want %v", classes, wantClasses)
		}
	}
	outbox, err := agentoutbox.NewPostgresStore(api.db.Pool(), agentoutbox.Config{
		ProducerID: "nano-worker", MaxRecords: 128, MaxEncodedBytes: 512 * 1024,
		MaxTraces: 16, LeaseDuration: 30 * time.Second, StagingObjects: objects,
	})
	if err != nil {
		t.Fatal(err)
	}
	batch, ok, err := outbox.ClaimBatch(ctx)
	if err != nil || !ok || len(batch.Batch.Chunks) != 1 || len(batch.Batch.Chunks[0].Attachments) != 6 {
		t.Fatalf("Replay Outbox claim = %#v ok=%t err=%v", batch, ok, err)
	}
}

type replayFlowModel struct {
	calls int
}

func (m *replayFlowModel) Decide(_ context.Context, request models.ModelRequest) (models.ModelOutcome, error) {
	m.calls++
	decision := models.ModelDecision{Final: &models.FinalDraft{Text: "Replay captured."}}
	kind := models.ModelResultFinalDraft
	if m.calls == 1 {
		decision = models.ModelDecision{Proposal: &models.ActionProposalBatch{Actions: []models.ActionProposal{{
			Name: "calculate", Input: json.RawMessage(`{"operation":"add","operands":["2","3"]}`),
		}}}}
		kind = models.ModelResultActionProposal
	}
	return models.ModelOutcome{ModelDecision: decision, Metadata: models.ModelCallMetadata{
		RequestedModel: request.Model, ResultKind: kind,
	}}, nil
}

func TestReplayStagerPersistsOnlyEncryptedObjectMetadataAndReconcilesIdentity(t *testing.T) {
	api, sessionCookie, csrfCookie, chatID := newChatFixture(t, "replay-staging@example.com")
	runID := admitRunForLeaseTest(t, api, sessionCookie, csrfCookie, chatID, "0190cdd2-5f2d-7ad8-b3f5-1b588788c441")
	trace, err := agent.LoadDurableTraceByRun(context.Background(), api.db.Pool(), runID)
	if err != nil {
		t.Fatalf("LoadDurableTraceByRun: %v", err)
	}
	keys, err := replay.NewDevelopmentKeyProvider("dev-key-v1", bytes.Repeat([]byte{0x42}, 32))
	if err != nil {
		t.Fatal(err)
	}
	sealer, err := replay.NewSealer(keys)
	if err != nil {
		t.Fatal(err)
	}
	objects := objectstore.NewMemoryStore()
	stager, err := replay.NewPostgresStager(api.db.Pool(), sealer, objects, replay.StagerConfig{
		ObjectPrefix: "producer-staging", Retention: 7 * 24 * time.Hour,
	})
	if err != nil {
		t.Fatalf("NewPostgresStager: %v", err)
	}
	payload, err := replay.NewPlainPayload(replay.ClassModelRequest, 1, []byte(`{"schema_version":1,"class":"model_request","messages":[{"role":"user","text":"private plan"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	request := replay.StageRequest{
		TraceID: trace.TraceID, IdentityKey: "attempt:1/decision:1/model_request", Payload: payload,
	}
	staged, err := stager.Stage(ctx, request)
	if err != nil {
		t.Fatalf("Stage: %v", err)
	}
	if staged.AttachmentID == "" || staged.ObjectKey == "" || staged.Class != replay.ClassModelRequest ||
		staged.PlaintextSHA256 != payload.SHA256 || staged.CiphertextBytes < 1 || staged.ExpiresAt.IsZero() {
		t.Fatalf("staged Replay = %#v", staged)
	}
	ciphertext, err := objects.Get(ctx, staged.ObjectKey, replay.MaxCiphertextBytes)
	if err != nil {
		t.Fatalf("Get staged object: %v", err)
	}
	if bytes.Contains(ciphertext, []byte("private plan")) || len(ciphertext) != staged.CiphertextBytes {
		t.Fatalf("staged object bytes = %d plaintext=%t", len(ciphertext), bytes.Contains(ciphertext, []byte("private plan")))
	}
	var metadataRows, ciphertextColumns, capacityBytes int
	if err := api.db.Pool().QueryRow(ctx, `select count(*) from agentobs_replay_staging where attachment_id = $1`, staged.AttachmentID).Scan(&metadataRows); err != nil {
		t.Fatal(err)
	}
	if err := api.db.Pool().QueryRow(ctx, `
		select count(*) from information_schema.columns
		where table_schema = 'public' and table_name = 'agentobs_replay_staging'
			and column_name in ('ciphertext', 'plaintext', 'payload')
	`).Scan(&ciphertextColumns); err != nil {
		t.Fatal(err)
	}
	if err := api.db.Pool().QueryRow(ctx, `select current_staged_ciphertext_bytes from agentobs_outbox_capacity where singleton`).Scan(&capacityBytes); err != nil {
		t.Fatal(err)
	}
	if metadataRows != 1 || ciphertextColumns != 0 || capacityBytes != staged.CiphertextBytes {
		t.Fatalf("staging metadata rows=%d ciphertext_columns=%d capacity=%d", metadataRows, ciphertextColumns, capacityBytes)
	}

	replayed, err := stager.Stage(ctx, request)
	if err != nil {
		t.Fatalf("Stage identical Replay: %v", err)
	}
	if replayed.AttachmentID != staged.AttachmentID || replayed.ObjectKey != staged.ObjectKey || objects.Len() != 1 {
		t.Fatalf("reconciled Replay = %#v objects=%d", replayed, objects.Len())
	}
	conflictingPayload, _ := replay.NewPlainPayload(replay.ClassModelRequest, 1, []byte(`{"schema_version":1,"class":"model_request","messages":[{"role":"user","text":"different"}]}`))
	_, err = stager.Stage(ctx, replay.StageRequest{
		TraceID: trace.TraceID, IdentityKey: request.IdentityKey, Payload: conflictingPayload,
	})
	if !errors.Is(err, replay.ErrIdentityConflict) || objects.Len() != 1 {
		t.Fatalf("conflicting Stage error=%v objects=%d", err, objects.Len())
	}
}

func TestReplayStagerObjectWriteFailureLeavesNoMetadata(t *testing.T) {
	api, sessionCookie, csrfCookie, chatID := newChatFixture(t, "replay-staging-object-failure@example.com")
	runID := admitRunForLeaseTest(t, api, sessionCookie, csrfCookie, chatID, "0190cdd2-5f2d-7ad8-b3f5-1b588788c449")
	trace, err := agent.LoadDurableTraceByRun(context.Background(), api.db.Pool(), runID)
	if err != nil {
		t.Fatal(err)
	}
	keys, _ := replay.NewDevelopmentKeyProvider("dev-key-v1", bytes.Repeat([]byte{0x29}, 32))
	sealer, _ := replay.NewSealer(keys)
	objects := &putFailingObjectStore{Store: objectstore.NewMemoryStore()}
	stager, err := replay.NewPostgresStager(api.db.Pool(), sealer, objects, replay.StagerConfig{ObjectPrefix: "producer-staging"})
	if err != nil {
		t.Fatal(err)
	}
	payload, _ := replay.NewPlainPayload(replay.ClassModelRequest, 1, []byte(`{"schema_version":1,"class":"model_request","messages":[]}`))
	_, err = stager.Stage(context.Background(), replay.StageRequest{
		TraceID: trace.TraceID, IdentityKey: "attempt:1/model:1/write-failure", Payload: payload,
	})
	if err == nil || !strings.Contains(err.Error(), "stage Replay object") {
		t.Fatalf("Stage error = %v, want object-write failure", err)
	}
	var metadata, capacity int
	if err := api.db.Pool().QueryRow(context.Background(), `select count(*) from agentobs_replay_staging where trace_id = $1`, trace.TraceID).Scan(&metadata); err != nil {
		t.Fatal(err)
	}
	if err := api.db.Pool().QueryRow(context.Background(), `select current_staged_ciphertext_bytes from agentobs_outbox_capacity where singleton`).Scan(&capacity); err != nil {
		t.Fatal(err)
	}
	if metadata != 0 || capacity != 0 {
		t.Fatalf("failed object write left metadata=%d capacity=%d", metadata, capacity)
	}
}

func TestReplayStagerCapacityFailureDeletesUnreferencedObject(t *testing.T) {
	api, sessionCookie, csrfCookie, chatID := newChatFixture(t, "replay-staging-capacity@example.com")
	runID := admitRunForLeaseTest(t, api, sessionCookie, csrfCookie, chatID, "0190cdd2-5f2d-7ad8-b3f5-1b588788c442")
	trace, err := agent.LoadDurableTraceByRun(context.Background(), api.db.Pool(), runID)
	if err != nil {
		t.Fatal(err)
	}
	keys, _ := replay.NewDevelopmentKeyProvider("dev-key-v1", bytes.Repeat([]byte{0x24}, 32))
	sealer, _ := replay.NewSealer(keys)
	objects := objectstore.NewMemoryStore()
	stager, err := replay.NewPostgresStager(api.db.Pool(), sealer, objects, replay.StagerConfig{ObjectPrefix: "producer-staging"})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if _, err := api.db.Pool().Exec(ctx, `
		update agentobs_outbox_capacity
		set max_staged_ciphertext_bytes = current_staged_ciphertext_bytes + 1
		where singleton
	`); err != nil {
		t.Fatal(err)
	}
	payload, _ := replay.NewPlainPayload(replay.ClassActionInput, 1, []byte(`{"schema_version":1,"class":"action_input","input":{"city":"Shanghai"}}`))
	_, err = stager.Stage(ctx, replay.StageRequest{
		TraceID: trace.TraceID, IdentityKey: "attempt:1/action:1/input", Payload: payload,
	})
	if !errors.Is(err, replay.ErrCapacityExceeded) {
		t.Fatalf("Stage error = %v, want ErrCapacityExceeded", err)
	}
	if objects.Len() != 0 {
		t.Fatalf("capacity failure retained %d unreferenced objects", objects.Len())
	}
}

func TestReplayStagingMaintenanceRemovesExpiredUnattachedAndOldOrphanObjects(t *testing.T) {
	api, sessionCookie, csrfCookie, chatID := newChatFixture(t, "replay-staging-maintenance@example.com")
	runID := admitRunForLeaseTest(t, api, sessionCookie, csrfCookie, chatID, "0190cdd2-5f2d-7ad8-b3f5-1b588788c446")
	trace, err := agent.LoadDurableTraceByRun(context.Background(), api.db.Pool(), runID)
	if err != nil {
		t.Fatal(err)
	}
	keys, _ := replay.NewDevelopmentKeyProvider("dev-key-v1", bytes.Repeat([]byte{0x27}, 32))
	sealer, _ := replay.NewSealer(keys)
	objects := objectstore.NewMemoryStore()
	stager, _ := replay.NewPostgresStager(api.db.Pool(), sealer, objects, replay.StagerConfig{ObjectPrefix: "producer-staging"})
	payload, _ := replay.NewPlainPayload(replay.ClassModelRequest, 1, []byte(`{"schema_version":1,"class":"model_request","messages":[{"role":"user","text":"expires"}]}`))
	staged, err := stager.Stage(context.Background(), replay.StageRequest{
		TraceID: trace.TraceID, IdentityKey: "attempt:1/model:1/expired", Payload: payload,
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if _, err := api.db.Pool().Exec(ctx, `update agentobs_replay_staging set expires_at = now() - interval '1 second' where attachment_id = $1`, staged.AttachmentID); err != nil {
		t.Fatal(err)
	}
	if err := objects.Put(ctx, "producer-staging/orphan", []byte("opaque")); err != nil {
		t.Fatal(err)
	}
	maintenance, err := replay.NewStagingMaintenance(api.db.Pool(), objects, replay.StagingMaintenanceConfig{
		ObjectPrefix: "producer-staging", BatchSize: 16, OrphanGrace: time.Hour,
		Now: func() time.Time { return time.Now().UTC().Add(2 * time.Hour) },
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := maintenance.RunOnce(ctx)
	if err != nil || result.ExpiredDeleted != 1 || result.OrphansDeleted != 1 || objects.Len() != 0 {
		t.Fatalf("RunOnce = %#v objects=%d err=%v", result, objects.Len(), err)
	}
	var metadata, capacity int
	_ = api.db.Pool().QueryRow(ctx, `select count(*) from agentobs_replay_staging where trace_id = $1`, trace.TraceID).Scan(&metadata)
	_ = api.db.Pool().QueryRow(ctx, `select current_staged_ciphertext_bytes from agentobs_outbox_capacity where singleton`).Scan(&capacity)
	if metadata != 0 || capacity != 0 {
		t.Fatalf("expired staging metadata=%d capacity=%d", metadata, capacity)
	}
}

func TestParentDeletionAtomicallyPrioritizesPurgeAndCleansProducerReplayAfterACK(t *testing.T) {
	api, sessionCookie, csrfCookie, chatID := newChatFixture(t, "replay-parent-purge@example.com")
	runID := admitRunForLeaseTest(t, api, sessionCookie, csrfCookie, chatID, "0190cdd2-5f2d-7ad8-b3f5-1b588788c447")
	ctx := context.Background()
	trace, err := agent.LoadDurableTraceByRun(ctx, api.db.Pool(), runID)
	if err != nil {
		t.Fatal(err)
	}
	keys, _ := replay.NewDevelopmentKeyProvider("dev-key-v1", bytes.Repeat([]byte{0x28}, 32))
	sealer, _ := replay.NewSealer(keys)
	objects := objectstore.NewMemoryStore()
	stager, _ := replay.NewPostgresStager(api.db.Pool(), sealer, objects, replay.StagerConfig{ObjectPrefix: "producer-staging"})
	payload, _ := replay.NewPlainPayload(replay.ClassModelRequest, 1, []byte(`{"schema_version":1,"class":"model_request","messages":[{"role":"user","text":"delete me"}]}`))
	if _, err := stager.Stage(ctx, replay.StageRequest{
		TraceID: trace.TraceID, IdentityKey: "attempt:1/model:1/delete", Payload: payload,
	}); err != nil {
		t.Fatal(err)
	}
	directObjectKey := replay.StagingTracePrefix("", trace.TraceID) + "/objects/direct-fixture"
	if err := objects.Put(ctx, directObjectKey, []byte("direct Replay ciphertext")); err != nil {
		t.Fatal(err)
	}
	var notebookID string
	if err := api.db.Pool().QueryRow(ctx, `select notebook_id from agent_trace_refs where trace_id = $1`, trace.TraceID).Scan(&notebookID); err != nil {
		t.Fatal(err)
	}
	if _, err := api.db.Pool().Exec(ctx, `delete from notebook_notebooks where id = $1`, notebookID); err != nil {
		t.Fatalf("delete parent Notebook: %v", err)
	}
	var refs, staging, commands, cleanupObjects int
	var deliveryState string
	_ = api.db.Pool().QueryRow(ctx, `select count(*) from agent_trace_refs where trace_id = $1`, trace.TraceID).Scan(&refs)
	_ = api.db.Pool().QueryRow(ctx, `select count(*) from agentobs_replay_staging where trace_id = $1`, trace.TraceID).Scan(&staging)
	if err := api.db.Pool().QueryRow(ctx, `
		select count(*), min(delivery_state) from agentobs_outbox_commands where trace_id = $1
	`, trace.TraceID).Scan(&commands, &deliveryState); err != nil {
		t.Fatal(err)
	}
	_ = api.db.Pool().QueryRow(ctx, `select count(*) from agentobs_outbox_command_objects`).Scan(&cleanupObjects)
	if refs != 0 || staging != 0 || commands != 1 || cleanupObjects != 1 || deliveryState != "ready" || objects.Len() != 2 {
		t.Fatalf("post-delete refs=%d staging=%d commands=%d cleanup=%d state=%s objects=%d", refs, staging, commands, cleanupObjects, deliveryState, objects.Len())
	}
	outbox, err := agentoutbox.NewPostgresStore(api.db.Pool(), agentoutbox.Config{
		ProducerID: "nano-worker", MaxRecords: 128, MaxEncodedBytes: 512 * 1024,
		MaxTraces: 16, LeaseDuration: 30 * time.Second, StagingObjects: objects,
	})
	if err != nil {
		t.Fatal(err)
	}
	claimed, ok, err := outbox.ClaimPurgeBatch(ctx)
	if err != nil || !ok || len(claimed.Batch.Commands) != 1 || claimed.Batch.Commands[0].TraceID != trace.TraceID {
		t.Fatalf("ClaimPurgeBatch = %#v ok=%t err=%v", claimed, ok, err)
	}
	if _, ok, err := outbox.ClaimBatch(ctx); err != nil || ok {
		t.Fatalf("ordinary ClaimBatch after parent delete ok=%t err=%v", ok, err)
	}
	if err := outbox.ApplyPurgeResult(ctx, claimed, collector.PurgeBatchResult{
		BatchID:  claimed.Batch.BatchID,
		Commands: []collector.PurgeCommandResult{{TraceID: trace.TraceID, Status: collector.PurgeAcknowledged}},
	}); err != nil {
		t.Fatalf("ApplyPurgeResult: %v", err)
	}
	_ = api.db.Pool().QueryRow(ctx, `select delivery_state from agentobs_outbox_commands where trace_id = $1`, trace.TraceID).Scan(&deliveryState)
	_ = api.db.Pool().QueryRow(ctx, `select count(*) from agentobs_outbox_command_objects`).Scan(&cleanupObjects)
	if deliveryState != "acknowledged" || cleanupObjects != 0 || objects.Len() != 0 {
		t.Fatalf("purge ACK state=%s cleanup=%d objects=%d", deliveryState, cleanupObjects, objects.Len())
	}
}

func TestTraceRecordAtomicallyBindsAnExistingStagedReplayAttachment(t *testing.T) {
	api, sessionCookie, csrfCookie, chatID := newChatFixture(t, "replay-binding@example.com")
	runID := admitRunForLeaseTest(t, api, sessionCookie, csrfCookie, chatID, "0190cdd2-5f2d-7ad8-b3f5-1b588788c443")
	trace, err := agent.LoadDurableTraceByRun(context.Background(), api.db.Pool(), runID)
	if err != nil {
		t.Fatal(err)
	}
	keys, _ := replay.NewDevelopmentKeyProvider("dev-key-v1", bytes.Repeat([]byte{0x33}, 32))
	sealer, _ := replay.NewSealer(keys)
	objects := objectstore.NewMemoryStore()
	stager, err := replay.NewPostgresStager(api.db.Pool(), sealer, objects, replay.StagerConfig{})
	if err != nil {
		t.Fatal(err)
	}
	payload, _ := replay.NewPlainPayload(replay.ClassModelRequest, 1, []byte(`{"schema_version":1,"class":"model_request","messages":[]}`))
	staged, err := stager.Stage(context.Background(), replay.StageRequest{
		TraceID: trace.TraceID, IdentityKey: "attempt:1/decision:1/model_request", Payload: payload,
	})
	if err != nil {
		t.Fatal(err)
	}
	exporter, err := agent.NewPostgresTraceExporter(api.db.Pool())
	if err != nil {
		t.Fatal(err)
	}
	event := traceRecord(agentobs.RecordEvent, trace.TraceID, trace.RootSpanID, "decision:1/model_request/staged", "agent.replay.staged")
	event.Attributes = []agentobs.Attribute{agentobs.String(replay.ModelRequestAttachmentKey, staged.AttachmentID)}
	if err := exporter.Export(context.Background(), event); err != nil {
		t.Fatalf("Export attached record: %v", err)
	}
	var state string
	var recordSequence int
	if err := api.db.Pool().QueryRow(context.Background(), `
		select state, record_sequence from agentobs_replay_staging where attachment_id = $1
	`, staged.AttachmentID).Scan(&state, &recordSequence); err != nil {
		t.Fatal(err)
	}
	if state != "attached" || recordSequence != 3 {
		t.Fatalf("bound Replay state/sequence = %s/%d", state, recordSequence)
	}
	outbox, err := agentoutbox.NewPostgresStore(api.db.Pool(), agentoutbox.Config{
		ProducerID: "nano-worker", MaxRecords: 128, MaxEncodedBytes: 512 * 1024,
		MaxTraces: 16, LeaseDuration: 30 * time.Second, StagingObjects: objects,
	})
	if err != nil {
		t.Fatal(err)
	}
	claimed, ok, err := outbox.ClaimBatch(context.Background())
	if err != nil || !ok || len(claimed.Batch.Chunks) != 1 || len(claimed.Batch.Chunks[0].Attachments) != 1 {
		t.Fatalf("ClaimBatch with Replay = %#v ok=%t err=%v", claimed, ok, err)
	}
	descriptor := claimed.Batch.Chunks[0].Attachments[0]
	if descriptor.AttachmentID != staged.AttachmentID || descriptor.RecordSequence != 3 ||
		descriptor.StagingObjectKey != staged.ObjectKey || descriptor.CiphertextSHA256 != staged.CiphertextSHA256 ||
		descriptor.CiphertextBytes != staged.CiphertextBytes {
		t.Fatalf("claimed Replay descriptor = %#v", descriptor)
	}
	if err := outbox.ApplyResult(context.Background(), claimed, collector.BatchResult{
		BatchID: claimed.Batch.BatchID,
		Chunks: []collector.ChunkResult{{
			TraceID: trace.TraceID, Status: collector.ChunkCommitted, CommittedThrough: 3,
		}},
	}); err != nil {
		t.Fatalf("ApplyResult with Replay ACK: %v", err)
	}
	var retainedStaging, retainedCapacity int
	if err := api.db.Pool().QueryRow(context.Background(), `select count(*) from agentobs_replay_staging`).Scan(&retainedStaging); err != nil {
		t.Fatal(err)
	}
	if err := api.db.Pool().QueryRow(context.Background(), `select current_staged_ciphertext_bytes from agentobs_outbox_capacity where singleton`).Scan(&retainedCapacity); err != nil {
		t.Fatal(err)
	}
	if objects.Len() != 0 || retainedStaging != 0 || retainedCapacity != 0 {
		t.Fatalf("Replay ACK cleanup objects/metadata/capacity = %d/%d/%d", objects.Len(), retainedStaging, retainedCapacity)
	}

	missing := traceRecord(agentobs.RecordEvent, trace.TraceID, trace.RootSpanID, "decision:1/model_decision/missing", "agent.replay.staged")
	missing.Attributes = []agentobs.Attribute{agentobs.String(replay.ModelDecisionAttachmentKey, "019bf000-0000-7000-8000-000000000099")}
	if err := exporter.Export(context.Background(), missing); err == nil {
		t.Fatal("Export accepted missing Replay Attachment")
	}
	loaded, err := agent.LoadDurableTrace(context.Background(), api.db.Pool(), trace.TraceID)
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.Records) != 3 {
		t.Fatalf("missing Attachment advanced Trace to %d records", len(loaded.Records))
	}
}

type putFailingObjectStore struct {
	objectstore.Store
}

func (*putFailingObjectStore) Put(context.Context, string, []byte) error {
	return errors.New("injected object write failure")
}
