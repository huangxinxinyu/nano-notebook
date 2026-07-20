package workload_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/huangxinxinyu/nano-notebook/internal/agent"
	"github.com/huangxinxinyu/nano-notebook/internal/jobs"
	"github.com/huangxinxinyu/nano-notebook/internal/sourcejobs"
	"github.com/huangxinxinyu/nano-notebook/internal/sourceprocessing"
	"github.com/huangxinxinyu/nano-notebook/internal/worker"
	"github.com/huangxinxinyu/nano-notebook/internal/workload"
)

func TestTenMixedInteractiveJobsCanRunWithoutSharingBackgroundCapacity(t *testing.T) {
	if err := workload.ValidateInteractiveCapacity(workload.DefaultAgentConcurrency, workload.DefaultSourceConcurrency); err != nil {
		t.Fatal(err)
	}
	if workload.DefaultAgentConcurrency+workload.DefaultSourceConcurrency != workload.TargetInteractiveConcurrency ||
		workload.DefaultBackgroundConcurrency >= workload.DefaultAgentConcurrency {
		t.Fatal("fixed Workload Class budgets do not reserve the target interactive capacity")
	}

	started := make(chan workload.Class, workload.TargetInteractiveConcurrency)
	release := make(chan struct{})
	agentJobs := make([]jobs.ClaimedJob, 0, workload.DefaultAgentConcurrency)
	for index := 0; index < workload.DefaultAgentConcurrency; index++ {
		agentJobs = append(agentJobs, jobs.ClaimedJob{ID: fmt.Sprintf("job_%d", index), RunID: fmt.Sprintf("run_%d", index), LeaseToken: fmt.Sprintf("lease_%d", index)})
	}
	sourceLeases := make([]sourcejobs.Lease, 0, workload.DefaultSourceConcurrency)
	for index := 0; index < workload.DefaultSourceConcurrency; index++ {
		sourceLeases = append(sourceLeases, sourcejobs.Lease{ID: fmt.Sprintf("source_job_%d", index), SourceID: fmt.Sprintf("source_%d", index), NotebookID: "nb", LeaseToken: fmt.Sprintf("token_%d", index)})
	}

	agentService := worker.NewServiceWithConcurrency(nil, &agentCapacityQueue{jobs: agentJobs}, capacityAgentExecutor{started: started, release: release}, time.Second, time.Minute, workload.DefaultAgentConcurrency)
	sourceService := sourceprocessing.NewServiceWithConcurrency(&sourceCapacityQueue{leases: sourceLeases}, capacitySourceProcessor{started: started, release: release}, time.Hour, time.Second, workload.DefaultSourceConcurrency)
	done := make(chan error, 2)
	go func() { _, err := agentService.ProcessAvailable(context.Background()); done <- err }()
	go func() { _, err := sourceService.ProcessAvailable(context.Background()); done <- err }()

	counts := map[workload.Class]int{}
	for index := 0; index < workload.TargetInteractiveConcurrency; index++ {
		select {
		case class := <-started:
			counts[class]++
		case <-time.After(time.Second):
			t.Fatalf("only %d/%d mixed Jobs started", index, workload.TargetInteractiveConcurrency)
		}
	}
	if counts[workload.AgentInteractive] != workload.DefaultAgentConcurrency || counts[workload.SourceProcessing] != workload.DefaultSourceConcurrency {
		t.Fatalf("started by Workload Class = %#v", counts)
	}
	close(release)
	for range 2 {
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
}

type agentCapacityQueue struct{ jobs []jobs.ClaimedJob }

func (q *agentCapacityQueue) ClaimNext(context.Context) (jobs.ClaimedJob, bool, error) {
	if len(q.jobs) == 0 {
		return jobs.ClaimedJob{}, false, nil
	}
	job := q.jobs[0]
	q.jobs = q.jobs[1:]
	return job, true, nil
}

func (*agentCapacityQueue) Heartbeat(context.Context, string, string, time.Duration) (bool, error) {
	return true, nil
}

func (*agentCapacityQueue) ReleaseLease(context.Context, string, string) (bool, error) {
	return true, nil
}

type capacityAgentExecutor struct {
	started chan<- workload.Class
	release <-chan struct{}
}

func (e capacityAgentExecutor) Execute(ctx context.Context, _ agent.Attempt) error {
	e.started <- workload.AgentInteractive
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-e.release:
		return nil
	}
}

type sourceCapacityQueue struct{ leases []sourcejobs.Lease }

func (q *sourceCapacityQueue) Claim(context.Context) (sourcejobs.Lease, bool, error) {
	if len(q.leases) == 0 {
		return sourcejobs.Lease{}, false, nil
	}
	lease := q.leases[0]
	q.leases = q.leases[1:]
	return lease, true, nil
}

func (*sourceCapacityQueue) Renew(context.Context, string, string) (time.Time, error) {
	return time.Now().Add(time.Minute), nil
}

type capacitySourceProcessor struct {
	started chan<- workload.Class
	release <-chan struct{}
}

func (p capacitySourceProcessor) ProcessLease(ctx context.Context, _ sourcejobs.Lease) error {
	p.started <- workload.SourceProcessing
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-p.release:
		return nil
	}
}
