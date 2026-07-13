package main

import (
	"context"
	"log/slog"
	"os"

	"github.com/huangxinxinyu/nano-notebook/internal/app"
)

func main() {
	dsn := os.Getenv("NANO_DATABASE_URL")
	if dsn == "" {
		dsn = "postgres://nano:nano@localhost:55432/nano?sslmode=disable"
	}
	ctx := context.Background()
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
	slog.Info("migrations applied")
}
