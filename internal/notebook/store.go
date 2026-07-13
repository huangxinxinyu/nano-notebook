package notebook

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
	ErrQuotaReached        = errors.New("quota reached")
	ErrNotFound            = errors.New("notebook not found")
)

type Store struct {
	db DBTX
}

type DBTX interface {
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
	Query(context.Context, string, ...any) (pgx.Rows, error)
	QueryRow(context.Context, string, ...any) pgx.Row
}

type beginner interface {
	Begin(context.Context) (pgx.Tx, error)
}

type Notebook struct {
	ID       string    `json:"id"`
	Title    string    `json:"title"`
	RecentAt time.Time `json:"recent_at,omitempty"`
}

func NewStore(db DBTX) *Store {
	return &Store{db: db}
}

func (s *Store) ListOwned(ctx context.Context, userID string, query string) ([]Notebook, error) {
	rows, err := s.db.Query(ctx, `
		select n.id, n.title, n.recent_at
		from notebook_notebooks n
		join notebook_memberships m on m.notebook_id = n.id
		where m.user_id = $1
		  and m.role = 'owner'
		  and ($2 = '' or lower(n.title) like '%' || lower($2) || '%')
		order by n.recent_at desc
		limit 100`, userID, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	notebooks := make([]Notebook, 0)
	for rows.Next() {
		var notebook Notebook
		if err := rows.Scan(&notebook.ID, &notebook.Title, &notebook.RecentAt); err != nil {
			return nil, err
		}
		notebooks = append(notebooks, notebook)
	}
	return notebooks, rows.Err()
}

func (s *Store) CreateOwned(ctx context.Context, userID, key, requestHash, notebookID, title string) (Notebook, bool, error) {
	if tx, ok := s.db.(pgx.Tx); ok {
		return createOwnedInTx(ctx, tx, userID, key, requestHash, notebookID, title)
	}
	starter, ok := s.db.(beginner)
	if !ok {
		return Notebook{}, false, errors.New("notebook create requires transaction starter")
	}
	tx, err := starter.Begin(ctx)
	if err != nil {
		return Notebook{}, false, err
	}
	defer tx.Rollback(ctx)
	created, reused, err := createOwnedInTx(ctx, tx, userID, key, requestHash, notebookID, title)
	if err != nil {
		return Notebook{}, false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Notebook{}, false, err
	}
	return created, reused, nil
}

func createOwnedInTx(ctx context.Context, tx pgx.Tx, userID, key, requestHash, notebookID, title string) (Notebook, bool, error) {
	if _, err := tx.Exec(ctx, `select pg_advisory_xact_lock(hashtextextended($1, 0))`, "create_notebook:"+userID); err != nil {
		return Notebook{}, false, err
	}
	var existingHash, existingJSON string
	var existingStatus int
	err := tx.QueryRow(ctx, `
		select request_hash, status_code, response_json::text
		from platform_idempotency_keys
		where principal_id = $1 and action = 'create_notebook' and key = $2`, userID, key).Scan(&existingHash, &existingStatus, &existingJSON)
	if err == nil {
		if existingHash != requestHash {
			return Notebook{}, false, ErrIdempotencyMismatch
		}
		var existing struct {
			Notebook Notebook `json:"notebook"`
		}
		if err := json.Unmarshal([]byte(existingJSON), &existing); err != nil {
			return Notebook{}, false, err
		}
		return existing.Notebook, true, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return Notebook{}, false, err
	}
	var owned int
	if err := tx.QueryRow(ctx, `select count(*) from notebook_memberships where user_id = $1 and role = 'owner'`, userID).Scan(&owned); err != nil {
		return Notebook{}, false, err
	}
	if owned >= 100 {
		return Notebook{}, false, ErrQuotaReached
	}
	_, err = tx.Exec(ctx, `insert into notebook_notebooks(id, title) values($1, $2)`, notebookID, title)
	if err != nil {
		return Notebook{}, false, err
	}
	_, err = tx.Exec(ctx, `insert into notebook_memberships(notebook_id, user_id, role) values($1, $2, 'owner')`, notebookID, userID)
	if err != nil {
		return Notebook{}, false, err
	}
	created := Notebook{ID: notebookID, Title: title}
	responseBytes, err := json.Marshal(map[string]any{"notebook": created})
	if err != nil {
		return Notebook{}, false, err
	}
	_, err = tx.Exec(ctx, `
		insert into platform_idempotency_keys(principal_id, action, key, request_hash, status_code, response_json)
		values($1, 'create_notebook', $2, $3, $4, $5::jsonb)`, userID, key, requestHash, http.StatusCreated, string(responseBytes))
	if err != nil {
		return Notebook{}, false, err
	}
	return created, false, nil
}

func (s *Store) GetOwned(ctx context.Context, userID, notebookID string) (Notebook, error) {
	var notebook Notebook
	err := s.db.QueryRow(ctx, `
		select n.id, n.title, n.recent_at
		from notebook_notebooks n
		join notebook_memberships m on m.notebook_id = n.id
		where n.id = $1 and m.user_id = $2 and m.role = 'owner'`, notebookID, userID).Scan(&notebook.ID, &notebook.Title, &notebook.RecentAt)
	if err != nil {
		return Notebook{}, ErrNotFound
	}
	return notebook, nil
}
