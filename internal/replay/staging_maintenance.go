package replay

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/huangxinxinyu/nano-notebook/internal/objectstore"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type StagingMaintenanceConfig struct {
	ObjectPrefix string
	BatchSize    int
	Interval     time.Duration
	OrphanGrace  time.Duration
	Now          func() time.Time
	ReportError  func(error)
}

type StagingMaintenanceResult struct {
	ExpiredDeleted int
	OrphansDeleted int
}

type StagingMaintenance struct {
	pool        *pgxpool.Pool
	objects     objectstore.Store
	config      StagingMaintenanceConfig
	orphanAfter string
}

func NewStagingMaintenance(pool *pgxpool.Pool, objects objectstore.Store, config StagingMaintenanceConfig) (*StagingMaintenance, error) {
	if pool == nil || objects == nil {
		return nil, errors.New("Replay staging maintenance dependencies are incomplete")
	}
	config.ObjectPrefix = strings.Trim(strings.TrimSpace(config.ObjectPrefix), "/")
	if config.ObjectPrefix == "" {
		config.ObjectPrefix = "agent-replay-staging"
	}
	if config.BatchSize == 0 {
		config.BatchSize = 100
	}
	if config.Interval == 0 {
		config.Interval = time.Minute
	}
	if config.OrphanGrace == 0 {
		config.OrphanGrace = time.Hour
	}
	if config.Now == nil {
		config.Now = func() time.Time { return time.Now().UTC() }
	}
	if config.ReportError == nil {
		config.ReportError = func(error) {}
	}
	if config.BatchSize < 1 || config.Interval <= 0 || config.OrphanGrace < 0 || len(config.ObjectPrefix) > 200 {
		return nil, errors.New("Replay staging maintenance bounds are invalid")
	}
	return &StagingMaintenance{pool: pool, objects: objects, config: config}, nil
}

func (m *StagingMaintenance) Run(ctx context.Context) error {
	if m == nil {
		return errors.New("nil Replay staging maintenance")
	}
	timer := time.NewTimer(0)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-timer.C:
			if _, err := m.RunOnce(ctx); err != nil && ctx.Err() == nil {
				m.config.ReportError(err)
			}
			timer.Reset(m.config.Interval)
		}
	}
}

func (m *StagingMaintenance) RunOnce(ctx context.Context) (StagingMaintenanceResult, error) {
	if m == nil || m.pool == nil || m.objects == nil {
		return StagingMaintenanceResult{}, errors.New("nil Replay staging maintenance")
	}
	expired, err := m.deleteExpired(ctx)
	result := StagingMaintenanceResult{ExpiredDeleted: expired}
	if err != nil {
		return result, err
	}
	orphans, err := m.sweepOrphans(ctx)
	result.OrphansDeleted = orphans
	return result, err
}

type stagingCandidate struct {
	id  string
	key string
}

func (m *StagingMaintenance) deleteExpired(ctx context.Context) (int, error) {
	tx, err := m.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `set local role nano_worker`); err != nil {
		return 0, err
	}
	rows, err := tx.Query(ctx, `
		select attachment_id::text, object_key
		from agentobs_replay_staging
		where state = 'staged' and expires_at <= now()
		order by expires_at, attachment_id limit $1
	`, m.config.BatchSize)
	if err != nil {
		return 0, err
	}
	items := make([]stagingCandidate, 0, m.config.BatchSize)
	for rows.Next() {
		var item stagingCandidate
		if err := rows.Scan(&item.id, &item.key); err != nil {
			rows.Close()
			return 0, err
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return 0, err
	}
	rows.Close()
	if err := tx.Commit(ctx); err != nil {
		return 0, err
	}
	deleted := 0
	for _, item := range items {
		if err := m.objects.Delete(ctx, item.key); err != nil {
			return deleted, fmt.Errorf("delete expired Replay staging object: %w", err)
		}
		removeTx, err := m.pool.BeginTx(ctx, pgx.TxOptions{})
		if err != nil {
			return deleted, err
		}
		if _, err := removeTx.Exec(ctx, `set local role nano_worker`); err != nil {
			_ = removeTx.Rollback(ctx)
			return deleted, err
		}
		tag, err := removeTx.Exec(ctx, `
			delete from agentobs_replay_staging
			where attachment_id = $1 and state = 'staged' and expires_at <= now()
		`, item.id)
		if err != nil {
			_ = removeTx.Rollback(ctx)
			return deleted, err
		}
		if err := removeTx.Commit(ctx); err != nil {
			return deleted, err
		}
		deleted += int(tag.RowsAffected())
	}
	return deleted, nil
}

func (m *StagingMaintenance) sweepOrphans(ctx context.Context) (int, error) {
	items, err := m.objects.List(ctx, m.config.ObjectPrefix+"/", m.orphanAfter, m.config.BatchSize)
	if err != nil {
		return 0, err
	}
	if len(items) == 0 {
		m.orphanAfter = ""
		return 0, nil
	}
	m.orphanAfter = items[len(items)-1].Key
	if len(items) < m.config.BatchSize {
		defer func() { m.orphanAfter = "" }()
	}
	keys := make([]string, 0, len(items))
	for _, item := range items {
		keys = append(keys, item.Key)
	}
	tx, err := m.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `set local role nano_worker`); err != nil {
		return 0, err
	}
	rows, err := tx.Query(ctx, `select object_key from agentobs_replay_staging where object_key = any($1::text[])`, keys)
	if err != nil {
		return 0, err
	}
	referenced := make(map[string]struct{}, len(items))
	for rows.Next() {
		var key string
		if err := rows.Scan(&key); err != nil {
			rows.Close()
			return 0, err
		}
		referenced[key] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return 0, err
	}
	rows.Close()
	if err := tx.Commit(ctx); err != nil {
		return 0, err
	}
	cutoff := m.config.Now().Add(-m.config.OrphanGrace)
	deleted := 0
	for _, item := range items {
		if _, found := referenced[item.Key]; found || item.ModifiedAt.After(cutoff) {
			continue
		}
		if err := m.objects.Delete(ctx, item.Key); err != nil {
			return deleted, err
		}
		deleted++
	}
	return deleted, nil
}
