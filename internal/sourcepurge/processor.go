package sourcepurge

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/huangxinxinyu/nano-notebook/internal/objectstore"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type objectDeleter interface {
	Delete(context.Context, string) error
}

type Processor struct {
	pool          *pgxpool.Pool
	objects       objectDeleter
	leaseDuration time.Duration
}

type lease struct {
	id        string
	objectKey string
	token     string
	attemptNo int
}

func NewProcessor(pool *pgxpool.Pool, objects objectDeleter, leaseDuration time.Duration) *Processor {
	return &Processor{pool: pool, objects: objects, leaseDuration: leaseDuration}
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
	err = p.objects.Delete(ctx, claimed.objectKey)
	if err != nil && !errors.Is(err, objectstore.ErrNotFound) {
		_ = p.releaseFailure(ctx, claimed)
		return true, err
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
			id, source_id, notebook_id, created_by_user_id, original_object_key, state
		) values ($1, $2, $3, $4, $5, 'pending')
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
	now := time.Now().UTC()
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
	err = tx.QueryRow(ctx, `
		update source_purge_jobs
		set state='running', attempt_no=attempt_no+1, lease_token=$2::uuid,
			lease_expires_at=$3, updated_at=$1
		where id=$4
		returning id, original_object_key, lease_token::text, attempt_no
	`, now, token, now.Add(p.leaseDuration), id).Scan(&claimed.id, &claimed.objectKey, &claimed.token, &claimed.attemptNo)
	if err != nil {
		return lease{}, false, err
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
