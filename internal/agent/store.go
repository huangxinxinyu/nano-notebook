package agent

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

var (
	ErrActiveRun   = errors.New("active run conflict")
	ErrRunNotFound = errors.New("agent run not found")
)

type DBTX interface {
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
	QueryRow(context.Context, string, ...any) pgx.Row
}

type RunRef struct {
	ID     string
	Status string
}

type RunSnapshot struct {
	ID        string  `json:"id"`
	Status    string  `json:"status"`
	ErrorCode *string `json:"error_code"`
}

type AssistantMessageSnapshot struct {
	ID         string    `json:"id"`
	Role       string    `json:"role"`
	Content    string    `json:"content"`
	AnswerMode string    `json:"answer_mode"`
	CreatedAt  time.Time `json:"created_at"`
}

type RunProjection struct {
	Run     RunSnapshot               `json:"run"`
	Message *AssistantMessageSnapshot `json:"message"`
}

func (s *Store) ByInputMessage(ctx context.Context, messageID string) (RunRef, error) {
	var run RunRef
	err := s.db.QueryRow(ctx, `select id, status from agent_runs where input_message_id = $1`, messageID).Scan(&run.ID, &run.Status)
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

func (s *Store) ActiveForChat(ctx context.Context, userID, chatID string) (RunSnapshot, bool, error) {
	var run RunSnapshot
	err := s.db.QueryRow(ctx, `
		select id, status, error_code
		from agent_runs
		where user_id = $1 and chat_id = $2 and status in ('queued', 'running')`, userID, chatID).
		Scan(&run.ID, &run.Status, &run.ErrorCode)
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
		select id, status, error_code, output_message_id
		from agent_runs
		where id = $1 and user_id = $2`, runID, userID).
		Scan(&projection.Run.ID, &projection.Run.Status, &projection.Run.ErrorCode, &outputMessageID)
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
		select id, role, content, answer_mode, created_at
		from chat_messages
		where id = $1`, *outputMessageID).
		Scan(&message.ID, &message.Role, &message.Content, &message.AnswerMode, &message.CreatedAt)
	if err != nil {
		return RunProjection{}, err
	}
	projection.Message = &message
	return projection, nil
}

type Store struct {
	db DBTX
}

func NewStore(db DBTX) *Store {
	return &Store{db: db}
}

func (s *Store) CreateQueued(ctx context.Context, runID, userID, chatID, inputMessageID, model, promptVersion string) error {
	_, err := s.db.Exec(ctx, `
		insert into agent_runs(id, user_id, chat_id, input_message_id, status, model, prompt_version)
		values($1, $2, $3, $4, 'queued', $5, $6)`, runID, userID, chatID, inputMessageID, model, promptVersion)
	return err
}
