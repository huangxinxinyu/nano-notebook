package agentbatch

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/huangxinxinyu/nano-notebook/internal/agentobs"
	"github.com/huangxinxinyu/nano-notebook/internal/collector"
)

var (
	ErrQueueFull = errors.New("Agent Trace memory queue is full")
	ErrShutdown  = errors.New("Agent Trace memory exporter is shut down")
)

type Envelope struct {
	Trace       collector.TraceDescriptor
	Record      agentobs.Record
	Attachments []collector.AttachmentDescriptor
}

type Sender interface {
	Send(context.Context, collector.Batch) (collector.BatchResult, error)
}

type Config struct {
	ProducerID        string
	Sender            Sender
	MaxPendingRecords int
	MaxPendingBytes   int
	MaxBatchRecords   int
	MaxBatchBytes     int
	MaxDelay          time.Duration
}

type Stats struct {
	PendingRecords  int
	PendingBytes    int
	InflightRecords int
	DroppedRecords  uint64
}

type queuedEnvelope struct {
	envelope Envelope
	bytes    int
	queuedAt time.Time
}

type Exporter struct {
	config Config
	ctx    context.Context
	cancel context.CancelFunc

	mu             sync.Mutex
	queue          []queuedEnvelope
	inflight       []queuedEnvelope
	inflightBatch  collector.Batch
	pendingRecords int
	pendingBytes   int
	droppedRecords uint64
	force          bool
	shutdown       bool
	flushWaiters   []chan error
	flushErr       error

	notify chan struct{}
	done   chan struct{}
}

func NewExporter(config Config) (*Exporter, error) {
	config.ProducerID = strings.TrimSpace(config.ProducerID)
	if config.ProducerID == "" || config.Sender == nil || config.MaxPendingRecords < 1 ||
		config.MaxPendingBytes < 1 || config.MaxBatchRecords < 1 || config.MaxBatchBytes < 1 ||
		config.MaxDelay <= 0 || config.MaxBatchRecords > config.MaxPendingRecords ||
		config.MaxBatchBytes > config.MaxPendingBytes {
		return nil, errors.New("Agent Trace memory exporter configuration is invalid")
	}
	exporter := &Exporter{config: config, notify: make(chan struct{}, 1), done: make(chan struct{})}
	exporter.ctx, exporter.cancel = context.WithCancel(context.Background())
	go exporter.run()
	return exporter, nil
}

func (e *Exporter) Offer(_ context.Context, envelope Envelope) error {
	if e == nil {
		return errors.New("nil Agent Trace memory exporter")
	}
	encodedBytes, err := validateEnvelope(envelope)
	if err != nil {
		return err
	}
	if encodedBytes > e.config.MaxBatchBytes {
		e.mu.Lock()
		e.droppedRecords++
		e.mu.Unlock()
		return fmt.Errorf("%w: one record exceeds the Batch byte bound", ErrQueueFull)
	}
	e.mu.Lock()
	if e.shutdown {
		e.mu.Unlock()
		return ErrShutdown
	}
	if e.pendingRecords >= e.config.MaxPendingRecords || e.pendingBytes+encodedBytes > e.config.MaxPendingBytes {
		e.droppedRecords++
		e.mu.Unlock()
		return ErrQueueFull
	}
	e.queue = append(e.queue, queuedEnvelope{envelope: cloneEnvelope(envelope), bytes: encodedBytes, queuedAt: time.Now()})
	e.pendingRecords++
	e.pendingBytes += encodedBytes
	e.mu.Unlock()
	e.signal()
	return nil
}

func (e *Exporter) Stats() Stats {
	if e == nil {
		return Stats{}
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	return Stats{
		PendingRecords: e.pendingRecords, PendingBytes: e.pendingBytes,
		InflightRecords: len(e.inflight), DroppedRecords: e.droppedRecords,
	}
}

func (e *Exporter) ForceFlush(ctx context.Context) error {
	if e == nil {
		return errors.New("nil Agent Trace memory exporter")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	e.mu.Lock()
	if e.pendingRecords == 0 {
		err := e.flushErr
		e.flushErr = nil
		e.mu.Unlock()
		return err
	}
	waiter := make(chan error, 1)
	e.flushWaiters = append(e.flushWaiters, waiter)
	e.force = true
	e.mu.Unlock()
	e.signal()
	select {
	case err := <-waiter:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (e *Exporter) Shutdown(ctx context.Context) error {
	if e == nil {
		return errors.New("nil Agent Trace memory exporter")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	e.mu.Lock()
	alreadyShutdown := e.shutdown
	e.shutdown = true
	e.force = true
	e.mu.Unlock()
	if alreadyShutdown {
		select {
		case <-e.done:
			return nil
		case <-ctx.Done():
			e.cancel()
			return ctx.Err()
		}
	}
	e.signal()
	if err := e.ForceFlush(ctx); err != nil {
		e.cancel()
		e.signal()
		return err
	}
	select {
	case <-e.done:
		return nil
	case <-ctx.Done():
		e.cancel()
		return ctx.Err()
	}
}

// Done is closed after the exporter's background delivery loop has stopped.
func (e *Exporter) Done() <-chan struct{} {
	if e == nil {
		closed := make(chan struct{})
		close(closed)
		return closed
	}
	return e.done
}

func (e *Exporter) run() {
	defer close(e.done)
	for {
		batch, wait, stop := e.nextBatch()
		if stop {
			return
		}
		if len(batch) == 0 {
			timer := time.NewTimer(wait)
			select {
			case <-e.notify:
				if !timer.Stop() {
					<-timer.C
				}
			case <-timer.C:
			}
			continue
		}
		wire := e.currentBatch(batch)
		result, err := e.config.Sender.Send(e.ctx, wire)
		if err == nil {
			err = validateResult(wire, result)
		}
		if err != nil {
			if e.ctx.Err() != nil {
				e.abort(e.ctx.Err())
				return
			}
			if !Retryable(err) {
				e.completeInflight(err, true)
				continue
			}
			timer := time.NewTimer(10 * time.Millisecond)
			select {
			case <-e.notify:
				if !timer.Stop() {
					<-timer.C
				}
			case <-timer.C:
			case <-e.ctx.Done():
				if !timer.Stop() {
					<-timer.C
				}
				e.abort(e.ctx.Err())
				return
			}
			continue
		}
		e.completeInflight(nil, false)
	}
}

func (e *Exporter) nextBatch() ([]queuedEnvelope, time.Duration, bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if len(e.inflight) > 0 {
		return append([]queuedEnvelope(nil), e.inflight...), 0, false
	}
	if len(e.queue) == 0 {
		if e.shutdown {
			return nil, 0, true
		}
		return nil, time.Hour, false
	}
	ready := e.force || len(e.queue) >= e.config.MaxBatchRecords
	if !ready {
		bytes := 0
		for _, queued := range e.queue {
			bytes += queued.bytes
			if bytes >= e.config.MaxBatchBytes {
				ready = true
				break
			}
		}
	}
	deadline := e.queue[0].queuedAt.Add(e.config.MaxDelay)
	if !ready && !time.Now().Before(deadline) {
		ready = true
	}
	if !ready {
		return nil, time.Until(deadline), false
	}
	count, bytes := 0, 0
	for count < len(e.queue) && count < e.config.MaxBatchRecords {
		next := e.queue[count]
		if count > 0 && bytes+next.bytes > e.config.MaxBatchBytes {
			break
		}
		bytes += next.bytes
		count++
	}
	e.inflight = append([]queuedEnvelope(nil), e.queue[:count]...)
	e.queue = append([]queuedEnvelope(nil), e.queue[count:]...)
	return append([]queuedEnvelope(nil), e.inflight...), 0, false
}

func (e *Exporter) buildBatch(items []queuedEnvelope) collector.Batch {
	batch := collector.Batch{
		ProtocolVersion: collector.DirectProtocolVersion,
		BatchID:         uuid.NewString(),
		ProducerID:      e.config.ProducerID,
		CreatedAt:       time.Now().UTC(),
	}
	chunkIndex := make(map[agentobs.TraceID]int)
	for _, item := range items {
		position, found := chunkIndex[item.envelope.Trace.TraceID]
		if !found {
			position = len(batch.Chunks)
			chunkIndex[item.envelope.Trace.TraceID] = position
			batch.Chunks = append(batch.Chunks, collector.TraceChunk{
				Trace: item.envelope.Trace, SequenceAuthority: collector.SequenceAuthorityCollector,
			})
		}
		hash, _ := item.envelope.Record.CanonicalHash()
		batch.Chunks[position].Records = append(batch.Chunks[position].Records, collector.SequencedRecord{
			Record: item.envelope.Record, CanonicalSHA256: hex.EncodeToString(hash[:]),
		})
		batch.Chunks[position].Attachments = append(batch.Chunks[position].Attachments, item.envelope.Attachments...)
	}
	return batch
}

func (e *Exporter) currentBatch(items []queuedEnvelope) collector.Batch {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.inflightBatch.BatchID == "" {
		e.inflightBatch = e.buildBatch(items)
	}
	return e.inflightBatch
}

func (e *Exporter) completeInflight(deliveryErr error, dropped bool) {
	e.mu.Lock()
	for _, item := range e.inflight {
		e.pendingRecords--
		e.pendingBytes -= item.bytes
	}
	if dropped {
		e.droppedRecords += uint64(len(e.inflight))
	}
	if deliveryErr != nil && e.flushErr == nil {
		e.flushErr = deliveryErr
	}
	e.inflight = nil
	e.inflightBatch = collector.Batch{}
	if e.pendingRecords == 0 {
		e.force = false
		flushErr := e.flushErr
		if len(e.flushWaiters) > 0 {
			e.flushErr = nil
		}
		for _, waiter := range e.flushWaiters {
			waiter <- flushErr
		}
		e.flushWaiters = nil
	}
	e.mu.Unlock()
	e.signal()
}

func (e *Exporter) abort(err error) {
	e.mu.Lock()
	for _, waiter := range e.flushWaiters {
		waiter <- err
	}
	e.flushWaiters = nil
	e.mu.Unlock()
}

func (e *Exporter) signal() {
	select {
	case e.notify <- struct{}{}:
	default:
	}
}

func validateEnvelope(envelope Envelope) (int, error) {
	if _, err := collector.CanonicalTraceDescriptor(envelope.Trace); err != nil {
		return 0, errors.New("Agent Trace envelope descriptor is invalid")
	}
	if err := envelope.Record.Validate(); err != nil {
		return 0, err
	}
	if envelope.Record.TraceID != envelope.Trace.TraceID || envelope.Record.SchemaVersion != envelope.Trace.SchemaVersion ||
		envelope.Record.SemanticConventionVersion != envelope.Trace.SemanticConventionVersion {
		return 0, errors.New("Agent Trace record changed its envelope")
	}
	encoded, err := json.Marshal(envelope)
	if err != nil {
		return 0, err
	}
	return len(encoded), nil
}

func validateResult(batch collector.Batch, result collector.BatchResult) error {
	if result.BatchID != batch.BatchID || len(result.Chunks) != len(batch.Chunks) {
		return newDeliveryError(false, errors.New("Collector Batch result does not match the request"))
	}
	for index, chunk := range result.Chunks {
		if chunk.TraceID != batch.Chunks[index].Trace.TraceID {
			return newDeliveryError(false, errors.New("Collector Batch result does not match the request"))
		}
		switch chunk.Status {
		case collector.ChunkCommitted:
		case collector.ChunkRetryable:
			return newDeliveryError(true, fmt.Errorf("Collector asked to retry Trace %s: %s", chunk.TraceID, chunk.Code))
		default:
			return newDeliveryError(false, fmt.Errorf("Collector rejected Trace %s: %s", chunk.TraceID, chunk.Code))
		}
	}
	return nil
}

func cloneEnvelope(envelope Envelope) Envelope {
	envelope.Record.Attributes = append([]agentobs.Attribute(nil), envelope.Record.Attributes...)
	envelope.Attachments = append([]collector.AttachmentDescriptor(nil), envelope.Attachments...)
	return envelope
}
