package app_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/huangxinxinyu/nano-notebook/internal/agent"
	"github.com/huangxinxinyu/nano-notebook/internal/agentobs"
	"github.com/huangxinxinyu/nano-notebook/internal/app"
	"github.com/huangxinxinyu/nano-notebook/internal/jobs"
	"github.com/jackc/pgx/v5"
)

func TestMigrationsInstallInternalTraceSchema(t *testing.T) {
	api := newTestAPI(t)
	ctx := context.Background()
	if err := app.RunMigrations(ctx, api.db); err != nil {
		t.Fatalf("reapply migrations: %v", err)
	}

	wantColumns := map[string][]string{
		"agent_traces": {
			"trace_id", "run_id", "root_span_id", "schema_version", "created_at",
		},
		"agent_trace_records": {
			"trace_id", "sequence_no", "identity_key", "record_kind", "span_id",
			"parent_span_id", "name", "target_trace_id", "target_span_id", "occurred_at",
			"payload_version", "payload", "payload_sha256", "created_at",
		},
	}
	for table, columns := range wantColumns {
		var found int
		if err := api.db.Pool().QueryRow(ctx, `
			select count(*)
			from information_schema.columns
			where table_schema = 'public' and table_name = $1
				and column_name = any($2::text[])`, table, columns).Scan(&found); err != nil {
			t.Fatal(err)
		}
		if found != len(columns) {
			t.Errorf("%s columns = %d, want %d", table, found, len(columns))
		}
	}
	if t.Failed() {
		return
	}

	for _, table := range []string{"agent_traces", "agent_trace_records"} {
		var rls, workerSelect, workerInsert, workerUpdate, workerDelete, appSelect, appInsert bool
		if err := api.db.Pool().QueryRow(ctx, `
			select c.relrowsecurity,
				has_table_privilege('nano_worker', c.oid, 'SELECT'),
				has_table_privilege('nano_worker', c.oid, 'INSERT'),
				has_table_privilege('nano_worker', c.oid, 'UPDATE'),
				has_table_privilege('nano_worker', c.oid, 'DELETE'),
				has_table_privilege('nano_app', c.oid, 'SELECT'),
				has_table_privilege('nano_app', c.oid, 'INSERT')
			from pg_class c
			join pg_namespace n on n.oid = c.relnamespace
			where n.nspname = 'public' and c.relname = $1`, table).Scan(
			&rls, &workerSelect, &workerInsert, &workerUpdate, &workerDelete, &appSelect, &appInsert,
		); err != nil {
			t.Fatal(err)
		}
		if !rls || !workerSelect || !workerInsert || workerUpdate || workerDelete || appSelect || !appInsert {
			t.Errorf("%s access rls=%t worker=%t/%t/%t/%t app=%t/%t", table, rls, workerSelect, workerInsert, workerUpdate, workerDelete, appSelect, appInsert)
		}
	}
}

func TestTraceRLSAllowsOwnedBlindInsertWithoutApplicationRead(t *testing.T) {
	api, sessionCookie, csrfCookie, chatID := newChatFixture(t, "trace-owner@example.com")
	runID := admitRunForLeaseTest(t, api, sessionCookie, csrfCookie, chatID, "0190cdd2-5f2d-7ad8-b3f5-1b588788c403")
	api.register(t, "trace-intruder@example.com")
	ctx := context.Background()
	var ownerID, intruderID string
	if err := api.db.Pool().QueryRow(ctx, `select user_id from agent_runs where id = $1`, runID).Scan(&ownerID); err != nil {
		t.Fatal(err)
	}
	if err := api.db.Pool().QueryRow(ctx, `select id from identity_users where canonical_email = 'trace-intruder@example.com'`).Scan(&intruderID); err != nil {
		t.Fatal(err)
	}
	if _, err := api.db.Pool().Exec(ctx, `delete from agent_traces where run_id = $1`, runID); err != nil {
		t.Fatal(err)
	}
	root := traceRecord(agentobs.RecordSpanStarted, "trace-owned", "root-owned", "root-start", "agent.execution")
	if err := api.db.WithRequestPrincipal(ctx, intruderID, func(tx pgx.Tx) error {
		return agent.CreateTraceInTx(ctx, tx, runID, root)
	}); err == nil {
		t.Fatal("cross-user Trace insert succeeded")
	}
	if err := api.db.WithRequestPrincipal(ctx, ownerID, func(tx pgx.Tx) error {
		return agent.CreateTraceInTx(ctx, tx, runID, root)
	}); err != nil {
		t.Fatalf("owned Trace insert: %v", err)
	}
	if err := api.db.WithRequestPrincipal(ctx, ownerID, func(tx pgx.Tx) error {
		var count int
		return tx.QueryRow(ctx, `select count(*) from agent_traces where run_id = $1`, runID).Scan(&count)
	}); err == nil {
		t.Fatal("application role read internal Trace table")
	}
	trace, err := agent.LoadDurableTraceByRun(ctx, api.db.Pool(), runID)
	if err != nil {
		t.Fatalf("internal loader: %v", err)
	}
	if trace.TraceID != root.TraceID || len(trace.Records) != 1 {
		t.Fatalf("internal Trace = %#v", trace)
	}
}

func TestMigrationsUpgradePopulatedSprint3DatabaseWithEmptyTraceHistory(t *testing.T) {
	api, sessionCookie, csrfCookie, chatID := newChatFixture(t, "trace-upgrade@example.com")
	terminalRunID := admitRunForLeaseTest(t, api, sessionCookie, csrfCookie, chatID, "0190cdd2-5f2d-7ad8-b3f5-1b588788c404")
	ctx := context.Background()
	if _, err := api.db.Pool().Exec(ctx, `update agent_runs set status = 'failed', error_code = 'legacy_failure', finished_at = now() where id = $1`, terminalRunID); err != nil {
		t.Fatal(err)
	}
	if _, err := api.db.Pool().Exec(ctx, `update agent_jobs set status = 'failed', finished_at = now() where run_id = $1`, terminalRunID); err != nil {
		t.Fatal(err)
	}
	activeRunID := admitRunForLeaseTest(t, api, sessionCookie, csrfCookie, chatID, "0190cdd2-5f2d-7ad8-b3f5-1b588788c405")
	if _, err := api.db.Pool().Exec(ctx, `drop table agent_trace_records, agent_traces cascade`); err != nil {
		t.Fatal(err)
	}
	if err := app.RunMigrations(ctx, api.db); err != nil {
		t.Fatalf("Sprint 3 populated upgrade: %v", err)
	}

	var runs, jobCount, messages, traces int
	if err := api.db.Pool().QueryRow(ctx, `select count(*) from agent_runs where id = any($1::text[])`, []string{terminalRunID, activeRunID}).Scan(&runs); err != nil {
		t.Fatal(err)
	}
	if err := api.db.Pool().QueryRow(ctx, `select count(*) from agent_jobs where run_id = any($1::text[])`, []string{terminalRunID, activeRunID}).Scan(&jobCount); err != nil {
		t.Fatal(err)
	}
	if err := api.db.Pool().QueryRow(ctx, `select count(*) from chat_messages where chat_id = $1`, chatID).Scan(&messages); err != nil {
		t.Fatal(err)
	}
	if err := api.db.Pool().QueryRow(ctx, `select count(*) from agent_traces`).Scan(&traces); err != nil {
		t.Fatal(err)
	}
	if runs != 2 || jobCount != 2 || messages != 2 || traces != 1 {
		t.Fatalf("populated upgrade state runs/jobs/messages/traces = %d/%d/%d/%d", runs, jobCount, messages, traces)
	}
	adopted, err := agent.LoadDurableTraceByRun(ctx, api.db.Pool(), activeRunID)
	if err != nil {
		t.Fatalf("load adopted active Trace: %v", err)
	}
	if len(adopted.Records) != 2 || adopted.Records[0].Name != agent.TraceSpanAgentExecution || adopted.Records[1].Name != agent.TraceEventMigrationAdopted {
		t.Fatalf("adopted Trace = %#v", adopted)
	}
	if _, err := agent.LoadDurableTraceByRun(ctx, api.db.Pool(), terminalRunID); !errors.Is(err, agent.ErrTraceNotFound) {
		t.Fatalf("historical terminal Trace error = %v, want not found", err)
	}
	claimed, ok, err := jobs.NewQueue(api.db.Pool()).ClaimNext(ctx)
	if err != nil || !ok || claimed.RunID != activeRunID {
		t.Fatalf("claim adopted active Run = %#v ok=%t err=%v", claimed, ok, err)
	}
}

func TestMigrationsAdoptRunningSprint3RunAtControlledBoundary(t *testing.T) {
	api, sessionCookie, csrfCookie, chatID := newChatFixture(t, "trace-upgrade-running@example.com")
	runID := admitRunForLeaseTest(t, api, sessionCookie, csrfCookie, chatID, "0190cdd2-5f2d-7ad8-b3f5-1b588788c406")
	ctx := context.Background()
	before, ok, err := jobs.NewQueue(api.db.Pool()).ClaimNext(ctx)
	if err != nil || !ok || before.AttemptNo != 1 {
		t.Fatalf("pre-migration claim = %#v ok=%t err=%v", before, ok, err)
	}
	if _, err := api.db.Pool().Exec(ctx, `drop table agent_trace_records, agent_traces cascade`); err != nil {
		t.Fatal(err)
	}
	if err := app.RunMigrations(ctx, api.db); err != nil {
		t.Fatalf("running Sprint 3 migration: %v", err)
	}
	var runStatus, jobStatus string
	var attemptNo int
	var leaseToken *string
	if err := api.db.Pool().QueryRow(ctx, `
		select r.status, j.status, j.attempt_no, j.lease_token::text
		from agent_runs r join agent_jobs j on j.run_id = r.id
		where r.id = $1`, runID).Scan(&runStatus, &jobStatus, &attemptNo, &leaseToken); err != nil {
		t.Fatal(err)
	}
	if runStatus != "queued" || jobStatus != "queued" || attemptNo != 0 || leaseToken != nil {
		t.Fatalf("adopted running state = %s/%s attempt=%d lease=%v", runStatus, jobStatus, attemptNo, leaseToken)
	}
	adopted, err := agent.LoadDurableTraceByRun(ctx, api.db.Pool(), runID)
	if err != nil || len(adopted.Records) != 2 || adopted.Records[1].Name != agent.TraceEventMigrationAdopted {
		t.Fatalf("adopted running Trace = %#v err=%v", adopted, err)
	}
	after, ok, err := jobs.NewQueue(api.db.Pool()).ClaimNext(ctx)
	if err != nil || !ok || after.RunID != runID || after.AttemptNo != 1 {
		t.Fatalf("post-migration claim = %#v ok=%t err=%v", after, ok, err)
	}
	trace, err := agent.LoadDurableTraceByRun(ctx, api.db.Pool(), runID)
	if err != nil || len(trace.Records) != 3 || trace.Records[2].Name != agent.TraceSpanJobAttempt {
		t.Fatalf("post-migration Attempt Trace = %#v err=%v", trace, err)
	}
}

func TestTraceSchemaRejectsInvalidLifecycleAndCascades(t *testing.T) {
	api, sessionCookie, csrfCookie, chatID := newChatFixture(t, "trace-constraints@example.com")
	runID := admitRunForLeaseTest(t, api, sessionCookie, csrfCookie, chatID, "0190cdd2-5f2d-7ad8-b3f5-1b588788c401")
	ctx := context.Background()
	const traceID = "trace-constraints"
	const rootSpanID = "span-root"
	if _, err := api.db.Pool().Exec(ctx, `delete from agent_traces where run_id = $1`, runID); err != nil {
		t.Fatal(err)
	}
	if _, err := api.db.Pool().Exec(ctx, `
		insert into agent_traces(trace_id, run_id, root_span_id, schema_version)
		values($1, $2, $3, 1)`, traceID, runID, rootSpanID); err != nil {
		t.Fatal(err)
	}
	payload := `{"semantic_convention_version":1,"attributes":[]}`
	hash := strings.Repeat("a", 64)
	insert := func(sequence int, identity, kind, spanID, parentID, name, targetTrace, targetSpan string) error {
		var parent, targetTraceValue, targetSpanValue any
		if parentID != "" {
			parent = parentID
		}
		if targetTrace != "" {
			targetTraceValue = targetTrace
		}
		if targetSpan != "" {
			targetSpanValue = targetSpan
		}
		_, err := api.db.Pool().Exec(ctx, `
			insert into agent_trace_records(
				trace_id, sequence_no, identity_key, record_kind, span_id,
				parent_span_id, name, target_trace_id, target_span_id,
				occurred_at, payload_version, payload, payload_sha256
			)
			values($1, $2, $3, $4, $5, $6, $7, $8, $9, now(), 1, $10::jsonb, $11)`,
			traceID, sequence, identity, kind, spanID, parent, name, targetTraceValue, targetSpanValue, payload, hash)
		return err
	}

	if err := insert(1, "bad-root", "span_started", "not-the-root", "", "agent.execution", "", ""); err == nil {
		t.Fatal("root Span that disagrees with envelope succeeded")
	}
	if err := insert(2, "gap", "span_started", rootSpanID, "", "agent.execution", "", ""); err == nil {
		t.Fatal("sequence gap succeeded")
	}
	if err := insert(1, "root-start", "span_started", rootSpanID, "", "agent.execution", "", ""); err != nil {
		t.Fatalf("valid root: %v", err)
	}
	if err := insert(2, "orphan-event", "event", "missing", "", "agent.event", "", ""); err == nil {
		t.Fatal("Event attached to unknown Span succeeded")
	}
	if err := insert(2, "orphan-child", "span_started", "span-child", "missing", "agent.model.call", "", ""); err == nil {
		t.Fatal("child attached to unknown parent succeeded")
	}
	if err := insert(2, "child-start", "span_started", "span-child", rootSpanID, "agent.model.call", "", ""); err != nil {
		t.Fatalf("valid child: %v", err)
	}
	if err := insert(3, "wrong-terminal", "span_ended", "span-child", "", "agent.changed", "", ""); err == nil {
		t.Fatal("terminal with a different semantic name succeeded")
	}
	if err := insert(3, "child-end", "span_ended", "span-child", "", "agent.model.call", "", ""); err != nil {
		t.Fatalf("valid terminal: %v", err)
	}
	if err := insert(4, "second-terminal", "span_ended", "span-child", "", "agent.model.call", "", ""); err == nil {
		t.Fatal("second terminal succeeded")
	}
	if err := insert(4, "unresolved-link", "link", rootSpanID, "", "continues", traceID, "missing"); err == nil {
		t.Fatal("unresolved Link succeeded")
	}
	if err := insert(4, "valid-link", "link", rootSpanID, "", "continues", traceID, "span-child"); err != nil {
		t.Fatalf("valid Link: %v", err)
	}

	if _, err := api.db.Pool().Exec(ctx, `delete from agent_runs where id = $1`, runID); err != nil {
		t.Fatalf("delete parent Run: %v", err)
	}
	var traces, records int
	if err := api.db.Pool().QueryRow(ctx, `select count(*) from agent_traces where run_id = $1`, runID).Scan(&traces); err != nil {
		t.Fatal(err)
	}
	if err := api.db.Pool().QueryRow(ctx, `select count(*) from agent_trace_records where trace_id = $1`, traceID).Scan(&records); err != nil {
		t.Fatal(err)
	}
	if traces != 0 || records != 0 {
		t.Fatalf("cascade retained traces/records = %d/%d", traces, records)
	}
}
