package agentoutbox

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/huangxinxinyu/nano-notebook/internal/agentobs"
	"github.com/huangxinxinyu/nano-notebook/internal/collector"
	"github.com/huangxinxinyu/nano-notebook/internal/replay"
	"github.com/jackc/pgx/v5"
)

type ClaimedPurgeBatch struct {
	LeaseToken string
	Batch      collector.PurgeBatch
}

func (s *PostgresStore) ClaimPurgeBatch(ctx context.Context) (ClaimedPurgeBatch, bool, error) {
	if s == nil || s.pool == nil {
		return ClaimedPurgeBatch{}, false, errors.New("nil Outbox PostgreSQL Store")
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return ClaimedPurgeBatch{}, false, err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `set local role nano_worker`); err != nil {
		return ClaimedPurgeBatch{}, false, err
	}
	if _, err := tx.Exec(ctx, `
		update agentobs_outbox_commands
		set delivery_state = 'ready', lease_token = null, lease_expires_at = null, updated_at = now()
		where delivery_state = 'leased' and lease_expires_at <= now()
	`); err != nil {
		return ClaimedPurgeBatch{}, false, err
	}
	rows, err := tx.Query(ctx, `
		select command_id, command_version, command_kind, trace_id, run_id,
			requested_at, attempt_count
		from agentobs_outbox_commands
		where delivery_state = 'ready' and next_attempt_at <= now()
		order by next_attempt_at, created_at, command_id
		for update skip locked limit $1
	`, s.config.MaxTraces)
	if err != nil {
		return ClaimedPurgeBatch{}, false, err
	}
	type claimedCommand struct {
		command  collector.PurgeCommand
		attempts int
	}
	commands := make([]claimedCommand, 0, s.config.MaxTraces)
	for rows.Next() {
		var item claimedCommand
		if err := rows.Scan(
			&item.command.CommandID, &item.command.CommandVersion, &item.command.Kind,
			&item.command.TraceID, &item.command.RunID, &item.command.RequestedAt, &item.attempts,
		); err != nil {
			rows.Close()
			return ClaimedPurgeBatch{}, false, err
		}
		commands = append(commands, item)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return ClaimedPurgeBatch{}, false, err
	}
	rows.Close()
	if len(commands) == 0 {
		return ClaimedPurgeBatch{}, false, tx.Commit(ctx)
	}
	var now time.Time
	if err := tx.QueryRow(ctx, `select now()`).Scan(&now); err != nil {
		return ClaimedPurgeBatch{}, false, err
	}
	leaseToken := uuid.NewString()
	batch := collector.PurgeBatch{
		ProtocolVersion: collector.ProtocolVersion, BatchID: uuid.NewString(), ProducerID: s.config.ProducerID,
		CreatedAt: now, Commands: make([]collector.PurgeCommand, 0, len(commands)),
	}
	for _, item := range commands {
		tag, err := tx.Exec(ctx, `
			update agentobs_outbox_commands
			set delivery_state = 'leased', lease_token = $2, lease_expires_at = $3,
				attempt_count = attempt_count + 1, updated_at = now()
			where command_id = $1 and delivery_state = 'ready'
		`, item.command.CommandID, leaseToken, now.Add(s.config.LeaseDuration))
		if err != nil {
			return ClaimedPurgeBatch{}, false, err
		}
		if tag.RowsAffected() != 1 {
			return ClaimedPurgeBatch{}, false, errors.New("Outbox purge command lease changed during claim")
		}
		batch.Commands = append(batch.Commands, item.command)
	}
	if err := tx.Commit(ctx); err != nil {
		return ClaimedPurgeBatch{}, false, err
	}
	return ClaimedPurgeBatch{LeaseToken: leaseToken, Batch: batch}, true, nil
}

func (s *PostgresStore) ApplyPurgeResult(ctx context.Context, claimed ClaimedPurgeBatch, result collector.PurgeBatchResult) error {
	if claimed.LeaseToken == "" || claimed.Batch.BatchID == "" || result.BatchID != claimed.Batch.BatchID || len(result.Commands) != len(claimed.Batch.Commands) {
		return errors.New("Collector purge result does not match the claimed Batch")
	}
	byTrace := make(map[agentobs.TraceID]collector.PurgeCommandResult, len(result.Commands))
	for _, commandResult := range result.Commands {
		if _, duplicate := byTrace[commandResult.TraceID]; duplicate {
			return fmt.Errorf("Collector purge result repeats Trace %s", commandResult.TraceID)
		}
		switch commandResult.Status {
		case collector.PurgeAcknowledged:
			if commandResult.Code != "" {
				return errors.New("acknowledged Collector purge result contains an error code")
			}
		case collector.PurgeRejected:
			if commandResult.Code == "" {
				return errors.New("rejected Collector purge result omits an error code")
			}
		default:
			return errors.New("Collector purge result status is unsupported")
		}
		byTrace[commandResult.TraceID] = commandResult
	}
	for _, command := range claimed.Batch.Commands {
		commandResult, found := byTrace[command.TraceID]
		if !found {
			return fmt.Errorf("Collector purge result omits Trace %s", command.TraceID)
		}
		if commandResult.Status != collector.PurgeAcknowledged {
			continue
		}
		keys, err := s.purgeObjectKeys(ctx, command.CommandID)
		if err != nil {
			return err
		}
		traceKeys, err := s.traceStagingObjectKeys(ctx, command.TraceID)
		if err != nil {
			return err
		}
		keys = append(keys, traceKeys...)
		if len(keys) > 0 && s.stagingObjects == nil {
			return errors.New("Outbox Replay staging object Store is unavailable")
		}
		for _, key := range keys {
			if err := s.stagingObjects.Delete(ctx, key); err != nil {
				return fmt.Errorf("delete purged Replay staging object: %w", err)
			}
		}
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `set local role nano_worker`); err != nil {
		return err
	}
	for _, command := range claimed.Batch.Commands {
		commandResult := byTrace[command.TraceID]
		var attempts int
		if err := tx.QueryRow(ctx, `
			select attempt_count from agentobs_outbox_commands
			where command_id = $1 and delivery_state = 'leased' and lease_token = $2
			for update
		`, command.CommandID, claimed.LeaseToken).Scan(&attempts); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return fmt.Errorf("Outbox purge lease for Trace %s is no longer authoritative", command.TraceID)
			}
			return err
		}
		if commandResult.Status == collector.PurgeAcknowledged {
			if _, err := tx.Exec(ctx, `delete from agentobs_outbox_command_objects where command_id = $1`, command.CommandID); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `
				update agentobs_outbox_commands
				set delivery_state = 'acknowledged', lease_token = null, lease_expires_at = null,
					last_error_code = null, updated_at = now()
				where command_id = $1
			`, command.CommandID); err != nil {
				return err
			}
		} else {
			if _, err := tx.Exec(ctx, `
				update agentobs_outbox_commands
				set delivery_state = 'quarantined', lease_token = null, lease_expires_at = null,
					last_error_code = $2, updated_at = now()
				where command_id = $1
			`, command.CommandID, commandResult.Code); err != nil {
				return err
			}
		}
	}
	return tx.Commit(ctx)
}

func (s *PostgresStore) traceStagingObjectKeys(ctx context.Context, traceID agentobs.TraceID) ([]string, error) {
	if s.stagingObjects == nil {
		return nil, nil
	}
	prefix := replay.StagingTracePrefix(s.config.StagingObjectPrefix, traceID) + "/"
	var keys []string
	after := ""
	for {
		objects, err := s.stagingObjects.List(ctx, prefix, after, 256)
		if err != nil {
			return nil, fmt.Errorf("list purged Replay staging objects: %w", err)
		}
		for _, object := range objects {
			keys = append(keys, object.Key)
			after = object.Key
		}
		if len(objects) < 256 {
			return keys, nil
		}
	}
}

func (s *PostgresStore) ReleasePurgeBatch(ctx context.Context, claimed ClaimedPurgeBatch, code string) error {
	if claimed.LeaseToken == "" || code == "" {
		return errors.New("Outbox purge release identity is incomplete")
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `set local role nano_worker`); err != nil {
		return err
	}
	for _, command := range claimed.Batch.Commands {
		var attempts int
		if err := tx.QueryRow(ctx, `
			select attempt_count from agentobs_outbox_commands
			where command_id = $1 and delivery_state = 'leased' and lease_token = $2
			for update
		`, command.CommandID, claimed.LeaseToken).Scan(&attempts); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `
			update agentobs_outbox_commands
			set delivery_state = 'ready', lease_token = null, lease_expires_at = null,
				next_attempt_at = now() + $2::interval, last_error_code = $3, updated_at = now()
			where command_id = $1
		`, command.CommandID, s.retryDelay(attempts), code); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

func (s *PostgresStore) purgeObjectKeys(ctx context.Context, commandID string) ([]string, error) {
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `set local role nano_worker`); err != nil {
		return nil, err
	}
	rows, err := tx.Query(ctx, `select object_key from agentobs_outbox_command_objects where command_id = $1 order by object_key`, commandID)
	if err != nil {
		return nil, err
	}
	var keys []string
	for rows.Next() {
		var key string
		if err := rows.Scan(&key); err != nil {
			rows.Close()
			return nil, err
		}
		keys = append(keys, key)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return keys, nil
}
