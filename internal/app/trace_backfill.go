package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/huangxinxinyu/nano-notebook/internal/agentobs"
	"github.com/huangxinxinyu/nano-notebook/internal/collector"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type legacyTraceEnvelope struct {
	traceID    agentobs.TraceID
	runID      string
	chatID     string
	notebookID string
	rootSpanID agentobs.SpanID
	schema     int
	agentName  string
}

type legacyTraceRecord struct {
	sequence int
	record   agentobs.Record
}

func backfillLegacyAgentTraces(ctx context.Context, pool *pgxpool.Pool) error {
	tx, err := pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `select pg_advisory_xact_lock(hashtextextended('agent_trace_outbox_backfill', 0))`); err != nil {
		return err
	}
	rows, err := tx.Query(ctx, `
		select t.trace_id, t.run_id, r.chat_id, c.notebook_id, t.root_span_id,
			t.schema_version, 'nano-research-agent'
		from agent_traces t
		join agent_runs r on r.id = t.run_id
		join chat_chats c on c.id = r.chat_id
		order by t.created_at, t.trace_id
	`)
	if err != nil {
		return err
	}
	envelopes := make([]legacyTraceEnvelope, 0)
	for rows.Next() {
		var envelope legacyTraceEnvelope
		if err := rows.Scan(
			&envelope.traceID, &envelope.runID, &envelope.chatID, &envelope.notebookID,
			&envelope.rootSpanID, &envelope.schema, &envelope.agentName,
		); err != nil {
			rows.Close()
			return err
		}
		envelopes = append(envelopes, envelope)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()

	for _, envelope := range envelopes {
		records, err := loadLegacyTraceRecords(ctx, tx, envelope)
		if err != nil {
			return fmt.Errorf("load legacy Trace %s: %w", envelope.traceID, err)
		}
		if len(records) == 0 {
			return fmt.Errorf("legacy Trace %s has no records", envelope.traceID)
		}
		var existingTraceID agentobs.TraceID
		err = tx.QueryRow(ctx, `select trace_id from agent_trace_refs where run_id = $1`, envelope.runID).Scan(&existingTraceID)
		if err == nil {
			if existingTraceID != envelope.traceID {
				return fmt.Errorf("legacy Trace %s conflicts with Outbox Trace %s for Run %s", envelope.traceID, existingTraceID, envelope.runID)
			}
			if err := verifyBackfilledTrace(ctx, tx, envelope, records); err != nil {
				return err
			}
			continue
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		semanticVersion := records[0].record.SemanticConventionVersion
		if _, err := tx.Exec(ctx, `
			insert into agent_trace_refs(
				trace_id, run_id, chat_id, notebook_id, root_span_id, agent_name,
				schema_version, semantic_convention_version
			) values ($1, $2, $3, $4, $5, $6, $7, $8)
		`, envelope.traceID, envelope.runID, envelope.chatID, envelope.notebookID,
			envelope.rootSpanID, envelope.agentName, envelope.schema, semanticVersion); err != nil {
			return err
		}
		for _, legacy := range records {
			if err := insertBackfilledTraceRecord(ctx, tx, legacy); err != nil {
				return fmt.Errorf("backfill legacy Trace %s sequence %d: %w", envelope.traceID, legacy.sequence, err)
			}
		}
		if err := verifyBackfilledTrace(ctx, tx, envelope, records); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

func loadLegacyTraceRecords(ctx context.Context, tx pgx.Tx, envelope legacyTraceEnvelope) ([]legacyTraceRecord, error) {
	rows, err := tx.Query(ctx, `
		select sequence_no, identity_key, record_kind, span_id, parent_span_id,
			name, target_trace_id, target_span_id, occurred_at, payload_version,
			payload::text, payload_sha256
		from agent_trace_records where trace_id = $1 order by sequence_no
	`, envelope.traceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	records := make([]legacyTraceRecord, 0)
	for rows.Next() {
		var legacy legacyTraceRecord
		var kind string
		var parentSpanID, targetTraceID, targetSpanID *string
		var payloadText, payloadHash string
		if err := rows.Scan(
			&legacy.sequence, &legacy.record.IdentityKey, &kind, &legacy.record.SpanID,
			&parentSpanID, &legacy.record.Name, &targetTraceID, &targetSpanID,
			&legacy.record.OccurredAt, &legacy.record.PayloadVersion, &payloadText, &payloadHash,
		); err != nil {
			return nil, err
		}
		payload, err := agentobs.DecodeCanonicalPayload([]byte(payloadText))
		if err != nil {
			return nil, err
		}
		legacy.record.SchemaVersion = envelope.schema
		legacy.record.SemanticConventionVersion = payload.SemanticConventionVersion
		legacy.record.TraceID = envelope.traceID
		legacy.record.Kind = agentobs.RecordKind(kind)
		legacy.record.Status = payload.Status
		legacy.record.Attributes = payload.Attributes
		if parentSpanID != nil {
			legacy.record.ParentSpanID = agentobs.SpanID(*parentSpanID)
		}
		if targetTraceID != nil {
			legacy.record.TargetTraceID = agentobs.TraceID(*targetTraceID)
		}
		if targetSpanID != nil {
			legacy.record.TargetSpanID = agentobs.SpanID(*targetSpanID)
		}
		canonicalPayload, err := legacy.record.CanonicalPayload()
		if err != nil {
			return nil, err
		}
		hash := sha256.Sum256(canonicalPayload)
		if hex.EncodeToString(hash[:]) != payloadHash {
			return nil, errors.New("legacy Trace payload hash mismatch")
		}
		if legacy.sequence != len(records)+1 {
			return nil, errors.New("legacy Trace sequence is not contiguous")
		}
		records = append(records, legacy)
	}
	return records, rows.Err()
}

func insertBackfilledTraceRecord(ctx context.Context, tx pgx.Tx, legacy legacyTraceRecord) error {
	payload, err := legacy.record.CanonicalPayload()
	if err != nil {
		return err
	}
	payloadHash := sha256.Sum256(payload)
	canonicalHash, err := legacy.record.CanonicalHash()
	if err != nil {
		return err
	}
	canonicalHex := hex.EncodeToString(canonicalHash[:])
	encoded, err := json.Marshal(collector.SequencedRecord{
		Sequence: legacy.sequence, Record: legacy.record, CanonicalSHA256: canonicalHex,
	})
	if err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `
		insert into agentobs_outbox_records(
			trace_id, sequence_no, identity_key, record_kind, span_id,
			parent_span_id, name, target_trace_id, target_span_id,
			occurred_at, occurred_at_unix_nano, payload_version, payload,
			payload_sha256, canonical_sha256, encoded_bytes
		) values (
			$1, $2, $3, $4, $5, nullif($6, ''), $7, nullif($8, ''), nullif($9, ''),
			$10, $11, $12, $13::jsonb, $14, $15, $16
		)
	`, legacy.record.TraceID, legacy.sequence, legacy.record.IdentityKey, legacy.record.Kind,
		legacy.record.SpanID, legacy.record.ParentSpanID, legacy.record.Name,
		legacy.record.TargetTraceID, legacy.record.TargetSpanID, legacy.record.OccurredAt,
		legacy.record.OccurredAt.UnixNano(), legacy.record.PayloadVersion, string(payload),
		hex.EncodeToString(payloadHash[:]), canonicalHex, len(encoded)); err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `select nano_advance_agent_trace_ref($1, $2, $3, $4)`,
		legacy.record.TraceID, legacy.sequence, legacy.record.Kind, legacy.record.SpanID)
	return err
}

func verifyBackfilledTrace(ctx context.Context, tx pgx.Tx, envelope legacyTraceEnvelope, records []legacyTraceRecord) error {
	var rootSpanID agentobs.SpanID
	var schema, nextSequence, storedCount int
	if err := tx.QueryRow(ctx, `
		select r.root_span_id, r.schema_version, r.next_sequence, count(o.sequence_no)
		from agent_trace_refs r
		left join agentobs_outbox_records o on o.trace_id = r.trace_id
		where r.trace_id = $1
		group by r.trace_id
	`, envelope.traceID).Scan(&rootSpanID, &schema, &nextSequence, &storedCount); err != nil {
		return err
	}
	if rootSpanID != envelope.rootSpanID || schema != envelope.schema || nextSequence != len(records)+1 || storedCount != len(records) {
		return fmt.Errorf("legacy Trace %s backfill count or envelope drift", envelope.traceID)
	}
	for _, legacy := range records {
		wantHash, err := legacy.record.CanonicalHash()
		if err != nil {
			return err
		}
		var gotHash string
		if err := tx.QueryRow(ctx, `
			select canonical_sha256 from agentobs_outbox_records
			where trace_id = $1 and sequence_no = $2
		`, envelope.traceID, legacy.sequence).Scan(&gotHash); err != nil {
			return err
		}
		if gotHash != hex.EncodeToString(wantHash[:]) {
			return fmt.Errorf("legacy Trace %s sequence %d canonical hash drift", envelope.traceID, legacy.sequence)
		}
	}
	return nil
}
