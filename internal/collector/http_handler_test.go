package collector_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

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
