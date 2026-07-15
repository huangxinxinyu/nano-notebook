package jobs

import (
	"context"

	"github.com/jackc/pgx/v5/pgconn"
)

type DBTX interface {
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
}

type Store struct {
	db DBTX
}

func NewStore(db DBTX) *Store {
	return &Store{db: db}
}

func (s *Store) CreateAgentRun(ctx context.Context, jobID, runID string) error {
	_, err := s.db.Exec(ctx, `
		insert into agent_jobs(id, kind, run_id, status)
		values($1, 'agent_run', $2, 'queued')`, jobID, runID)
	return err
}
