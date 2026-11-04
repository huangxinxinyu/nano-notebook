package collector

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/ClickHouse/clickhouse-go/v2/lib/driver"
	"github.com/huangxinxinyu/nano-notebook/internal/agentobs"
	"github.com/huangxinxinyu/nano-notebook/internal/objectstore"
	"github.com/huangxinxinyu/nano-notebook/internal/platform/metrics"
)

type ClickHouseStore struct {
	connection       driver.Conn
	stagingObjects   objectstore.Store
	replayObjects    objectstore.Store
	probeMu          sync.Mutex
	pendingProbes    []*clickHouseProbeRequest
	probeDelay       time.Duration
	probeBatchFn     func(context.Context, []*clickHouseProbeRequest)
	batchMu          sync.Mutex
	pending          []*clickHouseWriteRequest
	batchDelay       time.Duration
	writeBatchFn     func(context.Context, []*clickHouseWriteRequest) error
	metrics          *metrics.Catalog
	watermarkMu      sync.Mutex
	rawWatermark     int64
	summaryWatermark int64
}

func (s *ClickHouseStore) WithMetrics(catalog *metrics.Catalog) *ClickHouseStore {
	if s != nil {
		s.metrics = catalog
	}
	return s
}

type clickHouseWriteRequest struct {
	trace            TraceDescriptor
	records          []SequencedRecord
	projection       *TraceProjection
	attachments      []preparedAttachment
	committedThrough int
	source           KafkaSourcePosition
	done             chan error
}

type clickHouseProbeRequest struct {
	trace TraceDescriptor
	done  chan clickHouseProbeResult
}

type clickHouseProbeResult struct {
	exists bool
	err    error
}

func NewClickHouseStore(connection driver.Conn) (*ClickHouseStore, error) {
	if connection == nil {
		return nil, errors.New("nil Collector ClickHouse connection")
	}
	return &ClickHouseStore{
		connection: connection, probeDelay: 2 * time.Millisecond, batchDelay: 5 * time.Millisecond,
	}, nil
}

func NewClickHouseStoreWithReplay(connection driver.Conn, stagingObjects, replayObjects objectstore.Store) (*ClickHouseStore, error) {
	if stagingObjects == nil || replayObjects == nil {
		return nil, errors.New("Collector ClickHouse Replay Store dependencies are incomplete")
	}
	store, err := NewClickHouseStore(connection)
	if err != nil {
		return nil, err
	}
	store.stagingObjects = stagingObjects
	store.replayObjects = replayObjects
	return store, nil
}

func (s *ClickHouseStore) CommitTraceChunk(ctx context.Context, chunk TraceChunk) (int, error) {
	if s == nil || s.connection == nil {
		return 0, errors.New("nil Collector ClickHouse Store")
	}
	source, ok := KafkaSourcePositionFromContext(ctx)
	if !ok || strings.TrimSpace(source.Topic) == "" || source.Partition < 0 || source.Offset < 0 {
		return 0, errors.New("Collector ClickHouse ingest is missing its Kafka source position")
	}
	trace, err := CanonicalTraceDescriptor(chunk.Trace)
	if err != nil {
		return 0, &ChunkError{Code: CodeInvalidChunk, Err: err}
	}
	chunk.Trace = trace
	tombstoned, err := s.isTombstoned(ctx, chunk.Trace.TraceID)
	if err != nil {
		return 0, err
	}
	if tombstoned {
		return 0, &ChunkError{Code: CodeTombstoned, Err: errors.New("Collector ClickHouse Trace is tombstoned")}
	}
	var existing StoredTrace
	exists, err := s.traceExists(ctx, chunk.Trace)
	if err != nil {
		return 0, err
	}
	if exists {
		existing, err = s.loadTrace(ctx, chunk.Trace.TraceID, chunk.Trace.NotebookID)
		if err != nil {
			return 0, err
		}
	}
	chunk, err = resolveDirectAttachmentSequences(existing.Records, chunk)
	if err != nil {
		return 0, &ChunkError{Code: CodeInvalidChunk, CommittedThrough: existing.CommittedThrough, Err: err}
	}
	merged, committedThrough, err := validateAndMergeTraceChunk(ctx, memoryTrace{
		descriptor: existing.Trace,
		records:    existing.Records,
	}, chunk, s.spanExists)
	if err != nil {
		return 0, err
	}
	preparedAttachments, err := s.prepareReplayAttachments(ctx, chunk)
	if err != nil {
		return 0, err
	}
	newRecords := merged.records[len(existing.Records):]
	projection, projectionErr := BuildTraceProjection(StoredTrace{
		Trace: merged.descriptor, Records: merged.records, CommittedThrough: committedThrough,
	})
	var projected *TraceProjection
	if projectionErr == nil {
		projected = &projection
	}
	if len(newRecords) == 0 && projected == nil && len(preparedAttachments) == 0 {
		return committedThrough, nil
	}
	if err := s.enqueueWrite(clickHouseWriteRequest{
		trace: trace, records: newRecords, projection: projected, attachments: preparedAttachments, committedThrough: committedThrough,
		source: source, done: make(chan error, 1),
	}); err != nil {
		return 0, err
	}
	return committedThrough, nil
}

func (s *ClickHouseStore) enqueueWrite(request clickHouseWriteRequest) error {
	s.batchMu.Lock()
	s.pending = append(s.pending, &request)
	leader := len(s.pending) == 1
	s.batchMu.Unlock()
	if leader {
		timer := time.NewTimer(s.batchDelay)
		<-timer.C
		s.flushPendingWrites()
	}
	return <-request.done
}

func (s *ClickHouseStore) flushPendingWrites() {
	s.batchMu.Lock()
	requests := s.pending
	s.pending = nil
	s.batchMu.Unlock()
	if len(requests) == 0 {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	write := s.writeBatchFn
	if write == nil {
		write = s.writeBatch
	}
	err := write(ctx, requests)
	for _, request := range requests {
		request.done <- err
	}
}

func (s *ClickHouseStore) writeBatch(ctx context.Context, requests []*clickHouseWriteRequest) (writeErr error) {
	started := time.Now()
	defer func() {
		if s.metrics == nil {
			return
		}
		outcome := "success"
		if writeErr != nil {
			outcome = "error"
		}
		s.metrics.ClickHouseRequests.WithLabelValues("insert", outcome).Inc()
		s.metrics.ClickHouseRequestDuration.WithLabelValues("insert", outcome).Observe(time.Since(started).Seconds())
	}()
	recordCount := 0
	projectionCount := 0
	for _, request := range requests {
		recordCount += len(request.records)
		if request.projection != nil {
			projectionCount++
		}
	}
	if recordCount > 0 {
		if err := s.writeRawBatch(ctx, requests); err != nil {
			return err
		}
	}
	if err := s.writeReplayRefsBatch(ctx, requests); err != nil {
		return err
	}
	if projectionCount > 0 {
		if err := s.writeSummaryBatch(ctx, requests); err != nil {
			return err
		}
		if err := s.writeSpanAnalyticsBatch(ctx, requests); err != nil {
			return err
		}
	}
	return nil
}

func (s *ClickHouseStore) writeRawBatch(ctx context.Context, requests []*clickHouseWriteRequest) error {
	batch, err := s.connection.PrepareBatch(ctx, `
		INSERT INTO obs_trace_records_raw (
			trace_id, workload_kind, workload_id, run_id, chat_id, notebook_id,
			root_span_id, agent_name, trace_schema_version, semantic_convention_version,
			sequence, identity_key, kind, span_id, parent_span_id, target_trace_id,
			target_span_id, name, status, occurred_at, occurred_at_unix_nano,
			payload_version, canonical_payload, canonical_sha256,
			source_topic, source_partition, source_offset, ingest_version
		) VALUES
	`)
	if err != nil {
		return err
	}
	defer batch.Close()
	for _, request := range requests {
		ingestVersion := uint64(request.source.Offset) + 1
		for _, envelope := range request.records {
			payload, err := envelope.Record.CanonicalPayload()
			if err != nil {
				return err
			}
			trace := request.trace
			if err := batch.Append(
				string(trace.TraceID), string(trace.WorkloadKind), trace.WorkloadID, trace.RunID, trace.ChatID,
				trace.NotebookID, string(trace.RootSpanID), trace.AgentName, uint16(trace.SchemaVersion),
				uint16(trace.SemanticConventionVersion), uint32(envelope.Sequence), envelope.Record.IdentityKey,
				string(envelope.Record.Kind), string(envelope.Record.SpanID), string(envelope.Record.ParentSpanID),
				string(envelope.Record.TargetTraceID), string(envelope.Record.TargetSpanID), envelope.Record.Name,
				string(envelope.Record.Status), envelope.Record.OccurredAt.UTC(), envelope.Record.OccurredAt.UnixNano(),
				uint16(envelope.Record.PayloadVersion), string(payload), envelope.CanonicalSHA256,
				request.source.Topic, request.source.Partition, request.source.Offset, ingestVersion,
			); err != nil {
				return err
			}
		}
	}
	if err := batch.Send(); err != nil {
		return err
	}
	var latest int64
	for _, request := range requests {
		for _, envelope := range request.records {
			latest = max(latest, envelope.Record.OccurredAt.UnixNano())
		}
	}
	s.observeRawWatermark(latest)
	return nil
}

func (s *ClickHouseStore) writeSummaryBatch(ctx context.Context, requests []*clickHouseWriteRequest) error {
	batch, err := s.connection.PrepareBatch(ctx, `
		INSERT INTO obs_trace_summaries (
			trace_id, workload_kind, workload_id, run_id, chat_id, notebook_id,
			root_span_id, agent_name, started_at, started_at_unix_nano,
			last_observed_unix_nano, ended_at_unix_nano, duration_nanoseconds,
			status, active, models, input_tokens, output_tokens, total_tokens,
			cost_known, cost_amount, cost_currency, cost_source, attempt_count,
			providers, cached_tokens, reasoning_tokens, error_code, stop_reason,
			agent_definition, prompt_version, configuration_version,
			delegation_targets, delegation_outcomes, rag_stages, rag_degradations, citation_outcomes,
			committed_sequence, projected_sequence, ingest_version
		) VALUES
	`)
	if err != nil {
		return err
	}
	defer batch.Close()
	for _, request := range requests {
		if request.projection == nil {
			continue
		}
		summary := request.projection.Summary
		analytics := BuildTraceAnalyticsProjection(*request.projection)
		if err := batch.Append(
			string(summary.TraceID), string(summary.WorkloadKind), summary.WorkloadID, summary.RunID, summary.ChatID,
			summary.NotebookID, string(summary.RootSpanID), summary.AgentName, unixNanoTime(summary.StartedAtUnixNano),
			summary.StartedAtUnixNano, summary.LastObservedUnixNano, summary.EndedAtUnixNano,
			summary.DurationNanoseconds, string(summary.Status), summary.Active, summary.Models,
			summary.InputTokens, summary.OutputTokens, summary.TotalTokens, summary.Cost.Known,
			summary.Cost.Amount, summary.Cost.Currency, summary.Cost.Source, uint32(summary.AttemptCount),
			analytics.Providers, analytics.CachedTokens, analytics.ReasoningTokens, analytics.ErrorCode, analytics.StopReason,
			analytics.AgentDefinition, analytics.PromptVersion, analytics.ConfigurationVersion,
			analytics.DelegationTargets, analytics.DelegationOutcomes, analytics.RAGStages, analytics.RAGDegradations, analytics.CitationOutcomes,
			uint32(request.committedThrough), uint32(request.committedThrough), uint64(request.source.Offset)+1,
		); err != nil {
			return err
		}
	}
	if err := batch.Send(); err != nil {
		return err
	}
	var latest int64
	for _, request := range requests {
		if request.projection != nil {
			latest = max(latest, request.projection.Summary.LastObservedUnixNano)
		}
	}
	s.observeSummaryWatermark(latest)
	return nil
}

func (s *ClickHouseStore) observeRawWatermark(value int64) {
	if s == nil || value <= 0 {
		return
	}
	s.watermarkMu.Lock()
	s.rawWatermark = max(s.rawWatermark, value)
	s.updateWatermarkGapLocked()
	s.watermarkMu.Unlock()
}

func (s *ClickHouseStore) observeSummaryWatermark(value int64) {
	if s == nil || value <= 0 {
		return
	}
	s.watermarkMu.Lock()
	s.summaryWatermark = max(s.summaryWatermark, value)
	s.updateWatermarkGapLocked()
	s.watermarkMu.Unlock()
}

func (s *ClickHouseStore) updateWatermarkGapLocked() {
	if s.metrics == nil {
		return
	}
	gap := s.rawWatermark - s.summaryWatermark
	if gap < 0 {
		gap = 0
	}
	s.metrics.AgentTraceRawSummaryWatermarkGap.Set(float64(gap) / float64(time.Second))
}

func (s *ClickHouseStore) writeSpanAnalyticsBatch(ctx context.Context, requests []*clickHouseWriteRequest) error {
	batch, err := s.connection.PrepareBatch(ctx, `
		INSERT INTO obs_span_analytics (
			trace_id, notebook_id, agent_name, started_at, span_id, span_kind, name,
			tool_name, status, outcome, duration_nanoseconds, provider, requested_model,
			selected_model, cached_tokens, reasoning_tokens, error_code, retryable, ingest_version,
			input_tokens, decision_ordinal, span_started_unix_nano, system_sha256, tools_sha256
		) VALUES
	`)
	if err != nil {
		return err
	}
	defer batch.Close()
	for _, request := range requests {
		if request.projection == nil {
			continue
		}
		analytics := BuildTraceAnalyticsProjection(*request.projection)
		startedAt := unixNanoTime(request.projection.Summary.StartedAtUnixNano)
		ingestVersion := uint64(request.source.Offset) + 1
		for _, span := range analytics.Spans {
			if err := batch.Append(
				string(span.TraceID), span.NotebookID, request.projection.Summary.AgentName, startedAt,
				string(span.SpanID), span.SpanKind, span.Name, span.ToolName, string(span.Status), span.Outcome,
				span.DurationNanoseconds, span.Provider, span.RequestedModel, span.SelectedModel,
				span.CachedTokens, span.ReasoningTokens, span.ErrorCode, span.Retryable, ingestVersion,
				span.InputTokens, span.DecisionOrdinal, span.StartedAtUnixNano, span.SystemSHA256, span.ToolsSHA256,
			); err != nil {
				return err
			}
		}
	}
	return batch.Send()
}

func (s *ClickHouseStore) LoadTrace(ctx context.Context, traceID agentobs.TraceID) (StoredTrace, error) {
	if s == nil || s.connection == nil {
		return StoredTrace{}, errors.New("nil Collector ClickHouse Store")
	}
	return s.loadTrace(ctx, traceID, "")
}

func (s *ClickHouseStore) traceExists(ctx context.Context, trace TraceDescriptor) (bool, error) {
	request := &clickHouseProbeRequest{trace: trace, done: make(chan clickHouseProbeResult, 1)}
	s.probeMu.Lock()
	s.pendingProbes = append(s.pendingProbes, request)
	leader := len(s.pendingProbes) == 1
	s.probeMu.Unlock()
	if leader {
		timer := time.NewTimer(s.probeDelay)
		<-timer.C
		s.flushPendingProbes()
	}
	select {
	case result := <-request.done:
		return result.exists, result.err
	case <-ctx.Done():
		return false, ctx.Err()
	}
}

func (s *ClickHouseStore) flushPendingProbes() {
	s.probeMu.Lock()
	requests := s.pendingProbes
	s.pendingProbes = nil
	s.probeMu.Unlock()
	if len(requests) == 0 {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	probe := s.probeBatchFn
	if probe == nil {
		probe = s.queryTraceExistenceBatch
	}
	probe(ctx, requests)
}

func (s *ClickHouseStore) queryTraceExistenceBatch(ctx context.Context, requests []*clickHouseProbeRequest) {
	placeholders := make([]string, 0, len(requests))
	args := make([]any, 0, len(requests)*2)
	for _, request := range requests {
		placeholders = append(placeholders, "(?, ?)")
		args = append(args, request.trace.NotebookID, string(request.trace.TraceID))
	}
	rows, err := s.connection.Query(ctx, `
		SELECT notebook_id, trace_id
		FROM obs_trace_records_raw
		WHERE (notebook_id, trace_id) IN (`+strings.Join(placeholders, ", ")+`)
		GROUP BY notebook_id, trace_id
	`, args...)
	found := make(map[string]struct{}, len(requests))
	if err == nil {
		defer rows.Close()
		for rows.Next() {
			var notebookID, traceID string
			if scanErr := rows.Scan(&notebookID, &traceID); scanErr != nil {
				err = scanErr
				break
			}
			found[notebookID+"\x00"+traceID] = struct{}{}
		}
		if err == nil {
			err = rows.Err()
		}
	}
	for _, request := range requests {
		_, exists := found[request.trace.NotebookID+"\x00"+string(request.trace.TraceID)]
		request.done <- clickHouseProbeResult{exists: exists, err: err}
	}
}

func (s *ClickHouseStore) loadTrace(ctx context.Context, traceID agentobs.TraceID, notebookID string) (StoredTrace, error) {
	where := "trace_id = ?"
	args := []any{string(traceID)}
	if notebookID = strings.TrimSpace(notebookID); notebookID != "" {
		where = "notebook_id = ? AND trace_id = ?"
		args = []any{notebookID, string(traceID)}
	}
	rows, err := s.connection.Query(ctx, `
		SELECT
			trace_id, workload_kind, workload_id, run_id, chat_id, notebook_id,
			root_span_id, agent_name, trace_schema_version, semantic_convention_version,
			sequence, identity_key, kind, span_id, parent_span_id, target_trace_id,
			target_span_id, name, occurred_at_unix_nano, payload_version,
			canonical_payload, canonical_sha256
		FROM (
			SELECT * FROM obs_trace_records_raw
			WHERE `+where+`
			ORDER BY ingest_version DESC
			LIMIT 1 BY identity_key
		)
		ORDER BY sequence
	`, args...)
	if err != nil {
		return StoredTrace{}, err
	}
	defer rows.Close()
	var stored StoredTrace
	for rows.Next() {
		var descriptor TraceDescriptor
		var traceIDString, workloadKind, rootSpanID string
		var traceSchemaVersion, semanticConventionVersion uint16
		var sequence uint32
		var kind, spanID, parentSpanID, targetTraceID, targetSpanID string
		var occurredAtUnixNano int64
		var payloadVersion uint16
		var canonicalPayload, canonicalSHA256 string
		var envelope SequencedRecord
		if err := rows.Scan(
			&traceIDString, &workloadKind, &descriptor.WorkloadID, &descriptor.RunID, &descriptor.ChatID,
			&descriptor.NotebookID, &rootSpanID, &descriptor.AgentName, &traceSchemaVersion,
			&semanticConventionVersion, &sequence, &envelope.Record.IdentityKey, &kind, &spanID,
			&parentSpanID, &targetTraceID, &targetSpanID, &envelope.Record.Name, &occurredAtUnixNano,
			&payloadVersion, &canonicalPayload, &canonicalSHA256,
		); err != nil {
			return StoredTrace{}, err
		}
		descriptor.TraceID = agentobs.TraceID(traceIDString)
		descriptor.WorkloadKind = WorkloadKind(workloadKind)
		descriptor.RootSpanID = agentobs.SpanID(rootSpanID)
		descriptor.SchemaVersion = int(traceSchemaVersion)
		descriptor.SemanticConventionVersion = int(semanticConventionVersion)
		if len(stored.Records) == 0 {
			stored.Trace = descriptor
		} else if stored.Trace != descriptor {
			return StoredTrace{}, errors.New("Collector ClickHouse Trace descriptor changed")
		}
		payload, err := agentobs.DecodeCanonicalPayload([]byte(canonicalPayload))
		if err != nil {
			return StoredTrace{}, err
		}
		envelope.Sequence = int(sequence)
		envelope.CanonicalSHA256 = canonicalSHA256
		envelope.Record.SchemaVersion = descriptor.SchemaVersion
		envelope.Record.SemanticConventionVersion = payload.SemanticConventionVersion
		envelope.Record.Kind = agentobs.RecordKind(kind)
		envelope.Record.TraceID = descriptor.TraceID
		envelope.Record.SpanID = agentobs.SpanID(spanID)
		envelope.Record.ParentSpanID = agentobs.SpanID(parentSpanID)
		envelope.Record.TargetTraceID = agentobs.TraceID(targetTraceID)
		envelope.Record.TargetSpanID = agentobs.SpanID(targetSpanID)
		envelope.Record.Status = payload.Status
		envelope.Record.OccurredAt = unixNanoTime(occurredAtUnixNano)
		envelope.Record.PayloadVersion = int(payloadVersion)
		envelope.Record.Attributes = payload.Attributes
		if err := envelope.Record.Validate(); err != nil {
			return StoredTrace{}, err
		}
		hash, err := envelope.Record.CanonicalHash()
		if err != nil {
			return StoredTrace{}, err
		}
		if canonicalSHA256 != hex.EncodeToString(hash[:]) {
			return StoredTrace{}, errors.New("stored Collector ClickHouse canonical hash mismatch")
		}
		stored.Records = append(stored.Records, envelope)
	}
	if err := rows.Err(); err != nil {
		return StoredTrace{}, err
	}
	sort.Slice(stored.Records, func(i, j int) bool { return stored.Records[i].Sequence < stored.Records[j].Sequence })
	for index, envelope := range stored.Records {
		if envelope.Sequence != index+1 {
			return StoredTrace{}, fmt.Errorf("Collector ClickHouse Trace sequence %d is not contiguous", envelope.Sequence)
		}
	}
	stored.CommittedThrough = len(stored.Records)
	return stored, nil
}

func (s *ClickHouseStore) spanExists(ctx context.Context, traceID agentobs.TraceID, spanID agentobs.SpanID) (bool, error) {
	var count uint64
	if err := s.connection.QueryRow(ctx, `
		SELECT count() FROM obs_trace_records_raw
		WHERE trace_id = ? AND span_id = ? AND kind = 'span_started'
	`, string(traceID), string(spanID)).Scan(&count); err != nil {
		return false, err
	}
	return count != 0, nil
}

func unixNanoTime(value int64) (resultTime time.Time) {
	return time.Unix(0, value).UTC()
}
