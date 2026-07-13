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

	"github.com/jackc/pgx/v5"
)

type Config struct {
	CookieSecure bool
	Version      string
}

type Server struct {
	cfg Config
	db  *DB
	mux *http.ServeMux
}

func NewServer(cfg Config, db *DB) *Server {
	if cfg.Version == "" {
		cfg.Version = "dev"
	}
	s := &Server{cfg: cfg, db: db, mux: http.NewServeMux()}
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
	user, ok := s.currentUser(r)
	if !ok {
		writeError(w, r, http.StatusUnauthorized, "unauthorized", "error.session_expired")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"user": user})
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
	ctx := r.Context()
	tx, err := s.db.pool.Begin(ctx)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "internal", "error.internal")
		return
	}
	defer tx.Rollback(ctx)
	_, err = tx.Exec(ctx, `insert into identity_users(id, canonical_email, display_email) values($1, $2, $3)`, userID, email, strings.TrimSpace(req.Email))
	if err != nil {
		writeError(w, r, http.StatusConflict, "duplicate_email", "error.registration_unavailable")
		return
	}
	_, err = tx.Exec(ctx, `insert into identity_local_credentials(user_id, password_hash) values($1, $2)`, userID, passwordHash)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "internal", "error.internal")
		return
	}
	if err := tx.Commit(ctx); err != nil {
		writeError(w, r, http.StatusInternalServerError, "internal", "error.internal")
		return
	}
	if !s.issueSession(w, r, userID) {
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"user": publicUser{ID: userID, Email: email}})
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
	var userID, passwordHash string
	err = s.db.pool.QueryRow(r.Context(), `
		select u.id, c.password_hash
		from identity_users u
		join identity_local_credentials c on c.user_id = u.id
		where u.canonical_email = $1`, email).Scan(&userID, &passwordHash)
	if err != nil || !verifyPassword(passwordHash, req.Password) {
		_ = s.recordAttempt(r.Context(), email, false)
		writeError(w, r, http.StatusUnauthorized, "invalid_credentials", "error.invalid_credentials")
		return
	}
	_ = s.recordAttempt(r.Context(), email, true)
	if !s.issueSession(w, r, userID) {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"user": publicUser{ID: userID, Email: email}})
}

func (s *Server) signOut(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, r, http.StatusMethodNotAllowed, "method_not_allowed", "error.method_not_allowed")
		return
	}
	cookie, err := r.Cookie(sessionCookieName)
	if err == nil && cookie.Value != "" {
		_, _ = s.db.pool.Exec(r.Context(), `update identity_sessions set revoked_at = now() where token_hash = $1 and revoked_at is null`, hashToken(cookie.Value))
	}
	http.SetCookie(w, expiredCookie(sessionCookieName))
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
		if r.Header.Get("X-CSRF-Token") == "" {
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
	rows, err := s.db.pool.Query(r.Context(), `
		select n.id, n.title, n.recent_at
		from notebook_notebooks n
		join notebook_memberships m on m.notebook_id = n.id
		where m.user_id = $1
		  and m.role = 'owner'
		  and ($2 = '' or lower(n.title) like '%' || lower($2) || '%')
		order by n.recent_at desc
		limit 100`, userID, query)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "internal", "error.internal")
		return
	}
	defer rows.Close()
	notebooks := make([]map[string]any, 0)
	for rows.Next() {
		var id, title string
		var recentAt time.Time
		if err := rows.Scan(&id, &title, &recentAt); err != nil {
			writeError(w, r, http.StatusInternalServerError, "internal", "error.internal")
			return
		}
		notebooks = append(notebooks, map[string]any{"id": id, "title": title, "recent_at": recentAt})
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
	var existingHash, existingJSON string
	var existingStatus int
	err = s.db.pool.QueryRow(r.Context(), `
		select request_hash, status_code, response_json::text
		from platform_idempotency_keys
		where principal_id = $1 and action = 'create_notebook' and key = $2`, userID, key).Scan(&existingHash, &existingStatus, &existingJSON)
	if err == nil {
		if existingHash != hash {
			writeError(w, r, http.StatusConflict, "idempotency_mismatch", "error.idempotency_mismatch")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(existingJSON))
		return
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		writeError(w, r, http.StatusInternalServerError, "internal", "error.internal")
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
	var owned int
	if err := s.db.pool.QueryRow(r.Context(), `select count(*) from notebook_memberships where user_id = $1 and role = 'owner'`, userID).Scan(&owned); err != nil {
		writeError(w, r, http.StatusInternalServerError, "internal", "error.internal")
		return
	}
	if owned >= 100 {
		writeError(w, r, http.StatusConflict, "quota_reached", "error.notebook_quota")
		return
	}
	notebookID, err := newOpaqueID("nb")
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "internal", "error.internal")
		return
	}
	tx, err := s.db.pool.Begin(r.Context())
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "internal", "error.internal")
		return
	}
	defer tx.Rollback(r.Context())
	_, err = tx.Exec(r.Context(), `insert into notebook_notebooks(id, title) values($1, $2)`, notebookID, title)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "internal", "error.internal")
		return
	}
	_, err = tx.Exec(r.Context(), `insert into notebook_memberships(notebook_id, user_id, role) values($1, $2, 'owner')`, notebookID, userID)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "internal", "error.internal")
		return
	}
	response := map[string]any{"notebook": map[string]any{"id": notebookID, "title": title}}
	responseBytes, err := json.Marshal(response)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "internal", "error.internal")
		return
	}
	_, err = tx.Exec(r.Context(), `
		insert into platform_idempotency_keys(principal_id, action, key, request_hash, status_code, response_json)
		values($1, 'create_notebook', $2, $3, $4, $5::jsonb)`, userID, key, hash, http.StatusCreated, string(responseBytes))
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "internal", "error.internal")
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		writeError(w, r, http.StatusInternalServerError, "internal", "error.internal")
		return
	}
	writeJSON(w, http.StatusCreated, response)
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
	var title string
	err := s.db.pool.QueryRow(r.Context(), `
		select n.title
		from notebook_notebooks n
		join notebook_memberships m on m.notebook_id = n.id
		where n.id = $1 and m.user_id = $2 and m.role = 'owner'`, id, user.ID).Scan(&title)
	if err != nil {
		writeError(w, r, http.StatusNotFound, "not_found", "error.notebook_not_found")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"notebook": map[string]any{"id": id, "title": title}})
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
	_, err = s.db.pool.Exec(r.Context(), `
		insert into identity_sessions(id, user_id, token_hash, expires_at)
		values($1, $2, $3, $4)`, sessionID, userID, hashToken(token), expires)
	if err != nil {
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
	return true
}

func (s *Server) currentUser(r *http.Request) (publicUser, bool) {
	cookie, err := r.Cookie(sessionCookieName)
	if err != nil || cookie.Value == "" {
		return publicUser{}, false
	}
	var user publicUser
	err = s.db.pool.QueryRow(r.Context(), `
		select u.id, u.canonical_email
		from identity_sessions s
		join identity_users u on u.id = s.user_id
		where s.token_hash = $1
		  and s.revoked_at is null
		  and s.expires_at > now()`, hashToken(cookie.Value)).Scan(&user.ID, &user.Email)
	if err != nil {
		return publicUser{}, false
	}
	return user, true
}

func (s *Server) rateLimited(ctx context.Context, email string) (bool, error) {
	var failed int
	err := s.db.pool.QueryRow(ctx, `
		select count(*)
		from identity_auth_attempts
		where canonical_email = $1
		  and succeeded = false
		  and attempted_at > now() - interval '15 minutes'`, email).Scan(&failed)
	return failed >= 5, err
}

func (s *Server) recordAttempt(ctx context.Context, email string, succeeded bool) error {
	_, err := s.db.pool.Exec(ctx, `insert into identity_auth_attempts(canonical_email, succeeded) values($1, $2)`, email, succeeded)
	return err
}

type publicUser struct {
	ID    string `json:"id"`
	Email string `json:"email"`
}

const sessionCookieName = "nn_session"

func expiredCookie(name string) *http.Cookie {
	return &http.Cookie{Name: name, Value: "", Path: "/", MaxAge: -1, HttpOnly: true, SameSite: http.SameSiteLaxMode}
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
