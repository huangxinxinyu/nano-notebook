package app_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"testing"
	"time"

	"github.com/huangxinxinyu/nano-notebook/internal/agent"
	"github.com/huangxinxinyu/nano-notebook/internal/agentobs"
	"github.com/huangxinxinyu/nano-notebook/internal/agentobs/exportertest"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestPostgresTraceExporterRoundTripsAndReconcilesRecords(t *testing.T) {
	api, sessionCookie, csrfCookie, chatID := newChatFixture(t, "trace-exporter@example.com")
	runID := admitRunForLeaseTest(t, api, sessionCookie, csrfCookie, chatID, "0190cdd2-5f2d-7ad8-b3f5-1b588788c402")
	ctx := context.Background()
	root := traceRecord(agentobs.RecordSpanStarted, "trace-exporter", "root", "root-start", "agent.execution")

	tx, err := api.db.Pool().Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := agent.CreateTraceInTx(ctx, tx, runID, root); err != nil {
		t.Fatalf("CreateTraceInTx: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}

	exporter, err := agent.NewPostgresTraceExporter(api.db.Pool())
	if err != nil {
		t.Fatalf("NewPostgresTraceExporter: %v", err)
	}
	event := traceRecord(agentobs.RecordEvent, root.TraceID, root.SpanID, "event-admitted", "nano.run.admitted")
	event.Attributes = []agentobs.Attribute{
		agentobs.String("nano.run.id", runID),
		agentobs.Int64("nano.attempt.number", 0),
		agentobs.Bool("nano.admission.replayed", false),
	}
	if err := exporter.Export(ctx, event); err != nil {
		t.Fatalf("Export Event: %v", err)
	}
	if err := exporter.Export(ctx, event); err != nil {
		t.Fatalf("reconcile Event: %v", err)
	}
	conflict := event
	conflict.Name = "nano.run.changed"
	if err := exporter.Export(ctx, conflict); !errors.Is(err, agentobs.ErrIdentityConflict) {
		t.Fatalf("conflicting Event error = %v, want ErrIdentityConflict", err)
	}

	trace, err := agent.LoadDurableTraceByRun(ctx, api.db.Pool(), runID)
	if err != nil {
		t.Fatalf("LoadDurableTraceByRun: %v", err)
	}
	if trace.TraceID != root.TraceID || trace.RootSpanID != root.SpanID || trace.SchemaVersion != root.SchemaVersion {
		t.Fatalf("Trace envelope = %#v", trace)
	}
	if len(trace.Records) != 2 || trace.Records[0].Kind != agentobs.RecordSpanStarted || trace.Records[1].Kind != agentobs.RecordEvent {
		t.Fatalf("Trace records = %#v", trace.Records)
	}
	if got := trace.Records[1].Attributes; len(got) != 3 || got[0].Key != "nano.admission.replayed" || got[1].Key != "nano.attempt.number" || got[2].Key != "nano.run.id" {
		t.Fatalf("round-trip typed attributes = %#v", got)
	}
	if _, err := api.db.Pool().Exec(ctx, `
		update agent_trace_records
		set payload = jsonb_set(payload, '{attributes,0,bool}', 'true'::jsonb)
		where trace_id = $1 and identity_key = $2`, root.TraceID, event.IdentityKey); err != nil {
		t.Fatal(err)
	}
	if _, err := agent.LoadDurableTraceByRun(ctx, api.db.Pool(), runID); !errors.Is(err, agentobs.ErrIdentityConflict) {
		t.Fatalf("corrupted payload load error = %v, want ErrIdentityConflict", err)
	}
	if err := exporter.ForceFlush(ctx); err != nil {
		t.Fatalf("ForceFlush: %v", err)
	}
	if err := exporter.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
}

func TestPostgresTraceExporterConformance(t *testing.T) {
	api, _, _, chatID := newChatFixture(t, "trace-conformance@example.com")
	ctx := context.Background()
	if _, err := api.db.Pool().Exec(ctx, `truncate agent_trace_records, agent_traces`); err != nil {
		t.Fatal(err)
	}

	exportertest.Run(t, exportertest.Harness{
		New: func(t *testing.T) agentobs.Exporter {
			t.Helper()
			if _, err := api.db.Pool().Exec(ctx, `truncate agent_trace_records, agent_traces`); err != nil {
				t.Fatal(err)
			}
			exporter, err := agent.NewPostgresTraceExporter(api.db.Pool())
			if err != nil {
				t.Fatal(err)
			}
			return &provisioningTraceExporter{delegate: exporter, pool: api.db.Pool(), chatID: chatID}
		},
		Records: func(t *testing.T, _ agentobs.Exporter, traceID agentobs.TraceID) []agentobs.Record {
			t.Helper()
			trace, err := agent.LoadDurableTrace(ctx, api.db.Pool(), traceID)
			if err != nil {
				t.Fatal(err)
			}
			return trace.Records
		},
	})
}

func TestPostgresTraceExporterReconcilesUncertainCommit(t *testing.T) {
	tests := []struct {
		name            string
		commitThenFail  bool
		wantCommitCalls int
	}{
		{name: "committed response lost", commitThenFail: true, wantCommitCalls: 1},
		{name: "not committed then retry", commitThenFail: false, wantCommitCalls: 2},
	}
	for index, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			api, sessionCookie, csrfCookie, chatID := newChatFixture(t, "trace-uncertain-"+string(rune('a'+index))+"@example.com")
			runID := admitRunForLeaseTest(t, api, sessionCookie, csrfCookie, chatID, "0190cdd2-5f2d-7ad8-b3f5-1b588788c41"+string(rune('0'+index)))
			ctx := context.Background()
			root := traceRecord(agentobs.RecordSpanStarted, agentobs.TraceID("trace-uncertain-"+string(rune('a'+index))), "root", "root-start", "agent.execution")
			tx, err := api.db.Pool().Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if err := agent.CreateTraceInTx(ctx, tx, runID, root); err != nil {
				t.Fatal(err)
			}
			if err := tx.Commit(ctx); err != nil {
				t.Fatal(err)
			}

			commitCalls := 0
			exporter, err := agent.NewPostgresTraceExporter(api.db.Pool(), agent.WithTraceCommitFunc(func(ctx context.Context, tx pgx.Tx) error {
				commitCalls++
				if commitCalls == 1 {
					if tt.commitThenFail {
						if err := tx.Commit(ctx); err != nil {
							return err
						}
					}
					return errors.New("simulated uncertain Trace commit")
				}
				return tx.Commit(ctx)
			}))
			if err != nil {
				t.Fatal(err)
			}
			event := traceRecord(agentobs.RecordEvent, root.TraceID, root.SpanID, "uncertain-event", "nano.uncertain")
			if err := exporter.Export(ctx, event); err != nil {
				t.Fatalf("Export after uncertainty: %v", err)
			}
			if commitCalls != tt.wantCommitCalls {
				t.Fatalf("commit calls = %d, want %d", commitCalls, tt.wantCommitCalls)
			}
			var count int
			if err := api.db.Pool().QueryRow(ctx, `
				select count(*) from agent_trace_records
				where trace_id = $1 and identity_key = $2`, root.TraceID, event.IdentityKey).Scan(&count); err != nil {
				t.Fatal(err)
			}
			if count != 1 {
				t.Fatalf("uncertain Event count = %d, want 1", count)
			}
		})
	}
}

type provisioningTraceExporter struct {
	delegate *agent.PostgresTraceExporter
	pool     *pgxpool.Pool
	chatID   string
}

func (e *provisioningTraceExporter) Export(ctx context.Context, record agentobs.Record) error {
	if record.Kind == agentobs.RecordSpanStarted && record.ParentSpanID == "" {
		if err := e.ensureEnvelope(ctx, record); err != nil {
			return err
		}
	}
	return e.delegate.Export(ctx, record)
}

func (e *provisioningTraceExporter) ForceFlush(ctx context.Context) error {
	return e.delegate.ForceFlush(ctx)
}

func (e *provisioningTraceExporter) Shutdown(ctx context.Context) error {
	return e.delegate.Shutdown(ctx)
}

func (e *provisioningTraceExporter) ensureEnvelope(ctx context.Context, root agentobs.Record) error {
	digest := sha256.Sum256([]byte(root.TraceID))
	suffix := hex.EncodeToString(digest[:8])
	messageID := "msg_trace_conformance_" + suffix
	runID := "run_trace_conformance_" + suffix
	if _, err := e.pool.Exec(ctx, `
		insert into chat_messages(id, chat_id, role, content)
		values($1, $2, 'user', 'conformance fixture')
		on conflict (id) do nothing`, messageID, e.chatID); err != nil {
		return err
	}
	if _, err := e.pool.Exec(ctx, `
		insert into agent_runs(
			id, user_id, chat_id, input_message_id, status, model, prompt_version,
			error_code, finished_at
		)
		select $1, creator_user_id, id, $2, 'failed', 'fixture-model', 'fixture-v1',
			'fixture_terminal', now()
		from chat_chats where id = $3
		on conflict (id) do nothing`, runID, messageID, e.chatID); err != nil {
		return err
	}
	if _, err := e.pool.Exec(ctx, `
		insert into agent_traces(trace_id, run_id, root_span_id, schema_version)
		values($1, $2, $3, $4)
		on conflict (trace_id) do nothing`, root.TraceID, runID, root.SpanID, root.SchemaVersion); err != nil {
		return err
	}
	return nil
}

func traceRecord(kind agentobs.RecordKind, traceID agentobs.TraceID, spanID agentobs.SpanID, identity, name string) agentobs.Record {
	return agentobs.Record{
		SchemaVersion:             1,
		SemanticConventionVersion: 1,
		IdentityKey:               identity,
		Kind:                      kind,
		TraceID:                   traceID,
		SpanID:                    spanID,
		Name:                      name,
		OccurredAt:                time.Unix(1_700_000_000, 123).UTC(),
		PayloadVersion:            1,
	}
}
