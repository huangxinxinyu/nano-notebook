package app

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/huangxinxinyu/nano-notebook/internal/evidence"
	"github.com/huangxinxinyu/nano-notebook/internal/fetcher"
	"github.com/huangxinxinyu/nano-notebook/internal/source"
	"github.com/jackc/pgx/v5"
)

type memberSource struct {
	ID            string        `json:"id"`
	NotebookID    string        `json:"notebook_id"`
	Title         string        `json:"title"`
	Format        source.Format `json:"format"`
	ByteSize      int64         `json:"byte_size"`
	State         string        `json:"state"`
	FailureReason string        `json:"failure_reason,omitempty"`
	CreatedAt     time.Time     `json:"created_at"`
	UpdatedAt     time.Time     `json:"updated_at"`
}

func sourceForMember(item source.Source) memberSource {
	state := "processing"
	if item.State == source.StateReady {
		state = "ready"
	} else if item.State == source.StateFailed {
		state = "failed"
	}
	result := memberSource{
		ID: item.ID, NotebookID: item.NotebookID, Title: item.Title, Format: item.Format,
		ByteSize: item.ByteSize, State: state, CreatedAt: item.CreatedAt, UpdatedAt: item.UpdatedAt,
	}
	if state == "failed" {
		result.FailureReason = source.SafeFailureReason(item.FailureCode)
	}
	return result
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

func (s *Server) createURLSource(w http.ResponseWriter, r *http.Request, userID, notebookID string) {
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
	if s.cfg.SourceFetcher == nil || s.cfg.SourceSnapshots == nil {
		writeError(w, r, http.StatusServiceUnavailable, "source_fetch_unavailable", "error.source_fetch_unavailable")
		return
	}
	var requestBody struct {
		URL string `json:"url"`
	}
	if !readJSON(w, r, &requestBody) {
		return
	}
	requestURL, err := canonicalSourceURL(requestBody.URL)
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "validation_failed", "error.source_url_invalid")
		return
	}
	admissionID, err := newOpaqueID("urladm")
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "internal", "error.internal")
		return
	}
	sourceID, err := newOpaqueID("src")
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "internal", "error.internal")
		return
	}
	requestDigest := sourceURLRequestHash(notebookID, requestURL)
	var admission source.URLAdmission
	var reused bool
	err = s.db.WithRequestPrincipal(r.Context(), userID, func(tx pgx.Tx) error {
		var beginErr error
		admission, reused, beginErr = source.NewStore(tx).BeginURLAdmission(r.Context(), source.BeginURLAdmissionCommand{
			ID: admissionID, SourceID: sourceID, NotebookID: notebookID, IdempotencyKey: key,
			RequestHash: requestDigest, RequestURL: requestURL,
		})
		return beginErr
	})
	if errors.Is(err, source.ErrIdempotencyMismatch) {
		writeError(w, r, http.StatusConflict, "idempotency_mismatch", "error.idempotency_mismatch")
		return
	}
	if errors.Is(err, source.ErrAdmissionInProgress) {
		writeError(w, r, http.StatusConflict, "source_admission_in_progress", "error.source_admission_in_progress")
		return
	}
	if errors.Is(err, source.ErrNotFound) {
		writeError(w, r, http.StatusNotFound, "not_found", "error.notebook_not_found")
		return
	}
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "internal", "error.internal")
		return
	}
	if reused {
		var existing source.Source
		err = s.db.WithRequestPrincipal(r.Context(), userID, func(tx pgx.Tx) error {
			var lookupErr error
			existing, lookupErr = source.NewStore(tx).SourceByID(r.Context(), admission.SourceID)
			return lookupErr
		})
		if err != nil {
			writeError(w, r, http.StatusNotFound, "not_found", "error.source_not_found")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"source": sourceForMember(existing)})
		return
	}

	snapshot, err := s.cfg.SourceFetcher.Fetch(r.Context(), requestURL)
	if err != nil {
		s.failURLAdmission(r, userID, admission.ID, "fetch_failed")
		switch {
		case errors.Is(err, fetcher.ErrUnsafeDestination):
			writeError(w, r, http.StatusUnprocessableEntity, "unsafe_destination", "error.source_url_unsafe")
		case errors.Is(err, fetcher.ErrResponseTooLarge):
			writeError(w, r, http.StatusRequestEntityTooLarge, "source_too_large", "error.source_too_large")
		case errors.Is(err, fetcher.ErrUnsupportedType):
			writeError(w, r, http.StatusUnsupportedMediaType, "unsupported_source", "error.source_unsupported")
		default:
			writeError(w, r, http.StatusBadGateway, "source_fetch_failed", "error.source_fetch_failed")
		}
		return
	}
	digest := sha256.Sum256(snapshot.Payload)
	if len(snapshot.Payload) == 0 || int64(len(snapshot.Payload)) > 100*1024*1024 ||
		!strings.EqualFold(snapshot.ContentSHA256, hex.EncodeToString(digest[:])) {
		s.failURLAdmission(r, userID, admission.ID, "invalid_snapshot")
		writeError(w, r, http.StatusBadGateway, "source_fetch_failed", "error.source_fetch_failed")
		return
	}
	format, ok := source.FormatForMediaType(snapshot.MediaType)
	if !ok {
		s.failURLAdmission(r, userID, admission.ID, "unsupported_type")
		writeError(w, r, http.StatusUnsupportedMediaType, "unsupported_source", "error.source_unsupported")
		return
	}
	objectKey := "sources/" + admission.SourceID + "/original/" + strings.ToLower(snapshot.ContentSHA256)
	if err := s.cfg.SourceSnapshots.Put(r.Context(), objectKey, snapshot.Payload); err != nil {
		s.failURLAdmission(r, userID, admission.ID, "object_write_failed")
		writeError(w, r, http.StatusServiceUnavailable, "source_store_unavailable", "error.source_store_unavailable")
		return
	}
	jobID, err := newOpaqueID("srcjob")
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "internal", "error.internal")
		return
	}
	parsedFinalURL, _ := url.Parse(snapshot.FinalURL)
	title := parsedFinalURL.Hostname()
	if title == "" {
		title = "Web source"
	}
	var created source.Source
	err = s.db.WithRequestPrincipal(r.Context(), userID, func(tx pgx.Tx) error {
		var finalizeErr error
		created, _, finalizeErr = source.NewStore(tx).FinalizeURLAdmission(r.Context(), source.FinalizeURLAdmissionCommand{
			AdmissionID: admission.ID, ProcessingJobID: jobID, Title: title, Format: format,
			MediaType: snapshot.MediaType, ByteSize: int64(len(snapshot.Payload)),
			ContentSHA256: strings.ToLower(snapshot.ContentSHA256), OriginalObjectKey: objectKey,
			FinalURL: snapshot.FinalURL, CompletedAt: time.Now().UTC().Truncate(time.Microsecond),
		})
		return finalizeErr
	})
	if errors.Is(err, source.ErrQuotaReached) {
		writeError(w, r, http.StatusConflict, "quota_reached", "error.source_quota")
		return
	}
	if errors.Is(err, source.ErrNotFound) {
		writeError(w, r, http.StatusNotFound, "not_found", "error.notebook_not_found")
		return
	}
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "internal", "error.internal")
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"source": sourceForMember(created)})
}

func canonicalSourceURL(rawURL string) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Hostname() == "" ||
		parsed.User != nil || parsed.Fragment != "" {
		return "", source.ErrInvalidInput
	}
	parsed.Scheme = strings.ToLower(parsed.Scheme)
	parsed.Host = strings.ToLower(parsed.Host)
	return parsed.String(), nil
}

func sourceURLRequestHash(notebookID, requestURL string) string {
	canonical, _ := json.Marshal(struct {
		NotebookID string `json:"notebook_id"`
		URL        string `json:"url"`
	}{NotebookID: notebookID, URL: requestURL})
	return requestHash(canonical)
}

func (s *Server) failURLAdmission(r *http.Request, userID, admissionID, errorCode string) {
	_ = s.db.WithRequestPrincipal(r.Context(), userID, func(tx pgx.Tx) error {
		return source.NewStore(tx).FailURLAdmission(r.Context(), admissionID, errorCode, time.Now().UTC().Truncate(time.Microsecond))
	})
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
	if r.Method == http.MethodGet && len(parts) == 1 {
		var view evidence.SourceView
		err := s.db.WithRequestPrincipal(r.Context(), user.ID, func(tx pgx.Tx) error {
			var readErr error
			view, readErr = evidence.NewReader(tx).SourceView(r.Context(), parts[0])
			return readErr
		})
		switch {
		case err == nil:
			writeJSON(w, http.StatusOK, map[string]any{"source": view})
		case errors.Is(err, evidence.ErrSourceNotFound):
			writeError(w, r, http.StatusNotFound, "not_found", "error.source_not_found")
		case errors.Is(err, evidence.ErrSourceNotReady):
			writeError(w, r, http.StatusConflict, "source_not_ready", "error.source_not_ready")
		default:
			writeError(w, r, http.StatusInternalServerError, "internal", "error.internal")
		}
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
