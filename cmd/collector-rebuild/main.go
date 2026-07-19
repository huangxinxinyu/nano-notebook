package main

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/huangxinxinyu/nano-notebook/internal/agentobs"
	"github.com/huangxinxinyu/nano-notebook/internal/collector"
	"github.com/jackc/pgx/v5/pgxpool"
)

type rebuildConfig struct {
	DatabaseURL string
	TraceID     agentobs.TraceID
	All         bool
}

func main() {
	config, err := parseRebuildConfig(os.Args[1:], os.Getenv)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, config.DatabaseURL)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	defer pool.Close()
	if err := collector.RunMigrations(ctx, pool); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	projector, _ := collector.NewProjector(pool, collector.ProjectorConfig{})
	if config.All {
		count, err := projector.EnqueueRebuildAll(ctx)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		fmt.Printf("enqueued %d Trace projections\n", count)
		return
	}
	if err := projector.RebuildTrace(ctx, config.TraceID); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Printf("rebuilt Trace %s\n", config.TraceID)
}

func parseRebuildConfig(args []string, getenv func(string) string) (rebuildConfig, error) {
	if len(args) != 1 || (args[0] == "" || len(args[0]) > 128) {
		return rebuildConfig{}, errors.New("usage: collector-rebuild <all|trace-id>")
	}
	databaseURL := getenv("NANO_COLLECTOR_DATABASE_URL")
	if databaseURL == "" {
		databaseURL = "postgres://nano_observability:nano-observability@localhost:55432/nano_observability?sslmode=disable"
	}
	if args[0] == "all" {
		return rebuildConfig{DatabaseURL: databaseURL, All: true}, nil
	}
	return rebuildConfig{DatabaseURL: databaseURL, TraceID: agentobs.TraceID(args[0])}, nil
}
