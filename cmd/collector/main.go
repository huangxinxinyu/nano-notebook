package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/huangxinxinyu/nano-notebook/internal/collector"
	"github.com/huangxinxinyu/nano-notebook/internal/objectstore"
	"github.com/huangxinxinyu/nano-notebook/internal/platform/telemetry"
	"github.com/jackc/pgx/v5/pgxpool"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
)

type collectorConfig struct {
	DatabaseURL                string
	DatabaseMaxConns           int32
	DatabaseMinConns           int32
	ProjectionDatabaseMaxConns int32
	ProjectionDatabaseMinConns int32
	QueryDatabaseMaxConns      int32
	QueryDatabaseMinConns      int32
	Addr                       string
	ServiceToken               string
	QueryToken                 string
	ProducerID                 string
	ProducerIDPrefix           string
	MaxBodyBytes               int64
	ReplayStagingS3            objectstore.S3Config
	ReplayS3                   objectstore.S3Config
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	config, err := loadConfig()
	if err != nil {
		slog.Error("Collector configuration invalid", "error", err)
		os.Exit(1)
	}
	if err := run(ctx, config); err != nil {
		slog.Error("Collector stopped with error", "error", err)
		os.Exit(1)
	}
	slog.Info("Collector stopped")
}

func loadConfig() (collectorConfig, error) {
	maxConns, err := envInt32("NANO_COLLECTOR_DATABASE_MAX_CONNS", 16)
	if err != nil {
		return collectorConfig{}, err
	}
	minConns, err := envInt32("NANO_COLLECTOR_DATABASE_MIN_CONNS", 2)
	if err != nil {
		return collectorConfig{}, err
	}
	projectionMaxConns, err := envInt32("NANO_COLLECTOR_PROJECTION_DATABASE_MAX_CONNS", 4)
	if err != nil {
		return collectorConfig{}, err
	}
	projectionMinConns, err := envInt32("NANO_COLLECTOR_PROJECTION_DATABASE_MIN_CONNS", 1)
	if err != nil {
		return collectorConfig{}, err
	}
	queryMaxConns, err := envInt32("NANO_COLLECTOR_QUERY_DATABASE_MAX_CONNS", 8)
	if err != nil {
		return collectorConfig{}, err
	}
	queryMinConns, err := envInt32("NANO_COLLECTOR_QUERY_DATABASE_MIN_CONNS", 1)
	if err != nil {
		return collectorConfig{}, err
	}
	maxBodyBytes, err := envInt64("NANO_COLLECTOR_MAX_BODY_BYTES", 2*1024*1024)
	if err != nil {
		return collectorConfig{}, err
	}
	stagingUseTLS, err := envBool("NANO_REPLAY_STAGING_S3_USE_TLS", false)
	if err != nil {
		return collectorConfig{}, err
	}
	replayUseTLS, err := envBool("NANO_REPLAY_S3_USE_TLS", false)
	if err != nil {
		return collectorConfig{}, err
	}
	config := collectorConfig{
		DatabaseURL:                env("NANO_COLLECTOR_DATABASE_URL", "postgres://nano_observability:nano-observability@localhost:55432/nano_observability?sslmode=disable"),
		DatabaseMaxConns:           maxConns,
		DatabaseMinConns:           minConns,
		ProjectionDatabaseMaxConns: projectionMaxConns,
		ProjectionDatabaseMinConns: projectionMinConns,
		QueryDatabaseMaxConns:      queryMaxConns,
		QueryDatabaseMinConns:      queryMinConns,
		Addr:                       env("NANO_COLLECTOR_ADDR", ":8082"),
		ServiceToken:               env("NANO_COLLECTOR_SERVICE_TOKEN", "nano-local-collector-token"),
		QueryToken:                 env("NANO_COLLECTOR_QUERY_TOKEN", "nano-local-collector-query-token"),
		ProducerID:                 env("NANO_COLLECTOR_PRODUCER_ID", "nano-worker"),
		ProducerIDPrefix:           env("NANO_COLLECTOR_PRODUCER_ID_PREFIX", "nano-"),
		MaxBodyBytes:               maxBodyBytes,
		ReplayStagingS3: objectstore.S3Config{
			Endpoint:        env("NANO_REPLAY_STAGING_S3_ENDPOINT", "127.0.0.1:59000"),
			AccessKeyID:     env("NANO_REPLAY_STAGING_S3_ACCESS_KEY_ID", "nano"),
			SecretAccessKey: env("NANO_REPLAY_STAGING_S3_SECRET_ACCESS_KEY", "nano-password"),
			Bucket:          env("NANO_REPLAY_STAGING_S3_BUCKET", "nano-agent-replay-staging"),
			Region:          env("NANO_REPLAY_STAGING_S3_REGION", "us-east-1"), UseTLS: stagingUseTLS,
		},
		ReplayS3: objectstore.S3Config{
			Endpoint:        env("NANO_REPLAY_S3_ENDPOINT", "127.0.0.1:59000"),
			AccessKeyID:     env("NANO_REPLAY_S3_ACCESS_KEY_ID", "nano"),
			SecretAccessKey: env("NANO_REPLAY_S3_SECRET_ACCESS_KEY", "nano-password"),
			Bucket:          env("NANO_REPLAY_S3_BUCKET", "nano-agent-replay"),
			Region:          env("NANO_REPLAY_S3_REGION", "us-east-1"), UseTLS: replayUseTLS,
		},
	}
	if strings.TrimSpace(config.DatabaseURL) == "" || strings.TrimSpace(config.Addr) == "" ||
		strings.TrimSpace(config.ServiceToken) == "" || strings.TrimSpace(config.QueryToken) == "" ||
		(strings.TrimSpace(config.ProducerID) == "" && strings.TrimSpace(config.ProducerIDPrefix) == "") ||
		config.DatabaseMaxConns < 1 || config.DatabaseMinConns < 0 ||
		config.DatabaseMinConns > config.DatabaseMaxConns || config.MaxBodyBytes < 1 ||
		config.ProjectionDatabaseMaxConns < 1 || config.ProjectionDatabaseMinConns < 0 ||
		config.ProjectionDatabaseMinConns > config.ProjectionDatabaseMaxConns ||
		config.QueryDatabaseMaxConns < 1 || config.QueryDatabaseMinConns < 0 ||
		config.QueryDatabaseMinConns > config.QueryDatabaseMaxConns ||
		strings.TrimSpace(config.ReplayStagingS3.Endpoint) == "" || strings.TrimSpace(config.ReplayStagingS3.AccessKeyID) == "" ||
		strings.TrimSpace(config.ReplayStagingS3.SecretAccessKey) == "" || strings.TrimSpace(config.ReplayStagingS3.Bucket) == "" ||
		strings.TrimSpace(config.ReplayS3.Endpoint) == "" || strings.TrimSpace(config.ReplayS3.AccessKeyID) == "" ||
		strings.TrimSpace(config.ReplayS3.SecretAccessKey) == "" || strings.TrimSpace(config.ReplayS3.Bucket) == "" {
		return collectorConfig{}, errors.New("Collector configuration is incomplete or inconsistent")
	}
	return config, nil
}

func run(ctx context.Context, config collectorConfig) error {
	pool, err := openCollectorPool(ctx, config.DatabaseURL, config.DatabaseMaxConns, config.DatabaseMinConns)
	if err != nil {
		return fmt.Errorf("open Collector ingestion database: %w", err)
	}
	defer pool.Close()
	projectionPool, err := openCollectorPool(ctx, config.DatabaseURL, config.ProjectionDatabaseMaxConns, config.ProjectionDatabaseMinConns)
	if err != nil {
		return fmt.Errorf("open Collector projection database: %w", err)
	}
	defer projectionPool.Close()
	queryPool, err := openCollectorPool(ctx, config.DatabaseURL, config.QueryDatabaseMaxConns, config.QueryDatabaseMinConns)
	if err != nil {
		return fmt.Errorf("open Collector query database: %w", err)
	}
	defer queryPool.Close()
	if err := collector.RunMigrations(ctx, pool); err != nil {
		return fmt.Errorf("run Collector migrations: %w", err)
	}

	shutdownTelemetry, err := telemetry.Start(ctx, "nano-collector")
	if err != nil {
		return fmt.Errorf("start Collector telemetry: %w", err)
	}
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = shutdownTelemetry(shutdownCtx)
	}()
	telemetry.StartupSpan(ctx, "nano-collector")

	return runCollectorService(ctx, config, pool, projectionPool, queryPool)
}

func openCollectorPool(ctx context.Context, databaseURL string, maxConns, minConns int32) (*pgxpool.Pool, error) {
	poolConfig, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		return nil, fmt.Errorf("parse Collector database configuration: %w", err)
	}
	poolConfig.MaxConns = maxConns
	poolConfig.MinConns = minConns
	poolConfig.MaxConnLifetime = time.Hour
	pool, err := pgxpool.NewWithConfig(ctx, poolConfig)
	if err != nil {
		return nil, err
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, err
	}
	return pool, nil
}

func runCollectorService(ctx context.Context, config collectorConfig, pool, projectionPool, queryPool *pgxpool.Pool) error {
	stagingObjects, err := objectstore.NewS3Store(config.ReplayStagingS3)
	if err != nil {
		return fmt.Errorf("configure Collector staging object Store: %w", err)
	}
	replayObjects, err := objectstore.NewS3Store(config.ReplayS3)
	if err != nil {
		return fmt.Errorf("configure Collector Replay object Store: %w", err)
	}
	if err := stagingObjects.CheckReady(ctx); err != nil {
		return fmt.Errorf("check Collector staging object Store: %w", err)
	}
	if err := replayObjects.CheckReady(ctx); err != nil {
		return fmt.Errorf("check Collector Replay object Store: %w", err)
	}
	store, err := collector.NewPostgresStoreWithReplay(pool, stagingObjects, replayObjects)
	if err != nil {
		return err
	}
	replayMaintenance, err := collector.NewReplayMaintenance(pool, replayObjects, collector.ReplayMaintenanceConfig{
		ReportError: func(err error) { slog.Error("Collector Replay maintenance failed", "error", err) },
	})
	if err != nil {
		return err
	}
	projector, err := collector.NewProjector(projectionPool, collector.ProjectorConfig{
		ReportError: func(err error) { slog.Error("Collector projection failed", "error", err) },
	})
	if err != nil {
		return err
	}
	queryStore, err := collector.NewTraceQueryStore(queryPool, replayObjects)
	if err != nil {
		return err
	}
	ingestor, err := collector.NewIngestor(collector.IngestorConfig{
		ProducerID: config.ProducerID, ProducerIDPrefix: config.ProducerIDPrefix, Store: store,
	})
	if err != nil {
		return err
	}
	purger, err := collector.NewPurger(collector.PurgerConfig{ProducerID: config.ProducerID, Store: store})
	if err != nil {
		return err
	}
	handler, err := collector.NewHTTPHandler(collector.HTTPConfig{
		Ingestor: ingestor, Purger: purger, ServiceToken: config.ServiceToken, MaxBodyBytes: config.MaxBodyBytes,
		QueryStore: queryStore, QueryToken: config.QueryToken,
		Readiness: func(readyCtx context.Context) error {
			return errors.Join(pool.Ping(readyCtx), projectionPool.Ping(readyCtx), queryPool.Ping(readyCtx), stagingObjects.CheckReady(readyCtx), replayObjects.CheckReady(readyCtx))
		},
	})
	if err != nil {
		return err
	}
	service, err := collector.NewHTTPService(collector.HTTPServiceConfig{
		Handler: otelhttp.NewHandler(handler, "collector"), ReadHeaderTimeout: 5 * time.Second,
		ShutdownTimeout: 10 * time.Second,
	})
	if err != nil {
		return err
	}
	listener, err := (&net.ListenConfig{}).Listen(ctx, "tcp", config.Addr)
	if err != nil {
		return fmt.Errorf("listen for Collector HTTP: %w", err)
	}
	slog.Info("Collector listening", "addr", config.Addr, "producer_id", config.ProducerID, "producer_id_prefix", config.ProducerIDPrefix,
		"ingestion_database_max_connections", config.DatabaseMaxConns,
		"projection_database_max_connections", config.ProjectionDatabaseMaxConns,
		"query_database_max_connections", config.QueryDatabaseMaxConns, "max_body_bytes", config.MaxBodyBytes)
	maintenanceCtx, cancelMaintenance := context.WithCancel(ctx)
	maintenanceDone := make(chan error, 2)
	go func() { maintenanceDone <- replayMaintenance.Run(maintenanceCtx) }()
	go func() { maintenanceDone <- projector.Run(maintenanceCtx) }()
	serviceErr := service.Run(ctx, listener)
	cancelMaintenance()
	maintenanceErr := errors.Join(<-maintenanceDone, <-maintenanceDone)
	return errors.Join(serviceErr, maintenanceErr)
}

func envInt32(key string, fallback int32) (int32, error) {
	value := os.Getenv(key)
	if value == "" {
		return fallback, nil
	}
	parsed, err := strconv.ParseInt(value, 10, 32)
	if err != nil {
		return 0, fmt.Errorf("parse %s: %w", key, err)
	}
	return int32(parsed), nil
}

func envInt64(key string, fallback int64) (int64, error) {
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

func envBool(key string, fallback bool) (bool, error) {
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

func env(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}
