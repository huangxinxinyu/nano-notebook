package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/huangxinxinyu/nano-notebook/internal/documentrender"
)

type rendererConfig struct {
	Addr                 string
	ServiceToken         string
	RenderConfigID       string
	PDFiumBinary         string
	LibreOfficeBinary    string
	ScratchRoot          string
	MaxRuntime           time.Duration
	MaxInputBytes        int64
	MaxConvertedPDFBytes int64
	MaxPages             int
	MaxPixelsPerPage     int64
	MaxOutputBytes       int64
	MaxConcurrent        int
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	config, err := loadRendererConfig()
	if err != nil {
		slog.Error("document renderer configuration invalid", "error", err)
		os.Exit(1)
	}
	if err := os.MkdirAll(config.ScratchRoot, 0o700); err != nil {
		slog.Error("document renderer scratch unavailable", "error", err)
		os.Exit(1)
	}
	if _, err := exec.LookPath(config.PDFiumBinary); err != nil {
		slog.Error("PDFium binary unavailable", "error", err)
		os.Exit(1)
	}
	if _, err := exec.LookPath(config.LibreOfficeBinary); err != nil {
		slog.Error("LibreOffice binary unavailable", "error", err)
		os.Exit(1)
	}
	engine, err := documentrender.NewEngine(documentrender.EngineConfig{
		RenderConfigID: config.RenderConfigID, PDFiumBinary: config.PDFiumBinary, LibreOfficeBinary: config.LibreOfficeBinary,
		ScratchRoot: config.ScratchRoot, MaxRuntime: config.MaxRuntime, MaxConvertedPDFBytes: config.MaxConvertedPDFBytes,
		Runner: documentrender.OSCommandRunner{},
	})
	if err != nil {
		slog.Error("document renderer Engine invalid", "error", err)
		os.Exit(1)
	}
	handler, err := documentrender.NewServiceHandler(engine, documentrender.ServiceConfig{
		ServiceToken: config.ServiceToken, RenderConfigID: config.RenderConfigID, MaxInputBytes: config.MaxInputBytes,
		MaxPages: config.MaxPages, MaxPixelsPerPage: config.MaxPixelsPerPage, MaxOutputBytes: config.MaxOutputBytes,
		MaxConcurrent: config.MaxConcurrent,
	})
	if err != nil {
		slog.Error("document renderer Service invalid", "error", err)
		os.Exit(1)
	}
	mux := http.NewServeMux()
	mux.Handle("/v1/render", handler)
	mux.HandleFunc("/health/live", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"live","service":"document-renderer"}`))
	})
	server := &http.Server{
		Addr: config.Addr, Handler: mux, ReadHeaderTimeout: 5 * time.Second,
		WriteTimeout: config.MaxRuntime + 10*time.Second, IdleTimeout: 30 * time.Second,
	}
	serverDone := make(chan error, 1)
	go func() {
		slog.Info("document renderer listening", "addr", config.Addr, "render_config_id", config.RenderConfigID)
		serverDone <- server.ListenAndServe()
	}()
	select {
	case <-ctx.Done():
		shutdownContext, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdownContext); err != nil {
			slog.Error("document renderer shutdown failed", "error", err)
			os.Exit(1)
		}
		err := <-serverDone
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.Error("document renderer failed", "error", err)
			os.Exit(1)
		}
	case err := <-serverDone:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.Error("document renderer failed", "error", err)
			os.Exit(1)
		}
	}
}

func loadRendererConfig() (rendererConfig, error) {
	maxRuntime, err := rendererEnvDuration("NANO_DOCUMENT_RENDERER_MAX_RUNTIME", 90*time.Second)
	if err != nil {
		return rendererConfig{}, err
	}
	maxInput, err := rendererEnvInt64("NANO_DOCUMENT_RENDERER_MAX_INPUT_BYTES", 100*1024*1024)
	if err != nil {
		return rendererConfig{}, err
	}
	maxConverted, err := rendererEnvInt64("NANO_DOCUMENT_RENDERER_MAX_CONVERTED_PDF_BYTES", 256*1024*1024)
	if err != nil {
		return rendererConfig{}, err
	}
	maxPages, err := rendererEnvInt("NANO_DOCUMENT_RENDERER_MAX_PAGES", 500)
	if err != nil {
		return rendererConfig{}, err
	}
	maxPixels, err := rendererEnvInt64("NANO_DOCUMENT_RENDERER_MAX_PIXELS_PER_PAGE", 20_000_000)
	if err != nil {
		return rendererConfig{}, err
	}
	maxOutput, err := rendererEnvInt64("NANO_DOCUMENT_RENDERER_MAX_OUTPUT_BYTES", 256*1024*1024)
	if err != nil {
		return rendererConfig{}, err
	}
	maxConcurrent, err := rendererEnvInt("NANO_DOCUMENT_RENDERER_MAX_CONCURRENT", 2)
	if err != nil {
		return rendererConfig{}, err
	}
	config := rendererConfig{
		Addr:              rendererEnv("NANO_DOCUMENT_RENDERER_ADDR", "127.0.0.1:8084"),
		ServiceToken:      rendererEnv("NANO_DOCUMENT_RENDERER_SERVICE_TOKEN", "nano-local-renderer-token"),
		RenderConfigID:    rendererEnv("NANO_DOCUMENT_RENDER_CONFIG_ID", "pdfium-libreoffice-v1"),
		PDFiumBinary:      rendererEnv("NANO_PDFIUM_BINARY", "pdfium_test"),
		LibreOfficeBinary: rendererEnv("NANO_LIBREOFFICE_BINARY", "soffice"),
		ScratchRoot:       rendererEnv("NANO_DOCUMENT_RENDERER_SCRATCH_ROOT", os.TempDir()),
		MaxRuntime:        maxRuntime, MaxInputBytes: maxInput, MaxConvertedPDFBytes: maxConverted,
		MaxPages: maxPages, MaxPixelsPerPage: maxPixels, MaxOutputBytes: maxOutput, MaxConcurrent: maxConcurrent,
	}
	if config.Addr == "" || config.ServiceToken == "" || config.RenderConfigID == "" || config.PDFiumBinary == "" ||
		config.LibreOfficeBinary == "" || config.ScratchRoot == "" || config.MaxRuntime <= 0 || config.MaxRuntime > 10*time.Minute ||
		config.MaxInputBytes < 1 || config.MaxInputBytes > 100*1024*1024 || config.MaxConvertedPDFBytes < 1 || config.MaxConvertedPDFBytes > 1<<30 ||
		config.MaxPages < 1 || config.MaxPages > 500 || config.MaxPixelsPerPage < 1 || config.MaxPixelsPerPage > 100_000_000 ||
		config.MaxOutputBytes < 1 || config.MaxOutputBytes > 2<<30 || config.MaxConcurrent < 1 || config.MaxConcurrent > 64 {
		return rendererConfig{}, errors.New("document renderer configuration is incomplete or inconsistent")
	}
	return config, nil
}

func rendererEnv(key, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return fallback
}

func rendererEnvDuration(key string, fallback time.Duration) (time.Duration, error) {
	value := os.Getenv(key)
	if value == "" {
		return fallback, nil
	}
	parsed, err := time.ParseDuration(value)
	if err != nil {
		return 0, fmt.Errorf("parse %s: %w", key, err)
	}
	return parsed, nil
}

func rendererEnvInt(key string, fallback int) (int, error) {
	value, err := rendererEnvInt64(key, int64(fallback))
	return int(value), err
}

func rendererEnvInt64(key string, fallback int64) (int64, error) {
	value := os.Getenv(key)
	if value == "" {
		return fallback, nil
	}
	parsed, err := strconv.ParseInt(value, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("parse %s: %w", key, err)
	}
	return parsed, nil
}
