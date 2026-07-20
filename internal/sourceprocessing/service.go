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
}

func NewService(queue leaseQueue, processor leaseProcessor, heartbeatInterval, pollInterval time.Duration) *Service {
	return &Service{queue: queue, processor: processor, heartbeatInterval: heartbeatInterval, pollInterval: pollInterval}
}

func (s *Service) ProcessAvailable(ctx context.Context) (int, error) {
	if s == nil || s.queue == nil || s.processor == nil || s.heartbeatInterval <= 0 || s.pollInterval <= 0 {
		return 0, errors.New("invalid Source processing Service")
	}
	processed := 0
	var accumulated error
	for ctx.Err() == nil {
		lease, ok, err := s.queue.Claim(ctx)
		if err != nil {
			return processed, errors.Join(accumulated, err)
		}
		if !ok {
			return processed, accumulated
		}
		processed++
		if err := s.processLease(ctx, lease); err != nil {
			accumulated = errors.Join(accumulated, err)
		}
	}
	return processed, errors.Join(accumulated, ctx.Err())
}

func (s *Service) Run(ctx context.Context) error {
	if s == nil || s.queue == nil || s.processor == nil || s.heartbeatInterval <= 0 || s.pollInterval <= 0 {
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
