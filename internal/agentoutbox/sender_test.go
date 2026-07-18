package agentoutbox_test

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/huangxinxinyu/nano-notebook/internal/agentobs"
	"github.com/huangxinxinyu/nano-notebook/internal/agentoutbox"
	"github.com/huangxinxinyu/nano-notebook/internal/collector"
)

func TestSenderPostsAuthenticatedBatchAndAppliesCollectorResult(t *testing.T) {
	claimed := senderClaimFixture(t)
	store := &senderStore{claimed: claimed, claimOK: true}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/internal/agent-observability/v1/batches" {
			t.Errorf("request = %s %s", r.Method, r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer collector-secret" {
			t.Errorf("Authorization = %q", got)
		}
		if got := r.Header.Get("Content-Type"); got != "application/json" {
			t.Errorf("Content-Type = %q", got)
		}
		if got := r.Header.Get("Content-Encoding"); got != "gzip" {
			t.Errorf("Content-Encoding = %q", got)
		}
		body, err := gzip.NewReader(r.Body)
		if err != nil {
			t.Fatalf("open gzip Batch: %v", err)
		}
		defer body.Close()
		var batch collector.Batch
		decoder := json.NewDecoder(body)
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&batch); err != nil {
			t.Errorf("decode Batch: %v", err)
		}
		if batch.BatchID != claimed.Batch.BatchID || batch.ProducerID != claimed.Batch.ProducerID || len(batch.Chunks) != 1 {
			t.Errorf("posted Batch = %#v", batch)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(collector.BatchResult{
			BatchID: batch.BatchID,
			Chunks: []collector.ChunkResult{{
				TraceID: batch.Chunks[0].Trace.TraceID,
				Status:  collector.ChunkCommitted, CommittedThrough: 1,
			}},
		})
	}))
	defer server.Close()

	sender, err := agentoutbox.NewSender(store, agentoutbox.SenderConfig{
		Endpoint:     server.URL + "/internal/agent-observability/v1/batches",
		ServiceToken: "collector-secret",
		HTTPClient:   server.Client(),
	})
	if err != nil {
		t.Fatalf("NewSender: %v", err)
	}
	sent, err := sender.SendOnce(context.Background())
	if err != nil || !sent {
		t.Fatalf("SendOnce sent=%t err=%v", sent, err)
	}
	if store.applyCalls != 1 || store.applied.BatchID != claimed.Batch.BatchID || store.applied.Chunks[0].Status != collector.ChunkCommitted {
		t.Fatalf("applied result = %#v calls=%d", store.applied, store.applyCalls)
	}
}

func TestSenderPrioritizesPurgeCommandsAheadOfOrdinaryTraceDelivery(t *testing.T) {
	claimed := agentoutbox.ClaimedPurgeBatch{
		LeaseToken: "019bf000-0000-7000-8000-000000000011",
		Batch: collector.PurgeBatch{
			ProtocolVersion: collector.ProtocolVersion, BatchID: "purge-batch-sender", ProducerID: "nano-worker",
			CreatedAt: time.Now().UTC(), Commands: []collector.PurgeCommand{{
				CommandID: "purge/trace-sender", CommandVersion: 1, Kind: collector.CommandPurgeTrace,
				TraceID: "trace-sender", RunID: "run-sender", RequestedAt: time.Now().UTC(),
			}},
		},
	}
	store := &purgeSenderStore{purgeClaimed: claimed, purgeOK: true}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/internal/agent-observability/v1/purges" || r.Header.Get("Authorization") != "Bearer collector-secret" {
			t.Errorf("purge request = %s auth=%q", r.URL.Path, r.Header.Get("Authorization"))
		}
		var batch collector.PurgeBatch
		if err := json.NewDecoder(r.Body).Decode(&batch); err != nil {
			t.Fatal(err)
		}
		_ = json.NewEncoder(w).Encode(collector.PurgeBatchResult{
			BatchID:  batch.BatchID,
			Commands: []collector.PurgeCommandResult{{TraceID: batch.Commands[0].TraceID, Status: collector.PurgeAcknowledged}},
		})
	}))
	defer server.Close()
	sender, err := agentoutbox.NewSender(store, agentoutbox.SenderConfig{
		Endpoint:     server.URL + "/internal/agent-observability/v1/batches",
		ServiceToken: "collector-secret", HTTPClient: server.Client(),
	})
	if err != nil {
		t.Fatal(err)
	}
	attempted, err := sender.SendOnce(context.Background())
	if err != nil || !attempted || store.purgeApplyCalls != 1 || store.claimCalls != 0 {
		t.Fatalf("SendOnce attempted=%t err=%v purge_apply=%d ordinary_claims=%d", attempted, err, store.purgeApplyCalls, store.claimCalls)
	}
}

func TestSenderTurnsTemporaryHTTPFailuresIntoRetryableTraceResults(t *testing.T) {
	for _, status := range []int{http.StatusTooManyRequests, http.StatusInternalServerError, http.StatusServiceUnavailable} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			claimed := senderClaimFixture(t)
			store := &senderStore{claimed: claimed, claimOK: true}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				http.Error(w, "temporarily unavailable", status)
			}))
			defer server.Close()
			sender, err := agentoutbox.NewSender(store, agentoutbox.SenderConfig{
				Endpoint: server.URL, ServiceToken: "collector-secret", HTTPClient: server.Client(),
			})
			if err != nil {
				t.Fatalf("NewSender: %v", err)
			}
			attempted, err := sender.SendOnce(context.Background())
			if err == nil || !attempted {
				t.Fatalf("SendOnce attempted=%t err=%v", attempted, err)
			}
			assertSenderRetryResult(t, store, claimed)
		})
	}
}

func TestSenderTurnsTimeoutAndLostACKIntoRetryableTraceResults(t *testing.T) {
	t.Run("timeout", func(t *testing.T) {
		claimed := senderClaimFixture(t)
		store := &senderStore{claimed: claimed, claimOK: true}
		releaseHandler := make(chan struct{})
		server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
			<-releaseHandler
		}))
		defer func() {
			close(releaseHandler)
			server.Close()
		}()
		client := server.Client()
		client.Timeout = 10 * time.Millisecond
		sender, err := agentoutbox.NewSender(store, agentoutbox.SenderConfig{
			Endpoint: server.URL, ServiceToken: "collector-secret", HTTPClient: client,
		})
		if err != nil {
			t.Fatalf("NewSender: %v", err)
		}
		attempted, err := sender.SendOnce(context.Background())
		if err == nil || !attempted {
			t.Fatalf("SendOnce attempted=%t err=%v", attempted, err)
		}
		assertSenderRetryResult(t, store, claimed)
	})

	t.Run("connection closes before ACK", func(t *testing.T) {
		claimed := senderClaimFixture(t)
		store := &senderStore{claimed: claimed, claimOK: true}
		server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
			panic(http.ErrAbortHandler)
		}))
		defer server.Close()
		sender, err := agentoutbox.NewSender(store, agentoutbox.SenderConfig{
			Endpoint: server.URL, ServiceToken: "collector-secret", HTTPClient: server.Client(),
		})
		if err != nil {
			t.Fatalf("NewSender: %v", err)
		}
		attempted, err := sender.SendOnce(context.Background())
		if err == nil || !attempted {
			t.Fatalf("SendOnce attempted=%t err=%v", attempted, err)
		}
		assertSenderRetryResult(t, store, claimed)
	})
}

func TestSenderRejectsResultLargerThanConfiguredLimitAndReleasesClaim(t *testing.T) {
	claimed := senderClaimFixture(t)
	store := &senderStore{claimed: claimed, claimOK: true}
	validResult, err := json.Marshal(collector.BatchResult{
		BatchID: claimed.Batch.BatchID,
		Chunks: []collector.ChunkResult{{
			TraceID: claimed.Batch.Chunks[0].Trace.TraceID,
			Status:  collector.ChunkCommitted, CommittedThrough: 1,
		}},
	})
	if err != nil {
		t.Fatalf("marshal result: %v", err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(validResult)
	}))
	defer server.Close()
	sender, err := agentoutbox.NewSender(store, agentoutbox.SenderConfig{
		Endpoint: server.URL, ServiceToken: "collector-secret", HTTPClient: server.Client(),
		MaxResultBytes: int64(len(validResult) - 1),
	})
	if err != nil {
		t.Fatalf("NewSender: %v", err)
	}
	attempted, err := sender.SendOnce(context.Background())
	if err == nil || !attempted {
		t.Fatalf("SendOnce attempted=%t err=%v", attempted, err)
	}
	if store.applyCalls != 1 || store.applied.Chunks[0].Status != collector.ChunkRetryable {
		t.Fatalf("oversized result release = %#v calls=%d", store.applied, store.applyCalls)
	}
}

func TestSenderRunPollsEmptyStoreAndStopsOnCancellation(t *testing.T) {
	store := &senderStore{}
	sender, err := agentoutbox.NewSender(store, agentoutbox.SenderConfig{
		Endpoint:     "http://127.0.0.1:1/internal/agent-observability/v1/batches",
		ServiceToken: "collector-secret", HTTPClient: &http.Client{},
	})
	if err != nil {
		t.Fatalf("NewSender: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
	defer cancel()
	if err := sender.Run(ctx, 5*time.Millisecond); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if store.claimCalls < 2 {
		t.Fatalf("ClaimBatch calls = %d, want at least 2", store.claimCalls)
	}
}

func TestSenderRunReportsDeliveryErrorsWithoutStopping(t *testing.T) {
	claimed := senderClaimFixture(t)
	store := &senderStore{claimed: claimed, claimOK: true}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "temporarily unavailable", http.StatusServiceUnavailable)
	}))
	defer server.Close()
	errorsReported := make(chan error, 1)
	sender, err := agentoutbox.NewSender(store, agentoutbox.SenderConfig{
		Endpoint: server.URL, ServiceToken: "collector-secret", HTTPClient: server.Client(),
		ReportError: func(err error) {
			select {
			case errorsReported <- err:
			default:
			}
		},
	})
	if err != nil {
		t.Fatalf("NewSender: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- sender.Run(ctx, 5*time.Millisecond) }()
	select {
	case reported := <-errorsReported:
		if !bytes.Contains([]byte(reported.Error()), []byte("HTTP 503")) {
			t.Fatalf("reported error = %v", reported)
		}
	case <-time.After(time.Second):
		t.Fatal("delivery error was not reported")
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Run: %v", err)
	}
}

func TestSenderForceFlushDrainsEveryCurrentlyReadyBatch(t *testing.T) {
	claimed := senderClaimFixture(t)
	store := &senderStore{claimed: claimed, remainingClaims: 2}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var batch collector.Batch
		body, err := gzip.NewReader(r.Body)
		if err != nil {
			t.Fatalf("open gzip Batch: %v", err)
		}
		defer body.Close()
		if err := json.NewDecoder(body).Decode(&batch); err != nil {
			t.Fatalf("decode Batch: %v", err)
		}
		_ = json.NewEncoder(w).Encode(collector.BatchResult{
			BatchID: batch.BatchID,
			Chunks: []collector.ChunkResult{{
				TraceID: batch.Chunks[0].Trace.TraceID,
				Status:  collector.ChunkCommitted, CommittedThrough: 1,
			}},
		})
	}))
	defer server.Close()
	sender, err := agentoutbox.NewSender(store, agentoutbox.SenderConfig{
		Endpoint: server.URL, ServiceToken: "collector-secret", HTTPClient: server.Client(),
	})
	if err != nil {
		t.Fatalf("NewSender: %v", err)
	}
	if err := sender.ForceFlush(context.Background()); err != nil {
		t.Fatalf("ForceFlush: %v", err)
	}
	if store.applyCalls != 2 || store.claimCalls != 3 {
		t.Fatalf("ForceFlush apply/claim calls = %d/%d, want 2/3", store.applyCalls, store.claimCalls)
	}
}

type senderStore struct {
	claimed         agentoutbox.ClaimedBatch
	claimOK         bool
	claimErr        error
	claimCalls      int
	remainingClaims int
	applyCalls      int
	applied         collector.BatchResult
	applyErr        error
}

type purgeSenderStore struct {
	senderStore
	purgeClaimed    agentoutbox.ClaimedPurgeBatch
	purgeOK         bool
	purgeApplyCalls int
	purgeReleases   int
}

func (s *purgeSenderStore) ClaimPurgeBatch(context.Context) (agentoutbox.ClaimedPurgeBatch, bool, error) {
	return s.purgeClaimed, s.purgeOK, nil
}

func (s *purgeSenderStore) ApplyPurgeResult(_ context.Context, _ agentoutbox.ClaimedPurgeBatch, _ collector.PurgeBatchResult) error {
	s.purgeApplyCalls++
	s.purgeOK = false
	return nil
}

func (s *purgeSenderStore) ReleasePurgeBatch(context.Context, agentoutbox.ClaimedPurgeBatch, string) error {
	s.purgeReleases++
	s.purgeOK = false
	return nil
}

func assertSenderRetryResult(t *testing.T, store *senderStore, claimed agentoutbox.ClaimedBatch) {
	t.Helper()
	if store.applyCalls != 1 || len(store.applied.Chunks) != 1 {
		t.Fatalf("retry result = %#v calls=%d", store.applied, store.applyCalls)
	}
	result := store.applied.Chunks[0]
	if result.TraceID != claimed.Batch.Chunks[0].Trace.TraceID || result.Status != collector.ChunkRetryable || result.CommittedThrough != 0 || result.Code != agentoutbox.CodeTransportFailure {
		t.Fatalf("retry Trace result = %#v", result)
	}
}

func (s *senderStore) ClaimBatch(context.Context) (agentoutbox.ClaimedBatch, bool, error) {
	s.claimCalls++
	if s.remainingClaims > 0 {
		return s.claimed, true, s.claimErr
	}
	return s.claimed, s.claimOK, s.claimErr
}

func (s *senderStore) ApplyResult(_ context.Context, _ agentoutbox.ClaimedBatch, result collector.BatchResult) error {
	s.applyCalls++
	s.applied = result
	if s.remainingClaims > 0 {
		s.remainingClaims--
	}
	return s.applyErr
}

func senderClaimFixture(t *testing.T) agentoutbox.ClaimedBatch {
	t.Helper()
	record := agentobs.Record{
		SchemaVersion: 1, SemanticConventionVersion: 1,
		IdentityKey: "sender:root:start", Kind: agentobs.RecordSpanStarted,
		TraceID: "trace-sender", SpanID: "span-root", Name: "agent.execution",
		OccurredAt:     time.Date(2026, 7, 18, 12, 0, 0, 123, time.UTC),
		PayloadVersion: 1, Attributes: []agentobs.Attribute{},
	}
	hash, err := record.CanonicalHash()
	if err != nil {
		t.Fatalf("CanonicalHash: %v", err)
	}
	return agentoutbox.ClaimedBatch{
		LeaseToken: "019bf000-0000-7000-8000-000000000001",
		Batch: collector.Batch{
			ProtocolVersion: collector.ProtocolVersion,
			BatchID:         "019bf000-0000-7000-8000-000000000002", ProducerID: "nano-worker",
			CreatedAt: time.Date(2026, 7, 18, 12, 0, 1, 0, time.UTC),
			Chunks: []collector.TraceChunk{{
				Trace: collector.TraceDescriptor{
					TraceID: record.TraceID, RunID: "run-sender", ChatID: "chat-sender",
					NotebookID: "notebook-sender", RootSpanID: record.SpanID,
					AgentName: "nano-research-agent", SchemaVersion: 1,
					SemanticConventionVersion: 1,
				},
				FirstSequence: 1,
				Records: []collector.SequencedRecord{{
					Sequence: 1, Record: record, CanonicalSHA256: hex.EncodeToString(hash[:]),
				}},
			}},
		},
	}
}
