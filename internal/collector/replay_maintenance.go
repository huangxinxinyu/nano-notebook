package collector

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/huangxinxinyu/nano-notebook/internal/agentobs"
	"github.com/huangxinxinyu/nano-notebook/internal/objectstore"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type ReplayMaintenanceConfig struct {
	BatchSize   int
	Interval    time.Duration
	Lease       time.Duration
	OrphanGrace time.Duration
	Now         func() time.Time
	ReportError func(error)
}

type ReplayMaintenanceResult struct {
	Expired        int
	PurgesAdvanced int
	OrphansDeleted int
}

type ReplayMaintenance struct {
	pool        *pgxpool.Pool
	objects     objectstore.Store
	config      ReplayMaintenanceConfig
	orphanAfter string
}

func NewReplayMaintenance(pool *pgxpool.Pool, objects objectstore.Store, config ReplayMaintenanceConfig) (*ReplayMaintenance, error) {
	if pool == nil || objects == nil {
		return nil, errors.New("Collector Replay maintenance dependencies are incomplete")
	}
	if config.BatchSize == 0 {
		config.BatchSize = 100
	}
	if config.Interval == 0 {
		config.Interval = time.Minute
	}
	if config.Lease == 0 {
		config.Lease = 30 * time.Second
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
	if config.BatchSize < 1 || config.Interval <= 0 || config.Lease <= 0 || config.OrphanGrace < 0 {
		return nil, errors.New("Collector Replay maintenance bounds are invalid")
	}
	return &ReplayMaintenance{pool: pool, objects: objects, config: config}, nil
}

func (m *ReplayMaintenance) Run(ctx context.Context) error {
	if m == nil {
		return errors.New("nil Collector Replay maintenance")
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

func (m *ReplayMaintenance) RunOnce(ctx context.Context) (ReplayMaintenanceResult, error) {
	if m == nil || m.pool == nil || m.objects == nil {
		return ReplayMaintenanceResult{}, errors.New("nil Collector Replay maintenance")
	}
	var result ReplayMaintenanceResult
	expired, err := m.expire(ctx)
	result.Expired = expired
	if err != nil {
		return result, err
	}
	advanced, err := m.advancePurge(ctx)
	if advanced {
		result.PurgesAdvanced = 1
	}
	if err != nil {
		return result, err
	}
	orphans, err := m.sweepOrphans(ctx)
	result.OrphansDeleted = orphans
	return result, err
}

func (m *ReplayMaintenance) expire(ctx context.Context) (int, error) {
	rows, err := m.pool.Query(ctx, `
		select attachment_id::text, object_key
		from obs_payload_refs
		where state = 'available' and expires_at <= now()
		order by expires_at, attachment_id
		limit $1
	`, m.config.BatchSize)
	if err != nil {
		return 0, err
	}
	type candidate struct{ id, key string }
	candidates := make([]candidate, 0, m.config.BatchSize)
	for rows.Next() {
		var item candidate
		if err := rows.Scan(&item.id, &item.key); err != nil {
			rows.Close()
			return 0, err
		}
		candidates = append(candidates, item)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return 0, err
	}
	rows.Close()
	expired := 0
	for _, item := range candidates {
		if err := m.objects.Delete(ctx, item.key); err != nil {
			return expired, fmt.Errorf("delete expired Replay object: %w", err)
		}
		tag, err := m.pool.Exec(ctx, `
			update obs_payload_refs set state = 'expired', updated_at = now()
			where attachment_id = $1 and state = 'available' and expires_at <= now()
		`, item.id)
		if err != nil {
			return expired, err
		}
		expired += int(tag.RowsAffected())
	}
	return expired, nil
}

type purgeLease struct {
	traceID agentobs.TraceID
	stage   string
	token   string
}

func (m *ReplayMaintenance) claimPurge(ctx context.Context) (purgeLease, bool, error) {
	tx, err := m.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return purgeLease{}, false, err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `
		update obs_purge_queue set lease_token = null, lease_expires_at = null, updated_at = now()
		where stage != 'content_removed' and lease_expires_at <= now()
	`); err != nil {
		return purgeLease{}, false, err
	}
	var lease purgeLease
	lease.token = uuid.NewString()
	err = tx.QueryRow(ctx, `
		select trace_id, stage from obs_purge_queue
		where stage != 'content_removed' and lease_token is null and available_at <= now()
		order by available_at, trace_id
		for update skip locked limit 1
	`).Scan(&lease.traceID, &lease.stage)
	if errors.Is(err, pgx.ErrNoRows) {
		return purgeLease{}, false, tx.Commit(ctx)
	}
	if err != nil {
		return purgeLease{}, false, err
	}
	if _, err := tx.Exec(ctx, `
		update obs_purge_queue
		set lease_token = $2, lease_expires_at = now() + $3::interval,
			attempt_count = attempt_count + 1, updated_at = now()
		where trace_id = $1
	`, lease.traceID, lease.token, m.config.Lease); err != nil {
		return purgeLease{}, false, err
	}
	return lease, true, tx.Commit(ctx)
}

func (m *ReplayMaintenance) advancePurge(ctx context.Context) (bool, error) {
	lease, found, err := m.claimPurge(ctx)
	if err != nil || !found {
		return false, err
	}
	var advanceErr error
	switch lease.stage {
	case "pending":
		advanceErr = m.removePurgedObjects(ctx, lease)
	case "objects_removed":
		advanceErr = m.removePurgedContent(ctx, lease)
	default:
		advanceErr = errors.New("Collector purge queue has an invalid stage")
	}
	if advanceErr != nil {
		_, _ = m.pool.Exec(ctx, `
			update obs_purge_queue
			set lease_token = null, lease_expires_at = null, available_at = now() + interval '1 second',
				last_error_code = 'replay_maintenance_failed', updated_at = now()
			where trace_id = $1 and lease_token = $2
		`, lease.traceID, lease.token)
		return false, advanceErr
	}
	return true, nil
}

func (m *ReplayMaintenance) removePurgedObjects(ctx context.Context, lease purgeLease) error {
	rows, err := m.pool.Query(ctx, `select object_key from obs_payload_refs where trace_id = $1 order by attachment_id`, lease.traceID)
	if err != nil {
		return err
	}
	var keys []string
	for rows.Next() {
		var key string
		if err := rows.Scan(&key); err != nil {
			rows.Close()
			return err
		}
		keys = append(keys, key)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	for _, key := range keys {
		if err := m.objects.Delete(ctx, key); err != nil {
			return fmt.Errorf("delete purged Replay object: %w", err)
		}
	}
	tx, err := m.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `update obs_payload_refs set state = 'purged', updated_at = now() where trace_id = $1`, lease.traceID); err != nil {
		return err
	}
	tag, err := tx.Exec(ctx, `
		update obs_purge_queue
		set stage = 'objects_removed', lease_token = null, lease_expires_at = null,
			available_at = now(), last_error_code = null, updated_at = now()
		where trace_id = $1 and stage = 'pending' and lease_token = $2
	`, lease.traceID, lease.token)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return errors.New("Collector purge lease is no longer authoritative")
	}
	return tx.Commit(ctx)
}

func (m *ReplayMaintenance) removePurgedContent(ctx context.Context, lease purgeLease) error {
	tx, err := m.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if err := lockTraceID(ctx, tx, lease.traceID); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `delete from obs_payload_refs where trace_id = $1`, lease.traceID); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `delete from obs_traces where trace_id = $1`, lease.traceID); err != nil {
		return err
	}
	tag, err := tx.Exec(ctx, `
		update obs_purge_queue
		set stage = 'content_removed', lease_token = null, lease_expires_at = null,
			last_error_code = null, updated_at = now()
		where trace_id = $1 and stage = 'objects_removed' and lease_token = $2
	`, lease.traceID, lease.token)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return errors.New("Collector purge lease is no longer authoritative")
	}
	return tx.Commit(ctx)
}

func (m *ReplayMaintenance) sweepOrphans(ctx context.Context) (int, error) {
	items, err := m.objects.List(ctx, "agent-replay/", m.orphanAfter, m.config.BatchSize)
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
	rows, err := m.pool.Query(ctx, `select object_key from obs_payload_refs where object_key = any($1::text[])`, keys)
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
