package memory

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"sync"

	"github.com/huangxinxinyu/nano-notebook/internal/agentobs"
)

var ErrShutdown = errors.New("memory exporter is shut down")

const (
	DefaultMaxRecordsPerTrace   = 256
	DefaultMaxTracePayloadBytes = 1024 * 1024
	DefaultMaxLinksPerSpan      = 8
)

type Config struct {
	RecordLimits         *agentobs.Limits
	MaxRecordsPerTrace   int
	MaxTracePayloadBytes int
	MaxLinksPerSpan      int
	ResolveLink          func(agentobs.TraceID, agentobs.SpanID) bool
}

type Exporter struct {
	mu       sync.RWMutex
	records  []agentobs.Record
	traces   map[agentobs.TraceID]*traceState
	shutdown bool
	config   resolvedConfig
}

type resolvedConfig struct {
	recordLimits         agentobs.Limits
	maxRecordsPerTrace   int
	maxTracePayloadBytes int
	maxLinksPerSpan      int
	resolveLink          func(agentobs.TraceID, agentobs.SpanID) bool
}

type traceState struct {
	rootSpanID  agentobs.SpanID
	identities  map[string][sha256.Size]byte
	spans       map[agentobs.SpanID]*spanState
	recordCount int
	payloadSize int
}

type spanState struct {
	name      string
	ended     bool
	linkCount int
}

var _ agentobs.Exporter = (*Exporter)(nil)

func New() *Exporter {
	exporter, err := NewWithConfig(Config{})
	if err != nil {
		panic(err)
	}
	return exporter
}

func NewWithConfig(config Config) (*Exporter, error) {
	resolved := resolvedConfig{
		recordLimits:         agentobs.DefaultLimits(),
		maxRecordsPerTrace:   defaulted(config.MaxRecordsPerTrace, DefaultMaxRecordsPerTrace),
		maxTracePayloadBytes: defaulted(config.MaxTracePayloadBytes, DefaultMaxTracePayloadBytes),
		maxLinksPerSpan:      defaulted(config.MaxLinksPerSpan, DefaultMaxLinksPerSpan),
		resolveLink:          config.ResolveLink,
	}
	if config.RecordLimits != nil {
		resolved.recordLimits = *config.RecordLimits
	}
	if err := resolved.recordLimits.Validate(); err != nil {
		return nil, err
	}
	if resolved.maxRecordsPerTrace < 1 || resolved.maxTracePayloadBytes < 1 || resolved.maxLinksPerSpan < 1 {
		return nil, errors.New("memory exporter limits must be positive")
	}
	return &Exporter{traces: make(map[agentobs.TraceID]*traceState), config: resolved}, nil
}

func (e *Exporter) Export(_ context.Context, record agentobs.Record) error {
	if err := record.ValidateWithLimits(e.config.recordLimits); err != nil {
		return err
	}
	hash, err := record.CanonicalHashWithLimits(e.config.recordLimits)
	if err != nil {
		return err
	}
	payload, err := record.CanonicalPayloadWithLimits(e.config.recordLimits)
	if err != nil {
		return err
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.shutdown {
		return ErrShutdown
	}
	trace := e.traces[record.TraceID]
	if trace != nil {
		if existingHash, exists := trace.identities[record.IdentityKey]; exists {
			if existingHash == hash {
				return nil
			}
			return fmt.Errorf("%w: %s", agentobs.ErrIdentityConflict, record.IdentityKey)
		}
	}
	if err := e.validateLifecycle(record, trace); err != nil {
		return err
	}
	if trace == nil {
		trace = &traceState{
			rootSpanID: record.SpanID,
			identities: make(map[string][sha256.Size]byte),
			spans:      make(map[agentobs.SpanID]*spanState),
		}
		e.traces[record.TraceID] = trace
	}
	if trace.recordCount >= e.config.maxRecordsPerTrace || trace.payloadSize+len(payload) > e.config.maxTracePayloadBytes {
		return agentobs.ErrLimitExceeded
	}
	if record.Kind == agentobs.RecordLink && trace.spans[record.SpanID].linkCount >= e.config.maxLinksPerSpan {
		return agentobs.ErrLimitExceeded
	}
	e.applyLifecycle(record, trace)
	trace.identities[record.IdentityKey] = hash
	trace.recordCount++
	trace.payloadSize += len(payload)
	e.records = append(e.records, cloneRecord(record))
	return nil
}

func (e *Exporter) validateLifecycle(record agentobs.Record, trace *traceState) error {
	switch record.Kind {
	case agentobs.RecordSpanStarted:
		if trace == nil {
			if record.ParentSpanID != "" {
				return fmt.Errorf("%w: root Span must be recorded before child %s", agentobs.ErrLifecycle, record.SpanID)
			}
			return nil
		}
		if _, exists := trace.spans[record.SpanID]; exists {
			return fmt.Errorf("%w: Span %s already started", agentobs.ErrLifecycle, record.SpanID)
		}
		if record.ParentSpanID == "" {
			return fmt.Errorf("%w: Trace %s already has root Span %s", agentobs.ErrLifecycle, record.TraceID, trace.rootSpanID)
		}
		if _, exists := trace.spans[record.ParentSpanID]; !exists {
			return fmt.Errorf("%w: parent Span %s is unknown", agentobs.ErrLifecycle, record.ParentSpanID)
		}
	case agentobs.RecordSpanEnded:
		span, err := knownSpan(trace, record.SpanID)
		if err != nil {
			return err
		}
		if span.ended {
			return fmt.Errorf("%w: Span %s already ended", agentobs.ErrLifecycle, record.SpanID)
		}
		if span.name != record.Name {
			return fmt.Errorf("%w: terminal name %q does not match start %q", agentobs.ErrLifecycle, record.Name, span.name)
		}
	case agentobs.RecordEvent:
		if _, err := knownSpan(trace, record.SpanID); err != nil {
			return err
		}
	case agentobs.RecordLink:
		if _, err := knownSpan(trace, record.SpanID); err != nil {
			return err
		}
		targetTrace := e.traces[record.TargetTraceID]
		resolvedExternally := e.config.resolveLink != nil && e.config.resolveLink(record.TargetTraceID, record.TargetSpanID)
		if (targetTrace == nil || targetTrace.spans[record.TargetSpanID] == nil) && !resolvedExternally {
			return fmt.Errorf("%w: %s/%s", agentobs.ErrUnresolvedLink, record.TargetTraceID, record.TargetSpanID)
		}
	}
	return nil
}

func (e *Exporter) applyLifecycle(record agentobs.Record, trace *traceState) {
	switch record.Kind {
	case agentobs.RecordSpanStarted:
		trace.spans[record.SpanID] = &spanState{name: record.Name}
	case agentobs.RecordSpanEnded:
		trace.spans[record.SpanID].ended = true
	case agentobs.RecordLink:
		trace.spans[record.SpanID].linkCount++
	}
}

func knownSpan(trace *traceState, spanID agentobs.SpanID) (*spanState, error) {
	if trace == nil {
		return nil, fmt.Errorf("%w: Trace is unknown", agentobs.ErrLifecycle)
	}
	span := trace.spans[spanID]
	if span == nil {
		return nil, fmt.Errorf("%w: Span %s is unknown", agentobs.ErrLifecycle, spanID)
	}
	return span, nil
}

func (e *Exporter) Records() []agentobs.Record {
	e.mu.RLock()
	defer e.mu.RUnlock()
	records := make([]agentobs.Record, len(e.records))
	for index, record := range e.records {
		records[index] = cloneRecord(record)
	}
	return records
}

func (e *Exporter) ForceFlush(_ context.Context) error {
	e.mu.RLock()
	defer e.mu.RUnlock()
	if e.shutdown {
		return ErrShutdown
	}
	return nil
}

func (e *Exporter) Shutdown(_ context.Context) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.shutdown = true
	return nil
}

func cloneRecord(record agentobs.Record) agentobs.Record {
	record.Attributes = append([]agentobs.Attribute(nil), record.Attributes...)
	return record
}

func defaulted(value, fallback int) int {
	if value == 0 {
		return fallback
	}
	return value
}
