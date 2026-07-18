package collector

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/huangxinxinyu/nano-notebook/internal/agentobs"
	"github.com/huangxinxinyu/nano-notebook/internal/objectstore"
	"github.com/huangxinxinyu/nano-notebook/internal/replay"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

type PostgresStore struct {
	pool           *pgxpool.Pool
	stagingObjects objectstore.Store
	replayObjects  objectstore.Store
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

func NewPostgresStoreWithReplay(pool *pgxpool.Pool, stagingObjects, replayObjects objectstore.Store) (*PostgresStore, error) {
	if pool == nil || stagingObjects == nil || replayObjects == nil {
		return nil, errors.New("Collector Replay Store dependencies are incomplete")
	}
	return &PostgresStore{pool: pool, stagingObjects: stagingObjects, replayObjects: replayObjects}, nil
}

func (s *PostgresStore) CommitTraceChunk(ctx context.Context, chunk TraceChunk) (int, error) {
	if s == nil || s.pool == nil {
		return 0, errors.New("nil Collector PostgreSQL Store")
	}
	if err := validateTraceDescriptor(chunk.Trace); err != nil {
		return 0, &ChunkError{Code: CodeInvalidChunk, Err: err}
	}
	if err := validateAttachmentDescriptors(chunk); err != nil {
		return 0, &ChunkError{Code: CodeInvalidChunk, Err: err}
	}
	var tombstoned bool
	if err := s.pool.QueryRow(ctx, `select exists(select 1 from obs_trace_tombstones where trace_id = $1)`, chunk.Trace.TraceID).Scan(&tombstoned); err != nil {
		return 0, err
	}
	if tombstoned {
		return 0, &ChunkError{Code: CodeTombstoned, Err: errors.New("Collector Trace is tombstoned")}
	}
	preparedAttachments, err := s.prepareReplayAttachments(ctx, chunk)
	if err != nil {
		return 0, err
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx)
	if err := lockTraceID(ctx, tx, chunk.Trace.TraceID); err != nil {
		return 0, err
	}
	if err := tx.QueryRow(ctx, `select exists(select 1 from obs_trace_tombstones where trace_id = $1)`, chunk.Trace.TraceID).Scan(&tombstoned); err != nil {
		return 0, err
	}
	if tombstoned {
		return 0, &ChunkError{Code: CodeTombstoned, Err: errors.New("Collector Trace is tombstoned")}
	}

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
			Code: CodeTombstoned, CommittedThrough: existing.CommittedThrough,
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
	if err := insertPayloadRefs(ctx, tx, chunk.Trace.TraceID, preparedAttachments); err != nil {
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

type preparedAttachment struct {
	descriptor AttachmentDescriptor
	objectKey  string
}

func (s *PostgresStore) prepareReplayAttachments(ctx context.Context, chunk TraceChunk) ([]preparedAttachment, error) {
	if len(chunk.Attachments) == 0 {
		return nil, nil
	}
	if s.stagingObjects == nil || s.replayObjects == nil {
		return nil, &ChunkError{Code: CodeAttachmentUnavailable, Retryable: true, Err: errors.New("Collector Replay object stores are unavailable")}
	}
	prepared := make([]preparedAttachment, 0, len(chunk.Attachments))
	for _, descriptor := range chunk.Attachments {
		objectKey := "agent-replay/" + descriptor.AttachmentID
		existing, found, err := loadPayloadRef(ctx, s.pool, descriptor.AttachmentID)
		if err != nil {
			return nil, err
		}
		if found {
			if err := reconcilePayloadRef(existing, chunk.Trace.TraceID, descriptor, objectKey); err != nil {
				return nil, err
			}
			info, statErr := s.replayObjects.Stat(ctx, objectKey)
			if statErr == nil && info.Size == int64(descriptor.CiphertextBytes) {
				prepared = append(prepared, preparedAttachment{descriptor: descriptor, objectKey: objectKey})
				continue
			}
		}
		ciphertext, err := s.stagingObjects.Get(ctx, descriptor.StagingObjectKey, int64(descriptor.CiphertextBytes))
		if err != nil {
			if errors.Is(err, objectstore.ErrObjectTooLarge) {
				return nil, &ChunkError{
					Code: CodeAttachmentIntegrity,
					Err:  errors.New("Collector Replay ciphertext exceeds its declared size"),
				}
			}
			return nil, &ChunkError{
				Code: CodeAttachmentUnavailable, Retryable: true,
				Err: fmt.Errorf("Collector Replay staging object unavailable: %w", err),
			}
		}
		if len(ciphertext) != descriptor.CiphertextBytes {
			return nil, &ChunkError{Code: CodeAttachmentIntegrity, Err: errors.New("Collector Replay ciphertext size changed")}
		}
		digest := sha256.Sum256(ciphertext)
		if !bytes.Equal([]byte(descriptor.CiphertextSHA256), []byte(hex.EncodeToString(digest[:]))) {
			return nil, &ChunkError{Code: CodeAttachmentIntegrity, Err: errors.New("Collector Replay ciphertext hash changed")}
		}
		if err := s.replayObjects.Put(ctx, objectKey, ciphertext); err != nil {
			return nil, &ChunkError{
				Code: CodeAttachmentUnavailable, Retryable: true,
				Err: fmt.Errorf("store Collector Replay object: %w", err),
			}
		}
		prepared = append(prepared, preparedAttachment{descriptor: descriptor, objectKey: objectKey})
	}
	return prepared, nil
}

type storedPayloadRef struct {
	attachmentID     string
	traceID          agentobs.TraceID
	recordSequence   int
	class            replay.Class
	schemaVersion    int
	plaintextSHA256  string
	objectKey        string
	ciphertextBytes  int
	ciphertextSHA256 string
	compression      string
	encryption       string
	keyID            string
	wrappedKey       []byte
	nonce            []byte
	expiresAtNano    int64
}

func loadPayloadRef(ctx context.Context, query postgresQuerier, attachmentID string) (storedPayloadRef, bool, error) {
	var stored storedPayloadRef
	err := query.QueryRow(ctx, `
		select attachment_id::text, trace_id, record_sequence, class, schema_version,
			plaintext_sha256, object_key, ciphertext_bytes, ciphertext_sha256,
			compression, encryption, key_id, wrapped_key, nonce, expires_at_unix_nano
		from obs_payload_refs where attachment_id = $1
	`, attachmentID).Scan(
		&stored.attachmentID, &stored.traceID, &stored.recordSequence, &stored.class,
		&stored.schemaVersion, &stored.plaintextSHA256, &stored.objectKey,
		&stored.ciphertextBytes, &stored.ciphertextSHA256, &stored.compression,
		&stored.encryption, &stored.keyID, &stored.wrappedKey, &stored.nonce, &stored.expiresAtNano,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return storedPayloadRef{}, false, nil
	}
	if err != nil {
		return storedPayloadRef{}, false, err
	}
	return stored, true, nil
}

func reconcilePayloadRef(stored storedPayloadRef, traceID agentobs.TraceID, descriptor AttachmentDescriptor, objectKey string) error {
	if stored.attachmentID != descriptor.AttachmentID || stored.traceID != traceID || stored.recordSequence != descriptor.RecordSequence ||
		stored.class != descriptor.Class || stored.schemaVersion != descriptor.SchemaVersion ||
		stored.plaintextSHA256 != descriptor.PlaintextSHA256 || stored.objectKey != objectKey ||
		stored.ciphertextBytes != descriptor.CiphertextBytes || stored.ciphertextSHA256 != descriptor.CiphertextSHA256 ||
		stored.compression != descriptor.Compression || stored.encryption != descriptor.Encryption ||
		stored.keyID != descriptor.KeyID || !bytes.Equal(stored.wrappedKey, descriptor.WrappedKey) ||
		!bytes.Equal(stored.nonce, descriptor.Nonce) || stored.expiresAtNano != descriptor.ExpiresAt.UnixNano() {
		return &ChunkError{Code: CodeIdentityConflict, Err: errors.New("Collector Replay Attachment identity changed")}
	}
	return nil
}

func insertPayloadRefs(ctx context.Context, tx pgx.Tx, traceID agentobs.TraceID, attachments []preparedAttachment) error {
	for _, prepared := range attachments {
		descriptor := prepared.descriptor
		if _, err := tx.Exec(ctx, `
			insert into obs_payload_refs(
				attachment_id, trace_id, record_sequence, class, schema_version,
				plaintext_sha256, object_key, ciphertext_bytes, ciphertext_sha256,
				compression, encryption, key_id, wrapped_key, nonce,
				expires_at, expires_at_unix_nano
			) values (
				$1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16
			)
			on conflict (attachment_id) do nothing
		`, descriptor.AttachmentID, traceID, descriptor.RecordSequence,
			descriptor.Class, descriptor.SchemaVersion, descriptor.PlaintextSHA256,
			prepared.objectKey, descriptor.CiphertextBytes, descriptor.CiphertextSHA256,
			descriptor.Compression, descriptor.Encryption, descriptor.KeyID,
			descriptor.WrappedKey, descriptor.Nonce, descriptor.ExpiresAt, descriptor.ExpiresAt.UnixNano()); err != nil {
			var pgErr *pgconn.PgError
			if errors.As(err, &pgErr) && pgErr.Code == "23505" {
				return &ChunkError{Code: CodeIdentityConflict, Err: errors.New("Collector Replay Attachment identity conflicts with stored metadata")}
			}
			return err
		}
		stored, found, err := loadPayloadRef(ctx, tx, descriptor.AttachmentID)
		if err != nil {
			return err
		}
		if !found {
			return errors.New("Collector Replay Attachment metadata was not committed")
		}
		if err := reconcilePayloadRef(stored, traceID, descriptor, prepared.objectKey); err != nil {
			return err
		}
	}
	return nil
}

func (s *PostgresStore) TombstoneTrace(ctx context.Context, command PurgeCommand) error {
	if s == nil || s.pool == nil {
		return errors.New("nil Collector PostgreSQL Store")
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if err := lockTraceID(ctx, tx, command.TraceID); err != nil {
		return err
	}
	var existingTraceID agentobs.TraceID
	var existingRunID string
	var existingVersion int
	var existingProducerID string
	var existingRequestedAtUnixNano int64
	err = tx.QueryRow(ctx, `
		select trace_id, run_id, command_version, producer_id, requested_at_unix_nano
		from obs_purge_commands where command_id = $1
	`, command.CommandID).Scan(
		&existingTraceID, &existingRunID, &existingVersion, &existingProducerID, &existingRequestedAtUnixNano,
	)
	if err == nil {
		if existingTraceID != command.TraceID || existingRunID != command.RunID ||
			existingVersion != command.CommandVersion || existingProducerID != command.ProducerID ||
			existingRequestedAtUnixNano != command.RequestedAt.UnixNano() {
			return &PurgeCommandError{Code: CodeIdentityConflict, Err: errors.New("Collector purge command identity changed")}
		}
		return tx.Commit(ctx)
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	if _, err := tx.Exec(ctx, `
		insert into obs_trace_tombstones (trace_id, run_id)
		values ($1, $2)
		on conflict (trace_id) do nothing
	`, command.TraceID, command.RunID); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `
		insert into obs_purge_commands (
			command_id, trace_id, run_id, command_version, producer_id, requested_at, requested_at_unix_nano
		) values ($1, $2, $3, $4, $5, $6, $7)
	`, command.CommandID, command.TraceID, command.RunID, command.CommandVersion,
		command.ProducerID, command.RequestedAt, command.RequestedAt.UnixNano()); err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return &PurgeCommandError{Code: CodeIdentityConflict, Err: errors.New("Collector purge command identity changed")}
		}
		return err
	}
	if _, err := tx.Exec(ctx, `
		update obs_traces set tombstoned_at = coalesce(tombstoned_at, now()), updated_at = now()
		where trace_id = $1
	`, command.TraceID); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `
		insert into obs_purge_queue (trace_id) values ($1)
		on conflict (trace_id) do nothing
	`, command.TraceID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func lockTraceID(ctx context.Context, tx pgx.Tx, traceID agentobs.TraceID) error {
	_, err := tx.Exec(ctx, `select pg_advisory_xact_lock(hashtextextended($1, 0))`, traceID)
	return err
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
