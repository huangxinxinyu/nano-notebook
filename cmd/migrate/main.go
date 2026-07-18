package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"

	"github.com/huangxinxinyu/nano-notebook/internal/app"
	"github.com/huangxinxinyu/nano-notebook/internal/collector"
	"github.com/jackc/pgx/v5/pgxpool"
)

type migrationConfig struct {
	ApplicationDatabaseURL string
	CollectorDatabaseURL   string
}

func main() {
	if err := runMigrations(context.Background(), loadMigrationConfig()); err != nil {
		slog.Error("migrations failed", "error", err)
		os.Exit(1)
	}
	slog.Info("Application and Collector migrations applied")
}

func loadMigrationConfig() migrationConfig {
	return migrationConfig{
		ApplicationDatabaseURL: env("NANO_DATABASE_URL", "postgres://nano:nano@localhost:55432/nano?sslmode=disable"),
		CollectorDatabaseURL:   env("NANO_COLLECTOR_DATABASE_URL", "postgres://nano_observability:nano-observability@localhost:55432/nano_observability?sslmode=disable"),
	}
}

func runMigrations(ctx context.Context, config migrationConfig) error {
	db, err := app.OpenDB(ctx, config.ApplicationDatabaseURL)
	if err != nil {
		return fmt.Errorf("open Application database: %w", err)
	}
	defer db.Close()
	if err := app.RunMigrations(ctx, db); err != nil {
		return fmt.Errorf("run Application migrations: %w", err)
	}
	collectorPool, err := pgxpool.New(ctx, config.CollectorDatabaseURL)
	if err != nil {
		return fmt.Errorf("open Collector database: %w", err)
	}
	defer collectorPool.Close()
	if err := collectorPool.Ping(ctx); err != nil {
		return fmt.Errorf("ping Collector database: %w", err)
	}
	if err := collector.RunMigrations(ctx, collectorPool); err != nil {
		return fmt.Errorf("run Collector migrations: %w", err)
	}
	return nil
}

func env(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}
