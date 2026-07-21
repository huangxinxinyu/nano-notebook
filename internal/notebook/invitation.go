package notebook

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

var (
	ErrInvitationConflict = errors.New("invitation conflict")
	ErrMemberCapacity     = errors.New("member capacity reached")
	ErrInvalidInvitation  = errors.New("invalid invitation")
)

type Invitation struct {
	ID               string     `json:"id"`
	NotebookID       string     `json:"notebook_id"`
	CanonicalEmail   string     `json:"canonical_email"`
	DisplayEmail     string     `json:"display_email"`
	Role             string     `json:"role"`
	State            string     `json:"state"`
	InvitedByUserID  string     `json:"invited_by_user_id"`
	AcceptedByUserID *string    `json:"accepted_by_user_id,omitempty"`
	TokenGeneration  int        `json:"token_generation"`
	ExpiresAt        time.Time  `json:"expires_at"`
	AcceptedAt       *time.Time `json:"accepted_at,omitempty"`
	RevokedAt        *time.Time `json:"revoked_at,omitempty"`
	CreatedAt        time.Time  `json:"created_at"`
	UpdatedAt        time.Time  `json:"updated_at"`
}

type InvitationPreview struct {
	NotebookTitle string    `json:"notebook_title"`
	Role          string    `json:"role"`
	MaskedEmail   string    `json:"masked_email"`
	ExpiresAt     time.Time `json:"expires_at"`
}

func (s *Store) ResolveInvitation(ctx context.Context, tokenHash string) (InvitationPreview, error) {
	if len(tokenHash) != 64 {
		return InvitationPreview{}, ErrInvalidInvitation
	}
	var preview InvitationPreview
	var email string
	if err := s.db.QueryRow(ctx, `select notebook_title,invited_role,canonical_email,expires_at from nano_resolve_notebook_invitation($1)`, tokenHash).
		Scan(&preview.NotebookTitle, &preview.Role, &email, &preview.ExpiresAt); err != nil {
		return InvitationPreview{}, ErrInvalidInvitation
	}
	parts := strings.SplitN(email, "@", 2)
	if len(parts) != 2 || parts[0] == "" {
		return InvitationPreview{}, ErrInvalidInvitation
	}
	preview.MaskedEmail = string([]rune(parts[0])[0]) + "***@" + parts[1]
	return preview, nil
}

type CreateInvitationCommand struct {
	ID              string
	NotebookID      string
	InvitedByUserID string
	CanonicalEmail  string
	DisplayEmail    string
	Role            string
	TokenHash       string
	IdempotencyKey  string
	RequestHash     string
	MailMessageID   string
	MailLocale      string
	RawToken        string
	Now             time.Time
	ExpiresAt       time.Time
}

type Membership struct {
	NotebookID string    `json:"notebook_id"`
	UserID     string    `json:"user_id"`
	Role       string    `json:"role"`
	CreatedAt  time.Time `json:"created_at"`
}

type AcceptInvitationCommand struct {
	TokenHash      string
	UserID         string
	CanonicalEmail string
	IdempotencyKey string
	RequestHash    string
	Now            time.Time
}

type ResendInvitationCommand struct {
	InvitationID  string
	NotebookID    string
	UserID        string
	TokenHash     string
	RawToken      string
	MailMessageID string
	MailLocale    string
	Now           time.Time
	ExpiresAt     time.Time
}

func (s *Store) ListInvitations(ctx context.Context, notebookID, userID string, now time.Time) ([]Invitation, error) {
	if notebookID == "" || userID == "" || now.IsZero() {
		return nil, ErrInvalidInvitation
	}
	if _, err := s.db.Exec(ctx, `
		update notebook_invitations set state='expired',updated_at=$3
		where notebook_id=$1 and state='pending' and expires_at <= $3
		  and exists(select 1 from notebook_memberships m where m.notebook_id=$1 and m.user_id=$2 and m.role='owner')`, notebookID, userID, now); err != nil {
		return nil, err
	}
	rows, err := s.db.Query(ctx, `
		select i.id,i.notebook_id,i.canonical_email,i.display_email,i.role,i.state,i.invited_by_user_id,
			i.accepted_by_user_id,i.token_generation,i.expires_at,i.accepted_at,i.revoked_at,i.created_at,i.updated_at
		from notebook_invitations i
		where i.notebook_id=$1 and exists(select 1 from notebook_memberships m where m.notebook_id=i.notebook_id and m.user_id=$2 and m.role='owner')
		order by i.created_at desc,i.id`, notebookID, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	invitations := make([]Invitation, 0)
	for rows.Next() {
		var invitation Invitation
		if err := rows.Scan(&invitation.ID, &invitation.NotebookID, &invitation.CanonicalEmail, &invitation.DisplayEmail,
			&invitation.Role, &invitation.State, &invitation.InvitedByUserID, &invitation.AcceptedByUserID,
			&invitation.TokenGeneration, &invitation.ExpiresAt, &invitation.AcceptedAt, &invitation.RevokedAt,
			&invitation.CreatedAt, &invitation.UpdatedAt); err != nil {
			return nil, err
		}
		invitations = append(invitations, invitation)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(invitations) == 0 {
		var owner bool
		if err := s.db.QueryRow(ctx, `select exists(select 1 from notebook_memberships where notebook_id=$1 and user_id=$2 and role='owner')`, notebookID, userID).Scan(&owner); err != nil {
			return nil, err
		}
		if !owner {
			return nil, ErrNotFound
		}
	}
	return invitations, nil
}

func (s *Store) RevokeInvitation(ctx context.Context, notebookID, invitationID, userID string, now time.Time) error {
	if notebookID == "" || invitationID == "" || userID == "" || now.IsZero() {
		return ErrInvalidInvitation
	}
	if _, err := s.db.Exec(ctx, `select pg_advisory_xact_lock(hashtextextended($1, 0))`, "notebook_membership:"+notebookID); err != nil {
		return err
	}
	command, err := s.db.Exec(ctx, `
		update notebook_invitations i
		set state='revoked', revoked_at=$4, updated_at=$4
		where i.id=$1 and i.notebook_id=$2 and i.state='pending'
		  and exists(select 1 from notebook_memberships m where m.notebook_id=i.notebook_id and m.user_id=$3 and m.role='owner')`,
		invitationID, notebookID, userID, now)
	if err != nil {
		return err
	}
	if command.RowsAffected() != 1 {
		return ErrInvalidInvitation
	}
	return nil
}

func (s *Store) ResendInvitation(ctx context.Context, command ResendInvitationCommand) (Invitation, error) {
	if command.InvitationID == "" || command.NotebookID == "" || command.UserID == "" ||
		len(command.TokenHash) != 64 || command.RawToken == "" || command.MailMessageID == "" ||
		(command.MailLocale != "en" && command.MailLocale != "zh-CN") || command.Now.IsZero() || !command.ExpiresAt.After(command.Now) {
		return Invitation{}, ErrInvalidInvitation
	}
	if _, err := s.db.Exec(ctx, `select pg_advisory_xact_lock(hashtextextended($1, 0))`, "notebook_membership:"+command.NotebookID); err != nil {
		return Invitation{}, err
	}
	var invitation Invitation
	err := s.db.QueryRow(ctx, `
		update notebook_invitations i
		set state='pending', token_hash=$4, token_generation=token_generation+1,
			expires_at=$5, accepted_by_user_id=null, accepted_at=null, revoked_at=null, updated_at=$6
		where i.id=$1 and i.notebook_id=$2
		  and (i.state='expired' or (i.state='pending' and i.expires_at <= $6))
		  and exists(select 1 from notebook_memberships m where m.notebook_id=i.notebook_id and m.user_id=$3 and m.role='owner')
		returning id, notebook_id, canonical_email, display_email, role, state, invited_by_user_id,
			accepted_by_user_id, token_generation, expires_at, accepted_at, revoked_at, created_at, updated_at`,
		command.InvitationID, command.NotebookID, command.UserID, command.TokenHash, command.ExpiresAt, command.Now).
		Scan(&invitation.ID, &invitation.NotebookID, &invitation.CanonicalEmail, &invitation.DisplayEmail,
			&invitation.Role, &invitation.State, &invitation.InvitedByUserID, &invitation.AcceptedByUserID,
			&invitation.TokenGeneration, &invitation.ExpiresAt, &invitation.AcceptedAt, &invitation.RevokedAt,
			&invitation.CreatedAt, &invitation.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Invitation{}, ErrInvalidInvitation
	}
	if err != nil {
		return Invitation{}, err
	}
	payload, err := json.Marshal(map[string]any{
		"invitation_id": invitation.ID, "notebook_id": invitation.NotebookID, "role": invitation.Role,
		"token": command.RawToken, "expires_at": command.ExpiresAt,
	})
	if err != nil {
		return Invitation{}, err
	}
	if _, err := s.db.Exec(ctx, `
		insert into platform_mail_outbox(id,kind,invitation_id,actor_user_id,recipient_email,locale,payload,state,available_at,created_at,updated_at)
		values($1,'notebook_invitation',$2,$3,$4,$5,$6::jsonb,'pending',$7,$7,$7)`,
		command.MailMessageID, invitation.ID, command.UserID, invitation.CanonicalEmail, command.MailLocale, string(payload), command.Now); err != nil {
		return Invitation{}, err
	}
	return invitation, nil
}

func (s *Store) CreateInvitation(ctx context.Context, command CreateInvitationCommand) (Invitation, bool, error) {
	command.CanonicalEmail = strings.ToLower(strings.TrimSpace(command.CanonicalEmail))
	command.DisplayEmail = strings.TrimSpace(command.DisplayEmail)
	if command.ID == "" || command.NotebookID == "" || command.InvitedByUserID == "" ||
		command.IdempotencyKey == "" || len(command.RequestHash) != 64 || len(command.TokenHash) != 64 ||
		command.MailMessageID == "" || command.RawToken == "" || (command.MailLocale != "en" && command.MailLocale != "zh-CN") ||
		(command.Role != "viewer" && command.Role != "editor") || command.CanonicalEmail == "" ||
		!command.ExpiresAt.After(command.Now) {
		return Invitation{}, false, ErrInvalidInvitation
	}
	if _, err := s.db.Exec(ctx, `select pg_advisory_xact_lock(hashtextextended($1, 0))`, "notebook_membership:"+command.NotebookID); err != nil {
		return Invitation{}, false, err
	}

	var existingHash, responseJSON string
	err := s.db.QueryRow(ctx, `
		select request_hash, response_json::text
		from platform_idempotency_keys
		where principal_id=$1 and action='create_notebook_invitation' and key=$2`, command.InvitedByUserID, command.IdempotencyKey).
		Scan(&existingHash, &responseJSON)
	if err == nil {
		if existingHash != command.RequestHash {
			return Invitation{}, false, ErrIdempotencyMismatch
		}
		var response struct {
			Invitation Invitation `json:"invitation"`
		}
		if err := json.Unmarshal([]byte(responseJSON), &response); err != nil {
			return Invitation{}, false, err
		}
		return response.Invitation, true, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return Invitation{}, false, err
	}

	if _, err := s.db.Exec(ctx, `
		update notebook_invitations
		set state='expired', updated_at=$2
		where notebook_id=$1 and state='pending' and expires_at <= $2`, command.NotebookID, command.Now); err != nil {
		return Invitation{}, false, err
	}
	var owner bool
	if err := s.db.QueryRow(ctx, `
		select exists(select 1 from notebook_memberships where notebook_id=$1 and user_id=$2 and role='owner')`,
		command.NotebookID, command.InvitedByUserID).Scan(&owner); err != nil {
		return Invitation{}, false, err
	}
	if !owner {
		return Invitation{}, false, ErrNotFound
	}
	var memberExists bool
	if err := s.db.QueryRow(ctx, `select nano_notebook_email_is_member($1,$2)`, command.NotebookID, command.CanonicalEmail).Scan(&memberExists); err != nil {
		return Invitation{}, false, err
	}
	if memberExists {
		return Invitation{}, false, ErrInvitationConflict
	}
	var duplicate bool
	if err := s.db.QueryRow(ctx, `
		select exists(select 1 from notebook_invitations where notebook_id=$1 and canonical_email=$2 and state='pending')`,
		command.NotebookID, command.CanonicalEmail).Scan(&duplicate); err != nil {
		return Invitation{}, false, err
	}
	if duplicate {
		return Invitation{}, false, ErrInvitationConflict
	}
	var used int
	if err := s.db.QueryRow(ctx, `
		select nano_notebook_reserved_member_slots($1,$2)`, command.NotebookID, command.Now).Scan(&used); err != nil {
		return Invitation{}, false, err
	}
	if used >= 50 {
		return Invitation{}, false, ErrMemberCapacity
	}

	var created Invitation
	err = s.db.QueryRow(ctx, `
		insert into notebook_invitations(
			id, notebook_id, canonical_email, display_email, role, token_hash, token_generation,
			state, invited_by_user_id, expires_at, created_at, updated_at
		) values($1,$2,$3,$4,$5,$6,1,'pending',$7,$8,$9,$9)
		returning id, notebook_id, canonical_email, display_email, role, state, invited_by_user_id,
			accepted_by_user_id, token_generation, expires_at, accepted_at, revoked_at, created_at, updated_at`,
		command.ID, command.NotebookID, command.CanonicalEmail, command.DisplayEmail, command.Role,
		command.TokenHash, command.InvitedByUserID, command.ExpiresAt, command.Now).
		Scan(&created.ID, &created.NotebookID, &created.CanonicalEmail, &created.DisplayEmail, &created.Role,
			&created.State, &created.InvitedByUserID, &created.AcceptedByUserID, &created.TokenGeneration,
			&created.ExpiresAt, &created.AcceptedAt, &created.RevokedAt, &created.CreatedAt, &created.UpdatedAt)
	if err != nil {
		return Invitation{}, false, err
	}
	mailPayload, err := json.Marshal(map[string]any{
		"invitation_id": command.ID,
		"notebook_id":   command.NotebookID,
		"role":          command.Role,
		"token":         command.RawToken,
		"expires_at":    command.ExpiresAt,
	})
	if err != nil {
		return Invitation{}, false, err
	}
	if _, err := s.db.Exec(ctx, `
		insert into platform_mail_outbox(
			id, kind, invitation_id, actor_user_id, recipient_email, locale, payload,
			state, available_at, created_at, updated_at
		) values($1,'notebook_invitation',$2,$3,$4,$5,$6::jsonb,'pending',$7,$7,$7)`,
		command.MailMessageID, command.ID, command.InvitedByUserID, command.CanonicalEmail,
		command.MailLocale, string(mailPayload), command.Now); err != nil {
		return Invitation{}, false, err
	}
	response, err := json.Marshal(map[string]any{"invitation": created})
	if err != nil {
		return Invitation{}, false, err
	}
	if _, err := s.db.Exec(ctx, `
		insert into platform_idempotency_keys(principal_id, action, key, request_hash, status_code, response_json)
		values($1,'create_notebook_invitation',$2,$3,$4,$5::jsonb)`,
		command.InvitedByUserID, command.IdempotencyKey, command.RequestHash, http.StatusCreated, string(response)); err != nil {
		return Invitation{}, false, err
	}
	return created, false, nil
}

func (s *Store) AcceptInvitation(ctx context.Context, command AcceptInvitationCommand) (Membership, error) {
	command.CanonicalEmail = strings.ToLower(strings.TrimSpace(command.CanonicalEmail))
	if len(command.TokenHash) != 64 || command.UserID == "" || command.CanonicalEmail == "" ||
		command.IdempotencyKey == "" || len(command.RequestHash) != 64 || command.Now.IsZero() {
		return Membership{}, ErrInvalidInvitation
	}
	var existingHash, responseJSON string
	err := s.db.QueryRow(ctx, `
		select request_hash, response_json::text from platform_idempotency_keys
		where principal_id=$1 and action='accept_notebook_invitation' and key=$2`, command.UserID, command.IdempotencyKey).
		Scan(&existingHash, &responseJSON)
	if err == nil {
		if existingHash != command.RequestHash {
			return Membership{}, ErrIdempotencyMismatch
		}
		var response struct {
			Membership Membership `json:"membership"`
		}
		if err := json.Unmarshal([]byte(responseJSON), &response); err != nil {
			return Membership{}, err
		}
		return response.Membership, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return Membership{}, err
	}
	var notebookID string
	if err := s.db.QueryRow(ctx, `select notebook_id from notebook_invitations where token_hash=$1`, command.TokenHash).Scan(&notebookID); err != nil {
		return Membership{}, ErrInvalidInvitation
	}
	if _, err := s.db.Exec(ctx, `select pg_advisory_xact_lock(hashtextextended($1, 0))`, "notebook_membership:"+notebookID); err != nil {
		return Membership{}, err
	}
	var invitation Invitation
	err = s.db.QueryRow(ctx, `
		select id, notebook_id, canonical_email, display_email, role, state, invited_by_user_id,
			accepted_by_user_id, token_generation, expires_at, accepted_at, revoked_at, created_at, updated_at
		from notebook_invitations
		where token_hash=$1
		for update`, command.TokenHash).
		Scan(&invitation.ID, &invitation.NotebookID, &invitation.CanonicalEmail, &invitation.DisplayEmail,
			&invitation.Role, &invitation.State, &invitation.InvitedByUserID, &invitation.AcceptedByUserID,
			&invitation.TokenGeneration, &invitation.ExpiresAt, &invitation.AcceptedAt, &invitation.RevokedAt,
			&invitation.CreatedAt, &invitation.UpdatedAt)
	if err != nil {
		return Membership{}, ErrInvalidInvitation
	}
	var actualEmail string
	if err := s.db.QueryRow(ctx, `select canonical_email from identity_users where id=$1`, command.UserID).Scan(&actualEmail); err != nil {
		return Membership{}, ErrInvalidInvitation
	}
	if invitation.State != "pending" || !command.Now.Before(invitation.ExpiresAt) ||
		actualEmail != invitation.CanonicalEmail || command.CanonicalEmail != invitation.CanonicalEmail {
		if invitation.State == "pending" && !command.Now.Before(invitation.ExpiresAt) {
			_, _ = s.db.Exec(ctx, `update notebook_invitations set state='expired', updated_at=$2 where id=$1 and state='pending'`, invitation.ID, command.Now)
		}
		return Membership{}, ErrInvalidInvitation
	}
	var exists bool
	if err := s.db.QueryRow(ctx, `select exists(select 1 from notebook_memberships where notebook_id=$1 and user_id=$2)`, invitation.NotebookID, command.UserID).Scan(&exists); err != nil {
		return Membership{}, err
	}
	if exists {
		return Membership{}, ErrInvitationConflict
	}
	var membership Membership
	if err := s.db.QueryRow(ctx, `
		insert into notebook_memberships(notebook_id, user_id, role)
		values($1,$2,$3)
		returning notebook_id, user_id, role, created_at`, invitation.NotebookID, command.UserID, invitation.Role).
		Scan(&membership.NotebookID, &membership.UserID, &membership.Role, &membership.CreatedAt); err != nil {
		return Membership{}, err
	}
	if _, err := s.db.Exec(ctx, `
		update notebook_invitations
		set state='accepted', accepted_by_user_id=$2, accepted_at=$3, updated_at=$3
		where id=$1 and state='pending'`, invitation.ID, command.UserID, command.Now); err != nil {
		return Membership{}, err
	}
	response, err := json.Marshal(map[string]any{"membership": membership})
	if err != nil {
		return Membership{}, err
	}
	if _, err := s.db.Exec(ctx, `
		insert into platform_idempotency_keys(principal_id,action,key,request_hash,status_code,response_json)
		values($1,'accept_notebook_invitation',$2,$3,$4,$5::jsonb)`,
		command.UserID, command.IdempotencyKey, command.RequestHash, http.StatusOK, string(response)); err != nil {
		return Membership{}, err
	}
	return membership, nil
}
