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

	"github.com/huangxinxinyu/nano-notebook/internal/identity"
	"github.com/huangxinxinyu/nano-notebook/internal/notebook"
	"github.com/jackc/pgx/v5"
)

type Config struct {
	CookieSecure bool
	Version      string
}

type Server struct {
	cfg           Config
	db            *DB
	identity      *identity.Store
	notebookStore *notebook.Store
	mux           *http.ServeMux
}

func NewServer(cfg Config, db *DB) *Server {
	if cfg.Version == "" {
		cfg.Version = "dev"
	}
	s := &Server{cfg: cfg, db: db, identity: identity.NewStore(db.Pool()), notebookStore: notebook.NewStore(db.Pool()), mux: http.NewServeMux()}
	s.routes()
	return s
}

func (s *Server) Handler() http.Handler {
	return requestLogger(s.mux)
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
	writeJSON(w, http.StatusOK, map[string]any{"user": scopedUser})
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
	var notebooks []notebook.Notebook
	err := s.withRequestPrincipal(r.Context(), userID, func(_ *identity.Store, notebookStore *notebook.Store) error {
		var err error
		notebooks, err = notebookStore.ListOwned(r.Context(), userID, query)
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
	hash := requestHash(body)
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

func (s *Server) notebookByID(w http.ResponseWriter, r *http.Request) {
	user, ok := s.currentUser(r)
	if !ok {
		writeError(w, r, http.StatusUnauthorized, "unauthorized", "error.session_expired")
		return
	}
	if r.Method != http.MethodGet {
		writeError(w, r, http.StatusMethodNotAllowed, "method_not_allowed", "error.method_not_allowed")
		return
	}
	id := strings.TrimPrefix(r.URL.Path, "/api/v1/notebooks/")
	var notebookResult notebook.Notebook
	err := s.withRequestPrincipal(r.Context(), user.ID, func(_ *identity.Store, notebookStore *notebook.Store) error {
		var err error
		notebookResult, err = notebookStore.GetOwned(r.Context(), user.ID, id)
		return err
	})
	if err != nil {
		writeError(w, r, http.StatusNotFound, "not_found", "error.notebook_not_found")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"notebook": notebookResult})
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
