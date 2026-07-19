package app

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/huangxinxinyu/nano-notebook/internal/agentobs"
	"github.com/huangxinxinyu/nano-notebook/internal/collector"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type TraceMigrationVerificationReport struct {
	TraceCount  int
	RecordCount int
}

// VerifyCollectorTraceMigration compares the immutable Sprint 4 Trace authority
// with Collector raw storage. Callers must keep Agent admission and delivery
// stopped for the duration so the two independent database snapshots describe
// the same maintenance boundary.
func VerifyCollectorTraceMigration(ctx context.Context, applicationPool, collectorPool *pgxpool.Pool) (TraceMigrationVerificationReport, error) {
	if applicationPool == nil || collectorPool == nil {
		return TraceMigrationVerificationReport{}, errors.New("Trace migration verifier requires Application and Collector databases")
	}
	applicationTx, err := applicationPool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return TraceMigrationVerificationReport{}, fmt.Errorf("begin Application verification snapshot: %w", err)
	}
	defer func() { _ = applicationTx.Rollback(ctx) }()
	collectorTx, err := collectorPool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return TraceMigrationVerificationReport{}, fmt.Errorf("begin Collector verification snapshot: %w", err)
	}
	defer func() { _ = collectorTx.Rollback(ctx) }()

	rows, err := applicationTx.Query(ctx, `
		select t.trace_id, t.run_id, r.chat_id, c.notebook_id, t.root_span_id,
			t.schema_version, 'nano-research-agent'
		from agent_traces t
		join agent_runs r on r.id = t.run_id
		join chat_chats c on c.id = r.chat_id
		order by t.created_at, t.trace_id
	`)
	if err != nil {
		return TraceMigrationVerificationReport{}, fmt.Errorf("list Sprint 4 Trace authority: %w", err)
	}
	var envelopes []legacyTraceEnvelope
	for rows.Next() {
		var envelope legacyTraceEnvelope
		if err := rows.Scan(
			&envelope.traceID, &envelope.runID, &envelope.chatID, &envelope.notebookID,
			&envelope.rootSpanID, &envelope.schema, &envelope.agentName,
		); err != nil {
			rows.Close()
			return TraceMigrationVerificationReport{}, err
		}
		envelopes = append(envelopes, envelope)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return TraceMigrationVerificationReport{}, err
	}
	rows.Close()

	report := TraceMigrationVerificationReport{}
	for _, envelope := range envelopes {
		records, err := loadLegacyTraceRecords(ctx, applicationTx, envelope)
		if err != nil {
			return TraceMigrationVerificationReport{}, fmt.Errorf("verify Sprint 4 Trace %s: %w", envelope.traceID, err)
		}
		if len(records) == 0 {
			return TraceMigrationVerificationReport{}, fmt.Errorf("Sprint 4 Trace %s has no records", envelope.traceID)
		}
		if err := verifyCollectorTrace(ctx, collectorTx, envelope, records); err != nil {
			return TraceMigrationVerificationReport{}, err
		}
		report.TraceCount++
		report.RecordCount += len(records)
	}
	return report, nil
}

type collectorMigrationTrace struct {
	descriptor        collector.TraceDescriptor
	committedSequence int
	tombstonedAt      *time.Time
}

type collectorMigrationRecord struct {
	sequence        int
	identityKey     string
	canonicalSHA256 string
	record          agentobs.Record
}

func verifyCollectorTrace(ctx context.Context, tx pgx.Tx, envelope legacyTraceEnvelope, legacyRecords []legacyTraceRecord) error {
	var stored collectorMigrationTrace
	err := tx.QueryRow(ctx, `
		select trace_id, run_id, chat_id, notebook_id, root_span_id, agent_name,
			schema_version, semantic_convention_version, committed_sequence, tombstoned_at
		from obs_traces where trace_id = $1
	`, envelope.traceID).Scan(
		&stored.descriptor.TraceID, &stored.descriptor.RunID, &stored.descriptor.ChatID,
		&stored.descriptor.NotebookID, &stored.descriptor.RootSpanID, &stored.descriptor.AgentName,
		&stored.descriptor.SchemaVersion, &stored.descriptor.SemanticConventionVersion,
		&stored.committedSequence, &stored.tombstonedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("Sprint 4 Trace %s is missing from Collector", envelope.traceID)
	}
	if err != nil {
		return fmt.Errorf("load Collector Trace %s: %w", envelope.traceID, err)
	}
	wantDescriptor := collector.TraceDescriptor{
		TraceID: envelope.traceID, RunID: envelope.runID, ChatID: envelope.chatID,
		NotebookID: envelope.notebookID, RootSpanID: envelope.rootSpanID,
		AgentName: envelope.agentName, SchemaVersion: envelope.schema,
		SemanticConventionVersion: legacyRecords[0].record.SemanticConventionVersion,
	}
	if stored.descriptor != wantDescriptor {
		return fmt.Errorf("Sprint 4 Trace %s Collector descriptor drift", envelope.traceID)
	}
	if stored.tombstonedAt != nil {
		return fmt.Errorf("Sprint 4 Trace %s is tombstoned in Collector", envelope.traceID)
	}
	if stored.committedSequence != len(legacyRecords) {
		return fmt.Errorf("Sprint 4 Trace %s Collector record count drift: committed=%d source=%d", envelope.traceID, stored.committedSequence, len(legacyRecords))
	}

	rows, err := tx.Query(ctx, `
		select sequence, schema_version, identity_key, kind, span_id, parent_span_id,
			target_trace_id, target_span_id, name, occurred_at_unix_nano,
			payload_version, canonical_payload, canonical_sha256
		from obs_trace_records where trace_id = $1 order by sequence
	`, envelope.traceID)
	if err != nil {
		return fmt.Errorf("load Collector Trace %s records: %w", envelope.traceID, err)
	}
	collectorRecords := make([]collectorMigrationRecord, 0, len(legacyRecords))
	for rows.Next() {
		var storedRecord collectorMigrationRecord
		var kind, spanID, parentSpanID, targetTraceID, targetSpanID string
		var occurredAtUnixNano int64
		var canonicalPayload []byte
		if err := rows.Scan(
			&storedRecord.sequence, &storedRecord.record.SchemaVersion, &storedRecord.identityKey,
			&kind, &spanID, &parentSpanID, &targetTraceID, &targetSpanID,
			&storedRecord.record.Name, &occurredAtUnixNano, &storedRecord.record.PayloadVersion,
			&canonicalPayload, &storedRecord.canonicalSHA256,
		); err != nil {
			rows.Close()
			return err
		}
		payload, err := agentobs.DecodeCanonicalPayload(canonicalPayload)
		if err != nil {
			rows.Close()
			return fmt.Errorf("Sprint 4 Trace %s Collector canonical payload drift: %w", envelope.traceID, err)
		}
		storedRecord.record.IdentityKey = storedRecord.identityKey
		storedRecord.record.Kind = agentobs.RecordKind(kind)
		storedRecord.record.TraceID = envelope.traceID
		storedRecord.record.SpanID = agentobs.SpanID(spanID)
		storedRecord.record.ParentSpanID = agentobs.SpanID(parentSpanID)
		storedRecord.record.TargetTraceID = agentobs.TraceID(targetTraceID)
		storedRecord.record.TargetSpanID = agentobs.SpanID(targetSpanID)
		storedRecord.record.OccurredAt = time.Unix(0, occurredAtUnixNano).UTC()
		storedRecord.record.SemanticConventionVersion = payload.SemanticConventionVersion
		storedRecord.record.Status = payload.Status
		storedRecord.record.Attributes = payload.Attributes
		if err := storedRecord.record.Validate(); err != nil {
			rows.Close()
			return fmt.Errorf("Sprint 4 Trace %s Collector record %d is invalid: %w", envelope.traceID, storedRecord.sequence, err)
		}
		computedHash, err := storedRecord.record.CanonicalHash()
		if err != nil {
			rows.Close()
			return err
		}
		if storedRecord.canonicalSHA256 != hex.EncodeToString(computedHash[:]) {
			rows.Close()
			return fmt.Errorf("Sprint 4 Trace %s Collector record %d canonical hash drift", envelope.traceID, storedRecord.sequence)
		}
		collectorRecords = append(collectorRecords, storedRecord)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	if len(collectorRecords) != len(legacyRecords) {
		return fmt.Errorf("Sprint 4 Trace %s Collector record count drift: stored=%d source=%d", envelope.traceID, len(collectorRecords), len(legacyRecords))
	}
	for index, legacy := range legacyRecords {
		storedRecord := collectorRecords[index]
		if storedRecord.sequence != legacy.sequence {
			return fmt.Errorf("Sprint 4 Trace %s Collector sequence drift at source sequence %d", envelope.traceID, legacy.sequence)
		}
		if storedRecord.identityKey != legacy.record.IdentityKey {
			return fmt.Errorf("Sprint 4 Trace %s Collector identity key drift at sequence %d", envelope.traceID, legacy.sequence)
		}
		wantHash, err := legacy.record.CanonicalHash()
		if err != nil {
			return err
		}
		if storedRecord.canonicalSHA256 != hex.EncodeToString(wantHash[:]) {
			return fmt.Errorf("Sprint 4 Trace %s Collector record %d canonical hash drift", envelope.traceID, legacy.sequence)
		}
	}
	return nil
}
