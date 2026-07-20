package app

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/huangxinxinyu/nano-notebook/internal/source"
	"github.com/jackc/pgx/v5"
)

type memberSource struct {
	ID         string        `json:"id"`
	NotebookID string        `json:"notebook_id"`
	Title      string        `json:"title"`
	Format     source.Format `json:"format"`
	ByteSize   int64         `json:"byte_size"`
	State      string        `json:"state"`
	CreatedAt  time.Time     `json:"created_at"`
	UpdatedAt  time.Time     `json:"updated_at"`
}

func sourceForMember(item source.Source) memberSource {
	state := "processing"
	if item.State == source.StateReady {
		state = "ready"
	} else if item.State == source.StateFailed {
		state = "failed"
	}
	return memberSource{
		ID: item.ID, NotebookID: item.NotebookID, Title: item.Title, Format: item.Format,
		ByteSize: item.ByteSize, State: state, CreatedAt: item.CreatedAt, UpdatedAt: item.UpdatedAt,
	}
}

func (s *Server) notebookSources(w http.ResponseWriter, r *http.Request, userID, notebookID string) {
	if r.Method != http.MethodGet {
		writeError(w, r, http.StatusMethodNotAllowed, "method_not_allowed", "error.method_not_allowed")
		return
	}
	var items []source.Source
	err := s.db.WithRequestPrincipal(r.Context(), userID, func(tx pgx.Tx) error {
		var listErr error
		items, listErr = source.NewStore(tx).ListForNotebook(r.Context(), notebookID)
		return listErr
	})
	if errors.Is(err, source.ErrNotFound) {
		writeError(w, r, http.StatusNotFound, "not_found", "error.notebook_not_found")
		return
	}
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "internal", "error.internal")
		return
	}
	result := make([]memberSource, 0, len(items))
	for _, item := range items {
		result = append(result, sourceForMember(item))
	}
	writeJSON(w, http.StatusOK, map[string]any{"sources": result})
}

func (s *Server) sourceByID(w http.ResponseWriter, r *http.Request) {
	user, ok := s.currentUser(r)
	if !ok {
		writeError(w, r, http.StatusUnauthorized, "unauthorized", "error.session_expired")
		return
	}
	remainder := strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/v1/sources/"), "/")
	parts := strings.Split(remainder, "/")
	if parts[0] == "" || len(parts) > 2 || (len(parts) == 2 && parts[1] != "retry") {
		writeError(w, r, http.StatusMethodNotAllowed, "method_not_allowed", "error.method_not_allowed")
		return
	}
	if !validCSRF(r) {
		writeError(w, r, http.StatusForbidden, "csrf_required", "error.csrf_required")
		return
	}

	var err error
	switch {
	case r.Method == http.MethodPatch && len(parts) == 1:
		var req struct {
			Title string `json:"title"`
		}
		if !readJSON(w, r, &req) {
			return
		}
		var renamed source.Source
		err = s.db.WithRequestPrincipal(r.Context(), user.ID, func(tx pgx.Tx) error {
			var renameErr error
			renamed, renameErr = source.NewStore(tx).Rename(r.Context(), parts[0], req.Title)
			return renameErr
		})
		if err == nil {
			writeJSON(w, http.StatusOK, map[string]any{"source": sourceForMember(renamed)})
			return
		}
	case r.Method == http.MethodPost && len(parts) == 2:
		err = s.db.WithRequestPrincipal(r.Context(), user.ID, func(tx pgx.Tx) error {
			return source.NewStore(tx).RetryFailed(r.Context(), parts[0])
		})
		if err == nil {
			writeJSON(w, http.StatusAccepted, map[string]any{"source_id": parts[0], "state": "processing"})
			return
		}
	case r.Method == http.MethodDelete && len(parts) == 1:
		purgeID, idErr := newOpaqueID("srcpurge")
		if idErr != nil {
			writeError(w, r, http.StatusInternalServerError, "internal", "error.internal")
			return
		}
		err = s.db.WithRequestPrincipal(r.Context(), user.ID, func(tx pgx.Tx) error {
			_, removeErr := source.NewStore(tx).Remove(r.Context(), parts[0], purgeID)
			return removeErr
		})
		if err == nil {
			w.WriteHeader(http.StatusNoContent)
			return
		}
	default:
		writeError(w, r, http.StatusMethodNotAllowed, "method_not_allowed", "error.method_not_allowed")
		return
	}

	if errors.Is(err, source.ErrNotFound) {
		writeError(w, r, http.StatusNotFound, "not_found", "error.source_not_found")
		return
	}
	if errors.Is(err, source.ErrStateConflict) {
		writeError(w, r, http.StatusConflict, "source_state_conflict", "error.source_state_conflict")
		return
	}
	if errors.Is(err, source.ErrInvalidInput) {
		writeError(w, r, http.StatusBadRequest, "validation_failed", "error.source_invalid")
		return
	}
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "internal", "error.internal")
	}
}
