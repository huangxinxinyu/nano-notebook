package collector_test

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/huangxinxinyu/nano-notebook/internal/collector"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestTraceQueryFiltersAndCursorPagingRemainStable(t *testing.T) {
	ctx := context.Background()
	pool := openObservabilityTestPool(t, ctx)
	t.Cleanup(pool.Close)
	resetObservabilityTestSchema(t, ctx, pool)
	seedQuerySummary(t, ctx, pool, "trace-query-3", "run-search-3", "chat-search", "agent-a", "model-a", "ok", false, 300)
	seedQuerySummary(t, ctx, pool, "trace-query-2", "run-search-2", "chat-search", "agent-a", "model-a", "error", false, 200)
	seedQuerySummary(t, ctx, pool, "trace-query-1", "run-other", "chat-other", "agent-b", "model-b", "", true, 100)

	queries, err := collector.NewTraceQueryStore(pool, nil)
	if err != nil {
		t.Fatalf("NewTraceQueryStore: %v", err)
	}
	terminal := false
	first, err := queries.List(ctx, collector.TraceListQuery{
		IdentityPrefix: "run-search", AgentName: "agent-a", ModelName: "model-a",
		Active: &terminal, PageSize: 1,
	})
	if err != nil {
		t.Fatalf("List first: %v", err)
	}
	if len(first.Items) != 1 || first.Items[0].Summary.TraceID != "trace-query-3" || first.NextCursor == "" {
		t.Fatalf("first page = %#v", first)
	}
	seedQuerySummary(t, ctx, pool, "trace-query-4", "run-search-4", "chat-search", "agent-a", "model-a", "ok", false, 400)
	second, err := queries.List(ctx, collector.TraceListQuery{
		IdentityPrefix: "run-search", AgentName: "agent-a", ModelName: "model-a",
		Active: &terminal, PageSize: 1, Cursor: first.NextCursor,
	})
	if err != nil {
		t.Fatalf("List second: %v", err)
	}
	if len(second.Items) != 1 || second.Items[0].Summary.TraceID != "trace-query-2" {
		t.Fatalf("second page = %#v", second)
	}
	if _, err := queries.List(ctx, collector.TraceListQuery{PageSize: 101}); err == nil {
		t.Fatal("List accepted oversized page")
	}
	if _, err := queries.List(ctx, collector.TraceListQuery{Cursor: "not-a-cursor"}); err == nil {
		t.Fatal("List accepted malformed cursor")
	}
}

func TestCollectorQueryHTTPUsesControlPlaneCredentialNotProducerCredential(t *testing.T) {
	ctx := context.Background()
	pool := openObservabilityTestPool(t, ctx)
	t.Cleanup(pool.Close)
	resetObservabilityTestSchema(t, ctx, pool)
	seedQuerySummary(t, ctx, pool, "trace-query-http", "run-query-http", "chat-query-http", "agent-a", "model-a", "ok", false, 300)
	queries, _ := collector.NewTraceQueryStore(pool, nil)
	ingestor, _ := collector.NewIngestor(collector.IngestorConfig{ProducerID: "nano-worker", Store: collector.NewMemoryStore()})
	handler, err := collector.NewHTTPHandler(collector.HTTPConfig{
		Ingestor: ingestor, ServiceToken: "producer-secret", QueryStore: queries,
		QueryToken: "control-plane-secret", MaxBodyBytes: 1024,
	})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodGet, "/internal/agent-observability/v1/traces?page_size=10", nil)
	request.Header.Set("Authorization", "Bearer producer-secret")
	denied := httptest.NewRecorder()
	handler.ServeHTTP(denied, request)
	if denied.Code != http.StatusUnauthorized {
		t.Fatalf("producer credential query status=%d body=%s", denied.Code, denied.Body.String())
	}
	request = httptest.NewRequest(http.MethodGet, "/internal/agent-observability/v1/traces?page_size=10", nil)
	request.Header.Set("Authorization", "Bearer control-plane-secret")
	allowed := httptest.NewRecorder()
	handler.ServeHTTP(allowed, request)
	if allowed.Code != http.StatusOK || !bytes.Contains(allowed.Body.Bytes(), []byte(`"trace_id":"trace-query-http"`)) ||
		bytes.Contains(allowed.Body.Bytes(), []byte("canonical_payload")) {
		t.Fatalf("Control Plane query status=%d body=%s", allowed.Code, allowed.Body.String())
	}
}

func TestCollectorTraceDetailHTTPEncodesEmptyCollectionsAsArrays(t *testing.T) {
	ctx := context.Background()
	pool := openObservabilityTestPool(t, ctx)
	t.Cleanup(pool.Close)
	resetObservabilityTestSchema(t, ctx, pool)
	seedQuerySummary(t, ctx, pool, "trace-empty-collections", "run-empty-collections", "chat-empty-collections", "agent-a", "model-a", "ok", false, 300)
	queries, _ := collector.NewTraceQueryStore(pool, nil)
	ingestor, _ := collector.NewIngestor(collector.IngestorConfig{ProducerID: "nano-worker", Store: collector.NewMemoryStore()})
	handler, err := collector.NewHTTPHandler(collector.HTTPConfig{
		Ingestor: ingestor, ServiceToken: "producer-secret", QueryStore: queries,
		QueryToken: "control-plane-secret", MaxBodyBytes: 1024,
	})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodGet, "/internal/agent-observability/v1/traces/trace-empty-collections", nil)
	request.Header.Set("Authorization", "Bearer control-plane-secret")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("Trace Detail status=%d body=%s", response.Code, response.Body.String())
	}
	for _, field := range []string{`"spans":[]`, `"events":[]`, `"links":[]`} {
		if !bytes.Contains(response.Body.Bytes(), []byte(field)) {
			t.Errorf("Trace Detail omitted empty array %s: %s", field, response.Body.String())
		}
	}
}

func seedQuerySummary(t *testing.T, ctx context.Context, pool *pgxpool.Pool, traceID, runID, chatID, agentName, modelName, status string, active bool, started int64) {
	t.Helper()
	if _, err := pool.Exec(ctx, `
		insert into obs_traces(trace_id, workload_kind, workload_id, run_id, chat_id, notebook_id, root_span_id, agent_name, schema_version, semantic_convention_version, committed_sequence, projected_sequence)
		values ($1,'agent_run',$2,$2,$3,'notebook-query',$4,$5,1,1,1,1)
	`, traceID, runID, chatID, "root-"+traceID, agentName); err != nil {
		t.Fatalf("seed Trace: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		insert into obs_trace_summaries(trace_id, workload_kind, workload_id, run_id, chat_id, notebook_id, root_span_id, agent_name, started_at_unix_nano, last_observed_unix_nano, status, active, models, projected_sequence)
		values ($1,'agent_run',$2,$2,$3,'notebook-query',$4,$5,$6,$6,$7,$8,array[$9],1)
	`, traceID, runID, chatID, "root-"+traceID, agentName, started, status, active, modelName); err != nil {
		t.Fatalf("seed Summary: %v", err)
	}
}
