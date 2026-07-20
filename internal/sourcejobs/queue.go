package sourcejobs

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/huangxinxinyu/nano-notebook/internal/source"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var (
	ErrLeaseLost          = errors.New("Source processing lease lost")
	ErrTransitionConflict = errors.New("Source state transition conflict")
)

type Lease struct {
	ID             string
	SourceID       string
	NotebookID     string
	AttemptNo      int
	LeaseToken     string
	LeaseExpiresAt time.Time
}

type Queue struct {
	pool          *pgxpool.Pool
	leaseDuration time.Duration
}

func NewQueue(pool *pgxpool.Pool, leaseDuration time.Duration) *Queue {
	return &Queue{pool: pool, leaseDuration: leaseDuration}
}

func (q *Queue) Claim(ctx context.Context) (Lease, bool, error) {
	if q == nil || q.pool == nil || q.leaseDuration <= 0 {
		return Lease{}, false, errors.New("invalid Source processing Queue")
	}
	tx, err := q.pool.Begin(ctx)
	if err != nil {
		return Lease{}, false, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `set local role nano_worker`); err != nil {
		return Lease{}, false, err
	}
	now := time.Now().UTC()
	if _, err := tx.Exec(ctx, `
		with exhausted as (
			update source_processing_jobs
			set status='failed', lease_token=null, lease_expires_at=null,
				last_error_code='retry_exhausted', updated_at=$1
			where status='running' and lease_expires_at <= $1 and attempt_no >= 3
			returning source_id
		)
		update source_sources s
		set state='failed', updated_at=$1
		from exhausted e
		where s.id=e.source_id
	`, now); err != nil {
		return Lease{}, false, err
	}
	if _, err := tx.Exec(ctx, `
		update source_processing_jobs
		set status='queued', lease_token=null, lease_expires_at=null, available_at=$1, updated_at=$1
		where status='running' and lease_expires_at <= $1 and attempt_no < 3
	`, now); err != nil {
		return Lease{}, false, err
	}

	var jobID string
	err = tx.QueryRow(ctx, `
		select id
		from source_processing_jobs
		where status='queued' and available_at <= $1 and attempt_no < 3
		order by available_at, created_at, id
		for update skip locked
		limit 1
	`, now).Scan(&jobID)
	if errors.Is(err, pgx.ErrNoRows) {
		if err := tx.Commit(ctx); err != nil {
			return Lease{}, false, err
		}
		return Lease{}, false, nil
	}
	if err != nil {
		return Lease{}, false, err
	}
	token := uuid.NewString()
	expiresAt := now.Add(q.leaseDuration)
	var lease Lease
	err = tx.QueryRow(ctx, `
		update source_processing_jobs
		set status='running', attempt_no=attempt_no+1, lease_token=$2::uuid,
			lease_expires_at=$3, updated_at=$1
		where id=$4
		returning id, source_id, notebook_id, attempt_no, lease_token::text, lease_expires_at
	`, now, token, expiresAt, jobID).Scan(
		&lease.ID, &lease.SourceID, &lease.NotebookID, &lease.AttemptNo,
		&lease.LeaseToken, &lease.LeaseExpiresAt,
	)
	if err != nil {
		return Lease{}, false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Lease{}, false, err
	}
	return lease, true, nil
}

func (q *Queue) Advance(ctx context.Context, jobID, leaseToken string, expected, next source.State) error {
	if !validTransition(expected, next) {
		return ErrTransitionConflict
	}
	tx, err := q.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `set local role nano_worker`); err != nil {
		return err
	}
	var sourceID string
	var current source.State
	err = tx.QueryRow(ctx, `
		select s.id, s.state
		from source_processing_jobs j
		join source_sources s on s.id=j.source_id
		where j.id=$1 and j.status='running' and j.lease_token=$2::uuid and j.lease_expires_at > now()
		for update of j, s
	`, jobID, leaseToken).Scan(&sourceID, &current)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrLeaseLost
	}
	if err != nil {
		return err
	}
	if current != expected {
		return ErrTransitionConflict
	}
	if _, err := tx.Exec(ctx, `update source_sources set state=$2, updated_at=now() where id=$1`, sourceID, next); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (q *Queue) Renew(ctx context.Context, jobID, leaseToken string) (time.Time, error) {
	if q == nil || q.pool == nil || q.leaseDuration <= 0 {
		return time.Time{}, errors.New("invalid Source processing Queue")
	}
	tx, err := q.pool.Begin(ctx)
	if err != nil {
		return time.Time{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `set local role nano_worker`); err != nil {
		return time.Time{}, err
	}
	expiresAt := time.Now().UTC().Add(q.leaseDuration)
	err = tx.QueryRow(ctx, `
		update source_processing_jobs
		set lease_expires_at=$3, updated_at=now()
		where id=$1 and status='running' and lease_token=$2::uuid and lease_expires_at > now()
		returning lease_expires_at
	`, jobID, leaseToken, expiresAt).Scan(&expiresAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return time.Time{}, ErrLeaseLost
	}
	if err != nil {
		return time.Time{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return time.Time{}, err
	}
	return expiresAt, nil
}

func (q *Queue) Complete(ctx context.Context, jobID, leaseToken string) error {
	return q.finish(ctx, jobID, leaseToken, true, "")
}

func (q *Queue) Fail(ctx context.Context, jobID, leaseToken, errorCode string) error {
	errorCode = strings.TrimSpace(errorCode)
	if !validErrorCode(errorCode) {
		return errors.New("invalid Source processing error code")
	}
	return q.finish(ctx, jobID, leaseToken, false, errorCode)
}

func (q *Queue) finish(ctx context.Context, jobID, leaseToken string, succeeded bool, errorCode string) error {
	if q == nil || q.pool == nil {
		return errors.New("invalid Source processing Queue")
	}
	tx, err := q.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `set local role nano_worker`); err != nil {
		return err
	}
	var sourceID string
	var current source.State
	err = tx.QueryRow(ctx, `
		select s.id, s.state
		from source_processing_jobs j
		join source_sources s on s.id=j.source_id
		where j.id=$1 and j.status='running' and j.lease_token=$2::uuid and j.lease_expires_at > now()
		for update of j, s
	`, jobID, leaseToken).Scan(&sourceID, &current)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrLeaseLost
	}
	if err != nil {
		return err
	}
	if succeeded && current != source.StateVerifying {
		return ErrTransitionConflict
	}
	nextSource := source.StateFailed
	nextJob := "failed"
	var persistedError any = errorCode
	if succeeded {
		nextSource = source.StateReady
		nextJob = "succeeded"
		persistedError = nil
	}
	if _, err := tx.Exec(ctx, `update source_sources set state=$2, updated_at=now() where id=$1`, sourceID, nextSource); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `
		update source_processing_jobs
		set status=$2, lease_token=null, lease_expires_at=null, last_error_code=$3, updated_at=now()
		where id=$1
	`, jobID, nextJob, persistedError); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func validErrorCode(value string) bool {
	if value == "" || len(value) > 80 {
		return false
	}
	for _, character := range value {
		if (character < 'a' || character > 'z') && (character < '0' || character > '9') && character != '_' {
			return false
		}
	}
	return true
}

func validTransition(expected, next source.State) bool {
	return (expected == source.StateUploaded && next == source.StateValidating) ||
		(expected == source.StateValidating && next == source.StateNormalizing) ||
		(expected == source.StateNormalizing && next == source.StateSegmenting) ||
		(expected == source.StateSegmenting && next == source.StateIndexing) ||
		(expected == source.StateIndexing && next == source.StateVerifying)
}
