package app_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/huangxinxinyu/nano-notebook/internal/mailoutbox"
	"github.com/huangxinxinyu/nano-notebook/internal/notebook"
	"github.com/jackc/pgx/v5"
)

func TestMembershipSchemaAllowsViewerEditorAndOwnerRoles(t *testing.T) {
	api := newTestAPI(t)

	var definition string
	err := api.db.Pool().QueryRow(context.Background(), `
		select pg_get_constraintdef(oid)
		from pg_constraint
		where conrelid = 'notebook_memberships'::regclass
		  and conname = 'notebook_memberships_role_check'`).Scan(&definition)
	if err != nil {
		t.Fatal(err)
	}
	for _, role := range []string{"viewer", "editor", "owner"} {
		if !strings.Contains(definition, "'"+role+"'") {
			t.Fatalf("membership role constraint %q does not allow %q", definition, role)
		}
	}
}

func TestNotebookCapabilityMatrixUsesMembershipRole(t *testing.T) {
	api := newTestAPI(t)
	owner := api.register(t, "capability-owner@example.com")
	api.register(t, "capability-viewer@example.com")
	api.register(t, "capability-editor@example.com")
	notebookID := createSourceTestNotebook(t, api, owner, "capability-matrix")
	viewerID := sourceTestUserID(t, api, "capability-viewer@example.com")
	editorID := sourceTestUserID(t, api, "capability-editor@example.com")
	ownerID := sourceTestUserID(t, api, "capability-owner@example.com")

	if _, err := api.db.Pool().Exec(context.Background(), `
		insert into notebook_memberships(notebook_id, user_id, role)
		values($1,$2,'viewer'),($1,$3,'editor')`, notebookID, viewerID, editorID); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name   string
		userID string
		want   map[string]bool
	}{
		{name: "viewer", userID: viewerID, want: map[string]bool{"notebook.read": true, "source.read": true, "source.maintain": false, "notebook.manage": false}},
		{name: "editor", userID: editorID, want: map[string]bool{"notebook.read": true, "source.read": true, "source.maintain": true, "notebook.manage": false}},
		{name: "owner", userID: ownerID, want: map[string]bool{"notebook.read": true, "source.read": true, "source.maintain": true, "notebook.manage": true}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := api.db.WithRequestPrincipal(context.Background(), test.userID, func(tx pgx.Tx) error {
				for capability, expected := range test.want {
					var actual bool
					if err := tx.QueryRow(context.Background(), `select nano_has_notebook_capability($1,$2)`, notebookID, capability).Scan(&actual); err != nil {
						return err
					}
					if actual != expected {
						t.Errorf("capability %q = %t, want %t", capability, actual, expected)
					}
				}
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestMembershipRLSRejectsSelfEscalationAndOwnerRemoval(t *testing.T) {
	api := newTestAPI(t)
	owner := api.register(t, "membership-guard-owner@example.com")
	api.register(t, "membership-guard-viewer@example.com")
	notebookID := createSourceTestNotebook(t, api, owner, "membership-guard")
	ownerID := sourceTestUserID(t, api, "membership-guard-owner@example.com")
	viewerID := sourceTestUserID(t, api, "membership-guard-viewer@example.com")
	if _, err := api.db.Pool().Exec(context.Background(), `
		insert into notebook_memberships(notebook_id,user_id,role) values($1,$2,'viewer')`, notebookID, viewerID); err != nil {
		t.Fatal(err)
	}

	err := api.db.WithRequestPrincipal(context.Background(), viewerID, func(tx pgx.Tx) error {
		_, err := tx.Exec(context.Background(), `
			update notebook_memberships set role='editor' where notebook_id=$1 and user_id=$2`, notebookID, viewerID)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	var viewerRole string
	if err := api.db.Pool().QueryRow(context.Background(), `select role from notebook_memberships where notebook_id=$1 and user_id=$2`, notebookID, viewerID).Scan(&viewerRole); err != nil {
		t.Fatal(err)
	}
	if viewerRole != "viewer" {
		t.Fatalf("viewer changed their own Membership role to %q", viewerRole)
	}
	err = api.db.WithRequestPrincipal(context.Background(), ownerID, func(tx pgx.Tx) error {
		_, err := tx.Exec(context.Background(), `
			delete from notebook_memberships where notebook_id=$1 and user_id=$2`, notebookID, ownerID)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	var ownerCount int
	if err := api.db.Pool().QueryRow(context.Background(), `select count(*) from notebook_memberships where notebook_id=$1 and role='owner'`, notebookID).Scan(&ownerCount); err != nil {
		t.Fatal(err)
	}
	if ownerCount != 1 {
		t.Fatalf("owner count=%d after attempted self-removal", ownerCount)
	}
}

func TestSharedMembersListAndOpenNotebookWithTheirRole(t *testing.T) {
	api := newTestAPI(t)
	owner := api.register(t, "shared-list-owner@example.com")
	viewer := api.register(t, "shared-list-viewer@example.com")
	api.register(t, "shared-list-editor@example.com")
	notebookID := createSourceTestNotebook(t, api, owner, "Shared Research")
	viewerID := sourceTestUserID(t, api, "shared-list-viewer@example.com")
	editorID := sourceTestUserID(t, api, "shared-list-editor@example.com")
	if _, err := api.db.Pool().Exec(context.Background(), `
		insert into notebook_memberships(notebook_id, user_id, role)
		values($1,$2,'viewer'),($1,$3,'editor')`, notebookID, viewerID, editorID); err != nil {
		t.Fatal(err)
	}

	list := api.getWithCookie(t, "/api/v1/notebooks?scope=shared&query=source", viewer)
	if list.Code != 200 {
		t.Fatalf("shared list status=%d body=%s", list.Code, list.Body.String())
	}
	var listed struct {
		Notebooks []struct {
			ID   string `json:"id"`
			Role string `json:"role"`
		} `json:"notebooks"`
	}
	decodeBody(t, list, &listed)
	if len(listed.Notebooks) != 1 || listed.Notebooks[0].ID != notebookID || listed.Notebooks[0].Role != "viewer" {
		t.Fatalf("shared notebooks=%+v", listed.Notebooks)
	}

	opened := api.getWithCookie(t, "/api/v1/notebooks/"+notebookID, viewer)
	if opened.Code != 200 {
		t.Fatalf("shared get status=%d body=%s", opened.Code, opened.Body.String())
	}
	var payload struct {
		Notebook struct {
			ID   string `json:"id"`
			Role string `json:"role"`
		} `json:"notebook"`
	}
	decodeBody(t, opened, &payload)
	if payload.Notebook.ID != notebookID || payload.Notebook.Role != "viewer" {
		t.Fatalf("shared notebook=%+v", payload.Notebook)
	}
}

func TestInvitationSchemaDefinesLifecycleAndTokenAuthority(t *testing.T) {
	api := newTestAPI(t)

	var tableExists bool
	if err := api.db.Pool().QueryRow(context.Background(), `
		select to_regclass('public.notebook_invitations') is not null`).Scan(&tableExists); err != nil {
		t.Fatal(err)
	}
	if !tableExists {
		t.Fatal("notebook_invitations table is missing")
	}

	rows, err := api.db.Pool().Query(context.Background(), `
		select column_name
		from information_schema.columns
		where table_schema='public' and table_name='notebook_invitations'`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	columns := map[string]bool{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatal(err)
		}
		columns[name] = true
	}
	for _, required := range []string{"id", "notebook_id", "canonical_email", "display_email", "role", "token_hash", "token_generation", "state", "invited_by_user_id", "accepted_by_user_id", "expires_at", "accepted_at", "revoked_at", "created_at", "updated_at"} {
		if !columns[required] {
			t.Errorf("Invitation column %q is missing", required)
		}
	}
}

func TestOwnerCreatesPendingInvitation(t *testing.T) {
	api := newTestAPI(t)
	owner := api.register(t, "invite-owner@example.com")
	notebookID := createSourceTestNotebook(t, api, owner, "invite-create")
	ownerID := sourceTestUserID(t, api, "invite-owner@example.com")
	now := time.Date(2026, 7, 21, 8, 0, 0, 0, time.UTC)

	var created notebook.Invitation
	var reused bool
	err := api.db.WithRequestPrincipal(context.Background(), ownerID, func(tx pgx.Tx) error {
		var err error
		created, reused, err = notebook.NewStore(tx).CreateInvitation(context.Background(), notebook.CreateInvitationCommand{
			ID: "inv_create", NotebookID: notebookID, InvitedByUserID: ownerID,
			CanonicalEmail: "viewer@example.com", DisplayEmail: "Viewer@Example.com", Role: "viewer",
			TokenHash: strings.Repeat("a", 64), IdempotencyKey: "invite-create", RequestHash: strings.Repeat("b", 64),
			MailMessageID: "mail_inv_create", MailLocale: "en", RawToken: "raw-invitation-token",
			Now: now, ExpiresAt: now.Add(7 * 24 * time.Hour),
		})
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if reused || created.ID != "inv_create" || created.NotebookID != notebookID || created.Role != "viewer" || created.State != "pending" || !created.ExpiresAt.Equal(now.Add(7*24*time.Hour)) {
		t.Fatalf("created=%+v reused=%t", created, reused)
	}
	var mailState, recipient, rawToken string
	if err := api.db.Pool().QueryRow(context.Background(), `
		select state, recipient_email, payload->>'token'
		from platform_mail_outbox where id='mail_inv_create'`).Scan(&mailState, &recipient, &rawToken); err != nil {
		t.Fatal(err)
	}
	if mailState != "pending" || recipient != "viewer@example.com" || rawToken != "raw-invitation-token" {
		t.Fatalf("mail state=%q recipient=%q token=%q", mailState, recipient, rawToken)
	}
}

func TestMatchingUserAcceptsInvitationAtomically(t *testing.T) {
	api := newTestAPI(t)
	owner := api.register(t, "accept-owner@example.com")
	recipient := api.register(t, "accept-viewer@example.com")
	notebookID := createSourceTestNotebook(t, api, owner, "invite-accept")
	ownerID := sourceTestUserID(t, api, "accept-owner@example.com")
	recipientID := sourceTestUserID(t, api, "accept-viewer@example.com")
	now := time.Date(2026, 7, 21, 9, 0, 0, 0, time.UTC)
	tokenHash := strings.Repeat("c", 64)

	err := api.db.WithRequestPrincipal(context.Background(), ownerID, func(tx pgx.Tx) error {
		_, _, err := notebook.NewStore(tx).CreateInvitation(context.Background(), notebook.CreateInvitationCommand{
			ID: "inv_accept", NotebookID: notebookID, InvitedByUserID: ownerID,
			CanonicalEmail: "accept-viewer@example.com", DisplayEmail: "accept-viewer@example.com", Role: "viewer",
			TokenHash: tokenHash, IdempotencyKey: "invite-accept", RequestHash: strings.Repeat("d", 64),
			MailMessageID: "mail_inv_accept", MailLocale: "en", RawToken: "accept-raw-token",
			Now: now, ExpiresAt: now.Add(7 * 24 * time.Hour),
		})
		return err
	})
	if err != nil {
		t.Fatal(err)
	}

	var membership notebook.Membership
	err = api.db.WithRequestPrincipal(context.Background(), recipientID, func(tx pgx.Tx) error {
		var err error
		membership, err = notebook.NewStore(tx).AcceptInvitation(context.Background(), notebook.AcceptInvitationCommand{
			TokenHash: tokenHash, UserID: recipientID, CanonicalEmail: "accept-viewer@example.com", Now: now.Add(time.Hour),
			IdempotencyKey: "accept-once", RequestHash: strings.Repeat("9", 64),
		})
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if membership.NotebookID != notebookID || membership.UserID != recipientID || membership.Role != "viewer" {
		t.Fatalf("membership=%+v", membership)
	}
	var replay notebook.Membership
	err = api.db.WithRequestPrincipal(context.Background(), recipientID, func(tx pgx.Tx) error {
		var err error
		replay, err = notebook.NewStore(tx).AcceptInvitation(context.Background(), notebook.AcceptInvitationCommand{
			TokenHash: tokenHash, UserID: recipientID, CanonicalEmail: "accept-viewer@example.com", Now: now.Add(2 * time.Hour),
			IdempotencyKey: "accept-once", RequestHash: strings.Repeat("9", 64),
		})
		return err
	})
	if err != nil || replay != membership {
		t.Fatalf("idempotent replay=%+v err=%v, want %+v", replay, err, membership)
	}

	var state string
	if err := api.db.Pool().QueryRow(context.Background(), `select state from notebook_invitations where id='inv_accept'`).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if state != "accepted" {
		t.Fatalf("Invitation state=%q", state)
	}
	opened := api.getWithCookie(t, "/api/v1/notebooks/"+notebookID, recipient)
	if opened.Code != 200 {
		t.Fatalf("accepted recipient open status=%d body=%s", opened.Code, opened.Body.String())
	}
}

func TestOwnerRevokesAndResendsExpiredInvitationWithRotatedToken(t *testing.T) {
	api := newTestAPI(t)
	owner := api.register(t, "invite-lifecycle-owner@example.com")
	notebookID := createSourceTestNotebook(t, api, owner, "invite-lifecycle")
	ownerID := sourceTestUserID(t, api, "invite-lifecycle-owner@example.com")
	now := time.Now().UTC().Truncate(time.Second)
	create := func(id, hash string, at time.Time) {
		t.Helper()
		if err := api.db.WithRequestPrincipal(context.Background(), ownerID, func(tx pgx.Tx) error {
			_, _, err := notebook.NewStore(tx).CreateInvitation(context.Background(), notebook.CreateInvitationCommand{
				ID: id, NotebookID: notebookID, InvitedByUserID: ownerID,
				CanonicalEmail: "lifecycle-viewer@example.com", DisplayEmail: "lifecycle-viewer@example.com", Role: "viewer",
				TokenHash: hash, IdempotencyKey: id, RequestHash: strings.Repeat("8", 64),
				MailMessageID: "mail_" + id, MailLocale: "en", RawToken: "raw_" + id,
				Now: at, ExpiresAt: at.Add(7 * 24 * time.Hour),
			})
			return err
		}); err != nil {
			t.Fatal(err)
		}
	}
	create("inv_revoke", strings.Repeat("5", 64), now)
	if err := api.db.WithRequestPrincipal(context.Background(), ownerID, func(tx pgx.Tx) error {
		return notebook.NewStore(tx).RevokeInvitation(context.Background(), notebookID, "inv_revoke", ownerID, now.Add(time.Hour))
	}); err != nil {
		t.Fatal(err)
	}

	create("inv_resend", strings.Repeat("6", 64), now.Add(-8*24*time.Hour))
	var resent notebook.Invitation
	if err := api.db.WithRequestPrincipal(context.Background(), ownerID, func(tx pgx.Tx) error {
		var err error
		resent, err = notebook.NewStore(tx).ResendInvitation(context.Background(), notebook.ResendInvitationCommand{
			InvitationID: "inv_resend", NotebookID: notebookID, UserID: ownerID,
			TokenHash: strings.Repeat("7", 64), RawToken: "rotated-token", MailMessageID: "mail_inv_resend_2",
			MailLocale: "en", Now: now, ExpiresAt: now.Add(7 * 24 * time.Hour),
		})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if resent.State != "pending" || resent.TokenGeneration != 2 || !resent.ExpiresAt.Equal(now.Add(7*24*time.Hour)) {
		t.Fatalf("resent=%+v", resent)
	}
	var token string
	if err := api.db.Pool().QueryRow(context.Background(), `select payload->>'token' from platform_mail_outbox where id='mail_inv_resend_2'`).Scan(&token); err != nil {
		t.Fatal(err)
	}
	if token != "rotated-token" {
		t.Fatalf("resent mail token=%q", token)
	}
}

func TestInvitationHTTPJourneyCreatesMailAndAcceptsMatchingUser(t *testing.T) {
	api := newTestAPI(t)
	owner, ownerCSRF := api.registerWithCSRF(t, "http-invite-owner@example.com")
	recipient, recipientCSRF := api.registerWithCSRF(t, "http-invite-viewer@example.com")
	notebookID := createSourceTestNotebook(t, api, owner, "http-invite")

	created := api.postJSONWithCookieAndCSRF(t, "/api/v1/notebooks/"+notebookID+"/invitations", map[string]any{
		"email": " HTTP-Invite-Viewer@Example.com ", "role": "viewer", "locale": "en",
	}, owner, ownerCSRF, ownerCSRF.Value, "http-invite-create")
	if created.Code != 201 {
		t.Fatalf("create Invitation status=%d body=%s", created.Code, created.Body.String())
	}
	if strings.Contains(created.Body.String(), `"token":`) {
		t.Fatalf("Invitation response exposed token: %s", created.Body.String())
	}

	var rawToken string
	if err := api.db.Pool().QueryRow(context.Background(), `
		select payload->>'token' from platform_mail_outbox
		where recipient_email='http-invite-viewer@example.com'`).Scan(&rawToken); err != nil {
		t.Fatal(err)
	}
	if rawToken == "" {
		t.Fatal("mail Outbox token is empty")
	}
	resolved := api.getWithCookie(t, "/api/v1/invitations/resolve?token="+rawToken, nil)
	if resolved.Code != 200 || strings.Contains(strings.ToLower(resolved.Body.String()), "http-invite-viewer@example.com") || !strings.Contains(resolved.Body.String(), "h***@example.com") {
		t.Fatalf("resolve status=%d body=%s", resolved.Code, resolved.Body.String())
	}

	accepted := api.postJSONWithCookieAndCSRF(t, "/api/v1/invitations/accept", map[string]any{
		"token": rawToken,
	}, recipient, recipientCSRF, recipientCSRF.Value, "http-invite-accept")
	if accepted.Code != 200 {
		t.Fatalf("accept Invitation status=%d body=%s", accepted.Code, accepted.Body.String())
	}
	opened := api.getWithCookie(t, "/api/v1/notebooks/"+notebookID, recipient)
	if opened.Code != 200 {
		t.Fatalf("accepted shared Notebook status=%d body=%s", opened.Code, opened.Body.String())
	}
}

func TestInvitationCapacityCountsAllMembersBehindRLS(t *testing.T) {
	api := newTestAPI(t)
	owner := api.register(t, "capacity-owner@example.com")
	notebookID := createSourceTestNotebook(t, api, owner, "invite-capacity")
	ownerID := sourceTestUserID(t, api, "capacity-owner@example.com")
	for index := 0; index < 50; index++ {
		userID := fmt.Sprintf("usr_capacity_%02d", index)
		email := fmt.Sprintf("capacity-%02d@example.com", index)
		if _, err := api.db.Pool().Exec(context.Background(), `
			with inserted as (
				insert into identity_users(id,canonical_email,display_email) values($1,$2,$2)
				returning id
			)
			insert into notebook_memberships(notebook_id,user_id,role)
			select $3,id,'viewer' from inserted`, userID, email, notebookID); err != nil {
			t.Fatal(err)
		}
	}
	now := time.Date(2026, 7, 21, 10, 0, 0, 0, time.UTC)
	err := api.db.WithRequestPrincipal(context.Background(), ownerID, func(tx pgx.Tx) error {
		_, _, err := notebook.NewStore(tx).CreateInvitation(context.Background(), notebook.CreateInvitationCommand{
			ID: "inv_over_capacity", NotebookID: notebookID, InvitedByUserID: ownerID,
			CanonicalEmail: "overflow@example.com", DisplayEmail: "overflow@example.com", Role: "viewer",
			TokenHash: strings.Repeat("e", 64), IdempotencyKey: "over-capacity", RequestHash: strings.Repeat("f", 64),
			MailMessageID: "mail_over_capacity", MailLocale: "en", RawToken: "over-capacity-token",
			Now: now, ExpiresAt: now.Add(7 * 24 * time.Hour),
		})
		return err
	})
	if !errors.Is(err, notebook.ErrMemberCapacity) {
		t.Fatalf("51st reserved slot error=%v, want member capacity", err)
	}
}

func TestExistingMemberCannotBeInvitedAgain(t *testing.T) {
	api := newTestAPI(t)
	owner := api.register(t, "duplicate-member-owner@example.com")
	api.register(t, "duplicate-member-viewer@example.com")
	notebookID := createSourceTestNotebook(t, api, owner, "invite-existing-member")
	ownerID := sourceTestUserID(t, api, "duplicate-member-owner@example.com")
	viewerID := sourceTestUserID(t, api, "duplicate-member-viewer@example.com")
	if _, err := api.db.Pool().Exec(context.Background(), `
		insert into notebook_memberships(notebook_id,user_id,role) values($1,$2,'viewer')`, notebookID, viewerID); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 7, 21, 11, 0, 0, 0, time.UTC)
	err := api.db.WithRequestPrincipal(context.Background(), ownerID, func(tx pgx.Tx) error {
		_, _, err := notebook.NewStore(tx).CreateInvitation(context.Background(), notebook.CreateInvitationCommand{
			ID: "inv_existing_member", NotebookID: notebookID, InvitedByUserID: ownerID,
			CanonicalEmail: "duplicate-member-viewer@example.com", DisplayEmail: "duplicate-member-viewer@example.com", Role: "viewer",
			TokenHash: strings.Repeat("1", 64), IdempotencyKey: "invite-existing-member", RequestHash: strings.Repeat("2", 64),
			MailMessageID: "mail_existing_member", MailLocale: "en", RawToken: "existing-member-token",
			Now: now, ExpiresAt: now.Add(7 * 24 * time.Hour),
		})
		return err
	})
	if !errors.Is(err, notebook.ErrInvitationConflict) {
		t.Fatalf("existing Member Invitation error=%v, want conflict", err)
	}
}

func TestMembershipRoleTransferRemoveAndLeaveLifecycle(t *testing.T) {
	api := newTestAPI(t)
	owner := api.register(t, "member-life-owner@example.com")
	api.register(t, "member-life-editor@example.com")
	api.register(t, "member-life-viewer@example.com")
	notebookID := createSourceTestNotebook(t, api, owner, "member-life")
	ownerID := sourceTestUserID(t, api, "member-life-owner@example.com")
	editorID := sourceTestUserID(t, api, "member-life-editor@example.com")
	viewerID := sourceTestUserID(t, api, "member-life-viewer@example.com")
	if _, err := api.db.Pool().Exec(context.Background(), `
		insert into notebook_memberships(notebook_id,user_id,role) values($1,$2,'viewer'),($1,$3,'viewer')`,
		notebookID, editorID, viewerID); err != nil {
		t.Fatal(err)
	}
	if _, err := api.db.Pool().Exec(context.Background(), `
		insert into chat_chats(id,notebook_id,creator_user_id,title) values
			('chat_member_editor',$1,$2,'private former owner'),('chat_member_viewer',$1,$3,'private viewer')`,
		notebookID, ownerID, viewerID); err != nil {
		t.Fatal(err)
	}

	if err := api.db.WithRequestPrincipal(context.Background(), ownerID, func(tx pgx.Tx) error {
		store := notebook.NewStore(tx)
		if err := store.ChangeMemberRole(context.Background(), notebookID, ownerID, editorID, "editor"); err != nil {
			return err
		}
		return store.TransferOwnership(context.Background(), notebookID, ownerID, editorID)
	}); err != nil {
		t.Fatal(err)
	}
	var oldRole, newRole string
	if err := api.db.Pool().QueryRow(context.Background(), `
		select max(role) filter(where user_id=$2), max(role) filter(where user_id=$3)
		from notebook_memberships where notebook_id=$1`, notebookID, ownerID, editorID).Scan(&oldRole, &newRole); err != nil {
		t.Fatal(err)
	}
	if oldRole != "editor" || newRole != "owner" {
		t.Fatalf("transfer roles old=%q new=%q", oldRole, newRole)
	}

	if err := api.db.WithRequestPrincipal(context.Background(), editorID, func(tx pgx.Tx) error {
		return notebook.NewStore(tx).RemoveMember(context.Background(), notebookID, editorID, viewerID)
	}); err != nil {
		t.Fatal(err)
	}
	if err := api.db.WithRequestPrincipal(context.Background(), ownerID, func(tx pgx.Tx) error {
		return notebook.NewStore(tx).Leave(context.Background(), notebookID, ownerID)
	}); err != nil {
		t.Fatal(err)
	}
	var viewerMemberships, privateChats int
	if err := api.db.Pool().QueryRow(context.Background(), `select count(*) from notebook_memberships where notebook_id=$1 and user_id=$2`, notebookID, viewerID).Scan(&viewerMemberships); err != nil {
		t.Fatal(err)
	}
	if err := api.db.Pool().QueryRow(context.Background(), `select count(*) from chat_chats where id in ('chat_member_editor','chat_member_viewer')`).Scan(&privateChats); err != nil {
		t.Fatal(err)
	}
	if viewerMemberships != 0 || privateChats != 0 {
		t.Fatalf("after remove/leave viewer_memberships=%d private_chats=%d", viewerMemberships, privateChats)
	}
}

func TestOwnerRenamesAndDeletesSharedNotebookWithNotification(t *testing.T) {
	api := newTestAPI(t)
	owner := api.register(t, "notebook-life-owner@example.com")
	api.register(t, "notebook-life-viewer@example.com")
	notebookID := createSourceTestNotebook(t, api, owner, "notebook-life")
	ownerID := sourceTestUserID(t, api, "notebook-life-owner@example.com")
	viewerID := sourceTestUserID(t, api, "notebook-life-viewer@example.com")
	if _, err := api.db.Pool().Exec(context.Background(), `insert into notebook_memberships(notebook_id,user_id,role) values($1,$2,'viewer')`, notebookID, viewerID); err != nil {
		t.Fatal(err)
	}
	if err := api.db.WithRequestPrincipal(context.Background(), ownerID, func(tx pgx.Tx) error {
		store := notebook.NewStore(tx)
		if _, err := store.Rename(context.Background(), notebookID, ownerID, "Renamed shared research"); err != nil {
			return err
		}
		return store.Delete(context.Background(), notebook.DeleteCommand{
			NotebookID: notebookID, UserID: ownerID, Locale: "en", Now: time.Now().UTC(),
			NotificationIDs: map[string]string{viewerID: "mail_notebook_deleted"},
		})
	}); err != nil {
		t.Fatal(err)
	}
	var notebookCount, notificationCount int
	if err := api.db.Pool().QueryRow(context.Background(), `select count(*) from notebook_notebooks where id=$1`, notebookID).Scan(&notebookCount); err != nil {
		t.Fatal(err)
	}
	if err := api.db.Pool().QueryRow(context.Background(), `select count(*) from platform_mail_outbox where id='mail_notebook_deleted' and kind='notebook_deleted' and recipient_email='notebook-life-viewer@example.com'`).Scan(&notificationCount); err != nil {
		t.Fatal(err)
	}
	if notebookCount != 0 || notificationCount != 1 {
		t.Fatalf("after delete notebooks=%d notifications=%d", notebookCount, notificationCount)
	}
}

func TestMembershipAndNotebookLifecycleHTTPAuthority(t *testing.T) {
	api := newTestAPI(t)
	owner, ownerCSRF := api.registerWithCSRF(t, "http-life-owner@example.com")
	viewer, viewerCSRF := api.registerWithCSRF(t, "http-life-viewer@example.com")
	notebookID := createSourceTestNotebook(t, api, owner, "http-life")
	viewerID := sourceTestUserID(t, api, "http-life-viewer@example.com")
	if _, err := api.db.Pool().Exec(context.Background(), `insert into notebook_memberships(notebook_id,user_id,role) values($1,$2,'viewer')`, notebookID, viewerID); err != nil {
		t.Fatal(err)
	}
	if response := api.getWithCookie(t, "/api/v1/notebooks/"+notebookID+"/members", owner); response.Code != http.StatusOK {
		t.Fatalf("owner members status=%d body=%s", response.Code, response.Body.String())
	}
	if response := api.getWithCookie(t, "/api/v1/notebooks/"+notebookID+"/members", viewer); response.Code != http.StatusNotFound {
		t.Fatalf("viewer members status=%d body=%s", response.Code, response.Body.String())
	}
	changed := sharingJSONRequest(t, api, http.MethodPatch, "/api/v1/notebooks/"+notebookID+"/members/"+viewerID, map[string]any{"role": "editor"}, owner, ownerCSRF)
	if changed.Code != http.StatusOK {
		t.Fatalf("role change status=%d body=%s", changed.Code, changed.Body.String())
	}
	renamed := sharingJSONRequest(t, api, http.MethodPatch, "/api/v1/notebooks/"+notebookID, map[string]any{"title": "HTTP renamed"}, owner, ownerCSRF)
	if renamed.Code != http.StatusOK || !strings.Contains(renamed.Body.String(), "HTTP renamed") {
		t.Fatalf("rename status=%d body=%s", renamed.Code, renamed.Body.String())
	}
	left := sharingJSONRequest(t, api, http.MethodPost, "/api/v1/notebooks/"+notebookID+"/leave", map[string]any{}, viewer, viewerCSRF)
	if left.Code != http.StatusNoContent {
		t.Fatalf("leave status=%d body=%s", left.Code, left.Body.String())
	}
}

func sharingJSONRequest(t *testing.T, api *testAPI, method, path string, payload map[string]any, cookie, csrf *http.Cookie) *httptest.ResponseRecorder {
	t.Helper()
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(method, path, bytes.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-CSRF-Token", csrf.Value)
	request.AddCookie(cookie)
	request.AddCookie(csrf)
	response := httptest.NewRecorder()
	api.handler.ServeHTTP(response, request)
	return response
}

func TestMailOutboxWorkerClaimsAndScrubsDeliveredInvitation(t *testing.T) {
	api := newTestAPI(t)
	owner := api.register(t, "mail-worker-owner@example.com")
	notebookID := createSourceTestNotebook(t, api, owner, "mail-worker")
	ownerID := sourceTestUserID(t, api, "mail-worker-owner@example.com")
	now := time.Now().UTC().Add(-time.Minute)
	if err := api.db.WithRequestPrincipal(context.Background(), ownerID, func(tx pgx.Tx) error {
		_, _, err := notebook.NewStore(tx).CreateInvitation(context.Background(), notebook.CreateInvitationCommand{
			ID: "inv_mail_worker", NotebookID: notebookID, InvitedByUserID: ownerID,
			CanonicalEmail: "mail-recipient@example.com", DisplayEmail: "mail-recipient@example.com", Role: "viewer",
			TokenHash: strings.Repeat("3", 64), IdempotencyKey: "mail-worker", RequestHash: strings.Repeat("4", 64),
			MailMessageID: "mail_worker_delivery", MailLocale: "en", RawToken: "mail-worker-raw-token",
			Now: now, ExpiresAt: now.Add(7 * 24 * time.Hour),
		})
		return err
	}); err != nil {
		t.Fatal(err)
	}

	queue := mailoutbox.NewQueue(api.db.Pool(), 30*time.Second)
	delivery, ok, err := queue.Claim(context.Background())
	if err != nil || !ok {
		t.Fatalf("Claim delivery=%+v ok=%t err=%v", delivery, ok, err)
	}
	if delivery.ID != "mail_worker_delivery" || delivery.RecipientEmail != "mail-recipient@example.com" || delivery.Token != "mail-worker-raw-token" || delivery.LeaseToken == "" {
		t.Fatalf("delivery=%+v", delivery)
	}
	if err := queue.Complete(context.Background(), delivery.ID, delivery.LeaseToken); err != nil {
		t.Fatal(err)
	}
	var state string
	var hasToken bool
	if err := api.db.Pool().QueryRow(context.Background(), `
		select state, payload ? 'token' from platform_mail_outbox where id=$1`, delivery.ID).Scan(&state, &hasToken); err != nil {
		t.Fatal(err)
	}
	if state != "sent" || hasToken {
		t.Fatalf("completed mail state=%q has_token=%t", state, hasToken)
	}
}
