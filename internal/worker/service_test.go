package worker

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/huangxinxinyu/nano-notebook/internal/agent"
	"github.com/huangxinxinyu/nano-notebook/internal/jobs"
)

func TestServiceDrainsEveryQueuedJobThroughTheExecutor(t *testing.T) {
	queue := &recordingQueue{jobs: []jobs.ClaimedJob{{ID: "job_one", RunID: "run_one", LeaseToken: "lease_one"}, {ID: "job_two", RunID: "run_two", LeaseToken: "lease_two"}}, heartbeatOK: true}
	executor := &recordingExecutor{}
	service := NewService(nil, queue, executor, 5*time.Second, 210*time.Second)

	processed, err := service.ProcessAvailable(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if processed != 2 || !reflect.DeepEqual(executor.runIDs, []string{"run_one", "run_two"}) {
		t.Fatalf("processed=%d runs=%v", processed, executor.runIDs)
	}
}

func TestHeartbeatLeaseLossCancelsTheInFlightExecution(t *testing.T) {
	queue := &recordingQueue{
		jobs:        []jobs.ClaimedJob{{ID: "job_one", RunID: "run_one", LeaseToken: "lease_one"}},
		heartbeatOK: false,
	}
	executor := &blockingExecutor{started: make(chan struct{})}
	service := NewService(nil, queue, executor, time.Second, time.Minute)
	service.heartbeatInterval = time.Millisecond

	processed, err := service.ProcessAvailable(context.Background())
	if processed != 1 || !errors.Is(err, context.Canceled) {
		t.Fatalf("processed=%d err=%v, want cancelled execution", processed, err)
	}
	if queue.heartbeats != 1 {
		t.Fatalf("heartbeats=%d, want one lease-loss heartbeat", queue.heartbeats)
	}
}

func TestShutdownReleasesTheCurrentLeaseForImmediateRecovery(t *testing.T) {
	queue := &recordingQueue{
		jobs:        []jobs.ClaimedJob{{ID: "job_one", RunID: "run_one", LeaseToken: "lease_one"}},
		heartbeatOK: true,
	}
	executor := &blockingExecutor{started: make(chan struct{})}
	service := NewService(nil, queue, executor, time.Second, time.Minute)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := service.ProcessAvailable(ctx)
		done <- err
	}()
	<-executor.started
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("shutdown error = %v, want context cancellation", err)
	}
	if !reflect.DeepEqual(queue.released, []string{"job_one:lease_one"}) {
		t.Fatalf("released leases = %v", queue.released)
	}
}

type recordingQueue struct {
	mu          sync.Mutex
	jobs        []jobs.ClaimedJob
	heartbeatOK bool
	heartbeats  int
	released    []string
}

func (q *recordingQueue) ClaimNext(context.Context) (jobs.ClaimedJob, bool, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if len(q.jobs) == 0 {
		return jobs.ClaimedJob{}, false, nil
	}
	job := q.jobs[0]
	q.jobs = q.jobs[1:]
	return job, true, nil
}

func (q *recordingQueue) Heartbeat(context.Context, string, string, time.Duration) (bool, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.heartbeats++
	return q.heartbeatOK, nil
}

func (q *recordingQueue) ReleaseLease(_ context.Context, jobID, leaseToken string) (bool, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.released = append(q.released, jobID+":"+leaseToken)
	return true, nil
}

type recordingExecutor struct {
	runIDs []string
}

func (e *recordingExecutor) Execute(_ context.Context, attempt agent.Attempt) error {
	e.runIDs = append(e.runIDs, attempt.RunID)
	return nil
}

type blockingExecutor struct {
	started chan struct{}
}

func (e *blockingExecutor) Execute(ctx context.Context, _ agent.Attempt) error {
	close(e.started)
	<-ctx.Done()
	return ctx.Err()
}
