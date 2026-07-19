package app_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/huangxinxinyu/nano-notebook/internal/agent"
	"github.com/huangxinxinyu/nano-notebook/internal/agentbatch"
	"github.com/huangxinxinyu/nano-notebook/internal/agentobs"
	"github.com/huangxinxinyu/nano-notebook/internal/agentoutbox"
	"github.com/huangxinxinyu/nano-notebook/internal/app"
	"github.com/huangxinxinyu/nano-notebook/internal/collector"
	"github.com/huangxinxinyu/nano-notebook/internal/jobs"
	"github.com/huangxinxinyu/nano-notebook/internal/objectstore"
	"github.com/huangxinxinyu/nano-notebook/internal/replay"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const sprint5CapacitySummaryCount = 100_000

type capacityFixture struct {
	session, csrf *http.Cookie
	chatID        string
}

func TestSprint5TenConcurrentJobsRecoverFromLostCollectorACK(t *testing.T) {
	t.Skip("superseded by bounded-memory direct-delivery capacity gate")
	if os.Getenv("NANO_RUN_SPRINT5_CAPACITY") != "1" {
		t.Skip("set NANO_RUN_SPRINT5_CAPACITY=1 to run the production-shaped capacity gate")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()
	api := newTestAPI(t)
	fixtures := make([]capacityFixture, 10)
	for index := range fixtures {
		fixtures[index] = newCapacityChatFixture(t, api, index)
	}

	type admissionResult struct {
		index    int
		runID    string
		duration time.Duration
		err      error
	}
	admissions := make(chan admissionResult, len(fixtures))
	for index, currentFixture := range fixtures {
		go func(index int, currentFixture capacityFixture) {
			started := time.Now()
			response := api.postJSONWithCookieAndCSRF(t, "/api/v1/chats/"+currentFixture.chatID+"/messages", map[string]any{
				"id": fmt.Sprintf("0190cdd2-5f2d-7ad8-b3f5-%012x", index+1), "content": "Exercise Sprint 5 capacity.",
			}, currentFixture.session, currentFixture.csrf, currentFixture.csrf.Value, "")
			result := admissionResult{index: index, duration: time.Since(started)}
			if response.Code != http.StatusAccepted {
				result.err = fmt.Errorf("admission %d status=%d body=%s", index, response.Code, response.Body.String())
			} else {
				var body struct {
					RunID string `json:"run_id"`
				}
				if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil || body.RunID == "" {
					result.err = fmt.Errorf("decode admission %d: %w", index, err)
				} else {
					result.runID = body.RunID
				}
			}
			admissions <- result
		}(index, currentFixture)
	}
	runIDs := make([]string, len(fixtures))
	admissionDurations := make([]time.Duration, 0, len(fixtures))
	for range fixtures {
		result := <-admissions
		if result.err != nil {
			t.Fatal(result.err)
		}
		runIDs[result.index] = result.runID
		admissionDurations = append(admissionDurations, result.duration)
	}
	producerObjects := objectstore.NewMemoryStore()
	collectorReplayObjects := objectstore.NewMemoryStore()
	keys, err := replay.NewDevelopmentKeyProvider("capacity-key-v1", bytes.Repeat([]byte{0x5c}, 32))
	if err != nil {
		t.Fatal(err)
	}
	sealer, err := replay.NewSealer(keys)
	if err != nil {
		t.Fatal(err)
	}
	stager, err := replay.NewPostgresStager(api.db.Pool(), sealer, producerObjects, replay.StagerConfig{ObjectPrefix: "capacity-staging"})
	if err != nil {
		t.Fatal(err)
	}
	replayAttributesByRun := make(map[string][]agentobs.Attribute, len(runIDs))
	for _, runID := range runIDs {
		trace, loadErr := agent.LoadDurableTraceByRun(ctx, api.db.Pool(), runID)
		if loadErr != nil {
			t.Fatal(loadErr)
		}
		attributes := make([]agentobs.Attribute, 0, len(capacityReplayClasses))
		for _, replayClass := range capacityReplayClasses {
			payload, payloadErr := replay.NewPlainPayload(replayClass.class, 1, []byte(replayClass.payload))
			if payloadErr != nil {
				t.Fatal(payloadErr)
			}
			staged, stageErr := stager.Stage(ctx, replay.StageRequest{
				TraceID: trace.TraceID, IdentityKey: "capacity/" + string(replayClass.class), Payload: payload,
			})
			if stageErr != nil {
				t.Fatal(stageErr)
			}
			attributes = append(attributes, agentobs.String(replayClass.attributeKey, staged.AttachmentID))
		}
		replayAttributesByRun[runID] = attributes
	}

	queue := jobs.NewQueue(api.db.Pool())
	type claimResult struct {
		job jobs.ClaimedJob
		ok  bool
		err error
	}
	claims := make(chan claimResult, len(fixtures))
	for range fixtures {
		go func() {
			job, ok, err := queue.ClaimNext(ctx)
			claims <- claimResult{job: job, ok: ok, err: err}
		}()
	}
	claimed := make([]jobs.ClaimedJob, 0, len(fixtures))
	for range fixtures {
		result := <-claims
		if result.err != nil || !result.ok {
			t.Fatalf("concurrent Job claim ok=%t err=%v", result.ok, result.err)
		}
		claimed = append(claimed, result.job)
	}

	largeValue := strings.Repeat("x", 3400)
	appendErrors := make(chan error, len(claimed))
	appendStarted := time.Now()
	for _, claimedJob := range claimed {
		go func(claimedJob jobs.ClaimedJob) {
			runtime := agent.NewPostgresRuntime(api.db.Pool(), agent.BareSystemPrompt, nil)
			attemptContext, tracer, err := runtime.StartAttemptTrace(ctx, attemptFromClaim(claimedJob))
			if err != nil {
				appendErrors <- err
				return
			}
			for eventIndex := 0; eventIndex < 247; eventIndex++ {
				attributes := []agentobs.Attribute{agentobs.String("capacity.payload", largeValue)}
				if eventIndex < len(replayAttributesByRun[claimedJob.RunID]) {
					attributes = append(attributes, replayAttributesByRun[claimedJob.RunID][eventIndex])
				}
				if err := tracer.Event(attemptContext, agentobs.Event{
					IdentityKey: fmt.Sprintf("run/%s/capacity/event/%03d", claimedJob.RunID, eventIndex),
					Name:        "nano.capacity.event",
					Attributes:  attributes,
				}); err != nil {
					appendErrors <- fmt.Errorf("append Run %s event %d: %w", claimedJob.RunID, eventIndex, err)
					return
				}
				if eventIndex > 0 && eventIndex%50 == 0 {
					ok, heartbeatErr := queue.Heartbeat(ctx, claimedJob.ID, claimedJob.LeaseToken, jobs.DefaultLeaseDuration)
					if heartbeatErr != nil || !ok {
						appendErrors <- fmt.Errorf("heartbeat Run %s ok=%t: %w", claimedJob.RunID, ok, heartbeatErr)
						return
					}
				}
			}
			appendErrors <- nil
		}(claimedJob)
	}
	for range claimed {
		if err := <-appendErrors; err != nil {
			t.Fatal(err)
		}
	}
	appendDuration := time.Since(appendStarted)

	type cancellationResult struct {
		duration time.Duration
		err      error
	}
	cancellations := make(chan cancellationResult, len(fixtures))
	for index, currentFixture := range fixtures {
		go func(runID string, currentFixture capacityFixture) {
			started := time.Now()
			response := api.postJSONWithCookieAndCSRF(t, "/api/v1/agent-runs/"+runID+"/cancel", map[string]any{}, currentFixture.session, currentFixture.csrf, currentFixture.csrf.Value, "")
			result := cancellationResult{duration: time.Since(started)}
			if response.Code != http.StatusOK {
				result.err = fmt.Errorf("cancel Run %s status=%d body=%s", runID, response.Code, response.Body.String())
			}
			cancellations <- result
		}(runIDs[index], currentFixture)
	}
	cancellationDurations := make([]time.Duration, 0, len(fixtures))
	for range fixtures {
		result := <-cancellations
		if result.err != nil {
			t.Fatal(result.err)
		}
		cancellationDurations = append(cancellationDurations, result.duration)
	}

	expectedByTrace := make(map[string]int, len(fixtures))
	for _, runID := range runIDs {
		trace, err := agent.LoadDurableTraceByRun(ctx, api.db.Pool(), runID)
		if err != nil {
			t.Fatal(err)
		}
		if len(trace.Records) != 254 {
			t.Fatalf("capacity Trace %s records=%d, want 254", trace.TraceID, len(trace.Records))
		}
		expectedByTrace[string(trace.TraceID)] = len(trace.Records)
	}

	observability := openCapacityObservabilityPool(t, ctx)
	defer observability.Close()
	resetCapacityObservability(t, ctx, observability)
	collectorStore, err := collector.NewPostgresStoreWithReplay(observability, producerObjects, collectorReplayObjects)
	if err != nil {
		t.Fatal(err)
	}
	ingestor, err := collector.NewIngestor(collector.IngestorConfig{ProducerID: "capacity-producer", Store: collectorStore})
	if err != nil {
		t.Fatal(err)
	}
	collectorHandler, err := collector.NewHTTPHandler(collector.HTTPConfig{
		Ingestor: ingestor, ServiceToken: "capacity-ingest-token", MaxBodyBytes: 2 * 1024 * 1024,
	})
	if err != nil {
		t.Fatal(err)
	}
	var droppedACK atomic.Bool
	var collectorFailure atomic.Value
	collectorServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/internal/agent-observability/v1/batches" && droppedACK.CompareAndSwap(false, true) {
			recorder := httptest.NewRecorder()
			collectorHandler.ServeHTTP(recorder, request)
			if recorder.Code != http.StatusOK {
				collectorFailure.Store(fmt.Sprintf("lost-ACK commit status=%d body=%s", recorder.Code, recorder.Body.String()))
			}
			panic(http.ErrAbortHandler)
		}
		collectorHandler.ServeHTTP(w, request)
	}))
	defer collectorServer.Close()
	outbox, err := agentoutbox.NewPostgresStore(api.db.Pool(), agentoutbox.Config{
		ProducerID: "capacity-producer", MaxRecords: 128, MaxEncodedBytes: 512 * 1024,
		MaxTraces: 10, LeaseDuration: 30 * time.Second, BaseBackoff: 2 * time.Millisecond,
		MaxBackoff: 2 * time.Millisecond, RetryJitter: func() float64 { return 0 },
		StagingObjects: producerObjects,
	})
	if err != nil {
		t.Fatal(err)
	}
	sender, err := agentoutbox.NewSender(outbox, agentoutbox.SenderConfig{
		Endpoint:     collectorServer.URL + "/internal/agent-observability/v1/batches",
		ServiceToken: "capacity-ingest-token", HTTPClient: collectorServer.Client(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if attempted, sendErr := sender.SendOnce(ctx); !attempted || sendErr == nil {
		t.Fatalf("lost-ACK send attempted=%t err=%v", attempted, sendErr)
	}
	if failure := collectorFailure.Load(); failure != nil {
		t.Fatal(failure)
	}
	waitForCapacityTransportRetry(t, ctx, api.db.Pool())
	if err := sender.ForceFlush(ctx); err != nil {
		t.Fatal(err)
	}

	var rawRecords, distinctRecords, traceCount int
	if err := observability.QueryRow(ctx, `select count(*), count(distinct (trace_id, sequence)), count(distinct trace_id) from obs_trace_records`).Scan(&rawRecords, &distinctRecords, &traceCount); err != nil {
		t.Fatal(err)
	}
	if rawRecords != 2540 || distinctRecords != rawRecords || traceCount != 10 {
		t.Fatalf("Collector records=%d distinct=%d traces=%d", rawRecords, distinctRecords, traceCount)
	}
	var replayAttachments int
	if err := observability.QueryRow(ctx, `select count(*) from obs_payload_refs`).Scan(&replayAttachments); err != nil {
		t.Fatal(err)
	}
	if replayAttachments != 40 || collectorReplayObjects.Len() != 40 {
		t.Fatalf("Collector Replay metadata/objects=%d/%d, want 40/40", replayAttachments, collectorReplayObjects.Len())
	}
	for traceID, expected := range expectedByTrace {
		var count, cursor int
		if err := observability.QueryRow(ctx, `select count(*), max(sequence) from obs_trace_records where trace_id=$1`, traceID).Scan(&count, &cursor); err != nil {
			t.Fatal(err)
		}
		if count != expected || cursor != expected {
			t.Fatalf("Collector Trace %s count/cursor=%d/%d want %d", traceID, count, cursor, expected)
		}
	}
	var pendingRows, acknowledgedRefs int
	if err := api.db.Pool().QueryRow(ctx, `select count(*) from agentobs_outbox_records`).Scan(&pendingRows); err != nil {
		t.Fatal(err)
	}
	if err := api.db.Pool().QueryRow(ctx, `select count(*) from agent_trace_refs where delivery_state='acknowledged' and collector_cursor=terminal_sequence`).Scan(&acknowledgedRefs); err != nil {
		t.Fatal(err)
	}
	if pendingRows != 0 || acknowledgedRefs != 10 || !droppedACK.Load() {
		t.Fatalf("Outbox pending=%d acknowledged=%d dropped_ack=%t", pendingRows, acknowledgedRefs, droppedACK.Load())
	}
	var stagedMetadata int
	var stagedCiphertextBytes int64
	if err := api.db.Pool().QueryRow(ctx, `select count(*) from agentobs_replay_staging`).Scan(&stagedMetadata); err != nil {
		t.Fatal(err)
	}
	if err := api.db.Pool().QueryRow(ctx, `select current_staged_ciphertext_bytes from agentobs_outbox_capacity where singleton`).Scan(&stagedCiphertextBytes); err != nil {
		t.Fatal(err)
	}
	if producerObjects.Len() != 0 || stagedMetadata != 0 || stagedCiphertextBytes != 0 {
		t.Fatalf("producer Replay objects/metadata/bytes=%d/%d/%d, want zero after ACK", producerObjects.Len(), stagedMetadata, stagedCiphertextBytes)
	}

	projector, err := collector.NewProjector(observability, collector.ProjectorConfig{})
	if err != nil {
		t.Fatal(err)
	}
	for range fixtures {
		projected, runErr := projector.RunOnce(ctx)
		if runErr != nil || !projected {
			t.Fatalf("project capacity Trace projected=%t err=%v", projected, runErr)
		}
	}
	var summaries int
	if err := observability.QueryRow(ctx, `select count(*) from obs_trace_summaries`).Scan(&summaries); err != nil {
		t.Fatal(err)
	}
	if summaries != 10 {
		t.Fatalf("projected summaries=%d, want 10", summaries)
	}
	t.Logf("Sprint 5 10-Job capacity: admission_p95=%s terminal_p95=%s append_247x10=%s records=%d replay_attachments=%d",
		capacityP95(admissionDurations), capacityP95(cancellationDurations), appendDuration, rawRecords, replayAttachments)
}

func TestSprint5BoundedMemoryTenConcurrentTraceProducers(t *testing.T) {
	if os.Getenv("NANO_RUN_SPRINT5_CAPACITY") != "1" {
		t.Skip("set NANO_RUN_SPRINT5_CAPACITY=1 to run the production-shaped capacity gate")
	}
	store := collector.NewMemoryStore()
	ingestor, err := collector.NewIngestor(collector.IngestorConfig{ProducerID: "capacity-producer", Store: store})
	if err != nil {
		t.Fatal(err)
	}
	sender := &capacityDirectSender{ingestor: ingestor}
	exporter, err := agentbatch.NewExporter(agentbatch.Config{
		ProducerID: "capacity-producer", Sender: sender,
		MaxPendingRecords: 10_000, MaxPendingBytes: 32 * 1024 * 1024,
		MaxBatchRecords: 128, MaxBatchBytes: 512 * 1024, MaxDelay: 250 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	errorsByProducer := make(chan error, 10)
	for producer := 0; producer < 10; producer++ {
		go func(producer int) {
			traceID := agentobs.TraceID(fmt.Sprintf("trace-direct-cap-%02d", producer))
			rootID := agentobs.SpanID(fmt.Sprintf("root-direct-cap-%02d", producer))
			descriptor := collector.TraceDescriptor{
				TraceID: traceID, RunID: fmt.Sprintf("run-direct-cap-%02d", producer),
				ChatID: fmt.Sprintf("chat-direct-cap-%02d", producer), NotebookID: "notebook-direct-cap",
				RootSpanID: rootID, AgentName: "nano-research-agent", SchemaVersion: 1, SemanticConventionVersion: 1,
			}
			for recordIndex := 0; recordIndex < 254; recordIndex++ {
				kind, name, identity := agentobs.RecordEvent, "nano.capacity.event", fmt.Sprintf("run/%s/event/%03d", descriptor.RunID, recordIndex)
				if recordIndex == 0 {
					kind, name, identity = agentobs.RecordSpanStarted, "agent.execution", "run/"+descriptor.RunID+"/root/start"
				}
				record := agentobs.Record{
					SchemaVersion: 1, SemanticConventionVersion: 1, PayloadVersion: 1,
					IdentityKey: identity, Kind: kind, TraceID: traceID, SpanID: rootID, Name: name,
					OccurredAt: time.Unix(1_700_500_000+int64(recordIndex), 0).UTC(),
					Attributes: []agentobs.Attribute{agentobs.String("capacity.payload", strings.Repeat("x", 3400))},
				}
				if err := exporter.Offer(ctx, agentbatch.Envelope{Trace: descriptor, Record: record}); err != nil {
					errorsByProducer <- err
					return
				}
			}
			errorsByProducer <- nil
		}(producer)
	}
	for range 10 {
		if err := <-errorsByProducer; err != nil {
			t.Fatal(err)
		}
	}
	if err := exporter.ForceFlush(ctx); err != nil {
		t.Fatal(err)
	}
	stats := exporter.Stats()
	if stats.PendingRecords != 0 || stats.DroppedRecords != 0 || !sender.lostACK {
		t.Fatalf("direct capacity Stats=%#v lost_ack=%t", stats, sender.lostACK)
	}
	for producer := 0; producer < 10; producer++ {
		traceID := agentobs.TraceID(fmt.Sprintf("trace-direct-cap-%02d", producer))
		if got := len(store.Records(traceID)); got != 254 {
			t.Fatalf("Trace %s records=%d, want 254", traceID, got)
		}
	}
	if err := exporter.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
}

type capacityDirectSender struct {
	mu       sync.Mutex
	ingestor *collector.Ingestor
	lostACK  bool
}

func (s *capacityDirectSender) Send(ctx context.Context, batch collector.Batch) (collector.BatchResult, error) {
	result, err := s.ingestor.Ingest(ctx, batch)
	if err != nil {
		return collector.BatchResult{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.lostACK {
		s.lostACK = true
		return collector.BatchResult{}, errors.New("capacity ACK lost after Collector commit")
	}
	return result, nil
}

func TestSprint5QueryCapacityAtControlPlaneBoundary(t *testing.T) {
	if os.Getenv("NANO_RUN_SPRINT5_CAPACITY") != "1" {
		t.Skip("set NANO_RUN_SPRINT5_CAPACITY=1 to run the production-shaped capacity gate")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()

	observability := openCapacityObservabilityPool(t, ctx)
	defer observability.Close()
	resetCapacityObservability(t, ctx, observability)
	seedStarted := time.Now()
	seedCapacitySummaries(t, ctx, observability, sprint5CapacitySummaryCount)
	maximumTraceID := capacityTraceID(sprint5CapacitySummaryCount - 1)
	seedMaximumCapacityDetail(t, ctx, observability, maximumTraceID, 256)
	t.Logf("seeded %d Trace summaries and 256-Span detail in %s", sprint5CapacitySummaryCount, time.Since(seedStarted))

	queryStore, err := collector.NewTraceQueryStore(observability, nil)
	if err != nil {
		t.Fatal(err)
	}
	ingestor, _ := collector.NewIngestor(collector.IngestorConfig{ProducerID: "capacity-producer", Store: collector.NewMemoryStore()})
	collectorHandler, err := collector.NewHTTPHandler(collector.HTTPConfig{
		Ingestor: ingestor, ServiceToken: "capacity-ingest-token", QueryStore: queryStore,
		QueryToken: "capacity-query-token", MaxBodyBytes: 2 * 1024 * 1024,
	})
	if err != nil {
		t.Fatal(err)
	}
	collectorServer := httptest.NewServer(collectorHandler)
	defer collectorServer.Close()
	queryClient, err := collector.NewHTTPQueryClient(collector.HTTPQueryClientConfig{
		Endpoint: collectorServer.URL, ServiceToken: "capacity-query-token",
		Client: &http.Client{Timeout: 2 * time.Second},
	})
	if err != nil {
		t.Fatal(err)
	}

	api := newTestAPI(t)
	session, _ := api.registerWithCSRF(t, "sprint5-capacity-operator@example.com")
	var userID string
	if err := api.db.Pool().QueryRow(ctx, `select id from identity_users where canonical_email = 'sprint5-capacity-operator@example.com'`).Scan(&userID); err != nil {
		t.Fatal(err)
	}
	grantCapability(t, api, userID, "platform.trace.read")
	server := app.NewServer(app.Config{CookieSecure: false, AdminTraces: queryClient}, api.db)
	controlPlane := httptest.NewServer(server.Handler())
	defer controlPlane.Close()

	listPath := "/api/admin/traces?page_size=50"
	for range 3 {
		assertCapacityGET(t, controlPlane.URL+listPath, session)
	}
	listP95 := measureCapacityP95(t, 20, func() {
		assertCapacityGET(t, controlPlane.URL+listPath, session)
	})
	if listP95 >= 500*time.Millisecond {
		t.Fatalf("Trace list p95 = %s, want < 500ms", listP95)
	}

	detailPath := "/api/admin/traces/" + maximumTraceID
	for range 3 {
		assertCapacityGET(t, controlPlane.URL+detailPath, session)
	}
	detailP95 := measureCapacityP95(t, 20, func() {
		assertCapacityGET(t, controlPlane.URL+detailPath, session)
	})
	if detailP95 >= time.Second {
		t.Fatalf("maximum Trace detail p95 = %s, want < 1s", detailP95)
	}

	first := capacityListPage(t, controlPlane.URL+"/api/admin/traces?page_size=50", session)
	seedCapacitySummary(t, ctx, observability, sprint5CapacitySummaryCount+1, capacityStartedAt(sprint5CapacitySummaryCount+1), 1)
	second := capacityListPage(t, controlPlane.URL+"/api/admin/traces?page_size=50&cursor="+url.QueryEscape(first.Data.NextCursor), session)
	seen := make(map[string]bool, len(first.Data.Items))
	for _, item := range first.Data.Items {
		seen[string(item.Summary.TraceID)] = true
	}
	for _, item := range second.Data.Items {
		if seen[string(item.Summary.TraceID)] {
			t.Fatalf("cursor page repeated Trace %q after concurrent ingestion", item.Summary.TraceID)
		}
	}
	t.Logf("Sprint 5 capacity: list_p95=%s detail_256_span_p95=%s", listP95, detailP95)
}

func openCapacityObservabilityPool(t *testing.T, ctx context.Context) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("NANO_TEST_OBSERVABILITY_CAPACITY_DATABASE_URL")
	if dsn == "" {
		t.Fatal("NANO_TEST_OBSERVABILITY_CAPACITY_DATABASE_URL is required for the capacity gate")
	}
	config, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(config.ConnConfig.Database, "_capacity") {
		t.Fatalf("capacity gate refuses non-capacity database %q", config.ConnConfig.Database)
	}
	config.MaxConns = 16
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		t.Fatal(err)
	}
	return pool
}

func resetCapacityObservability(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	t.Helper()
	if _, err := pool.Exec(ctx, `drop schema if exists public cascade`); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `create schema public`); err != nil {
		t.Fatal(err)
	}
	if err := collector.RunMigrations(ctx, pool); err != nil {
		t.Fatal(err)
	}
}

func seedCapacitySummaries(t *testing.T, ctx context.Context, pool *pgxpool.Pool, count int) {
	t.Helper()
	traceColumns := []string{"trace_id", "run_id", "chat_id", "notebook_id", "root_span_id", "agent_name", "schema_version", "semantic_convention_version", "committed_sequence", "projected_sequence"}
	inserted, err := pool.CopyFrom(ctx, pgx.Identifier{"obs_traces"}, traceColumns, pgx.CopyFromSlice(count, func(index int) ([]any, error) {
		sequence := 1
		if index == count-1 {
			sequence = 256
		}
		traceID := capacityTraceID(index)
		return []any{traceID, capacityRunID(index), capacityChatID(index), "notebook-capacity", capacityRootSpanID(index), "nano-research-agent", 1, 1, sequence, sequence}, nil
	}))
	if err != nil || inserted != int64(count) {
		t.Fatalf("seed obs_traces inserted=%d: %v", inserted, err)
	}
	summaryColumns := []string{"trace_id", "run_id", "chat_id", "notebook_id", "root_span_id", "agent_name", "started_at_unix_nano", "last_observed_unix_nano", "ended_at_unix_nano", "duration_nanoseconds", "status", "active", "models", "input_tokens", "output_tokens", "total_tokens", "cost_known", "cost_amount", "cost_currency", "cost_source", "attempt_count", "projected_sequence"}
	inserted, err = pool.CopyFrom(ctx, pgx.Identifier{"obs_trace_summaries"}, summaryColumns, pgx.CopyFromSlice(count, func(index int) ([]any, error) {
		sequence := 1
		if index == count-1 {
			sequence = 256
		}
		started := capacityStartedAt(index)
		return []any{capacityTraceID(index), capacityRunID(index), capacityChatID(index), "notebook-capacity", capacityRootSpanID(index), "nano-research-agent", started, started + 1_000_000_000, started + 1_000_000_000, int64(1_000_000_000), "ok", false, []string{"qwen-flash"}, int64(64), int64(32), int64(96), true, 0.001, "USD", "provider_reported", 1, sequence}, nil
	}))
	if err != nil || inserted != int64(count) {
		t.Fatalf("seed obs_trace_summaries inserted=%d: %v", inserted, err)
	}
}

func seedMaximumCapacityDetail(t *testing.T, ctx context.Context, pool *pgxpool.Pool, traceID string, spanCount int) {
	t.Helper()
	columns := []string{"trace_id", "span_id", "parent_span_id", "name", "start_sequence", "end_sequence", "started_at_unix_nano", "ended_at_unix_nano", "duration_nanoseconds", "status", "start_attributes", "end_attributes", "replay_references", "model_analysis"}
	rootSpanID := capacityRootSpanID(sprint5CapacitySummaryCount - 1)
	started := capacityStartedAt(sprint5CapacitySummaryCount - 1)
	inserted, err := pool.CopyFrom(ctx, pgx.Identifier{"obs_spans"}, columns, pgx.CopyFromSlice(spanCount, func(index int) ([]any, error) {
		spanID, parentID, name := fmt.Sprintf("span-cap-%03d", index), rootSpanID, "agent.action"
		if index == 0 {
			spanID, parentID, name = rootSpanID, "", "agent.execution"
		}
		observed := started + int64(index)*1_000_000
		return []any{traceID, spanID, parentID, name, index + 1, index + 1, observed, observed + 1_000_000, int64(1_000_000), "ok", []byte(`[]`), []byte(`[]`), []byte(`[]`), nil}, nil
	}))
	if err != nil || inserted != int64(spanCount) {
		t.Fatalf("seed obs_spans inserted=%d: %v", inserted, err)
	}
}

func seedCapacitySummary(t *testing.T, ctx context.Context, pool *pgxpool.Pool, index int, started int64, sequence int) {
	t.Helper()
	traceID := capacityTraceID(index)
	if _, err := pool.Exec(ctx, `insert into obs_traces(trace_id,run_id,chat_id,notebook_id,root_span_id,agent_name,schema_version,semantic_convention_version,committed_sequence,projected_sequence) values($1,$2,$3,'notebook-capacity',$4,'nano-research-agent',1,1,$5,$5)`, traceID, capacityRunID(index), capacityChatID(index), capacityRootSpanID(index), sequence); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `insert into obs_trace_summaries(trace_id,run_id,chat_id,notebook_id,root_span_id,agent_name,started_at_unix_nano,last_observed_unix_nano,ended_at_unix_nano,duration_nanoseconds,status,active,models,input_tokens,output_tokens,total_tokens,cost_known,cost_amount,cost_currency,cost_source,attempt_count,projected_sequence) values($1,$2,$3,'notebook-capacity',$4,'nano-research-agent',$5,$5,$5,0,'ok',false,array['qwen-flash'],0,0,0,true,0,'USD','provider_reported',1,$6)`, traceID, capacityRunID(index), capacityChatID(index), capacityRootSpanID(index), started, sequence); err != nil {
		t.Fatal(err)
	}
}

func assertCapacityGET(t *testing.T, target string, session *http.Cookie) {
	t.Helper()
	response := capacityGET(t, target, session)
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(response.Body, 4096))
		t.Fatalf("GET %s status=%d body=%s", target, response.StatusCode, body)
	}
	if _, err := io.Copy(io.Discard, response.Body); err != nil {
		t.Fatal(err)
	}
}

func capacityListPage(t *testing.T, target string, session *http.Cookie) struct {
	Data collector.TraceListResult `json:"data"`
} {
	t.Helper()
	response := capacityGET(t, target, session)
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("GET %s status=%d", target, response.StatusCode)
	}
	var page struct {
		Data collector.TraceListResult `json:"data"`
	}
	if err := json.NewDecoder(response.Body).Decode(&page); err != nil {
		t.Fatal(err)
	}
	if len(page.Data.Items) != 50 || page.Data.NextCursor == "" {
		t.Fatalf("capacity page items=%d cursor=%q", len(page.Data.Items), page.Data.NextCursor)
	}
	return page
}

func capacityGET(t *testing.T, target string, session *http.Cookie) *http.Response {
	t.Helper()
	request, err := http.NewRequest(http.MethodGet, target, nil)
	if err != nil {
		t.Fatal(err)
	}
	request.AddCookie(session)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	return response
}

func measureCapacityP95(t *testing.T, samples int, operation func()) time.Duration {
	t.Helper()
	durations := make([]time.Duration, 0, samples)
	for range samples {
		started := time.Now()
		operation()
		durations = append(durations, time.Since(started))
	}
	sort.Slice(durations, func(left, right int) bool { return durations[left] < durations[right] })
	return durations[(samples*95+99)/100-1]
}

func newCapacityChatFixture(t *testing.T, api *testAPI, index int) capacityFixture {
	t.Helper()
	session, csrf := api.registerWithCSRF(t, fmt.Sprintf("sprint5-capacity-%02d@example.com", index))
	notebook := api.postJSONWithCookieAndCSRF(t, "/api/v1/notebooks", map[string]any{
		"title": fmt.Sprintf("Sprint 5 Capacity %02d", index),
	}, session, csrf, csrf.Value, fmt.Sprintf("capacity-notebook-%02d", index))
	if notebook.Code != http.StatusCreated {
		t.Fatalf("create capacity Notebook %d status=%d body=%s", index, notebook.Code, notebook.Body.String())
	}
	var notebookBody struct {
		Notebook struct {
			ID string `json:"id"`
		} `json:"notebook"`
	}
	decodeBody(t, notebook, &notebookBody)
	chat := api.postJSONWithCookieAndCSRF(t, "/api/v1/notebooks/"+notebookBody.Notebook.ID+"/chats", map[string]any{}, session, csrf, csrf.Value, fmt.Sprintf("capacity-chat-%02d", index))
	if chat.Code != http.StatusCreated {
		t.Fatalf("create capacity Chat %d status=%d body=%s", index, chat.Code, chat.Body.String())
	}
	var chatBody struct {
		Chat struct {
			ID string `json:"id"`
		} `json:"chat"`
	}
	decodeBody(t, chat, &chatBody)
	return capacityFixture{session: session, csrf: csrf, chatID: chatBody.Chat.ID}
}

func capacityP95(durations []time.Duration) time.Duration {
	ordered := append([]time.Duration(nil), durations...)
	sort.Slice(ordered, func(left, right int) bool { return ordered[left] < ordered[right] })
	return ordered[(len(ordered)*95+99)/100-1]
}

func waitForCapacityTransportRetry(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	t.Helper()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	timeout := time.NewTimer(time.Second)
	defer timeout.Stop()
	for {
		var ready bool
		if err := pool.QueryRow(ctx, `
			select exists(
				select 1 from agent_trace_refs
				where delivery_state = 'ready'
					and last_error_code = $1
					and next_attempt_at <= now()
			)
		`, agentoutbox.CodeTransportFailure).Scan(&ready); err != nil {
			t.Fatal(err)
		}
		if ready {
			return
		}
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-timeout.C:
			t.Fatal("timed out waiting for the lost-ACK batch to become retryable")
		case <-ticker.C:
		}
	}
}

var capacityReplayClasses = []struct {
	class        replay.Class
	attributeKey string
	payload      string
}{
	{replay.ClassModelRequest, replay.ModelRequestAttachmentKey, `{"schema_version":1,"class":"model_request","model":"qwen-flash","messages":[{"role":"user","content":"capacity"}]}`},
	{replay.ClassModelDecision, replay.ModelDecisionAttachmentKey, `{"schema_version":1,"class":"model_decision","result_kind":"action_proposal"}`},
	{replay.ClassActionInput, replay.ActionInputAttachmentKey, `{"schema_version":1,"class":"action_input","name":"calculate","input":{"operation":"add"}}`},
	{replay.ClassActionResult, replay.ActionResultAttachmentKey, `{"schema_version":1,"class":"action_result","name":"calculate","status":"succeeded","output":{"value":"2"}}`},
}

func capacityTraceID(index int) string    { return fmt.Sprintf("trace-cap-%06d", index) }
func capacityRunID(index int) string      { return fmt.Sprintf("run-cap-%06d", index) }
func capacityChatID(index int) string     { return fmt.Sprintf("chat-cap-%06d", index%10_000) }
func capacityRootSpanID(index int) string { return fmt.Sprintf("root-cap-%06d", index) }
func capacityStartedAt(index int) int64   { return 1_700_000_000_000_000_000 + int64(index)*1_000_000 }
