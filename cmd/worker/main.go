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
	"github.com/huangxinxinyu/nano-notebook/internal/agentoutbox"
	"github.com/huangxinxinyu/nano-notebook/internal/app"
	"github.com/huangxinxinyu/nano-notebook/internal/evidence"
	"github.com/huangxinxinyu/nano-notebook/internal/jobs"
	"github.com/huangxinxinyu/nano-notebook/internal/models"
	"github.com/huangxinxinyu/nano-notebook/internal/objectstore"
	"github.com/huangxinxinyu/nano-notebook/internal/platform/telemetry"
	"github.com/huangxinxinyu/nano-notebook/internal/qdrantstore"
	"github.com/huangxinxinyu/nano-notebook/internal/replay"
	"github.com/huangxinxinyu/nano-notebook/internal/sourcejobs"
	"github.com/huangxinxinyu/nano-notebook/internal/sourceprocessing"
	"github.com/huangxinxinyu/nano-notebook/internal/sourceprojection"
	"github.com/huangxinxinyu/nano-notebook/internal/sourcepurge"
	agentworker "github.com/huangxinxinyu/nano-notebook/internal/worker"
	"github.com/huangxinxinyu/nano-notebook/internal/workload"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel"
)

type workerConfig struct {
	DatabaseURL                 string
	Addr                        string
	CollectorEndpoint           string
	CollectorServiceToken       string
	ProducerID                  string
	BatchMaxRecords             int
	BatchMaxEncodedBytes        int
	BatchMaxDelay               time.Duration
	HTTPTimeout                 time.Duration
	PurgeMaxCommands            int
	PurgeLeaseDuration          time.Duration
	PurgePollInterval           time.Duration
	PurgeBaseBackoff            time.Duration
	PurgeMaxBackoff             time.Duration
	ReplayStagingS3             objectstore.S3Config
	SourceS3                    objectstore.S3Config
	SourcePurgeLease            time.Duration
	SourcePurgePoll             time.Duration
	QdrantURL                   string
	QdrantAPIKey                string
	QdrantCollection            string
	QdrantDenseDimensions       int
	SourceProcessingLease       time.Duration
	SourceProcessingHeartbeat   time.Duration
	SourceProcessingPoll        time.Duration
	SourceExtractionConfigID    string
	SourceVisionModel           string
	SourceTranscriptionModel    string
	SourceVisionPromptVersion   string
	AgentVerifierModel          string
	AgentVerifierPrompt         string
	SourceProcessingMaxBytes    int64
	SourceProcessingMaxRunes    int
	AgentInteractiveConcurrency int
	SourceProcessingConcurrency int
	ReplayKeyID                 string
	ReplayKEK                   []byte
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
	sourceObjects, err := objectstore.NewS3Store(config.SourceS3)
	if err != nil {
		slog.Error("Source object Store invalid", "error", err)
		os.Exit(1)
	}
	if err := sourceObjects.CheckReady(ctx); err != nil {
		slog.Error("Source object Store unavailable", "error", err)
		os.Exit(1)
	}
	qdrant, err := qdrantstore.New(qdrantstore.Config{
		BaseURL: config.QdrantURL, APIKey: config.QdrantAPIKey, Collection: config.QdrantCollection,
		DenseDimensions: config.QdrantDenseDimensions, RequestTimeout: config.HTTPTimeout,
		HTTPClient: &http.Client{Timeout: config.HTTPTimeout, Transport: otelhttp.NewTransport(http.DefaultTransport)},
	})
	if err != nil {
		slog.Error("Qdrant projection Store invalid", "error", err)
		os.Exit(1)
	}
	if err := qdrant.EnsureCollection(ctx); err != nil {
		slog.Error("Qdrant projection Store unavailable", "error", err)
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
	replayStager, err := replay.NewObjectStager(sealer, stagingObjects, replay.StagerConfig{})
	if err != nil {
		slog.Error("Replay Stager invalid", "error", err)
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
		MaxBatchRecords: config.BatchMaxRecords, MaxBatchBytes: config.BatchMaxEncodedBytes, MaxDelay: config.BatchMaxDelay,
	})
	if err != nil {
		slog.Error("Agent Trace memory exporter invalid", "error", err)
		os.Exit(1)
	}
	purgePostgres, err := agentoutbox.NewPurgeStore(db.Pool(), agentoutbox.PurgeStoreConfig{
		ProducerID: config.ProducerID, MaxCommands: config.PurgeMaxCommands,
		LeaseDuration: config.PurgeLeaseDuration,
		BaseBackoff:   config.PurgeBaseBackoff, MaxBackoff: config.PurgeMaxBackoff,
		StagingObjects: stagingObjects,
	})
	if err != nil {
		slog.Error("Agent Trace purge Store invalid", "error", err)
		os.Exit(1)
	}
	purgeSender, err := agentoutbox.NewPurgeSender(purgePostgres, agentoutbox.SenderConfig{
		PurgeEndpoint: strings.TrimSuffix(config.CollectorEndpoint, "/v2/batches") + "/v1/purges",
		ServiceToken:  config.CollectorServiceToken,
		HTTPClient:    &http.Client{Timeout: config.HTTPTimeout, Transport: otelhttp.NewTransport(http.DefaultTransport)},
		ReportError: func(err error) {
			slog.Error("Agent Trace purge delivery failed; durable command retained", "error", err)
		},
	})
	if err != nil {
		slog.Error("Agent Trace purge Sender invalid", "error", err)
		os.Exit(1)
	}
	grounder := agent.NewGroundingService(db.Pool(), modelClient, modelClient, agent.GroundingConfig{
		VerifierModel: config.AgentVerifierModel, VerifierPromptVersion: config.AgentVerifierPrompt,
	})
	runtime := agent.NewPostgresRuntime(db.Pool(), agent.BareSystemPrompt, nil,
		agent.WithTraceSink(traceExporter), agent.WithBestEffortTraceExporter(traceBridge),
		agent.WithReplayStager(replayStager), agent.WithGroundingService(grounder))
	evidenceSearch := agent.NewEvidenceSearchService(db.Pool(), qdrant, modelClient)
	registry, err := agent.NewActionRegistry(
		agent.NewCalculateAction(), agent.NewCurrentTimeAction(nil), agent.NewSearchEvidenceAction(evidenceSearch),
	)
	if err != nil {
		slog.Error("worker Action registry invalid", "error", err)
		os.Exit(1)
	}
	controller := agent.NewController(runtime, modelClient, registry)
	workerService := agentworker.NewServiceWithConcurrency(db.Pool(), jobs.NewQueueWithTraceSink(db.Pool(), traceExporter), controller, 5*time.Second, 210*time.Second, config.AgentInteractiveConcurrency)
	workerDone := make(chan error, 1)
	go func() {
		err := workerService.Run(ctx)
		workerDone <- err
		if err != nil && ctx.Err() == nil {
			slog.Error("agent worker failed", "error", err)
			stop()
		}
	}()
	purgeDone := make(chan error, 1)
	go func() { purgeDone <- purgeSender.Run(ctx, config.PurgePollInterval) }()
	sourcePurgeDone := make(chan error, 1)
	sourcePurgeProcessor := sourcepurge.NewProcessorWithProjectionPurger(db.Pool(), sourceObjects, qdrant, config.SourcePurgeLease)
	go func() { sourcePurgeDone <- sourcePurgeProcessor.Run(ctx, config.SourcePurgePoll) }()
	sourceQueue := sourcejobs.NewQueue(db.Pool(), config.SourceProcessingLease)
	sourceProcessor := sourceprocessing.NewProcessorWithExtractorAndTrace(
		db.Pool(), sourceQueue, evidence.NewPublisher(db.Pool(), sourceObjects), sourceObjects,
		sourceprojection.New(db.Pool(), qdrant, modelClient),
		sourceprocessing.NewNativeExtractor(modelClient, sourceprocessing.NativeExtractorConfig{
			VisionModel: config.SourceVisionModel, TranscriptionModel: config.SourceTranscriptionModel,
			VisionPromptVersion: config.SourceVisionPromptVersion,
		}), traceExporter,
		sourceprocessing.Config{
			ExtractionConfigID: config.SourceExtractionConfigID,
			ExtractorAdapterID: "native-in-process",
			MaxSourceBytes:     config.SourceProcessingMaxBytes, MaxNormalizedRunes: config.SourceProcessingMaxRunes,
		},
	)
	sourceProcessingService := sourceprocessing.NewServiceWithConcurrency(
		sourceQueue, sourceProcessor, config.SourceProcessingHeartbeat, config.SourceProcessingPoll, config.SourceProcessingConcurrency,
	)
	sourceProcessingDone := make(chan error, 1)
	go func() { sourceProcessingDone <- sourceProcessingService.Run(ctx) }()

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
	case err := <-purgeDone:
		if err != nil {
			slog.Error("Agent Trace purge Sender shutdown failed", "error", err)
			os.Exit(1)
		}
	case <-shutdownCtx.Done():
		slog.Error("Agent Trace purge Sender did not stop before shutdown", "error", shutdownCtx.Err())
		os.Exit(1)
	}
	select {
	case err := <-sourcePurgeDone:
		if err != nil {
			slog.Error("Source purge Processor shutdown failed", "error", err)
			os.Exit(1)
		}
	case <-shutdownCtx.Done():
		slog.Error("Source purge Processor did not stop before shutdown", "error", shutdownCtx.Err())
		os.Exit(1)
	}
	select {
	case err := <-sourceProcessingDone:
		if err != nil {
			slog.Error("Source processing Service shutdown failed", "error", err)
			os.Exit(1)
		}
	case <-shutdownCtx.Done():
		slog.Error("Source processing Service did not stop before shutdown", "error", shutdownCtx.Err())
		os.Exit(1)
	}
	if err := purgeSender.ForceFlush(shutdownCtx); err != nil {
		slog.Warn("Agent Trace purge flush incomplete; durable command remains for restart", "error", err)
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
	maxRecords, err := workerEnvInt("NANO_TRACE_BATCH_MAX_RECORDS", 128)
	if err != nil {
		return workerConfig{}, err
	}
	maxEncodedBytes, err := workerEnvInt("NANO_TRACE_BATCH_MAX_ENCODED_BYTES", 512*1024)
	if err != nil {
		return workerConfig{}, err
	}
	purgeMaxCommands, err := workerEnvInt("NANO_TRACE_PURGE_MAX_COMMANDS", 16)
	if err != nil {
		return workerConfig{}, err
	}
	purgeLeaseDuration, err := workerEnvDuration("NANO_TRACE_PURGE_LEASE_DURATION", 30*time.Second)
	if err != nil {
		return workerConfig{}, err
	}
	purgePollInterval, err := workerEnvDuration("NANO_TRACE_PURGE_POLL_INTERVAL", 100*time.Millisecond)
	if err != nil {
		return workerConfig{}, err
	}
	maxDelay, err := workerEnvDuration("NANO_TRACE_BATCH_MAX_DELAY", 250*time.Millisecond)
	if err != nil {
		return workerConfig{}, err
	}
	httpTimeout, err := workerEnvDuration("NANO_TRACE_HTTP_TIMEOUT", 10*time.Second)
	if err != nil {
		return workerConfig{}, err
	}
	purgeBaseBackoff, err := workerEnvDuration("NANO_TRACE_PURGE_BASE_BACKOFF", time.Second)
	if err != nil {
		return workerConfig{}, err
	}
	purgeMaxBackoff, err := workerEnvDuration("NANO_TRACE_PURGE_MAX_BACKOFF", time.Minute)
	if err != nil {
		return workerConfig{}, err
	}
	replayUseTLS, err := workerEnvBool("NANO_REPLAY_STAGING_S3_USE_TLS", false)
	if err != nil {
		return workerConfig{}, err
	}
	sourceUseTLS, err := workerEnvBool("NANO_SOURCE_S3_USE_TLS", false)
	if err != nil {
		return workerConfig{}, err
	}
	sourcePurgeLease, err := workerEnvDuration("NANO_SOURCE_PURGE_LEASE_DURATION", 30*time.Second)
	if err != nil {
		return workerConfig{}, err
	}
	sourcePurgePoll, err := workerEnvDuration("NANO_SOURCE_PURGE_POLL_INTERVAL", time.Second)
	if err != nil {
		return workerConfig{}, err
	}
	sourceProcessingLease, err := workerEnvDuration("NANO_SOURCE_PROCESSING_LEASE_DURATION", 2*time.Minute)
	if err != nil {
		return workerConfig{}, err
	}
	sourceProcessingHeartbeat, err := workerEnvDuration("NANO_SOURCE_PROCESSING_HEARTBEAT_INTERVAL", 30*time.Second)
	if err != nil {
		return workerConfig{}, err
	}
	sourceProcessingPoll, err := workerEnvDuration("NANO_SOURCE_PROCESSING_POLL_INTERVAL", time.Second)
	if err != nil {
		return workerConfig{}, err
	}
	qdrantDenseDimensions, err := workerEnvInt("NANO_QDRANT_DENSE_DIMENSIONS", 1024)
	if err != nil {
		return workerConfig{}, err
	}
	sourceProcessingMaxBytes, err := workerEnvInt("NANO_SOURCE_PROCESSING_MAX_BYTES", 100*1024*1024)
	if err != nil {
		return workerConfig{}, err
	}
	sourceProcessingMaxRunes, err := workerEnvInt("NANO_SOURCE_PROCESSING_MAX_RUNES", 20_000_000)
	if err != nil {
		return workerConfig{}, err
	}
	agentInteractiveConcurrency, err := workerEnvInt("NANO_AGENT_INTERACTIVE_CONCURRENCY", workload.DefaultAgentConcurrency)
	if err != nil {
		return workerConfig{}, err
	}
	sourceProcessingConcurrency, err := workerEnvInt("NANO_SOURCE_PROCESSING_CONCURRENCY", workload.DefaultSourceConcurrency)
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
		BatchMaxRecords:       maxRecords, BatchMaxEncodedBytes: maxEncodedBytes, BatchMaxDelay: maxDelay,
		HTTPTimeout: httpTimeout, PurgeMaxCommands: purgeMaxCommands,
		PurgeLeaseDuration: purgeLeaseDuration, PurgePollInterval: purgePollInterval,
		PurgeBaseBackoff: purgeBaseBackoff, PurgeMaxBackoff: purgeMaxBackoff,
		ReplayStagingS3: objectstore.S3Config{
			Endpoint:        env("NANO_REPLAY_STAGING_S3_ENDPOINT", "127.0.0.1:59000"),
			AccessKeyID:     env("NANO_REPLAY_STAGING_S3_ACCESS_KEY_ID", "nano"),
			SecretAccessKey: env("NANO_REPLAY_STAGING_S3_SECRET_ACCESS_KEY", "nano-password"),
			Bucket:          env("NANO_REPLAY_STAGING_S3_BUCKET", "nano-agent-replay-staging"),
			Region:          env("NANO_REPLAY_STAGING_S3_REGION", "us-east-1"), UseTLS: replayUseTLS,
		},
		SourceS3: objectstore.S3Config{
			Endpoint:        env("NANO_SOURCE_S3_ENDPOINT", "127.0.0.1:59000"),
			AccessKeyID:     env("NANO_SOURCE_S3_ACCESS_KEY_ID", "nano"),
			SecretAccessKey: env("NANO_SOURCE_S3_SECRET_ACCESS_KEY", "nano-password"),
			Bucket:          env("NANO_SOURCE_S3_BUCKET", "nano-sources"),
			Region:          env("NANO_SOURCE_S3_REGION", "us-east-1"), UseTLS: sourceUseTLS,
		},
		SourcePurgeLease: sourcePurgeLease, SourcePurgePoll: sourcePurgePoll,
		QdrantURL:             env("NANO_QDRANT_URL", "http://127.0.0.1:56333"),
		QdrantAPIKey:          strings.TrimSpace(os.Getenv("NANO_QDRANT_API_KEY")),
		QdrantCollection:      env("NANO_QDRANT_COLLECTION", "nano-source-evidence"),
		QdrantDenseDimensions: qdrantDenseDimensions,
		SourceProcessingLease: sourceProcessingLease, SourceProcessingHeartbeat: sourceProcessingHeartbeat,
		SourceProcessingPoll: sourceProcessingPoll, SourceExtractionConfigID: env("NANO_SOURCE_EXTRACTION_CONFIG_ID", "extract-text-v1"),
		SourceVisionModel:         env("NANO_SOURCE_VISION_MODEL", "gemini/gemini-2.5-flash"),
		SourceTranscriptionModel:  env("NANO_SOURCE_TRANSCRIPTION_MODEL", "openai/whisper-1"),
		SourceVisionPromptVersion: env("NANO_SOURCE_VISION_PROMPT_VERSION", "vision-normalize-v1"),
		AgentVerifierModel:        env("NANO_AGENT_VERIFIER_MODEL", "aliyun/qwen-flash"),
		AgentVerifierPrompt:       env("NANO_AGENT_VERIFIER_PROMPT_VERSION", "claim-support-v1"),
		SourceProcessingMaxBytes:  int64(sourceProcessingMaxBytes), SourceProcessingMaxRunes: sourceProcessingMaxRunes,
		AgentInteractiveConcurrency: agentInteractiveConcurrency, SourceProcessingConcurrency: sourceProcessingConcurrency,
		ReplayKeyID: env("NANO_REPLAY_KEY_ID", "nano-local-replay-key-v1"), ReplayKEK: replayKEK,
	}
	if strings.TrimSpace(config.DatabaseURL) == "" || strings.TrimSpace(config.Addr) == "" ||
		strings.TrimSpace(collectorURL) == "" || strings.TrimSpace(config.CollectorServiceToken) == "" ||
		strings.TrimSpace(config.ProducerID) == "" || config.BatchMaxRecords < 1 ||
		config.BatchMaxEncodedBytes < 1 || config.BatchMaxDelay < 0 || config.HTTPTimeout <= 0 ||
		config.PurgeMaxCommands < 1 || config.PurgeLeaseDuration <= 0 || config.PurgePollInterval <= 0 ||
		config.PurgeBaseBackoff <= 0 || config.PurgeMaxBackoff < config.PurgeBaseBackoff || strings.TrimSpace(config.ReplayStagingS3.Endpoint) == "" ||
		strings.TrimSpace(config.ReplayStagingS3.AccessKeyID) == "" || strings.TrimSpace(config.ReplayStagingS3.SecretAccessKey) == "" ||
		strings.TrimSpace(config.ReplayStagingS3.Bucket) == "" || strings.TrimSpace(config.SourceS3.Endpoint) == "" ||
		strings.TrimSpace(config.SourceS3.AccessKeyID) == "" || strings.TrimSpace(config.SourceS3.SecretAccessKey) == "" ||
		strings.TrimSpace(config.SourceS3.Bucket) == "" || config.SourcePurgeLease <= 0 || config.SourcePurgePoll <= 0 ||
		strings.TrimSpace(config.QdrantURL) == "" || strings.TrimSpace(config.QdrantCollection) == "" || config.QdrantDenseDimensions <= 0 ||
		config.SourceProcessingLease <= 0 || config.SourceProcessingHeartbeat <= 0 || config.SourceProcessingHeartbeat >= config.SourceProcessingLease ||
		config.SourceProcessingPoll <= 0 || strings.TrimSpace(config.SourceExtractionConfigID) == "" ||
		strings.TrimSpace(config.SourceVisionModel) == "" || strings.TrimSpace(config.SourceTranscriptionModel) == "" ||
		strings.TrimSpace(config.SourceVisionPromptVersion) == "" ||
		strings.TrimSpace(config.AgentVerifierModel) == "" || strings.TrimSpace(config.AgentVerifierPrompt) == "" ||
		config.SourceProcessingMaxBytes <= 0 || config.SourceProcessingMaxBytes > 100*1024*1024 || config.SourceProcessingMaxRunes <= 0 ||
		workload.ValidateInteractiveCapacity(config.AgentInteractiveConcurrency, config.SourceProcessingConcurrency) != nil ||
		strings.TrimSpace(config.ReplayKeyID) == "" || len(config.ReplayKEK) != 32 {
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
