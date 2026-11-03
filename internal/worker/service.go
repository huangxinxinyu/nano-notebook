package worker

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/huangxinxinyu/nano-notebook/internal/agent"
	"github.com/huangxinxinyu/nano-notebook/internal/jobs"
	"github.com/huangxinxinyu/nano-notebook/internal/platform/metrics"
	"github.com/jackc/pgx/v5/pgxpool"
)

type JobQueue interface {
	ClaimNext(context.Context) (jobs.ClaimedJob, bool, error)
	Heartbeat(context.Context, string, string, time.Duration) (bool, error)
	ReleaseLease(context.Context, string, string) (bool, error)
	ResolveAttempt(context.Context, jobs.ClaimedJob, agent.AttemptResolution) (agent.AttemptResolution, error)
}

type Executor interface {
	ExecuteAttempt(context.Context, agent.Attempt) agent.AttemptResolution
}

type Service struct {
	pool              *pgxpool.Pool
	queue             JobQueue
	executor          Executor
	scanInterval      time.Duration
	runTimeout        time.Duration
	heartbeatInterval time.Duration
	leaseDuration     time.Duration
	maxConcurrency    int
	metrics           *metrics.Catalog
}

// WithMetrics attaches the leak-detection gauges nano_worker_inflight_attempts
// and nano_worker_heartbeat_goroutines (PRD criterion 55) and returns the
// same Service, chainable onto either constructor above.
func (s *Service) WithMetrics(catalog *metrics.Catalog) *Service {
	s.metrics = catalog
	return s
}

func NewService(pool *pgxpool.Pool, queue JobQueue, executor Executor, scanInterval, runTimeout time.Duration) *Service {
	return NewServiceWithConcurrency(pool, queue, executor, scanInterval, runTimeout, 1)
}

func NewServiceWithConcurrency(pool *pgxpool.Pool, queue JobQueue, executor Executor, scanInterval, runTimeout time.Duration, maxConcurrency int) *Service {
	if scanInterval <= 0 {
		scanInterval = 5 * time.Second
	}
	if runTimeout <= 0 {
		runTimeout = 210 * time.Second
	}
	if maxConcurrency <= 0 {
		maxConcurrency = 1
	}
	return &Service{
		pool: pool, queue: queue, executor: executor,
		scanInterval: scanInterval, runTimeout: runTimeout,
		heartbeatInterval: 10 * time.Second, leaseDuration: jobs.DefaultLeaseDuration, maxConcurrency: maxConcurrency,
	}
}

func (s *Service) ProcessAvailable(ctx context.Context) (int, error) {
	type result struct {
		job jobs.ClaimedJob
		err error
	}
	processed := 0
	var processErr error
	results := make(chan result, s.maxConcurrency)
	inFlight := 0
	claiming := true
	for claiming || inFlight > 0 {
		if ctx.Err() != nil {
			claiming = false
		}
		for claiming && inFlight < s.maxConcurrency && ctx.Err() == nil {
			job, ok, err := s.queue.ClaimNext(ctx)
			if err != nil {
				processErr = errors.Join(processErr, err)
				claiming = false
				break
			}
			if !ok {
				claiming = false
				break
			}
			processed++
			inFlight++
			if s.metrics != nil {
				s.metrics.WorkerInflightAttempts.Inc()
			}
			go func() {
				results <- result{job: job, err: s.executeClaim(ctx, job)}
			}()
		}
		if inFlight == 0 {
			break
		}
		completed := <-results
		inFlight--
		if s.metrics != nil {
			s.metrics.WorkerInflightAttempts.Dec()
		}
		if completed.err != nil {
			processErr = errors.Join(processErr, completed.err)
			slog.Error("agent run execution failed", "run_id", completed.job.RunID, "job_id", completed.job.ID, "error", completed.err)
		}
	}
	return processed, processErr
}

func (s *Service) executeClaim(ctx context.Context, job jobs.ClaimedJob) error {
	timeoutCtx, timeoutCancel := context.WithTimeout(ctx, s.runTimeout)
	runCtx, cancelRun := context.WithCancelCause(timeoutCtx)
	stopHeartbeat := make(chan struct{})
	heartbeatDone := make(chan struct{})
	heartbeatFailure := make(chan error, 1)
	if s.metrics != nil {
		s.metrics.WorkerHeartbeatGoroutines.Inc()
	}
	go func() {
		defer close(heartbeatDone)
		if s.metrics != nil {
			defer s.metrics.WorkerHeartbeatGoroutines.Dec()
		}
		ticker := time.NewTicker(s.heartbeatInterval)
		defer ticker.Stop()
		for {
			select {
			case <-runCtx.Done():
				return
			case <-stopHeartbeat:
				return
			case <-ticker.C:
				ok, err := s.queue.Heartbeat(runCtx, job.ID, job.LeaseToken, s.leaseDuration)
				if err != nil {
					heartbeatFailure <- err
					cancelRun(err)
					return
				}
				if !ok {
					heartbeatFailure <- agent.ErrLeaseLost
					cancelRun(agent.ErrLeaseLost)
					return
				}
			}
		}
	}()

	attempt := agent.Attempt{JobID: job.ID, RunID: job.RunID, AttemptNo: job.AttemptNo, LeaseToken: job.LeaseToken}
	resolution := s.executor.ExecuteAttempt(runCtx, attempt)
	contextCause := context.Cause(runCtx)
	close(stopHeartbeat)
	<-heartbeatDone
	cancelRun(nil)
	timeoutCancel()

	var heartbeatErr error
	select {
	case heartbeatErr = <-heartbeatFailure:
	default:
	}
	if errors.Is(heartbeatErr, agent.ErrLeaseLost) {
		return nil
	}
	if heartbeatErr != nil || (errors.Is(contextCause, context.Canceled) && ctx.Err() != nil) {
		releaseCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		_, releaseErr := s.queue.ReleaseLease(releaseCtx, job.ID, job.LeaseToken)
		cancel()
		if releaseErr != nil {
			slog.Warn("agent Job lease release failed; natural expiry will recover it", "job_id", job.ID, "error", releaseErr)
		}
		return errors.Join(heartbeatErr, ctx.Err())
	}
	if !resolution.Valid() {
		resolution = agent.ClassifyAttempt(errors.New("Role Executor returned an invalid Attempt disposition"), contextCause)
	}
	if resolution.Disposition == agent.AttemptAbandoned {
		if resolution.ErrorCode == agent.AttemptCauseCancelled {
			releaseCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			_, releaseErr := s.queue.ReleaseLease(releaseCtx, job.ID, job.LeaseToken)
			cancel()
			if releaseErr != nil {
				return releaseErr
			}
		}
		return nil
	}
	if resolution.Disposition == agent.AttemptRetryable {
		// A provider's Retry-After is a minimum, so it only ever lengthens this.
		resolution.Backoff = max(resolution.Backoff, agent.AttemptRetryBackoff(job.AttemptNo, job.ID))
	}
	resolveCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	actual, resolveErr := s.queue.ResolveAttempt(resolveCtx, job, resolution)
	cancel()
	if resolveErr != nil {
		return resolveErr
	}
	if actual.Disposition == agent.AttemptRetryable {
		slog.Warn("agent run attempt scheduled for retry", "run_id", job.RunID, "job_id", job.ID, "attempt", job.AttemptNo, "error_code", actual.ErrorCode, "backoff", actual.Backoff)
	}
	return nil
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
