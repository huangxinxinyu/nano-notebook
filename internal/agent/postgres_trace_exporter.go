package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/huangxinxinyu/nano-notebook/internal/agentobs"
	"github.com/huangxinxinyu/nano-notebook/internal/collector"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

var (
	ErrTraceNotFound         = errors.New("durable Agent Trace not found")
	ErrTraceExporterShutdown = errors.New("PostgreSQL Trace Exporter is shut down")
)

type DurableTrace struct {
	TraceID       agentobs.TraceID
	RunID         string
	RootSpanID    agentobs.SpanID
	SchemaVersion int
	CreatedAt     time.Time
	Records       []agentobs.Record
}

type PostgresTraceExporter struct {
	pool     *pgxpool.Pool
	commit   func(context.Context, pgx.Tx) error
	mu       sync.RWMutex
	shutdown bool
}

type TraceExporterOption func(*PostgresTraceExporter)

func WithTraceCommitFunc(commit func(context.Context, pgx.Tx) error) TraceExporterOption {
	return func(exporter *PostgresTraceExporter) {
		if commit != nil {
			exporter.commit = commit
		}
	}
}

var _ agentobs.Exporter = (*PostgresTraceExporter)(nil)

func NewPostgresTraceExporter(pool *pgxpool.Pool, options ...TraceExporterOption) (*PostgresTraceExporter, error) {
	if pool == nil {
		return nil, errors.New("PostgreSQL Trace Exporter requires a pool")
	}
	exporter := &PostgresTraceExporter{
		pool:   pool,
		commit: func(ctx context.Context, tx pgx.Tx) error { return tx.Commit(ctx) },
	}
	for _, option := range options {
		option(exporter)
	}
	return exporter, nil
}

func CreateTraceInTx(ctx context.Context, tx pgx.Tx, runID string, root agentobs.Record) error {
	if tx == nil || strings.TrimSpace(runID) == "" {
		return errors.New("Trace admission dependencies are incomplete")
	}
	root = normalizeTraceRecord(root)
	if err := root.Validate(); err != nil {
		return err
	}
	if root.Kind != agentobs.RecordSpanStarted || root.ParentSpanID != "" {
		return fmt.Errorf("%w: Trace root must be a root Span start", agentobs.ErrLifecycle)
	}
	if _, err := tx.Exec(ctx, `
		insert into agent_trace_refs(
			trace_id, run_id, chat_id, notebook_id, root_span_id, agent_name,
			schema_version, semantic_convention_version
		)
		select $1, r.id, r.chat_id, c.notebook_id, $2, 'nano-research-agent', $3, $4
		from agent_runs r join chat_chats c on c.id = r.chat_id
		where r.id = $5
	`, root.TraceID, root.SpanID, root.SchemaVersion, root.SemanticConventionVersion, runID); err != nil {
		return err
	}
	return insertTraceRecord(ctx, tx, 1, root)
}

func (e *PostgresTraceExporter) Export(ctx context.Context, record agentobs.Record) error {
	if e == nil || e.pool == nil {
		return errors.New("nil PostgreSQL Trace Exporter")
	}
	record = normalizeTraceRecord(record)
	if err := record.Validate(); err != nil {
		return err
	}
	e.mu.RLock()
	defer e.mu.RUnlock()
	if e.shutdown {
		return ErrTraceExporterShutdown
	}
	var appendErr error
	for try := 0; try < 2; try++ {
		err := e.exportOnce(ctx, record)
		if err == nil {
			return nil
		}
		appendErr = err
		if errors.Is(err, agentobs.ErrIdentityConflict) || errors.Is(err, agentobs.ErrLifecycle) || errors.Is(err, agentobs.ErrLimitExceeded) || ctx.Err() != nil {
			return err
		}
		matched, reconcileErr := e.reconcile(ctx, record)
		if reconcileErr != nil {
			return errors.Join(err, reconcileErr)
		}
		if matched {
			return nil
		}
	}
	return fmt.Errorf("PostgreSQL Trace Exporter exhausted append retries: %w", appendErr)
}

func (e *PostgresTraceExporter) exportOnce(ctx context.Context, record agentobs.Record) error {
	tx, err := e.workerTx(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `select pg_advisory_xact_lock(hashtextextended($1, 0))`, "agent_trace:"+string(record.TraceID)); err != nil {
		return err
	}
	var schemaVersion, sequence int
	if err := tx.QueryRow(ctx, `
		select schema_version, next_sequence
		from agent_trace_refs where trace_id = $1
		for update`, record.TraceID).Scan(&schemaVersion, &sequence); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrTraceNotFound
		}
		return err
	}
	if schemaVersion != record.SchemaVersion {
		return fmt.Errorf("%w: Trace schema version changed", agentobs.ErrLifecycle)
	}
	existing, found, err := traceRecordByIdentity(ctx, tx, record.TraceID, record.IdentityKey, schemaVersion)
	if err != nil {
		return err
	}
	if found {
		return reconcileTraceRecord(existing, record)
	}
	if err := insertTraceRecord(ctx, tx, sequence, record); err != nil {
		return classifyTraceDatabaseError(err)
	}
	return e.commit(ctx, tx)
}

func (e *PostgresTraceExporter) reconcile(ctx context.Context, record agentobs.Record) (bool, error) {
	tx, err := e.workerTx(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(ctx)
	var schemaVersion int
	if err := tx.QueryRow(ctx, `select schema_version from agent_trace_refs where trace_id = $1`, record.TraceID).Scan(&schemaVersion); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return false, ErrTraceNotFound
		}
		return false, err
	}
	existing, found, err := traceRecordByIdentity(ctx, tx, record.TraceID, record.IdentityKey, schemaVersion)
	if err != nil || !found {
		return false, err
	}
	if err := reconcileTraceRecord(existing, record); err != nil {
		return false, err
	}
	return true, tx.Commit(ctx)
}

func (e *PostgresTraceExporter) workerTx(ctx context.Context) (pgx.Tx, error) {
	tx, err := e.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx, `set local role nano_worker`); err != nil {
		tx.Rollback(ctx)
		return nil, err
	}
	return tx, nil
}

func (e *PostgresTraceExporter) ForceFlush(context.Context) error {
	if e == nil {
		return errors.New("nil PostgreSQL Trace Exporter")
	}
	e.mu.RLock()
	defer e.mu.RUnlock()
	if e.shutdown {
		return ErrTraceExporterShutdown
	}
	return nil
}

func (e *PostgresTraceExporter) Shutdown(context.Context) error {
	if e == nil {
		return errors.New("nil PostgreSQL Trace Exporter")
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	e.shutdown = true
	return nil
}

func LoadDurableTraceByRun(ctx context.Context, db DBTX, runID string) (DurableTrace, error) {
	var trace DurableTrace
	if err := db.QueryRow(ctx, `
		select trace_id, run_id, root_span_id, schema_version, created_at
		from agent_trace_refs where run_id = $1`, runID).Scan(
		&trace.TraceID, &trace.RunID, &trace.RootSpanID, &trace.SchemaVersion, &trace.CreatedAt,
	); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return DurableTrace{}, ErrTraceNotFound
		}
		return DurableTrace{}, err
	}
	return loadDurableTraceRecords(ctx, db, trace)
}

func LoadDurableTrace(ctx context.Context, db DBTX, traceID agentobs.TraceID) (DurableTrace, error) {
	var trace DurableTrace
	if err := db.QueryRow(ctx, `
		select trace_id, run_id, root_span_id, schema_version, created_at
		from agent_trace_refs where trace_id = $1`, traceID).Scan(
		&trace.TraceID, &trace.RunID, &trace.RootSpanID, &trace.SchemaVersion, &trace.CreatedAt,
	); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return DurableTrace{}, ErrTraceNotFound
		}
		return DurableTrace{}, err
	}
	return loadDurableTraceRecords(ctx, db, trace)
}

func loadDurableTraceRecords(ctx context.Context, db DBTX, trace DurableTrace) (DurableTrace, error) {
	rows, err := db.Query(ctx, `
		select sequence_no, identity_key, record_kind, span_id, parent_span_id,
			name, target_trace_id, target_span_id, occurred_at, payload_version,
			payload::text, payload_sha256
		from agentobs_outbox_records
		where trace_id = $1
		order by sequence_no`, trace.TraceID)
	if err != nil {
		return DurableTrace{}, err
	}
	defer rows.Close()
	trace.Records = make([]agentobs.Record, 0)
	for rows.Next() {
		record, sequence, err := scanTraceRecord(rows, trace.TraceID, trace.SchemaVersion)
		if err != nil {
			return DurableTrace{}, err
		}
		if sequence != len(trace.Records)+1 {
			return DurableTrace{}, fmt.Errorf("%w: non-contiguous stored sequence", agentobs.ErrLifecycle)
		}
		trace.Records = append(trace.Records, record)
	}
	if err := rows.Err(); err != nil {
		return DurableTrace{}, err
	}
	if err := validateLoadedTrace(ctx, db, trace); err != nil {
		return DurableTrace{}, err
	}
	return trace, nil
}

func insertTraceRecord(ctx context.Context, db DBTX, sequence int, record agentobs.Record) error {
	payload, err := record.CanonicalPayload()
	if err != nil {
		return err
	}
	payloadHash := sha256.Sum256(payload)
	canonicalHash, err := record.CanonicalHash()
	if err != nil {
		return err
	}
	encodedRecord, err := json.Marshal(collector.SequencedRecord{
		Sequence: sequence, Record: record, CanonicalSHA256: hex.EncodeToString(canonicalHash[:]),
	})
	if err != nil {
		return err
	}
	encodedBytes := len(encodedRecord)
	if _, err := db.Exec(ctx, `
		insert into agentobs_outbox_records(
			trace_id, sequence_no, identity_key, record_kind, span_id,
			parent_span_id, name, target_trace_id, target_span_id,
			occurred_at, occurred_at_unix_nano, payload_version, payload,
			payload_sha256, canonical_sha256, encoded_bytes
		) values (
			$1, $2, $3, $4, $5, nullif($6, ''), $7, nullif($8, ''), nullif($9, ''),
			$10, $11, $12, $13::jsonb, $14, $15, $16
		)
	`, record.TraceID, sequence, record.IdentityKey, record.Kind, record.SpanID,
		record.ParentSpanID, record.Name, record.TargetTraceID, record.TargetSpanID,
		record.OccurredAt, record.OccurredAt.UnixNano(), record.PayloadVersion, string(payload),
		hex.EncodeToString(payloadHash[:]), hex.EncodeToString(canonicalHash[:]), encodedBytes); err != nil {
		return err
	}
	_, err = db.Exec(ctx, `select nano_advance_agent_trace_ref($1, $2, $3, $4)`,
		record.TraceID, sequence, record.Kind, record.SpanID)
	return err
}

const selectTraceRecordColumns = `
	sequence_no, identity_key, record_kind, span_id, parent_span_id,
	name, target_trace_id, target_span_id, occurred_at, payload_version,
	payload::text, payload_sha256`

func traceRecordByIdentity(ctx context.Context, db DBTX, traceID agentobs.TraceID, identity string, schemaVersion int) (agentobs.Record, bool, error) {
	record, _, err := scanTraceRecord(db.QueryRow(ctx, `
		select `+selectTraceRecordColumns+`
		from agentobs_outbox_records
		where trace_id = $1 and identity_key = $2`, traceID, identity), traceID, schemaVersion)
	if errors.Is(err, pgx.ErrNoRows) {
		return agentobs.Record{}, false, nil
	}
	return record, err == nil, err
}

type traceRecordScanner interface {
	Scan(...any) error
}

func scanTraceRecord(row traceRecordScanner, traceID agentobs.TraceID, schemaVersion int) (agentobs.Record, int, error) {
	var sequence int
	var record agentobs.Record
	var kind string
	var parentSpanID, targetTraceID, targetSpanID *string
	var payloadText, payloadHash string
	if err := row.Scan(
		&sequence, &record.IdentityKey, &kind, &record.SpanID, &parentSpanID,
		&record.Name, &targetTraceID, &targetSpanID, &record.OccurredAt, &record.PayloadVersion,
		&payloadText, &payloadHash,
	); err != nil {
		return agentobs.Record{}, 0, err
	}
	payload, err := agentobs.DecodeCanonicalPayload([]byte(payloadText))
	if err != nil {
		return agentobs.Record{}, 0, err
	}
	record.SchemaVersion = schemaVersion
	record.SemanticConventionVersion = payload.SemanticConventionVersion
	record.TraceID = traceID
	record.Kind = agentobs.RecordKind(kind)
	record.Status = payload.Status
	record.Attributes = payload.Attributes
	if parentSpanID != nil {
		record.ParentSpanID = agentobs.SpanID(*parentSpanID)
	}
	if targetTraceID != nil {
		record.TargetTraceID = agentobs.TraceID(*targetTraceID)
	}
	if targetSpanID != nil {
		record.TargetSpanID = agentobs.SpanID(*targetSpanID)
	}
	if err := record.Validate(); err != nil {
		return agentobs.Record{}, 0, err
	}
	canonical, err := record.CanonicalPayload()
	if err != nil {
		return agentobs.Record{}, 0, err
	}
	hash := sha256.Sum256(canonical)
	if hex.EncodeToString(hash[:]) != payloadHash {
		return agentobs.Record{}, 0, fmt.Errorf("%w: stored payload hash mismatch", agentobs.ErrIdentityConflict)
	}
	return record, sequence, nil
}

func reconcileTraceRecord(existing, candidate agentobs.Record) error {
	existingHash, err := existing.CanonicalHash()
	if err != nil {
		return err
	}
	candidateHash, err := candidate.CanonicalHash()
	if err != nil {
		return err
	}
	if existingHash != candidateHash {
		return fmt.Errorf("%w: %s", agentobs.ErrIdentityConflict, candidate.IdentityKey)
	}
	return nil
}

func validateLoadedTrace(ctx context.Context, db DBTX, trace DurableTrace) error {
	if len(trace.Records) == 0 || trace.Records[0].Kind != agentobs.RecordSpanStarted || trace.Records[0].SpanID != trace.RootSpanID || trace.Records[0].ParentSpanID != "" {
		return fmt.Errorf("%w: loaded Trace root is invalid", agentobs.ErrLifecycle)
	}
	spans := make(map[agentobs.SpanID]string)
	ended := make(map[agentobs.SpanID]bool)
	for _, record := range trace.Records {
		switch record.Kind {
		case agentobs.RecordSpanStarted:
			if _, duplicate := spans[record.SpanID]; duplicate {
				return fmt.Errorf("%w: duplicate Span start", agentobs.ErrLifecycle)
			}
			if record.ParentSpanID != "" {
				if _, exists := spans[record.ParentSpanID]; !exists {
					return fmt.Errorf("%w: unresolved loaded parent", agentobs.ErrLifecycle)
				}
			}
			spans[record.SpanID] = record.Name
		case agentobs.RecordSpanEnded:
			name, exists := spans[record.SpanID]
			if !exists || ended[record.SpanID] || name != record.Name {
				return fmt.Errorf("%w: invalid loaded terminal", agentobs.ErrLifecycle)
			}
			ended[record.SpanID] = true
		case agentobs.RecordEvent, agentobs.RecordLink:
			if _, exists := spans[record.SpanID]; !exists {
				return fmt.Errorf("%w: unresolved loaded source", agentobs.ErrLifecycle)
			}
		}
		if record.Kind == agentobs.RecordLink {
			if record.TargetTraceID == trace.TraceID {
				if _, exists := spans[record.TargetSpanID]; !exists {
					return agentobs.ErrUnresolvedLink
				}
				continue
			}
			var exists bool
			if err := db.QueryRow(ctx, `
				select exists(
					select 1 from agentobs_outbox_records
					where trace_id = $1 and span_id = $2 and record_kind = 'span_started'
					union all
					select 1 from agent_trace_refs
					where trace_id = $1 and root_span_id = $2
				)`, record.TargetTraceID, record.TargetSpanID).Scan(&exists); err != nil {
				return err
			}
			if !exists {
				return agentobs.ErrUnresolvedLink
			}
		}
	}
	return nil
}

func normalizeTraceRecord(record agentobs.Record) agentobs.Record {
	record.OccurredAt = record.OccurredAt.UTC().Truncate(time.Microsecond)
	return record
}

func classifyTraceDatabaseError(err error) error {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		return err
	}
	message := strings.ToLower(pgErr.Message)
	if strings.Contains(message, "limit exceeded") || strings.Contains(message, "too large") {
		return fmt.Errorf("%w: %s", agentobs.ErrLimitExceeded, pgErr.Message)
	}
	if pgErr.Code == "23514" || pgErr.Code == "23505" {
		return fmt.Errorf("%w: %s", agentobs.ErrLifecycle, pgErr.Message)
	}
	return err
}
