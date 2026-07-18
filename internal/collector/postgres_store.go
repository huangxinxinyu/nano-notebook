package collector

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/huangxinxinyu/nano-notebook/internal/agentobs"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type PostgresStore struct {
	pool *pgxpool.Pool
}

type StoredTrace struct {
	Trace            TraceDescriptor
	CommittedThrough int
	ProjectedThrough int
	Tombstoned       bool
	Records          []SequencedRecord
}

func NewPostgresStore(pool *pgxpool.Pool) *PostgresStore {
	return &PostgresStore{pool: pool}
}

func (s *PostgresStore) CommitTraceChunk(ctx context.Context, chunk TraceChunk) (int, error) {
	if s == nil || s.pool == nil {
		return 0, errors.New("nil Collector PostgreSQL Store")
	}
	if err := validateTraceDescriptor(chunk.Trace); err != nil {
		return 0, &ChunkError{Code: CodeInvalidChunk, Err: err}
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx)

	if _, err := tx.Exec(ctx, `
		insert into obs_traces (
			trace_id, run_id, chat_id, notebook_id, root_span_id, agent_name,
			schema_version, semantic_convention_version
		) values ($1, $2, $3, $4, $5, $6, $7, $8)
		on conflict (trace_id) do nothing
	`, chunk.Trace.TraceID, chunk.Trace.RunID, chunk.Trace.ChatID, chunk.Trace.NotebookID,
		chunk.Trace.RootSpanID, chunk.Trace.AgentName, chunk.Trace.SchemaVersion,
		chunk.Trace.SemanticConventionVersion); err != nil {
		return 0, err
	}

	existing, err := loadStoredTrace(ctx, tx, chunk.Trace.TraceID, true)
	if err != nil {
		return 0, err
	}
	if existing.Tombstoned {
		return 0, &ChunkError{
			Code: CodeInvalidChunk, CommittedThrough: existing.CommittedThrough,
			Err: errors.New("Collector Trace is tombstoned"),
		}
	}
	merged, committedThrough, err := validateAndMergeTraceChunk(ctx, memoryTrace{
		descriptor: existing.Trace,
		records:    existing.Records,
	}, chunk)
	if err != nil {
		return 0, err
	}

	newRecords := merged.records[len(existing.Records):]
	if err := insertTraceRecords(ctx, tx, newRecords); err != nil {
		return 0, err
	}
	if _, err := tx.Exec(ctx, `
		update obs_traces
		set committed_sequence = $2, updated_at = now()
		where trace_id = $1
	`, chunk.Trace.TraceID, committedThrough); err != nil {
		return 0, err
	}
	if _, err := tx.Exec(ctx, `
		insert into obs_projection_queue (trace_id, target_sequence)
		values ($1, $2)
		on conflict (trace_id) do update
		set target_sequence = greatest(obs_projection_queue.target_sequence, excluded.target_sequence),
			available_at = least(obs_projection_queue.available_at, now()),
			updated_at = now()
	`, chunk.Trace.TraceID, committedThrough); err != nil {
		return 0, err
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, err
	}
	return committedThrough, nil
}

func (s *PostgresStore) LoadTrace(ctx context.Context, traceID agentobs.TraceID) (StoredTrace, error) {
	if s == nil || s.pool == nil {
		return StoredTrace{}, errors.New("nil Collector PostgreSQL Store")
	}
	return loadStoredTrace(ctx, s.pool, traceID, false)
}

type postgresQuerier interface {
	QueryRow(context.Context, string, ...any) pgx.Row
	Query(context.Context, string, ...any) (pgx.Rows, error)
}

func loadStoredTrace(ctx context.Context, query postgresQuerier, traceID agentobs.TraceID, forUpdate bool) (StoredTrace, error) {
	lockClause := ""
	if forUpdate {
		lockClause = " for update"
	}
	var stored StoredTrace
	var tombstonedAt *time.Time
	err := query.QueryRow(ctx, `
		select trace_id, run_id, chat_id, notebook_id, root_span_id, agent_name,
			schema_version, semantic_convention_version, committed_sequence,
			projected_sequence, tombstoned_at
		from obs_traces
		where trace_id = $1`+lockClause,
		traceID,
	).Scan(
		&stored.Trace.TraceID, &stored.Trace.RunID, &stored.Trace.ChatID,
		&stored.Trace.NotebookID, &stored.Trace.RootSpanID, &stored.Trace.AgentName,
		&stored.Trace.SchemaVersion, &stored.Trace.SemanticConventionVersion,
		&stored.CommittedThrough, &stored.ProjectedThrough, &tombstonedAt,
	)
	if err != nil {
		return StoredTrace{}, err
	}
	stored.Tombstoned = tombstonedAt != nil
	records, err := query.Query(ctx, `
		select sequence, schema_version, identity_key, kind, span_id, parent_span_id,
			target_trace_id, target_span_id, name, occurred_at_unix_nano,
			payload_version, canonical_payload, canonical_sha256
		from obs_trace_records
		where trace_id = $1
		order by sequence
	`, traceID)
	if err != nil {
		return StoredTrace{}, err
	}
	defer records.Close()
	for records.Next() {
		envelope, err := scanStoredRecord(records, traceID)
		if err != nil {
			return StoredTrace{}, err
		}
		stored.Records = append(stored.Records, envelope)
	}
	if err := records.Err(); err != nil {
		return StoredTrace{}, err
	}
	if len(stored.Records) != stored.CommittedThrough {
		return StoredTrace{}, fmt.Errorf("Collector Trace cursor %d has %d records", stored.CommittedThrough, len(stored.Records))
	}
	return stored, nil
}

type rowScanner interface {
	Scan(...any) error
}

func scanStoredRecord(row rowScanner, traceID agentobs.TraceID) (SequencedRecord, error) {
	var envelope SequencedRecord
	var kind string
	var spanID, parentSpanID, targetTraceID, targetSpanID string
	var occurredAtUnixNano int64
	var payload []byte
	if err := row.Scan(
		&envelope.Sequence, &envelope.Record.SchemaVersion, &envelope.Record.IdentityKey,
		&kind, &spanID, &parentSpanID, &targetTraceID, &targetSpanID,
		&envelope.Record.Name, &occurredAtUnixNano, &envelope.Record.PayloadVersion,
		&payload, &envelope.CanonicalSHA256,
	); err != nil {
		return SequencedRecord{}, err
	}
	canonicalPayload, err := agentobs.DecodeCanonicalPayload(payload)
	if err != nil {
		return SequencedRecord{}, err
	}
	envelope.Record.Kind = agentobs.RecordKind(kind)
	envelope.Record.TraceID = traceID
	envelope.Record.SpanID = agentobs.SpanID(spanID)
	envelope.Record.ParentSpanID = agentobs.SpanID(parentSpanID)
	envelope.Record.TargetTraceID = agentobs.TraceID(targetTraceID)
	envelope.Record.TargetSpanID = agentobs.SpanID(targetSpanID)
	envelope.Record.OccurredAt = time.Unix(0, occurredAtUnixNano).UTC()
	envelope.Record.SemanticConventionVersion = canonicalPayload.SemanticConventionVersion
	envelope.Record.Status = canonicalPayload.Status
	envelope.Record.Attributes = canonicalPayload.Attributes
	if err := envelope.Record.Validate(); err != nil {
		return SequencedRecord{}, err
	}
	hash, err := envelope.Record.CanonicalHash()
	if err != nil {
		return SequencedRecord{}, err
	}
	if envelope.CanonicalSHA256 != hex.EncodeToString(hash[:]) {
		return SequencedRecord{}, errors.New("stored Collector canonical hash mismatch")
	}
	return envelope, nil
}

func insertTraceRecords(ctx context.Context, tx pgx.Tx, records []SequencedRecord) error {
	if len(records) == 0 {
		return nil
	}
	batch := &pgx.Batch{}
	for _, envelope := range records {
		payload, err := envelope.Record.CanonicalPayload()
		if err != nil {
			return err
		}
		batch.Queue(`
			insert into obs_trace_records (
				trace_id, sequence, schema_version, identity_key, kind, span_id,
				parent_span_id, target_trace_id, target_span_id, name, occurred_at,
				occurred_at_unix_nano, payload_version, canonical_payload, canonical_sha256
			) values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15)
		`, envelope.Record.TraceID, envelope.Sequence, envelope.Record.SchemaVersion,
			envelope.Record.IdentityKey, string(envelope.Record.Kind), envelope.Record.SpanID,
			envelope.Record.ParentSpanID, envelope.Record.TargetTraceID, envelope.Record.TargetSpanID,
			envelope.Record.Name, envelope.Record.OccurredAt, envelope.Record.OccurredAt.UnixNano(),
			envelope.Record.PayloadVersion, payload, envelope.CanonicalSHA256)
	}
	results := tx.SendBatch(ctx, batch)
	for range records {
		if _, err := results.Exec(); err != nil {
			_ = results.Close()
			return err
		}
	}
	return results.Close()
}
