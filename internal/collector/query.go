package collector

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/huangxinxinyu/nano-notebook/internal/agentobs"
	"github.com/huangxinxinyu/nano-notebook/internal/objectstore"
	"github.com/huangxinxinyu/nano-notebook/internal/replay"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var (
	ErrTraceNotFound     = errors.New("Collector Trace not found")
	ErrProjectionPending = errors.New("Collector Trace projection is pending")
	ErrReplayNotFound    = errors.New("Collector Replay not found")
	ErrReplayExpired     = errors.New("Collector Replay expired")
	ErrReplayUnavailable = errors.New("Collector Replay unavailable")
)

type TraceQueryStore struct {
	pool          *pgxpool.Pool
	replayObjects objectstore.Store
}

type TraceListQuery struct {
	StartedAfterUnixNano  *int64
	StartedBeforeUnixNano *int64
	IdentityExact         string
	IdentityPrefix        string
	AgentName             string
	ModelName             string
	Status                string
	Active                *bool
	Cursor                string
	PageSize              int
}

type TraceListItem struct {
	Summary          TraceSummary `json:"summary"`
	CommittedThrough int          `json:"committed_sequence"`
	ProjectedThrough int          `json:"projected_sequence"`
	ProjectionLagged bool         `json:"projection_lagged"`
}

type TraceListResult struct {
	Items      []TraceListItem `json:"items"`
	NextCursor string          `json:"next_cursor,omitempty"`
}

type OpaqueReplay struct {
	AttachmentID string               `json:"attachment_id"`
	TraceID      agentobs.TraceID     `json:"trace_id"`
	SpanID       agentobs.SpanID      `json:"span_id"`
	Class        replay.Class         `json:"class"`
	Sealed       replay.SealedPayload `json:"sealed"`
}

type traceCursor struct {
	StartedAtUnixNano int64            `json:"started_at_unix_nano"`
	TraceID           agentobs.TraceID `json:"trace_id"`
}

func NewTraceQueryStore(pool *pgxpool.Pool, replayObjects objectstore.Store) (*TraceQueryStore, error) {
	if pool == nil {
		return nil, errors.New("Collector Trace query database is required")
	}
	return &TraceQueryStore{pool: pool, replayObjects: replayObjects}, nil
}

func (s *TraceQueryStore) List(ctx context.Context, query TraceListQuery) (TraceListResult, error) {
	if s == nil || s.pool == nil {
		return TraceListResult{}, errors.New("nil Collector Trace query Store")
	}
	if query.PageSize == 0 {
		query.PageSize = 50
	}
	if query.PageSize < 1 || query.PageSize > 100 || len(query.IdentityExact) > 128 || len(query.IdentityPrefix) > 128 ||
		len(query.AgentName) > 160 || len(query.ModelName) > 160 || len(query.Status) > 32 || len(query.Cursor) > 512 {
		return TraceListResult{}, errors.New("Collector Trace list query bounds are invalid")
	}
	if query.StartedAfterUnixNano != nil && query.StartedBeforeUnixNano != nil && *query.StartedAfterUnixNano >= *query.StartedBeforeUnixNano {
		return TraceListResult{}, errors.New("Collector Trace time range is invalid")
	}
	clauses := []string{"t.tombstoned_at is null"}
	args := make([]any, 0, 10)
	bind := func(value any) string {
		args = append(args, value)
		return fmt.Sprintf("$%d", len(args))
	}
	if query.StartedAfterUnixNano != nil {
		clauses = append(clauses, "s.started_at_unix_nano >= "+bind(*query.StartedAfterUnixNano))
	}
	if query.StartedBeforeUnixNano != nil {
		clauses = append(clauses, "s.started_at_unix_nano < "+bind(*query.StartedBeforeUnixNano))
	}
	if query.IdentityExact != "" {
		parameter := bind(query.IdentityExact)
		clauses = append(clauses, "(s.trace_id = "+parameter+" or s.workload_id = "+parameter+" or s.run_id = "+parameter+" or s.chat_id = "+parameter+")")
	}
	if query.IdentityPrefix != "" {
		parameter := bind(escapeLikePrefix(query.IdentityPrefix) + "%")
		clauses = append(clauses, "(s.trace_id like "+parameter+" escape '\\' or s.workload_id like "+parameter+" escape '\\' or s.run_id like "+parameter+" escape '\\' or s.chat_id like "+parameter+" escape '\\')")
	}
	if query.AgentName != "" {
		clauses = append(clauses, "s.agent_name = "+bind(query.AgentName))
	}
	if query.ModelName != "" {
		clauses = append(clauses, bind(query.ModelName)+" = any(s.models)")
	}
	if query.Status != "" {
		clauses = append(clauses, "s.status = "+bind(query.Status))
	}
	if query.Active != nil {
		clauses = append(clauses, "s.active = "+bind(*query.Active))
	}
	if query.Cursor != "" {
		cursor, err := decodeTraceCursor(query.Cursor)
		if err != nil {
			return TraceListResult{}, err
		}
		startedParameter := bind(cursor.StartedAtUnixNano)
		traceParameter := bind(cursor.TraceID)
		clauses = append(clauses, "(s.started_at_unix_nano, s.trace_id) < ("+startedParameter+", "+traceParameter+")")
	}
	args = append(args, query.PageSize+1)
	rows, err := s.pool.Query(ctx, `
		select s.trace_id, s.workload_kind, s.workload_id, s.run_id, s.chat_id, s.notebook_id, s.root_span_id, s.agent_name,
			s.started_at_unix_nano, s.last_observed_unix_nano, s.ended_at_unix_nano,
			s.duration_nanoseconds, s.status, s.active, s.models, s.input_tokens,
			s.output_tokens, s.total_tokens, s.cost_known, s.cost_amount,
			s.cost_currency, s.cost_source, s.attempt_count,
			t.committed_sequence, t.projected_sequence
		from obs_trace_summaries s join obs_traces t using (trace_id)
		where `+strings.Join(clauses, " and ")+`
		order by s.started_at_unix_nano desc, s.trace_id desc
		limit $`+fmt.Sprint(len(args)), args...)
	if err != nil {
		return TraceListResult{}, err
	}
	defer rows.Close()
	result := TraceListResult{Items: make([]TraceListItem, 0, query.PageSize)}
	for rows.Next() {
		var item TraceListItem
		var status string
		var costAmount *float64
		summary := &item.Summary
		if err := rows.Scan(&summary.TraceID, &summary.WorkloadKind, &summary.WorkloadID, &summary.RunID, &summary.ChatID, &summary.NotebookID,
			&summary.RootSpanID, &summary.AgentName, &summary.StartedAtUnixNano,
			&summary.LastObservedUnixNano, &summary.EndedAtUnixNano, &summary.DurationNanoseconds,
			&status, &summary.Active, &summary.Models, &summary.InputTokens, &summary.OutputTokens,
			&summary.TotalTokens, &summary.Cost.Known, &costAmount, &summary.Cost.Currency,
			&summary.Cost.Source, &summary.AttemptCount, &item.CommittedThrough,
			&item.ProjectedThrough); err != nil {
			return TraceListResult{}, err
		}
		summary.Status = agentobs.Status(status)
		summary.Cost.Amount = costAmount
		item.ProjectionLagged = item.ProjectedThrough < item.CommittedThrough
		result.Items = append(result.Items, item)
	}
	if err := rows.Err(); err != nil {
		return TraceListResult{}, err
	}
	if len(result.Items) > query.PageSize {
		result.Items = result.Items[:query.PageSize]
		last := result.Items[len(result.Items)-1].Summary
		result.NextCursor = encodeTraceCursor(traceCursor{StartedAtUnixNano: last.StartedAtUnixNano, TraceID: last.TraceID})
	}
	return result, nil
}

func (s *TraceQueryStore) Detail(ctx context.Context, traceID agentobs.TraceID) (ProjectedTrace, error) {
	if s == nil || s.pool == nil || traceID == "" || len(traceID) > 128 {
		return ProjectedTrace{}, errors.New("Collector Trace detail query is invalid")
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return ProjectedTrace{}, err
	}
	defer tx.Rollback(ctx)
	result, err := loadProjectedTrace(ctx, tx, traceID, true)
	if err == nil {
		if err := tx.Commit(ctx); err != nil {
			return ProjectedTrace{}, fmt.Errorf("commit Collector Trace detail access boundary: %w", err)
		}
		return result, nil
	}
	var exists, tombstoned bool
	if scanErr := tx.QueryRow(ctx, `select true, tombstoned_at is not null from obs_traces where trace_id = $1`, traceID).Scan(&exists, &tombstoned); scanErr != nil {
		return ProjectedTrace{}, ErrTraceNotFound
	}
	if exists && !tombstoned {
		return ProjectedTrace{}, ErrProjectionPending
	}
	return ProjectedTrace{}, ErrTraceNotFound
}

func (s *TraceQueryStore) Replay(ctx context.Context, traceID agentobs.TraceID, spanID agentobs.SpanID, attachmentID string) (OpaqueReplay, error) {
	if s == nil || s.pool == nil || s.replayObjects == nil || traceID == "" || spanID == "" || attachmentID == "" ||
		len(traceID) > 128 || len(spanID) > 128 || len(attachmentID) > 64 {
		return OpaqueReplay{}, ErrReplayNotFound
	}
	var result OpaqueReplay
	var state, objectKey string
	var ciphertextBytes int
	var expired bool
	result.TraceID, result.SpanID, result.AttachmentID = traceID, spanID, attachmentID
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return OpaqueReplay{}, err
	}
	defer tx.Rollback(ctx)
	err = tx.QueryRow(ctx, `
		select p.class, p.schema_version, p.plaintext_sha256, p.object_key,
			p.ciphertext_bytes, p.ciphertext_sha256, p.compression, p.encryption,
			p.key_id, p.wrapped_key, p.nonce, p.state, p.expires_at <= now()
		from obs_payload_refs p
		join obs_traces t on t.trace_id = p.trace_id and t.tombstoned_at is null
		join obs_trace_records r on r.trace_id = p.trace_id and r.sequence = p.record_sequence
		where p.trace_id = $1 and r.span_id = $2 and p.attachment_id = $3
		for share of t
	`, traceID, spanID, attachmentID).Scan(&result.Class, &result.Sealed.SchemaVersion,
		&result.Sealed.PlaintextSHA256, &objectKey, &ciphertextBytes,
		&result.Sealed.CiphertextSHA256, &result.Sealed.Compression, &result.Sealed.Encryption,
		&result.Sealed.KeyID, &result.Sealed.WrappedKey, &result.Sealed.Nonce, &state, &expired)
	if err != nil {
		return OpaqueReplay{}, ErrReplayNotFound
	}
	if state != "available" || expired {
		if state == "expired" || expired {
			return OpaqueReplay{}, ErrReplayExpired
		}
		return OpaqueReplay{}, ErrReplayNotFound
	}
	ciphertext, err := s.replayObjects.Get(ctx, objectKey, int64(ciphertextBytes))
	if err != nil || len(ciphertext) != ciphertextBytes {
		return OpaqueReplay{}, ErrReplayUnavailable
	}
	digest := sha256.Sum256(ciphertext)
	if subtle.ConstantTimeCompare([]byte(result.Sealed.CiphertextSHA256), []byte(hex.EncodeToString(digest[:]))) != 1 {
		return OpaqueReplay{}, ErrReplayUnavailable
	}
	result.Sealed.Class = result.Class
	result.Sealed.Ciphertext = ciphertext
	if err := tx.Commit(ctx); err != nil {
		return OpaqueReplay{}, fmt.Errorf("commit Collector Replay access boundary: %w", err)
	}
	return result, nil
}

func escapeLikePrefix(value string) string {
	replacer := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return replacer.Replace(value)
}

func encodeTraceCursor(cursor traceCursor) string {
	payload, _ := json.Marshal(cursor)
	return base64.RawURLEncoding.EncodeToString(payload)
}

func decodeTraceCursor(value string) (traceCursor, error) {
	payload, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil || len(payload) > 256 {
		return traceCursor{}, errors.New("Collector Trace cursor is invalid")
	}
	var cursor traceCursor
	decoder := json.NewDecoder(strings.NewReader(string(payload)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&cursor); err != nil || cursor.StartedAtUnixNano == 0 || cursor.TraceID == "" || len(cursor.TraceID) > 128 {
		return traceCursor{}, errors.New("Collector Trace cursor is invalid")
	}
	return cursor, nil
}
