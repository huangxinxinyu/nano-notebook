package agent

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/huangxinxinyu/nano-notebook/internal/agentobs"
	"github.com/huangxinxinyu/nano-notebook/internal/models"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const BareSystemPrompt = `You are Nano Notebook's research assistant. Answer the user's question directly and in the user's language. This capability currently uses general model knowledge and has no Sources or web research. Never invent citations, claim to have read Notebook Sources, or claim to have searched the web. Do not block a useful answer because Sources are absent. When relevant material would materially improve accuracy, depth, recency, verification, or citation quality, briefly suggest what Sources the user could add. Do not repeat that suggestion mechanically. Do not expose hidden chain-of-thought; provide a concise explanation or reasoning summary when useful.`

const GroundedSystemPrompt = `You are Nano Notebook's source-aware research assistant. The Run has a fixed server-controlled set of selected Sources. You must always use search_evidence before answering, then answer the current request rather than continuing an older topic. Decide whether the retrieved content helps with that request. Return the final answer as ordinary plain text. When you use information from a retrieved Source, place [source:<source_id>] immediately after the material it supports, using only source_id values present in search_evidence results. When retrieval fails or its content is empty, irrelevant, or unnecessary, and the current request can be answered without Sources, answer normally: a failed or unhelpful search is not a reason to refuse, apologize, or claim that ordinary capabilities are unavailable. Mention a retrieval limitation only when the current request actually asks for information from selected Sources and the failure prevents a supported answer. Otherwise omit Source markers. Never invent a Source, quotation, search result, or marker, and do not imply that selected Sources support unmarked material. Do not expose hidden chain-of-thought; provide only the useful answer and concise disclosed limitations.`

var ErrLeaseLost = errors.New("agent attempt lease lost")

type PostgresRuntime struct {
	pool         *pgxpool.Pool
	systemPrompt string
	newMessageID func() string
	commit       func(context.Context, pgx.Tx) error
	telemetry    agentobs.Exporter
	traceSink    TraceSink
	replayStager ReplayStager
	grounder     *GroundingService
}

type RuntimeOption func(*PostgresRuntime)

func WithCommitFunc(commit func(context.Context, pgx.Tx) error) RuntimeOption {
	return func(runtime *PostgresRuntime) {
		if commit != nil {
			runtime.commit = commit
		}
	}
}

func WithBestEffortTraceExporter(exporter agentobs.Exporter) RuntimeOption {
	return func(runtime *PostgresRuntime) {
		runtime.telemetry = exporter
	}
}

func WithTraceSink(sink TraceSink) RuntimeOption {
	return func(runtime *PostgresRuntime) {
		runtime.traceSink = sink
	}
}

func (r *PostgresRuntime) beginTraceScope(ctx context.Context) (context.Context, *TraceScope, error) {
	sink := TraceSink(DiscardTraceSink{})
	if r != nil && r.traceSink != nil {
		sink = r.traceSink
	}
	scope, err := NewTraceScope(sink)
	if err != nil {
		return ctx, nil, err
	}
	return ContextWithTraceScope(ctx, scope), scope, nil
}

func publishCommittedTrace(ctx context.Context, scope *TraceScope) {
	if scope != nil {
		_ = scope.PublishAfterCommit(ctx)
	}
}

func WithReplayStager(stager ReplayStager) RuntimeOption {
	return func(runtime *PostgresRuntime) {
		runtime.replayStager = stager
	}
}

func WithGroundingService(grounder *GroundingService) RuntimeOption {
	return func(runtime *PostgresRuntime) {
		runtime.grounder = grounder
	}
}

func (r *PostgresRuntime) ReplayStager() ReplayStager {
	if r == nil {
		return nil
	}
	return r.replayStager
}

func (r *PostgresRuntime) PrepareFinal(ctx context.Context, attempt Attempt, execution Execution, prefix CheckpointPrefix, draft models.FinalDraft) (models.FinalDraft, error) {
	if r != nil && r.grounder != nil {
		return r.grounder.Prepare(ctx, attempt, prefix, draft)
	}
	if execution.SelectedSourceCount > 0 {
		return models.FinalDraft{}, ErrGroundingIncomplete
	}
	return draft, nil
}

func (r *PostgresRuntime) PrepareFinalTraced(ctx context.Context, tracer *agentobs.Tracer, attempt Attempt, execution Execution, prefix CheckpointPrefix, draft models.FinalDraft) (models.FinalDraft, error) {
	if r != nil && r.grounder != nil {
		return r.grounder.PrepareTraced(ctx, tracer, r.replayStager, attempt, prefix, draft)
	}
	if execution.SelectedSourceCount > 0 {
		return models.FinalDraft{}, ErrGroundingIncomplete
	}
	return draft, nil
}

func NewPostgresRuntime(pool *pgxpool.Pool, systemPrompt string, newMessageID func() string, options ...RuntimeOption) *PostgresRuntime {
	if systemPrompt == "" {
		systemPrompt = BareSystemPrompt
	}
	if newMessageID == nil {
		newMessageID = func() string { return "msg_" + uuid.NewString() }
	}
	runtime := &PostgresRuntime{
		pool: pool, systemPrompt: systemPrompt, newMessageID: newMessageID,
		commit: func(ctx context.Context, tx pgx.Tx) error { return tx.Commit(ctx) },
	}
	for _, option := range options {
		option(runtime)
	}
	return runtime
}

func (r *PostgresRuntime) Load(ctx context.Context, attempt Attempt) (Execution, error) {
	tx, err := r.workerTx(ctx)
	if err != nil {
		return Execution{}, err
	}
	defer tx.Rollback(ctx)
	var execution Execution
	var deadlineValid bool
	err = tx.QueryRow(ctx, `
		select r.id, r.chat_id, r.user_id, r.input_message_id, r.model,
			r.prompt_version, r.agent_config_id, r.time_zone, r.deadline_at,
			r.action_decision_limit, r.final_decision_limit,
			r.action_limit, r.action_batch_limit,
			r.action_result_byte_limit, r.action_results_byte_limit,
			r.selected_source_count,
			r.deadline_at > now()
		from agent_runs r
		join agent_jobs j on j.run_id = r.id
		join chat_chats c on c.id = r.chat_id and c.creator_user_id = r.user_id
		join notebook_memberships m on m.notebook_id = c.notebook_id and m.user_id = r.user_id
		where r.id = $1 and j.id = $2 and j.lease_token = $3::uuid
			and r.status = 'running' and j.status = 'running'
			and j.lease_expires_at > now() and r.output_message_id is null`, attempt.RunID, attempt.JobID, attempt.LeaseToken).
		Scan(
			&execution.RunID, &execution.ChatID, &execution.UserID, &execution.InputMessageID, &execution.Model,
			&execution.PromptVersion, &execution.AgentConfigID, &execution.TimeZone, &execution.DeadlineAt,
			&execution.ActionDecisionLimit, &execution.FinalDecisionLimit,
			&execution.ActionLimit, &execution.ActionBatchLimit,
			&execution.ActionResultByteLimit, &execution.ActionResultsByteLimit,
			&execution.SelectedSourceCount,
			&deadlineValid,
		)
	if errors.Is(err, pgx.ErrNoRows) {
		return Execution{}, ErrLeaseLost
	}
	if err != nil {
		return Execution{}, err
	}
	if !deadlineValid {
		return Execution{}, ErrRunDeadlineExceeded
	}
	execution.Attempt = attempt
	if err := tx.Commit(ctx); err != nil {
		return Execution{}, err
	}
	return execution, nil
}

func (r *PostgresRuntime) Build(ctx context.Context, execution Execution) (models.ModelRequest, error) {
	tx, err := r.workerTx(ctx)
	if err != nil {
		return models.ModelRequest{}, err
	}
	defer tx.Rollback(ctx)
	rows, err := tx.Query(ctx, `
		with cutoff as (
			select id, created_at
			from chat_messages
			where id = $2 and chat_id = $1
		),
		recent as (
			select m.id, m.role, m.content, m.created_at
			from chat_messages m, cutoff c
			where m.chat_id = $1 and (m.created_at, m.id) <= (c.created_at, c.id)
			order by m.created_at desc, m.id desc
			limit 20
		)
		select role, content
		from recent
		order by created_at, id`, execution.ChatID, execution.InputMessageID)
	if err != nil {
		return models.ModelRequest{}, err
	}
	defer rows.Close()
	messages := make([]models.ModelMessage, 0, 21)
	systemPrompt := r.systemPrompt
	if execution.SelectedSourceCount > 0 && systemPrompt == BareSystemPrompt {
		systemPrompt = GroundedSystemPrompt
	}
	messages = append(messages, models.ModelMessage{Role: models.RoleSystem, Content: systemPrompt})
	for rows.Next() {
		var message models.ModelMessage
		if err := rows.Scan(&message.Role, &message.Content); err != nil {
			return models.ModelRequest{}, err
		}
		messages = append(messages, message)
	}
	if err := rows.Err(); err != nil {
		return models.ModelRequest{}, err
	}
	if len(messages) == 1 {
		return models.ModelRequest{}, errors.New("Run context has no durable Messages")
	}
	if err := tx.Commit(ctx); err != nil {
		return models.ModelRequest{}, err
	}
	return models.ModelRequest{Model: execution.Model, Messages: messages}, nil
}

func (r *PostgresRuntime) PublishFinal(ctx context.Context, attempt Attempt, draft models.FinalDraft) error {
	if _, err := NewFinalDraftCheckpoint(1, draft); err != nil {
		return err
	}
	return r.publishResult(ctx, attempt, draft.Text, &draft)
}

func (r *PostgresRuntime) publishResult(ctx context.Context, attempt Attempt, text string, expectedFinal *models.FinalDraft) error {
	messageID := r.newMessageID()
	if messageID == "" {
		return errors.New("empty Assistant Message ID")
	}
	var publishErr error
	for publishTry := 0; publishTry < 2; publishTry++ {
		publishErr = r.publishOnce(ctx, attempt, messageID, text, expectedFinal)
		if publishErr == nil {
			return nil
		}
		if errors.Is(publishErr, ErrLeaseLost) || errors.Is(publishErr, ErrRunDeadlineExceeded) || errors.Is(publishErr, ErrCheckpointInvalid) {
			return publishErr
		}
		state, reconcileErr := r.reconcilePublication(ctx, attempt)
		if reconcileErr != nil {
			return errors.Join(publishErr, reconcileErr)
		}
		switch state {
		case publicationCompleted:
			return nil
		case publicationLeaseLost:
			return ErrLeaseLost
		case publicationDeadline:
			return ErrRunDeadlineExceeded
		case publicationCurrent:
			continue
		}
	}
	return publishErr
}

func (r *PostgresRuntime) publishOnce(ctx context.Context, attempt Attempt, messageID, text string, expectedFinal *models.FinalDraft) error {
	tx, err := r.workerTx(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	traceCtx, traceScope, err := r.beginTraceScope(ctx)
	if err != nil {
		return err
	}
	if traceScope != nil {
		defer traceScope.Rollback()
	}
	if err := lockCheckpointAuthority(ctx, tx, attempt); err != nil {
		return err
	}
	var chatID string
	if err := tx.QueryRow(ctx, `select chat_id from agent_runs where id = $1`, attempt.RunID).Scan(&chatID); err != nil {
		return err
	}
	if expectedFinal != nil {
		checkpoints, err := loadRunCheckpoints(ctx, tx, attempt.RunID)
		if err != nil {
			return err
		}
		prefix, err := LoadCheckpointPrefix(ctx, checkpoints)
		if err != nil {
			return err
		}
		prefixHash, prefixErr := finalDraftSHA256(valueOrEmptyFinal(prefix.Final))
		expectedHash, expectedErr := finalDraftSHA256(*expectedFinal)
		if prefix.Final == nil || prefixErr != nil || expectedErr != nil || prefixHash != expectedHash {
			return invalidCheckpoint("publication Final Draft does not match accepted prefix")
		}
	}
	groundingOutcome, err := validateGroundingPublication(ctx, tx, attempt.RunID, expectedFinal)
	if err != nil {
		return err
	}
	recorder, err := NewRunTraceRecorder(traceCtx, tx, attempt.RunID)
	if err != nil {
		return err
	}
	tracer, err := agentobs.NewTracer(agentobs.TracerConfig{
		Recorder: recorder, SemanticConventionVersion: TraceSemanticConventionVersion,
	})
	if err != nil {
		return err
	}
	attemptSpan, err := recorder.SpanContextByIdentity(traceCtx, TraceAttemptStartIdentity(attempt.RunID, attempt.AttemptNo))
	if err != nil {
		return err
	}
	publicationContext, _, err := tracer.StartSpan(agentobs.ContextWithSpanContext(traceCtx, attemptSpan), agentobs.SpanStart{
		IdentityKey: fmt.Sprintf("run/%s/attempt/%d/publication/start", attempt.RunID, attempt.AttemptNo),
		Name:        TraceSpanPublication,
		Attributes:  []agentobs.Attribute{agentobs.String(TraceKeyGroundingOutcome, groundingOutcome)},
	})
	if err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `
		insert into chat_messages(id, chat_id, role, content)
		values($1, $2, 'assistant', $3)`, messageID, chatID, text); err != nil {
		return err
	}
	if groundingOutcome == "source_cited" {
		if _, err := tx.Exec(ctx, `
			insert into chat_citations(
				message_id,citation_id,run_id,reference_kind,reference_ordinal,notebook_id,source_id
			)
			select $1,c.citation_id,c.run_id,'source',c.reference_ordinal,c.notebook_id,c.source_id
			from agent_draft_source_references c
			where c.run_id=$2
			order by c.reference_ordinal
		`, messageID, attempt.RunID); err != nil {
			return err
		}
	}
	runTag, err := tx.Exec(ctx, `
		update agent_runs
		set output_message_id = $2,
			status = 'completed',
			error_code = null,
			finished_at = now(),
			updated_at = now()
		where id = $1 and status = 'running' and output_message_id is null`, attempt.RunID, messageID)
	if err != nil {
		return err
	}
	jobTag, err := tx.Exec(ctx, `
		update agent_jobs
		set status = 'succeeded', lease_token = null, lease_expires_at = null,
			finished_at = now(), updated_at = now()
		where id = $1 and status = 'running' and lease_token = $2::uuid`, attempt.JobID, attempt.LeaseToken)
	if err != nil {
		return err
	}
	if runTag.RowsAffected() != 1 || jobTag.RowsAffected() != 1 {
		return errors.New("Run publication did not transition Run and Job together")
	}
	if _, err := tx.Exec(ctx, `update chat_chats set updated_at = now() where id = $1`, chatID); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `select pg_notify('nano_agent_runs', $1)`, attempt.RunID); err != nil {
		return err
	}
	if err := tracer.Event(publicationContext, agentobs.Event{
		IdentityKey: fmt.Sprintf("run/%s/attempt/%d/publication/passed", attempt.RunID, attempt.AttemptNo),
		Name:        TraceEventPublicationPassed,
		Attributes:  []agentobs.Attribute{agentobs.String(TraceKeyGroundingOutcome, groundingOutcome)},
	}); err != nil {
		return err
	}
	if err := tracer.EndSpan(publicationContext, agentobs.SpanEnd{Name: TraceSpanPublication, Status: agentobs.StatusOK, Attributes: []agentobs.Attribute{
		agentobs.String(TraceKeyGroundingOutcome, groundingOutcome),
	}}); err != nil {
		return err
	}
	if err := RecordRunTerminalInTx(traceCtx, tx, attempt.RunID, RunTerminalTrace{
		RunStatus: "completed", SpanStatus: agentobs.StatusOK, AttemptNo: attempt.AttemptNo,
	}); err != nil {
		return err
	}
	if err := r.commit(ctx, tx); err != nil {
		return err
	}
	publishCommittedTrace(traceCtx, traceScope)
	return nil
}

func valueOrEmptyFinal(draft *models.FinalDraft) models.FinalDraft {
	if draft == nil {
		return models.FinalDraft{}
	}
	return *draft
}

type publicationState int

const (
	publicationLeaseLost publicationState = iota
	publicationCurrent
	publicationCompleted
	publicationDeadline
)

func (r *PostgresRuntime) reconcilePublication(ctx context.Context, attempt Attempt) (publicationState, error) {
	tx, err := r.workerTx(ctx)
	if err != nil {
		return publicationLeaseLost, err
	}
	defer tx.Rollback(ctx)
	var runStatus, jobStatus string
	var outputMessageID *string
	var currentLease, deadlineValid bool
	err = tx.QueryRow(ctx, `
		select r.status, r.output_message_id, j.status,
			coalesce(j.id = $2 and j.lease_token = $3::uuid and j.lease_expires_at > now(), false),
			r.deadline_at > now()
		from agent_runs r
		join agent_jobs j on j.run_id = r.id
		where r.id = $1`, attempt.RunID, attempt.JobID, attempt.LeaseToken).
		Scan(&runStatus, &outputMessageID, &jobStatus, &currentLease, &deadlineValid)
	if errors.Is(err, pgx.ErrNoRows) {
		return publicationLeaseLost, nil
	}
	if err != nil {
		return publicationLeaseLost, err
	}
	if err := tx.Commit(ctx); err != nil {
		return publicationLeaseLost, err
	}
	if runStatus == "completed" && outputMessageID != nil && jobStatus == "succeeded" {
		return publicationCompleted, nil
	}
	if runStatus == "running" && jobStatus == "running" && outputMessageID == nil && !deadlineValid {
		return publicationDeadline, nil
	}
	if runStatus == "running" && jobStatus == "running" && outputMessageID == nil && currentLease && deadlineValid {
		return publicationCurrent, nil
	}
	return publicationLeaseLost, nil
}

func (r *PostgresRuntime) Fail(ctx context.Context, attempt Attempt, errorCode string) error {
	if errorCode == "" {
		errorCode = string(models.ErrorUnavailable)
	}
	tx, err := r.workerTx(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	traceCtx, traceScope, err := r.beginTraceScope(ctx)
	if err != nil {
		return err
	}
	if traceScope != nil {
		defer traceScope.Rollback()
	}
	var jobID string
	err = tx.QueryRow(ctx, `
		select j.id
		from agent_runs r
		join agent_jobs j on j.run_id = r.id
		where r.id = $1 and j.id = $2 and j.lease_token = $3::uuid
			and j.lease_expires_at > now()
			and r.status = 'running' and j.status = 'running'
		for update of r, j`, attempt.RunID, attempt.JobID, attempt.LeaseToken).Scan(&jobID)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrLeaseLost
	}
	if err != nil {
		return err
	}
	runTag, err := tx.Exec(ctx, `
		update agent_runs
		set status = 'failed', error_code = $2, finished_at = now(), updated_at = now()
		where id = $1 and status = 'running' and output_message_id is null`, attempt.RunID, errorCode)
	if err != nil {
		return err
	}
	jobTag, err := tx.Exec(ctx, `
		update agent_jobs
		set status = 'failed', lease_token = null, lease_expires_at = null,
			finished_at = now(), updated_at = now()
		where id = $1 and status = 'running' and lease_token = $2::uuid`, jobID, attempt.LeaseToken)
	if err != nil {
		return err
	}
	if runTag.RowsAffected() != 1 || jobTag.RowsAffected() != 1 {
		return errors.New("Run failure did not transition Run and Job together")
	}
	if _, err := tx.Exec(ctx, `select pg_notify('nano_agent_runs', $1)`, attempt.RunID); err != nil {
		return err
	}
	if err := RecordRunTerminalInTx(traceCtx, tx, attempt.RunID, RunTerminalTrace{
		RunStatus: "failed", SpanStatus: agentobs.StatusError, ErrorCode: errorCode, AttemptNo: attempt.AttemptNo,
	}); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	publishCommittedTrace(traceCtx, traceScope)
	return nil
}

func (r *PostgresRuntime) workerTx(ctx context.Context) (pgx.Tx, error) {
	if r.pool == nil {
		return nil, errors.New("nil PostgreSQL pool")
	}
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx, `set local role nano_worker`); err != nil {
		tx.Rollback(ctx)
		return nil, fmt.Errorf("set worker role: %w", err)
	}
	return tx, nil
}
