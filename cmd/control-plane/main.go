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
	"github.com/huangxinxinyu/nano-notebook/internal/agentcatalog"
	"github.com/huangxinxinyu/nano-notebook/internal/app"
	"github.com/huangxinxinyu/nano-notebook/internal/collector"
	"github.com/huangxinxinyu/nano-notebook/internal/objectstore"
	"github.com/huangxinxinyu/nano-notebook/internal/platform/metrics"
	"github.com/huangxinxinyu/nano-notebook/internal/platform/telemetry"
	"github.com/huangxinxinyu/nano-notebook/internal/realtime"
	"github.com/huangxinxinyu/nano-notebook/internal/replay"
	"github.com/huangxinxinyu/nano-notebook/internal/webreader"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
)

type controlPlaneConfig struct {
	DatabaseURL           string
	Addr                  string
	CollectorURL          string
	CollectorQueryToken   string
	ProducerID            string
	TraceKafkaBrokers     []string
	TraceKafkaTopic       string
	TraceKafkaClientID    string
	ReplayKeyID           string
	ReplayKEK             []byte
	CookieSecure          bool
	Version               string
	DefaultModel          string
	ResearchModel         string
	AgentConfigurationID  string
	AgentRelease          agentcatalog.Reference
	SourceS3              objectstore.S3Config
	WebReaderURL          string
	WebReaderServiceToken string
	WebReaderTimeout      time.Duration
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
	definitionCatalog, err := agentcatalog.LoadEmbedded()
	if err != nil {
		slog.Error("Control Plane Agent Catalog invalid", "error", err)
		os.Exit(1)
	}
	if _, err := app.VerifyAgentCatalogReady(ctx, db, definitionCatalog, config.AgentRelease); err != nil {
		slog.Error("Control Plane Agent Catalog readiness failed", "release", config.AgentRelease, "error", err)
		os.Exit(1)
	}
	runConfig := agent.DefaultRunConfig(config.AgentConfigurationID)
	promptSet, agentConfiguration, err := agent.DefaultAgentConfigurationBundle(config.AgentConfigurationID, config.DefaultModel, config.ResearchModel, runConfig)
	if err != nil {
		slog.Error("Agent Configuration registration failed", "error", err)
		os.Exit(1)
	}
	if err := app.RegisterAgentConfiguration(ctx, db, promptSet, agentConfiguration); err != nil {
		slog.Error("Agent Configuration registration failed", "error", err)
		os.Exit(1)
	}
	sourceStore, err := objectstore.NewS3Store(config.SourceS3)
	if err != nil {
		slog.Error("Source object Store configuration invalid", "error", err)
		os.Exit(1)
	}
	if err := sourceStore.CheckReady(ctx); err != nil {
		slog.Error("Source object Store unavailable", "error", err)
		os.Exit(1)
	}
	sourceReader, err := webreader.NewHTTPAdapter(webreader.HTTPConfig{
		Endpoint: config.WebReaderURL, ServiceToken: config.WebReaderServiceToken,
		HTTPClient: &http.Client{Timeout: config.WebReaderTimeout, Transport: otelhttp.NewTransport(http.DefaultTransport)},
	})
	if err != nil {
		slog.Error("Source web-reader client configuration invalid", "error", err)
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

	metricsRegistry := metrics.NewRegistry()
	metricsCatalog := metrics.NewCatalog(metricsRegistry)
	metricsAddr := env("NANO_CONTROL_PLANE_METRICS_ADDR", "0.0.0.0:9091")
	metricsServer := metrics.NewAdminServer(metricsAddr, metricsRegistry)
	go func() {
		if err := metricsServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.Error("control-plane metrics listener failed", "error", err)
		}
	}()
	go metrics.ObservePoolStats(ctx, metricsCatalog, "control_plane", 15*time.Second, func() metrics.PoolStat {
		stat := db.Pool().Stat()
		return metrics.PoolStat{
			AcquiredConns: stat.AcquiredConns(), IdleConns: stat.IdleConns(),
			TotalConns: stat.TotalConns(), MaxConns: stat.MaxConns(),
		}
	})

	queryClient, err := collector.NewHTTPQueryClient(collector.HTTPQueryClientConfig{
		Endpoint: config.CollectorURL, ServiceToken: config.CollectorQueryToken,
	})
	if err != nil {
		slog.Error("Collector Query client configuration invalid", "error", err)
		os.Exit(1)
	}
	traceSink, err := agentbatch.NewManagedKafkaTraceSink(ctx, agentbatch.ManagedKafkaTraceSinkConfig{
		ProducerID: config.ProducerID, Brokers: config.TraceKafkaBrokers,
		Topic: config.TraceKafkaTopic, ClientID: config.TraceKafkaClientID,
		MaxBufferedRecords: agentbatch.DefaultKafkaTraceMaxBufferedRecords,
		MaxBufferedBytes:   agentbatch.DefaultKafkaTraceMaxBufferedBytes,
		DeliveryTimeout:    agentbatch.DefaultKafkaTraceDeliveryTimeout,
		Linger:             agentbatch.DefaultKafkaTraceLinger, ReadinessTimeout: agentbatch.DefaultKafkaTraceReadinessTimeout,
		MaxMessageBytes: agentbatch.DefaultKafkaTraceMaxMessageBytes, Observer: metricsCatalog,
	})
	if err != nil {
		slog.Error("Agent Trace Kafka Sink unavailable", "error", err)
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
		AgentRun: runConfig, AgentConfiguration: agentConfiguration,
		AgentCatalog: definitionCatalog, AgentRelease: config.AgentRelease,
		AdminTraces: queryClient, AdminTraceAnalytics: queryClient, ReplaySealer: replaySealer, TraceSink: traceSink,
		SourceUploads: sourceStore,
		SourceReader:  sourceReader, SourceSnapshots: sourceStore,
		Metrics: metricsCatalog,
	}, db)
	runListener := realtime.NewRunListener(db.Pool(), server.NotifyRun)
	go func() {
		if err := runListener.Run(ctx); err != nil && ctx.Err() == nil {
			slog.Error("Run projection listener failed", "error", err)
			stop()
		}
	}()
	sourceListener := realtime.NewSourceListener(db.Pool(), server.NotifySourceDiscovery, server.NotifyNotebookSources)
	go func() {
		if err := sourceListener.Run(ctx); err != nil && ctx.Err() == nil {
			slog.Error("Source projection listener failed", "error", err)
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
	if err := metricsServer.Shutdown(shutdownCtx); err != nil {
		slog.Warn("control-plane metrics listener shutdown incomplete", "error", err)
	}
	if err := traceSink.Shutdown(shutdownCtx); err != nil {
		slog.Warn("Agent Trace Kafka flush incomplete; buffered records may be lost on process exit", "error", err)
	}
	slog.Info("control-plane stopped")
}

func loadControlPlaneConfig() (controlPlaneConfig, error) {
	replayKEK, err := base64.StdEncoding.DecodeString(env("NANO_REPLAY_KEK_BASE64", "bmFuby1sb2NhbC1kZXYta2VrLTAwMDAwMDAwMDAwMDA="))
	if err != nil {
		return controlPlaneConfig{}, fmt.Errorf("parse NANO_REPLAY_KEK_BASE64: %w", err)
	}
	sourceUseTLS, err := strconv.ParseBool(env("NANO_SOURCE_S3_USE_TLS", "false"))
	if err != nil {
		return controlPlaneConfig{}, fmt.Errorf("parse NANO_SOURCE_S3_USE_TLS: %w", err)
	}
	agentRelease, err := agentcatalog.ParseReference(env("NANO_AGENT_RELEASE", "nano.default@26"))
	if err != nil {
		return controlPlaneConfig{}, fmt.Errorf("parse NANO_AGENT_RELEASE: %w", err)
	}
	webReaderTimeout, err := time.ParseDuration(env("NANO_WEB_READER_TIMEOUT", "90s"))
	if err != nil {
		return controlPlaneConfig{}, fmt.Errorf("parse NANO_WEB_READER_TIMEOUT: %w", err)
	}
	config := controlPlaneConfig{
		DatabaseURL:         env("NANO_DATABASE_URL", "postgres://nano:nano@localhost:55432/nano?sslmode=disable"),
		Addr:                env("NANO_CONTROL_PLANE_ADDR", ":8080"),
		CollectorURL:        strings.TrimRight(env("NANO_COLLECTOR_URL", "http://127.0.0.1:8082"), "/"),
		CollectorQueryToken: env("NANO_COLLECTOR_QUERY_TOKEN", "nano-local-collector-query-token"),
		ProducerID:          env("NANO_CONTROL_PLANE_PRODUCER_ID", "nano-control-plane"),
		TraceKafkaBrokers:   splitTraceKafkaBrokers(env("NANO_AGENT_TRACE_KAFKA_BROKERS", "127.0.0.1:59092")),
		TraceKafkaTopic:     env("NANO_AGENT_TRACE_KAFKA_TOPIC", "nano.observability.agent-trace.v1"),
		TraceKafkaClientID:  env("NANO_AGENT_TRACE_KAFKA_CLIENT_ID", "nano-control-plane-agent-trace"),
		ReplayKeyID:         env("NANO_REPLAY_KEY_ID", "nano-local-replay-key-v1"), ReplayKEK: replayKEK,
		CookieSecure: os.Getenv("NANO_COOKIE_SECURE") == "true", Version: env("NANO_VERSION", "dev"),
		DefaultModel:         env("NANO_CHAT_MODEL", "aliyun/qwen-plus"),
		ResearchModel:        env("NANO_RESEARCH_MODEL", env("NANO_CHAT_MODEL", "aliyun/qwen-plus")),
		AgentConfigurationID: env("NANO_AGENT_CONFIGURATION_ID", "nano-interactive-v1"),
		AgentRelease:         agentRelease,
		SourceS3: objectstore.S3Config{
			Endpoint:        env("NANO_SOURCE_S3_ENDPOINT", "127.0.0.1:59000"),
			AccessKeyID:     env("NANO_SOURCE_S3_ACCESS_KEY_ID", "nano"),
			SecretAccessKey: env("NANO_SOURCE_S3_SECRET_ACCESS_KEY", "nano-password"),
			Bucket:          env("NANO_SOURCE_S3_BUCKET", "nano-sources"),
			Region:          env("NANO_SOURCE_S3_REGION", "us-east-1"),
			UseTLS:          sourceUseTLS,
		},
		WebReaderURL:          strings.TrimRight(env("NANO_WEB_READER_URL", "http://127.0.0.1:8085"), "/"),
		WebReaderServiceToken: env("NANO_WEB_READER_SERVICE_TOKEN", "nano-local-reader-token"),
		WebReaderTimeout:      webReaderTimeout,
	}
	if strings.TrimSpace(config.DatabaseURL) == "" || strings.TrimSpace(config.Addr) == "" ||
		config.AgentRelease.Identity == "" || strings.TrimSpace(config.CollectorURL) == "" || strings.TrimSpace(config.CollectorQueryToken) == "" ||
		strings.TrimSpace(config.ProducerID) == "" ||
		strings.TrimSpace(config.ReplayKeyID) == "" || strings.TrimSpace(config.WebReaderURL) == "" ||
		strings.TrimSpace(config.WebReaderServiceToken) == "" || config.WebReaderTimeout <= 0 || len(config.ReplayKEK) != 32 {
		return controlPlaneConfig{}, errors.New("Control Plane configuration is incomplete")
	}
	if len(config.TraceKafkaBrokers) == 0 || strings.TrimSpace(config.TraceKafkaTopic) == "" || strings.TrimSpace(config.TraceKafkaClientID) == "" {
		return controlPlaneConfig{}, errors.New("Control Plane Agent Trace Kafka configuration is incomplete")
	}
	return config, nil
}

func splitTraceKafkaBrokers(value string) []string {
	var brokers []string
	for _, broker := range strings.Split(value, ",") {
		if broker = strings.TrimSpace(broker); broker != "" {
			brokers = append(brokers, broker)
		}
	}
	return brokers
}

func env(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}
