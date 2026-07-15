package jobs

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Queue struct {
	pool *pgxpool.Pool
}

type ClaimedJob struct {
	ID    string
	RunID string
}

func NewQueue(pool *pgxpool.Pool) *Queue {
	return &Queue{pool: pool}
}

func (q *Queue) ClaimNext(ctx context.Context) (ClaimedJob, bool, error) {
	tx, err := q.pool.Begin(ctx)
	if err != nil {
		return ClaimedJob{}, false, err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `set local role nano_worker`); err != nil {
		return ClaimedJob{}, false, err
	}
	var job ClaimedJob
	err = tx.QueryRow(ctx, `
		select id, run_id
		from agent_jobs
		where status = 'queued'
		order by created_at, id
		for update skip locked
		limit 1`).Scan(&job.ID, &job.RunID)
	if errors.Is(err, pgx.ErrNoRows) {
		return ClaimedJob{}, false, nil
	}
	if err != nil {
		return ClaimedJob{}, false, err
	}
	jobTag, err := tx.Exec(ctx, `
		update agent_jobs
		set status = 'running', started_at = now(), updated_at = now()
		where id = $1 and status = 'queued'`, job.ID)
	if err != nil {
		return ClaimedJob{}, false, err
	}
	runTag, err := tx.Exec(ctx, `
		update agent_runs
		set status = 'running', started_at = now(), updated_at = now()
		where id = $1 and status = 'queued'`, job.RunID)
	if err != nil {
		return ClaimedJob{}, false, err
	}
	if jobTag.RowsAffected() != 1 || runTag.RowsAffected() != 1 {
		return ClaimedJob{}, false, errors.New("queued Job and Run did not transition together")
	}
	if _, err := tx.Exec(ctx, `select pg_notify('nano_agent_runs', $1)`, job.RunID); err != nil {
		return ClaimedJob{}, false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return ClaimedJob{}, false, err
	}
	return job, true, nil
}
