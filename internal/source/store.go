package source

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

var (
	ErrNotFound     = errors.New("source or notebook not found")
	ErrDuplicate    = errors.New("duplicate Source")
	ErrQuotaReached = errors.New("Source quota reached")
)

type DuplicateError struct {
	ExistingSourceID string
}

func (e *DuplicateError) Error() string {
	return "duplicate Source: existing Source " + e.ExistingSourceID
}

func (e *DuplicateError) Unwrap() error {
	return ErrDuplicate
}

type Capability string

const (
	CapabilityRead     Capability = "source.read"
	CapabilityMaintain Capability = "source.maintain"
)

type Format string

const (
	FormatTXT Format = "txt"
)

type State string

const (
	StateUploaded State = "uploaded"
)

type Source struct {
	ID                string    `json:"id"`
	NotebookID        string    `json:"notebook_id"`
	Title             string    `json:"title"`
	Format            Format    `json:"format"`
	MediaType         string    `json:"media_type"`
	ByteSize          int64     `json:"byte_size"`
	ContentSHA256     string    `json:"content_sha256"`
	OriginalObjectKey string    `json:"-"`
	State             State     `json:"state"`
	CreatedAt         time.Time `json:"created_at"`
	UpdatedAt         time.Time `json:"updated_at"`
}

type CreateUploadedCommand struct {
	ID                string
	NotebookID        string
	Title             string
	Format            Format
	MediaType         string
	ByteSize          int64
	ContentSHA256     string
	OriginalObjectKey string
}

type DBTX interface {
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
	Query(context.Context, string, ...any) (pgx.Rows, error)
	QueryRow(context.Context, string, ...any) pgx.Row
}

type Store struct {
	db DBTX
}

func NewStore(db DBTX) *Store {
	return &Store{db: db}
}

func (s *Store) CreateUploaded(ctx context.Context, command CreateUploadedCommand) (Source, error) {
	if err := s.requireCapability(ctx, command.NotebookID, CapabilityMaintain); err != nil {
		return Source{}, err
	}
	if _, err := s.db.Exec(ctx, `
		select pg_advisory_xact_lock(hashtextextended($1, 0))
	`, "source-notebook:"+command.NotebookID); err != nil {
		return Source{}, err
	}
	var existingID string
	err := s.db.QueryRow(ctx, `
		select id
		from source_sources
		where notebook_id = $1 and input_kind = 'file' and content_sha256 = $2
	`, command.NotebookID, command.ContentSHA256).Scan(&existingID)
	if err == nil {
		return Source{}, &DuplicateError{ExistingSourceID: existingID}
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return Source{}, err
	}
	var sourceCount int
	if err := s.db.QueryRow(ctx, `
		select count(*) from source_sources where notebook_id = $1
	`, command.NotebookID).Scan(&sourceCount); err != nil {
		return Source{}, err
	}
	if sourceCount >= 50 {
		return Source{}, ErrQuotaReached
	}
	var created Source
	err = s.db.QueryRow(ctx, `
		insert into source_sources(
			id, notebook_id, input_kind, format, title, media_type, byte_size,
			content_sha256, original_object_key, state
		) values ($1, $2, 'file', $3, $4, $5, $6, $7, $8, 'uploaded')
		returning id, notebook_id, title, format, media_type, byte_size,
			content_sha256, original_object_key, state, created_at, updated_at`,
		command.ID, command.NotebookID, command.Format, command.Title, command.MediaType,
		command.ByteSize, command.ContentSHA256, command.OriginalObjectKey,
	).Scan(
		&created.ID, &created.NotebookID, &created.Title, &created.Format, &created.MediaType,
		&created.ByteSize, &created.ContentSHA256, &created.OriginalObjectKey, &created.State,
		&created.CreatedAt, &created.UpdatedAt,
	)
	return created, err
}

func (s *Store) ListForNotebook(ctx context.Context, notebookID string) ([]Source, error) {
	if err := s.requireCapability(ctx, notebookID, CapabilityRead); err != nil {
		return nil, err
	}
	rows, err := s.db.Query(ctx, `
		select id, notebook_id, title, format, media_type, byte_size,
			content_sha256, original_object_key, state, created_at, updated_at
		from source_sources
		where notebook_id = $1
		order by created_at, id`, notebookID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	sources := make([]Source, 0)
	for rows.Next() {
		var item Source
		if err := rows.Scan(
			&item.ID, &item.NotebookID, &item.Title, &item.Format, &item.MediaType,
			&item.ByteSize, &item.ContentSHA256, &item.OriginalObjectKey, &item.State,
			&item.CreatedAt, &item.UpdatedAt,
		); err != nil {
			return nil, err
		}
		sources = append(sources, item)
	}
	return sources, rows.Err()
}

func (s *Store) requireCapability(ctx context.Context, notebookID string, capability Capability) error {
	var allowed bool
	if err := s.db.QueryRow(ctx, `select nano_has_notebook_capability($1, $2)`, notebookID, capability).Scan(&allowed); err != nil {
		return err
	}
	if !allowed {
		return ErrNotFound
	}
	return nil
}
