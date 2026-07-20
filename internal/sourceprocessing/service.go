package sourceprocessing

import (
	"context"
	"errors"
	"time"

	"github.com/huangxinxinyu/nano-notebook/internal/sourcejobs"
)

type leaseQueue interface {
	Claim(context.Context) (sourcejobs.Lease, bool, error)
	Renew(context.Context, string, string) (time.Time, error)
}

type leaseProcessor interface {
	ProcessLease(context.Context, sourcejobs.Lease) error
}

type Service struct {
	queue             leaseQueue
	processor         leaseProcessor
	heartbeatInterval time.Duration
	pollInterval      time.Duration
	maxConcurrency    int
}

func NewService(queue leaseQueue, processor leaseProcessor, heartbeatInterval, pollInterval time.Duration) *Service {
	return NewServiceWithConcurrency(queue, processor, heartbeatInterval, pollInterval, 1)
}

func NewServiceWithConcurrency(queue leaseQueue, processor leaseProcessor, heartbeatInterval, pollInterval time.Duration, maxConcurrency int) *Service {
	return &Service{queue: queue, processor: processor, heartbeatInterval: heartbeatInterval, pollInterval: pollInterval, maxConcurrency: maxConcurrency}
}

func (s *Service) ProcessAvailable(ctx context.Context) (int, error) {
	if s == nil || s.queue == nil || s.processor == nil || s.heartbeatInterval <= 0 || s.pollInterval <= 0 || s.maxConcurrency <= 0 {
		return 0, errors.New("invalid Source processing Service")
	}
	type result struct{ err error }
	processed := 0
	var accumulated error
	results := make(chan result, s.maxConcurrency)
	inFlight := 0
	claiming := true
	for (claiming || inFlight > 0) && ctx.Err() == nil {
		for claiming && inFlight < s.maxConcurrency {
			lease, ok, err := s.queue.Claim(ctx)
			if err != nil {
				accumulated = errors.Join(accumulated, err)
				claiming = false
				break
			}
			if !ok {
				claiming = false
				break
			}
			processed++
			inFlight++
			go func() {
				results <- result{err: s.processLease(ctx, lease)}
			}()
		}
		if inFlight == 0 {
			break
		}
		completed := <-results
		inFlight--
		if completed.err != nil {
			accumulated = errors.Join(accumulated, completed.err)
		}
	}
	if ctx.Err() != nil {
		for inFlight > 0 {
			completed := <-results
			inFlight--
			accumulated = errors.Join(accumulated, completed.err)
		}
	}
	return processed, errors.Join(accumulated, ctx.Err())
}

func (s *Service) Run(ctx context.Context) error {
	if s == nil || s.queue == nil || s.processor == nil || s.heartbeatInterval <= 0 || s.pollInterval <= 0 || s.maxConcurrency <= 0 {
		return errors.New("invalid Source processing Service")
	}
	for ctx.Err() == nil {
		_, _ = s.ProcessAvailable(ctx)
		timer := time.NewTimer(s.pollInterval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil
		case <-timer.C:
		}
	}
	return nil
}

func (s *Service) processLease(ctx context.Context, lease sourcejobs.Lease) error {
	leaseCtx, cancel := context.WithCancelCause(ctx)
	stopHeartbeat := make(chan struct{})
	heartbeatDone := make(chan error, 1)
	go func() {
		ticker := time.NewTicker(s.heartbeatInterval)
		defer ticker.Stop()
		for {
			select {
			case <-leaseCtx.Done():
				heartbeatDone <- nil
				return
			case <-stopHeartbeat:
				heartbeatDone <- nil
				return
			case <-ticker.C:
				if _, err := s.queue.Renew(leaseCtx, lease.ID, lease.LeaseToken); err != nil {
					cancel(err)
					heartbeatDone <- err
					return
				}
			}
		}
	}()
	processErr := s.processor.ProcessLease(leaseCtx, lease)
	close(stopHeartbeat)
	heartbeatErr := <-heartbeatDone
	cancel(nil)
	if heartbeatErr != nil {
		return errors.Join(processErr, heartbeatErr)
	}
	return processErr
}
