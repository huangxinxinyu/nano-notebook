package main

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/huangxinxinyu/nano-notebook/internal/app"
	"github.com/huangxinxinyu/nano-notebook/internal/collector"
	"github.com/huangxinxinyu/nano-notebook/internal/platform/telemetry"
	"github.com/huangxinxinyu/nano-notebook/internal/realtime"
	"github.com/huangxinxinyu/nano-notebook/internal/replay"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
)

type controlPlaneConfig struct {
	DatabaseURL         string
	Addr                string
	CollectorURL        string
	CollectorQueryToken string
	ReplayKeyID         string
	ReplayKEK           []byte
	CookieSecure        bool
	Version             string
	DefaultModel        string
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	config, err := loadControlPlaneConfig()
	if err != nil {
		slog.Error("Control Plane configuration invalid", "error", err)
		os.Exit(1)
	}
	db, err := app.OpenDB(ctx, config.DatabaseURL)
	if err != nil {
		slog.Error("database unavailable", "error", err)
		os.Exit(1)
	}
	defer db.Close()
	if err := app.RunMigrations(ctx, db); err != nil {
		slog.Error("migrations failed", "error", err)
		os.Exit(1)
	}
	shutdownTelemetry, err := telemetry.Start(ctx, "nano-control-plane")
	if err != nil {
		slog.Error("telemetry unavailable", "error", err)
		os.Exit(1)
	}
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = shutdownTelemetry(shutdownCtx)
	}()
	telemetry.StartupSpan(ctx, "nano-control-plane")

	queryClient, err := collector.NewHTTPQueryClient(collector.HTTPQueryClientConfig{
		Endpoint: config.CollectorURL, ServiceToken: config.CollectorQueryToken,
	})
	if err != nil {
		slog.Error("Collector Query client configuration invalid", "error", err)
		os.Exit(1)
	}
	keyProvider, err := replay.NewDevelopmentKeyProvider(config.ReplayKeyID, config.ReplayKEK)
	if err != nil {
		slog.Error("Replay key configuration invalid", "error", err)
		os.Exit(1)
	}
	replaySealer, err := replay.NewSealer(keyProvider)
	if err != nil {
		slog.Error("Replay opener configuration invalid", "error", err)
		os.Exit(1)
	}
	server := app.NewServer(app.Config{
		CookieSecure: config.CookieSecure, Version: config.Version, DefaultModel: config.DefaultModel,
		AdminTraces: queryClient, ReplaySealer: replaySealer,
	}, db)
	runListener := realtime.NewRunListener(db.Pool(), server.NotifyRun)
	go func() {
		if err := runListener.Run(ctx); err != nil && ctx.Err() == nil {
			slog.Error("Run projection listener failed", "error", err)
			stop()
		}
	}()
	httpServer := &http.Server{
		Addr:              config.Addr,
		Handler:           otelhttp.NewHandler(server.Handler(), "control-plane"),
		ReadHeaderTimeout: 5 * time.Second,
	}

	go func() {
		slog.Info("control-plane listening", "addr", httpServer.Addr, "provider_credentials_required", false)
		if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.Error("control-plane failed", "error", err)
			os.Exit(1)
		}
	}()

	<-ctx.Done()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := httpServer.Shutdown(shutdownCtx); err != nil {
		slog.Error("control-plane shutdown failed", "error", err)
		os.Exit(1)
	}
	slog.Info("control-plane stopped")
}

func loadControlPlaneConfig() (controlPlaneConfig, error) {
	replayKEK, err := base64.StdEncoding.DecodeString(env("NANO_REPLAY_KEK_BASE64", "bmFuby1sb2NhbC1kZXYta2VrLTAwMDAwMDAwMDAwMDA="))
	if err != nil {
		return controlPlaneConfig{}, fmt.Errorf("parse NANO_REPLAY_KEK_BASE64: %w", err)
	}
	config := controlPlaneConfig{
		DatabaseURL:         env("NANO_DATABASE_URL", "postgres://nano:nano@localhost:55432/nano?sslmode=disable"),
		Addr:                env("NANO_CONTROL_PLANE_ADDR", ":8080"),
		CollectorURL:        strings.TrimRight(env("NANO_COLLECTOR_URL", "http://127.0.0.1:8082"), "/"),
		CollectorQueryToken: env("NANO_COLLECTOR_QUERY_TOKEN", "nano-local-collector-query-token"),
		ReplayKeyID:         env("NANO_REPLAY_KEY_ID", "nano-local-replay-key-v1"), ReplayKEK: replayKEK,
		CookieSecure: os.Getenv("NANO_COOKIE_SECURE") == "true", Version: env("NANO_VERSION", "dev"),
		DefaultModel: env("NANO_CHAT_MODEL", "aliyun/qwen-flash"),
	}
	if strings.TrimSpace(config.DatabaseURL) == "" || strings.TrimSpace(config.Addr) == "" ||
		strings.TrimSpace(config.CollectorURL) == "" || strings.TrimSpace(config.CollectorQueryToken) == "" ||
		strings.TrimSpace(config.ReplayKeyID) == "" || len(config.ReplayKEK) != 32 {
		return controlPlaneConfig{}, errors.New("Control Plane configuration is incomplete")
	}
	return config, nil
}

func env(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}
