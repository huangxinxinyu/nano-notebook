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
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/huangxinxinyu/nano-notebook/internal/agent"
	"github.com/huangxinxinyu/nano-notebook/internal/agentbatch"
	"github.com/huangxinxinyu/nano-notebook/internal/agentobs/otelbridge"
	"github.com/huangxinxinyu/nano-notebook/internal/app"
	"github.com/huangxinxinyu/nano-notebook/internal/jobs"
	"github.com/huangxinxinyu/nano-notebook/internal/models"
	"github.com/huangxinxinyu/nano-notebook/internal/objectstore"
	"github.com/huangxinxinyu/nano-notebook/internal/platform/telemetry"
	"github.com/huangxinxinyu/nano-notebook/internal/replay"
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
	ReplayStagingS3       objectstore.S3Config
	ReplayKeyID           string
	ReplayKEK             []byte
}

type traceFlusher interface {
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
	stagingObjects, err := objectstore.NewS3Store(config.ReplayStagingS3)
	if err != nil {
		slog.Error("Replay staging object Store invalid", "error", err)
		os.Exit(1)
	}
	if err := stagingObjects.CheckReady(ctx); err != nil {
		slog.Error("Replay staging object Store unavailable", "error", err)
		os.Exit(1)
	}
	keyProvider, err := replay.NewDevelopmentKeyProvider(config.ReplayKeyID, config.ReplayKEK)
	if err != nil {
		slog.Error("Replay key provider invalid", "error", err)
		os.Exit(1)
	}
	sealer, err := replay.NewSealer(keyProvider)
	if err != nil {
		slog.Error("Replay envelope encryption invalid", "error", err)
		os.Exit(1)
	}
	replayStager, err := replay.NewPostgresStager(db.Pool(), sealer, stagingObjects, replay.StagerConfig{})
	if err != nil {
		slog.Error("Replay Stager invalid", "error", err)
		os.Exit(1)
	}
	stagingMaintenance, err := replay.NewStagingMaintenance(db.Pool(), stagingObjects, replay.StagingMaintenanceConfig{
		ReportError: func(err error) { slog.Error("Replay staging maintenance failed", "error", err) },
	})
	if err != nil {
		slog.Error("Replay staging maintenance invalid", "error", err)
		os.Exit(1)
	}
	batchHTTP, err := agentbatch.NewHTTPSender(agentbatch.HTTPSenderConfig{
		Endpoint: config.CollectorEndpoint, ServiceToken: config.CollectorServiceToken,
		HTTPClient: &http.Client{Timeout: config.HTTPTimeout, Transport: otelhttp.NewTransport(http.DefaultTransport)},
	})
	if err != nil {
		slog.Error("Agent Trace HTTP Sender invalid", "error", err)
		os.Exit(1)
	}
	traceExporter, err := agentbatch.NewExporter(agentbatch.Config{
		ProducerID: config.ProducerID, Sender: batchHTTP,
		MaxPendingRecords: 10_000, MaxPendingBytes: 32 * 1024 * 1024,
		MaxBatchRecords: config.MaxRecords, MaxBatchBytes: config.MaxEncodedBytes, MaxDelay: config.MaxDelay,
	})
	if err != nil {
		slog.Error("Agent Trace memory exporter invalid", "error", err)
		os.Exit(1)
	}
	runtime := agent.NewPostgresRuntime(db.Pool(), agent.BareSystemPrompt, nil,
		agent.WithTraceSink(traceExporter), agent.WithBestEffortTraceExporter(traceBridge), agent.WithReplayStager(replayStager))
	registry, err := agent.NewActionRegistry(agent.NewCalculateAction(), agent.NewCurrentTimeAction(nil))
	if err != nil {
		slog.Error("worker Action registry invalid", "error", err)
		os.Exit(1)
	}
	controller := agent.NewController(runtime, modelClient, registry)
	workerService := agentworker.NewService(db.Pool(), jobs.NewQueueWithTraceSink(db.Pool(), traceExporter), controller, 5*time.Second, 210*time.Second)
	workerDone := make(chan error, 1)
	go func() {
		err := workerService.Run(ctx)
		workerDone <- err
		if err != nil && ctx.Err() == nil {
			slog.Error("agent worker failed", "error", err)
			stop()
		}
	}()
	stagingMaintenanceDone := make(chan error, 1)
	go func() { stagingMaintenanceDone <- stagingMaintenance.Run(ctx) }()

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
	case err := <-stagingMaintenanceDone:
		if err != nil {
			slog.Error("Replay staging maintenance shutdown failed", "error", err)
			os.Exit(1)
		}
	case <-shutdownCtx.Done():
		slog.Error("Replay staging maintenance did not stop before shutdown", "error", shutdownCtx.Err())
		os.Exit(1)
	}
	if err := shutdownTraceExporter(shutdownCtx, traceExporter); err != nil {
		slog.Warn("Agent Trace memory flush incomplete; bounded unsent records were dropped on process exit", "error", err)
	}
	slog.Info("worker stopped")
}

func shutdownTraceExporter(ctx context.Context, exporter interface {
	traceFlusher
	Shutdown(context.Context) error
}) error {
	if err := exporter.ForceFlush(ctx); err != nil {
		return err
	}
	return exporter.Shutdown(ctx)
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
	replayUseTLS, err := workerEnvBool("NANO_REPLAY_STAGING_S3_USE_TLS", false)
	if err != nil {
		return workerConfig{}, err
	}
	replayKEK, err := base64.StdEncoding.DecodeString(env("NANO_REPLAY_KEK_BASE64", "bmFuby1sb2NhbC1kZXYta2VrLTAwMDAwMDAwMDAwMDA="))
	if err != nil {
		return workerConfig{}, fmt.Errorf("parse NANO_REPLAY_KEK_BASE64: %w", err)
	}
	collectorURL := strings.TrimRight(env("NANO_COLLECTOR_URL", "http://127.0.0.1:8082"), "/")
	config := workerConfig{
		DatabaseURL:           env("NANO_DATABASE_URL", "postgres://nano:nano@localhost:55432/nano?sslmode=disable"),
		Addr:                  env("NANO_WORKER_ADDR", ":8081"),
		CollectorEndpoint:     collectorURL + "/internal/agent-observability/v2/batches",
		CollectorServiceToken: env("NANO_COLLECTOR_SERVICE_TOKEN", "nano-local-collector-token"),
		ProducerID:            env("NANO_COLLECTOR_PRODUCER_ID", "nano-worker"),
		MaxRecords:            maxRecords, MaxEncodedBytes: maxEncodedBytes, MaxTraces: maxTraces,
		LeaseDuration: leaseDuration, PollInterval: pollInterval, MaxDelay: maxDelay, HTTPTimeout: httpTimeout,
		BaseBackoff: baseBackoff, MaxBackoff: maxBackoff,
		ReplayStagingS3: objectstore.S3Config{
			Endpoint:        env("NANO_REPLAY_STAGING_S3_ENDPOINT", "127.0.0.1:59000"),
			AccessKeyID:     env("NANO_REPLAY_STAGING_S3_ACCESS_KEY_ID", "nano"),
			SecretAccessKey: env("NANO_REPLAY_STAGING_S3_SECRET_ACCESS_KEY", "nano-password"),
			Bucket:          env("NANO_REPLAY_STAGING_S3_BUCKET", "nano-agent-replay-staging"),
			Region:          env("NANO_REPLAY_STAGING_S3_REGION", "us-east-1"), UseTLS: replayUseTLS,
		},
		ReplayKeyID: env("NANO_REPLAY_KEY_ID", "nano-local-replay-key-v1"), ReplayKEK: replayKEK,
	}
	if strings.TrimSpace(config.DatabaseURL) == "" || strings.TrimSpace(config.Addr) == "" ||
		strings.TrimSpace(collectorURL) == "" || strings.TrimSpace(config.CollectorServiceToken) == "" ||
		strings.TrimSpace(config.ProducerID) == "" || config.MaxRecords < 1 ||
		config.MaxEncodedBytes < 1 || config.MaxTraces < 1 || config.LeaseDuration <= 0 ||
		config.PollInterval <= 0 || config.MaxDelay < 0 || config.HTTPTimeout <= 0 || config.BaseBackoff <= 0 ||
		config.MaxBackoff < config.BaseBackoff || strings.TrimSpace(config.ReplayStagingS3.Endpoint) == "" ||
		strings.TrimSpace(config.ReplayStagingS3.AccessKeyID) == "" || strings.TrimSpace(config.ReplayStagingS3.SecretAccessKey) == "" ||
		strings.TrimSpace(config.ReplayStagingS3.Bucket) == "" || strings.TrimSpace(config.ReplayKeyID) == "" || len(config.ReplayKEK) != 32 {
		return workerConfig{}, errors.New("worker configuration is incomplete or inconsistent")
	}
	return config, nil
}

func workerEnvBool(key string, fallback bool) (bool, error) {
	value := os.Getenv(key)
	if value == "" {
		return fallback, nil
	}
	parsed, err := strconv.ParseBool(value)
	if err != nil {
		return false, fmt.Errorf("parse %s: %w", key, err)
	}
	return parsed, nil
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
