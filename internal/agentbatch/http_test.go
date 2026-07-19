package agentbatch_test

import (
	"compress/gzip"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/huangxinxinyu/nano-notebook/internal/agentbatch"
	"github.com/huangxinxinyu/nano-notebook/internal/collector"
)

func TestHTTPSenderPostsAuthenticatedGzipBatchAndParsesACK(t *testing.T) {
	want := directWireBatch(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/internal/agent-observability/v2/batches" {
			t.Errorf("request = %s %s", r.Method, r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer collector-secret" {
			t.Errorf("Authorization = %q", got)
		}
		if got := r.Header.Get("Content-Encoding"); got != "gzip" {
			t.Errorf("Content-Encoding = %q", got)
		}
		compressed, err := gzip.NewReader(r.Body)
		if err != nil {
			t.Errorf("gzip reader: %v", err)
			http.Error(w, "bad gzip", http.StatusBadRequest)
			return
		}
		defer compressed.Close()
		decoder := json.NewDecoder(compressed)
		decoder.DisallowUnknownFields()
		var got collector.Batch
		if err := decoder.Decode(&got); err != nil {
			t.Errorf("decode Batch: %v", err)
			http.Error(w, "bad Batch", http.StatusBadRequest)
			return
		}
		if got.BatchID != want.BatchID || got.ProtocolVersion != collector.DirectProtocolVersion || got.Chunks[0].SequenceAuthority != collector.SequenceAuthorityCollector {
			t.Errorf("Batch = %#v", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(committedResult(got))
	}))
	defer server.Close()

	sender, err := agentbatch.NewHTTPSender(agentbatch.HTTPSenderConfig{
		Endpoint:       server.URL + "/internal/agent-observability/v2/batches",
		ServiceToken:   "collector-secret",
		HTTPClient:     server.Client(),
		MaxResultBytes: 1 << 20,
	})
	if err != nil {
		t.Fatalf("NewHTTPSender: %v", err)
	}
	result, err := sender.Send(context.Background(), want)
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if result.BatchID != want.BatchID || len(result.Chunks) != 1 || result.Chunks[0].Status != collector.ChunkCommitted {
		t.Fatalf("result = %#v", result)
	}
}

func TestHTTPSenderClassifiesHTTPFailures(t *testing.T) {
	tests := []struct {
		name      string
		status    int
		retryable bool
	}{
		{name: "rate limited", status: http.StatusTooManyRequests, retryable: true},
		{name: "Collector unavailable", status: http.StatusServiceUnavailable, retryable: true},
		{name: "unauthorized", status: http.StatusUnauthorized, retryable: false},
		{name: "invalid Batch", status: http.StatusBadRequest, retryable: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(test.status)
			}))
			defer server.Close()
			sender, err := agentbatch.NewHTTPSender(agentbatch.HTTPSenderConfig{
				Endpoint: server.URL, ServiceToken: "collector-secret", HTTPClient: server.Client(),
			})
			if err != nil {
				t.Fatal(err)
			}
			_, err = sender.Send(context.Background(), directWireBatch(t))
			if err == nil {
				t.Fatal("Send error = nil")
			}
			if got := agentbatch.Retryable(err); got != test.retryable {
				t.Fatalf("Retryable(%v) = %t, want %t", err, got, test.retryable)
			}
		})
	}
}

func TestHTTPSenderTreatsTransportFailureAsRetryable(t *testing.T) {
	sender, err := agentbatch.NewHTTPSender(agentbatch.HTTPSenderConfig{
		Endpoint: "http://collector.invalid", ServiceToken: "collector-secret",
		HTTPClient: &http.Client{Transport: roundTripperFunc(func(*http.Request) (*http.Response, error) {
			return nil, errors.New("connection reset")
		})},
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = sender.Send(context.Background(), directWireBatch(t))
	if err == nil || !agentbatch.Retryable(err) {
		t.Fatalf("transport error = %v, want retryable", err)
	}
}

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

func directWireBatch(t *testing.T) collector.Batch {
	t.Helper()
	envelope := traceEnvelope("wire-record")
	hash, err := envelope.Record.CanonicalHash()
	if err != nil {
		t.Fatal(err)
	}
	return collector.Batch{
		ProtocolVersion: collector.DirectProtocolVersion,
		BatchID:         "batch-wire", ProducerID: "nano-worker/wire", CreatedAt: time.Now().UTC(),
		Chunks: []collector.TraceChunk{{
			Trace: envelope.Trace, SequenceAuthority: collector.SequenceAuthorityCollector,
			Records: []collector.SequencedRecord{{
				Record: envelope.Record, CanonicalSHA256: hex.EncodeToString(hash[:]),
			}},
		}},
	}
}
