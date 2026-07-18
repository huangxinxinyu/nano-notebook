package collector

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
)

type HTTPConfig struct {
	Ingestor     *Ingestor
	ServiceToken string
	MaxBodyBytes int64
}

type httpHandler struct {
	ingestor     *Ingestor
	serviceToken string
	maxBodyBytes int64
	mux          *http.ServeMux
}

func NewHTTPHandler(config HTTPConfig) (http.Handler, error) {
	if config.Ingestor == nil || strings.TrimSpace(config.ServiceToken) == "" || config.MaxBodyBytes < 1 {
		return nil, errors.New("Collector HTTP configuration is incomplete")
	}
	handler := &httpHandler{
		ingestor: config.Ingestor, serviceToken: config.ServiceToken, maxBodyBytes: config.MaxBodyBytes,
		mux: http.NewServeMux(),
	}
	handler.mux.HandleFunc("/internal/agent-observability/v1/batches", handler.batches)
	handler.mux.HandleFunc("/internal/agent-observability/v1/health", handler.health)
	return handler, nil
}

func (h *httpHandler) health(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeHTTPJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": map[string]string{"code": "method_not_allowed"}})
		return
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
	r.Body = http.MaxBytesReader(w, r.Body, h.maxBodyBytes)
	decoder := json.NewDecoder(r.Body)
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

func (h *httpHandler) authorized(r *http.Request) bool {
	const prefix = "Bearer "
	header := r.Header.Get("Authorization")
	if !strings.HasPrefix(header, prefix) {
		return false
	}
	candidate := strings.TrimPrefix(header, prefix)
	return subtle.ConstantTimeCompare([]byte(candidate), []byte(h.serviceToken)) == 1
}

func writeHTTPJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}
