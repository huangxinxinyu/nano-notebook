package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"sync"

	"github.com/huangxinxinyu/nano-notebook/internal/agentbatch"
	"github.com/huangxinxinyu/nano-notebook/internal/agentobs"
	"github.com/huangxinxinyu/nano-notebook/internal/collector"
)

var ErrTraceTransactionClosed = errors.New("Agent Trace transaction is closed")

type TraceSink interface {
	Offer(context.Context, agentbatch.Envelope) error
}

type TracePublishResult struct {
	Attempted int
	Accepted  int
	Failed    int
	Errors    []error
}

type TraceTransaction struct {
	descriptor collector.TraceDescriptor
	sink       TraceSink

	mu        sync.Mutex
	records   []agentobs.Record
	closed    bool
	rollback  bool
	published bool
}

type traceTransactionContextKey struct{}

func DeterministicSpanID(traceID agentobs.TraceID, semanticIdentity string) (agentobs.SpanID, error) {
	semanticIdentity = strings.TrimSpace(semanticIdentity)
	if strings.TrimSpace(string(traceID)) == "" || semanticIdentity == "" {
		return "", errors.New("deterministic Span identity is incomplete")
	}
	digest := sha256.Sum256([]byte(string(traceID) + "\x00" + semanticIdentity))
	return agentobs.SpanID(hex.EncodeToString(digest[:16])), nil
}

func NewTraceTransaction(descriptor collector.TraceDescriptor, sink TraceSink) (*TraceTransaction, error) {
	if sink == nil || strings.TrimSpace(string(descriptor.TraceID)) == "" ||
		strings.TrimSpace(descriptor.RunID) == "" || strings.TrimSpace(descriptor.ChatID) == "" ||
		strings.TrimSpace(descriptor.NotebookID) == "" || strings.TrimSpace(string(descriptor.RootSpanID)) == "" ||
		strings.TrimSpace(descriptor.AgentName) == "" || descriptor.SchemaVersion < 1 ||
		descriptor.SemanticConventionVersion < 1 {
		return nil, errors.New("Agent Trace transaction dependencies are incomplete")
	}
	return &TraceTransaction{descriptor: descriptor, sink: sink}, nil
}

func ContextWithTraceTransaction(ctx context.Context, transaction *TraceTransaction) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if transaction == nil {
		return ctx
	}
	return context.WithValue(ctx, traceTransactionContextKey{}, transaction)
}

func TraceTransactionFromContext(ctx context.Context) (*TraceTransaction, bool) {
	if ctx == nil {
		return nil, false
	}
	transaction, ok := ctx.Value(traceTransactionContextKey{}).(*TraceTransaction)
	return transaction, ok && transaction != nil
}

func (t *TraceTransaction) Record(_ context.Context, record agentobs.Record) error {
	if t == nil || t.sink == nil {
		return errors.New("nil Agent Trace transaction")
	}
	record = normalizeTraceRecord(record)
	if err := record.Validate(); err != nil {
		return err
	}
	if record.TraceID != t.descriptor.TraceID || record.SchemaVersion != t.descriptor.SchemaVersion ||
		record.SemanticConventionVersion != t.descriptor.SemanticConventionVersion {
		return errors.New("Agent Trace record changed its transaction envelope")
	}
	record.Attributes = append([]agentobs.Attribute(nil), record.Attributes...)
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed {
		return ErrTraceTransactionClosed
	}
	t.records = append(t.records, record)
	return nil
}

func (t *TraceTransaction) Rollback() {
	if t == nil {
		return
	}
	t.mu.Lock()
	t.closed = true
	t.rollback = true
	t.records = nil
	t.mu.Unlock()
}

// PublishAfterCommit offers buffered diagnostics after the product transaction commits.
// Delivery errors are data in the result and cannot turn a committed product operation
// into an application failure.
func (t *TraceTransaction) PublishAfterCommit(ctx context.Context) TracePublishResult {
	if t == nil {
		return TracePublishResult{}
	}
	if ctx == nil {
		ctx = context.Background()
	}
	t.mu.Lock()
	if t.rollback || t.published {
		t.mu.Unlock()
		return TracePublishResult{}
	}
	t.closed = true
	t.published = true
	records := append([]agentobs.Record(nil), t.records...)
	t.records = nil
	t.mu.Unlock()

	result := TracePublishResult{Attempted: len(records)}
	for _, record := range records {
		err := t.sink.Offer(ctx, agentbatch.Envelope{Trace: t.descriptor, Record: record})
		if err != nil {
			result.Failed++
			result.Errors = append(result.Errors, err)
			continue
		}
		result.Accepted++
	}
	return result
}
