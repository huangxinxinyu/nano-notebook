package worker

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/huangxinxinyu/nano-notebook/internal/jobs"
)

func TestServiceDrainsEveryQueuedJobThroughTheExecutor(t *testing.T) {
	queue := &recordingQueue{jobs: []jobs.ClaimedJob{{ID: "job_one", RunID: "run_one"}, {ID: "job_two", RunID: "run_two"}}}
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

type recordingQueue struct {
	jobs []jobs.ClaimedJob
}

func (q *recordingQueue) ClaimNext(context.Context) (jobs.ClaimedJob, bool, error) {
	if len(q.jobs) == 0 {
		return jobs.ClaimedJob{}, false, nil
	}
	job := q.jobs[0]
	q.jobs = q.jobs[1:]
	return job, true, nil
}

type recordingExecutor struct {
	runIDs []string
}

func (e *recordingExecutor) Execute(_ context.Context, runID string) error {
	e.runIDs = append(e.runIDs, runID)
	return nil
}
