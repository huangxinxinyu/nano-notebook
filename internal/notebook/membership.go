package notebook

import (
	"context"
	"errors"
	"time"
)

var ErrInvalidMembership = errors.New("invalid membership command")

type Member struct {
	UserID         string    `json:"user_id"`
	CanonicalEmail string    `json:"canonical_email"`
	DisplayEmail   string    `json:"display_email"`
	Role           string    `json:"role"`
	CreatedAt      time.Time `json:"created_at"`
}

func (s *Store) ListMembers(ctx context.Context, notebookID string) ([]Member, error) {
	rows, err := s.db.Query(ctx, `select user_id,canonical_email,display_email,role,created_at from nano_notebook_member_directory($1)`, notebookID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	members := make([]Member, 0)
	for rows.Next() {
		var member Member
		if err := rows.Scan(&member.UserID, &member.CanonicalEmail, &member.DisplayEmail, &member.Role, &member.CreatedAt); err != nil {
			return nil, err
		}
		members = append(members, member)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(members) == 0 {
		return nil, ErrNotFound
	}
	return members, nil
}

func (s *Store) ChangeMemberRole(ctx context.Context, notebookID, actorUserID, targetUserID, role string) error {
	if notebookID == "" || actorUserID == "" || targetUserID == "" || (role != "viewer" && role != "editor") {
		return ErrInvalidMembership
	}
	command, err := s.db.Exec(ctx, `
		update notebook_memberships target
		set role=$4
		where target.notebook_id=$1 and target.user_id=$3 and target.role in ('viewer','editor')
		  and exists(select 1 from notebook_memberships actor where actor.notebook_id=$1 and actor.user_id=$2 and actor.role='owner')`,
		notebookID, actorUserID, targetUserID, role)
	if err != nil {
		return err
	}
	if command.RowsAffected() != 1 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) TransferOwnership(ctx context.Context, notebookID, actorUserID, targetUserID string) error {
	if notebookID == "" || actorUserID == "" || targetUserID == "" || actorUserID == targetUserID {
		return ErrInvalidMembership
	}
	if _, err := s.db.Exec(ctx, `select pg_advisory_xact_lock(hashtextextended($1, 0))`, "notebook_membership:"+notebookID); err != nil {
		return err
	}
	var transferred bool
	if err := s.db.QueryRow(ctx, `select nano_transfer_notebook_ownership($1,$2,$3)`, notebookID, actorUserID, targetUserID).Scan(&transferred); err != nil {
		return err
	}
	if !transferred {
		return ErrNotFound
	}
	return nil
}

func (s *Store) RemoveMember(ctx context.Context, notebookID, actorUserID, targetUserID string) error {
	return s.depart(ctx, notebookID, actorUserID, targetUserID, true)
}

func (s *Store) Leave(ctx context.Context, notebookID, userID string) error {
	return s.depart(ctx, notebookID, userID, userID, false)
}

func (s *Store) depart(ctx context.Context, notebookID, actorUserID, targetUserID string, ownerAction bool) error {
	if notebookID == "" || actorUserID == "" || targetUserID == "" {
		return ErrInvalidMembership
	}
	if _, err := s.db.Exec(ctx, `select pg_advisory_xact_lock(hashtextextended($1, 0))`, "notebook_membership:"+notebookID); err != nil {
		return err
	}
	var departed bool
	if err := s.db.QueryRow(ctx, `select nano_depart_notebook_member($1,$2,$3,$4)`, notebookID, actorUserID, targetUserID, ownerAction).Scan(&departed); err != nil {
		return err
	}
	if !departed {
		return ErrNotFound
	}
	return nil
}
