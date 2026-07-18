package collector_test

import (
	"context"
	"testing"
	"time"

	"github.com/huangxinxinyu/nano-notebook/internal/collector"
)

func TestPurgerIsIdempotentAndPreventsLateTraceResurrection(t *testing.T) {
	store := collector.NewMemoryStore()
	purger, err := collector.NewPurger(collector.PurgerConfig{ProducerID: "nano-worker", Store: store})
	if err != nil {
		t.Fatalf("NewPurger: %v", err)
	}
	batch := collector.PurgeBatch{
		ProtocolVersion: collector.ProtocolVersion,
		BatchID:         "purge-batch-1",
		ProducerID:      "nano-worker",
		CreatedAt:       time.Unix(1_700_000_200, 0).UTC(),
		Commands: []collector.PurgeCommand{{
			CommandID: "purge-command-1", CommandVersion: 1, Kind: collector.CommandPurgeTrace,
			TraceID: "trace-1", RunID: "run-1", RequestedAt: time.Unix(1_700_000_100, 0).UTC(),
		}},
	}
	for attempt := 1; attempt <= 2; attempt++ {
		result, purgeErr := purger.Purge(context.Background(), batch)
		if purgeErr != nil {
			t.Fatalf("Purge attempt %d: %v", attempt, purgeErr)
		}
		if len(result.Commands) != 1 || result.Commands[0].Status != collector.PurgeAcknowledged || result.Commands[0].TraceID != "trace-1" {
			t.Fatalf("Purge attempt %d result = %#v", attempt, result)
		}
	}

	ingestor, err := collector.NewIngestor(collector.IngestorConfig{ProducerID: "nano-worker", Store: store})
	if err != nil {
		t.Fatalf("NewIngestor: %v", err)
	}
	ingestResult, err := ingestor.Ingest(context.Background(), validCollectorBatch(t))
	if err != nil {
		t.Fatalf("late Ingest transport error: %v", err)
	}
	if got := ingestResult.Chunks[0]; got.Status != collector.ChunkRejected || got.Code != collector.CodeTombstoned {
		t.Fatalf("late Ingest result = %#v", got)
	}
	if got := len(store.Records("trace-1")); got != 0 {
		t.Fatalf("late Ingest resurrected %d records", got)
	}
}
