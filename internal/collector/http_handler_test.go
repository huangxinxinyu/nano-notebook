package collector_test

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/huangxinxinyu/nano-notebook/internal/collector"
)

func TestHTTPHandlerRejectsMissingServiceCredentialBeforeIngest(t *testing.T) {
	store := collector.NewMemoryStore()
	ingestor, err := collector.NewIngestor(collector.IngestorConfig{ProducerID: "nano-worker", Store: store})
	if err != nil {
		t.Fatalf("NewIngestor: %v", err)
	}
	handler, err := collector.NewHTTPHandler(collector.HTTPConfig{
		Ingestor:     ingestor,
		ServiceToken: "collector-secret",
		MaxBodyBytes: 1024 * 1024,
	})
	if err != nil {
		t.Fatalf("NewHTTPHandler: %v", err)
	}
	body, err := json.Marshal(validCollectorBatch(t))
	if err != nil {
		t.Fatalf("Marshal Batch: %v", err)
	}
	request := httptest.NewRequest(http.MethodPost, "/internal/agent-observability/v1/batches", bytes.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	if got := len(store.Records("trace-1")); got != 0 {
		t.Fatalf("unauthorized request stored %d records", got)
	}
}

func TestHTTPHandlerIngestsAuthorizedBatchAndReturnsChunkACK(t *testing.T) {
	store := collector.NewMemoryStore()
	ingestor, err := collector.NewIngestor(collector.IngestorConfig{ProducerID: "nano-worker", Store: store})
	if err != nil {
		t.Fatalf("NewIngestor: %v", err)
	}
	handler, err := collector.NewHTTPHandler(collector.HTTPConfig{
		Ingestor: ingestor, ServiceToken: "collector-secret", MaxBodyBytes: 1024 * 1024,
	})
	if err != nil {
		t.Fatalf("NewHTTPHandler: %v", err)
	}
	body, err := json.Marshal(validCollectorBatch(t))
	if err != nil {
		t.Fatalf("Marshal Batch: %v", err)
	}
	request := httptest.NewRequest(http.MethodPost, "/internal/agent-observability/v1/batches", bytes.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer collector-secret")
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	var result collector.BatchResult
	if err := json.NewDecoder(response.Body).Decode(&result); err != nil {
		t.Fatalf("Decode BatchResult: %v", err)
	}
	if result.BatchID != "batch-1" || len(result.Chunks) != 1 || result.Chunks[0].CommittedThrough != 2 {
		t.Fatalf("BatchResult = %#v", result)
	}
	if got := len(store.Records("trace-1")); got != 2 {
		t.Fatalf("authorized request stored %d records, want 2", got)
	}
}

func TestHTTPHandlerIngestsBoundedGzipBatch(t *testing.T) {
	store := collector.NewMemoryStore()
	ingestor, err := collector.NewIngestor(collector.IngestorConfig{ProducerID: "nano-worker", Store: store})
	if err != nil {
		t.Fatalf("NewIngestor: %v", err)
	}
	handler, err := collector.NewHTTPHandler(collector.HTTPConfig{
		Ingestor: ingestor, ServiceToken: "collector-secret", MaxBodyBytes: 1024 * 1024,
	})
	if err != nil {
		t.Fatalf("NewHTTPHandler: %v", err)
	}
	var body bytes.Buffer
	compressor := gzip.NewWriter(&body)
	if err := json.NewEncoder(compressor).Encode(validCollectorBatch(t)); err != nil {
		t.Fatalf("compress Batch: %v", err)
	}
	if err := compressor.Close(); err != nil {
		t.Fatalf("close compressed Batch: %v", err)
	}
	request := httptest.NewRequest(http.MethodPost, "/internal/agent-observability/v1/batches", &body)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Content-Encoding", "gzip")
	request.Header.Set("Authorization", "Bearer collector-secret")
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	if got := len(store.Records("trace-1")); got != 2 {
		t.Fatalf("gzip request stored %d records, want 2", got)
	}
}

func TestHTTPHandlerExposesCollectorHealthWithoutServiceCredential(t *testing.T) {
	store := collector.NewMemoryStore()
	ingestor, err := collector.NewIngestor(collector.IngestorConfig{ProducerID: "nano-worker", Store: store})
	if err != nil {
		t.Fatalf("NewIngestor: %v", err)
	}
	handler, err := collector.NewHTTPHandler(collector.HTTPConfig{
		Ingestor: ingestor, ServiceToken: "collector-secret", MaxBodyBytes: 1024 * 1024,
	})
	if err != nil {
		t.Fatalf("NewHTTPHandler: %v", err)
	}
	request := httptest.NewRequest(http.MethodGet, "/internal/agent-observability/v1/health", nil)
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	if got := response.Body.String(); !bytes.Contains([]byte(got), []byte(`"status":"ready"`)) || !bytes.Contains([]byte(got), []byte(`"service":"collector"`)) {
		t.Fatalf("health body = %s", got)
	}
}

func TestHTTPHandlerReportsNotReadyWhenReadinessCheckFails(t *testing.T) {
	store := collector.NewMemoryStore()
	ingestor, err := collector.NewIngestor(collector.IngestorConfig{ProducerID: "nano-worker", Store: store})
	if err != nil {
		t.Fatalf("NewIngestor: %v", err)
	}
	handler, err := collector.NewHTTPHandler(collector.HTTPConfig{
		Ingestor: ingestor, ServiceToken: "collector-secret", MaxBodyBytes: 1024 * 1024,
		Readiness: func(context.Context) error { return errors.New("database unavailable") },
	})
	if err != nil {
		t.Fatalf("NewHTTPHandler: %v", err)
	}
	request := httptest.NewRequest(http.MethodGet, "/internal/agent-observability/v1/health", nil)
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	if got := response.Body.String(); !bytes.Contains([]byte(got), []byte(`"status":"not_ready"`)) {
		t.Fatalf("health body = %s", got)
	}
}

func TestHTTPHandlerRejectsOversizedBatchBeforeIngest(t *testing.T) {
	store := collector.NewMemoryStore()
	ingestor, err := collector.NewIngestor(collector.IngestorConfig{ProducerID: "nano-worker", Store: store})
	if err != nil {
		t.Fatalf("NewIngestor: %v", err)
	}
	handler, err := collector.NewHTTPHandler(collector.HTTPConfig{
		Ingestor: ingestor, ServiceToken: "collector-secret", MaxBodyBytes: 64,
	})
	if err != nil {
		t.Fatalf("NewHTTPHandler: %v", err)
	}
	body, err := json.Marshal(validCollectorBatch(t))
	if err != nil {
		t.Fatalf("Marshal Batch: %v", err)
	}
	request := httptest.NewRequest(http.MethodPost, "/internal/agent-observability/v1/batches", bytes.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer collector-secret")
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	if got := len(store.Records("trace-1")); got != 0 {
		t.Fatalf("oversized request stored %d records", got)
	}
}

func TestHTTPHandlerRejectsTrailingJSONBeforeIngest(t *testing.T) {
	store := collector.NewMemoryStore()
	ingestor, err := collector.NewIngestor(collector.IngestorConfig{ProducerID: "nano-worker", Store: store})
	if err != nil {
		t.Fatalf("NewIngestor: %v", err)
	}
	handler, err := collector.NewHTTPHandler(collector.HTTPConfig{
		Ingestor: ingestor, ServiceToken: "collector-secret", MaxBodyBytes: 1024 * 1024,
	})
	if err != nil {
		t.Fatalf("NewHTTPHandler: %v", err)
	}
	body, err := json.Marshal(validCollectorBatch(t))
	if err != nil {
		t.Fatalf("Marshal Batch: %v", err)
	}
	body = append(body, []byte(`{}`)...)
	request := httptest.NewRequest(http.MethodPost, "/internal/agent-observability/v1/batches", bytes.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer collector-secret")
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	if got := len(store.Records("trace-1")); got != 0 {
		t.Fatalf("trailing request stored %d records", got)
	}
}

func TestHTTPHandlerReturnsServiceUnavailableWhenStoreFails(t *testing.T) {
	ingestor, err := collector.NewIngestor(collector.IngestorConfig{
		ProducerID: "nano-worker",
		Store:      unavailableStore{},
	})
	if err != nil {
		t.Fatalf("NewIngestor: %v", err)
	}
	handler, err := collector.NewHTTPHandler(collector.HTTPConfig{
		Ingestor: ingestor, ServiceToken: "collector-secret", MaxBodyBytes: 1024 * 1024,
	})
	if err != nil {
		t.Fatalf("NewHTTPHandler: %v", err)
	}
	body, err := json.Marshal(validCollectorBatch(t))
	if err != nil {
		t.Fatalf("Marshal Batch: %v", err)
	}
	request := httptest.NewRequest(http.MethodPost, "/internal/agent-observability/v1/batches", bytes.NewReader(body))
	request.Header.Set("Authorization", "Bearer collector-secret")
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	if got := response.Body.String(); !bytes.Contains([]byte(got), []byte(`"code":"collector_unavailable"`)) {
		t.Fatalf("body = %s", got)
	}
}

func TestHTTPHandlerAcknowledgesPurgeAndRejectsLateBatch(t *testing.T) {
	store := collector.NewMemoryStore()
	ingestor, err := collector.NewIngestor(collector.IngestorConfig{ProducerID: "nano-worker", Store: store})
	if err != nil {
		t.Fatalf("NewIngestor: %v", err)
	}
	purger, err := collector.NewPurger(collector.PurgerConfig{ProducerID: "nano-worker", Store: store})
	if err != nil {
		t.Fatalf("NewPurger: %v", err)
	}
	handler, err := collector.NewHTTPHandler(collector.HTTPConfig{
		Ingestor: ingestor, Purger: purger, ServiceToken: "collector-secret", MaxBodyBytes: 1024 * 1024,
	})
	if err != nil {
		t.Fatalf("NewHTTPHandler: %v", err)
	}
	purge := collector.PurgeBatch{
		ProtocolVersion: collector.ProtocolVersion, BatchID: "purge-batch-http", ProducerID: "nano-worker",
		CreatedAt: time.Unix(1_700_000_200, 0).UTC(),
		Commands: []collector.PurgeCommand{{
			CommandID: "purge-command-http", CommandVersion: 1, Kind: collector.CommandPurgeTrace,
			TraceID: "trace-1", RunID: "run-1", RequestedAt: time.Unix(1_700_000_100, 0).UTC(),
		}},
	}
	purgeBody, err := json.Marshal(purge)
	if err != nil {
		t.Fatalf("Marshal PurgeBatch: %v", err)
	}
	purgeRequest := httptest.NewRequest(http.MethodPost, "/internal/agent-observability/v1/purges", bytes.NewReader(purgeBody))
	purgeRequest.Header.Set("Authorization", "Bearer collector-secret")
	purgeResponse := httptest.NewRecorder()
	handler.ServeHTTP(purgeResponse, purgeRequest)
	if purgeResponse.Code != http.StatusOK {
		t.Fatalf("purge status = %d, body = %s", purgeResponse.Code, purgeResponse.Body.String())
	}
	var purgeResult collector.PurgeBatchResult
	if err := json.NewDecoder(purgeResponse.Body).Decode(&purgeResult); err != nil {
		t.Fatalf("Decode PurgeBatchResult: %v", err)
	}
	if len(purgeResult.Commands) != 1 || purgeResult.Commands[0].Status != collector.PurgeAcknowledged {
		t.Fatalf("PurgeBatchResult = %#v", purgeResult)
	}

	lateBody, err := json.Marshal(validCollectorBatch(t))
	if err != nil {
		t.Fatalf("Marshal late Batch: %v", err)
	}
	lateRequest := httptest.NewRequest(http.MethodPost, "/internal/agent-observability/v1/batches", bytes.NewReader(lateBody))
	lateRequest.Header.Set("Authorization", "Bearer collector-secret")
	lateResponse := httptest.NewRecorder()
	handler.ServeHTTP(lateResponse, lateRequest)
	var lateResult collector.BatchResult
	if err := json.NewDecoder(lateResponse.Body).Decode(&lateResult); err != nil {
		t.Fatalf("Decode late BatchResult: %v", err)
	}
	if lateResponse.Code != http.StatusOK || lateResult.Chunks[0].Code != collector.CodeTombstoned {
		t.Fatalf("late status/result = %d/%#v", lateResponse.Code, lateResult)
	}
}

type unavailableStore struct{}

func (unavailableStore) CommitTraceChunk(context.Context, collector.TraceChunk) (int, error) {
	return 0, errors.New("database unavailable")
}
