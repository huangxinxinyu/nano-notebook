package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/huangxinxinyu/nano-notebook/internal/app"
)

type grantConfig struct {
	DatabaseURL string
	Action      string
	Email       string
	Capability  string
}

func main() {
	config, err := parseGrantConfig(os.Args[1:], os.Getenv)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	ctx := context.Background()
	db, err := app.OpenDB(ctx, config.DatabaseURL)
	if err != nil {
		fmt.Fprintln(os.Stderr, "open Application database:", err)
		os.Exit(1)
	}
	defer db.Close()
	if err := app.RunMigrations(ctx, db); err != nil {
		fmt.Fprintln(os.Stderr, "run Application migrations:", err)
		os.Exit(1)
	}
	var userID string
	if err := db.Pool().QueryRow(ctx, `select id from identity_users where canonical_email = $1`, config.Email).Scan(&userID); err != nil {
		fmt.Fprintln(os.Stderr, "find User:", err)
		os.Exit(1)
	}
	if config.Action == "grant" {
		_, err = db.Pool().Exec(ctx, `
			insert into platform_capability_grants(user_id, capability, granted_by)
			values($1,$2,'platform-grant-cli') on conflict do nothing
		`, userID, config.Capability)
	} else {
		_, err = db.Pool().Exec(ctx, `delete from platform_capability_grants where user_id=$1 and capability=$2`, userID, config.Capability)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, config.Action+" capability:", err)
		os.Exit(1)
	}
	fmt.Printf("%s %s for %s\n", config.Action, config.Capability, config.Email)
}

func parseGrantConfig(args []string, getenv func(string) string) (grantConfig, error) {
	if len(args) != 3 {
		return grantConfig{}, errors.New("usage: platform-grant <grant|revoke> <email> <platform.trace.read|platform.trace.replay>")
	}
	config := grantConfig{
		DatabaseURL: getenv("NANO_DATABASE_URL"), Action: strings.ToLower(strings.TrimSpace(args[0])),
		Email: strings.ToLower(strings.TrimSpace(args[1])), Capability: strings.TrimSpace(args[2]),
	}
	if config.DatabaseURL == "" {
		config.DatabaseURL = "postgres://nano:nano@localhost:55432/nano?sslmode=disable"
	}
	if (config.Action != "grant" && config.Action != "revoke") || !strings.Contains(config.Email, "@") ||
		(config.Capability != "platform.trace.read" && config.Capability != "platform.trace.replay") {
		return grantConfig{}, errors.New("platform capability grant arguments are invalid")
	}
	return config, nil
}
