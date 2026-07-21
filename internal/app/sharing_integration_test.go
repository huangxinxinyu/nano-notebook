package app_test

import (
	"context"
	"strings"
	"testing"

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
