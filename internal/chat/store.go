package chat

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
	ErrIdempotencyMismatch = errors.New("idempotency mismatch")
	ErrMessageConflict     = errors.New("message id conflict")
	ErrNotFound            = errors.New("notebook or chat not found")
)

type DBTX interface {
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
	Query(context.Context, string, ...any) (pgx.Rows, error)
	QueryRow(context.Context, string, ...any) pgx.Row
}

type Store struct {
	db DBTX
}

type Chat struct {
	ID         string    `json:"id"`
	NotebookID string    `json:"notebook_id"`
	Title      string    `json:"title"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
}

type Message struct {
	ID        string    `json:"id"`
	ChatID    string    `json:"chat_id"`
	Role      string    `json:"role"`
	Content   string    `json:"content"`
	CreatedAt time.Time `json:"created_at"`
}

func NewStore(db DBTX) *Store {
	return &Store{db: db}
}

func (s *Store) ListPrivate(ctx context.Context, userID, notebookID string) ([]Chat, error) {
	if err := s.requireNotebookAccess(ctx, userID, notebookID); err != nil {
		return nil, err
	}
	rows, err := s.db.Query(ctx, `
		select id, notebook_id, title, created_at, updated_at
		from chat_chats
		where notebook_id = $1 and creator_user_id = $2
		order by updated_at desc, id desc`, notebookID, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	chats := make([]Chat, 0)
	for rows.Next() {
		var item Chat
		if err := rows.Scan(&item.ID, &item.NotebookID, &item.Title, &item.CreatedAt, &item.UpdatedAt); err != nil {
			return nil, err
		}
		chats = append(chats, item)
	}
	return chats, rows.Err()
}

func (s *Store) CreatePrivate(ctx context.Context, userID, notebookID, key, requestHash, chatID, title string) (Chat, bool, error) {
	if _, err := s.db.Exec(ctx, `select pg_advisory_xact_lock(hashtextextended($1, 0))`, "create_chat:"+userID); err != nil {
		return Chat{}, false, err
	}
	var existingHash, existingJSON string
	err := s.db.QueryRow(ctx, `
		select request_hash, response_json::text
		from platform_idempotency_keys
		where principal_id = $1 and action = 'create_chat' and key = $2`, userID, key).Scan(&existingHash, &existingJSON)
	if err == nil {
		if existingHash != requestHash {
			return Chat{}, false, ErrIdempotencyMismatch
		}
		var body struct {
			Chat Chat `json:"chat"`
		}
		if err := json.Unmarshal([]byte(existingJSON), &body); err != nil {
			return Chat{}, false, err
		}
		return body.Chat, true, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return Chat{}, false, err
	}
	if err := s.requireNotebookAccess(ctx, userID, notebookID); err != nil {
		return Chat{}, false, err
	}
	var created Chat
	err = s.db.QueryRow(ctx, `
		insert into chat_chats(id, notebook_id, creator_user_id, title)
		values($1, $2, $3, $4)
		returning id, notebook_id, title, created_at, updated_at`, chatID, notebookID, userID, title).
		Scan(&created.ID, &created.NotebookID, &created.Title, &created.CreatedAt, &created.UpdatedAt)
	if err != nil {
		return Chat{}, false, err
	}
	response, err := json.Marshal(map[string]any{"chat": created})
	if err != nil {
		return Chat{}, false, err
	}
	_, err = s.db.Exec(ctx, `
		insert into platform_idempotency_keys(principal_id, action, key, request_hash, status_code, response_json)
		values($1, 'create_chat', $2, $3, $4, $5::jsonb)`, userID, key, requestHash, http.StatusCreated, string(response))
	if err != nil {
		return Chat{}, false, err
	}
	return created, false, nil
}

func (s *Store) GetPrivate(ctx context.Context, userID, chatID string) (Chat, error) {
	var item Chat
	err := s.db.QueryRow(ctx, `
		select id, notebook_id, title, created_at, updated_at
		from chat_chats
		where id = $1 and creator_user_id = $2`, chatID, userID).
		Scan(&item.ID, &item.NotebookID, &item.Title, &item.CreatedAt, &item.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Chat{}, ErrNotFound
	}
	return item, err
}

func (s *Store) InsertUserMessage(ctx context.Context, messageID, chatID, content string) error {
	_, err := s.db.Exec(ctx, `
		insert into chat_messages(id, chat_id, role, content)
		values($1, $2, 'user', $3)`, messageID, chatID, content)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(ctx, `update chat_chats set updated_at = now() where id = $1`, chatID)
	return err
}

func (s *Store) MessageByID(ctx context.Context, messageID string) (Message, bool, error) {
	var message Message
	err := s.db.QueryRow(ctx, `
		select id, chat_id, role, content
		from chat_messages
		where id = $1`, messageID).Scan(&message.ID, &message.ChatID, &message.Role, &message.Content)
	if errors.Is(err, pgx.ErrNoRows) {
		return Message{}, false, nil
	}
	if err != nil {
		return Message{}, false, err
	}
	return message, true, nil
}

func (s *Store) ListMessages(ctx context.Context, chatID string) ([]Message, error) {
	rows, err := s.db.Query(ctx, `
		select id, chat_id, role, content, created_at
		from chat_messages
		where chat_id = $1
		order by created_at, id`, chatID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	messages := make([]Message, 0)
	for rows.Next() {
		var message Message
		if err := rows.Scan(&message.ID, &message.ChatID, &message.Role, &message.Content, &message.CreatedAt); err != nil {
			return nil, err
		}
		messages = append(messages, message)
	}
	return messages, rows.Err()
}

func (s *Store) requireNotebookAccess(ctx context.Context, userID, notebookID string) error {
	var allowed bool
	err := s.db.QueryRow(ctx, `
		select exists(
			select 1
			from notebook_memberships
			where notebook_id = $1 and user_id = $2
		)`, notebookID, userID).Scan(&allowed)
	if err != nil {
		return err
	}
	if !allowed {
		return ErrNotFound
	}
	return nil
}
