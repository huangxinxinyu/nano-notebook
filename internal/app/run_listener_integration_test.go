package app_test

import (
	"context"
	"testing"
	"time"

	"github.com/huangxinxinyu/nano-notebook/internal/realtime"
)

func TestSharedRunListenerForwardsOnlyTheCommittedRunID(t *testing.T) {
	api := newTestAPI(t)
	notifications := make(chan string, 1)
	listener := realtime.NewRunListener(api.db.Pool(), func(runID string) {
		if runID != "" {
			notifications <- runID
		}
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- listener.Run(ctx) }()

	select {
	case <-listener.Ready():
	case <-time.After(3 * time.Second):
		t.Fatal("Run listener did not become ready")
	}
	if _, err := api.db.Pool().Exec(ctx, `select pg_notify('nano_agent_runs', 'run_committed')`); err != nil {
		t.Fatal(err)
	}
	select {
	case runID := <-notifications:
		if runID != "run_committed" {
			t.Fatalf("notification payload = %q", runID)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Run notification was not forwarded")
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Run listener did not stop")
	}
}
