package app

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/huangxinxinyu/nano-notebook/internal/identity"
	"github.com/huangxinxinyu/nano-notebook/internal/notebook"
	"github.com/jackc/pgx/v5"
)

func (s *Server) resolveInvitation(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, r, http.StatusMethodNotAllowed, "method_not_allowed", "error.method_not_allowed")
		return
	}
	token := strings.TrimSpace(r.URL.Query().Get("token"))
	if token == "" {
		writeError(w, r, http.StatusNotFound, "invitation_unavailable", "error.invitation_unavailable")
		return
	}
	var preview notebook.InvitationPreview
	err := s.db.WithRequestPrincipal(r.Context(), "", func(tx pgx.Tx) error {
		var err error
		preview, err = notebook.NewStore(tx).ResolveInvitation(r.Context(), hashToken(token))
		return err
	})
	if err != nil {
		writeError(w, r, http.StatusNotFound, "invitation_unavailable", "error.invitation_unavailable")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"invitation": preview})
}

func (s *Server) notebookInvitations(w http.ResponseWriter, r *http.Request, user identity.User, notebookID string) {
	if r.Method == http.MethodGet {
		var invitations []notebook.Invitation
		err := s.withRequestPrincipal(r.Context(), user.ID, func(_ *identity.Store, store *notebook.Store) error {
			var err error
			invitations, err = store.ListInvitations(r.Context(), notebookID, user.ID, time.Now().UTC())
			return err
		})
		if err != nil {
			writeError(w, r, http.StatusNotFound, "not_found", "error.notebook_not_found")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"invitations": invitations})
		return
	}
	if r.Method != http.MethodPost {
		writeError(w, r, http.StatusMethodNotAllowed, "method_not_allowed", "error.method_not_allowed")
		return
	}
	if !validCSRF(r) {
		writeError(w, r, http.StatusForbidden, "csrf_required", "error.csrf_required")
		return
	}
	key := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
	if key == "" || len(key) > 255 {
		writeError(w, r, http.StatusBadRequest, "idempotency_required", "error.idempotency_required")
		return
	}
	var req struct {
		Email  string `json:"email"`
		Role   string `json:"role"`
		Locale string `json:"locale"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	email := canonicalEmail(req.Email)
	role := strings.ToLower(strings.TrimSpace(req.Role))
	locale := strings.TrimSpace(req.Locale)
	if !strings.Contains(email, "@") || (role != "viewer" && role != "editor") || (locale != "en" && locale != "zh-CN") {
		writeError(w, r, http.StatusBadRequest, "validation_failed", "error.invitation_invalid")
		return
	}
	canonical, _ := json.Marshal(struct {
		Email  string `json:"email"`
		Role   string `json:"role"`
		Locale string `json:"locale"`
	}{Email: email, Role: role, Locale: locale})
	invitationID, err := newOpaqueID("inv")
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "internal", "error.internal")
		return
	}
	mailID, err := newOpaqueID("mail")
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "internal", "error.internal")
		return
	}
	token, err := newToken()
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "internal", "error.internal")
		return
	}
	now := time.Now().UTC()
	var invitation notebook.Invitation
	var reused bool
	err = s.withRequestPrincipal(r.Context(), user.ID, func(_ *identity.Store, store *notebook.Store) error {
		var createErr error
		invitation, reused, createErr = store.CreateInvitation(r.Context(), notebook.CreateInvitationCommand{
			ID: invitationID, NotebookID: notebookID, InvitedByUserID: user.ID,
			CanonicalEmail: email, DisplayEmail: strings.TrimSpace(req.Email), Role: role,
			TokenHash: hashToken(token), IdempotencyKey: key, RequestHash: requestHash(canonical),
			MailMessageID: mailID, MailLocale: locale, RawToken: token,
			Now: now, ExpiresAt: now.Add(7 * 24 * time.Hour),
		})
		return createErr
	})
	switch {
	case errors.Is(err, notebook.ErrNotFound):
		writeError(w, r, http.StatusNotFound, "not_found", "error.notebook_not_found")
		return
	case errors.Is(err, notebook.ErrInvitationConflict):
		writeError(w, r, http.StatusConflict, "invitation_conflict", "error.invitation_conflict")
		return
	case errors.Is(err, notebook.ErrMemberCapacity):
		writeError(w, r, http.StatusConflict, "member_capacity_reached", "error.member_capacity")
		return
	case errors.Is(err, notebook.ErrIdempotencyMismatch):
		writeError(w, r, http.StatusConflict, "idempotency_mismatch", "error.idempotency_mismatch")
		return
	case err != nil:
		writeError(w, r, http.StatusInternalServerError, "internal", "error.internal")
		return
	}
	status := http.StatusCreated
	if reused {
		status = http.StatusOK
	}
	writeJSON(w, status, map[string]any{"invitation": invitation})
}

func (s *Server) notebookInvitationByID(w http.ResponseWriter, r *http.Request, user identity.User, notebookID, invitationID, action string) {
	if !validCSRF(r) {
		writeError(w, r, http.StatusForbidden, "csrf_required", "error.csrf_required")
		return
	}
	if r.Method == http.MethodDelete && action == "" {
		err := s.withRequestPrincipal(r.Context(), user.ID, func(_ *identity.Store, store *notebook.Store) error {
			return store.RevokeInvitation(r.Context(), notebookID, invitationID, user.ID, time.Now().UTC())
		})
		if err != nil {
			writeError(w, r, http.StatusNotFound, "not_found", "error.invitation_unavailable")
			return
		}
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if r.Method == http.MethodPost && action == "resend" {
		var req struct {
			Locale string `json:"locale"`
		}
		if !readJSON(w, r, &req) || (req.Locale != "en" && req.Locale != "zh-CN") {
			return
		}
		token, tokenErr := newToken()
		mailID, mailErr := newOpaqueID("mail")
		if tokenErr != nil || mailErr != nil {
			writeError(w, r, http.StatusInternalServerError, "internal", "error.internal")
			return
		}
		now := time.Now().UTC()
		var invitation notebook.Invitation
		err := s.withRequestPrincipal(r.Context(), user.ID, func(_ *identity.Store, store *notebook.Store) error {
			var err error
			invitation, err = store.ResendInvitation(r.Context(), notebook.ResendInvitationCommand{
				InvitationID: invitationID, NotebookID: notebookID, UserID: user.ID,
				TokenHash: hashToken(token), RawToken: token, MailMessageID: mailID, MailLocale: req.Locale,
				Now: now, ExpiresAt: now.Add(7 * 24 * time.Hour),
			})
			return err
		})
		if err != nil {
			writeError(w, r, http.StatusConflict, "invitation_unavailable", "error.invitation_unavailable")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"invitation": invitation})
		return
	}
	writeError(w, r, http.StatusMethodNotAllowed, "method_not_allowed", "error.method_not_allowed")
}

func (s *Server) notebookMembers(w http.ResponseWriter, r *http.Request, user identity.User, notebookID string) {
	if r.Method != http.MethodGet {
		writeError(w, r, http.StatusMethodNotAllowed, "method_not_allowed", "error.method_not_allowed")
		return
	}
	var members []notebook.Member
	err := s.withRequestPrincipal(r.Context(), user.ID, func(_ *identity.Store, store *notebook.Store) error {
		var err error
		members, err = store.ListMembers(r.Context(), notebookID)
		return err
	})
	if err != nil {
		writeError(w, r, http.StatusNotFound, "not_found", "error.notebook_not_found")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"members": members})
}

func (s *Server) notebookMemberCommand(w http.ResponseWriter, r *http.Request, user identity.User, notebookID, targetID, action string) {
	if !validCSRF(r) {
		writeError(w, r, http.StatusForbidden, "csrf_required", "error.csrf_required")
		return
	}
	err := s.withRequestPrincipal(r.Context(), user.ID, func(_ *identity.Store, store *notebook.Store) error {
		switch {
		case r.Method == http.MethodDelete && action == "":
			return store.RemoveMember(r.Context(), notebookID, user.ID, targetID)
		case r.Method == http.MethodPost && action == "transfer":
			return store.TransferOwnership(r.Context(), notebookID, user.ID, targetID)
		case r.Method == http.MethodPatch && action == "":
			var req struct {
				Role string `json:"role"`
			}
			if !readJSON(w, r, &req) {
				return notebook.ErrInvalidMembership
			}
			return store.ChangeMemberRole(r.Context(), notebookID, user.ID, targetID, strings.ToLower(strings.TrimSpace(req.Role)))
		default:
			return notebook.ErrInvalidMembership
		}
	})
	if err != nil {
		writeError(w, r, http.StatusNotFound, "not_found", "error.notebook_not_found")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) leaveNotebook(w http.ResponseWriter, r *http.Request, user identity.User, notebookID string) {
	if r.Method != http.MethodPost || !validCSRF(r) {
		writeError(w, r, http.StatusForbidden, "forbidden", "error.csrf_required")
		return
	}
	err := s.withRequestPrincipal(r.Context(), user.ID, func(_ *identity.Store, store *notebook.Store) error {
		return store.Leave(r.Context(), notebookID, user.ID)
	})
	if err != nil {
		writeError(w, r, http.StatusConflict, "cannot_leave", "error.notebook_owner_cannot_leave")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) mutateNotebook(w http.ResponseWriter, r *http.Request, user identity.User, notebookID string) {
	if !validCSRF(r) {
		writeError(w, r, http.StatusForbidden, "csrf_required", "error.csrf_required")
		return
	}
	switch r.Method {
	case http.MethodPatch:
		var req struct {
			Title string `json:"title"`
		}
		if !readJSON(w, r, &req) {
			return
		}
		var renamed notebook.Notebook
		err := s.withRequestPrincipal(r.Context(), user.ID, func(_ *identity.Store, store *notebook.Store) error {
			var err error
			renamed, err = store.Rename(r.Context(), notebookID, user.ID, req.Title)
			return err
		})
		if err != nil {
			writeError(w, r, http.StatusNotFound, "not_found", "error.notebook_not_found")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"notebook": renamed})
	case http.MethodDelete:
		locale := r.URL.Query().Get("locale")
		if locale != "zh-CN" {
			locale = "en"
		}
		err := s.withRequestPrincipal(r.Context(), user.ID, func(_ *identity.Store, store *notebook.Store) error {
			members, err := store.ListMembers(r.Context(), notebookID)
			if err != nil {
				return err
			}
			ids := make(map[string]string, len(members)-1)
			for _, member := range members {
				if member.UserID == user.ID {
					continue
				}
				id, err := newOpaqueID("mail")
				if err != nil {
					return err
				}
				ids[member.UserID] = id
			}
			return store.Delete(r.Context(), notebook.DeleteCommand{
				NotebookID: notebookID, UserID: user.ID, Locale: locale, Now: time.Now().UTC(), NotificationIDs: ids,
			})
		})
		if err != nil {
			writeError(w, r, http.StatusNotFound, "not_found", "error.notebook_not_found")
			return
		}
		w.WriteHeader(http.StatusNoContent)
	default:
		writeError(w, r, http.StatusMethodNotAllowed, "method_not_allowed", "error.method_not_allowed")
	}
}

func (s *Server) acceptInvitation(w http.ResponseWriter, r *http.Request) {
	user, ok := s.currentUser(r)
	if !ok {
		writeError(w, r, http.StatusUnauthorized, "unauthorized", "error.session_expired")
		return
	}
	if r.Method != http.MethodPost {
		writeError(w, r, http.StatusMethodNotAllowed, "method_not_allowed", "error.method_not_allowed")
		return
	}
	if !validCSRF(r) {
		writeError(w, r, http.StatusForbidden, "csrf_required", "error.csrf_required")
		return
	}
	key := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
	if key == "" || len(key) > 255 {
		writeError(w, r, http.StatusBadRequest, "idempotency_required", "error.idempotency_required")
		return
	}
	var req struct {
		Token string `json:"token"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	token := strings.TrimSpace(req.Token)
	if token == "" {
		writeError(w, r, http.StatusBadRequest, "validation_failed", "error.invitation_invalid")
		return
	}
	canonical, _ := json.Marshal(struct {
		TokenHash string `json:"token_hash"`
	}{TokenHash: hashToken(token)})
	var membership notebook.Membership
	err := s.withRequestPrincipal(r.Context(), user.ID, func(_ *identity.Store, store *notebook.Store) error {
		var acceptErr error
		membership, acceptErr = store.AcceptInvitation(r.Context(), notebook.AcceptInvitationCommand{
			TokenHash: hashToken(token), UserID: user.ID, CanonicalEmail: user.Email,
			IdempotencyKey: key, RequestHash: requestHash(canonical), Now: time.Now().UTC(),
		})
		return acceptErr
	})
	if err != nil {
		writeError(w, r, http.StatusConflict, "invitation_unavailable", "error.invitation_unavailable")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"membership": membership})
}
