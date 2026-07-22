package source

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

var (
	ErrNotFound            = errors.New("source or notebook not found")
	ErrDuplicate           = errors.New("duplicate Source")
	ErrQuotaReached        = errors.New("Source quota reached")
	ErrIdempotencyMismatch = errors.New("upload intent idempotency mismatch")
	ErrUploadIntentExpired = errors.New("upload intent expired")
	ErrStateConflict       = errors.New("Source state conflict")
	ErrInvalidInput        = errors.New("invalid Source input")
	ErrAdmissionInProgress = errors.New("URL admission in progress")
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
	FormatTXT      Format = "txt"
	FormatMarkdown Format = "markdown"
	FormatPDF      Format = "pdf"
	FormatDOCX     Format = "docx"
	FormatPPTX     Format = "pptx"
	FormatMP3      Format = "mp3"
	FormatWAV      Format = "wav"
	FormatM4A      Format = "m4a"
	FormatPNG      Format = "png"
	FormatJPEG     Format = "jpeg"
	FormatWebP     Format = "webp"
	FormatHTML     Format = "html"
	FormatYouTube  Format = "youtube"
)

type fileFormatSpec struct {
	extensions []string
	mediaTypes []string
}

var supportedFileFormats = map[Format]fileFormatSpec{
	FormatTXT:      {extensions: []string{".txt"}, mediaTypes: []string{"text/plain"}},
	FormatMarkdown: {extensions: []string{".md", ".markdown"}, mediaTypes: []string{"text/markdown"}},
	FormatPDF:      {extensions: []string{".pdf"}, mediaTypes: []string{"application/pdf"}},
	FormatDOCX:     {extensions: []string{".docx"}, mediaTypes: []string{"application/vnd.openxmlformats-officedocument.wordprocessingml.document"}},
	FormatPPTX:     {extensions: []string{".pptx"}, mediaTypes: []string{"application/vnd.openxmlformats-officedocument.presentationml.presentation"}},
	FormatMP3:      {extensions: []string{".mp3"}, mediaTypes: []string{"audio/mpeg"}},
	FormatWAV:      {extensions: []string{".wav"}, mediaTypes: []string{"audio/wav", "audio/x-wav"}},
	FormatM4A:      {extensions: []string{".m4a"}, mediaTypes: []string{"audio/mp4", "audio/x-m4a"}},
	FormatPNG:      {extensions: []string{".png"}, mediaTypes: []string{"image/png"}},
	FormatJPEG:     {extensions: []string{".jpg", ".jpeg"}, mediaTypes: []string{"image/jpeg"}},
	FormatWebP:     {extensions: []string{".webp"}, mediaTypes: []string{"image/webp"}},
}

func ValidFileAdmission(title string, format Format, mediaType string) bool {
	spec, ok := supportedFileFormats[format]
	if !ok {
		return false
	}
	extension := strings.ToLower(filepath.Ext(strings.TrimSpace(title)))
	mediaType = strings.ToLower(strings.TrimSpace(mediaType))
	extensionAllowed := false
	for _, candidate := range spec.extensions {
		if extension == candidate {
			extensionAllowed = true
			break
		}
	}
	if !extensionAllowed {
		return false
	}
	for _, candidate := range spec.mediaTypes {
		if mediaType == candidate {
			return true
		}
	}
	return false
}

func FormatForMediaType(mediaType string) (Format, bool) {
	mediaType = strings.ToLower(strings.TrimSpace(mediaType))
	if mediaType == "text/html" {
		return FormatHTML, true
	}
	if mediaType == "application/vnd.nano.youtube-captions+json" {
		return FormatYouTube, true
	}
	for format, spec := range supportedFileFormats {
		for _, candidate := range spec.mediaTypes {
			if mediaType == candidate {
				return format, true
			}
		}
	}
	return "", false
}

type State string

const (
	StateUploaded    State = "uploaded"
	StateValidating  State = "validating"
	StateNormalizing State = "normalizing"
	StateSegmenting  State = "segmenting"
	StateIndexing    State = "indexing"
	StateVerifying   State = "verifying"
	StateReady       State = "ready"
	StateFailed      State = "failed"
)

type UploadIntentState string

const (
	UploadIntentPending   UploadIntentState = "pending"
	UploadIntentFinalized UploadIntentState = "finalized"
	UploadIntentExpired   UploadIntentState = "expired"
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
	FailureCode       string    `json:"-"`
	CreatedAt         time.Time `json:"created_at"`
	UpdatedAt         time.Time `json:"updated_at"`
}

func SafeFailureReason(internalCode string) string {
	switch strings.TrimSpace(internalCode) {
	case "processing_budget_exceeded":
		return "limits_exceeded"
	case "source_object_missing", "source_integrity_mismatch":
		return "source_unavailable"
	case "extraction_invalid":
		return "content_unreadable"
	case "projection_invalid":
		return "indexing_failed"
	case "retrieval_unavailable":
		return "retrieval_unavailable"
	case "retry_exhausted":
		return "processing_interrupted"
	default:
		return "processing_failed"
	}
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

type UploadIntent struct {
	ID             string            `json:"id"`
	SourceID       string            `json:"source_id"`
	NotebookID     string            `json:"notebook_id"`
	IdempotencyKey string            `json:"-"`
	RequestHash    string            `json:"-"`
	Title          string            `json:"title"`
	Format         Format            `json:"format"`
	MediaType      string            `json:"media_type"`
	ByteSize       int64             `json:"byte_size"`
	ContentSHA256  string            `json:"content_sha256"`
	ObjectKey      string            `json:"-"`
	State          UploadIntentState `json:"state"`
	ExpiresAt      time.Time         `json:"expires_at"`
	CreatedAt      time.Time         `json:"created_at"`
	FinalizedAt    *time.Time        `json:"finalized_at,omitempty"`
}

type CreateUploadIntentCommand struct {
	ID             string
	SourceID       string
	NotebookID     string
	IdempotencyKey string
	RequestHash    string
	Title          string
	Format         Format
	MediaType      string
	ByteSize       int64
	ContentSHA256  string
	ObjectKey      string
	ExpiresAt      time.Time
}

type URLAdmissionState string

const (
	URLAdmissionPending   URLAdmissionState = "pending"
	URLAdmissionCompleted URLAdmissionState = "completed"
	URLAdmissionFailed    URLAdmissionState = "failed"
)

type URLAdmission struct {
	ID             string
	SourceID       string
	NotebookID     string
	IdempotencyKey string
	RequestHash    string
	RequestURL     string
	State          URLAdmissionState
	ErrorCode      *string
	CreatedAt      time.Time
	CompletedAt    *time.Time
}

type BeginURLAdmissionCommand struct {
	ID             string
	SourceID       string
	NotebookID     string
	IdempotencyKey string
	RequestHash    string
	RequestURL     string
}

type FinalizeURLAdmissionCommand struct {
	AdmissionID       string
	ProcessingJobID   string
	Title             string
	Format            Format
	MediaType         string
	ByteSize          int64
	ContentSHA256     string
	OriginalObjectKey string
	FinalURL          string
	CompletedAt       time.Time
}

type PurgeState string

const (
	PurgePending PurgeState = "pending"
)

type PurgeIntent struct {
	ID                string     `json:"id"`
	SourceID          string     `json:"source_id"`
	NotebookID        string     `json:"notebook_id"`
	OriginalObjectKey string     `json:"-"`
	State             PurgeState `json:"state"`
	CreatedAt         time.Time  `json:"created_at"`
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

func (s *Store) CreateUploadIntent(ctx context.Context, command CreateUploadIntentCommand) (UploadIntent, bool, error) {
	if err := s.requireCapability(ctx, command.NotebookID, CapabilityMaintain); err != nil {
		return UploadIntent{}, false, err
	}
	var principalID string
	if err := s.db.QueryRow(ctx, `select nullif(current_setting('app.principal_id', true), '')`).Scan(&principalID); err != nil {
		return UploadIntent{}, false, err
	}
	if _, err := s.db.Exec(ctx, `
		select pg_advisory_xact_lock(hashtextextended($1, 0))
	`, "source-upload-intent:"+principalID+":"+command.IdempotencyKey); err != nil {
		return UploadIntent{}, false, err
	}
	existing, err := s.uploadIntentByIdempotency(ctx, principalID, command.IdempotencyKey)
	if err == nil {
		if existing.RequestHash != command.RequestHash {
			return UploadIntent{}, false, ErrIdempotencyMismatch
		}
		return existing, true, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return UploadIntent{}, false, err
	}

	var created UploadIntent
	err = s.db.QueryRow(ctx, `
		insert into source_upload_intents(
			id, source_id, notebook_id, created_by_user_id, idempotency_key, request_hash,
			title, format, media_type, byte_size, content_sha256, object_key, state, expires_at
		) values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, 'pending', $13)
		returning id, source_id, notebook_id, idempotency_key, request_hash, title, format,
			media_type, byte_size, content_sha256, object_key, state, expires_at, created_at, finalized_at`,
		command.ID, command.SourceID, command.NotebookID, principalID, command.IdempotencyKey,
		command.RequestHash, command.Title, command.Format, command.MediaType, command.ByteSize,
		command.ContentSHA256, command.ObjectKey, command.ExpiresAt,
	).Scan(
		&created.ID, &created.SourceID, &created.NotebookID, &created.IdempotencyKey,
		&created.RequestHash, &created.Title, &created.Format, &created.MediaType, &created.ByteSize,
		&created.ContentSHA256, &created.ObjectKey, &created.State, &created.ExpiresAt,
		&created.CreatedAt, &created.FinalizedAt,
	)
	return created, false, err
}

func (s *Store) BeginURLAdmission(ctx context.Context, command BeginURLAdmissionCommand) (URLAdmission, bool, error) {
	if err := s.requireCapability(ctx, command.NotebookID, CapabilityMaintain); err != nil {
		return URLAdmission{}, false, err
	}
	var principalID string
	if err := s.db.QueryRow(ctx, `select nullif(current_setting('app.principal_id', true), '')`).Scan(&principalID); err != nil {
		return URLAdmission{}, false, err
	}
	if _, err := s.db.Exec(ctx, `select pg_advisory_xact_lock(hashtextextended($1, 0))`, "source-url-admission:"+principalID+":"+command.IdempotencyKey); err != nil {
		return URLAdmission{}, false, err
	}
	existing, err := s.urlAdmissionByKey(ctx, principalID, command.IdempotencyKey)
	if err == nil {
		if existing.RequestHash != command.RequestHash {
			return URLAdmission{}, false, ErrIdempotencyMismatch
		}
		if existing.State == URLAdmissionPending {
			return URLAdmission{}, false, ErrAdmissionInProgress
		}
		if existing.State == URLAdmissionCompleted {
			return existing, true, nil
		}
		_, err = s.db.Exec(ctx, `
			update source_url_admissions set state='pending', error_code=null, completed_at=null where id=$1
		`, existing.ID)
		if err != nil {
			return URLAdmission{}, false, err
		}
		existing.State, existing.ErrorCode, existing.CompletedAt = URLAdmissionPending, nil, nil
		return existing, false, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return URLAdmission{}, false, err
	}
	var created URLAdmission
	err = s.db.QueryRow(ctx, `
		insert into source_url_admissions(
			id, source_id, notebook_id, created_by_user_id, idempotency_key, request_hash, request_url, state
		) values ($1, $2, $3, $4, $5, $6, $7, 'pending')
		returning id, source_id, notebook_id, idempotency_key, request_hash, request_url,
			state, error_code, created_at, completed_at
	`, command.ID, command.SourceID, command.NotebookID, principalID, command.IdempotencyKey,
		command.RequestHash, command.RequestURL).Scan(
		&created.ID, &created.SourceID, &created.NotebookID, &created.IdempotencyKey,
		&created.RequestHash, &created.RequestURL, &created.State, &created.ErrorCode,
		&created.CreatedAt, &created.CompletedAt,
	)
	return created, false, err
}

func (s *Store) FinalizeURLAdmission(ctx context.Context, command FinalizeURLAdmissionCommand) (Source, bool, error) {
	var admission URLAdmission
	err := s.db.QueryRow(ctx, `
		select id, source_id, notebook_id, idempotency_key, request_hash, request_url,
			state, error_code, created_at, completed_at
		from source_url_admissions where id=$1 for update
	`, command.AdmissionID).Scan(
		&admission.ID, &admission.SourceID, &admission.NotebookID, &admission.IdempotencyKey,
		&admission.RequestHash, &admission.RequestURL, &admission.State, &admission.ErrorCode,
		&admission.CreatedAt, &admission.CompletedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return Source{}, false, ErrNotFound
	}
	if err != nil {
		return Source{}, false, err
	}
	if err := s.requireCapability(ctx, admission.NotebookID, CapabilityMaintain); err != nil {
		return Source{}, false, err
	}
	if admission.State == URLAdmissionCompleted {
		item, err := s.sourceByID(ctx, admission.SourceID)
		return item, true, err
	}
	if admission.State != URLAdmissionPending {
		return Source{}, false, ErrStateConflict
	}
	if _, err := s.db.Exec(ctx, `select pg_advisory_xact_lock(hashtextextended($1, 0))`, "source-notebook:"+admission.NotebookID); err != nil {
		return Source{}, false, err
	}
	var sourceCount int
	if err := s.db.QueryRow(ctx, `select count(*) from source_sources where notebook_id=$1`, admission.NotebookID).Scan(&sourceCount); err != nil {
		return Source{}, false, err
	}
	if sourceCount >= 50 {
		return Source{}, false, ErrQuotaReached
	}
	var created Source
	err = s.db.QueryRow(ctx, `
		insert into source_sources(
			id, notebook_id, input_kind, format, title, media_type, byte_size,
			content_sha256, original_object_key, origin_url, final_url, state
		) values ($1, $2, 'url', $3, $4, $5, $6, $7, $8, $9, $10, 'uploaded')
		returning id, notebook_id, title, format, media_type, byte_size,
			content_sha256, original_object_key, state, created_at, updated_at
	`, admission.SourceID, admission.NotebookID, command.Format, command.Title, command.MediaType,
		command.ByteSize, command.ContentSHA256, command.OriginalObjectKey, admission.RequestURL,
		command.FinalURL).Scan(
		&created.ID, &created.NotebookID, &created.Title, &created.Format, &created.MediaType,
		&created.ByteSize, &created.ContentSHA256, &created.OriginalObjectKey, &created.State,
		&created.CreatedAt, &created.UpdatedAt,
	)
	if err != nil {
		return Source{}, false, err
	}
	if _, err := s.db.Exec(ctx, `
		insert into source_processing_jobs(id, source_id, notebook_id, status) values ($1, $2, $3, 'queued')
	`, command.ProcessingJobID, created.ID, created.NotebookID); err != nil {
		return Source{}, false, err
	}
	if _, err := s.db.Exec(ctx, `
		update source_url_admissions set state='completed', completed_at=$2 where id=$1
	`, admission.ID, command.CompletedAt); err != nil {
		return Source{}, false, err
	}
	return created, false, nil
}

func (s *Store) FailURLAdmission(ctx context.Context, id, errorCode string, completedAt time.Time) error {
	tag, err := s.db.Exec(ctx, `
		update source_url_admissions
		set state='failed', error_code=$2, completed_at=$3
		where id=$1 and state='pending'
	`, id, errorCode, completedAt)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return ErrStateConflict
	}
	return nil
}

func (s *Store) urlAdmissionByKey(ctx context.Context, principalID, key string) (URLAdmission, error) {
	var admission URLAdmission
	err := s.db.QueryRow(ctx, `
		select id, source_id, notebook_id, idempotency_key, request_hash, request_url,
			state, error_code, created_at, completed_at
		from source_url_admissions where created_by_user_id=$1 and idempotency_key=$2
	`, principalID, key).Scan(
		&admission.ID, &admission.SourceID, &admission.NotebookID, &admission.IdempotencyKey,
		&admission.RequestHash, &admission.RequestURL, &admission.State, &admission.ErrorCode,
		&admission.CreatedAt, &admission.CompletedAt,
	)
	return admission, err
}

func (s *Store) SourceByID(ctx context.Context, id string) (Source, error) {
	return s.sourceByID(ctx, id)
}

func (s *Store) uploadIntentByIdempotency(ctx context.Context, principalID, key string) (UploadIntent, error) {
	var intent UploadIntent
	err := s.db.QueryRow(ctx, `
		select id, source_id, notebook_id, idempotency_key, request_hash, title, format,
			media_type, byte_size, content_sha256, object_key, state, expires_at, created_at, finalized_at
		from source_upload_intents
		where created_by_user_id = $1 and idempotency_key = $2`, principalID, key,
	).Scan(
		&intent.ID, &intent.SourceID, &intent.NotebookID, &intent.IdempotencyKey,
		&intent.RequestHash, &intent.Title, &intent.Format, &intent.MediaType, &intent.ByteSize,
		&intent.ContentSHA256, &intent.ObjectKey, &intent.State, &intent.ExpiresAt,
		&intent.CreatedAt, &intent.FinalizedAt,
	)
	return intent, err
}

func (s *Store) UploadIntentByID(ctx context.Context, id string) (UploadIntent, error) {
	var intent UploadIntent
	err := s.db.QueryRow(ctx, `
		select id, source_id, notebook_id, idempotency_key, request_hash, title, format,
			media_type, byte_size, content_sha256, object_key, state, expires_at, created_at, finalized_at
		from source_upload_intents
		where id = $1`, id,
	).Scan(
		&intent.ID, &intent.SourceID, &intent.NotebookID, &intent.IdempotencyKey,
		&intent.RequestHash, &intent.Title, &intent.Format, &intent.MediaType, &intent.ByteSize,
		&intent.ContentSHA256, &intent.ObjectKey, &intent.State, &intent.ExpiresAt,
		&intent.CreatedAt, &intent.FinalizedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return UploadIntent{}, ErrNotFound
	}
	return intent, err
}

func (s *Store) FinalizeUploadIntent(ctx context.Context, id, processingJobID, originalObjectKey string, now time.Time) (Source, bool, error) {
	var intent UploadIntent
	err := s.db.QueryRow(ctx, `
		select id, source_id, notebook_id, idempotency_key, request_hash, title, format,
			media_type, byte_size, content_sha256, object_key, state, expires_at, created_at, finalized_at
		from source_upload_intents
		where id = $1
		for update`, id,
	).Scan(
		&intent.ID, &intent.SourceID, &intent.NotebookID, &intent.IdempotencyKey,
		&intent.RequestHash, &intent.Title, &intent.Format, &intent.MediaType, &intent.ByteSize,
		&intent.ContentSHA256, &intent.ObjectKey, &intent.State, &intent.ExpiresAt,
		&intent.CreatedAt, &intent.FinalizedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return Source{}, false, ErrNotFound
	}
	if err != nil {
		return Source{}, false, err
	}
	if err := s.requireCapability(ctx, intent.NotebookID, CapabilityMaintain); err != nil {
		return Source{}, false, err
	}
	if intent.State == UploadIntentFinalized {
		created, err := s.sourceByID(ctx, intent.SourceID)
		return created, true, err
	}
	if intent.State != UploadIntentPending || !intent.ExpiresAt.After(now) {
		return Source{}, false, ErrUploadIntentExpired
	}

	created, err := s.CreateUploaded(ctx, CreateUploadedCommand{
		ID: intent.SourceID, NotebookID: intent.NotebookID, Title: intent.Title,
		Format: intent.Format, MediaType: intent.MediaType, ByteSize: intent.ByteSize,
		ContentSHA256: intent.ContentSHA256, OriginalObjectKey: originalObjectKey,
	})
	if err != nil {
		return Source{}, false, err
	}
	if _, err := s.db.Exec(ctx, `
		insert into source_processing_jobs(id, source_id, notebook_id, status)
		values ($1, $2, $3, 'queued')
	`, processingJobID, created.ID, created.NotebookID); err != nil {
		return Source{}, false, err
	}
	if _, err := s.db.Exec(ctx, `
		update source_upload_intents
		set state = 'finalized', finalized_at = $2
		where id = $1
	`, intent.ID, now); err != nil {
		return Source{}, false, err
	}
	return created, false, nil
}

func (s *Store) sourceByID(ctx context.Context, id string) (Source, error) {
	var item Source
	err := s.db.QueryRow(ctx, `
		select id, notebook_id, title, format, media_type, byte_size,
			content_sha256, original_object_key, state,
			coalesce((select j.last_error_code from source_processing_jobs j where j.source_id=source_sources.id order by j.created_at desc, j.id desc limit 1),''),
			created_at, updated_at
		from source_sources where id = $1`, id,
	).Scan(
		&item.ID, &item.NotebookID, &item.Title, &item.Format, &item.MediaType,
		&item.ByteSize, &item.ContentSHA256, &item.OriginalObjectKey, &item.State, &item.FailureCode,
		&item.CreatedAt, &item.UpdatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return Source{}, ErrNotFound
	}
	return item, err
}

func (s *Store) Rename(ctx context.Context, id, title string) (Source, error) {
	title = strings.TrimSpace(title)
	if title == "" || len([]rune(title)) > 255 {
		return Source{}, ErrInvalidInput
	}
	current, err := s.sourceByID(ctx, id)
	if err != nil {
		return Source{}, err
	}
	if err := s.requireCapability(ctx, current.NotebookID, CapabilityMaintain); err != nil {
		return Source{}, err
	}
	if _, err := s.db.Exec(ctx, `update source_sources set title=$2, updated_at=now() where id=$1`, id, title); err != nil {
		return Source{}, err
	}
	return s.sourceByID(ctx, id)
}

func (s *Store) RetryFailed(ctx context.Context, id string) error {
	current, err := s.sourceByIDForUpdate(ctx, id)
	if err != nil {
		return err
	}
	if err := s.requireCapability(ctx, current.NotebookID, CapabilityMaintain); err != nil {
		return err
	}
	if current.State != StateFailed {
		return ErrStateConflict
	}
	jobUpdate, err := s.db.Exec(ctx, `
		update source_processing_jobs
		set status='queued', attempt_no=0, available_at=now(), lease_token=null,
			lease_expires_at=null, last_error_code=null, updated_at=now()
		where source_id=$1 and status='failed'
	`, id)
	if err != nil {
		return err
	}
	if jobUpdate.RowsAffected() != 1 {
		return ErrStateConflict
	}
	commandTag, err := s.db.Exec(ctx, `update source_sources set state='uploaded', updated_at=now() where id=$1`, id)
	if err != nil {
		return err
	}
	if commandTag.RowsAffected() != 1 {
		return ErrStateConflict
	}
	return nil
}

func (s *Store) Remove(ctx context.Context, id, purgeID string) (PurgeIntent, error) {
	current, err := s.sourceByIDForUpdate(ctx, id)
	if err != nil {
		return PurgeIntent{}, err
	}
	if err := s.requireCapability(ctx, current.NotebookID, CapabilityMaintain); err != nil {
		return PurgeIntent{}, err
	}
	var principalID string
	if err := s.db.QueryRow(ctx, `select nullif(current_setting('app.principal_id', true), '')`).Scan(&principalID); err != nil {
		return PurgeIntent{}, err
	}
	objectKeys := []string{current.OriginalObjectKey}
	rows, err := s.db.Query(ctx, `
		select artifact_object_key from source_evidence_revisions where source_id=$1 order by revision_no
	`, current.ID)
	if err != nil {
		return PurgeIntent{}, err
	}
	for rows.Next() {
		var objectKey string
		if err := rows.Scan(&objectKey); err != nil {
			rows.Close()
			return PurgeIntent{}, err
		}
		objectKeys = append(objectKeys, objectKey)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return PurgeIntent{}, err
	}
	rows.Close()
	rows, err = s.db.Query(ctx, `
		select object_key from source_viewer_artifacts where source_id=$1 order by revision_id,ordinal
	`, current.ID)
	if err != nil {
		return PurgeIntent{}, err
	}
	for rows.Next() {
		var objectKey string
		if err := rows.Scan(&objectKey); err != nil {
			rows.Close()
			return PurgeIntent{}, err
		}
		objectKeys = append(objectKeys, objectKey)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return PurgeIntent{}, err
	}
	rows.Close()
	type projectionScope struct {
		NotebookID     string `json:"notebook_id"`
		SourceID       string `json:"source_id"`
		RevisionID     string `json:"revision_id"`
		IndexVersionID string `json:"index_version_id"`
	}
	projectionScopes := make([]projectionScope, 0)
	rows, err = s.db.Query(ctx, `
		select b.notebook_id,b.source_id,b.revision_id,b.index_version_id
		from retrieval_source_index_builds b where b.source_id=$1
		order by b.index_version_id,b.revision_id
	`, current.ID)
	if err != nil {
		return PurgeIntent{}, err
	}
	for rows.Next() {
		var scope projectionScope
		if err := rows.Scan(&scope.NotebookID, &scope.SourceID, &scope.RevisionID, &scope.IndexVersionID); err != nil {
			rows.Close()
			return PurgeIntent{}, err
		}
		projectionScopes = append(projectionScopes, scope)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return PurgeIntent{}, err
	}
	rows.Close()
	sort.Strings(objectKeys)
	objectManifest, err := json.Marshal(objectKeys)
	if err != nil {
		return PurgeIntent{}, err
	}
	projectionManifest, err := json.Marshal(projectionScopes)
	if err != nil {
		return PurgeIntent{}, err
	}
	var purge PurgeIntent
	err = s.db.QueryRow(ctx, `
		insert into source_purge_jobs(
			id, source_id, notebook_id, created_by_user_id, original_object_key, object_keys, projection_scopes, state
		) values ($1, $2, $3, $4, $5, $6, $7, 'pending')
		returning id, source_id, notebook_id, original_object_key, state, created_at
	`, purgeID, current.ID, current.NotebookID, principalID, current.OriginalObjectKey, objectManifest, projectionManifest).Scan(
		&purge.ID, &purge.SourceID, &purge.NotebookID, &purge.OriginalObjectKey, &purge.State, &purge.CreatedAt,
	)
	if err != nil {
		return PurgeIntent{}, err
	}
	if _, err := s.db.Exec(ctx, `delete from source_sources where id=$1`, current.ID); err != nil {
		return PurgeIntent{}, err
	}
	return purge, nil
}

func (s *Store) sourceByIDForUpdate(ctx context.Context, id string) (Source, error) {
	var item Source
	err := s.db.QueryRow(ctx, `
		select id, notebook_id, title, format, media_type, byte_size,
			content_sha256, original_object_key, state, created_at, updated_at
		from source_sources where id=$1 for update`, id,
	).Scan(
		&item.ID, &item.NotebookID, &item.Title, &item.Format, &item.MediaType,
		&item.ByteSize, &item.ContentSHA256, &item.OriginalObjectKey, &item.State,
		&item.CreatedAt, &item.UpdatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return Source{}, ErrNotFound
	}
	return item, err
}

func (s *Store) ListForNotebook(ctx context.Context, notebookID string) ([]Source, error) {
	if err := s.requireCapability(ctx, notebookID, CapabilityRead); err != nil {
		return nil, err
	}
	rows, err := s.db.Query(ctx, `
		select s.id, s.notebook_id, s.title, s.format, s.media_type, s.byte_size,
			s.content_sha256, s.original_object_key, s.state, coalesce(j.last_error_code,''), s.created_at, s.updated_at
		from source_sources s
		left join lateral (
			select last_error_code from source_processing_jobs where source_id=s.id order by created_at desc, id desc limit 1
		) j on true
		where s.notebook_id = $1
		order by s.created_at, s.id`, notebookID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	sources := make([]Source, 0)
	for rows.Next() {
		var item Source
		if err := rows.Scan(
			&item.ID, &item.NotebookID, &item.Title, &item.Format, &item.MediaType,
			&item.ByteSize, &item.ContentSHA256, &item.OriginalObjectKey, &item.State, &item.FailureCode,
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
