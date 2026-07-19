package agentoutbox_test

import (
	"context"
	"net/http"
	"testing"

	"github.com/huangxinxinyu/nano-notebook/internal/agentoutbox"
	"github.com/huangxinxinyu/nano-notebook/internal/collector"
)

func TestPurgeSenderRequiresNoFullTraceBatchInterface(t *testing.T) {
	store := purgeOnlySenderStore{}
	sender, err := agentoutbox.NewPurgeSender(store, agentoutbox.SenderConfig{
		PurgeEndpoint: "http://127.0.0.1:1/internal/agent-observability/v1/purges",
		ServiceToken:  "collector-secret",
		HTTPClient:    &http.Client{},
	})
	if err != nil {
		t.Fatalf("NewPurgeSender: %v", err)
	}
	if attempted, err := sender.SendOnce(context.Background()); err != nil || attempted {
		t.Fatalf("SendOnce attempted=%t err=%v", attempted, err)
	}
}

type purgeOnlySenderStore struct{}

func (purgeOnlySenderStore) ClaimPurgeBatch(context.Context) (agentoutbox.ClaimedPurgeBatch, bool, error) {
	return agentoutbox.ClaimedPurgeBatch{}, false, nil
}

func (purgeOnlySenderStore) ApplyPurgeResult(context.Context, agentoutbox.ClaimedPurgeBatch, collector.PurgeBatchResult) error {
	return nil
}

func (purgeOnlySenderStore) ReleasePurgeBatch(context.Context, agentoutbox.ClaimedPurgeBatch, string) error {
	return nil
}
