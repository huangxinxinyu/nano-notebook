package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/huangxinxinyu/nano-notebook/internal/agent"
	"github.com/huangxinxinyu/nano-notebook/internal/agentobs/otelbridge"
	"github.com/huangxinxinyu/nano-notebook/internal/agentoutbox"
	"github.com/huangxinxinyu/nano-notebook/internal/app"
	"github.com/huangxinxinyu/nano-notebook/internal/jobs"
	"github.com/huangxinxinyu/nano-notebook/internal/models"
	"github.com/huangxinxinyu/nano-notebook/internal/platform/telemetry"
	agentworker "github.com/huangxinxinyu/nano-notebook/internal/worker"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel"
)

type workerConfig struct {
	DatabaseURL           string
	Addr                  string
	CollectorEndpoint     string
	CollectorServiceToken string
	ProducerID            string
	MaxRecords            int
	MaxEncodedBytes       int
	MaxTraces             int
	LeaseDuration         time.Duration
	PollInterval          time.Duration
	MaxDelay              time.Duration
	HTTPTimeout           time.Duration
	BaseBackoff           time.Duration
	MaxBackoff            time.Duration
}

type outboxFlusher interface {
	ForceFlush(context.Context) error
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	config, err := loadWorkerConfig()
	if err != nil {
		slog.Error("worker configuration invalid", "error", err)
		os.Exit(1)
	}
	db, err := app.OpenDB(ctx, config.DatabaseURL)
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
	traceBridge, err := otelbridge.New(otel.Tracer("nano-agent-observability"))
	if err != nil {
		slog.Error("Agent Trace telemetry bridge unavailable", "error", err)
		os.Exit(1)
	}
	defer traceBridge.Shutdown(context.Background())
	runtime := agent.NewPostgresRuntime(db.Pool(), agent.BareSystemPrompt, nil, agent.WithBestEffortTraceExporter(traceBridge))
	registry, err := agent.NewActionRegistry(agent.NewCalculateAction(), agent.NewCurrentTimeAction(nil))
	if err != nil {
		slog.Error("worker Action registry invalid", "error", err)
		os.Exit(1)
	}
	controller := agent.NewController(runtime, modelClient, registry)
	workerService := agentworker.NewService(db.Pool(), jobs.NewQueue(db.Pool()), controller, 5*time.Second, 210*time.Second)
	outboxStore, err := agentoutbox.NewPostgresStore(db.Pool(), agentoutbox.Config{
		ProducerID: config.ProducerID, MaxRecords: config.MaxRecords,
		MaxEncodedBytes: config.MaxEncodedBytes, MaxTraces: config.MaxTraces,
		LeaseDuration: config.LeaseDuration, BaseBackoff: config.BaseBackoff, MaxBackoff: config.MaxBackoff,
		MaxDelay: config.MaxDelay,
	})
	if err != nil {
		slog.Error("Agent Trace Outbox invalid", "error", err)
		os.Exit(1)
	}
	sender, err := agentoutbox.NewSender(outboxStore, agentoutbox.SenderConfig{
		Endpoint: config.CollectorEndpoint, ServiceToken: config.CollectorServiceToken,
		ReportError: func(err error) {
			slog.Error("Agent Trace Batch delivery failed; durable records retained for retry", "error", err)
		},
		HTTPClient: &http.Client{
			Timeout:   config.HTTPTimeout,
			Transport: otelhttp.NewTransport(http.DefaultTransport),
		},
	})
	if err != nil {
		slog.Error("Agent Trace Sender invalid", "error", err)
		os.Exit(1)
	}
	workerDone := make(chan error, 1)
	go func() {
		err := workerService.Run(ctx)
		workerDone <- err
		if err != nil && ctx.Err() == nil {
			slog.Error("agent worker failed", "error", err)
			stop()
		}
	}()
	senderDone := make(chan error, 1)
	go func() {
		err := sender.Run(ctx, config.PollInterval)
		senderDone <- err
		if err != nil && ctx.Err() == nil {
			slog.Error("Agent Trace Sender failed", "error", err)
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

	httpServer := &http.Server{Addr: config.Addr, Handler: otelhttp.NewHandler(mux, "worker"), ReadHeaderTimeout: 5 * time.Second}
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
	select {
	case err := <-workerDone:
		if err != nil {
			slog.Error("agent worker shutdown failed", "error", err)
			os.Exit(1)
		}
	case <-shutdownCtx.Done():
		slog.Error("agent worker did not release its lease before shutdown", "error", shutdownCtx.Err())
		os.Exit(1)
	}
	select {
	case err := <-senderDone:
		if err != nil {
			slog.Error("Agent Trace Sender shutdown failed", "error", err)
			os.Exit(1)
		}
	case <-shutdownCtx.Done():
		slog.Error("Agent Trace Sender did not stop before shutdown", "error", shutdownCtx.Err())
		os.Exit(1)
	}
	if err := flushOutboxOnShutdown(shutdownCtx, sender); err != nil {
		slog.Warn("Agent Trace Outbox flush incomplete; durable records remain for the next Sender", "error", err)
	}
	slog.Info("worker stopped")
}

func flushOutboxOnShutdown(ctx context.Context, flusher outboxFlusher) error {
	return flusher.ForceFlush(ctx)
}

func loadWorkerConfig() (workerConfig, error) {
	maxRecords, err := workerEnvInt("NANO_OUTBOX_MAX_RECORDS", 128)
	if err != nil {
		return workerConfig{}, err
	}
	maxEncodedBytes, err := workerEnvInt("NANO_OUTBOX_MAX_ENCODED_BYTES", 512*1024)
	if err != nil {
		return workerConfig{}, err
	}
	maxTraces, err := workerEnvInt("NANO_OUTBOX_MAX_TRACES", 16)
	if err != nil {
		return workerConfig{}, err
	}
	leaseDuration, err := workerEnvDuration("NANO_OUTBOX_LEASE_DURATION", 30*time.Second)
	if err != nil {
		return workerConfig{}, err
	}
	pollInterval, err := workerEnvDuration("NANO_OUTBOX_POLL_INTERVAL", 100*time.Millisecond)
	if err != nil {
		return workerConfig{}, err
	}
	maxDelay, err := workerEnvDuration("NANO_OUTBOX_MAX_DELAY", 250*time.Millisecond)
	if err != nil {
		return workerConfig{}, err
	}
	httpTimeout, err := workerEnvDuration("NANO_OUTBOX_HTTP_TIMEOUT", 10*time.Second)
	if err != nil {
		return workerConfig{}, err
	}
	baseBackoff, err := workerEnvDuration("NANO_OUTBOX_BASE_BACKOFF", time.Second)
	if err != nil {
		return workerConfig{}, err
	}
	maxBackoff, err := workerEnvDuration("NANO_OUTBOX_MAX_BACKOFF", time.Minute)
	if err != nil {
		return workerConfig{}, err
	}
	collectorURL := strings.TrimRight(env("NANO_COLLECTOR_URL", "http://127.0.0.1:8082"), "/")
	config := workerConfig{
		DatabaseURL:           env("NANO_DATABASE_URL", "postgres://nano:nano@localhost:55432/nano?sslmode=disable"),
		Addr:                  env("NANO_WORKER_ADDR", ":8081"),
		CollectorEndpoint:     collectorURL + "/internal/agent-observability/v1/batches",
		CollectorServiceToken: env("NANO_COLLECTOR_SERVICE_TOKEN", "nano-local-collector-token"),
		ProducerID:            env("NANO_COLLECTOR_PRODUCER_ID", "nano-worker"),
		MaxRecords:            maxRecords, MaxEncodedBytes: maxEncodedBytes, MaxTraces: maxTraces,
		LeaseDuration: leaseDuration, PollInterval: pollInterval, MaxDelay: maxDelay, HTTPTimeout: httpTimeout,
		BaseBackoff: baseBackoff, MaxBackoff: maxBackoff,
	}
	if strings.TrimSpace(config.DatabaseURL) == "" || strings.TrimSpace(config.Addr) == "" ||
		strings.TrimSpace(collectorURL) == "" || strings.TrimSpace(config.CollectorServiceToken) == "" ||
		strings.TrimSpace(config.ProducerID) == "" || config.MaxRecords < 1 ||
		config.MaxEncodedBytes < 1 || config.MaxTraces < 1 || config.LeaseDuration <= 0 ||
		config.PollInterval <= 0 || config.MaxDelay < 0 || config.HTTPTimeout <= 0 || config.BaseBackoff <= 0 ||
		config.MaxBackoff < config.BaseBackoff {
		return workerConfig{}, errors.New("worker configuration is incomplete or inconsistent")
	}
	return config, nil
}

func workerEnvInt(key string, fallback int) (int, error) {
	value := os.Getenv(key)
	if value == "" {
		return fallback, nil
	}
	parsed, err := strconv.Atoi(value)
	if err != nil {
		return 0, fmt.Errorf("parse %s: %w", key, err)
	}
	return parsed, nil
}

func workerEnvDuration(key string, fallback time.Duration) (time.Duration, error) {
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
