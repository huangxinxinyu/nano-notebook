package agent_test

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/huangxinxinyu/nano-notebook/internal/agent"
	"github.com/huangxinxinyu/nano-notebook/internal/agentbatch"
	"github.com/huangxinxinyu/nano-notebook/internal/agentobs"
	"github.com/huangxinxinyu/nano-notebook/internal/collector"
)

func TestDeterministicSpanIDIsStableAcrossProcessInstances(t *testing.T) {
	first, err := agent.DeterministicSpanID("trace-stable", "run/run-1/attempt/2")
	if err != nil {
		t.Fatal(err)
	}
	second, err := agent.DeterministicSpanID("trace-stable", "run/run-1/attempt/2")
	if err != nil {
		t.Fatal(err)
	}
	other, err := agent.DeterministicSpanID("trace-stable", "run/run-1/attempt/3")
	if err != nil {
		t.Fatal(err)
	}
	if first == "" || first != second || first == other {
		t.Fatalf("deterministic Span IDs = %q, %q, %q", first, second, other)
	}
}

func TestTraceTransactionRollbackPublishesNothing(t *testing.T) {
	sink := &recordingTraceSink{}
	buffer := newTraceTransaction(t, sink)
	if err := buffer.Record(context.Background(), transactionRecord("record-rollback")); err != nil {
		t.Fatal(err)
	}
	buffer.Rollback()
	result := buffer.PublishAfterCommit(context.Background())
	if len(sink.envelopes) != 0 || result.Attempted != 0 || result.Failed != 0 {
		t.Fatalf("rollback publish = %#v, envelopes = %#v", result, sink.envelopes)
	}
}

func TestTraceTransactionPublishesBufferedIdentitiesOnlyAfterCommit(t *testing.T) {
	sink := &recordingTraceSink{}
	buffer := newTraceTransaction(t, sink)
	ctx := agent.ContextWithTraceTransaction(context.Background(), buffer)
	if got, ok := agent.TraceTransactionFromContext(ctx); !ok || got != buffer {
		t.Fatalf("context Trace transaction = %#v, %t", got, ok)
	}
	for _, identity := range []string{"record-1", "record-2"} {
		if err := buffer.Record(ctx, transactionRecord(identity)); err != nil {
			t.Fatal(err)
		}
	}
	if len(sink.envelopes) != 0 {
		t.Fatalf("published before product commit: %#v", sink.envelopes)
	}
	result := buffer.PublishAfterCommit(ctx)
	if result.Attempted != 2 || result.Accepted != 2 || result.Failed != 0 {
		t.Fatalf("publish result = %#v", result)
	}
	identities := []string{sink.envelopes[0].Record.IdentityKey, sink.envelopes[1].Record.IdentityKey}
	if !reflect.DeepEqual(identities, []string{"record-1", "record-2"}) {
		t.Fatalf("published identities = %#v", identities)
	}
}

func TestTraceTransactionPublishFailureCannotUndoCommittedProductResult(t *testing.T) {
	sink := &recordingTraceSink{failIdentity: "record-1"}
	buffer := newTraceTransaction(t, sink)
	if err := buffer.Record(context.Background(), transactionRecord("record-1")); err != nil {
		t.Fatal(err)
	}
	productResult := "committed"
	publish := buffer.PublishAfterCommit(context.Background())
	if productResult != "committed" {
		t.Fatalf("product result = %q", productResult)
	}
	if publish.Attempted != 1 || publish.Accepted != 0 || publish.Failed != 1 {
		t.Fatalf("publish result = %#v", publish)
	}
	if len(publish.Errors) != 1 || !errors.Is(publish.Errors[0], agentbatch.ErrQueueFull) {
		t.Fatalf("publish errors = %#v", publish.Errors)
	}
}

func TestTraceScopePublishesMultipleTraceBuffersOnlyAfterProductCommit(t *testing.T) {
	sink := &recordingTraceSink{}
	scope, err := agent.NewTraceScope(sink)
	if err != nil {
		t.Fatal(err)
	}
	ctx := agent.ContextWithTraceScope(context.Background(), scope)
	first := newTraceTransaction(t, sink)
	secondDescriptor := collector.TraceDescriptor{
		TraceID: "trace-second", RunID: "run-second", ChatID: "chat-second", NotebookID: "notebook-second",
		RootSpanID: "root-second", AgentName: "nano-research-agent", SchemaVersion: 1, SemanticConventionVersion: 1,
	}
	first, err = scope.Transaction(first.Descriptor())
	if err != nil {
		t.Fatal(err)
	}
	second, err := scope.Transaction(secondDescriptor)
	if err != nil {
		t.Fatal(err)
	}
	if got, ok := agent.TraceScopeFromContext(ctx); !ok || got != scope {
		t.Fatalf("context Trace scope = %#v, %t", got, ok)
	}
	if err := first.Record(ctx, transactionRecord("first-record")); err != nil {
		t.Fatal(err)
	}
	secondRecord := transactionRecord("second-record")
	secondRecord.TraceID = secondDescriptor.TraceID
	secondRecord.SpanID = secondDescriptor.RootSpanID
	if err := second.Record(ctx, secondRecord); err != nil {
		t.Fatal(err)
	}
	if got := scope.PublishAfterCommit(ctx); got.Attempted != 2 || got.Accepted != 2 {
		t.Fatalf("scope publish = %#v", got)
	}
}

type recordingTraceSink struct {
	envelopes    []agentbatch.Envelope
	failIdentity string
}

func (s *recordingTraceSink) Offer(_ context.Context, envelope agentbatch.Envelope) error {
	s.envelopes = append(s.envelopes, envelope)
	if envelope.Record.IdentityKey == s.failIdentity {
		return agentbatch.ErrQueueFull
	}
	return nil
}

func newTraceTransaction(t *testing.T, sink agent.TraceSink) *agent.TraceTransaction {
	t.Helper()
	buffer, err := agent.NewTraceTransaction(collector.TraceDescriptor{
		TraceID: "trace-transaction", RunID: "run-transaction", ChatID: "chat-transaction",
		NotebookID: "notebook-transaction", RootSpanID: "root-transaction", AgentName: "nano-research-agent",
		SchemaVersion: 1, SemanticConventionVersion: 1,
	}, sink)
	if err != nil {
		t.Fatal(err)
	}
	return buffer
}

func transactionRecord(identity string) agentobs.Record {
	return agentobs.Record{
		SchemaVersion: 1, SemanticConventionVersion: 1, PayloadVersion: 1,
		IdentityKey: identity, Kind: agentobs.RecordEvent, TraceID: "trace-transaction",
		SpanID: "root-transaction", Name: "nano.transaction.event",
		OccurredAt: time.Unix(1_700_400_000, 0).UTC(),
	}
}
