package agentoutbox

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"math/rand/v2"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/huangxinxinyu/nano-notebook/internal/agentobs"
	"github.com/huangxinxinyu/nano-notebook/internal/collector"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Config struct {
	ProducerID      string
	MaxRecords      int
	MaxEncodedBytes int
	MaxTraces       int
	LeaseDuration   time.Duration
	MaxDelay        time.Duration
	BaseBackoff     time.Duration
	MaxBackoff      time.Duration
	RetryJitter     func() float64
}

func (c Config) withDefaults() Config {
	if c.BaseBackoff == 0 {
		c.BaseBackoff = time.Second
	}
	if c.MaxBackoff == 0 {
		c.MaxBackoff = time.Minute
	}
	if c.RetryJitter == nil {
		c.RetryJitter = rand.Float64
	}
	return c
}

func (c Config) validate() error {
	if strings.TrimSpace(c.ProducerID) == "" {
		return errors.New("Outbox producer ID is required")
	}
	if c.MaxRecords < 1 || c.MaxEncodedBytes < 1 || c.MaxTraces < 1 {
		return errors.New("Outbox batch limits must be positive")
	}
	if c.LeaseDuration <= 0 {
		return errors.New("Outbox lease duration must be positive")
	}
	if c.MaxDelay < 0 {
		return errors.New("Outbox batch delay cannot be negative")
	}
	if c.BaseBackoff <= 0 || c.MaxBackoff < c.BaseBackoff {
		return errors.New("Outbox retry backoff is invalid")
	}
	return nil
}

type PostgresStore struct {
	pool   *pgxpool.Pool
	config Config
}

type ClaimedBatch struct {
	LeaseToken string
	Batch      collector.Batch
}

func NewPostgresStore(pool *pgxpool.Pool, config Config) (*PostgresStore, error) {
	if pool == nil {
		return nil, errors.New("Outbox PostgreSQL pool is required")
	}
	config = config.withDefaults()
	if err := config.validate(); err != nil {
		return nil, err
	}
	return &PostgresStore{pool: pool, config: config}, nil
}

type traceRef struct {
	traceID                   agentobs.TraceID
	runID                     string
	chatID                    string
	notebookID                string
	rootSpanID                agentobs.SpanID
	agentName                 string
	schemaVersion             int
	semanticConventionVersion int
	collectorCursor           int
	terminalSequence          *int
}

func (s *PostgresStore) ClaimBatch(ctx context.Context) (ClaimedBatch, bool, error) {
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return ClaimedBatch{}, false, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := tx.Exec(ctx, `set local role nano_worker`); err != nil {
		return ClaimedBatch{}, false, fmt.Errorf("assume Outbox worker role: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		update agent_trace_refs
		set delivery_state = 'ready', lease_token = null, lease_expires_at = null,
			updated_at = now()
		where delivery_state = 'leased' and lease_expires_at <= now()
	`); err != nil {
		return ClaimedBatch{}, false, fmt.Errorf("reclaim expired Outbox leases: %w", err)
	}

	rows, err := tx.Query(ctx, `
		select trace_id, run_id, chat_id, notebook_id, root_span_id, agent_name,
			schema_version, semantic_convention_version, collector_cursor, terminal_sequence
		from agent_trace_refs
		where delivery_state = 'ready'
			and next_attempt_at <= now()
			and collector_cursor < next_sequence - 1
		order by next_attempt_at, created_at, trace_id
		for update skip locked
		limit $1
	`, s.config.MaxTraces)
	if err != nil {
		return ClaimedBatch{}, false, fmt.Errorf("select ready Outbox traces: %w", err)
	}
	refs := make([]traceRef, 0, s.config.MaxTraces)
	for rows.Next() {
		var ref traceRef
		if err := rows.Scan(
			&ref.traceID, &ref.runID, &ref.chatID, &ref.notebookID, &ref.rootSpanID,
			&ref.agentName, &ref.schemaVersion, &ref.semanticConventionVersion, &ref.collectorCursor,
			&ref.terminalSequence,
		); err != nil {
			rows.Close()
			return ClaimedBatch{}, false, err
		}
		refs = append(refs, ref)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return ClaimedBatch{}, false, err
	}
	rows.Close()
	if len(refs) == 0 {
		if err := tx.Commit(ctx); err != nil {
			return ClaimedBatch{}, false, err
		}
		return ClaimedBatch{}, false, nil
	}

	var now time.Time
	if err := tx.QueryRow(ctx, `select now()`).Scan(&now); err != nil {
		return ClaimedBatch{}, false, fmt.Errorf("read Outbox database time: %w", err)
	}
	leaseToken := uuid.NewString()
	batch := collector.Batch{
		ProtocolVersion: collector.ProtocolVersion,
		BatchID:         uuid.NewString(),
		ProducerID:      s.config.ProducerID,
		CreatedAt:       now,
		Chunks:          make([]collector.TraceChunk, 0, len(refs)),
	}
	totalRecords := 0
	totalBytes := 0
	var oldestReady time.Time
	terminalUrgent := false
	stopBatch := false
	for _, ref := range refs {
		if stopBatch || totalRecords >= s.config.MaxRecords {
			break
		}
		remainingRecords := s.config.MaxRecords - totalRecords
		recordRows, err := tx.Query(ctx, `
			select sequence_no, identity_key, record_kind, span_id,
				coalesce(parent_span_id, ''), coalesce(target_trace_id, ''),
				coalesce(target_span_id, ''), name, occurred_at_unix_nano,
				payload_version, payload::text, canonical_sha256, encoded_bytes, created_at
			from agentobs_outbox_records
			where trace_id = $1 and sequence_no > $2
			order by sequence_no
			limit $3
		`, ref.traceID, ref.collectorCursor, remainingRecords)
		if err != nil {
			return ClaimedBatch{}, false, fmt.Errorf("load Outbox records for Trace %s: %w", ref.traceID, err)
		}
		chunk := collector.TraceChunk{
			Trace: collector.TraceDescriptor{
				TraceID: ref.traceID, RunID: ref.runID, ChatID: ref.chatID,
				NotebookID: ref.notebookID, RootSpanID: ref.rootSpanID,
				AgentName: ref.agentName, SchemaVersion: ref.schemaVersion,
				SemanticConventionVersion: ref.semanticConventionVersion,
			},
			FirstSequence: ref.collectorCursor + 1,
			Records:       make([]collector.SequencedRecord, 0, remainingRecords),
		}
		for recordRows.Next() {
			envelope, encodedBytes, createdAt, err := scanOutboxRecord(recordRows, ref.traceID, ref.schemaVersion)
			if err != nil {
				recordRows.Close()
				return ClaimedBatch{}, false, err
			}
			if totalRecords > 0 && totalBytes+encodedBytes > s.config.MaxEncodedBytes {
				stopBatch = true
				break
			}
			chunk.Records = append(chunk.Records, envelope)
			totalRecords++
			totalBytes += encodedBytes
			if oldestReady.IsZero() || createdAt.Before(oldestReady) {
				oldestReady = createdAt
			}
		}
		if err := recordRows.Err(); err != nil {
			recordRows.Close()
			return ClaimedBatch{}, false, err
		}
		recordRows.Close()
		if len(chunk.Records) == 0 {
			break
		}
		terminalUrgent = terminalUrgent || ref.terminalSequence != nil
		batch.Chunks = append(batch.Chunks, chunk)
	}
	if len(batch.Chunks) == 0 {
		return ClaimedBatch{}, false, errors.New("ready Outbox Trace has no claimable records")
	}
	flush := s.config.MaxDelay == 0 || terminalUrgent || stopBatch ||
		totalRecords >= s.config.MaxRecords || totalBytes >= s.config.MaxEncodedBytes ||
		(!oldestReady.IsZero() && !oldestReady.After(now.Add(-s.config.MaxDelay)))
	if !flush {
		if err := tx.Commit(ctx); err != nil {
			return ClaimedBatch{}, false, err
		}
		return ClaimedBatch{}, false, nil
	}
	for _, chunk := range batch.Chunks {
		if _, err := tx.Exec(ctx, `
			update agent_trace_refs
			set delivery_state = 'leased', lease_token = $2, lease_expires_at = $3,
				attempt_count = attempt_count + 1, updated_at = now()
			where trace_id = $1
		`, chunk.Trace.TraceID, leaseToken, now.Add(s.config.LeaseDuration)); err != nil {
			return ClaimedBatch{}, false, fmt.Errorf("lease Outbox Trace %s: %w", chunk.Trace.TraceID, err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return ClaimedBatch{}, false, err
	}
	return ClaimedBatch{LeaseToken: leaseToken, Batch: batch}, true, nil
}

func (s *PostgresStore) ApplyResult(ctx context.Context, claimed ClaimedBatch, result collector.BatchResult) error {
	if claimed.LeaseToken == "" || claimed.Batch.BatchID == "" {
		return errors.New("claimed Outbox Batch is incomplete")
	}
	if result.BatchID != claimed.Batch.BatchID {
		return errors.New("Collector Batch result ID does not match claim")
	}
	if len(result.Chunks) != len(claimed.Batch.Chunks) {
		return errors.New("Collector Batch result does not cover every claimed Trace")
	}
	resultsByTrace := make(map[agentobs.TraceID]collector.ChunkResult, len(result.Chunks))
	for _, chunkResult := range result.Chunks {
		if _, exists := resultsByTrace[chunkResult.TraceID]; exists {
			return fmt.Errorf("Collector Batch result repeats Trace %s", chunkResult.TraceID)
		}
		resultsByTrace[chunkResult.TraceID] = chunkResult
	}

	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `set local role nano_worker`); err != nil {
		return fmt.Errorf("assume Outbox worker role: %w", err)
	}
	for _, chunk := range claimed.Batch.Chunks {
		chunkResult, exists := resultsByTrace[chunk.Trace.TraceID]
		if !exists {
			return fmt.Errorf("Collector Batch result omits Trace %s", chunk.Trace.TraceID)
		}
		if len(chunk.Records) == 0 {
			return fmt.Errorf("claimed Trace %s has no records", chunk.Trace.TraceID)
		}
		lastSequence := chunk.Records[len(chunk.Records)-1].Sequence
		var collectorCursor, nextSequence, attemptCount int
		var terminalSequence *int
		if err := tx.QueryRow(ctx, `
			select collector_cursor, next_sequence, terminal_sequence, attempt_count
			from agent_trace_refs
			where trace_id = $1 and delivery_state = 'leased' and lease_token = $2
			for update
		`, chunk.Trace.TraceID, claimed.LeaseToken).Scan(&collectorCursor, &nextSequence, &terminalSequence, &attemptCount); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return fmt.Errorf("Outbox lease for Trace %s is no longer authoritative", chunk.Trace.TraceID)
			}
			return err
		}
		if collectorCursor != chunk.FirstSequence-1 {
			return fmt.Errorf("Outbox cursor for Trace %s changed from claimed sequence", chunk.Trace.TraceID)
		}
		switch chunkResult.Status {
		case collector.ChunkCommitted:
			if chunkResult.CommittedThrough != lastSequence {
				return fmt.Errorf("Collector cursor %d does not match claimed Trace %s sequence %d", chunkResult.CommittedThrough, chunk.Trace.TraceID, lastSequence)
			}
			deliveryState := "ready"
			if chunkResult.CommittedThrough == nextSequence-1 {
				deliveryState = "acknowledged"
			}
			if _, err := tx.Exec(ctx, `
				update agent_trace_refs
				set collector_cursor = $2, delivery_state = $3, lease_token = null,
					lease_expires_at = null, next_attempt_at = now(), last_error_code = null,
					quarantined_at = null, updated_at = now()
				where trace_id = $1
			`, chunk.Trace.TraceID, chunkResult.CommittedThrough, deliveryState); err != nil {
				return err
			}
			if terminalSequence != nil && chunkResult.CommittedThrough >= *terminalSequence {
				if _, err := tx.Exec(ctx, `delete from agentobs_outbox_records where trace_id = $1`, chunk.Trace.TraceID); err != nil {
					return err
				}
			}
		case collector.ChunkRetryable:
			if chunkResult.Code == "" || chunkResult.CommittedThrough != collectorCursor {
				return fmt.Errorf("retryable Collector result for Trace %s is invalid", chunk.Trace.TraceID)
			}
			if _, err := tx.Exec(ctx, `
				update agent_trace_refs
				set delivery_state = 'ready', lease_token = null, lease_expires_at = null,
					next_attempt_at = now() + $2::interval, last_error_code = $3, updated_at = now()
				where trace_id = $1
			`, chunk.Trace.TraceID, s.retryDelay(attemptCount), chunkResult.Code); err != nil {
				return err
			}
		case collector.ChunkRejected:
			if chunkResult.Code == "" || chunkResult.CommittedThrough != collectorCursor {
				return fmt.Errorf("rejected Collector result for Trace %s is invalid", chunk.Trace.TraceID)
			}
			if _, err := tx.Exec(ctx, `
				update agent_trace_refs
				set delivery_state = 'quarantined', lease_token = null, lease_expires_at = null,
					last_error_code = $2, quarantined_at = now(), updated_at = now()
				where trace_id = $1
			`, chunk.Trace.TraceID, chunkResult.Code); err != nil {
				return err
			}
		default:
			return fmt.Errorf("unsupported Collector result status %q", chunkResult.Status)
		}
	}
	return tx.Commit(ctx)
}

func (s *PostgresStore) retryDelay(attemptCount int) time.Duration {
	delay := s.config.BaseBackoff
	for attempt := 1; attempt < attemptCount && delay < s.config.MaxBackoff; attempt++ {
		if delay > s.config.MaxBackoff/2 {
			delay = s.config.MaxBackoff
			break
		}
		delay *= 2
	}
	jitter := s.config.RetryJitter()
	if jitter < 0 {
		jitter = 0
	}
	if jitter > 1 {
		jitter = 1
	}
	delay = time.Duration(float64(delay) * (0.5 + jitter))
	if delay > s.config.MaxBackoff {
		return s.config.MaxBackoff
	}
	return delay
}

type rowScanner interface {
	Scan(...any) error
}

func scanOutboxRecord(row rowScanner, traceID agentobs.TraceID, schemaVersion int) (collector.SequencedRecord, int, time.Time, error) {
	var envelope collector.SequencedRecord
	var kind string
	var spanID, parentSpanID, targetTraceID, targetSpanID string
	var occurredAtUnixNano int64
	var payloadText string
	var encodedBytes int
	var createdAt time.Time
	if err := row.Scan(
		&envelope.Sequence, &envelope.Record.IdentityKey, &kind, &spanID,
		&parentSpanID, &targetTraceID, &targetSpanID, &envelope.Record.Name,
		&occurredAtUnixNano, &envelope.Record.PayloadVersion, &payloadText,
		&envelope.CanonicalSHA256, &encodedBytes, &createdAt,
	); err != nil {
		return collector.SequencedRecord{}, 0, time.Time{}, err
	}
	payload, err := agentobs.DecodeCanonicalPayload([]byte(payloadText))
	if err != nil {
		return collector.SequencedRecord{}, 0, time.Time{}, fmt.Errorf("decode Outbox canonical payload: %w", err)
	}
	envelope.Record.SchemaVersion = schemaVersion
	envelope.Record.SemanticConventionVersion = payload.SemanticConventionVersion
	envelope.Record.Kind = agentobs.RecordKind(kind)
	envelope.Record.TraceID = traceID
	envelope.Record.SpanID = agentobs.SpanID(spanID)
	envelope.Record.ParentSpanID = agentobs.SpanID(parentSpanID)
	envelope.Record.TargetTraceID = agentobs.TraceID(targetTraceID)
	envelope.Record.TargetSpanID = agentobs.SpanID(targetSpanID)
	envelope.Record.Status = payload.Status
	envelope.Record.OccurredAt = time.Unix(0, occurredAtUnixNano).UTC()
	envelope.Record.Attributes = payload.Attributes
	if err := envelope.Record.Validate(); err != nil {
		return collector.SequencedRecord{}, 0, time.Time{}, fmt.Errorf("validate Outbox record: %w", err)
	}
	hash, err := envelope.Record.CanonicalHash()
	if err != nil {
		return collector.SequencedRecord{}, 0, time.Time{}, err
	}
	if envelope.CanonicalSHA256 != hex.EncodeToString(hash[:]) {
		return collector.SequencedRecord{}, 0, time.Time{}, errors.New("stored Outbox canonical hash mismatch")
	}
	return envelope, encodedBytes, createdAt, nil
}
