package collector

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/huangxinxinyu/nano-notebook/internal/agentobs"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type ProjectorConfig struct {
	Interval    time.Duration
	Lease       time.Duration
	RetryDelay  time.Duration
	ReportError func(error)
}

type Projector struct {
	pool   *pgxpool.Pool
	config ProjectorConfig
}

type ProjectedTrace struct {
	Projection       TraceProjection `json:"projection"`
	CommittedThrough int             `json:"committed_sequence"`
	ProjectedThrough int             `json:"projected_sequence"`
	CanonicalJSON    string          `json:"-"`
}

func NewProjector(pool *pgxpool.Pool, config ProjectorConfig) (*Projector, error) {
	if pool == nil {
		return nil, errors.New("Collector Projector database is required")
	}
	if config.Interval == 0 {
		config.Interval = 100 * time.Millisecond
	}
	if config.Lease == 0 {
		config.Lease = 30 * time.Second
	}
	if config.RetryDelay == 0 {
		config.RetryDelay = 5 * time.Second
	}
	if config.ReportError == nil {
		config.ReportError = func(error) {}
	}
	if config.Interval <= 0 || config.Lease <= 0 || config.RetryDelay <= 0 {
		return nil, errors.New("Collector Projector bounds are invalid")
	}
	return &Projector{pool: pool, config: config}, nil
}

func (p *Projector) Run(ctx context.Context) error {
	if p == nil || p.pool == nil {
		return errors.New("nil Collector Projector")
	}
	timer := time.NewTimer(0)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-timer.C:
			projected, err := p.RunOnce(ctx)
			if err != nil && ctx.Err() == nil {
				p.config.ReportError(err)
			}
			if projected {
				timer.Reset(0)
			} else {
				timer.Reset(p.config.Interval)
			}
		}
	}
}

func (p *Projector) RunOnce(ctx context.Context) (bool, error) {
	if p == nil || p.pool == nil {
		return false, errors.New("nil Collector Projector")
	}
	traceID, leaseToken, found, err := p.claim(ctx)
	if err != nil || !found {
		return false, err
	}
	if err := p.projectTrace(ctx, traceID, leaseToken); err != nil {
		if releaseErr := p.fail(ctx, traceID, leaseToken, "projection_invalid"); releaseErr != nil {
			return false, errors.Join(err, releaseErr)
		}
		return false, err
	}
	return true, nil
}

func (p *Projector) RebuildTrace(ctx context.Context, traceID agentobs.TraceID) error {
	if p == nil || p.pool == nil || traceID == "" {
		return errors.New("Collector Projector rebuild target is invalid")
	}
	return p.projectTrace(ctx, traceID, "")
}

func (p *Projector) EnqueueRebuildAll(ctx context.Context) (int64, error) {
	if p == nil || p.pool == nil {
		return 0, errors.New("nil Collector Projector")
	}
	tag, err := p.pool.Exec(ctx, `
		insert into obs_projection_queue(trace_id, target_sequence)
		select trace_id, committed_sequence from obs_traces
		where tombstoned_at is null and committed_sequence > 0
		on conflict (trace_id) do update set
			target_sequence = excluded.target_sequence,
			available_at = now(), attempt_count = 0,
			lease_token = null, lease_expires_at = null,
			last_error_code = null, updated_at = now()
	`)
	return tag.RowsAffected(), err
}

func (p *Projector) claim(ctx context.Context) (agentobs.TraceID, string, bool, error) {
	tx, err := p.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return "", "", false, err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `
		update obs_projection_queue set lease_token = null, lease_expires_at = null, updated_at = now()
		where lease_token is not null and lease_expires_at <= now()
	`); err != nil {
		return "", "", false, err
	}
	var traceID agentobs.TraceID
	err = tx.QueryRow(ctx, `
		select trace_id from obs_projection_queue
		where lease_token is null and available_at <= now()
		order by available_at, trace_id
		for update skip locked limit 1
	`).Scan(&traceID)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", "", false, tx.Commit(ctx)
	}
	if err != nil {
		return "", "", false, err
	}
	token := uuid.NewString()
	if _, err := tx.Exec(ctx, `
		update obs_projection_queue set lease_token = $2,
			lease_expires_at = now() + ($3 * interval '1 second'),
			attempt_count = attempt_count + 1, updated_at = now()
		where trace_id = $1
	`, traceID, token, p.config.Lease.Seconds()); err != nil {
		return "", "", false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return "", "", false, err
	}
	return traceID, token, true, nil
}

func (p *Projector) fail(ctx context.Context, traceID agentobs.TraceID, token, code string) error {
	_, err := p.pool.Exec(ctx, `
		update obs_projection_queue set lease_token = null, lease_expires_at = null,
			available_at = now() + ($3 * interval '1 second'), last_error_code = $4, updated_at = now()
		where trace_id = $1 and lease_token = $2
	`, traceID, token, p.config.RetryDelay.Seconds(), code)
	return err
}

func (p *Projector) projectTrace(ctx context.Context, traceID agentobs.TraceID, leaseToken string) error {
	tx, err := p.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	stored, err := loadStoredTrace(ctx, tx, traceID, true)
	if err != nil {
		return fmt.Errorf("load Collector Trace for projection: %w", err)
	}
	if stored.Tombstoned {
		return errors.New("Collector Trace is tombstoned")
	}
	projection, err := BuildTraceProjection(stored)
	if err != nil {
		return err
	}
	if err := replaceProjection(ctx, tx, projection, stored.CommittedThrough); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `
		update obs_traces set projected_sequence = $2, updated_at = now() where trace_id = $1
	`, traceID, stored.CommittedThrough); err != nil {
		return err
	}
	if leaseToken == "" {
		_, err = tx.Exec(ctx, `delete from obs_projection_queue where trace_id = $1 and target_sequence <= $2`, traceID, stored.CommittedThrough)
	} else {
		_, err = tx.Exec(ctx, `delete from obs_projection_queue where trace_id = $1 and lease_token = $2 and target_sequence <= $3`, traceID, leaseToken, stored.CommittedThrough)
	}
	if err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func replaceProjection(ctx context.Context, tx pgx.Tx, projection TraceProjection, projectedSequence int) error {
	traceID := projection.Summary.TraceID
	if _, err := tx.Exec(ctx, `delete from obs_trace_summaries where trace_id = $1`, traceID); err != nil {
		return err
	}
	summary := projection.Summary
	if _, err := tx.Exec(ctx, `
		insert into obs_trace_summaries(
			trace_id, run_id, chat_id, notebook_id, root_span_id, agent_name,
			started_at_unix_nano, last_observed_unix_nano, ended_at_unix_nano,
			duration_nanoseconds, status, active, models, input_tokens, output_tokens,
			total_tokens, cost_known, cost_amount, cost_currency, cost_source,
			attempt_count, projected_sequence
		) values ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21,$22)
	`, summary.TraceID, summary.RunID, summary.ChatID, summary.NotebookID, summary.RootSpanID,
		summary.AgentName, summary.StartedAtUnixNano, summary.LastObservedUnixNano,
		summary.EndedAtUnixNano, summary.DurationNanoseconds, string(summary.Status), summary.Active,
		summary.Models, summary.InputTokens, summary.OutputTokens, summary.TotalTokens,
		summary.Cost.Known, summary.Cost.Amount, summary.Cost.Currency, summary.Cost.Source,
		summary.AttemptCount, projectedSequence); err != nil {
		return err
	}
	for _, span := range projection.Spans {
		startAttributes, err := json.Marshal(span.StartAttributes)
		if err != nil {
			return err
		}
		endAttributes, err := json.Marshal(span.EndAttributes)
		if err != nil {
			return err
		}
		references, err := json.Marshal(span.Replay)
		if err != nil {
			return err
		}
		var model any
		if span.Model != nil {
			encoded, marshalErr := json.Marshal(span.Model)
			if marshalErr != nil {
				return marshalErr
			}
			model = encoded
		}
		if _, err := tx.Exec(ctx, `
			insert into obs_spans(trace_id, span_id, parent_span_id, name, start_sequence,
				end_sequence, started_at_unix_nano, ended_at_unix_nano, duration_nanoseconds,
				status, start_attributes, end_attributes, replay_references, model_analysis)
			values ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14)
		`, span.TraceID, span.SpanID, span.ParentSpanID, span.Name, span.StartSequence,
			span.EndSequence, span.StartedAtUnixNano, span.EndedAtUnixNano, span.DurationNanoseconds,
			string(span.Status), startAttributes, endAttributes, references, model); err != nil {
			return err
		}
	}
	for _, event := range projection.Events {
		attributes, err := json.Marshal(event.Attributes)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `insert into obs_events(trace_id, sequence, span_id, name, occurred_at_unix_nano, attributes) values ($1,$2,$3,$4,$5,$6)`,
			event.TraceID, event.Sequence, event.SpanID, event.Name, event.OccurredAtUnixNano, attributes); err != nil {
			return err
		}
	}
	for _, link := range projection.Links {
		attributes, err := json.Marshal(link.Attributes)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `insert into obs_links(trace_id, sequence, span_id, name, target_trace_id, target_span_id, occurred_at_unix_nano, attributes) values ($1,$2,$3,$4,$5,$6,$7,$8)`,
			link.TraceID, link.Sequence, link.SpanID, link.Name, link.TargetTraceID, link.TargetSpanID, link.OccurredAtUnixNano, attributes); err != nil {
			return err
		}
	}
	return nil
}

func LoadProjectedTrace(ctx context.Context, pool *pgxpool.Pool, traceID agentobs.TraceID) (ProjectedTrace, error) {
	if pool == nil || traceID == "" {
		return ProjectedTrace{}, errors.New("Collector projected Trace query is invalid")
	}
	return loadProjectedTrace(ctx, pool, traceID, false)
}

func loadProjectedTrace(ctx context.Context, query postgresQuerier, traceID agentobs.TraceID, lockTrace bool) (ProjectedTrace, error) {
	var result ProjectedTrace
	var status string
	var costAmount *float64
	summary := &result.Projection.Summary
	lockClause := ""
	if lockTrace {
		lockClause = " for share of t"
	}
	if err := query.QueryRow(ctx, `
		select s.trace_id, s.run_id, s.chat_id, s.notebook_id, s.root_span_id, s.agent_name,
			s.started_at_unix_nano, s.last_observed_unix_nano, s.ended_at_unix_nano,
			s.duration_nanoseconds, s.status, s.active, s.models, s.input_tokens,
			s.output_tokens, s.total_tokens, s.cost_known, s.cost_amount,
			s.cost_currency, s.cost_source, s.attempt_count,
			t.committed_sequence, t.projected_sequence
		from obs_trace_summaries s join obs_traces t using (trace_id)
		where s.trace_id = $1 and t.tombstoned_at is null
	`+lockClause, traceID).Scan(&summary.TraceID, &summary.RunID, &summary.ChatID, &summary.NotebookID,
		&summary.RootSpanID, &summary.AgentName, &summary.StartedAtUnixNano,
		&summary.LastObservedUnixNano, &summary.EndedAtUnixNano, &summary.DurationNanoseconds,
		&status, &summary.Active, &summary.Models, &summary.InputTokens, &summary.OutputTokens,
		&summary.TotalTokens, &summary.Cost.Known, &costAmount, &summary.Cost.Currency,
		&summary.Cost.Source, &summary.AttemptCount, &result.CommittedThrough,
		&result.ProjectedThrough); err != nil {
		return ProjectedTrace{}, err
	}
	summary.Status = agentobs.Status(status)
	summary.Cost.Amount = costAmount
	if err := loadProjectedChildren(ctx, query, &result); err != nil {
		return ProjectedTrace{}, err
	}
	normalizeProjectedTraceCollections(&result.Projection)
	canonical, err := json.Marshal(struct {
		Projection       TraceProjection
		CommittedThrough int
		ProjectedThrough int
	}{result.Projection, result.CommittedThrough, result.ProjectedThrough})
	if err != nil {
		return ProjectedTrace{}, err
	}
	result.CanonicalJSON = string(canonical)
	return result, nil
}

func normalizeProjectedTraceCollections(projection *TraceProjection) {
	if projection.Summary.Models == nil {
		projection.Summary.Models = []string{}
	}
	if projection.Spans == nil {
		projection.Spans = []SpanProjection{}
	}
	for index := range projection.Spans {
		span := &projection.Spans[index]
		if span.StartAttributes == nil {
			span.StartAttributes = []agentobs.Attribute{}
		}
		if span.EndAttributes == nil {
			span.EndAttributes = []agentobs.Attribute{}
		}
		if span.Replay == nil {
			span.Replay = []ReplayReferenceProjection{}
		}
	}
	if projection.Events == nil {
		projection.Events = []EventProjection{}
	}
	for index := range projection.Events {
		if projection.Events[index].Attributes == nil {
			projection.Events[index].Attributes = []agentobs.Attribute{}
		}
	}
	if projection.Links == nil {
		projection.Links = []LinkProjection{}
	}
	for index := range projection.Links {
		if projection.Links[index].Attributes == nil {
			projection.Links[index].Attributes = []agentobs.Attribute{}
		}
	}
}

func loadProjectedChildren(ctx context.Context, query postgresQuerier, result *ProjectedTrace) error {
	traceID := result.Projection.Summary.TraceID
	rows, err := query.Query(ctx, `select span_id, parent_span_id, name, start_sequence, end_sequence, started_at_unix_nano, ended_at_unix_nano, duration_nanoseconds, status, start_attributes, end_attributes, replay_references, model_analysis from obs_spans where trace_id = $1 order by start_sequence`, traceID)
	if err != nil {
		return err
	}
	for rows.Next() {
		span := SpanProjection{TraceID: traceID}
		var status string
		var start, end, references []byte
		var model []byte
		if err := rows.Scan(&span.SpanID, &span.ParentSpanID, &span.Name, &span.StartSequence,
			&span.EndSequence, &span.StartedAtUnixNano, &span.EndedAtUnixNano,
			&span.DurationNanoseconds, &status, &start, &end, &references, &model); err != nil {
			rows.Close()
			return err
		}
		span.Status = agentobs.Status(status)
		if err := json.Unmarshal(start, &span.StartAttributes); err != nil {
			return err
		}
		if err := json.Unmarshal(end, &span.EndAttributes); err != nil {
			return err
		}
		if err := json.Unmarshal(references, &span.Replay); err != nil {
			return err
		}
		if len(model) > 0 && string(model) != "null" {
			span.Model = &ModelAnalysisProjection{}
			if err := json.Unmarshal(model, span.Model); err != nil {
				return err
			}
		}
		result.Projection.Spans = append(result.Projection.Spans, span)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	if err := loadProjectedEvents(ctx, query, result); err != nil {
		return err
	}
	return loadProjectedLinks(ctx, query, result)
}

func loadProjectedEvents(ctx context.Context, query postgresQuerier, result *ProjectedTrace) error {
	rows, err := query.Query(ctx, `select sequence, span_id, name, occurred_at_unix_nano, attributes from obs_events where trace_id = $1 order by sequence`, result.Projection.Summary.TraceID)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		event := EventProjection{TraceID: result.Projection.Summary.TraceID}
		var attributes []byte
		if err := rows.Scan(&event.Sequence, &event.SpanID, &event.Name, &event.OccurredAtUnixNano, &attributes); err != nil {
			return err
		}
		if err := json.Unmarshal(attributes, &event.Attributes); err != nil {
			return err
		}
		result.Projection.Events = append(result.Projection.Events, event)
	}
	return rows.Err()
}

func loadProjectedLinks(ctx context.Context, query postgresQuerier, result *ProjectedTrace) error {
	rows, err := query.Query(ctx, `select sequence, span_id, name, target_trace_id, target_span_id, occurred_at_unix_nano, attributes from obs_links where trace_id = $1 order by sequence`, result.Projection.Summary.TraceID)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		link := LinkProjection{TraceID: result.Projection.Summary.TraceID}
		var attributes []byte
		if err := rows.Scan(&link.Sequence, &link.SpanID, &link.Name, &link.TargetTraceID,
			&link.TargetSpanID, &link.OccurredAtUnixNano, &attributes); err != nil {
			return err
		}
		if err := json.Unmarshal(attributes, &link.Attributes); err != nil {
			return err
		}
		result.Projection.Links = append(result.Projection.Links, link)
	}
	return rows.Err()
}
