package sourceprocessing

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/huangxinxinyu/nano-notebook/internal/sourcejobs"
)

func TestServiceDrainsSourceLeasesAndRenewsLongWork(t *testing.T) {
	queue := &serviceQueue{leases: []sourcejobs.Lease{
		{ID: "job_1", SourceID: "src_1", NotebookID: "nb", LeaseToken: "token_1"},
		{ID: "job_2", SourceID: "src_2", NotebookID: "nb", LeaseToken: "token_2"},
	}}
	processor := &serviceProcessor{delay: 20 * time.Millisecond}
	service := NewService(queue, processor, 5*time.Millisecond, 10*time.Millisecond)
	processed, err := service.ProcessAvailable(context.Background())
	if err != nil || processed != 2 {
		t.Fatalf("ProcessAvailable processed=%d err=%v", processed, err)
	}
	if len(processor.processed) != 2 || queue.renewals == 0 {
		t.Fatalf("processed=%v renewals=%d", processor.processed, queue.renewals)
	}
}

func TestServiceRunsTheReservedSourceProcessingCapacityConcurrently(t *testing.T) {
	const capacity = 4
	leases := make([]sourcejobs.Lease, 0, capacity)
	for index := 0; index < capacity; index++ {
		leases = append(leases, sourcejobs.Lease{ID: fmt.Sprintf("job_%d", index), SourceID: fmt.Sprintf("src_%d", index), NotebookID: "nb", LeaseToken: fmt.Sprintf("token_%d", index)})
	}
	queue := &serviceQueue{leases: leases}
	processor := &concurrentSourceProcessor{started: make(chan struct{}, capacity), release: make(chan struct{})}
	service := NewServiceWithConcurrency(queue, processor, 5*time.Millisecond, 10*time.Millisecond, capacity)
	done := make(chan error, 1)
	go func() {
		processed, err := service.ProcessAvailable(context.Background())
		if processed != capacity {
			done <- fmt.Errorf("processed=%d, want %d", processed, capacity)
			return
		}
		done <- err
	}()
	for index := 0; index < capacity; index++ {
		select {
		case <-processor.started:
		case <-time.After(time.Second):
			t.Fatalf("only %d/%d Source jobs started concurrently", index, capacity)
		}
	}
	close(processor.release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestServiceStopsWorkWhenLeaseRenewalFails(t *testing.T) {
	queue := &serviceQueue{
		leases:   []sourcejobs.Lease{{ID: "job_lost", SourceID: "src", NotebookID: "nb", LeaseToken: "token"}},
		renewErr: sourcejobs.ErrLeaseLost,
	}
	processor := &serviceProcessor{waitForCancellation: true}
	service := NewService(queue, processor, time.Millisecond, 10*time.Millisecond)
	processed, err := service.ProcessAvailable(context.Background())
	if processed != 1 || !errors.Is(err, sourcejobs.ErrLeaseLost) {
		t.Fatalf("ProcessAvailable processed=%d err=%v", processed, err)
	}
}

type serviceQueue struct {
	mu       sync.Mutex
	leases   []sourcejobs.Lease
	renewals int
	renewErr error
}

func (q *serviceQueue) Claim(context.Context) (sourcejobs.Lease, bool, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if len(q.leases) == 0 {
		return sourcejobs.Lease{}, false, nil
	}
	lease := q.leases[0]
	q.leases = q.leases[1:]
	return lease, true, nil
}

func (q *serviceQueue) Renew(context.Context, string, string) (time.Time, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.renewals++
	return time.Now().Add(time.Minute), q.renewErr
}

type serviceProcessor struct {
	mu                  sync.Mutex
	processed           []string
	delay               time.Duration
	waitForCancellation bool
}

type concurrentSourceProcessor struct {
	started chan struct{}
	release chan struct{}
}

func (p *concurrentSourceProcessor) ProcessLease(ctx context.Context, _ sourcejobs.Lease) error {
	p.started <- struct{}{}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-p.release:
		return nil
	}
}

func (p *serviceProcessor) ProcessLease(ctx context.Context, lease sourcejobs.Lease) error {
	p.mu.Lock()
	p.processed = append(p.processed, lease.ID)
	p.mu.Unlock()
	if p.waitForCancellation {
		<-ctx.Done()
		return ctx.Err()
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(p.delay):
		return nil
	}
}
