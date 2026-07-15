package realtime

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

type RunListener struct {
	pool      *pgxpool.Pool
	onWake    func(string)
	ready     chan struct{}
	readyOnce sync.Once
}

func NewRunListener(pool *pgxpool.Pool, onWake func(string)) *RunListener {
	return &RunListener{pool: pool, onWake: onWake, ready: make(chan struct{})}
}

func (l *RunListener) Ready() <-chan struct{} {
	return l.ready
}

func (l *RunListener) Run(ctx context.Context) error {
	for ctx.Err() == nil {
		if err := l.listen(ctx); err != nil && ctx.Err() == nil {
			slog.Warn("Run projection listener disconnected", "error", err)
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

func (l *RunListener) listen(ctx context.Context) error {
	connection, err := l.pool.Acquire(ctx)
	if err != nil {
		return err
	}
	defer connection.Release()
	if _, err := connection.Exec(ctx, `listen nano_agent_runs`); err != nil {
		return err
	}
	l.readyOnce.Do(func() { close(l.ready) })
	if l.onWake != nil {
		l.onWake("")
	}
	for ctx.Err() == nil {
		notification, err := connection.Conn().WaitForNotification(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		if l.onWake != nil {
			l.onWake(notification.Payload)
		}
	}
	return nil
}
