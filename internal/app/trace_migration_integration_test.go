package app_test

import (
	"context"
	"testing"

	"github.com/huangxinxinyu/nano-notebook/internal/app"
)

func TestMigrationsRetireFullTraceOutboxAndKeepPurgeCommands(t *testing.T) {
	api := newTestAPI(t)
	ctx := context.Background()
	if _, err := api.db.Pool().Exec(ctx, `
		create table agentobs_outbox_records (record_id bigint primary key);
		create function validate_agentobs_outbox_record() returns trigger
		language plpgsql as $$ begin return new; end $$;
		create trigger agentobs_outbox_records_validate_before_insert
		before insert on agentobs_outbox_records
		for each row execute function validate_agentobs_outbox_record();
	`); err != nil {
		t.Fatal(err)
	}
	if err := app.RunMigrations(ctx, api.db); err != nil {
		t.Fatalf("retire a legacy full Trace Outbox schema: %v", err)
	}

	for _, table := range []string{
		"agentobs_outbox_records",
		"agentobs_outbox_capacity",
		"agentobs_replay_staging",
	} {
		var exists bool
		if err := api.db.Pool().QueryRow(ctx, `select to_regclass('public.' || $1) is not null`, table).Scan(&exists); err != nil {
			t.Fatal(err)
		}
		if exists {
			t.Errorf("retired full Trace relation %s still exists", table)
		}
	}

	for _, table := range []string{
		"agent_trace_refs",
		"agentobs_outbox_commands",
		"agentobs_outbox_command_objects",
	} {
		var exists bool
		if err := api.db.Pool().QueryRow(ctx, `select to_regclass('public.' || $1) is not null`, table).Scan(&exists); err != nil {
			t.Fatal(err)
		}
		if !exists {
			t.Errorf("required Trace anchor/purge relation %s is missing", table)
		}
	}

	retiredColumns := []string{
		"next_sequence", "collector_cursor", "terminal_sequence", "delivery_state",
		"lease_token", "lease_expires_at", "next_attempt_at", "attempt_count",
		"last_error_code", "quarantined_at", "updated_at",
	}
	var found int
	if err := api.db.Pool().QueryRow(ctx, `
		select count(*)
		from information_schema.columns
		where table_schema = 'public' and table_name = 'agent_trace_refs'
			and column_name = any($1::text[])
	`, retiredColumns).Scan(&found); err != nil {
		t.Fatal(err)
	}
	if found != 0 {
		t.Errorf("agent_trace_refs still has %d retired delivery columns", found)
	}

	for _, function := range []string{
		"nano_advance_agent_trace_ref(text,integer,text,text)",
		"nano_owned_run_trace_state(text)",
		"nano_owned_trace_span(text,text)",
	} {
		var exists bool
		if err := api.db.Pool().QueryRow(ctx, `select to_regprocedure($1) is not null`, function).Scan(&exists); err != nil {
			t.Fatal(err)
		}
		if exists {
			t.Errorf("retired full Trace function %s still exists", function)
		}
	}
	var descriptorExists bool
	if err := api.db.Pool().QueryRow(ctx, `select to_regprocedure('nano_owned_run_trace_descriptor(text)') is not null`).Scan(&descriptorExists); err != nil {
		t.Fatal(err)
	}
	if !descriptorExists {
		t.Error("owned Trace anchor descriptor function is missing")
	}
}
