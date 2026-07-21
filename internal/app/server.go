package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/huangxinxinyu/nano-notebook/internal/agent"
	"github.com/huangxinxinyu/nano-notebook/internal/chat"
	"github.com/huangxinxinyu/nano-notebook/internal/collector"
	"github.com/huangxinxinyu/nano-notebook/internal/fetcher"
	"github.com/huangxinxinyu/nano-notebook/internal/identity"
	"github.com/huangxinxinyu/nano-notebook/internal/jobs"
	"github.com/huangxinxinyu/nano-notebook/internal/notebook"
	"github.com/huangxinxinyu/nano-notebook/internal/objectstore"
	"github.com/huangxinxinyu/nano-notebook/internal/replay"
	"github.com/huangxinxinyu/nano-notebook/internal/source"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

type SourceUploadStore interface {
	PresignUpload(context.Context, objectstore.UploadPolicyRequest) (objectstore.UploadPolicy, error)
	PromoteUpload(context.Context, objectstore.UploadPolicyRequest, string) (objectstore.ObjectInfo, error)
}

type SourceSnapshotStore interface {
	Put(context.Context, string, []byte) error
	Get(context.Context, string, int64) ([]byte, error)
}

type Config struct {
	CookieSecure    bool
	Version         string
	DefaultModel    string
	AgentRun        agent.RunConfig
	AdminTraces     collector.QueryClient
	ReplaySealer    *replay.Sealer
	TraceSink       agent.TraceSink
	SourceUploads   SourceUploadStore
	SourceFetcher   fetcher.SnapshotFetcher
	SourceSnapshots SourceSnapshotStore
}

type Server struct {
	cfg           Config
	db            *DB
	identity      *identity.Store
	notebookStore *notebook.Store
	mux           *http.ServeMux
	runHub        *runHub
	adminTraces   collector.QueryClient
	replaySealer  *replay.Sealer
}

func NewServer(cfg Config, db *DB) *Server {
	if cfg.Version == "" {
		cfg.Version = "dev"
	}
	if cfg.DefaultModel == "" {
		cfg.DefaultModel = "aliyun/qwen-flash"
	}
	cfg.AgentRun = normalizedRunConfig(cfg.AgentRun)
	s := &Server{cfg: cfg, db: db, identity: identity.NewStore(db.Pool()), notebookStore: notebook.NewStore(db.Pool()), mux: http.NewServeMux(), runHub: newRunHub(), adminTraces: cfg.AdminTraces, replaySealer: cfg.ReplaySealer}
	s.routes()
	return s
}

func (s *Server) Handler() http.Handler {
	return requestLogger(s.withTraceDelivery(s.mux))
}

func (s *Server) withTraceDelivery(next http.Handler) http.Handler {
	if s == nil {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sink := agent.TraceSink(agent.DiscardTraceSink{})
		if s.cfg.TraceSink != nil {
			sink = s.cfg.TraceSink
		}
		scope, err := agent.NewTraceScope(sink)
		if err != nil {
			next.ServeHTTP(w, r)
			return
		}
		ctx := agent.ContextWithTraceScope(r.Context(), scope)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func (s *Server) routes() {
	s.mux.HandleFunc("/health/live", s.healthLive)
	s.mux.HandleFunc("/health/ready", s.healthReady)
	s.mux.HandleFunc("/version", s.version)
	s.mux.HandleFunc("/api/v1/session", s.session)
	s.mux.HandleFunc("/api/v1/auth/register", s.register)
	s.mux.HandleFunc("/api/v1/auth/sign-in", s.signIn)
	s.mux.HandleFunc("/api/v1/auth/sign-out", s.signOut)
	s.mux.HandleFunc("/api/v1/notebooks", s.notebooks)
	s.mux.HandleFunc("/api/v1/notebooks/", s.notebookByID)
	s.mux.HandleFunc("/api/v1/invitations/accept", s.acceptInvitation)
	s.mux.HandleFunc("/api/v1/invitations/resolve", s.resolveInvitation)
	s.mux.HandleFunc("/api/v1/sources/", s.sourceByID)
	s.mux.HandleFunc("/api/v1/citations/", s.citationByID)
	s.mux.HandleFunc("/api/v1/source-upload-intents/", s.sourceUploadIntentByID)
	s.mux.HandleFunc("/api/v1/chats/", s.chatByID)
	s.mux.HandleFunc("/api/v1/agent-runs/", s.agentRunByID)
	s.mux.HandleFunc("/api/admin/traces", s.adminTraceList)
	s.mux.HandleFunc("/api/admin/traces/", s.adminTraceByID)
}

func (s *Server) citationByID(w http.ResponseWriter, r *http.Request) {
	user, ok := s.currentUser(r)
	if !ok {
		writeError(w, r, http.StatusUnauthorized, "unauthorized", "error.session_expired")
		return
	}
	if r.Method != http.MethodGet {
		writeError(w, r, http.StatusMethodNotAllowed, "method_not_allowed", "error.method_not_allowed")
		return
	}
	citationID := strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/v1/citations/"), "/")
	if citationID == "" || strings.Contains(citationID, "/") {
		writeError(w, r, http.StatusNotFound, "not_found", "error.citation_not_found")
		return
	}
	var view agent.CitationView
	err := s.db.WithRequestPrincipal(r.Context(), user.ID, func(tx pgx.Tx) error {
		var err error
		view, err = agent.NewStore(tx).CitationViewForUser(r.Context(), user.ID, citationID)
		return err
	})
	if errors.Is(err, agent.ErrCitationNotFound) {
		writeError(w, r, http.StatusNotFound, "not_found", "error.citation_not_found")
		return
	}
	if errors.Is(err, agent.ErrCitationUnavailable) {
		writeError(w, r, http.StatusGone, "citation_unavailable", "error.citation_unavailable")
		return
	}
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "internal", "error.internal")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"citation": view})
}

func (s *Server) NotifyRun(runID string) {
	if s != nil && s.runHub != nil {
		s.runHub.notify(runID)
	}
}

func (s *Server) healthLive(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"status": "live", "service": "control-plane"})
}

func (s *Server) healthReady(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	if s.db == nil || s.db.pool == nil || s.db.pool.Ping(ctx) != nil {
		writeError(w, r, http.StatusServiceUnavailable, "not_ready", "error.control_plane_unavailable")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "ready", "service": "control-plane"})
}

func (s *Server) version(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"version": s.cfg.Version})
}

func (s *Server) session(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, r, http.StatusMethodNotAllowed, "method_not_allowed", "error.method_not_allowed")
		return
	}
	cookie, err := r.Cookie(sessionCookieName)
	if err != nil || cookie.Value == "" {
		writeError(w, r, http.StatusUnauthorized, "session_missing", "error.session_missing")
		return
	}
	user, ok := s.currentUser(r)
	if !ok {
		writeError(w, r, http.StatusUnauthorized, "session_expired", "error.session_expired")
		return
	}
	var scopedUser identity.User
	err = s.withRequestPrincipal(r.Context(), user.ID, func(identityStore *identity.Store, _ *notebook.Store) error {
		var ok bool
		scopedUser, ok = identityStore.UserByID(r.Context(), user.ID)
		if !ok {
			return identity.ErrMissingUser
		}
		return nil
	})
	if err != nil {
		writeError(w, r, http.StatusUnauthorized, "session_expired", "error.session_expired")
		return
	}
	capabilities, err := s.platformCapabilities(r.Context(), user.ID)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "internal", "error.internal")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"user": scopedUser, "platform_capabilities": capabilities})
}

func (s *Server) register(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, r, http.StatusMethodNotAllowed, "method_not_allowed", "error.method_not_allowed")
		return
	}
	var req struct {
		Email    string `json:"email"`
		Password string `json:"password"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	email := canonicalEmail(req.Email)
	if !strings.Contains(email, "@") {
		writeError(w, r, http.StatusBadRequest, "validation_failed", "error.email_invalid")
		return
	}
	if err := validatePassword(req.Password); err != nil {
		writeError(w, r, http.StatusBadRequest, "validation_failed", "error.password_policy")
		return
	}
	passwordHash, err := hashPassword(req.Password)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "internal", "error.internal")
		return
	}
	userID, err := newOpaqueID("usr")
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "internal", "error.internal")
		return
	}
	err = s.identity.RegisterLocalUser(r.Context(), userID, email, strings.TrimSpace(req.Email), passwordHash)
	if errors.Is(err, identity.ErrDuplicateEmail) {
		writeError(w, r, http.StatusConflict, "duplicate_email", "error.registration_unavailable")
		return
	}
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "internal", "error.internal")
		return
	}
	if !s.issueSession(w, r, userID) {
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"user": identity.User{ID: userID, Email: email}})
}

func (s *Server) signIn(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, r, http.StatusMethodNotAllowed, "method_not_allowed", "error.method_not_allowed")
		return
	}
	var req struct {
		Email    string `json:"email"`
		Password string `json:"password"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	email := canonicalEmail(req.Email)
	limited, err := s.rateLimited(r.Context(), email)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "internal", "error.internal")
		return
	}
	if limited {
		writeError(w, r, http.StatusTooManyRequests, "rate_limited", "error.rate_limited")
		return
	}
	userID, passwordHash, err := s.identity.LocalCredential(r.Context(), email)
	if err != nil || !verifyPassword(passwordHash, req.Password) {
		_ = s.recordAttempt(r.Context(), email, false)
		writeError(w, r, http.StatusUnauthorized, "invalid_credentials", "error.invalid_credentials")
		return
	}
	_ = s.recordAttempt(r.Context(), email, true)
	if !s.issueSession(w, r, userID) {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"user": identity.User{ID: userID, Email: email}})
}

func (s *Server) signOut(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, r, http.StatusMethodNotAllowed, "method_not_allowed", "error.method_not_allowed")
		return
	}
	if !validCSRF(r) {
		writeError(w, r, http.StatusForbidden, "csrf_required", "error.csrf_required")
		return
	}
	cookie, err := r.Cookie(sessionCookieName)
	user, ok := s.currentUser(r)
	if err == nil && cookie.Value != "" && ok {
		if err := s.withRequestPrincipal(r.Context(), user.ID, func(identityStore *identity.Store, _ *notebook.Store) error {
			return identityStore.RevokeSession(r.Context(), hashToken(cookie.Value))
		}); err != nil {
			writeError(w, r, http.StatusInternalServerError, "internal", "error.internal")
			return
		}
	}
	http.SetCookie(w, expiredCookie(sessionCookieName, true, s.cfg.CookieSecure))
	http.SetCookie(w, expiredCookie(csrfCookieName, false, s.cfg.CookieSecure))
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) notebooks(w http.ResponseWriter, r *http.Request) {
	user, ok := s.currentUser(r)
	if !ok {
		writeError(w, r, http.StatusUnauthorized, "unauthorized", "error.session_expired")
		return
	}
	switch r.Method {
	case http.MethodGet:
		s.listNotebooks(w, r, user.ID)
	case http.MethodPost:
		if !validCSRF(r) {
			writeError(w, r, http.StatusForbidden, "csrf_required", "error.csrf_required")
			return
		}
		s.createNotebook(w, r, user.ID)
	default:
		writeError(w, r, http.StatusMethodNotAllowed, "method_not_allowed", "error.method_not_allowed")
	}
}

func (s *Server) listNotebooks(w http.ResponseWriter, r *http.Request, userID string) {
	query := strings.TrimSpace(r.URL.Query().Get("query"))
	scope := strings.TrimSpace(r.URL.Query().Get("scope"))
	if scope == "" {
		scope = "all"
	}
	if scope != "all" && scope != "owned" && scope != "shared" {
		writeError(w, r, http.StatusBadRequest, "validation_failed", "error.notebook_scope")
		return
	}
	var notebooks []notebook.Notebook
	err := s.withRequestPrincipal(r.Context(), userID, func(_ *identity.Store, notebookStore *notebook.Store) error {
		var err error
		notebooks, err = notebookStore.ListVisible(r.Context(), userID, query, scope)
		return err
	})
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "internal", "error.internal")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"notebooks": notebooks})
}

func (s *Server) createNotebook(w http.ResponseWriter, r *http.Request, userID string) {
	key := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
	if key == "" {
		writeError(w, r, http.StatusBadRequest, "idempotency_required", "error.idempotency_required")
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "bad_request", "error.bad_request")
		return
	}
	var req struct {
		Title string `json:"title"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		writeError(w, r, http.StatusBadRequest, "bad_request", "error.bad_request")
		return
	}
	title := strings.TrimSpace(req.Title)
	if title == "" || len([]rune(title)) > 160 {
		writeError(w, r, http.StatusBadRequest, "validation_failed", "error.notebook_title")
		return
	}
	hash := notebookCreateRequestHash(title)
	notebookID, err := newOpaqueID("nb")
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "internal", "error.internal")
		return
	}
	var created notebook.Notebook
	var reused bool
	err = s.withRequestPrincipal(r.Context(), userID, func(_ *identity.Store, notebookStore *notebook.Store) error {
		var err error
		created, reused, err = notebookStore.CreateOwned(r.Context(), userID, key, hash, notebookID, title)
		return err
	})
	if errors.Is(err, notebook.ErrIdempotencyMismatch) {
		writeError(w, r, http.StatusConflict, "idempotency_mismatch", "error.idempotency_mismatch")
		return
	}
	if errors.Is(err, notebook.ErrQuotaReached) {
		writeError(w, r, http.StatusConflict, "quota_reached", "error.notebook_quota")
		return
	}
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "internal", "error.internal")
		return
	}
	status := http.StatusCreated
	if reused {
		status = http.StatusOK
	}
	writeJSON(w, status, map[string]any{"notebook": created})
}

func notebookCreateRequestHash(title string) string {
	canonical, _ := json.Marshal(struct {
		Title string `json:"title"`
	}{Title: title})
	return requestHash(canonical)
}

func (s *Server) notebookByID(w http.ResponseWriter, r *http.Request) {
	user, ok := s.currentUser(r)
	if !ok {
		writeError(w, r, http.StatusUnauthorized, "unauthorized", "error.session_expired")
		return
	}
	remainder := strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/v1/notebooks/"), "/")
	parts := strings.Split(remainder, "/")
	if len(parts) == 3 && parts[0] != "" && parts[1] == "sources" && parts[2] == "urls" {
		s.createURLSource(w, r, user.ID, parts[0])
		return
	}
	if len(parts) == 2 && parts[0] != "" && parts[1] == "sources" {
		s.notebookSources(w, r, user.ID, parts[0])
		return
	}
	if len(parts) == 3 && parts[0] != "" && parts[1] == "sources" && parts[2] == "upload-intents" {
		s.createSourceUploadIntent(w, r, user.ID, parts[0])
		return
	}
	if len(parts) == 2 && parts[0] != "" && parts[1] == "chats" {
		s.notebookChats(w, r, user.ID, parts[0])
		return
	}
	if len(parts) == 2 && parts[0] != "" && parts[1] == "invitations" {
		s.notebookInvitations(w, r, user, parts[0])
		return
	}
	if len(parts) >= 3 && len(parts) <= 4 && parts[0] != "" && parts[1] == "invitations" && parts[2] != "" {
		action := ""
		if len(parts) == 4 {
			action = parts[3]
		}
		s.notebookInvitationByID(w, r, user, parts[0], parts[2], action)
		return
	}
	if len(parts) == 2 && parts[0] != "" && parts[1] == "members" {
		s.notebookMembers(w, r, user, parts[0])
		return
	}
	if len(parts) >= 3 && len(parts) <= 4 && parts[0] != "" && parts[1] == "members" && parts[2] != "" {
		action := ""
		if len(parts) == 4 {
			action = parts[3]
		}
		s.notebookMemberCommand(w, r, user, parts[0], parts[2], action)
		return
	}
	if len(parts) == 2 && parts[0] != "" && parts[1] == "leave" {
		s.leaveNotebook(w, r, user, parts[0])
		return
	}
	if len(parts) == 1 && parts[0] != "" && (r.Method == http.MethodPatch || r.Method == http.MethodDelete) {
		s.mutateNotebook(w, r, user, parts[0])
		return
	}
	if r.Method != http.MethodGet || len(parts) != 1 || parts[0] == "" {
		writeError(w, r, http.StatusMethodNotAllowed, "method_not_allowed", "error.method_not_allowed")
		return
	}
	id := parts[0]
	var notebookResult notebook.Notebook
	err := s.withRequestPrincipal(r.Context(), user.ID, func(_ *identity.Store, notebookStore *notebook.Store) error {
		var err error
		notebookResult, err = notebookStore.GetVisible(r.Context(), user.ID, id)
		return err
	})
	if err != nil {
		writeError(w, r, http.StatusNotFound, "not_found", "error.notebook_not_found")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"notebook": notebookResult})
}

func (s *Server) createSourceUploadIntent(w http.ResponseWriter, r *http.Request, userID, notebookID string) {
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
	if s.cfg.SourceUploads == nil {
		writeError(w, r, http.StatusServiceUnavailable, "source_upload_unavailable", "error.source_upload_unavailable")
		return
	}

	var req struct {
		Title         string        `json:"title"`
		Format        source.Format `json:"format"`
		MediaType     string        `json:"media_type"`
		ByteSize      int64         `json:"byte_size"`
		ContentSHA256 string        `json:"content_sha256"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	req.Title = strings.TrimSpace(req.Title)
	req.MediaType = strings.TrimSpace(req.MediaType)
	req.ContentSHA256 = strings.ToLower(strings.TrimSpace(req.ContentSHA256))
	if req.Title == "" || len([]rune(req.Title)) > 255 || !source.ValidFileAdmission(req.Title, req.Format, req.MediaType) ||
		req.ByteSize < 1 || req.ByteSize > 100*1024*1024 ||
		!validLowerSHA256(req.ContentSHA256) {
		writeError(w, r, http.StatusBadRequest, "validation_failed", "error.source_upload_invalid")
		return
	}

	intentID, err := newOpaqueID("upl")
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "internal", "error.internal")
		return
	}
	sourceID, err := newOpaqueID("src")
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "internal", "error.internal")
		return
	}
	expiresAt := time.Now().UTC().Truncate(time.Microsecond).Add(15 * time.Minute)
	objectKey := "source-upload-intents/" + intentID + "/payload"
	requestHash := sourceUploadIntentRequestHash(notebookID, req.Title, req.Format, req.MediaType, req.ByteSize, req.ContentSHA256)
	var intent source.UploadIntent
	var reused bool
	err = s.db.WithRequestPrincipal(r.Context(), userID, func(tx pgx.Tx) error {
		var createErr error
		intent, reused, createErr = source.NewStore(tx).CreateUploadIntent(r.Context(), source.CreateUploadIntentCommand{
			ID: intentID, SourceID: sourceID, NotebookID: notebookID,
			IdempotencyKey: key, RequestHash: requestHash, Title: req.Title, Format: req.Format,
			MediaType: req.MediaType, ByteSize: req.ByteSize, ContentSHA256: req.ContentSHA256,
			ObjectKey: objectKey, ExpiresAt: expiresAt,
		})
		return createErr
	})
	if errors.Is(err, source.ErrIdempotencyMismatch) {
		writeError(w, r, http.StatusConflict, "idempotency_mismatch", "error.idempotency_mismatch")
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
	upload, err := s.cfg.SourceUploads.PresignUpload(r.Context(), objectstore.UploadPolicyRequest{
		Key: intent.ObjectKey, ContentFormat: string(intent.Format), MediaType: intent.MediaType, ByteSize: intent.ByteSize,
		ContentSHA256: intent.ContentSHA256, ExpiresAt: intent.ExpiresAt,
	})
	if err != nil {
		writeError(w, r, http.StatusServiceUnavailable, "source_upload_unavailable", "error.source_upload_unavailable")
		return
	}
	status := http.StatusCreated
	if reused {
		status = http.StatusOK
	}
	writeJSON(w, status, map[string]any{"upload_intent": intent, "upload": upload})
}

func sourceUploadIntentRequestHash(notebookID, title string, format source.Format, mediaType string, byteSize int64, contentSHA256 string) string {
	canonical, _ := json.Marshal(struct {
		NotebookID    string        `json:"notebook_id"`
		Title         string        `json:"title"`
		Format        source.Format `json:"format"`
		MediaType     string        `json:"media_type"`
		ByteSize      int64         `json:"byte_size"`
		ContentSHA256 string        `json:"content_sha256"`
	}{notebookID, title, format, mediaType, byteSize, contentSHA256})
	return requestHash(canonical)
}

func validLowerSHA256(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, character := range value {
		if (character < '0' || character > '9') && (character < 'a' || character > 'f') {
			return false
		}
	}
	return true
}

func (s *Server) sourceUploadIntentByID(w http.ResponseWriter, r *http.Request) {
	user, ok := s.currentUser(r)
	if !ok {
		writeError(w, r, http.StatusUnauthorized, "unauthorized", "error.session_expired")
		return
	}
	remainder := strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/v1/source-upload-intents/"), "/")
	parts := strings.Split(remainder, "/")
	if r.Method != http.MethodPost || len(parts) != 2 || parts[0] == "" || parts[1] != "finalize" {
		writeError(w, r, http.StatusMethodNotAllowed, "method_not_allowed", "error.method_not_allowed")
		return
	}
	if !validCSRF(r) {
		writeError(w, r, http.StatusForbidden, "csrf_required", "error.csrf_required")
		return
	}
	if s.cfg.SourceUploads == nil {
		writeError(w, r, http.StatusServiceUnavailable, "source_upload_unavailable", "error.source_upload_unavailable")
		return
	}

	var intent source.UploadIntent
	err := s.db.WithRequestPrincipal(r.Context(), user.ID, func(tx pgx.Tx) error {
		var lookupErr error
		intent, lookupErr = source.NewStore(tx).UploadIntentByID(r.Context(), parts[0])
		return lookupErr
	})
	if errors.Is(err, source.ErrNotFound) {
		writeError(w, r, http.StatusNotFound, "not_found", "error.source_upload_not_found")
		return
	}
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "internal", "error.internal")
		return
	}
	now := time.Now().UTC().Truncate(time.Microsecond)
	finalObjectKey := "sources/" + intent.SourceID + "/original/" + intent.ContentSHA256
	if intent.State == source.UploadIntentPending {
		if !intent.ExpiresAt.After(now) {
			writeError(w, r, http.StatusGone, "upload_intent_expired", "error.source_upload_expired")
			return
		}
		_, err = s.cfg.SourceUploads.PromoteUpload(r.Context(), objectstore.UploadPolicyRequest{
			Key: intent.ObjectKey, ContentFormat: string(intent.Format), MediaType: intent.MediaType, ByteSize: intent.ByteSize,
			ContentSHA256: intent.ContentSHA256, ExpiresAt: intent.ExpiresAt,
		}, finalObjectKey)
		if errors.Is(err, objectstore.ErrUploadMismatch) || errors.Is(err, objectstore.ErrNotFound) {
			writeError(w, r, http.StatusConflict, "source_upload_mismatch", "error.source_upload_mismatch")
			return
		}
		if err != nil {
			writeError(w, r, http.StatusServiceUnavailable, "source_upload_unavailable", "error.source_upload_unavailable")
			return
		}
	}
	jobID, err := newOpaqueID("srcjob")
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "internal", "error.internal")
		return
	}
	var created source.Source
	var reused bool
	err = s.db.WithRequestPrincipal(r.Context(), user.ID, func(tx pgx.Tx) error {
		var finalizeErr error
		created, reused, finalizeErr = source.NewStore(tx).FinalizeUploadIntent(r.Context(), intent.ID, jobID, finalObjectKey, now)
		return finalizeErr
	})
	if errors.Is(err, source.ErrNotFound) {
		writeError(w, r, http.StatusNotFound, "not_found", "error.source_upload_not_found")
		return
	}
	if errors.Is(err, source.ErrUploadIntentExpired) {
		writeError(w, r, http.StatusGone, "upload_intent_expired", "error.source_upload_expired")
		return
	}
	if errors.Is(err, source.ErrDuplicate) {
		writeError(w, r, http.StatusConflict, "duplicate_source", "error.source_duplicate")
		return
	}
	if errors.Is(err, source.ErrQuotaReached) {
		writeError(w, r, http.StatusConflict, "quota_reached", "error.source_quota")
		return
	}
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "internal", "error.internal")
		return
	}
	status := http.StatusCreated
	if reused {
		status = http.StatusOK
	}
	writeJSON(w, status, map[string]any{"source": created})
}

func (s *Server) notebookChats(w http.ResponseWriter, r *http.Request, userID, notebookID string) {
	switch r.Method {
	case http.MethodGet:
		var chats []chat.Chat
		err := s.withChatPrincipal(r.Context(), userID, func(store *chat.Store) error {
			var err error
			chats, err = store.ListPrivate(r.Context(), userID, notebookID)
			return err
		})
		if errors.Is(err, chat.ErrNotFound) {
			writeError(w, r, http.StatusNotFound, "not_found", "error.notebook_not_found")
			return
		}
		if err != nil {
			writeError(w, r, http.StatusInternalServerError, "internal", "error.internal")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"chats": chats})
	case http.MethodPost:
		if !validCSRF(r) {
			writeError(w, r, http.StatusForbidden, "csrf_required", "error.csrf_required")
			return
		}
		key := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
		if key == "" {
			writeError(w, r, http.StatusBadRequest, "idempotency_required", "error.idempotency_required")
			return
		}
		chatID, err := newOpaqueID("chat")
		if err != nil {
			writeError(w, r, http.StatusInternalServerError, "internal", "error.internal")
			return
		}
		const title = "New chat"
		hash := requestHash([]byte(notebookID + "\x00" + title))
		var created chat.Chat
		var reused bool
		err = s.withChatPrincipal(r.Context(), userID, func(store *chat.Store) error {
			var err error
			created, reused, err = store.CreatePrivate(r.Context(), userID, notebookID, key, hash, chatID, title)
			return err
		})
		if errors.Is(err, chat.ErrIdempotencyMismatch) {
			writeError(w, r, http.StatusConflict, "idempotency_mismatch", "error.idempotency_mismatch")
			return
		}
		if errors.Is(err, chat.ErrNotFound) {
			writeError(w, r, http.StatusNotFound, "not_found", "error.notebook_not_found")
			return
		}
		if err != nil {
			writeError(w, r, http.StatusInternalServerError, "internal", "error.internal")
			return
		}
		status := http.StatusCreated
		if reused {
			status = http.StatusOK
		}
		writeJSON(w, status, map[string]any{"chat": created})
	default:
		writeError(w, r, http.StatusMethodNotAllowed, "method_not_allowed", "error.method_not_allowed")
	}
}

func (s *Server) chatByID(w http.ResponseWriter, r *http.Request) {
	user, ok := s.currentUser(r)
	if !ok {
		writeError(w, r, http.StatusUnauthorized, "unauthorized", "error.session_expired")
		return
	}
	remainder := strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/v1/chats/"), "/")
	parts := strings.Split(remainder, "/")
	if len(parts) == 1 && parts[0] != "" && r.Method == http.MethodGet {
		s.chatSnapshot(w, r, user.ID, parts[0])
		return
	}
	if len(parts) == 2 && parts[0] != "" && parts[1] == "messages" && r.Method == http.MethodPost {
		s.admitMessage(w, r, user.ID, parts[0])
		return
	}
	writeError(w, r, http.StatusMethodNotAllowed, "method_not_allowed", "error.method_not_allowed")
}

func (s *Server) chatSnapshot(w http.ResponseWriter, r *http.Request, userID, chatID string) {
	var chatResult chat.Chat
	var messages []chat.Message
	var runs []agent.RunSnapshot
	var citations []agent.CitationSnapshot
	err := s.db.WithRequestPrincipal(r.Context(), userID, func(tx pgx.Tx) error {
		chatStore := chat.NewStore(tx)
		var err error
		chatResult, err = chatStore.GetPrivate(r.Context(), userID, chatID)
		if err != nil {
			return err
		}
		messages, err = chatStore.ListMessages(r.Context(), chatID)
		if err != nil {
			return err
		}
		runs, err = agent.NewStore(tx).LatestForChat(r.Context(), userID, chatID)
		if err != nil {
			return err
		}
		citations, err = agent.NewStore(tx).CitationsForChat(r.Context(), userID, chatID)
		return err
	})
	if errors.Is(err, chat.ErrNotFound) {
		writeError(w, r, http.StatusNotFound, "not_found", "error.chat_not_found")
		return
	}
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "internal", "error.internal")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"chat": chatResult, "messages": messages, "runs": runs, "citations": citations})
}

func (s *Server) agentRunByID(w http.ResponseWriter, r *http.Request) {
	user, ok := s.currentUser(r)
	if !ok {
		writeError(w, r, http.StatusUnauthorized, "unauthorized", "error.session_expired")
		return
	}
	remainder := strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/v1/agent-runs/"), "/")
	parts := strings.Split(remainder, "/")
	if len(parts) != 2 || parts[0] == "" {
		writeError(w, r, http.StatusMethodNotAllowed, "method_not_allowed", "error.method_not_allowed")
		return
	}
	if r.Method == http.MethodGet && parts[1] == "events" {
		s.streamRun(w, r, user.ID, parts[0])
		return
	}
	if r.Method == http.MethodPost && parts[1] == "cancel" {
		s.cancelRun(w, r, user.ID, parts[0])
		return
	}
	if r.Method == http.MethodPost && parts[1] == "retry" {
		s.retryRun(w, r, user.ID, parts[0])
		return
	}
	writeError(w, r, http.StatusMethodNotAllowed, "method_not_allowed", "error.method_not_allowed")
}

func (s *Server) cancelRun(w http.ResponseWriter, r *http.Request, userID, runID string) {
	if !validCSRF(r) {
		writeError(w, r, http.StatusForbidden, "csrf_required", "error.csrf_required")
		return
	}
	var run agent.RunSnapshot
	err := s.db.WithRequestPrincipal(r.Context(), userID, func(tx pgx.Tx) error {
		var err error
		run, err = agent.NewStore(tx).Cancel(r.Context(), userID, runID)
		return err
	})
	if errors.Is(err, agent.ErrRunNotFound) {
		writeError(w, r, http.StatusNotFound, "not_found", "error.run_not_found")
		return
	}
	if errors.Is(err, agent.ErrRunNotCancellable) {
		writeError(w, r, http.StatusConflict, "run_not_cancellable", "error.run_not_cancellable")
		return
	}
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "internal", "error.internal")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"run": run})
}

func (s *Server) retryRun(w http.ResponseWriter, r *http.Request, userID, sourceRunID string) {
	if !validCSRF(r) {
		writeError(w, r, http.StatusForbidden, "csrf_required", "error.csrf_required")
		return
	}
	var req struct {
		TimeZone string `json:"time_zone"`
	}
	if r.Body != nil && r.ContentLength != 0 && !readJSON(w, r, &req) {
		return
	}
	timeZone := normalizeBrowserTimeZone(req.TimeZone)
	key := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
	if key == "" {
		writeError(w, r, http.StatusBadRequest, "idempotency_required", "error.idempotency_required")
		return
	}
	runID, err := newOpaqueID("run")
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "internal", "error.internal")
		return
	}
	jobID, err := newOpaqueID("job")
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "internal", "error.internal")
		return
	}
	var run agent.RunSnapshot
	err = s.db.WithRequestPrincipal(r.Context(), userID, func(tx pgx.Tx) error {
		var err error
		run, _, err = agent.NewStore(tx).RetryQueued(r.Context(), userID, sourceRunID, key, requestHash([]byte(sourceRunID+"\x00"+timeZone)), runID, jobID, timeZone, s.cfg.AgentRun)
		return err
	})
	if errors.Is(err, agent.ErrRunNotFound) {
		writeError(w, r, http.StatusNotFound, "not_found", "error.run_not_found")
		return
	}
	if errors.Is(err, agent.ErrRunNotRetryable) {
		writeError(w, r, http.StatusConflict, "run_not_retryable", "error.run_not_retryable")
		return
	}
	if errors.Is(err, agent.ErrRetryNotLatest) {
		writeError(w, r, http.StatusConflict, "retry_not_latest", "error.retry_not_latest")
		return
	}
	if errors.Is(err, agent.ErrActiveRun) || isUniqueViolation(err, "agent_runs_one_active_per_user_idx") || isUniqueViolation(err, "agent_runs_one_active_per_input_idx") {
		writeError(w, r, http.StatusConflict, "active_run_conflict", "error.active_run_conflict")
		return
	}
	if errors.Is(err, agent.ErrIdempotencyMismatch) {
		writeError(w, r, http.StatusConflict, "idempotency_mismatch", "error.idempotency_mismatch")
		return
	}
	if errors.Is(err, agent.ErrEvidenceSetInvalid) {
		writeError(w, r, http.StatusConflict, "evidence_set_invalid", "error.evidence_set_invalid")
		return
	}
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "internal", "error.internal")
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"run": run})
}

func (s *Server) streamRun(w http.ResponseWriter, r *http.Request, userID, runID string) {
	if _, err := s.runProjection(r.Context(), userID, runID); err != nil {
		if errors.Is(err, agent.ErrRunNotFound) {
			writeError(w, r, http.StatusNotFound, "not_found", "error.run_not_found")
		} else {
			writeError(w, r, http.StatusInternalServerError, "internal", "error.internal")
		}
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, r, http.StatusInternalServerError, "stream_unsupported", "error.internal")
		return
	}
	wake, unsubscribe := s.runHub.subscribe(runID)
	defer unsubscribe()
	projection, err := s.runProjection(r.Context(), userID, runID)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "internal", "error.internal")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	if err := writeRunEvent(w, projection); err != nil {
		return
	}
	flusher.Flush()
	if terminalRun(projection.Run.Status) {
		return
	}
	heartbeat := time.NewTicker(15 * time.Second)
	defer heartbeat.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-heartbeat.C:
			projection, err := s.runProjection(r.Context(), userID, runID)
			if err != nil {
				return
			}
			if terminalRun(projection.Run.Status) {
				if err := writeRunEvent(w, projection); err != nil {
					return
				}
				flusher.Flush()
				return
			}
			if _, err := io.WriteString(w, ": heartbeat\n\n"); err != nil {
				return
			}
			flusher.Flush()
		case <-wake:
			projection, err := s.runProjection(r.Context(), userID, runID)
			if err != nil {
				return
			}
			if err := writeRunEvent(w, projection); err != nil {
				return
			}
			flusher.Flush()
			if terminalRun(projection.Run.Status) {
				return
			}
		}
	}
}

func (s *Server) runProjection(ctx context.Context, userID, runID string) (agent.RunProjection, error) {
	var projection agent.RunProjection
	err := s.db.WithRequestPrincipal(ctx, userID, func(tx pgx.Tx) error {
		store := agent.NewStore(tx)
		if _, err := store.ExpireIfOverdue(ctx, userID, runID); err != nil {
			return err
		}
		var err error
		projection, err = store.ProjectionForUser(ctx, userID, runID)
		return err
	})
	return projection, err
}

func writeRunEvent(w io.Writer, projection agent.RunProjection) error {
	payload, err := json.Marshal(projection)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(w, "event: run\ndata: %s\n\n", payload)
	return err
}

func terminalRun(status string) bool {
	return status == "completed" || status == "failed" || status == "cancelled"
}

func (s *Server) admitMessage(w http.ResponseWriter, r *http.Request, userID, chatID string) {
	if !validCSRF(r) {
		writeError(w, r, http.StatusForbidden, "csrf_required", "error.csrf_required")
		return
	}
	var req struct {
		ID        string   `json:"id"`
		Content   string   `json:"content"`
		TimeZone  string   `json:"time_zone"`
		SourceIDs []string `json:"source_ids"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	if _, err := uuid.Parse(req.ID); err != nil || len(req.ID) != 36 || strings.TrimSpace(req.Content) == "" || len([]rune(req.Content)) > 8000 || len(req.SourceIDs) > 50 {
		writeError(w, r, http.StatusBadRequest, "validation_failed", "error.message_invalid")
		return
	}
	for index := range req.SourceIDs {
		req.SourceIDs[index] = strings.TrimSpace(req.SourceIDs[index])
		if req.SourceIDs[index] == "" {
			writeError(w, r, http.StatusBadRequest, "validation_failed", "error.message_invalid")
			return
		}
	}
	runID, err := newOpaqueID("run")
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "internal", "error.internal")
		return
	}
	jobID, err := newOpaqueID("job")
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "internal", "error.internal")
		return
	}
	status := "queued"
	promptVersion := agent.BarePromptVersion
	if len(req.SourceIDs) > 0 {
		promptVersion = agent.GroundedPromptVersion
	}
	err = s.db.WithRequestPrincipal(r.Context(), userID, func(tx pgx.Tx) error {
		if _, err := tx.Exec(r.Context(), `select pg_advisory_xact_lock(hashtextextended($1, 0))`, "admit_agent_run:"+userID); err != nil {
			return err
		}
		chatStore := chat.NewStore(tx)
		if _, err := chatStore.GetPrivate(r.Context(), userID, chatID); err != nil {
			return err
		}
		existing, found, err := chatStore.MessageByID(r.Context(), req.ID)
		if err != nil {
			return err
		}
		if found {
			if existing.ChatID != chatID || existing.Role != "user" || existing.Content != req.Content {
				return chat.ErrMessageConflict
			}
			run, err := agent.NewStore(tx).ByInputMessage(r.Context(), req.ID)
			if err != nil {
				return err
			}
			matches, err := agent.NewStore(tx).EvidenceSetMatches(r.Context(), run.ID, req.SourceIDs)
			if err != nil {
				return err
			}
			if !matches {
				return chat.ErrMessageConflict
			}
			runID = run.ID
			status = run.Status
			return nil
		}
		agentStore := agent.NewStore(tx)
		if _, err := agentStore.ExpireIfOverdue(r.Context(), userID, ""); err != nil {
			return err
		}
		if _, active, err := agentStore.ActiveByUser(r.Context(), userID); err != nil {
			return err
		} else if active {
			return agent.ErrActiveRun
		}
		if err := chatStore.InsertUserMessage(r.Context(), req.ID, chatID, req.Content); err != nil {
			return err
		}
		if err := agentStore.CreateQueued(r.Context(), runID, userID, chatID, req.ID, s.cfg.DefaultModel, promptVersion, normalizeBrowserTimeZone(req.TimeZone), s.cfg.AgentRun); err != nil {
			return err
		}
		if err := agentStore.PinEvidenceSet(r.Context(), runID, userID, req.SourceIDs); err != nil {
			return err
		}
		if err := jobs.NewStore(tx).CreateAgentRun(r.Context(), jobID, runID); err != nil {
			return err
		}
		if err := agent.StartRunTraceInTx(r.Context(), tx, runID, s.cfg.DefaultModel, promptVersion, nil); err != nil {
			return err
		}
		_, err = tx.Exec(r.Context(), `select pg_notify('nano_agent_jobs', $1)`, jobID)
		return err
	})
	if errors.Is(err, chat.ErrNotFound) {
		writeError(w, r, http.StatusNotFound, "not_found", "error.chat_not_found")
		return
	}
	if errors.Is(err, chat.ErrMessageConflict) || isUniqueViolation(err, "chat_messages_pkey") {
		writeError(w, r, http.StatusConflict, "message_id_conflict", "error.message_id_conflict")
		return
	}
	if errors.Is(err, agent.ErrActiveRun) || isUniqueViolation(err, "agent_runs_one_active_per_user_idx") {
		writeError(w, r, http.StatusConflict, "active_run_conflict", "error.active_run_conflict")
		return
	}
	if errors.Is(err, agent.ErrEvidenceSetInvalid) {
		writeError(w, r, http.StatusConflict, "evidence_set_invalid", "error.evidence_set_invalid")
		return
	}
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "internal", "error.internal")
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"message_id": req.ID, "run_id": runID, "status": status})
}

func normalizeBrowserTimeZone(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "UTC"
	}
	if _, err := time.LoadLocation(value); err != nil {
		return "UTC"
	}
	return value
}

func normalizedRunConfig(value agent.RunConfig) agent.RunConfig {
	if value.ID == "" {
		value.ID = "nano-interactive-v1"
	}
	if value.ActionDecisionLimit <= 0 {
		value.ActionDecisionLimit = 4
	}
	if value.FinalDecisionLimit <= 0 {
		value.FinalDecisionLimit = 1
	}
	if value.ActionLimit <= 0 {
		value.ActionLimit = 8
	}
	if value.ActionBatchLimit <= 0 {
		value.ActionBatchLimit = 4
	}
	if value.ActionResultByteLimit <= 0 {
		value.ActionResultByteLimit = 16 * 1024
	}
	if value.ActionResultsByteLimit <= 0 {
		value.ActionResultsByteLimit = 64 * 1024
	}
	if value.Deadline <= 0 {
		value.Deadline = 10 * time.Minute
	}
	return value
}

func isUniqueViolation(err error, constraint string) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505" && pgErr.ConstraintName == constraint
}

func (s *Server) issueSession(w http.ResponseWriter, r *http.Request, userID string) bool {
	token, err := newToken()
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "internal", "error.internal")
		return false
	}
	sessionID, err := newOpaqueID("ses")
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "internal", "error.internal")
		return false
	}
	expires := time.Now().UTC().Add(24 * time.Hour)
	if err := s.identity.CreateSession(r.Context(), sessionID, userID, hashToken(token), expires); err != nil {
		writeError(w, r, http.StatusInternalServerError, "internal", "error.internal")
		return false
	}
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   s.cfg.CookieSecure,
		Expires:  expires,
	})
	csrfToken, err := newToken()
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "internal", "error.internal")
		return false
	}
	http.SetCookie(w, &http.Cookie{
		Name:     csrfCookieName,
		Value:    csrfToken,
		Path:     "/",
		HttpOnly: false,
		SameSite: http.SameSiteLaxMode,
		Secure:   s.cfg.CookieSecure,
		Expires:  expires,
	})
	return true
}

func (s *Server) currentUser(r *http.Request) (identity.User, bool) {
	cookie, err := r.Cookie(sessionCookieName)
	if err != nil || cookie.Value == "" {
		return identity.User{}, false
	}
	return s.identity.CurrentUser(r.Context(), hashToken(cookie.Value))
}

func (s *Server) withRequestPrincipal(ctx context.Context, userID string, fn func(*identity.Store, *notebook.Store) error) error {
	return s.db.WithRequestPrincipal(ctx, userID, func(tx pgx.Tx) error {
		identityStore := identity.NewStore(tx)
		notebookStore := notebook.NewStore(tx)
		return fn(identityStore, notebookStore)
	})
}

func (s *Server) withChatPrincipal(ctx context.Context, userID string, fn func(*chat.Store) error) error {
	return s.db.WithRequestPrincipal(ctx, userID, func(tx pgx.Tx) error {
		return fn(chat.NewStore(tx))
	})
}

func (s *Server) rateLimited(ctx context.Context, email string) (bool, error) {
	return s.identity.RateLimited(ctx, email)
}

func (s *Server) recordAttempt(ctx context.Context, email string, succeeded bool) error {
	return s.identity.RecordAttempt(ctx, email, succeeded)
}

const sessionCookieName = "nn_session"
const csrfCookieName = "nn_csrf"

func validCSRF(r *http.Request) bool {
	cookie, err := r.Cookie(csrfCookieName)
	if err != nil || cookie.Value == "" {
		return false
	}
	header := r.Header.Get("X-CSRF-Token")
	return header != "" && subtleConstantEqual(header, cookie.Value)
}

func expiredCookie(name string, httpOnly bool, secure bool) *http.Cookie {
	return &http.Cookie{Name: name, Value: "", Path: "/", MaxAge: -1, HttpOnly: httpOnly, SameSite: http.SameSiteLaxMode, Secure: secure}
}

func readJSON(w http.ResponseWriter, r *http.Request, target any) bool {
	defer r.Body.Close()
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(target); err != nil {
		writeError(w, r, http.StatusBadRequest, "bad_request", "error.bad_request")
		return false
	}
	return true
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

func writeError(w http.ResponseWriter, r *http.Request, status int, code, messageKey string) {
	requestID := requestIDFrom(r)
	writeJSON(w, status, map[string]any{
		"error": map[string]any{
			"code":        code,
			"message_key": messageKey,
			"request_id":  requestID,
		},
	})
}

func requestLogger(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestID := r.Header.Get("X-Request-ID")
		if requestID == "" {
			requestID = fmt.Sprintf("req_%d", time.Now().UnixNano())
		}
		ctx := context.WithValue(r.Context(), requestIDKey{}, requestID)
		slog.Info("request", "request_id", requestID, "method", r.Method, "path", r.URL.Path)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func requestIDFrom(r *http.Request) string {
	if v, ok := r.Context().Value(requestIDKey{}).(string); ok && v != "" {
		return v
	}
	return r.Header.Get("X-Request-ID")
}

type requestIDKey struct{}
