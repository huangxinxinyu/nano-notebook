package replay

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/huangxinxinyu/nano-notebook/internal/agentobs"
	"github.com/huangxinxinyu/nano-notebook/internal/objectstore"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

var (
	ErrIdentityConflict = errors.New("Replay attachment identity conflicts with staged payload")
	ErrCapacityExceeded = errors.New("Replay staging capacity exceeded")
)

type StagerConfig struct {
	ObjectPrefix string
	Retention    time.Duration
}

type PostgresStager struct {
	pool         *pgxpool.Pool
	sealer       *Sealer
	objects      objectstore.Store
	objectPrefix string
	retention    time.Duration
}

type StageRequest struct {
	TraceID     agentobs.TraceID
	IdentityKey string
	Payload     PlainPayload
}

type StagedAttachment struct {
	AttachmentID     string
	TraceID          agentobs.TraceID
	IdentityKey      string
	Class            Class
	SchemaVersion    int
	PlaintextSHA256  string
	ObjectKey        string
	CiphertextBytes  int
	CiphertextSHA256 string
	Compression      string
	Encryption       string
	KeyID            string
	WrappedKey       []byte
	Nonce            []byte
	ExpiresAt        time.Time
}

func NewPostgresStager(pool *pgxpool.Pool, sealer *Sealer, objects objectstore.Store, config StagerConfig) (*PostgresStager, error) {
	if pool == nil || sealer == nil || objects == nil {
		return nil, errors.New("Replay Stager dependencies are incomplete")
	}
	config.ObjectPrefix = strings.Trim(strings.TrimSpace(config.ObjectPrefix), "/")
	if config.ObjectPrefix == "" {
		config.ObjectPrefix = "agent-replay-staging"
	}
	if len(config.ObjectPrefix) > 200 {
		return nil, errors.New("Replay staging object prefix is too long")
	}
	if config.Retention == 0 {
		config.Retention = 7 * 24 * time.Hour
	}
	if config.Retention <= 0 {
		return nil, errors.New("Replay retention must be positive")
	}
	return &PostgresStager{
		pool: pool, sealer: sealer, objects: objects,
		objectPrefix: config.ObjectPrefix, retention: config.Retention,
	}, nil
}

func (s *PostgresStager) Stage(ctx context.Context, request StageRequest) (StagedAttachment, error) {
	if err := validateStageRequest(request); err != nil {
		return StagedAttachment{}, err
	}
	existing, found, err := s.load(ctx, request.TraceID, request.IdentityKey)
	if err != nil {
		return StagedAttachment{}, err
	}
	if found {
		if err := reconcileStagedAttachment(existing, request.Payload); err != nil {
			return StagedAttachment{}, err
		}
		info, err := s.objects.Stat(ctx, existing.ObjectKey)
		if err != nil {
			return StagedAttachment{}, fmt.Errorf("staged Replay object is unavailable: %w", err)
		}
		if info.Size != int64(existing.CiphertextBytes) {
			return StagedAttachment{}, ErrIntegrity
		}
		return existing, nil
	}

	sealed, err := s.sealer.Seal(ctx, request.Payload)
	if err != nil {
		return StagedAttachment{}, err
	}
	attachmentID := uuid.NewString()
	objectKey := s.objectPrefix + "/" + uuid.NewString()
	if err := s.objects.Put(ctx, objectKey, sealed.Ciphertext); err != nil {
		return StagedAttachment{}, fmt.Errorf("stage Replay object: %w", err)
	}
	staged := StagedAttachment{
		AttachmentID: attachmentID, TraceID: request.TraceID, IdentityKey: request.IdentityKey,
		Class: sealed.Class, SchemaVersion: sealed.SchemaVersion, PlaintextSHA256: sealed.PlaintextSHA256,
		ObjectKey: objectKey, CiphertextBytes: len(sealed.Ciphertext), CiphertextSHA256: sealed.CiphertextSHA256,
		Compression: sealed.Compression, Encryption: sealed.Encryption, KeyID: sealed.KeyID,
		WrappedKey: append([]byte(nil), sealed.WrappedKey...), Nonce: append([]byte(nil), sealed.Nonce...),
	}
	stored, inserted, err := s.insert(ctx, staged)
	if err != nil {
		return StagedAttachment{}, s.cleanupFailedStage(ctx, objectKey, classifyStagingError(err))
	}
	if inserted {
		return stored, nil
	}
	if err := reconcileStagedAttachment(stored, request.Payload); err != nil {
		return StagedAttachment{}, s.cleanupFailedStage(ctx, objectKey, err)
	}
	if err := s.objects.Delete(ctx, objectKey); err != nil {
		return StagedAttachment{}, fmt.Errorf("remove duplicate Replay staging object: %w", err)
	}
	return stored, nil
}

func validateStageRequest(request StageRequest) error {
	if strings.TrimSpace(string(request.TraceID)) == "" || len(request.TraceID) > 160 ||
		strings.TrimSpace(request.IdentityKey) == "" || len(request.IdentityKey) > 200 {
		return errors.New("Replay Stage request identity is invalid")
	}
	validated, err := NewPlainPayload(request.Payload.Class, request.Payload.SchemaVersion, request.Payload.Bytes)
	if err != nil {
		return err
	}
	if request.Payload.SHA256 != "" && request.Payload.SHA256 != validated.SHA256 {
		return ErrIntegrity
	}
	return nil
}

func (s *PostgresStager) load(ctx context.Context, traceID agentobs.TraceID, identityKey string) (StagedAttachment, bool, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return StagedAttachment{}, false, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `set local role nano_worker`); err != nil {
		return StagedAttachment{}, false, err
	}
	staged, err := scanStagedAttachment(tx.QueryRow(ctx, stagedAttachmentSelect+`
		where trace_id = $1 and identity_key = $2`, traceID, identityKey))
	if errors.Is(err, pgx.ErrNoRows) {
		return StagedAttachment{}, false, nil
	}
	if err != nil {
		return StagedAttachment{}, false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return StagedAttachment{}, false, err
	}
	return staged, true, nil
}

func (s *PostgresStager) insert(ctx context.Context, staged StagedAttachment) (StagedAttachment, bool, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return StagedAttachment{}, false, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `set local role nano_worker`); err != nil {
		return StagedAttachment{}, false, err
	}
	var insertedID *string
	if err := tx.QueryRow(ctx, `
		insert into agentobs_replay_staging(
			attachment_id, trace_id, identity_key, class, schema_version,
			plaintext_sha256, object_key, ciphertext_bytes, ciphertext_sha256,
			compression, encryption, key_id, wrapped_key, nonce, expires_at
		) values (
			$1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14,
			now() + $15::interval
		)
		on conflict (trace_id, identity_key) do nothing
		returning attachment_id
	`, staged.AttachmentID, staged.TraceID, staged.IdentityKey, staged.Class, staged.SchemaVersion,
		staged.PlaintextSHA256, staged.ObjectKey, staged.CiphertextBytes, staged.CiphertextSHA256,
		staged.Compression, staged.Encryption, staged.KeyID, staged.WrappedKey, staged.Nonce,
		s.retention).Scan(&insertedID); err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return StagedAttachment{}, false, err
	}
	inserted := insertedID != nil
	stored, err := scanStagedAttachment(tx.QueryRow(ctx, stagedAttachmentSelect+`
		where trace_id = $1 and identity_key = $2`, staged.TraceID, staged.IdentityKey))
	if err != nil {
		return StagedAttachment{}, false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return StagedAttachment{}, false, err
	}
	return stored, inserted, nil
}

const stagedAttachmentSelect = `
	select attachment_id::text, trace_id, identity_key, class, schema_version,
		plaintext_sha256, object_key, ciphertext_bytes, ciphertext_sha256,
		compression, encryption, key_id, wrapped_key, nonce, expires_at
	from agentobs_replay_staging
`

type stagingRow interface {
	Scan(...any) error
}

func scanStagedAttachment(row stagingRow) (StagedAttachment, error) {
	var staged StagedAttachment
	if err := row.Scan(
		&staged.AttachmentID, &staged.TraceID, &staged.IdentityKey, &staged.Class, &staged.SchemaVersion,
		&staged.PlaintextSHA256, &staged.ObjectKey, &staged.CiphertextBytes, &staged.CiphertextSHA256,
		&staged.Compression, &staged.Encryption, &staged.KeyID, &staged.WrappedKey, &staged.Nonce, &staged.ExpiresAt,
	); err != nil {
		return StagedAttachment{}, err
	}
	return staged, nil
}

func reconcileStagedAttachment(staged StagedAttachment, payload PlainPayload) error {
	if staged.Class != payload.Class || staged.SchemaVersion != payload.SchemaVersion || staged.PlaintextSHA256 != payload.SHA256 {
		return ErrIdentityConflict
	}
	return nil
}

func (s *PostgresStager) cleanupFailedStage(ctx context.Context, objectKey string, cause error) error {
	if err := s.objects.Delete(ctx, objectKey); err != nil {
		return errors.Join(cause, fmt.Errorf("remove unreferenced Replay staging object: %w", err))
	}
	return cause
}

func classifyStagingError(err error) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "54000" {
		return fmt.Errorf("%w: %v", ErrCapacityExceeded, err)
	}
	if strings.Contains(strings.ToLower(err.Error()), "limit exceeded") {
		return fmt.Errorf("%w: %v", ErrCapacityExceeded, err)
	}
	return err
}
