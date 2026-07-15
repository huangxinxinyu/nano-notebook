package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/huangxinxinyu/nano-notebook/internal/app"
	"github.com/huangxinxinyu/nano-notebook/internal/platform/telemetry"
	"github.com/huangxinxinyu/nano-notebook/internal/realtime"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	dsn := env("NANO_DATABASE_URL", "postgres://nano:nano@localhost:55432/nano?sslmode=disable")
	db, err := app.OpenDB(ctx, dsn)
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

	server := app.NewServer(app.Config{
		CookieSecure: os.Getenv("NANO_COOKIE_SECURE") == "true",
		Version:      env("NANO_VERSION", "dev"),
		DefaultModel: env("NANO_CHAT_MODEL", "aliyun/qwen-flash"),
	}, db)
	runListener := realtime.NewRunListener(db.Pool(), server.NotifyRun)
	go func() {
		if err := runListener.Run(ctx); err != nil && ctx.Err() == nil {
			slog.Error("Run projection listener failed", "error", err)
			stop()
		}
	}()
	httpServer := &http.Server{
		Addr:              env("NANO_CONTROL_PLANE_ADDR", ":8080"),
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

func env(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}
