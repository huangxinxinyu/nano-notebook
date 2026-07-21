package notebook

import (
	"context"
	"encoding/json"
	"strings"
	"time"
)

type DeleteCommand struct {
	NotebookID      string
	UserID          string
	Locale          string
	Now             time.Time
	NotificationIDs map[string]string
}

func (s *Store) Rename(ctx context.Context, notebookID, userID, title string) (Notebook, error) {
	title = strings.TrimSpace(title)
	if notebookID == "" || userID == "" || title == "" || len([]rune(title)) > 160 {
		return Notebook{}, ErrNotFound
	}
	var result Notebook
	err := s.db.QueryRow(ctx, `
		update notebook_notebooks n set title=$3,updated_at=now(),recent_at=now()
		where n.id=$1 and exists(select 1 from notebook_memberships m where m.notebook_id=n.id and m.user_id=$2 and m.role='owner')
		returning n.id,n.title,'owner',n.recent_at`, notebookID, userID, title).
		Scan(&result.ID, &result.Title, &result.Role, &result.RecentAt)
	if err != nil {
		return Notebook{}, ErrNotFound
	}
	return result, nil
}

func (s *Store) Delete(ctx context.Context, command DeleteCommand) error {
	if command.NotebookID == "" || command.UserID == "" || (command.Locale != "en" && command.Locale != "zh-CN") || command.Now.IsZero() {
		return ErrNotFound
	}
	if _, err := s.db.Exec(ctx, `select pg_advisory_xact_lock(hashtextextended($1, 0))`, "notebook_membership:"+command.NotebookID); err != nil {
		return err
	}
	members, err := s.ListMembers(ctx, command.NotebookID)
	if err != nil {
		return err
	}
	var title string
	if err := s.db.QueryRow(ctx, `select title from notebook_notebooks where id=$1`, command.NotebookID).Scan(&title); err != nil {
		return ErrNotFound
	}
	for _, member := range members {
		if member.UserID == command.UserID {
			continue
		}
		messageID := command.NotificationIDs[member.UserID]
		if messageID == "" {
			return ErrInvalidMembership
		}
		payload, err := json.Marshal(map[string]any{"notebook_title": title})
		if err != nil {
			return err
		}
		if _, err := s.db.Exec(ctx, `
			insert into platform_mail_outbox(id,kind,actor_user_id,recipient_email,locale,payload,state,available_at,created_at,updated_at)
			values($1,'notebook_deleted',$2,$3,$4,$5::jsonb,'pending',$6,$6,$6)`,
			messageID, command.UserID, member.CanonicalEmail, command.Locale, string(payload), command.Now); err != nil {
			return err
		}
	}
	var deleted bool
	if err := s.db.QueryRow(ctx, `select nano_delete_notebook($1,$2)`, command.NotebookID, command.UserID).Scan(&deleted); err != nil {
		return err
	}
	if !deleted {
		return ErrNotFound
	}
	return nil
}
