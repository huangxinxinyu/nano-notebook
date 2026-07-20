package documentrender

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"image/png"
	"io"
	"net/http"
	"strconv"
	"strings"
)

type ServiceConfig struct {
	ServiceToken     string
	RenderConfigID   string
	MaxInputBytes    int64
	MaxPages         int
	MaxPixelsPerPage int64
	MaxOutputBytes   int64
	MaxConcurrent    int
}

type ServiceHandler struct {
	renderer  Adapter
	config    ServiceConfig
	admission chan struct{}
}

func NewServiceHandler(renderer Adapter, config ServiceConfig) (*ServiceHandler, error) {
	config.ServiceToken = strings.TrimSpace(config.ServiceToken)
	config.RenderConfigID = strings.TrimSpace(config.RenderConfigID)
	if renderer == nil || config.ServiceToken == "" || config.RenderConfigID == "" || config.MaxInputBytes < 1 || config.MaxInputBytes > 100*1024*1024 ||
		config.MaxPages < 1 || config.MaxPages > 500 || config.MaxPixelsPerPage < 1 || config.MaxPixelsPerPage > 100_000_000 ||
		config.MaxOutputBytes < 1 || config.MaxOutputBytes > 2<<30 || config.MaxConcurrent < 1 || config.MaxConcurrent > 64 {
		return nil, errors.New("document renderer Service configuration is invalid")
	}
	return &ServiceHandler{renderer: renderer, config: config, admission: make(chan struct{}, config.MaxConcurrent)}, nil
}

func (h *ServiceHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if h == nil || h.renderer == nil {
		http.Error(w, "service unavailable", http.StatusServiceUnavailable)
		return
	}
	provided := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	if subtle.ConstantTimeCompare([]byte(provided), []byte(h.config.ServiceToken)) != 1 {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if r.Method != http.MethodPost || r.URL.Path != "/v1/render" {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	if r.ContentLength > h.config.MaxInputBytes {
		http.Error(w, "input too large", http.StatusRequestEntityTooLarge)
		return
	}
	payload, err := io.ReadAll(io.LimitReader(r.Body, h.config.MaxInputBytes+1))
	if err != nil {
		http.Error(w, "invalid input", http.StatusBadRequest)
		return
	}
	if int64(len(payload)) > h.config.MaxInputBytes {
		http.Error(w, "input too large", http.StatusRequestEntityTooLarge)
		return
	}
	request, err := h.requestFromHTTP(r, payload)
	if err != nil {
		http.Error(w, "invalid render request", http.StatusBadRequest)
		return
	}
	select {
	case h.admission <- struct{}{}:
		defer func() { <-h.admission }()
	default:
		http.Error(w, "renderer busy", http.StatusServiceUnavailable)
		return
	}
	result, err := h.renderer.Render(r.Context(), request, payload)
	if err != nil {
		http.Error(w, "render failed", http.StatusUnprocessableEntity)
		return
	}
	encoded, err := encodeArchive(request, result)
	if err != nil {
		http.Error(w, "invalid render output", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(encoded)
}

func (h *ServiceHandler) requestFromHTTP(r *http.Request, payload []byte) (Request, error) {
	mediaType := strings.TrimSpace(strings.Split(r.Header.Get("Content-Type"), ";")[0])
	format := Format("")
	switch mediaType {
	case "application/pdf":
		format = FormatPDF
	case "application/vnd.openxmlformats-officedocument.presentationml.presentation":
		format = FormatPPTX
	}
	maxPages, errPages := strconv.Atoi(r.Header.Get("X-Nano-Max-Pages"))
	dpi, errDPI := strconv.Atoi(r.Header.Get("X-Nano-Render-DPI"))
	maxPixels, errPixels := strconv.ParseInt(r.Header.Get("X-Nano-Max-Pixels-Per-Page"), 10, 64)
	maxOutput, errOutput := strconv.ParseInt(r.Header.Get("X-Nano-Max-Output-Bytes"), 10, 64)
	if errPages != nil || errDPI != nil || errPixels != nil || errOutput != nil || r.Header.Get("X-Nano-Render-Config-ID") != h.config.RenderConfigID ||
		maxPages > h.config.MaxPages || maxPixels > h.config.MaxPixelsPerPage || maxOutput > h.config.MaxOutputBytes {
		return Request{}, ErrRequestInvalid
	}
	request := Request{
		SchemaVersion: 1, SourceID: r.Header.Get("X-Nano-Source-ID"), Format: format,
		InputSHA256: r.Header.Get("X-Nano-Input-SHA256"), InputBytes: int64(len(payload)),
		RenderConfigID: h.config.RenderConfigID, MaxPages: maxPages, DPI: dpi,
		MaxPixelsPerPage: maxPixels, MaxOutputBytes: maxOutput,
	}
	if err := request.Validate(); err != nil {
		return Request{}, err
	}
	digest := sha256.Sum256(payload)
	if hex.EncodeToString(digest[:]) != request.InputSHA256 {
		return Request{}, ErrRequestInvalid
	}
	return request, nil
}

func encodeArchive(request Request, result Result) ([]byte, error) {
	if err := Validate(request, result.Manifest); err != nil || len(result.Assets) != len(result.Manifest.Pages) {
		return nil, ErrManifestInvalid
	}
	var output bytes.Buffer
	archive := zip.NewWriter(&output)
	manifestPayload, err := json.Marshal(result.Manifest)
	if err != nil {
		return nil, ErrManifestInvalid
	}
	if err := writeArchiveFile(archive, "manifest.json", manifestPayload); err != nil {
		return nil, err
	}
	for index, asset := range result.Assets {
		page := result.Manifest.Pages[index]
		if asset.Page != page || int64(len(asset.Payload)) != page.Bytes {
			return nil, ErrManifestInvalid
		}
		digest := sha256.Sum256(asset.Payload)
		configuration, err := png.DecodeConfig(bytes.NewReader(asset.Payload))
		if err != nil || hex.EncodeToString(digest[:]) != page.SHA256 || configuration.Width != page.Width || configuration.Height != page.Height {
			return nil, ErrManifestInvalid
		}
		if err := writeArchiveFile(archive, page.Filename, asset.Payload); err != nil {
			return nil, err
		}
	}
	if err := archive.Close(); err != nil {
		return nil, err
	}
	return output.Bytes(), nil
}

func writeArchiveFile(archive *zip.Writer, name string, payload []byte) error {
	entry, err := archive.Create(name)
	if err != nil {
		return err
	}
	_, err = entry.Write(payload)
	return err
}
