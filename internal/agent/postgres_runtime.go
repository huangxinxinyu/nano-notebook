package agent

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/huangxinxinyu/nano-notebook/internal/models"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const BareSystemPrompt = `You are Nano Notebook's research assistant. Answer the user's question directly and in the user's language. This capability currently uses general model knowledge and has no Sources or web research. Never invent citations, claim to have read Notebook Sources, or claim to have searched the web. Do not block a useful answer because Sources are absent. When relevant material would materially improve accuracy, depth, recency, verification, or citation quality, briefly suggest what Sources the user could add. Do not repeat that suggestion mechanically. Do not expose hidden chain-of-thought; provide a concise explanation or reasoning summary when useful.`

var ErrLeaseLost = errors.New("agent attempt lease lost")

type PostgresRuntime struct {
	pool         *pgxpool.Pool
	systemPrompt string
	newMessageID func() string
	commit       func(context.Context, pgx.Tx) error
}

type RuntimeOption func(*PostgresRuntime)

func WithCommitFunc(commit func(context.Context, pgx.Tx) error) RuntimeOption {
	return func(runtime *PostgresRuntime) {
		if commit != nil {
			runtime.commit = commit
		}
	}
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
			r.prompt_version, r.time_zone, r.deadline_at,
			r.action_decision_limit, r.final_decision_limit,
			r.action_limit, r.action_batch_limit,
			r.action_result_byte_limit, r.action_results_byte_limit,
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
			&execution.PromptVersion, &execution.TimeZone, &execution.DeadlineAt,
			&execution.ActionDecisionLimit, &execution.FinalDecisionLimit,
			&execution.ActionLimit, &execution.ActionBatchLimit,
			&execution.ActionResultByteLimit, &execution.ActionResultsByteLimit,
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

func (r *PostgresRuntime) Build(ctx context.Context, execution Execution) (models.ChatRequest, error) {
	tx, err := r.workerTx(ctx)
	if err != nil {
		return models.ChatRequest{}, err
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
		return models.ChatRequest{}, err
	}
	defer rows.Close()
	messages := make([]models.ChatMessage, 0, 21)
	messages = append(messages, models.ChatMessage{Role: "system", Content: r.systemPrompt})
	for rows.Next() {
		var message models.ChatMessage
		if err := rows.Scan(&message.Role, &message.Content); err != nil {
			return models.ChatRequest{}, err
		}
		messages = append(messages, message)
	}
	if err := rows.Err(); err != nil {
		return models.ChatRequest{}, err
	}
	if len(messages) == 1 {
		return models.ChatRequest{}, errors.New("Run context has no durable Messages")
	}
	if err := tx.Commit(ctx); err != nil {
		return models.ChatRequest{}, err
	}
	return models.ChatRequest{Model: execution.Model, Messages: messages}, nil
}

func (r *PostgresRuntime) Publish(ctx context.Context, attempt Attempt, result models.ChatResult) error {
	messageID := r.newMessageID()
	if messageID == "" {
		return errors.New("empty Assistant Message ID")
	}
	var publishErr error
	for publishTry := 0; publishTry < 2; publishTry++ {
		publishErr = r.publishOnce(ctx, attempt, messageID, result)
		if publishErr == nil {
			return nil
		}
		if errors.Is(publishErr, ErrLeaseLost) {
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
		case publicationCurrent:
			continue
		}
	}
	return publishErr
}

func (r *PostgresRuntime) publishOnce(ctx context.Context, attempt Attempt, messageID string, result models.ChatResult) error {
	tx, err := r.workerTx(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var chatID, jobID string
	var authorized bool
	err = tx.QueryRow(ctx, `
		select r.chat_id, j.id, exists(
			select 1
			from chat_chats c
			join notebook_memberships m on m.notebook_id = c.notebook_id
			where c.id = r.chat_id and c.creator_user_id = r.user_id and m.user_id = r.user_id
		)
		from agent_runs r
		join agent_jobs j on j.run_id = r.id
		where r.id = $1 and j.id = $2 and j.lease_token = $3::uuid
			and j.lease_expires_at > now()
			and r.status = 'running' and j.status = 'running' and r.output_message_id is null
		for update of r, j`, attempt.RunID, attempt.JobID, attempt.LeaseToken).Scan(&chatID, &jobID, &authorized)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrLeaseLost
	}
	if err != nil {
		return err
	}
	if !authorized {
		return errors.New("Run is no longer authorized to publish")
	}
	if _, err := tx.Exec(ctx, `
		insert into chat_messages(id, chat_id, role, content)
		values($1, $2, 'assistant', $3)`, messageID, chatID, result.Text); err != nil {
		return err
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
		where id = $1 and status = 'running' and lease_token = $2::uuid`, jobID, attempt.LeaseToken)
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
	return r.commit(ctx, tx)
}

type publicationState int

const (
	publicationLeaseLost publicationState = iota
	publicationCurrent
	publicationCompleted
)

func (r *PostgresRuntime) reconcilePublication(ctx context.Context, attempt Attempt) (publicationState, error) {
	tx, err := r.workerTx(ctx)
	if err != nil {
		return publicationLeaseLost, err
	}
	defer tx.Rollback(ctx)
	var runStatus, jobStatus string
	var outputMessageID *string
	var currentLease bool
	err = tx.QueryRow(ctx, `
		select r.status, r.output_message_id, j.status,
			coalesce(j.id = $2 and j.lease_token = $3::uuid and j.lease_expires_at > now(), false)
		from agent_runs r
		join agent_jobs j on j.run_id = r.id
		where r.id = $1`, attempt.RunID, attempt.JobID, attempt.LeaseToken).
		Scan(&runStatus, &outputMessageID, &jobStatus, &currentLease)
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
	if runStatus == "running" && jobStatus == "running" && outputMessageID == nil && currentLease {
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
	return tx.Commit(ctx)
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
