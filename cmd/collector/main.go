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
	"github.com/huangxinxinyu/nano-notebook/internal/platform/telemetry"
	"github.com/jackc/pgx/v5/pgxpool"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
)

type collectorConfig struct {
	DatabaseURL      string
	DatabaseMaxConns int32
	DatabaseMinConns int32
	Addr             string
	ServiceToken     string
	ProducerID       string
	MaxBodyBytes     int64
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
	maxBodyBytes, err := envInt64("NANO_COLLECTOR_MAX_BODY_BYTES", 2*1024*1024)
	if err != nil {
		return collectorConfig{}, err
	}
	config := collectorConfig{
		DatabaseURL:      env("NANO_COLLECTOR_DATABASE_URL", "postgres://nano_observability:nano-observability@localhost:55432/nano_observability?sslmode=disable"),
		DatabaseMaxConns: maxConns,
		DatabaseMinConns: minConns,
		Addr:             env("NANO_COLLECTOR_ADDR", ":8082"),
		ServiceToken:     env("NANO_COLLECTOR_SERVICE_TOKEN", "nano-local-collector-token"),
		ProducerID:       env("NANO_COLLECTOR_PRODUCER_ID", "nano-worker"),
		MaxBodyBytes:     maxBodyBytes,
	}
	if strings.TrimSpace(config.DatabaseURL) == "" || strings.TrimSpace(config.Addr) == "" ||
		strings.TrimSpace(config.ServiceToken) == "" || strings.TrimSpace(config.ProducerID) == "" ||
		config.DatabaseMaxConns < 1 || config.DatabaseMinConns < 0 ||
		config.DatabaseMinConns > config.DatabaseMaxConns || config.MaxBodyBytes < 1 {
		return collectorConfig{}, errors.New("Collector configuration is incomplete or inconsistent")
	}
	return config, nil
}

func run(ctx context.Context, config collectorConfig) error {
	poolConfig, err := pgxpool.ParseConfig(config.DatabaseURL)
	if err != nil {
		return fmt.Errorf("parse Collector database configuration: %w", err)
	}
	poolConfig.MaxConns = config.DatabaseMaxConns
	poolConfig.MinConns = config.DatabaseMinConns
	poolConfig.MaxConnLifetime = time.Hour
	pool, err := pgxpool.NewWithConfig(ctx, poolConfig)
	if err != nil {
		return fmt.Errorf("open Collector database: %w", err)
	}
	defer pool.Close()
	if err := pool.Ping(ctx); err != nil {
		return fmt.Errorf("ping Collector database: %w", err)
	}
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

	store := collector.NewPostgresStore(pool)
	ingestor, err := collector.NewIngestor(collector.IngestorConfig{ProducerID: config.ProducerID, Store: store})
	if err != nil {
		return err
	}
	handler, err := collector.NewHTTPHandler(collector.HTTPConfig{
		Ingestor: ingestor, ServiceToken: config.ServiceToken, MaxBodyBytes: config.MaxBodyBytes,
		Readiness: pool.Ping,
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
	slog.Info("Collector listening", "addr", config.Addr, "producer_id", config.ProducerID,
		"database_max_connections", config.DatabaseMaxConns, "max_body_bytes", config.MaxBodyBytes)
	return service.Run(ctx, listener)
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

func env(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}
