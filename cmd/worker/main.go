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

	"github.com/huangxinxinyu/nano-notebook/internal/agent"
	"github.com/huangxinxinyu/nano-notebook/internal/app"
	"github.com/huangxinxinyu/nano-notebook/internal/jobs"
	"github.com/huangxinxinyu/nano-notebook/internal/models"
	"github.com/huangxinxinyu/nano-notebook/internal/platform/telemetry"
	agentworker "github.com/huangxinxinyu/nano-notebook/internal/worker"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	dsn := env("NANO_DATABASE_URL", "postgres://nano:nano@localhost:55432/nano?sslmode=disable")
	db, err := app.OpenDB(ctx, dsn)
	if err != nil {
		slog.Error("worker database unavailable", "error", err)
		os.Exit(1)
	}
	defer db.Close()
	shutdownTelemetry, err := telemetry.Start(ctx, "nano-worker")
	if err != nil {
		slog.Error("worker telemetry unavailable", "error", err)
		os.Exit(1)
	}
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = shutdownTelemetry(shutdownCtx)
	}()
	telemetry.StartupSpan(ctx, "nano-worker")

	modelClient := models.NewBifrostClient(env("NANO_BIFROST_URL", "http://127.0.0.1:56666"), &http.Client{}, 2048)
	runtime := agent.NewPostgresRuntime(db.Pool(), agent.BareSystemPrompt, nil)
	loop := agent.NewLoop(runtime, runtime, agent.NewModelRunner(modelClient), runtime)
	workerService := agentworker.NewService(db.Pool(), jobs.NewQueue(db.Pool()), loop, 5*time.Second, 210*time.Second)
	go func() {
		if err := workerService.Run(ctx); err != nil && ctx.Err() == nil {
			slog.Error("agent worker failed", "error", err)
			stop()
		}
	}()

	mux := http.NewServeMux()
	mux.HandleFunc("/health/live", func(w http.ResponseWriter, r *http.Request) {
		writeWorkerJSON(w, http.StatusOK, `{"status":"live","service":"worker","mode":"agent"}`)
	})
	mux.HandleFunc("/health/ready", func(w http.ResponseWriter, r *http.Request) {
		pingCtx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		if db.Pool().Ping(pingCtx) != nil {
			writeWorkerJSON(w, http.StatusServiceUnavailable, `{"status":"not_ready","service":"worker"}`)
			return
		}
		writeWorkerJSON(w, http.StatusOK, `{"status":"ready","service":"worker","mode":"agent"}`)
	})

	httpServer := &http.Server{Addr: env("NANO_WORKER_ADDR", ":8081"), Handler: otelhttp.NewHandler(mux, "worker"), ReadHeaderTimeout: 5 * time.Second}
	go func() {
		slog.Info("worker listening", "addr", httpServer.Addr, "mode", "agent", "provider_credentials_required", true)
		if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.Error("worker failed", "error", err)
			os.Exit(1)
		}
	}()

	<-ctx.Done()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := httpServer.Shutdown(shutdownCtx); err != nil {
		slog.Error("worker shutdown failed", "error", err)
		os.Exit(1)
	}
	slog.Info("worker stopped")
}

func writeWorkerJSON(w http.ResponseWriter, status int, body string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write([]byte(body))
}

func env(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}
