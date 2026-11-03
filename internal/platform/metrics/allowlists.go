package metrics

// TaskKind is the closed set of Task kinds recognized in Sprint 12
// (PRD criterion 16).
var TaskKindValues = []string{"agent_run", "studio_output", "source_processing"}

// AgentRunVariantValues is the released production Agent Definition catalog
// (internal/agentcatalog/definitions), restricted to the six roots on the
// current release manifest (PRD criterion 22).
var AgentRunVariantValues = []string{
	"chat.leader@1",
	"research.source-discovery@1",
	"studio.report@1",
	"studio.flashcards@1",
	"studio.mind-map@1",
	"studio.data-table@1",
}

// StudioOutputVariantValues is the four released Studio Output kinds
// (PRD criterion 23; docs/sprint/SPRINT-11-PRD.md criterion 12).
var StudioOutputVariantValues = []string{"report", "flashcards", "mind_map", "data_table"}

// SourceProcessingVariantValues mirrors source_sources.input_kind's check
// constraint (internal/app/db.go: `input_kind in ('file', 'url')`) — the
// field internal/sourcejobs/queue.go actually queries for task_variant.
// A prior version of this list mirrored internal/source.Format (the file
// extension/media format) instead, which is a different, much finer-grained
// column; that mismatch meant every source_processing metric fell back to
// "other". Caught by a live end-to-end simulation, not by a fixture test —
// SourceProcessingVariantValues has no test asserting it against the real
// schema, so a future migration widening input_kind's domain will silently
// re-trigger this same bucket-to-"other" failure mode.
var SourceProcessingVariantValues = []string{"file", "url"}

// OutcomeValues is the closed Task terminal outcome set (PRD criterion 18).
var OutcomeValues = []string{"completed", "failed", "cancelled", "expired"}

// AttemptDispositionValues mirrors internal/agent.AttemptDisposition.
var AttemptDispositionValues = []string{"completed", "waiting", "retryable", "terminal", "abandoned"}

// RetrievalDegradationValues mirrors the degradation strings produced by
// internal/retrieval.Pipeline.Search (internal/retrieval/pipeline.go).
var RetrievalDegradationValues = []string{"dense_unavailable", "bm25_unavailable", "reranker_unavailable"}

// RetrievalStageValues mirrors internal/retrieval.SearchDiagnostics' fields.
var RetrievalStageValues = []string{"dense", "bm25", "fuse", "evidence_load", "rerank"}

// ErrorLayerValues is the closed set of architectural layers a typed
// failure can be attributed to (PRD criterion 43).
var ErrorLayerValues = []string{
	"model", "tool", "retrieval", "storage", "authorization", "contract", "budget", "lifecycle",
}

// ErrorCodeValues is the closed error-code allowlist. It is built from a
// full source enumeration of every ErrorCode/Code string literal that
// internal/agent's ClassifyAttempt, classifyMCPToolExecutionError, and
// grounding error classification can currently produce (see
// .planning/2026-08-01-sprint-12-observability-metrics/findings.md for the
// grep that produced this list). It is intentionally broader than the PRD
// section 4.5 draft list, which was a snapshot taken before this
// enumeration; this list is the authoritative one enforced at emit time.
var ErrorCodeValues = []string{
	// models.ErrorKind (internal/models/bifrost.go)
	"model_timeout", "model_unavailable", "model_invalid_response", "model_request_rejected",
	// ToolCallError.Code literals reachable through ClassifyAttempt
	// (internal/agent/mcp_tool_plane.go, attempt_disposition.go)
	"action_budget_exhausted", "action_id_invalid", "action_result_invalid",
	"attempt_authority_lost", "attempt_context_expired", "attempt_deadline_expired",
	"attempt_session_closed", "definition_not_found", "mcp_call_failed",
	"mcp_list_failed", "mcp_result_invalid", "mcp_tool_list_mismatch",
	"tool_input_invalid", "tool_not_allowed", "tool_scope_invalid", "tool_execution_failed",
	// websearch.Err* (internal/websearch), both direct and MCP-wrapped
	"discovery_timeout", "discovery_rate_limited", "discovery_unavailable",
	"discovery_not_configured", "discovery_invalid_query", "discovery_invalid_response",
	// internal/agent.ClassifyAttempt direct cases
	"attempt_timeout", "run_deadline_exceeded", "checkpoint_invalid",
	"research_authority_lost", "agent_execution_failed",
	// internal/agent.AttemptDisposition abandoned causes
	"lease_lost", "cancelled",
	// internal/agent studio/executor and grounding/retrieval domain errors
	"studio_definition_invalid", "studio_executor_unavailable",
	"retrieval_unavailable", "grounding_invalid", "result_contract_invalid",
}

// HTTPMethodValues bounds the method label to the methods Nano's routes
// actually accept.
var HTTPMethodValues = []string{"GET", "POST", "PATCH", "DELETE"}

// SSEStreamValues is the closed set of Nano's SSE stream kinds
// (internal/app/server.go streamRun, internal/app/source_sse.go,
// internal/app/studio_routes.go streamStudioOutput).
var SSEStreamValues = []string{"run", "source_discovery", "notebook_sources", "studio_output"}

// SSECloseReasonValues is the closed set of reasons an SSE stream ends.
var SSECloseReasonValues = []string{"terminal", "client_disconnect", "server_shutdown", "error"}
