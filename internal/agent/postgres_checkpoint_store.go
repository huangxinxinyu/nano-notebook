package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/huangxinxinyu/nano-notebook/internal/models"
	"github.com/jackc/pgx/v5"
)

var ErrRunDeadlineExceeded = errors.New("run_deadline_exceeded")

type checkpointReconcileState int

const (
	checkpointReconcileLost checkpointReconcileState = iota
	checkpointReconcileCurrent
	checkpointReconcileMatched
	checkpointReconcileDeadline
)

// AppendCheckpoint appends one accepted outcome under the current Attempt's
// Lease. Replaying the same identity and canonical payload is idempotent.
func (r *PostgresRuntime) AppendCheckpoint(ctx context.Context, attempt Attempt, pending PendingCheckpoint) (Checkpoint, error) {
	var appendErr error
	for appendTry := 0; appendTry < 2; appendTry++ {
		checkpoint, err := r.appendCheckpointOnce(ctx, attempt, pending)
		if err == nil {
			return checkpoint, nil
		}
		appendErr = err
		if errors.Is(err, ErrLeaseLost) || errors.Is(err, ErrRunDeadlineExceeded) || errors.Is(err, ErrCheckpointInvalid) || ctx.Err() != nil {
			return Checkpoint{}, err
		}

		checkpoint, state, reconcileErr := r.reconcileCheckpoint(ctx, attempt, pending)
		if reconcileErr != nil {
			return Checkpoint{}, errors.Join(appendErr, reconcileErr)
		}
		switch state {
		case checkpointReconcileMatched:
			return checkpoint, nil
		case checkpointReconcileLost:
			return Checkpoint{}, ErrLeaseLost
		case checkpointReconcileCurrent:
			continue
		case checkpointReconcileDeadline:
			return Checkpoint{}, ErrRunDeadlineExceeded
		}
	}
	return Checkpoint{}, appendErr
}

// LoadCheckpointPrefix loads and validates the authoritative accepted prefix
// for the current Attempt. Counters and the next incomplete node are derived
// from these immutable rows rather than process memory.
func (r *PostgresRuntime) LoadCheckpointPrefix(ctx context.Context, attempt Attempt) (CheckpointPrefix, error) {
	tx, err := r.workerTx(ctx)
	if err != nil {
		return CheckpointPrefix{}, err
	}
	defer tx.Rollback(ctx)
	if err := lockCheckpointAuthority(ctx, tx, attempt); err != nil {
		return CheckpointPrefix{}, err
	}
	checkpoints, err := loadRunCheckpoints(ctx, tx, attempt.RunID)
	if err != nil {
		return CheckpointPrefix{}, err
	}
	prefix, err := LoadCheckpointPrefix(ctx, checkpoints)
	if err != nil {
		return CheckpointPrefix{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return CheckpointPrefix{}, err
	}
	return prefix, nil
}

func (r *PostgresRuntime) CheckAuthority(ctx context.Context, attempt Attempt) error {
	tx, err := r.workerTx(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if err := lockCheckpointAuthority(ctx, tx, attempt); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (r *PostgresRuntime) appendCheckpointOnce(ctx context.Context, attempt Attempt, pending PendingCheckpoint) (Checkpoint, error) {
	tx, err := r.workerTx(ctx)
	if err != nil {
		return Checkpoint{}, err
	}
	defer tx.Rollback(ctx)
	if err := lockCheckpointAuthority(ctx, tx, attempt); err != nil {
		return Checkpoint{}, err
	}

	checkpoints, err := loadRunCheckpoints(ctx, tx, attempt.RunID)
	if err != nil {
		return Checkpoint{}, err
	}
	if _, err := LoadCheckpointPrefix(ctx, checkpoints); err != nil {
		return Checkpoint{}, err
	}
	for _, existing := range checkpoints {
		if existing.IdentityKey != pending.IdentityKey {
			continue
		}
		if !checkpointMatches(existing, pending) {
			return Checkpoint{}, invalidCheckpoint("identity %q has conflicting payload", pending.IdentityKey)
		}
		return existing, nil
	}
	checkpoint := Checkpoint{SequenceNo: len(checkpoints) + 1, PendingCheckpoint: pending}
	if _, err := LoadCheckpointPrefix(ctx, append(checkpoints, checkpoint)); err != nil {
		return Checkpoint{}, err
	}

	var actionIndex any
	if pending.ActionIndex != nil {
		actionIndex = *pending.ActionIndex
	}
	var actionID any
	if pending.ActionID != "" {
		actionID = pending.ActionID
	}
	err = tx.QueryRow(ctx, `
		insert into agent_run_checkpoints(
			run_id, sequence_no, identity_key, kind, decision_no,
			action_index, action_id, payload_version, payload, payload_sha256
		)
		values($1, $2, $3, $4, $5, $6, $7, $8, $9::jsonb, $10)
		returning created_at`,
		attempt.RunID, checkpoint.SequenceNo, pending.IdentityKey, string(pending.Kind), pending.DecisionNo,
		actionIndex, actionID, pending.PayloadVersion, []byte(pending.Payload), pending.PayloadSHA256,
	).Scan(&checkpoint.CreatedAt)
	if err != nil {
		return Checkpoint{}, err
	}
	if err := RecordCheckpointAcceptedInTx(ctx, tx, attempt, checkpoint); err != nil {
		return Checkpoint{}, err
	}
	if err := r.commit(ctx, tx); err != nil {
		return Checkpoint{}, err
	}
	return checkpoint, nil
}

func lockCheckpointAuthority(ctx context.Context, tx pgx.Tx, attempt Attempt) error {
	var runStatus, jobStatus string
	var leaseValid, deadlineValid, outputEmpty, authorized bool
	err := tx.QueryRow(ctx, `
		select r.status, j.status,
			coalesce(j.lease_expires_at > now(), false),
			r.deadline_at > now(),
			r.output_message_id is null,
			exists(
				select 1
				from chat_chats c
				join notebook_memberships m on m.notebook_id = c.notebook_id
				where c.id = r.chat_id and c.creator_user_id = r.user_id and m.user_id = r.user_id
			)
		from agent_runs r
		join agent_jobs j on j.run_id = r.id
		where r.id = $1 and j.id = $2 and j.lease_token = $3::uuid and j.attempt_no = $4
		for update of r, j`, attempt.RunID, attempt.JobID, attempt.LeaseToken, attempt.AttemptNo).
		Scan(&runStatus, &jobStatus, &leaseValid, &deadlineValid, &outputEmpty, &authorized)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrLeaseLost
	}
	if err != nil {
		return err
	}
	if runStatus != "running" || jobStatus != "running" || !outputEmpty || !leaseValid {
		return ErrLeaseLost
	}
	if !deadlineValid {
		return ErrRunDeadlineExceeded
	}
	if !authorized {
		return errors.New("Run is no longer authorized to checkpoint")
	}
	return nil
}

func (r *PostgresRuntime) reconcileCheckpoint(ctx context.Context, attempt Attempt, pending PendingCheckpoint) (Checkpoint, checkpointReconcileState, error) {
	tx, err := r.workerTx(ctx)
	if err != nil {
		return Checkpoint{}, checkpointReconcileLost, err
	}
	defer tx.Rollback(ctx)

	checkpoint, found, err := checkpointByIdentity(ctx, tx, attempt.RunID, pending.IdentityKey)
	if err != nil {
		return Checkpoint{}, checkpointReconcileLost, err
	}
	if found {
		if !checkpointMatches(checkpoint, pending) {
			return Checkpoint{}, checkpointReconcileLost, invalidCheckpoint("identity %q has conflicting payload", pending.IdentityKey)
		}
		if err := tx.Commit(ctx); err != nil {
			return Checkpoint{}, checkpointReconcileLost, err
		}
		return checkpoint, checkpointReconcileMatched, nil
	}

	var runStatus, jobStatus string
	var outputEmpty, deadlineValid, leaseValid, authorized bool
	err = tx.QueryRow(ctx, `
		select r.status, j.status, r.output_message_id is null,
			r.deadline_at > now(),
			coalesce(j.lease_expires_at > now(), false),
			exists(
				select 1
				from chat_chats c
				join notebook_memberships m on m.notebook_id = c.notebook_id
				where c.id = r.chat_id and c.creator_user_id = r.user_id and m.user_id = r.user_id
			)
		from agent_runs r
		join agent_jobs j on j.run_id = r.id
		where r.id = $1 and j.id = $2 and j.lease_token = $3::uuid and j.attempt_no = $4`,
		attempt.RunID, attempt.JobID, attempt.LeaseToken, attempt.AttemptNo).
		Scan(&runStatus, &jobStatus, &outputEmpty, &deadlineValid, &leaseValid, &authorized)
	if errors.Is(err, pgx.ErrNoRows) {
		return Checkpoint{}, checkpointReconcileLost, nil
	}
	if err != nil {
		return Checkpoint{}, checkpointReconcileLost, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Checkpoint{}, checkpointReconcileLost, err
	}
	active := runStatus == "running" && jobStatus == "running" && outputEmpty && authorized
	if active && !deadlineValid {
		return Checkpoint{}, checkpointReconcileDeadline, nil
	}
	if active && leaseValid {
		return Checkpoint{}, checkpointReconcileCurrent, nil
	}
	return Checkpoint{}, checkpointReconcileLost, nil
}

type checkpointScanner interface {
	Scan(dest ...any) error
}

func scanCheckpoint(row checkpointScanner) (Checkpoint, error) {
	var checkpoint Checkpoint
	var kind string
	var actionID *string
	var payload string
	err := row.Scan(
		&checkpoint.SequenceNo,
		&checkpoint.IdentityKey,
		&kind,
		&checkpoint.DecisionNo,
		&checkpoint.ActionIndex,
		&actionID,
		&checkpoint.PayloadVersion,
		&payload,
		&checkpoint.PayloadSHA256,
		&checkpoint.CreatedAt,
	)
	if err != nil {
		return Checkpoint{}, err
	}
	checkpoint.Kind = CheckpointKind(kind)
	if actionID != nil {
		checkpoint.ActionID = *actionID
	}
	checkpoint.Payload, err = canonicalStoredCheckpointPayload(checkpoint, []byte(payload))
	if err != nil {
		return Checkpoint{}, err
	}
	return checkpoint, nil
}

func canonicalStoredCheckpointPayload(checkpoint Checkpoint, raw []byte) (json.RawMessage, error) {
	var expected PendingCheckpoint
	var err error
	switch checkpoint.Kind {
	case CheckpointActionProposal:
		var payload proposalCheckpointPayload
		if err := decodeCheckpointPayload(raw, &payload); err != nil || len(payload.Actions) == 0 {
			return nil, invalidCheckpoint("stored proposal payload is invalid")
		}
		batch := models.ActionProposalBatch{Actions: make([]models.ActionProposal, 0, len(payload.Actions))}
		for index, action := range payload.Actions {
			if action.Index != index || action.ActionID != actionID(checkpoint.DecisionNo, index) {
				return nil, invalidCheckpoint("stored proposal Action ordinal is invalid")
			}
			batch.Actions = append(batch.Actions, models.ActionProposal{Name: action.Name, Input: action.Input})
		}
		expected, err = NewProposalCheckpoint(checkpoint.DecisionNo, batch)
	case CheckpointActionResult:
		if checkpoint.ActionIndex == nil {
			return nil, invalidCheckpoint("stored Action Result index is missing")
		}
		var payload actionResultCheckpointPayload
		if err := decodeCheckpointPayload(raw, &payload); err != nil {
			return nil, invalidCheckpoint("stored Action Result payload is invalid")
		}
		if payload.ActionID != checkpoint.ActionID {
			return nil, invalidCheckpoint("stored Action Result identity is invalid")
		}
		expected, err = NewActionResultCheckpoint(checkpoint.DecisionNo, *checkpoint.ActionIndex, checkpoint.ActionID, ActionResult{
			Status: payload.Status, Output: payload.Output, ErrorCode: payload.ErrorCode,
		})
	case CheckpointFinalDraft:
		var payload finalDraftCheckpointPayload
		if err := decodeCheckpointPayload(raw, &payload); err != nil {
			return nil, invalidCheckpoint("stored Final Draft payload is invalid")
		}
		expected, err = NewFinalDraftCheckpoint(checkpoint.DecisionNo, models.FinalDraft{Text: payload.Text})
	default:
		return nil, invalidCheckpoint("stored checkpoint kind %q is invalid", checkpoint.Kind)
	}
	if err != nil {
		return nil, invalidCheckpoint("stored checkpoint payload is invalid: %v", err)
	}
	return expected.Payload, nil
}

func decodeCheckpointPayload(raw []byte, destination any) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	decoder.UseNumber()
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errors.New("checkpoint payload has trailing JSON")
	}
	return nil
}

func actionID(decisionNo, actionIndex int) string {
	return fmt.Sprintf("decision:%d/action:%d", decisionNo, actionIndex)
}

const selectCheckpointColumns = `
	sequence_no, identity_key, kind, decision_no, action_index, action_id,
	payload_version, payload::text, payload_sha256, created_at`

func checkpointByIdentity(ctx context.Context, tx pgx.Tx, runID, identityKey string) (Checkpoint, bool, error) {
	checkpoint, err := scanCheckpoint(tx.QueryRow(ctx, `
		select `+selectCheckpointColumns+`
		from agent_run_checkpoints
		where run_id = $1 and identity_key = $2`, runID, identityKey))
	if errors.Is(err, pgx.ErrNoRows) {
		return Checkpoint{}, false, nil
	}
	if err != nil {
		return Checkpoint{}, false, err
	}
	return checkpoint, true, nil
}

func loadRunCheckpoints(ctx context.Context, tx pgx.Tx, runID string) ([]Checkpoint, error) {
	rows, err := tx.Query(ctx, `
		select `+selectCheckpointColumns+`
		from agent_run_checkpoints
		where run_id = $1
		order by sequence_no`, runID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	checkpoints := make([]Checkpoint, 0)
	for rows.Next() {
		checkpoint, err := scanCheckpoint(rows)
		if err != nil {
			return nil, err
		}
		checkpoints = append(checkpoints, checkpoint)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return checkpoints, nil
}
