package agent

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

var (
	ErrActiveRun           = errors.New("active run conflict")
	ErrRunNotFound         = errors.New("agent run not found")
	ErrRunNotCancellable   = errors.New("agent run not cancellable")
	ErrRunNotRetryable     = errors.New("agent run not retryable")
	ErrRetryNotLatest      = errors.New("agent run input is not latest")
	ErrIdempotencyMismatch = errors.New("idempotency mismatch")
)

type DBTX interface {
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
	Query(context.Context, string, ...any) (pgx.Rows, error)
	QueryRow(context.Context, string, ...any) pgx.Row
}

type RunRef struct {
	ID     string
	Status string
}

type RunSnapshot struct {
	ID             string  `json:"id"`
	InputMessageID string  `json:"input_message_id"`
	Status         string  `json:"status"`
	ErrorCode      *string `json:"error_code"`
}

type AssistantMessageSnapshot struct {
	ID        string    `json:"id"`
	Role      string    `json:"role"`
	Content   string    `json:"content"`
	CreatedAt time.Time `json:"created_at"`
}

type RunProjection struct {
	Run     RunSnapshot               `json:"run"`
	Message *AssistantMessageSnapshot `json:"message"`
}

func (s *Store) ByInputMessage(ctx context.Context, messageID string) (RunRef, error) {
	var run RunRef
	err := s.db.QueryRow(ctx, `
		select id, status
		from agent_runs
		where input_message_id = $1
		order by created_at, id
		limit 1`, messageID).Scan(&run.ID, &run.Status)
	return run, err
}

func (s *Store) ActiveByUser(ctx context.Context, userID string) (RunRef, bool, error) {
	var run RunRef
	err := s.db.QueryRow(ctx, `
		select id, status
		from agent_runs
		where user_id = $1 and status in ('queued', 'running')`, userID).Scan(&run.ID, &run.Status)
	if errors.Is(err, pgx.ErrNoRows) {
		return RunRef{}, false, nil
	}
	if err != nil {
		return RunRef{}, false, err
	}
	return run, true, nil
}

// ExpireIfOverdue atomically terminalizes every matching active Run whose
// admission-pinned deadline has passed. Empty filters are used by the Worker;
// request-principal callers pass both user and/or Run identity.
func (s *Store) ExpireIfOverdue(ctx context.Context, userID, runID string) (int, error) {
	rows, err := s.db.Query(ctx, `
		select r.id, j.id
		from agent_runs r
		join agent_jobs j on j.run_id = r.id
		where r.status in ('queued', 'running')
			and j.status in ('queued', 'running')
			and r.deadline_at <= now()
			and ($1 = '' or r.user_id = $1)
			and ($2 = '' or r.id = $2)
		order by r.id
		for update of r, j`, userID, runID)
	if err != nil {
		return 0, err
	}
	type overdueRun struct {
		runID string
		jobID string
	}
	overdue := make([]overdueRun, 0)
	for rows.Next() {
		var item overdueRun
		if err := rows.Scan(&item.runID, &item.jobID); err != nil {
			rows.Close()
			return 0, err
		}
		overdue = append(overdue, item)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return 0, err
	}
	rows.Close()
	for _, item := range overdue {
		runTag, err := s.db.Exec(ctx, `
			update agent_runs
			set status = 'failed', error_code = 'run_deadline_exceeded',
				finished_at = now(), updated_at = now()
			where id = $1 and status in ('queued', 'running') and deadline_at <= now()`, item.runID)
		if err != nil {
			return 0, err
		}
		jobTag, err := s.db.Exec(ctx, `
			update agent_jobs
			set status = 'failed', lease_token = null, lease_expires_at = null,
				finished_at = now(), updated_at = now()
			where id = $1 and status in ('queued', 'running')`, item.jobID)
		if err != nil {
			return 0, err
		}
		if runTag.RowsAffected() != 1 || jobTag.RowsAffected() != 1 {
			return 0, errors.New("deadline expiry did not transition Run and Job together")
		}
		if _, err := s.db.Exec(ctx, `select pg_notify('nano_agent_runs', $1)`, item.runID); err != nil {
			return 0, err
		}
	}
	return len(overdue), nil
}

func (s *Store) ActiveForChat(ctx context.Context, userID, chatID string) (RunSnapshot, bool, error) {
	var run RunSnapshot
	err := s.db.QueryRow(ctx, `
		select id, input_message_id, status, error_code
		from agent_runs
		where user_id = $1 and chat_id = $2 and status in ('queued', 'running')`, userID, chatID).
		Scan(&run.ID, &run.InputMessageID, &run.Status, &run.ErrorCode)
	if errors.Is(err, pgx.ErrNoRows) {
		return RunSnapshot{}, false, nil
	}
	if err != nil {
		return RunSnapshot{}, false, err
	}
	return run, true, nil
}

func (s *Store) ProjectionForUser(ctx context.Context, userID, runID string) (RunProjection, error) {
	var projection RunProjection
	var outputMessageID *string
	err := s.db.QueryRow(ctx, `
		select id, input_message_id, status, error_code, output_message_id
		from agent_runs
		where id = $1 and user_id = $2`, runID, userID).
		Scan(&projection.Run.ID, &projection.Run.InputMessageID, &projection.Run.Status, &projection.Run.ErrorCode, &outputMessageID)
	if errors.Is(err, pgx.ErrNoRows) {
		return RunProjection{}, ErrRunNotFound
	}
	if err != nil {
		return RunProjection{}, err
	}
	if outputMessageID == nil {
		return projection, nil
	}
	var message AssistantMessageSnapshot
	err = s.db.QueryRow(ctx, `
		select id, role, content, created_at
		from chat_messages
		where id = $1`, *outputMessageID).
		Scan(&message.ID, &message.Role, &message.Content, &message.CreatedAt)
	if err != nil {
		return RunProjection{}, err
	}
	projection.Message = &message
	return projection, nil
}

func (s *Store) LatestForChat(ctx context.Context, userID, chatID string) ([]RunSnapshot, error) {
	rows, err := s.db.Query(ctx, `
		select distinct on (input_message_id) id, input_message_id, status, error_code
		from agent_runs
		where user_id = $1 and chat_id = $2
		order by input_message_id, created_at desc, id desc`, userID, chatID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	runs := make([]RunSnapshot, 0)
	for rows.Next() {
		var run RunSnapshot
		if err := rows.Scan(&run.ID, &run.InputMessageID, &run.Status, &run.ErrorCode); err != nil {
			return nil, err
		}
		runs = append(runs, run)
	}
	return runs, rows.Err()
}

func (s *Store) Cancel(ctx context.Context, userID, runID string) (RunSnapshot, error) {
	var run RunSnapshot
	var jobID string
	err := s.db.QueryRow(ctx, `
		select r.id, r.input_message_id, r.status, r.error_code, j.id
		from agent_runs r
		join agent_jobs j on j.run_id = r.id
		where r.id = $1 and r.user_id = $2
		for update of r, j`, runID, userID).
		Scan(&run.ID, &run.InputMessageID, &run.Status, &run.ErrorCode, &jobID)
	if errors.Is(err, pgx.ErrNoRows) {
		return RunSnapshot{}, ErrRunNotFound
	}
	if err != nil {
		return RunSnapshot{}, err
	}
	if run.Status == "cancelled" {
		return run, nil
	}
	if run.Status == "completed" || run.Status == "failed" {
		return RunSnapshot{}, ErrRunNotCancellable
	}
	runTag, err := s.db.Exec(ctx, `
		update agent_runs
		set status = 'cancelled', error_code = null, finished_at = now(), updated_at = now()
		where id = $1 and status in ('queued', 'running')`, runID)
	if err != nil {
		return RunSnapshot{}, err
	}
	jobTag, err := s.db.Exec(ctx, `
		update agent_jobs
		set status = 'cancelled', lease_token = null, lease_expires_at = null,
			finished_at = now(), updated_at = now()
		where id = $1 and status in ('queued', 'running')`, jobID)
	if err != nil {
		return RunSnapshot{}, err
	}
	if runTag.RowsAffected() != 1 || jobTag.RowsAffected() != 1 {
		return RunSnapshot{}, ErrRunNotCancellable
	}
	if _, err := s.db.Exec(ctx, `select pg_notify('nano_agent_runs', $1)`, runID); err != nil {
		return RunSnapshot{}, err
	}
	run.Status = "cancelled"
	run.ErrorCode = nil
	return run, nil
}

func (s *Store) RetryQueued(ctx context.Context, userID, sourceRunID, key, requestHash, runID, jobID, timeZone string, config RunConfig) (RunSnapshot, bool, error) {
	if _, err := s.db.Exec(ctx, `select pg_advisory_xact_lock(hashtextextended($1, 0))`, "admit_agent_run:"+userID); err != nil {
		return RunSnapshot{}, false, err
	}
	var existingHash, existingJSON string
	err := s.db.QueryRow(ctx, `
		select request_hash, response_json::text
		from platform_idempotency_keys
		where principal_id = $1 and action = 'retry_agent_run' and key = $2`, userID, key).
		Scan(&existingHash, &existingJSON)
	if err == nil {
		if existingHash != requestHash {
			return RunSnapshot{}, false, ErrIdempotencyMismatch
		}
		var body struct {
			Run RunSnapshot `json:"run"`
		}
		if err := json.Unmarshal([]byte(existingJSON), &body); err != nil {
			return RunSnapshot{}, false, err
		}
		return body.Run, true, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return RunSnapshot{}, false, err
	}
	if _, err := s.ExpireIfOverdue(ctx, userID, ""); err != nil {
		return RunSnapshot{}, false, err
	}

	var inputMessageID, chatID, model, promptVersion, status string
	err = s.db.QueryRow(ctx, `
		select input_message_id, chat_id, model, prompt_version, status
		from agent_runs
		where id = $1 and user_id = $2
		for update`, sourceRunID, userID).
		Scan(&inputMessageID, &chatID, &model, &promptVersion, &status)
	if errors.Is(err, pgx.ErrNoRows) {
		return RunSnapshot{}, false, ErrRunNotFound
	}
	if err != nil {
		return RunSnapshot{}, false, err
	}
	if status != "failed" && status != "cancelled" {
		return RunSnapshot{}, false, ErrRunNotRetryable
	}
	var latestRunID, latestMessageID string
	var completed bool
	err = s.db.QueryRow(ctx, `
		select
			(select id from agent_runs where input_message_id = $1 order by created_at desc, id desc limit 1),
			(select id from chat_messages where chat_id = $2 order by created_at desc, id desc limit 1),
			exists(select 1 from agent_runs where input_message_id = $1 and status = 'completed')`,
		inputMessageID, chatID).Scan(&latestRunID, &latestMessageID, &completed)
	if err != nil {
		return RunSnapshot{}, false, err
	}
	if latestRunID != sourceRunID || latestMessageID != inputMessageID {
		return RunSnapshot{}, false, ErrRetryNotLatest
	}
	if completed {
		return RunSnapshot{}, false, ErrRunNotRetryable
	}
	if _, active, err := s.ActiveByUser(ctx, userID); err != nil {
		return RunSnapshot{}, false, err
	} else if active {
		return RunSnapshot{}, false, ErrActiveRun
	}
	if err := s.CreateQueued(ctx, runID, userID, chatID, inputMessageID, model, promptVersion, timeZone, config); err != nil {
		return RunSnapshot{}, false, err
	}
	if _, err := s.db.Exec(ctx, `
		insert into agent_jobs(id, kind, run_id, status)
		values($1, 'agent_run', $2, 'queued')`, jobID, runID); err != nil {
		return RunSnapshot{}, false, err
	}
	run := RunSnapshot{ID: runID, InputMessageID: inputMessageID, Status: "queued"}
	response, err := json.Marshal(map[string]any{"run": run})
	if err != nil {
		return RunSnapshot{}, false, err
	}
	if _, err := s.db.Exec(ctx, `
		insert into platform_idempotency_keys(principal_id, action, key, request_hash, status_code, response_json)
		values($1, 'retry_agent_run', $2, $3, $4, $5::jsonb)`, userID, key, requestHash, http.StatusAccepted, string(response)); err != nil {
		return RunSnapshot{}, false, err
	}
	if _, err := s.db.Exec(ctx, `select pg_notify('nano_agent_jobs', $1)`, jobID); err != nil {
		return RunSnapshot{}, false, err
	}
	return run, false, nil
}

type Store struct {
	db DBTX
}

func NewStore(db DBTX) *Store {
	return &Store{db: db}
}

func (s *Store) CreateQueued(ctx context.Context, runID, userID, chatID, inputMessageID, model, promptVersion, timeZone string, config RunConfig) error {
	_, err := s.db.Exec(ctx, `
		insert into agent_runs(
			id, user_id, chat_id, input_message_id, status, model, prompt_version,
			time_zone, deadline_at, action_decision_limit, final_decision_limit,
			action_limit, action_batch_limit, action_result_byte_limit, action_results_byte_limit
		)
		values(
			$1, $2, $3, $4, 'queued', $5, $6,
			$7, now() + ($8 * interval '1 millisecond'), $9, $10, $11, $12, $13, $14
		)`,
		runID, userID, chatID, inputMessageID, model, promptVersion,
		timeZone, config.Deadline.Milliseconds(), config.ActionDecisionLimit, config.FinalDecisionLimit,
		config.ActionLimit, config.ActionBatchLimit, config.ActionResultByteLimit, config.ActionResultsByteLimit,
	)
	return err
}
