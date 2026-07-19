package collector

import (
	"compress/gzip"
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/huangxinxinyu/nano-notebook/internal/agentobs"
)

type HTTPConfig struct {
	Ingestor     *Ingestor
	Purger       *Purger
	ServiceToken string
	MaxBodyBytes int64
	Readiness    func(context.Context) error
	QueryStore   *TraceQueryStore
	QueryToken   string
}

type httpHandler struct {
	ingestor     *Ingestor
	purger       *Purger
	serviceToken string
	maxBodyBytes int64
	readiness    func(context.Context) error
	queryStore   *TraceQueryStore
	queryToken   string
	mux          *http.ServeMux
}

func NewHTTPHandler(config HTTPConfig) (http.Handler, error) {
	if config.Ingestor == nil || strings.TrimSpace(config.ServiceToken) == "" || config.MaxBodyBytes < 1 {
		return nil, errors.New("Collector HTTP configuration is incomplete")
	}
	if config.QueryStore != nil && strings.TrimSpace(config.QueryToken) == "" {
		return nil, errors.New("Collector Query HTTP credential is required")
	}
	handler := &httpHandler{
		ingestor: config.Ingestor, serviceToken: config.ServiceToken, maxBodyBytes: config.MaxBodyBytes,
		purger: config.Purger, readiness: config.Readiness, queryStore: config.QueryStore,
		queryToken: config.QueryToken, mux: http.NewServeMux(),
	}
	handler.mux.HandleFunc("/internal/agent-observability/v1/batches", handler.batches)
	handler.mux.HandleFunc("/internal/agent-observability/v2/batches", handler.batches)
	handler.mux.HandleFunc("/internal/agent-observability/v1/purges", handler.purges)
	handler.mux.HandleFunc("/internal/agent-observability/v1/health", handler.health)
	handler.mux.HandleFunc("/internal/agent-observability/v1/traces", handler.traces)
	handler.mux.HandleFunc("/internal/agent-observability/v1/traces/", handler.traceByID)
	return handler, nil
}

func (h *httpHandler) traces(w http.ResponseWriter, r *http.Request) {
	if !h.authorizedWith(r, h.queryToken) {
		writeHTTPJSON(w, http.StatusUnauthorized, map[string]any{"error": map[string]string{"code": "unauthorized"}})
		return
	}
	if r.Method != http.MethodGet {
		writeHTTPJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": map[string]string{"code": "method_not_allowed"}})
		return
	}
	if h.queryStore == nil {
		writeHTTPJSON(w, http.StatusServiceUnavailable, map[string]any{"error": map[string]string{"code": "collector_unavailable"}})
		return
	}
	query := TraceListQuery{
		IdentityExact: r.URL.Query().Get("identity"), IdentityPrefix: r.URL.Query().Get("identity_prefix"),
		AgentName: r.URL.Query().Get("agent"), ModelName: r.URL.Query().Get("model"),
		Status: r.URL.Query().Get("status"), Cursor: r.URL.Query().Get("cursor"),
	}
	var err error
	query.PageSize, err = parseOptionalInt(r.URL.Query().Get("page_size"), 50)
	if err == nil {
		query.StartedAfterUnixNano, err = parseOptionalInt64(r.URL.Query().Get("started_after_unix_nano"))
	}
	if err == nil {
		query.StartedBeforeUnixNano, err = parseOptionalInt64(r.URL.Query().Get("started_before_unix_nano"))
	}
	if err == nil && r.URL.Query().Has("active") {
		var active bool
		active, err = strconv.ParseBool(r.URL.Query().Get("active"))
		query.Active = &active
	}
	if err != nil {
		writeHTTPJSON(w, http.StatusBadRequest, map[string]any{"error": map[string]string{"code": "invalid_query"}})
		return
	}
	result, err := h.queryStore.List(r.Context(), query)
	if err != nil {
		writeHTTPJSON(w, http.StatusBadRequest, map[string]any{"error": map[string]string{"code": "invalid_query"}})
		return
	}
	writeHTTPJSON(w, http.StatusOK, map[string]any{"schema_version": 1, "data": result})
}

func (h *httpHandler) traceByID(w http.ResponseWriter, r *http.Request) {
	if !h.authorizedWith(r, h.queryToken) {
		writeHTTPJSON(w, http.StatusUnauthorized, map[string]any{"error": map[string]string{"code": "unauthorized"}})
		return
	}
	if r.Method != http.MethodGet {
		writeHTTPJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": map[string]string{"code": "method_not_allowed"}})
		return
	}
	if h.queryStore == nil {
		writeHTTPJSON(w, http.StatusServiceUnavailable, map[string]any{"error": map[string]string{"code": "collector_unavailable"}})
		return
	}
	path := strings.TrimPrefix(r.URL.Path, "/internal/agent-observability/v1/traces/")
	parts := strings.Split(path, "/")
	if len(parts) == 1 && parts[0] != "" {
		detail, err := h.queryStore.Detail(r.Context(), agentobs.TraceID(parts[0]))
		if errors.Is(err, ErrTraceNotFound) {
			writeHTTPJSON(w, http.StatusNotFound, map[string]any{"error": map[string]string{"code": "trace_not_found"}})
			return
		}
		if errors.Is(err, ErrProjectionPending) {
			writeHTTPJSON(w, http.StatusConflict, map[string]any{"error": map[string]string{"code": "trace_projection_pending"}})
			return
		}
		if err != nil {
			writeHTTPJSON(w, http.StatusServiceUnavailable, map[string]any{"error": map[string]string{"code": "collector_unavailable"}})
			return
		}
		writeHTTPJSON(w, http.StatusOK, map[string]any{"schema_version": 1, "data": detail})
		return
	}
	if len(parts) == 3 && parts[0] != "" && parts[1] == "replay" && parts[2] != "" {
		spanID := agentobs.SpanID(r.URL.Query().Get("span_id"))
		payload, err := h.queryStore.Replay(r.Context(), agentobs.TraceID(parts[0]), spanID, parts[2])
		switch {
		case errors.Is(err, ErrReplayExpired):
			writeHTTPJSON(w, http.StatusGone, map[string]any{"error": map[string]string{"code": "replay_expired"}})
		case errors.Is(err, ErrReplayNotFound):
			writeHTTPJSON(w, http.StatusNotFound, map[string]any{"error": map[string]string{"code": "replay_not_found"}})
		case errors.Is(err, ErrReplayUnavailable):
			writeHTTPJSON(w, http.StatusServiceUnavailable, map[string]any{"error": map[string]string{"code": "replay_unavailable"}})
		case err != nil:
			writeHTTPJSON(w, http.StatusServiceUnavailable, map[string]any{"error": map[string]string{"code": "collector_unavailable"}})
		default:
			w.Header().Set("Cache-Control", "no-store")
			writeHTTPJSON(w, http.StatusOK, map[string]any{"schema_version": 1, "data": payload})
		}
		return
	}
	writeHTTPJSON(w, http.StatusNotFound, map[string]any{"error": map[string]string{"code": "trace_not_found"}})
}

func parseOptionalInt(value string, fallback int) (int, error) {
	if value == "" {
		return fallback, nil
	}
	return strconv.Atoi(value)
}

func parseOptionalInt64(value string) (*int64, error) {
	if value == "" {
		return nil, nil
	}
	parsed, err := strconv.ParseInt(value, 10, 64)
	return &parsed, err
}

func (h *httpHandler) purges(w http.ResponseWriter, r *http.Request) {
	if !h.authorized(r) {
		writeHTTPJSON(w, http.StatusUnauthorized, map[string]any{"error": map[string]string{"code": "unauthorized"}})
		return
	}
	if r.Method != http.MethodPost {
		writeHTTPJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": map[string]string{"code": "method_not_allowed"}})
		return
	}
	if h.purger == nil {
		writeHTTPJSON(w, http.StatusServiceUnavailable, map[string]any{"error": map[string]string{"code": "collector_unavailable"}})
		return
	}
	body, err := h.boundedRequestBody(w, r)
	if err != nil {
		writeHTTPJSON(w, http.StatusBadRequest, map[string]any{"error": map[string]string{"code": "invalid_batch"}})
		return
	}
	defer body.Close()
	decoder := json.NewDecoder(body)
	decoder.DisallowUnknownFields()
	var batch PurgeBatch
	if err := decoder.Decode(&batch); err != nil {
		var maxBytesErr *http.MaxBytesError
		if errors.As(err, &maxBytesErr) {
			writeHTTPJSON(w, http.StatusRequestEntityTooLarge, map[string]any{"error": map[string]string{"code": "batch_too_large"}})
			return
		}
		writeHTTPJSON(w, http.StatusBadRequest, map[string]any{"error": map[string]string{"code": "invalid_batch"}})
		return
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		writeHTTPJSON(w, http.StatusBadRequest, map[string]any{"error": map[string]string{"code": "invalid_batch"}})
		return
	}
	result, err := h.purger.Purge(r.Context(), batch)
	if err != nil {
		if errors.Is(err, ErrInvalidBatch) {
			writeHTTPJSON(w, http.StatusBadRequest, map[string]any{"error": map[string]string{"code": "invalid_batch"}})
			return
		}
		w.Header().Set("Retry-After", "1")
		writeHTTPJSON(w, http.StatusServiceUnavailable, map[string]any{"error": map[string]string{"code": "collector_unavailable"}})
		return
	}
	writeHTTPJSON(w, http.StatusOK, result)
}

func (h *httpHandler) health(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeHTTPJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": map[string]string{"code": "method_not_allowed"}})
		return
	}
	if h.readiness != nil {
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		if err := h.readiness(ctx); err != nil {
			writeHTTPJSON(w, http.StatusServiceUnavailable, map[string]string{"status": "not_ready", "service": "collector"})
			return
		}
	}
	writeHTTPJSON(w, http.StatusOK, map[string]string{"status": "ready", "service": "collector"})
}

func (h *httpHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h.mux.ServeHTTP(w, r)
}

func (h *httpHandler) batches(w http.ResponseWriter, r *http.Request) {
	if !h.authorized(r) {
		writeHTTPJSON(w, http.StatusUnauthorized, map[string]any{"error": map[string]string{"code": "unauthorized"}})
		return
	}
	if r.Method != http.MethodPost {
		writeHTTPJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": map[string]string{"code": "method_not_allowed"}})
		return
	}
	body, err := h.boundedRequestBody(w, r)
	if err != nil {
		writeHTTPJSON(w, http.StatusBadRequest, map[string]any{"error": map[string]string{"code": "invalid_batch"}})
		return
	}
	defer body.Close()
	decoder := json.NewDecoder(body)
	decoder.DisallowUnknownFields()
	var batch Batch
	if err := decoder.Decode(&batch); err != nil {
		var maxBytesErr *http.MaxBytesError
		if errors.As(err, &maxBytesErr) {
			writeHTTPJSON(w, http.StatusRequestEntityTooLarge, map[string]any{"error": map[string]string{"code": "batch_too_large"}})
			return
		}
		writeHTTPJSON(w, http.StatusBadRequest, map[string]any{"error": map[string]string{"code": "invalid_batch"}})
		return
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		writeHTTPJSON(w, http.StatusBadRequest, map[string]any{"error": map[string]string{"code": "invalid_batch"}})
		return
	}
	result, err := h.ingestor.Ingest(r.Context(), batch)
	if err != nil {
		if errors.Is(err, ErrInvalidBatch) {
			writeHTTPJSON(w, http.StatusBadRequest, map[string]any{"error": map[string]string{"code": "invalid_batch"}})
			return
		}
		w.Header().Set("Retry-After", "1")
		writeHTTPJSON(w, http.StatusServiceUnavailable, map[string]any{"error": map[string]string{"code": "collector_unavailable"}})
		return
	}
	writeHTTPJSON(w, http.StatusOK, result)
}

type boundedGzipBody struct {
	io.ReadCloser
	compressed io.ReadCloser
}

func (b *boundedGzipBody) Close() error {
	return errors.Join(b.ReadCloser.Close(), b.compressed.Close())
}

func (h *httpHandler) boundedRequestBody(w http.ResponseWriter, r *http.Request) (io.ReadCloser, error) {
	compressed := http.MaxBytesReader(w, r.Body, h.maxBodyBytes)
	encoding := strings.TrimSpace(strings.ToLower(r.Header.Get("Content-Encoding")))
	switch encoding {
	case "":
		return compressed, nil
	case "gzip":
		decompressor, err := gzip.NewReader(compressed)
		if err != nil {
			_ = compressed.Close()
			return nil, err
		}
		decompressed := http.MaxBytesReader(w, decompressor, h.maxBodyBytes)
		return &boundedGzipBody{ReadCloser: decompressed, compressed: compressed}, nil
	default:
		_ = compressed.Close()
		return nil, errors.New("unsupported Collector content encoding")
	}
}

func (h *httpHandler) authorized(r *http.Request) bool {
	return h.authorizedWith(r, h.serviceToken)
}

func (h *httpHandler) authorizedWith(r *http.Request, token string) bool {
	const prefix = "Bearer "
	header := r.Header.Get("Authorization")
	if token == "" || !strings.HasPrefix(header, prefix) {
		return false
	}
	candidate := strings.TrimPrefix(header, prefix)
	return subtle.ConstantTimeCompare([]byte(candidate), []byte(token)) == 1
}

func writeHTTPJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}
