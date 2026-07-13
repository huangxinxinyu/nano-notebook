package identity

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

var (
	ErrDuplicateEmail = errors.New("duplicate email")
	ErrMissingUser    = errors.New("missing user")
)

type DBTX interface {
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
	Query(context.Context, string, ...any) (pgx.Rows, error)
	QueryRow(context.Context, string, ...any) pgx.Row
}

type beginner interface {
	Begin(context.Context) (pgx.Tx, error)
}

type Store struct {
	db DBTX
}

type User struct {
	ID    string `json:"id"`
	Email string `json:"email"`
}

func NewStore(db DBTX) *Store {
	return &Store{db: db}
}

func (s *Store) RegisterLocalUser(ctx context.Context, userID, canonicalEmail, displayEmail, passwordHash string) error {
	starter, ok := s.db.(beginner)
	if !ok {
		return errors.New("identity registration requires privileged transaction starter")
	}
	tx, err := starter.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	_, err = tx.Exec(ctx, `insert into identity_users(id, canonical_email, display_email) values($1, $2, $3)`, userID, canonicalEmail, displayEmail)
	if err != nil {
		return ErrDuplicateEmail
	}
	_, err = tx.Exec(ctx, `insert into identity_local_credentials(user_id, password_hash) values($1, $2)`, userID, passwordHash)
	if err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *Store) LocalCredential(ctx context.Context, canonicalEmail string) (userID string, passwordHash string, err error) {
	err = s.db.QueryRow(ctx, `
		select u.id, c.password_hash
		from identity_users u
		join identity_local_credentials c on c.user_id = u.id
		where u.canonical_email = $1`, canonicalEmail).Scan(&userID, &passwordHash)
	return userID, passwordHash, err
}

func (s *Store) CreateSession(ctx context.Context, sessionID, userID, tokenHash string, expiresAt time.Time) error {
	_, err := s.db.Exec(ctx, `
		insert into identity_sessions(id, user_id, token_hash, expires_at)
		values($1, $2, $3, $4)`, sessionID, userID, tokenHash, expiresAt)
	return err
}

func (s *Store) CurrentUser(ctx context.Context, tokenHash string) (User, bool) {
	var user User
	err := s.db.QueryRow(ctx, `
		select u.id, u.canonical_email
		from identity_sessions s
		join identity_users u on u.id = s.user_id
		where s.token_hash = $1
		  and s.revoked_at is null
		  and s.expires_at > now()`, tokenHash).Scan(&user.ID, &user.Email)
	if err != nil {
		return User{}, false
	}
	return user, true
}

func (s *Store) UserByID(ctx context.Context, userID string) (User, bool) {
	var user User
	err := s.db.QueryRow(ctx, `select id, canonical_email from identity_users where id = $1`, userID).Scan(&user.ID, &user.Email)
	if err != nil {
		return User{}, false
	}
	return user, true
}

func (s *Store) RevokeSession(ctx context.Context, tokenHash string) error {
	_, err := s.db.Exec(ctx, `update identity_sessions set revoked_at = now() where token_hash = $1 and revoked_at is null`, tokenHash)
	return err
}

func (s *Store) RateLimited(ctx context.Context, canonicalEmail string) (bool, error) {
	var failed int
	err := s.db.QueryRow(ctx, `
		select count(*)
		from identity_auth_attempts
		where canonical_email = $1
		  and succeeded = false
		  and attempted_at > now() - interval '15 minutes'`, canonicalEmail).Scan(&failed)
	return failed >= 5, err
}

func (s *Store) RecordAttempt(ctx context.Context, canonicalEmail string, succeeded bool) error {
	_, err := s.db.Exec(ctx, `insert into identity_auth_attempts(canonical_email, succeeded) values($1, $2)`, canonicalEmail, succeeded)
	return err
}

func IsMissingCredential(err error) bool {
	return errors.Is(err, pgx.ErrNoRows)
}
