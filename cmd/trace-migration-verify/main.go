package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/huangxinxinyu/nano-notebook/internal/app"
	"github.com/jackc/pgx/v5/pgxpool"
)

type traceMigrationVerificationConfig struct {
	ApplicationDatabaseURL string
	CollectorDatabaseURL   string
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	config, err := loadTraceMigrationVerificationConfig(os.Getenv)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	report, err := runTraceMigrationVerification(ctx, config)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Printf("verified %d Sprint 4 Traces and %d records in Collector\n", report.TraceCount, report.RecordCount)
}

func loadTraceMigrationVerificationConfig(getenv func(string) string) (traceMigrationVerificationConfig, error) {
	if getenv == nil {
		return traceMigrationVerificationConfig{}, errors.New("environment reader is required")
	}
	applicationURL := strings.TrimSpace(getenv("NANO_DATABASE_URL"))
	if applicationURL == "" {
		applicationURL = "postgres://nano:nano@localhost:55432/nano?sslmode=disable"
	}
	collectorURL := strings.TrimSpace(getenv("NANO_COLLECTOR_DATABASE_URL"))
	if collectorURL == "" {
		collectorURL = "postgres://nano_observability:nano-observability@localhost:55432/nano_observability?sslmode=disable"
	}
	applicationConfig, err := pgxpool.ParseConfig(applicationURL)
	if err != nil {
		return traceMigrationVerificationConfig{}, fmt.Errorf("parse Application verification database: %w", err)
	}
	collectorConfig, err := pgxpool.ParseConfig(collectorURL)
	if err != nil {
		return traceMigrationVerificationConfig{}, fmt.Errorf("parse Collector verification database: %w", err)
	}
	if strings.EqualFold(applicationConfig.ConnConfig.Host, collectorConfig.ConnConfig.Host) &&
		applicationConfig.ConnConfig.Port == collectorConfig.ConnConfig.Port &&
		applicationConfig.ConnConfig.Database == collectorConfig.ConnConfig.Database {
		return traceMigrationVerificationConfig{}, errors.New("Application and Collector verification databases must be independent")
	}
	return traceMigrationVerificationConfig{
		ApplicationDatabaseURL: applicationURL,
		CollectorDatabaseURL:   collectorURL,
	}, nil
}

func runTraceMigrationVerification(ctx context.Context, config traceMigrationVerificationConfig) (app.TraceMigrationVerificationReport, error) {
	applicationPool, err := pgxpool.New(ctx, config.ApplicationDatabaseURL)
	if err != nil {
		return app.TraceMigrationVerificationReport{}, fmt.Errorf("open Application verification database: %w", err)
	}
	defer applicationPool.Close()
	if err := applicationPool.Ping(ctx); err != nil {
		return app.TraceMigrationVerificationReport{}, fmt.Errorf("ping Application verification database: %w", err)
	}
	collectorPool, err := pgxpool.New(ctx, config.CollectorDatabaseURL)
	if err != nil {
		return app.TraceMigrationVerificationReport{}, fmt.Errorf("open Collector verification database: %w", err)
	}
	defer collectorPool.Close()
	if err := collectorPool.Ping(ctx); err != nil {
		return app.TraceMigrationVerificationReport{}, fmt.Errorf("ping Collector verification database: %w", err)
	}
	return app.VerifyCollectorTraceMigration(ctx, applicationPool, collectorPool)
}
