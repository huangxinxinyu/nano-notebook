package collector

import (
	"compress/gzip"
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"
)

type HTTPConfig struct {
	Ingestor     *Ingestor
	Purger       *Purger
	ServiceToken string
	MaxBodyBytes int64
	Readiness    func(context.Context) error
}

type httpHandler struct {
	ingestor     *Ingestor
	purger       *Purger
	serviceToken string
	maxBodyBytes int64
	readiness    func(context.Context) error
	mux          *http.ServeMux
}

func NewHTTPHandler(config HTTPConfig) (http.Handler, error) {
	if config.Ingestor == nil || strings.TrimSpace(config.ServiceToken) == "" || config.MaxBodyBytes < 1 {
		return nil, errors.New("Collector HTTP configuration is incomplete")
	}
	handler := &httpHandler{
		ingestor: config.Ingestor, serviceToken: config.ServiceToken, maxBodyBytes: config.MaxBodyBytes,
		purger: config.Purger, readiness: config.Readiness, mux: http.NewServeMux(),
	}
	handler.mux.HandleFunc("/internal/agent-observability/v1/batches", handler.batches)
	handler.mux.HandleFunc("/internal/agent-observability/v1/purges", handler.purges)
	handler.mux.HandleFunc("/internal/agent-observability/v1/health", handler.health)
	return handler, nil
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
