package sourcepurge

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/huangxinxinyu/nano-notebook/internal/objectstore"
	"github.com/huangxinxinyu/nano-notebook/internal/qdrantstore"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type objectDeleter interface {
	Delete(context.Context, string) error
}

type projectionPurger interface {
	DeleteScope(context.Context, qdrantstore.Scope) error
}

type Processor struct {
	pool          *pgxpool.Pool
	objects       objectDeleter
	projections   projectionPurger
	leaseDuration time.Duration
}

type lease struct {
	id               string
	objectKeys       []string
	projectionScopes []projectionScope
	token            string
	attemptNo        int
}

type projectionScope struct {
	NotebookID     string `json:"notebook_id"`
	SourceID       string `json:"source_id"`
	RevisionID     string `json:"revision_id"`
	IndexVersionID string `json:"index_version_id"`
}

func NewProcessor(pool *pgxpool.Pool, objects objectDeleter, leaseDuration time.Duration) *Processor {
	return &Processor{pool: pool, objects: objects, leaseDuration: leaseDuration}
}

func NewProcessorWithProjectionPurger(pool *pgxpool.Pool, objects objectDeleter, projections projectionPurger, leaseDuration time.Duration) *Processor {
	return &Processor{pool: pool, objects: objects, projections: projections, leaseDuration: leaseDuration}
}

func (p *Processor) RunOnce(ctx context.Context) (bool, error) {
	if p == nil || p.pool == nil || p.objects == nil || p.leaseDuration <= 0 {
		return false, errors.New("invalid Source purge Processor")
	}
	if _, err := p.materializeExpiredUpload(ctx); err != nil {
		return false, err
	}
	claimed, ok, err := p.claim(ctx)
	if err != nil || !ok {
		return false, err
	}
	for _, objectKey := range claimed.objectKeys {
		err = p.objects.Delete(ctx, objectKey)
		if err != nil && !errors.Is(err, objectstore.ErrNotFound) {
			_ = p.releaseFailure(ctx, claimed)
			return true, err
		}
	}
	if len(claimed.projectionScopes) > 0 && p.projections == nil {
		_ = p.releaseFailure(ctx, claimed)
		return true, errors.New("Source projection purge is not configured")
	}
	for _, scope := range claimed.projectionScopes {
		if err := p.projections.DeleteScope(ctx, qdrantstore.Scope{
			NotebookID: scope.NotebookID, IndexVersionID: scope.IndexVersionID,
			Evidence: []qdrantstore.EvidenceRef{{SourceID: scope.SourceID, RevisionID: scope.RevisionID}},
		}); err != nil {
			_ = p.releaseFailure(ctx, claimed)
			return true, err
		}
	}
	if err := p.complete(ctx, claimed); err != nil {
		return true, err
	}
	return true, nil
}

func (p *Processor) materializeExpiredUpload(ctx context.Context) (bool, error) {
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `set local role nano_worker`); err != nil {
		return false, err
	}
	var intentID, sourceID, notebookID, userID, objectKey string
	err = tx.QueryRow(ctx, `
		select id, source_id, notebook_id, created_by_user_id, object_key
		from source_upload_intents
		where state='pending' and expires_at <= now()
		order by expires_at, id
		for update skip locked limit 1
	`).Scan(&intentID, &sourceID, &notebookID, &userID, &objectKey)
	if errors.Is(err, pgx.ErrNoRows) {
		if err := tx.Commit(ctx); err != nil {
			return false, err
		}
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if _, err := tx.Exec(ctx, `
		insert into source_purge_jobs(
			id, source_id, notebook_id, created_by_user_id, original_object_key, object_keys, projection_scopes, state
		) values ($1, $2, $3, $4, $5, jsonb_build_array($5::text), '[]'::jsonb, 'pending')
	`, "srcpurge_"+uuid.NewString(), sourceID, notebookID, userID, objectKey); err != nil {
		return false, err
	}
	if _, err := tx.Exec(ctx, `update source_upload_intents set state='expired' where id=$1`, intentID); err != nil {
		return false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return false, err
	}
	return true, nil
}

func (p *Processor) Run(ctx context.Context, pollInterval time.Duration) error {
	if pollInterval <= 0 {
		return errors.New("positive Source purge poll interval is required")
	}
	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()
	for {
		processed, err := p.RunOnce(ctx)
		if err != nil && ctx.Err() == nil {
			// The durable row was retained for retry; continue polling.
		}
		if processed && err == nil {
			continue
		}
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}

func (p *Processor) claim(ctx context.Context) (lease, bool, error) {
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return lease{}, false, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `set local role nano_worker`); err != nil {
		return lease{}, false, err
	}
	var now time.Time
	if err := tx.QueryRow(ctx, `select clock_timestamp()`).Scan(&now); err != nil {
		return lease{}, false, err
	}
	if _, err := tx.Exec(ctx, `
		update source_purge_jobs
		set state=case when attempt_no >= 10 then 'failed' else 'pending' end,
			lease_token=null, lease_expires_at=null,
			last_error_code=case when attempt_no >= 10 then 'retry_exhausted' else last_error_code end,
			updated_at=$1
		where state='running' and lease_expires_at <= $1
	`, now); err != nil {
		return lease{}, false, err
	}
	var id string
	err = tx.QueryRow(ctx, `
		select id from source_purge_jobs
		where state='pending' and attempt_no < 10
		order by created_at, id
		for update skip locked limit 1
	`).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		if err := tx.Commit(ctx); err != nil {
			return lease{}, false, err
		}
		return lease{}, false, nil
	}
	if err != nil {
		return lease{}, false, err
	}
	token := uuid.NewString()
	var claimed lease
	var objectManifest, projectionManifest []byte
	err = tx.QueryRow(ctx, `
		update source_purge_jobs
		set state='running', attempt_no=attempt_no+1, lease_token=$2::uuid,
			lease_expires_at=$3, updated_at=$1
		where id=$4
		returning id, object_keys, projection_scopes, lease_token::text, attempt_no
	`, now, token, now.Add(p.leaseDuration), id).Scan(&claimed.id, &objectManifest, &projectionManifest, &claimed.token, &claimed.attemptNo)
	if err != nil {
		return lease{}, false, err
	}
	if json.Unmarshal(objectManifest, &claimed.objectKeys) != nil || json.Unmarshal(projectionManifest, &claimed.projectionScopes) != nil || len(claimed.objectKeys) == 0 {
		return lease{}, false, errors.New("invalid Source purge manifest")
	}
	if err := tx.Commit(ctx); err != nil {
		return lease{}, false, err
	}
	return claimed, true, nil
}

func (p *Processor) complete(ctx context.Context, claimed lease) error {
	return p.finish(ctx, claimed, "succeeded", nil)
}

func (p *Processor) releaseFailure(ctx context.Context, claimed lease) error {
	state := "pending"
	if claimed.attemptNo >= 10 {
		state = "failed"
	}
	return p.finish(ctx, claimed, state, "object_delete_failed")
}

func (p *Processor) finish(ctx context.Context, claimed lease, state string, errorCode any) error {
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `set local role nano_worker`); err != nil {
		return err
	}
	tag, err := tx.Exec(ctx, `
		update source_purge_jobs
		set state=$3, lease_token=null, lease_expires_at=null, last_error_code=$4, updated_at=now()
		where id=$1 and state='running' and lease_token=$2::uuid and lease_expires_at > now()
	`, claimed.id, claimed.token, state, errorCode)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return errors.New("Source purge lease lost")
	}
	return tx.Commit(ctx)
}
