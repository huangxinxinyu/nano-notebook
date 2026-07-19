package agentoutbox_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/huangxinxinyu/nano-notebook/internal/agentoutbox"
	"github.com/huangxinxinyu/nano-notebook/internal/collector"
)

func TestPurgeSenderPostsAuthenticatedCommandAndAppliesResult(t *testing.T) {
	claimed := purgeClaimFixture()
	store := &purgeSenderStore{claimed: claimed, claimOK: true}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/internal/agent-observability/v1/purges" || r.Header.Get("Authorization") != "Bearer collector-secret" {
			t.Errorf("request path=%s auth=%q", r.URL.Path, r.Header.Get("Authorization"))
		}
		var batch collector.PurgeBatch
		if err := json.NewDecoder(r.Body).Decode(&batch); err != nil {
			t.Fatal(err)
		}
		_ = json.NewEncoder(w).Encode(collector.PurgeBatchResult{
			BatchID: batch.BatchID,
			Commands: []collector.PurgeCommandResult{{
				TraceID: batch.Commands[0].TraceID, Status: collector.PurgeAcknowledged,
			}},
		})
	}))
	defer server.Close()
	sender, err := agentoutbox.NewPurgeSender(store, agentoutbox.SenderConfig{
		PurgeEndpoint: server.URL + "/internal/agent-observability/v1/purges",
		ServiceToken:  "collector-secret", HTTPClient: server.Client(),
	})
	if err != nil {
		t.Fatal(err)
	}
	attempted, err := sender.SendOnce(context.Background())
	if err != nil || !attempted || store.applyCalls != 1 || store.releaseCalls != 0 {
		t.Fatalf("SendOnce attempted=%t err=%v apply=%d release=%d", attempted, err, store.applyCalls, store.releaseCalls)
	}
}

func TestPurgeSenderReleasesClaimAfterTemporaryHTTPFailure(t *testing.T) {
	store := &purgeSenderStore{claimed: purgeClaimFixture(), claimOK: true}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "temporarily unavailable", http.StatusServiceUnavailable)
	}))
	defer server.Close()
	sender, err := agentoutbox.NewPurgeSender(store, agentoutbox.SenderConfig{
		PurgeEndpoint: server.URL, ServiceToken: "collector-secret", HTTPClient: server.Client(),
	})
	if err != nil {
		t.Fatal(err)
	}
	attempted, err := sender.SendOnce(context.Background())
	if err == nil || !attempted || store.releaseCalls != 1 || store.releaseCode != agentoutbox.CodeTransportFailure {
		t.Fatalf("SendOnce attempted=%t err=%v releases=%d code=%q", attempted, err, store.releaseCalls, store.releaseCode)
	}
}

func purgeClaimFixture() agentoutbox.ClaimedPurgeBatch {
	return agentoutbox.ClaimedPurgeBatch{
		LeaseToken: "019bf000-0000-7000-8000-000000000011",
		Batch: collector.PurgeBatch{
			ProtocolVersion: collector.ProtocolVersion, BatchID: "purge-batch-sender", ProducerID: "nano-worker",
			CreatedAt: time.Now().UTC(), Commands: []collector.PurgeCommand{{
				CommandID: "purge/trace-sender", CommandVersion: 1, Kind: collector.CommandPurgeTrace,
				TraceID: "trace-sender", RunID: "run-sender", RequestedAt: time.Now().UTC(),
			}},
		},
	}
}

type purgeSenderStore struct {
	claimed      agentoutbox.ClaimedPurgeBatch
	claimOK      bool
	applyCalls   int
	releaseCalls int
	releaseCode  string
}

func (s *purgeSenderStore) ClaimPurgeBatch(context.Context) (agentoutbox.ClaimedPurgeBatch, bool, error) {
	claimed, ok := s.claimed, s.claimOK
	s.claimOK = false
	return claimed, ok, nil
}

func (s *purgeSenderStore) ApplyPurgeResult(context.Context, agentoutbox.ClaimedPurgeBatch, collector.PurgeBatchResult) error {
	s.applyCalls++
	return nil
}

func (s *purgeSenderStore) ReleasePurgeBatch(_ context.Context, _ agentoutbox.ClaimedPurgeBatch, code string) error {
	s.releaseCalls++
	s.releaseCode = code
	return nil
}
