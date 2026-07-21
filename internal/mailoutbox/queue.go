package mailoutbox

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var ErrLeaseLost = errors.New("mail Outbox lease lost")

type Delivery struct {
	ID             string
	Kind           string
	InvitationID   string
	RecipientEmail string
	Locale         string
	Token          string
	NotebookTitle  string
	AttemptNo      int
	LeaseToken     string
	LeaseExpiresAt time.Time
}

type Queue struct {
	pool          *pgxpool.Pool
	leaseDuration time.Duration
}

func NewQueue(pool *pgxpool.Pool, leaseDuration time.Duration) *Queue {
	return &Queue{pool: pool, leaseDuration: leaseDuration}
}

func (q *Queue) Claim(ctx context.Context) (Delivery, bool, error) {
	if q == nil || q.pool == nil || q.leaseDuration <= 0 {
		return Delivery{}, false, errors.New("invalid mail Outbox Queue")
	}
	tx, err := q.pool.Begin(ctx)
	if err != nil {
		return Delivery{}, false, err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `set local role nano_worker`); err != nil {
		return Delivery{}, false, err
	}
	var now time.Time
	if err := tx.QueryRow(ctx, `select clock_timestamp()`).Scan(&now); err != nil {
		return Delivery{}, false, err
	}
	if _, err := tx.Exec(ctx, `
		update platform_mail_outbox
		set state=case when attempt_no >= 10 then 'failed' else 'pending' end,
			lease_token=null, lease_expires_at=null, available_at=$1, updated_at=$1,
			last_error_code=case when attempt_no >= 10 then 'retry_exhausted' else last_error_code end
		where state='leased' and lease_expires_at <= $1`, now); err != nil {
		return Delivery{}, false, err
	}
	var delivery Delivery
	var payloadBytes []byte
	err = tx.QueryRow(ctx, `
		select id, kind, coalesce(invitation_id,''), recipient_email, locale, payload, attempt_no
		from platform_mail_outbox
		where state='pending' and available_at <= $1 and attempt_no < 10
		order by available_at, created_at, id
		for update skip locked
		limit 1`, now).Scan(&delivery.ID, &delivery.Kind, &delivery.InvitationID, &delivery.RecipientEmail, &delivery.Locale, &payloadBytes, &delivery.AttemptNo)
	if errors.Is(err, pgx.ErrNoRows) {
		if err := tx.Commit(ctx); err != nil {
			return Delivery{}, false, err
		}
		return Delivery{}, false, nil
	}
	if err != nil {
		return Delivery{}, false, err
	}
	var payload struct {
		Token         string `json:"token"`
		NotebookTitle string `json:"notebook_title"`
	}
	if err := json.Unmarshal(payloadBytes, &payload); err != nil ||
		(delivery.Kind == "notebook_invitation" && payload.Token == "") ||
		(delivery.Kind == "notebook_deleted" && payload.NotebookTitle == "") {
		return Delivery{}, false, errors.New("invalid mail Outbox payload")
	}
	delivery.Token = payload.Token
	delivery.NotebookTitle = payload.NotebookTitle
	delivery.LeaseToken = uuid.NewString()
	delivery.LeaseExpiresAt = now.Add(q.leaseDuration)
	err = tx.QueryRow(ctx, `
		update platform_mail_outbox
		set state='leased', attempt_no=attempt_no+1, lease_token=$2::uuid,
			lease_expires_at=$3, updated_at=$4
		where id=$1 and state='pending'
		returning attempt_no`, delivery.ID, delivery.LeaseToken, delivery.LeaseExpiresAt, now).Scan(&delivery.AttemptNo)
	if err != nil {
		return Delivery{}, false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Delivery{}, false, err
	}
	return delivery, true, nil
}

func (q *Queue) Complete(ctx context.Context, id, leaseToken string) error {
	if q == nil || q.pool == nil || id == "" || leaseToken == "" {
		return ErrLeaseLost
	}
	tx, err := q.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `set local role nano_worker`); err != nil {
		return err
	}
	tag, err := tx.Exec(ctx, `
		update platform_mail_outbox
		set state='sent', payload=payload-'token', lease_token=null, lease_expires_at=null,
			last_error_code=null, sent_at=now(), updated_at=now()
		where id=$1 and state='leased' and lease_token=$2::uuid and lease_expires_at > now()`, id, leaseToken)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return ErrLeaseLost
	}
	return tx.Commit(ctx)
}

func (q *Queue) Release(ctx context.Context, id, leaseToken, errorCode string) error {
	if q == nil || q.pool == nil || id == "" || leaseToken == "" || errorCode == "" {
		return ErrLeaseLost
	}
	tx, err := q.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `set local role nano_worker`); err != nil {
		return err
	}
	tag, err := tx.Exec(ctx, `
		update platform_mail_outbox
		set state=case when attempt_no >= 10 then 'failed' else 'pending' end,
			available_at=case when attempt_no >= 10 then available_at else now() + make_interval(secs => least(300, attempt_no * attempt_no)) end,
			lease_token=null, lease_expires_at=null, last_error_code=$3, updated_at=now()
		where id=$1 and state='leased' and lease_token=$2::uuid`, id, leaseToken, errorCode)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return ErrLeaseLost
	}
	return tx.Commit(ctx)
}
