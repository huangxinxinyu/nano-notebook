package agentbatch_test

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/huangxinxinyu/nano-notebook/internal/agentbatch"
	"github.com/huangxinxinyu/nano-notebook/internal/agentobs"
	"github.com/huangxinxinyu/nano-notebook/internal/collector"
)

func TestExporterCountsInflightRecordsAgainstPendingBound(t *testing.T) {
	sender := &blockingSender{
		batches: make(chan collector.Batch, 1),
		results: make(chan collector.BatchResult, 1),
	}
	exporter, err := agentbatch.NewExporter(agentbatch.Config{
		ProducerID:        "nano-worker/test",
		Sender:            sender,
		MaxPendingRecords: 2,
		MaxPendingBytes:   1 << 20,
		MaxBatchRecords:   2,
		MaxBatchBytes:     1 << 20,
		MaxDelay:          time.Hour,
	})
	if err != nil {
		t.Fatalf("NewExporter: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = exporter.Shutdown(ctx)
	})

	if err := exporter.Offer(context.Background(), traceEnvelope("record-1")); err != nil {
		t.Fatalf("Offer first: %v", err)
	}
	if err := exporter.Offer(context.Background(), traceEnvelope("record-2")); err != nil {
		t.Fatalf("Offer second: %v", err)
	}
	batch := receiveBatch(t, sender.batches)
	if got := batchRecordCount(batch); got != 2 {
		t.Fatalf("inflight records = %d, want 2", got)
	}
	if err := exporter.Offer(context.Background(), traceEnvelope("record-3")); !errors.Is(err, agentbatch.ErrQueueFull) {
		t.Fatalf("Offer beyond bound error = %v, want ErrQueueFull", err)
	}
	stats := exporter.Stats()
	if stats.PendingRecords != 2 || stats.InflightRecords != 2 || stats.DroppedRecords != 1 {
		t.Fatalf("queue Stats = %#v", stats)
	}

	sender.results <- committedResult(batch)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := exporter.ForceFlush(ctx); err != nil {
		t.Fatalf("ForceFlush: %v", err)
	}
}

func TestExporterRetriesTheSameBatchAfterUncertainTransportFailure(t *testing.T) {
	sender := &retrySender{batches: make(chan collector.Batch, 2)}
	exporter, err := agentbatch.NewExporter(agentbatch.Config{
		ProducerID:        "nano-worker/retry",
		Sender:            sender,
		MaxPendingRecords: 4,
		MaxPendingBytes:   1 << 20,
		MaxBatchRecords:   1,
		MaxBatchBytes:     1 << 20,
		MaxDelay:          time.Hour,
	})
	if err != nil {
		t.Fatalf("NewExporter: %v", err)
	}
	if err := exporter.Offer(context.Background(), traceEnvelope("retry-record")); err != nil {
		t.Fatalf("Offer: %v", err)
	}
	first := receiveBatch(t, sender.batches)
	second := receiveBatch(t, sender.batches)
	if second.BatchID != first.BatchID {
		t.Fatalf("retry Batch ID = %q, want %q", second.BatchID, first.BatchID)
	}
	if second.Chunks[0].Records[0].Record.IdentityKey != first.Chunks[0].Records[0].Record.IdentityKey {
		t.Fatalf("retry record changed: %#v / %#v", first, second)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := exporter.ForceFlush(ctx); err != nil {
		t.Fatalf("ForceFlush: %v", err)
	}
	if err := exporter.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
}

func TestExporterFlushesOnEncodedByteThreshold(t *testing.T) {
	sender := &immediateSender{batches: make(chan collector.Batch, 1)}
	first := traceEnvelope("byte-record-1")
	second := traceEnvelope("byte-record-2")
	firstJSON, _ := json.Marshal(first)
	secondJSON, _ := json.Marshal(second)
	threshold := len(firstJSON) + len(secondJSON)
	exporter, err := agentbatch.NewExporter(agentbatch.Config{
		ProducerID: "nano-worker/bytes", Sender: sender,
		MaxPendingRecords: 10, MaxPendingBytes: threshold * 2,
		MaxBatchRecords: 10, MaxBatchBytes: threshold, MaxDelay: time.Hour,
	})
	if err != nil {
		t.Fatalf("NewExporter: %v", err)
	}
	if err := exporter.Offer(context.Background(), first); err != nil {
		t.Fatal(err)
	}
	select {
	case batch := <-sender.batches:
		t.Fatalf("flushed before byte threshold: %#v", batch)
	case <-time.After(20 * time.Millisecond):
	}
	if err := exporter.Offer(context.Background(), second); err != nil {
		t.Fatal(err)
	}
	if got := batchRecordCount(receiveBatch(t, sender.batches)); got != 2 {
		t.Fatalf("byte-threshold Batch records = %d, want 2", got)
	}
	shutdownExporter(t, exporter)
}

func TestExporterFlushesOnMaximumDelayAndGroupsTraces(t *testing.T) {
	sender := &immediateSender{batches: make(chan collector.Batch, 1)}
	exporter, err := agentbatch.NewExporter(agentbatch.Config{
		ProducerID: "nano-control-plane/delay", Sender: sender,
		MaxPendingRecords: 10, MaxPendingBytes: 1 << 20,
		MaxBatchRecords: 10, MaxBatchBytes: 1 << 20, MaxDelay: 20 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("NewExporter: %v", err)
	}
	first := traceEnvelope("delay-record-1")
	second := traceEnvelope("delay-record-2")
	second.Trace.TraceID = "trace-batch-second"
	second.Trace.RunID = "run-batch-second"
	second.Trace.ChatID = "chat-batch-second"
	second.Trace.NotebookID = "notebook-batch-second"
	second.Trace.RootSpanID = "root-batch-second"
	second.Record.TraceID = second.Trace.TraceID
	second.Record.SpanID = second.Trace.RootSpanID
	if err := exporter.Offer(context.Background(), first); err != nil {
		t.Fatal(err)
	}
	if err := exporter.Offer(context.Background(), second); err != nil {
		t.Fatal(err)
	}
	batch := receiveBatch(t, sender.batches)
	if len(batch.Chunks) != 2 || batchRecordCount(batch) != 2 {
		t.Fatalf("delayed multi-Trace Batch = %#v", batch)
	}
	shutdownExporter(t, exporter)
}

func TestExporterDropsPermanentlyRejectedBatchAndReportsFlushError(t *testing.T) {
	sender := &rejectedSender{batches: make(chan collector.Batch, 1)}
	exporter, err := agentbatch.NewExporter(agentbatch.Config{
		ProducerID: "nano-worker/rejected", Sender: sender,
		MaxPendingRecords: 1, MaxPendingBytes: 1 << 20,
		MaxBatchRecords: 1, MaxBatchBytes: 1 << 20, MaxDelay: time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := exporter.Offer(context.Background(), traceEnvelope("rejected-record")); err != nil {
		t.Fatal(err)
	}
	receiveBatch(t, sender.batches)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := exporter.ForceFlush(ctx); err == nil || agentbatch.Retryable(err) {
		t.Fatalf("ForceFlush error = %v, want permanent rejection", err)
	}
	stats := exporter.Stats()
	if stats.PendingRecords != 0 || stats.DroppedRecords != 1 {
		t.Fatalf("Stats after permanent rejection = %#v", stats)
	}
	if err := exporter.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
}

func TestExporterShutdownDeadlineCancelsBlockingSend(t *testing.T) {
	sender := &contextBlockingSender{started: make(chan struct{})}
	exporter, err := agentbatch.NewExporter(agentbatch.Config{
		ProducerID: "nano-worker/shutdown", Sender: sender,
		MaxPendingRecords: 1, MaxPendingBytes: 1 << 20,
		MaxBatchRecords: 1, MaxBatchBytes: 1 << 20, MaxDelay: time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := exporter.Offer(context.Background(), traceEnvelope("shutdown-record")); err != nil {
		t.Fatal(err)
	}
	select {
	case <-sender.started:
	case <-time.After(time.Second):
		t.Fatal("Sender did not start")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := exporter.Shutdown(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Shutdown error = %v, want deadline exceeded", err)
	}
	select {
	case <-exporter.Done():
	case <-time.After(time.Second):
		t.Fatal("exporter goroutine did not stop after Shutdown deadline")
	}
}

type blockingSender struct {
	batches chan collector.Batch
	results chan collector.BatchResult
}

type retrySender struct {
	mu       sync.Mutex
	attempts int
	batches  chan collector.Batch
}

type immediateSender struct {
	batches chan collector.Batch
}

type rejectedSender struct {
	batches chan collector.Batch
}

func (s *rejectedSender) Send(_ context.Context, batch collector.Batch) (collector.BatchResult, error) {
	s.batches <- batch
	return collector.BatchResult{BatchID: batch.BatchID, Chunks: []collector.ChunkResult{{
		TraceID: batch.Chunks[0].Trace.TraceID, Status: collector.ChunkRejected, Code: collector.CodeInvalidChunk,
	}}}, nil
}

type contextBlockingSender struct {
	started chan struct{}
	once    sync.Once
}

func (s *contextBlockingSender) Send(ctx context.Context, _ collector.Batch) (collector.BatchResult, error) {
	s.once.Do(func() { close(s.started) })
	<-ctx.Done()
	return collector.BatchResult{}, ctx.Err()
}

func (s *immediateSender) Send(_ context.Context, batch collector.Batch) (collector.BatchResult, error) {
	s.batches <- batch
	return committedResult(batch), nil
}

func (s *retrySender) Send(_ context.Context, batch collector.Batch) (collector.BatchResult, error) {
	s.batches <- batch
	s.mu.Lock()
	s.attempts++
	attempt := s.attempts
	s.mu.Unlock()
	if attempt == 1 {
		return collector.BatchResult{}, errors.New("response lost after commit")
	}
	return committedResult(batch), nil
}

func (s *blockingSender) Send(ctx context.Context, batch collector.Batch) (collector.BatchResult, error) {
	select {
	case s.batches <- batch:
	case <-ctx.Done():
		return collector.BatchResult{}, ctx.Err()
	}
	select {
	case result := <-s.results:
		return result, nil
	case <-ctx.Done():
		return collector.BatchResult{}, ctx.Err()
	}
}

func traceEnvelope(identity string) agentbatch.Envelope {
	traceID := agentobs.TraceID("trace-batch")
	rootID := agentobs.SpanID("root-batch")
	return agentbatch.Envelope{
		Trace: collector.TraceDescriptor{
			TraceID: traceID, RunID: "run-batch", ChatID: "chat-batch", NotebookID: "notebook-batch",
			RootSpanID: rootID, AgentName: "nano-research-agent", SchemaVersion: 1, SemanticConventionVersion: 1,
		},
		Record: agentobs.Record{
			SchemaVersion: 1, SemanticConventionVersion: 1, IdentityKey: identity,
			Kind: agentobs.RecordEvent, TraceID: traceID, SpanID: rootID,
			Name: "nano.batch.event", OccurredAt: time.Unix(1_700_300_000, 0).UTC(), PayloadVersion: 1,
		},
	}
}

func receiveBatch(t *testing.T, batches <-chan collector.Batch) collector.Batch {
	t.Helper()
	select {
	case batch := <-batches:
		return batch
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for Batch")
		return collector.Batch{}
	}
}

func batchRecordCount(batch collector.Batch) int {
	total := 0
	for _, chunk := range batch.Chunks {
		total += len(chunk.Records)
	}
	return total
}

func committedResult(batch collector.Batch) collector.BatchResult {
	result := collector.BatchResult{BatchID: batch.BatchID, Chunks: make([]collector.ChunkResult, 0, len(batch.Chunks))}
	for _, chunk := range batch.Chunks {
		result.Chunks = append(result.Chunks, collector.ChunkResult{
			TraceID: chunk.Trace.TraceID, Status: collector.ChunkCommitted,
			CommittedThrough: len(chunk.Records),
		})
	}
	return result
}

func shutdownExporter(t *testing.T, exporter *agentbatch.Exporter) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := exporter.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
}
