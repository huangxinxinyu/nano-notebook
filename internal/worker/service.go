package worker

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/huangxinxinyu/nano-notebook/internal/jobs"
	"github.com/jackc/pgx/v5/pgxpool"
)

type JobQueue interface {
	ClaimNext(context.Context) (jobs.ClaimedJob, bool, error)
}

type Executor interface {
	Execute(context.Context, string) error
}

type Service struct {
	pool         *pgxpool.Pool
	queue        JobQueue
	executor     Executor
	scanInterval time.Duration
	runTimeout   time.Duration
}

func NewService(pool *pgxpool.Pool, queue JobQueue, executor Executor, scanInterval, runTimeout time.Duration) *Service {
	if scanInterval <= 0 {
		scanInterval = 5 * time.Second
	}
	if runTimeout <= 0 {
		runTimeout = 210 * time.Second
	}
	return &Service{pool: pool, queue: queue, executor: executor, scanInterval: scanInterval, runTimeout: runTimeout}
}

func (s *Service) ProcessAvailable(ctx context.Context) (int, error) {
	processed := 0
	var processErr error
	for {
		job, ok, err := s.queue.ClaimNext(ctx)
		if err != nil {
			return processed, errors.Join(processErr, err)
		}
		if !ok {
			return processed, processErr
		}
		processed++
		runCtx, cancel := context.WithTimeout(ctx, s.runTimeout)
		err = s.executor.Execute(runCtx, job.RunID)
		cancel()
		if err != nil {
			processErr = errors.Join(processErr, err)
			slog.Error("agent run execution failed", "run_id", job.RunID, "job_id", job.ID, "error", err)
		}
	}
}

func (s *Service) Run(ctx context.Context) error {
	if s.pool == nil {
		return errors.New("Worker notification listener requires PostgreSQL")
	}
	for ctx.Err() == nil {
		if err := s.listen(ctx); err != nil && ctx.Err() == nil {
			slog.Warn("agent Job listener disconnected", "error", err)
			timer := time.NewTimer(time.Second)
			select {
			case <-ctx.Done():
				timer.Stop()
			case <-timer.C:
			}
		}
	}
	return nil
}

func (s *Service) listen(ctx context.Context) error {
	connection, err := s.pool.Acquire(ctx)
	if err != nil {
		return err
	}
	defer connection.Release()
	if _, err := connection.Exec(ctx, `listen nano_agent_jobs`); err != nil {
		return err
	}
	for ctx.Err() == nil {
		if processed, err := s.ProcessAvailable(ctx); err != nil {
			slog.Warn("agent Job drain completed with failures", "processed", processed, "error", err)
		}
		waitCtx, cancel := context.WithTimeout(ctx, s.scanInterval)
		_, err := connection.Conn().WaitForNotification(waitCtx)
		cancel()
		if ctx.Err() != nil {
			return nil
		}
		if errors.Is(err, context.DeadlineExceeded) {
			continue
		}
		if err != nil {
			return err
		}
	}
	return nil
}
