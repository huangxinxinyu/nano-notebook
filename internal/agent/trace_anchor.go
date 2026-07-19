package agent

import (
	"context"
	"errors"
	"strings"

	"github.com/huangxinxinyu/nano-notebook/internal/agentobs"
	"github.com/huangxinxinyu/nano-notebook/internal/collector"
	"github.com/jackc/pgx/v5"
)

var ErrTraceNotFound = errors.New("Agent Trace anchor not found")

func createTraceAnchorInTx(ctx context.Context, tx pgx.Tx, runID string, root agentobs.Record) (collector.TraceDescriptor, error) {
	if tx == nil || strings.TrimSpace(runID) == "" {
		return collector.TraceDescriptor{}, errors.New("Trace admission dependencies are incomplete")
	}
	root = normalizeTraceRecord(root)
	if err := root.Validate(); err != nil {
		return collector.TraceDescriptor{}, err
	}
	if root.Kind != agentobs.RecordSpanStarted || root.ParentSpanID != "" {
		return collector.TraceDescriptor{}, errors.New("Trace root must be a root Span start")
	}
	descriptor := collector.TraceDescriptor{
		TraceID: root.TraceID, RunID: runID, RootSpanID: root.SpanID,
		AgentName: "nano-research-agent", SchemaVersion: root.SchemaVersion,
		SemanticConventionVersion: root.SemanticConventionVersion,
	}
	if err := tx.QueryRow(ctx, `
		select r.chat_id, c.notebook_id
		from agent_runs r join chat_chats c on c.id = r.chat_id
		where r.id = $1
	`, runID).Scan(&descriptor.ChatID, &descriptor.NotebookID); err != nil {
		return collector.TraceDescriptor{}, err
	}
	if _, err := tx.Exec(ctx, `
		insert into agent_trace_refs(
			trace_id, run_id, chat_id, notebook_id, root_span_id, agent_name,
			schema_version, semantic_convention_version
		)
		values($1, $2, $3, $4, $5, $6, $7, $8)
	`, descriptor.TraceID, descriptor.RunID, descriptor.ChatID, descriptor.NotebookID,
		descriptor.RootSpanID, descriptor.AgentName, descriptor.SchemaVersion,
		descriptor.SemanticConventionVersion); err != nil {
		return collector.TraceDescriptor{}, err
	}
	return descriptor, nil
}
