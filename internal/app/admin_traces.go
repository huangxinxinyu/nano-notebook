package app

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/huangxinxinyu/nano-notebook/internal/agentobs"
	"github.com/huangxinxinyu/nano-notebook/internal/collector"
	"github.com/jackc/pgx/v5"
)

const (
	capabilityTraceRead   = "platform.trace.read"
	capabilityTraceReplay = "platform.trace.replay"
)

func (s *Server) platformCapabilities(ctx context.Context, userID string) ([]string, error) {
	capabilities := make([]string, 0, 2)
	err := s.db.WithRequestPrincipal(ctx, userID, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select capability from platform_capability_grants where user_id = $1 order by capability`, userID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var capability string
			if err := rows.Scan(&capability); err != nil {
				return err
			}
			capabilities = append(capabilities, capability)
		}
		return rows.Err()
	})
	return capabilities, err
}

func (s *Server) adminPrincipal(w http.ResponseWriter, r *http.Request) (string, map[string]bool, bool) {
	user, ok := s.currentUser(r)
	if !ok {
		writeError(w, r, http.StatusUnauthorized, "unauthorized", "error.session_expired")
		return "", nil, false
	}
	capabilities, err := s.platformCapabilities(r.Context(), user.ID)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "internal", "error.internal")
		return "", nil, false
	}
	grants := make(map[string]bool, len(capabilities))
	for _, capability := range capabilities {
		grants[capability] = true
	}
	if !grants[capabilityTraceRead] {
		writeError(w, r, http.StatusForbidden, "trace_forbidden", "error.trace_forbidden")
		return user.ID, grants, false
	}
	return user.ID, grants, true
}

func (s *Server) adminTraceList(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, r, http.StatusMethodNotAllowed, "method_not_allowed", "error.method_not_allowed")
		return
	}
	if _, _, ok := s.adminPrincipal(w, r); !ok {
		return
	}
	if s.adminTraces == nil {
		writeError(w, r, http.StatusServiceUnavailable, "trace_temporarily_unavailable", "error.trace_temporarily_unavailable")
		return
	}
	query, err := parseAdminTraceListQuery(r)
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_trace_query", "error.bad_request")
		return
	}
	result, err := s.adminTraces.List(r.Context(), query)
	if err != nil {
		writeAdminTraceError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"schema_version": 1, "data": result})
}

func (s *Server) adminTraceByID(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, r, http.StatusMethodNotAllowed, "method_not_allowed", "error.method_not_allowed")
		return
	}
	userID, grants, ok := s.adminPrincipal(w, r)
	if !ok {
		return
	}
	path := strings.TrimPrefix(r.URL.Path, "/api/admin/traces/")
	parts := strings.Split(path, "/")
	if len(parts) == 1 && validAdminIdentifier(parts[0], 128) {
		s.adminTraceDetail(w, r, agentobs.TraceID(parts[0]))
		return
	}
	if len(parts) == 3 && validAdminIdentifier(parts[0], 128) && parts[1] == "replay" && validAdminIdentifier(parts[2], 128) {
		s.adminTraceReplay(w, r, userID, grants, agentobs.TraceID(parts[0]), parts[2])
		return
	}
	writeError(w, r, http.StatusNotFound, "trace_not_found", "error.trace_not_found")
}

func (s *Server) adminTraceDetail(w http.ResponseWriter, r *http.Request, traceID agentobs.TraceID) {
	if s.adminTraces == nil {
		writeError(w, r, http.StatusServiceUnavailable, "trace_temporarily_unavailable", "error.trace_temporarily_unavailable")
		return
	}
	detail, err := s.adminTraces.Detail(r.Context(), traceID)
	if err != nil {
		writeAdminTraceError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"schema_version": 1, "data": detail})
}

func (s *Server) adminTraceReplay(w http.ResponseWriter, r *http.Request, userID string, grants map[string]bool, traceID agentobs.TraceID, replayID string) {
	spanID := agentobs.SpanID(strings.TrimSpace(r.URL.Query().Get("span_id")))
	if !validAdminIdentifier(string(spanID), 128) {
		writeError(w, r, http.StatusBadRequest, "invalid_trace_query", "error.bad_request")
		return
	}
	if !grants[capabilityTraceReplay] {
		if err := s.recordReplayAudit(r.Context(), userID, traceID, spanID, replayID, "", "denied", "replay_forbidden"); err != nil {
			writeError(w, r, http.StatusInternalServerError, "replay_audit_unavailable", "error.internal")
			return
		}
		writeError(w, r, http.StatusForbidden, "replay_forbidden", "error.replay_forbidden")
		return
	}
	if s.adminTraces == nil || s.replaySealer == nil {
		_ = s.recordReplayAudit(r.Context(), userID, traceID, spanID, replayID, "", "failed", "replay_unavailable")
		writeError(w, r, http.StatusServiceUnavailable, "replay_unavailable", "error.replay_unavailable")
		return
	}
	opaque, err := s.adminTraces.Replay(r.Context(), traceID, spanID, replayID)
	if err != nil {
		code := adminReplayFailureCode(err)
		if auditErr := s.recordReplayAudit(r.Context(), userID, traceID, spanID, replayID, "", "failed", code); auditErr != nil {
			writeError(w, r, http.StatusInternalServerError, "replay_audit_unavailable", "error.internal")
			return
		}
		writeAdminTraceError(w, r, err)
		return
	}
	plain, err := s.replaySealer.Open(r.Context(), opaque.Sealed)
	if err != nil || !json.Valid(plain.Bytes) {
		if auditErr := s.recordReplayAudit(r.Context(), userID, traceID, spanID, replayID, string(opaque.Class), "failed", "replay_corrupt"); auditErr != nil {
			writeError(w, r, http.StatusInternalServerError, "replay_audit_unavailable", "error.internal")
			return
		}
		writeError(w, r, http.StatusServiceUnavailable, "replay_corrupt", "error.replay_unavailable")
		return
	}
	if err := s.recordReplayAudit(r.Context(), userID, traceID, spanID, replayID, string(opaque.Class), "allowed", ""); err != nil {
		writeError(w, r, http.StatusInternalServerError, "replay_audit_unavailable", "error.internal")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, map[string]any{
		"schema_version": 1,
		"data": map[string]any{
			"replay_id": replayID, "trace_id": traceID, "span_id": spanID,
			"class": opaque.Class, "payload": json.RawMessage(plain.Bytes),
		},
	})
}

func (s *Server) recordReplayAudit(ctx context.Context, userID string, traceID agentobs.TraceID, spanID agentobs.SpanID, replayID, class, outcome, failureCode string) error {
	auditID, err := newOpaqueID("aud")
	if err != nil {
		return err
	}
	return s.db.WithRequestPrincipal(ctx, userID, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `
			insert into platform_replay_access_audit(id, operator_user_id, trace_id, span_id, replay_id, replay_class, outcome, failure_code)
			values($1,$2,$3,$4,$5,$6,$7,$8)
		`, auditID, userID, traceID, spanID, replayID, class, outcome, failureCode)
		return err
	})
}

func parseAdminTraceListQuery(r *http.Request) (collector.TraceListQuery, error) {
	values := r.URL.Query()
	query := collector.TraceListQuery{
		IdentityExact: strings.TrimSpace(values.Get("identity")), IdentityPrefix: strings.TrimSpace(values.Get("identity_prefix")),
		AgentName: strings.TrimSpace(values.Get("agent")), ModelName: strings.TrimSpace(values.Get("model")),
		Status: strings.TrimSpace(values.Get("status")), Cursor: strings.TrimSpace(values.Get("cursor")), PageSize: 50,
	}
	if value := values.Get("page_size"); value != "" {
		parsed, err := strconv.Atoi(value)
		if err != nil {
			return collector.TraceListQuery{}, err
		}
		query.PageSize = parsed
	}
	var err error
	query.StartedAfterUnixNano, err = parseAdminTime(values.Get("started_after"))
	if err != nil {
		return collector.TraceListQuery{}, err
	}
	query.StartedBeforeUnixNano, err = parseAdminTime(values.Get("started_before"))
	if err != nil {
		return collector.TraceListQuery{}, err
	}
	if values.Has("active") {
		active, parseErr := strconv.ParseBool(values.Get("active"))
		if parseErr != nil {
			return collector.TraceListQuery{}, parseErr
		}
		query.Active = &active
	}
	if query.PageSize < 1 || query.PageSize > 100 || len(query.Cursor) > 512 ||
		len(query.IdentityExact) > 128 || len(query.IdentityPrefix) > 128 || len(query.AgentName) > 160 || len(query.ModelName) > 160 || len(query.Status) > 32 {
		return collector.TraceListQuery{}, errors.New("Admin Trace query bounds are invalid")
	}
	return query, nil
}

func parseAdminTime(value string) (*int64, error) {
	if strings.TrimSpace(value) == "" {
		return nil, nil
	}
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return nil, err
	}
	nanoseconds := parsed.UnixNano()
	return &nanoseconds, nil
}

func validAdminIdentifier(value string, max int) bool {
	if value == "" || len(value) > max || strings.ContainsAny(value, "\\/?#\x00") {
		return false
	}
	return true
}

func adminReplayFailureCode(err error) string {
	switch {
	case errors.Is(err, collector.ErrReplayExpired):
		return "replay_expired"
	case errors.Is(err, collector.ErrReplayNotFound):
		return "replay_not_found"
	default:
		return "replay_unavailable"
	}
}

func writeAdminTraceError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, collector.ErrTraceNotFound):
		writeError(w, r, http.StatusNotFound, "trace_not_found", "error.trace_not_found")
	case errors.Is(err, collector.ErrProjectionPending):
		writeError(w, r, http.StatusConflict, "trace_projection_pending", "error.trace_projection_pending")
	case errors.Is(err, collector.ErrReplayExpired):
		writeError(w, r, http.StatusGone, "replay_expired", "error.replay_expired")
	case errors.Is(err, collector.ErrReplayNotFound):
		writeError(w, r, http.StatusNotFound, "replay_unavailable", "error.replay_unavailable")
	case errors.Is(err, collector.ErrReplayUnavailable):
		writeError(w, r, http.StatusServiceUnavailable, "replay_unavailable", "error.replay_unavailable")
	default:
		writeError(w, r, http.StatusServiceUnavailable, "trace_temporarily_unavailable", "error.trace_temporarily_unavailable")
	}
}
