package agent

import (
	"fmt"
	"strings"
	"time"

	"github.com/huangxinxinyu/nano-notebook/internal/platform/metrics"
)

// TaskMetricsRecorder emits the Sprint 12 task-lifecycle metrics
// (docs/sprint/SPRINT-12-PRD.md section 4.3) from the points in this
// package and internal/jobs that already know a Run's terminal state
// changed, immediately after the transaction performing that write commits
// — the same idiom this codebase already uses for Trace publication
// (TraceScope.PublishAfterCommit). A nil recorder is a safe no-op so tests
// and call sites that don't care about metrics need not construct one.
type TaskMetricsRecorder struct {
	catalog              *metrics.Catalog
	taskKind             *metrics.Allowlist
	agentVariant         *metrics.Allowlist
	studioVariant        *metrics.Allowlist
	sourceProcessVariant *metrics.Allowlist
	outcome              *metrics.Allowlist
	disposition          *metrics.Allowlist
	degradation          *metrics.Allowlist
	errorLayer           *metrics.Allowlist
	errorCode            *metrics.Allowlist
	modelName            *metrics.Allowlist
	resultKind           *metrics.Allowlist
	tool                 *metrics.Allowlist
	retrievalOK          *metrics.Allowlist
}

// NewTaskMetricsRecorder builds a recorder bound to catalog. modelNames is
// the deployment's configured set of model identities (e.g. the
// NANO_CHAT_MODEL / NANO_RESEARCH_MODEL / NANO_SOURCE_VISION_MODEL
// environment values) — PRD criterion 35 requires the "model" label be
// "constrained to the configured allowlist", which is necessarily a
// deployment-time set, not a value this leaf package can hardcode.
func NewTaskMetricsRecorder(catalog *metrics.Catalog, modelNames ...string) *TaskMetricsRecorder {
	if catalog == nil {
		return nil
	}
	return &TaskMetricsRecorder{
		catalog:              catalog,
		taskKind:             metrics.NewAllowlist("nano_task_terminal_total", "task_kind", metrics.TaskKindValues, catalog.LabelRejected),
		agentVariant:         metrics.NewAllowlist("nano_task_terminal_total", "task_variant", metrics.AgentRunVariantValues, catalog.LabelRejected),
		studioVariant:        metrics.NewAllowlist("nano_task_terminal_total", "task_variant", metrics.StudioOutputVariantValues, catalog.LabelRejected),
		sourceProcessVariant: metrics.NewAllowlist("nano_task_terminal_total", "task_variant", metrics.SourceProcessingVariantValues, catalog.LabelRejected),
		outcome:              metrics.NewAllowlist("nano_task_terminal_total", "outcome", metrics.OutcomeValues, catalog.LabelRejected),
		disposition:          metrics.NewAllowlist("nano_agent_attempt_total", "disposition", metrics.AttemptDispositionValues, catalog.LabelRejected),
		degradation:          metrics.NewAllowlist("nano_agent_run_degraded_total", "degradation", metrics.RetrievalDegradationValues, catalog.LabelRejected),
		errorLayer:           metrics.NewAllowlist("nano_error_total", "layer", metrics.ErrorLayerValues, catalog.LabelRejected),
		errorCode:            metrics.NewAllowlist("nano_error_total", "error_code", metrics.ErrorCodeValues, catalog.LabelRejected),
		modelName:            metrics.NewAllowlist("nano_model_call_seconds", "model", modelNames, catalog.LabelRejected),
		resultKind:           metrics.NewAllowlist("nano_model_call_seconds", "result_kind", modelResultKindValues, catalog.LabelRejected),
		tool:                 metrics.NewAllowlist("nano_tool_execution_seconds", "tool", toolNameValues, catalog.LabelRejected),
		retrievalOK:          metrics.NewAllowlist("nano_retrieval_search_seconds", "outcome", []string{"completed", "failed"}, catalog.LabelRejected),
	}
}

// modelResultKindValues mirrors models.ModelResultKind (internal/models/decision.go).
var modelResultKindValues = []string{"final_draft", "action_proposal", "invalid_response", "timeout", "unavailable"}

// toolNameValues is the closed set of MCP tools the Controller can invoke
// through the tool plane (internal/agent/mcp_tool_plane.go, actions.go).
var toolNameValues = []string{"search_evidence", "discover_sources", "inspect_source", "web_search", "current_time", "save_url_as_source", "configured_delegation", "read_tool_result", "search_text", "read_source"}

// ClassifyTask maps a pinned Agent Definition identity/version to the
// (task_kind, task_variant) pair used across every task-lifecycle metric.
// The four Studio Definitions (docs/sprint/SPRINT-11-PRD.md) are reported
// under task_kind "studio_output" with their short product kind, per PRD
// criterion 23; every other Definition is "agent_run" with its full
// "identity@version" reference, per PRD criterion 22.
func ClassifyTask(definitionIdentity string, definitionVersion int) (taskKind, taskVariant string) {
	switch definitionIdentity {
	case "studio.report":
		return "studio_output", "report"
	case "studio.flashcards":
		return "studio_output", "flashcards"
	case "studio.mind-map":
		return "studio_output", "mind_map"
	case "studio.data-table":
		return "studio_output", "data_table"
	}
	if definitionIdentity == "" {
		return "agent_run", ""
	}
	return "agent_run", fmt.Sprintf("%s@%d", definitionIdentity, definitionVersion)
}

// TaskOutcomeForRun maps a terminal agent_runs.status plus its error_code
// to the Sprint 12 outcome vocabulary. The schema has no distinct "expired"
// status (internal/app/db.go agent_runs check constraint); deadline
// exhaustion is stored as status='failed', error_code='run_deadline_exceeded'
// or 'recovery_exhausted' (internal/jobs/queue.go exhaustRecovery), and is
// reported as "expired" per PRD criterion 20: the system, not the Member,
// ended the Task.
func TaskOutcomeForRun(status, errorCode string) string {
	switch status {
	case "completed":
		return "completed"
	case "cancelled":
		return "cancelled"
	case "failed":
		if errorCode == "run_deadline_exceeded" || errorCode == "recovery_exhausted" {
			return "expired"
		}
		return "failed"
	default:
		return "failed"
	}
}

func (r *TaskMetricsRecorder) variantAllowlist(taskKind string) *metrics.Allowlist {
	switch taskKind {
	case "studio_output":
		return r.studioVariant
	case "source_processing":
		return r.sourceProcessVariant
	default:
		return r.agentVariant
	}
}

// RecordAttempt increments nano_agent_attempt_total for every Attempt
// resolution reached in internal/jobs.Queue.ResolveAttempt or an executor's
// direct commit, regardless of whether the Run itself became terminal.
func (r *TaskMetricsRecorder) RecordAttempt(taskKind, taskVariant string, disposition string) {
	if r == nil {
		return
	}
	variant := r.variantAllowlist(taskKind).Value(taskVariant)
	r.catalog.AgentAttempt.WithLabelValues(variant, r.disposition.Value(disposition)).Inc()
}

// RecordTerminal increments nano_task_terminal_total, nano_agent_run_attempts,
// and nano_task_end_to_end_seconds for a Run/Output/pipeline that just
// reached a terminal state. admittedAt is the Task's creation time
// (agent_runs.created_at or the source_processing equivalent); attempts is
// the number of Attempts consumed.
func (r *TaskMetricsRecorder) RecordTerminal(taskKind, taskVariant, outcome string, attempts int, admittedAt time.Time) {
	if r == nil {
		return
	}
	kind := r.taskKind.Value(taskKind)
	variant := r.variantAllowlist(taskKind).Value(taskVariant)
	out := r.outcome.Value(outcome)
	r.catalog.TaskTerminal.WithLabelValues(kind, variant, out).Inc()
	if attempts > 0 {
		r.catalog.AgentRunAttempts.WithLabelValues(variant).Observe(float64(attempts))
	}
	if !admittedAt.IsZero() {
		r.catalog.TaskEndToEnd.WithLabelValues(kind, variant, out).Observe(time.Since(admittedAt).Seconds())
	}
}

// RecordDegraded increments nano_agent_run_degraded_total for a Run that
// reached "completed" while one or more retrieval channels were degraded
// (PRD criteria 26-27).
func (r *TaskMetricsRecorder) RecordDegraded(degradation string) {
	if r == nil {
		return
	}
	r.catalog.AgentRunDegraded.WithLabelValues(r.degradation.Value(degradation)).Inc()
}

// RecordRetrievalDegraded increments nano_retrieval_degraded_total for one
// search call that declared a degradation (PRD criterion 39) — distinct
// from RecordDegraded, which is scoped to a Run's terminal state.
func (r *TaskMetricsRecorder) RecordRetrievalDegraded(degradation string) {
	if r == nil {
		return
	}
	r.catalog.RetrievalDegraded.WithLabelValues(r.degradation.Value(degradation)).Inc()
}

// RecordError increments nano_error_total for a typed failure classified
// by ClassifyAttempt, classifyMCPToolExecutionError, or an equivalent
// domain classifier.
func (r *TaskMetricsRecorder) RecordError(taskKind, layer, errorCode string) {
	if r == nil {
		return
	}
	kind := r.taskKind.Value(taskKind)
	r.catalog.ErrorTotal.WithLabelValues(kind, r.errorLayer.Value(layer), r.errorCode.Value(errorCode)).Inc()
}

// RecordQueueWait observes nano_task_queue_wait_seconds at the moment a Job
// is claimed (internal/jobs.Queue.ClaimNext), from Job.available_at.
func (r *TaskMetricsRecorder) RecordQueueWait(taskKind, taskVariant string, wait time.Duration) {
	if r == nil || wait < 0 {
		return
	}
	kind := r.taskKind.Value(taskKind)
	variant := r.variantAllowlist(taskKind).Value(taskVariant)
	r.catalog.TaskQueueWait.WithLabelValues(kind, variant).Observe(wait.Seconds())
}

// RecordModelCall observes nano_model_call_seconds from the already-computed
// models.ModelOutcome.Metadata.Latency (internal/models/bifrost.go), adding
// no new timing code (PRD criterion 35).
func (r *TaskMetricsRecorder) RecordModelCall(model, resultKind, outcome string, latency time.Duration) {
	if r == nil || latency < 0 {
		return
	}
	r.catalog.ModelCall.WithLabelValues(r.modelName.Value(model), r.resultKind.Value(resultKind), outcome).Observe(latency.Seconds())
}

// RecordToolExecution observes nano_tool_execution_seconds at the MCP tool
// plane boundary (PRD criterion 37).
func (r *TaskMetricsRecorder) RecordToolExecution(tool, outcome string, latency time.Duration) {
	if r == nil || latency < 0 {
		return
	}
	r.catalog.ToolExecution.WithLabelValues(r.tool.Value(tool), outcome).Observe(latency.Seconds())
}

// RecordRetrievalSearch observes nano_retrieval_search_seconds and, for
// each stage already timed by retrieval.SearchDiagnostics, nano_retrieval_stage_seconds
// (PRD criteria 38-39), without adding new timing code inside internal/retrieval.
func (r *TaskMetricsRecorder) RecordRetrievalSearch(outcome string, total time.Duration) {
	if r == nil || total < 0 {
		return
	}
	r.catalog.RetrievalSearch.WithLabelValues(r.retrievalOK.Value(outcome)).Observe(total.Seconds())
}

func (r *TaskMetricsRecorder) RecordRetrievalStage(stage, outcome string, duration time.Duration) {
	if r == nil || duration < 0 {
		return
	}
	r.catalog.RetrievalStage.WithLabelValues(stage, outcome).Observe(duration.Seconds())
}

// ErrorLayerForCode gives a best-effort architectural layer for an error
// code produced by ClassifyAttempt, used when the caller does not already
// know which layer classified the failure (PRD criterion 43-44).
func ErrorLayerForCode(code string) string {
	switch {
	case strings.HasPrefix(code, "model_"):
		return "model"
	case strings.HasPrefix(code, "discovery_"), strings.HasPrefix(code, "tool_"), strings.HasPrefix(code, "mcp_"),
		strings.HasPrefix(code, "action_"), code == "attempt_authority_lost", code == "attempt_context_expired",
		code == "attempt_deadline_expired", code == "attempt_session_closed", code == "definition_not_found",
		code == "tool_execution_failed":
		return "tool"
	case code == "retrieval_unavailable":
		return "retrieval"
	case code == "grounding_invalid", code == "result_contract_invalid", code == "studio_definition_invalid":
		return "contract"
	case code == "attempt_timeout", code == "run_deadline_exceeded", code == "recovery_exhausted":
		return "lifecycle"
	case code == "lease_lost", code == "cancelled":
		return "lifecycle"
	case code == "studio_executor_unavailable":
		return "budget"
	default:
		return "lifecycle"
	}
}
