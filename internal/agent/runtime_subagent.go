package agent

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/huangxinxinyu/nano-notebook/internal/agentcatalog"
	"github.com/huangxinxinyu/nano-notebook/internal/agentobs"
	"github.com/huangxinxinyu/nano-notebook/internal/agentobs/semconv"
	"github.com/huangxinxinyu/nano-notebook/internal/models"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const runtimeSubagentInstructions = `You are a runtime subagent working for a parent researcher. Your task is the assigned request below; the accepted plan is background context. Investigate only your assigned part and return a concise result with concrete evidence, source references, uncertainty, and unresolved gaps to your parent. You inherit its research tools, model, skills, and source scope. Your TODO, transcript, context compaction, and workspace files are independent. You cannot create or manage agents. You may use workspace files for your own analysis, but you do not have to assemble or publish a complete report. Return your findings directly as Final when your assigned task is complete. This subagent completion contract takes precedence over instructions to complete the parent's full report.`

// A Research Run reads each long document through its own reader subagent,
// so the total bounds how many long sources one Run can read; the active
// limit bounds concurrent provider load.
const (
	runtimeSubagentMaxActive = 4
	runtimeSubagentMaxTotal  = 32
)

func recordCancelledRuntimeSubagentsInTx(ctx context.Context, tx pgx.Tx, parentID string) error {
	rows, err := tx.Query(ctx, `select child.id,coalesce(s.cancelled_attempt_no,0)
		from agent_subagents s join agent_runs child on child.id=s.child_run_id
		where s.parent_run_id=$1 and child.status='cancelled' and child.error_code='parent_terminal'
		order by child.id`, parentID)
	if err != nil {
		return err
	}
	type cancelledChild struct {
		id      string
		attempt int
	}
	var children []cancelledChild
	for rows.Next() {
		var child cancelledChild
		if err := rows.Scan(&child.id, &child.attempt); err != nil {
			rows.Close()
			return err
		}
		children = append(children, child)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, child := range children {
		if err := RecordRunTerminalInTx(ctx, tx, child.id, RunTerminalTrace{
			CauseEvent: TraceEventCancellation, RunStatus: "cancelled", SpanStatus: agentobs.StatusCancelled,
			ErrorCode: "parent_terminal", AttemptNo: child.attempt,
		}); err != nil {
			return err
		}
	}
	return nil
}

func (r *ResearchRuntime) publishRuntimeSubagentFinal(ctx context.Context, attempt Attempt, draft models.FinalDraft) error {
	if err := draft.Validate(); err != nil {
		return err
	}
	traceCtx, scope, err := r.base.beginTraceScope(ctx)
	if err != nil {
		return err
	}
	defer scope.Rollback()
	tx, err := r.base.workerTx(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if err := lockCheckpointAuthority(ctx, tx, attempt); err != nil {
		return err
	}
	checkpoints, err := loadRunCheckpoints(ctx, tx, attempt.RunID)
	if err != nil {
		return err
	}
	prefix, err := LoadCheckpointPrefix(ctx, checkpoints)
	if err != nil {
		return err
	}
	if prefix.Final == nil || prefix.Final.Text != draft.Text {
		return invalidCheckpoint("subagent publication does not match accepted Final")
	}
	if err := storeConfiguredFinalResult(ctx, tx, attempt.RunID, draft.Text); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `update agent_runs set status='completed',finished_at=now(),updated_at=now() where id=$1`, attempt.RunID); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `update agent_jobs set status='succeeded',lease_token=null,lease_expires_at=null,finished_at=now(),updated_at=now() where id=$1`, attempt.JobID); err != nil {
		return err
	}
	if err := RecordRunTerminalInTx(traceCtx, tx, attempt.RunID, RunTerminalTrace{RunStatus: "completed", SpanStatus: agentobs.StatusOK, AttemptNo: attempt.AttemptNo}); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	publishCommittedTrace(traceCtx, scope)
	return nil
}

type runtimeSubagentAction struct {
	pool      *pgxpool.Pool
	name      string
	traceSink TraceSink
}

type spawnAgentInput struct {
	Message  string `json:"message"`
	TaskName string `json:"task_name,omitempty"`
}

type waitAgentInput struct {
	AgentIDs  []string `json:"agent_ids"`
	TimeoutMS *int     `json:"timeout_ms,omitempty"`
}

type runtimeSubagentObservation struct {
	AgentID   string `json:"agent_id"`
	TaskName  string `json:"task_name"`
	Status    string `json:"status"`
	Result    string `json:"result,omitempty"`
	ErrorCode string `json:"error_code,omitempty"`
}

func NewRuntimeSubagentToolRegistrations(pool *pgxpool.Pool, traceSinks ...TraceSink) []MCPToolRegistration {
	var sink TraceSink
	if len(traceSinks) > 0 {
		sink = traceSinks[0]
	}
	registrations := make([]MCPToolRegistration, 0, 3)
	for _, name := range []string{"spawn_agent", "wait_agent", "list_agents"} {
		scheduling := agentcatalog.ToolOrderedSync
		if name == "list_agents" {
			scheduling = agentcatalog.ToolParallel
		}
		registrations = append(registrations, MCPToolRegistration{
			Action: &runtimeSubagentAction{pool: pool, name: name, traceSink: sink}, Scheduling: scheduling, CrashReplaySafe: true,
		})
	}
	return registrations
}

func (a *runtimeSubagentAction) Definition() models.ActionDefinition {
	switch a.name {
	case "spawn_agent":
		return models.ActionDefinition{Name: a.name,
			Description: "Start an independent subagent asynchronously with your pinned model, tools, skills, and research scope. Specify its task and expected deliverable. Children cannot spawn agents. At most four children run concurrently; all share your tree budget and deadline.",
			InputSchema: json.RawMessage(`{"type":"object","additionalProperties":false,"required":["message"],"properties":{"message":{"type":"string","minLength":1,"maxLength":16000},"task_name":{"type":"string","minLength":1,"maxLength":80}}}`)}
	case "wait_agent":
		return models.ActionDefinition{Name: a.name,
			Description: "Collect results from your subagents. Return when any selected agent finishes or the timeout expires. Waiting releases the worker; unfinished agents continue. timeout_ms defaults to 30000 and may be 0 for a status check. Results remain readable on later calls.",
			InputSchema: json.RawMessage(`{"type":"object","additionalProperties":false,"required":["agent_ids"],"properties":{"agent_ids":{"type":"array","minItems":1,"maxItems":16,"uniqueItems":true,"items":{"type":"string","minLength":1,"maxLength":255}},"timeout_ms":{"type":"integer","minimum":0,"maximum":60000}}}`)}
	default:
		return models.ActionDefinition{Name: "list_agents", Description: "List the status and results of subagents created by this Run without waiting.", InputSchema: json.RawMessage(`{"type":"object","additionalProperties":false}`)}
	}
}

func (a *runtimeSubagentAction) ValidateInput(raw json.RawMessage) error {
	switch a.name {
	case "spawn_agent":
		var input spawnAgentInput
		if decodeExactJSON(raw, &input) != nil || strings.TrimSpace(input.Message) == "" || !utf8.ValidString(input.Message) || utf8.RuneCountInString(input.Message) > 16000 || utf8.RuneCountInString(input.TaskName) > 80 {
			return errors.New("invalid spawn_agent input")
		}
	case "wait_agent":
		var input waitAgentInput
		if decodeExactJSON(raw, &input) != nil || len(input.AgentIDs) < 1 || len(input.AgentIDs) > 16 || input.TimeoutMS != nil && (*input.TimeoutMS < 0 || *input.TimeoutMS > 60000) {
			return errors.New("invalid wait_agent input")
		}
		seen := map[string]bool{}
		for _, id := range input.AgentIDs {
			if strings.TrimSpace(id) == "" || len(id) > 255 || seen[id] {
				return errors.New("invalid wait_agent IDs")
			}
			seen[id] = true
		}
	case "list_agents":
		var input struct{}
		if decodeExactJSON(raw, &input) != nil {
			return errors.New("invalid list_agents input")
		}
	default:
		return errors.New("unknown runtime subagent tool")
	}
	return nil
}

func (*runtimeSubagentAction) CrashReplaySafe() bool { return true }

func (*runtimeSubagentAction) Available(execution Execution) (bool, string) {
	if execution.ParentRunID != "" {
		return false, "subagent_cannot_delegate"
	}
	if execution.MemberRole != "owner" && execution.MemberRole != "editor" {
		return false, string(LeaderPolicyMembershipDenied)
	}
	return true, ""
}

func (a *runtimeSubagentAction) Execute(ctx context.Context, request ActionRequest) (ActionResult, error) {
	if a.pool == nil {
		return ActionResult{}, errors.New("runtime subagent store unavailable")
	}
	if err := a.ValidateInput(request.Input); err != nil {
		return ActionResult{}, err
	}
	traceCtx, traceScope, err := (&PostgresRuntime{pool: a.pool, traceSink: a.traceSink}).beginTraceScope(ctx)
	if err != nil {
		return ActionResult{}, err
	}
	defer traceScope.Rollback()
	ctx = traceCtx
	tx, err := (&PostgresRuntime{pool: a.pool}).workerTx(ctx)
	if err != nil {
		return ActionResult{}, err
	}
	defer tx.Rollback(ctx)
	if err := lockCheckpointAuthority(ctx, tx, request.Attempt); err != nil {
		return ActionResult{}, err
	}
	var allowed, child bool
	if err := tx.QueryRow(ctx, `select runtime_kind='configured' and executor_identity='research_root'
		and definition_identity=$2 and definition_version=$3 and definition_sha256=$4
		and exists(select 1 from agent_definition_versions definition
			where definition.definition_identity=run.definition_identity and definition.definition_version=run.definition_version
			and definition.canonical_sha256=run.definition_sha256 and definition.tool_allowlist ? $5)
		and exists(select 1 from agent_trees tree join chat_runs product on product.root_agent_run_id=tree.root_agent_run_id
			join chat_chats chat on chat.id=product.chat_id
			join notebook_memberships member on member.notebook_id=chat.notebook_id and member.user_id=product.user_id
			where tree.id=run.tree_id and member.role in ('owner','editor')),
		exists(select 1 from agent_subagents where child_run_id=$1)
		from agent_runs run where id=$1`, request.Attempt.RunID, request.Definition.Identity, request.Definition.Version, request.DefinitionSHA256, a.name).Scan(&allowed, &child); err != nil {
		return ActionResult{}, err
	}
	if child {
		return ActionResult{Status: ActionDomainError, ErrorCode: "subagent_cannot_delegate"}, nil
	}
	if !allowed {
		return ActionResult{Status: ActionDomainError, ErrorCode: "subagent_tools_not_allowed"}, nil
	}
	switch a.name {
	case "spawn_agent":
		return a.spawn(ctx, tx, request)
	case "wait_agent":
		return a.wait(ctx, tx, request)
	default:
		agents, err := loadRuntimeSubagents(ctx, tx, request.Attempt.RunID, nil)
		if err != nil {
			return ActionResult{}, err
		}
		return commitSubagentResult(ctx, tx, map[string]any{"agents": agents})
	}
}

func (a *runtimeSubagentAction) spawn(ctx context.Context, tx pgx.Tx, request ActionRequest) (ActionResult, error) {
	var input spawnAgentInput
	_ = json.Unmarshal(request.Input, &input)
	if strings.TrimSpace(input.TaskName) == "" {
		input.TaskName = "Research task"
	}
	var existingID, existingMessage, existingName string
	err := tx.QueryRow(ctx, `select child_run_id,message,task_name from agent_subagents where parent_run_id=$1 and action_id=$2`, request.Attempt.RunID, request.ActionID).Scan(&existingID, &existingMessage, &existingName)
	if err == nil {
		if existingMessage != input.Message || existingName != input.TaskName {
			return ActionResult{}, invalidCheckpoint("spawn identity conflicts with accepted task")
		}
		return commitSubagentResult(ctx, tx, map[string]string{"agent_id": existingID, "task_name": existingName})
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return ActionResult{}, err
	}
	active, total, err := countRuntimeSubagentsInTx(ctx, tx, request.Attempt.RunID)
	if err != nil {
		return ActionResult{}, err
	}
	if active >= runtimeSubagentMaxActive || total >= runtimeSubagentMaxTotal {
		return ActionResult{Status: ActionDomainError, ErrorCode: "subagent_capacity_exhausted"}, nil
	}
	childID, err := createRuntimeSubagentInTx(ctx, tx, request, request.ActionID, input, len(request.Input))
	if err != nil {
		return ActionResult{}, err
	}
	return commitSubagentResult(ctx, tx, map[string]string{"agent_id": childID, "task_name": input.TaskName})
}

func countRuntimeSubagentsInTx(ctx context.Context, tx pgx.Tx, parentID string) (active, total int, err error) {
	err = tx.QueryRow(ctx, `select count(*) filter(where child.status in ('queued','running')),count(*)
		from agent_subagents s join agent_runs child on child.id=s.child_run_id where s.parent_run_id=$1`, parentID).Scan(&active, &total)
	return active, total, err
}

// createRuntimeSubagentInTx admits one child Run under the caller's capacity
// check. actionID keys the child to the parent Action that created it, so a
// replayed Action finds its child instead of creating another.
func createRuntimeSubagentInTx(ctx context.Context, tx pgx.Tx, request ActionRequest, actionID string, input spawnAgentInput, contextBytes int) (string, error) {
	childID, jobID := "run_"+uuid.NewString(), "job_"+uuid.NewString()
	if err := chargeConfiguredTreeInTx(ctx, tx, request.Attempt.RunID, treeBudgetCharge{ContextBytes: contextBytes}); err != nil {
		return "", err
	}
	_, err := tx.Exec(ctx, `insert into agent_runs(id,status,runtime_kind,tree_id,
		definition_identity,definition_version,definition_sha256,executor_identity,
		model_policy_identity,model_policy_version,model_policy_sha256,provider_model,
		provider_capability_identity,provider_capability_version,provider_capability_sha256,
		model_context_policy_identity,model_context_policy_version,model_context_policy_sha256,
		parent_context_manifest,selected_source_count)
		select $2,'queued',runtime_kind,tree_id,definition_identity,definition_version,definition_sha256,executor_identity,
		model_policy_identity,model_policy_version,model_policy_sha256,provider_model,
		provider_capability_identity,provider_capability_version,provider_capability_sha256,
		model_context_policy_identity,model_context_policy_version,model_context_policy_sha256,
		parent_context_manifest,selected_source_count from agent_runs where id=$1`, request.Attempt.RunID, childID)
	if err != nil {
		return "", err
	}
	if _, err = tx.Exec(ctx, `insert into agent_subagents(child_run_id,parent_run_id,action_id,task_name,message) values($1,$2,$3,$4,$5)`, childID, request.Attempt.RunID, actionID, input.TaskName, input.Message); err != nil {
		return "", err
	}
	if _, err = tx.Exec(ctx, `insert into agent_run_evidence_set(run_id,ordinal,notebook_id,source_id,evidence_revision_id,index_version_id)
		select $2,ordinal,notebook_id,source_id,evidence_revision_id,index_version_id from agent_run_evidence_set where run_id=$1`, request.Attempt.RunID, childID); err != nil {
		return "", err
	}
	if _, err = tx.Exec(ctx, `insert into agent_jobs(id,kind,run_id,status) values($1,'agent_run',$2,'queued')`, jobID, childID); err != nil {
		return "", err
	}
	var model string
	if err = tx.QueryRow(ctx, `select provider_model from agent_runs where id=$1`, childID).Scan(&model); err != nil {
		return "", err
	}
	if err = StartRunTraceInTx(ctx, tx, childID, model, request.Definition.String(), nil); err != nil {
		return "", err
	}
	if err = recordRuntimeSubagentSpawnInTx(ctx, tx, request.Attempt.RunID, childID); err != nil {
		return "", err
	}
	if _, err = tx.Exec(ctx, `select pg_notify('nano_agent_jobs',$1)`, jobID); err != nil {
		return "", err
	}
	return childID, nil
}

func (a *runtimeSubagentAction) wait(ctx context.Context, tx pgx.Tx, request ActionRequest) (ActionResult, error) {
	var input waitAgentInput
	_ = json.Unmarshal(request.Input, &input)
	// Serialize the observation-to-yield boundary with selected children.
	// A completing child then either appears terminal in this observation or
	// sees the queued parent after commit and wakes it; no wakeup is lost.
	rows, err := tx.Query(ctx, `select child.id from agent_subagents s join agent_runs child on child.id=s.child_run_id
		where s.parent_run_id=$1 and child.id=any($2) order by child.id for update of child`, request.Attempt.RunID, input.AgentIDs)
	if err != nil {
		return ActionResult{}, err
	}
	for rows.Next() {
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return ActionResult{}, err
	}
	agents, err := loadRuntimeSubagents(ctx, tx, request.Attempt.RunID, input.AgentIDs)
	if err != nil {
		return ActionResult{}, err
	}
	if len(agents) != len(input.AgentIDs) {
		return ActionResult{Status: ActionDomainError, ErrorCode: "subagent_not_owned"}, nil
	}
	autoReaders, err := waitAgentResearchQueue(ctx, tx, request)
	if err != nil {
		return ActionResult{}, err
	}
	result := func(timedOut bool) map[string]any {
		value := map[string]any{"agents": agents, "timed_out": timedOut}
		if len(autoReaders) > 0 {
			value["auto_dispatched_readers"] = autoReaders
		}
		return value
	}
	timeoutMS := 30000
	if input.TimeoutMS != nil {
		timeoutMS = *input.TimeoutMS
	}
	ready := false
	for _, item := range agents {
		ready = ready || item.Status == "completed" || item.Status == "failed" || item.Status == "cancelled"
	}
	if ready || timeoutMS == 0 {
		return commitSubagentResult(ctx, tx, result(!ready))
	}
	if _, err = tx.Exec(ctx, `insert into agent_subagent_waits(parent_run_id,action_id,agent_ids,deadline_at)
		values($1,$2,$3,now()+$4*interval '1 millisecond') on conflict(parent_run_id,action_id) do nothing`, request.Attempt.RunID, request.ActionID, input.AgentIDs, timeoutMS); err != nil {
		return ActionResult{}, err
	}
	var expired bool
	var deadline time.Time
	if err = tx.QueryRow(ctx, `select deadline_at<=now(),deadline_at from agent_subagent_waits where parent_run_id=$1 and action_id=$2`, request.Attempt.RunID, request.ActionID).Scan(&expired, &deadline); err != nil {
		return ActionResult{}, err
	}
	if expired {
		return commitSubagentResult(ctx, tx, result(true))
	}
	if _, err = tx.Exec(ctx, `update agent_runs set status='queued',updated_at=now() where id=$1`, request.Attempt.RunID); err != nil {
		return ActionResult{}, err
	}
	if _, err = tx.Exec(ctx, `update agent_jobs set status='queued',available_at=$2,lease_token=null,lease_expires_at=null,updated_at=now() where id=$1`, request.Attempt.JobID, deadline); err != nil {
		return ActionResult{}, err
	}
	if err = RecordAttemptWaitingInTx(ctx, tx, request.Attempt.RunID, request.Attempt.JobID, request.Attempt.AttemptNo); err != nil {
		return ActionResult{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return ActionResult{}, err
	}
	if scope, ok := TraceScopeFromContext(ctx); ok {
		_ = scope.PublishAfterCommit(ctx)
	}
	return ActionResult{}, ErrLeaseLost
}

func recordRuntimeSubagentSpawnInTx(ctx context.Context, tx pgx.Tx, parentID, childID string) error {
	parent, parentTracer, err := rootTrace(ctx, tx, parentID)
	if err != nil {
		return err
	}
	child, childTracer, err := rootTrace(ctx, tx, childID)
	if err != nil {
		return err
	}
	parentCtx := agentobs.ContextWithSpanContext(ctx, parent.RootSpanContext())
	childCtx := agentobs.ContextWithSpanContext(ctx, child.RootSpanContext())
	if err := parentTracer.Link(parentCtx, agentobs.Link{IdentityKey: "run/" + parentID + "/delegates/" + childID, Name: semconv.LinkDelegates, Target: child.RootSpanContext()}); err != nil {
		return err
	}
	if err := childTracer.Link(childCtx, agentobs.Link{IdentityKey: "run/" + childID + "/delegated-from/" + parentID, Name: semconv.LinkDelegatedFrom, Target: parent.RootSpanContext()}); err != nil {
		return err
	}
	return parentTracer.Event(parentCtx, agentobs.Event{
		IdentityKey: "run/" + parentID + "/subagent/" + childID + "/created", Name: TraceEventDelegationCreated,
		Attributes: []agentobs.Attribute{agentobs.String(TraceKeyParentRunID, parentID), agentobs.String(TraceKeyChildRunID, childID), agentobs.Int64(TraceKeyDelegationDepth, 1)},
	})
}

func loadRuntimeSubagents(ctx context.Context, tx pgx.Tx, parentID string, ids []string) ([]runtimeSubagentObservation, error) {
	rows, err := tx.Query(ctx, `select s.child_run_id,s.task_name,child.status,
		case when child.status='completed' then coalesce((select payload->>'text' from agent_run_checkpoints where run_id=child.id and kind='final_draft'),'') else '' end,
		coalesce(child.error_code,'') from agent_subagents s join agent_runs child on child.id=s.child_run_id
		where s.parent_run_id=$1 and ($2::text[] is null or s.child_run_id=any($2)) order by s.created_at,s.child_run_id`, parentID, ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]runtimeSubagentObservation, 0)
	for rows.Next() {
		var item runtimeSubagentObservation
		if err := rows.Scan(&item.AgentID, &item.TaskName, &item.Status, &item.Result, &item.ErrorCode); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func commitSubagentResult(ctx context.Context, tx pgx.Tx, value any) (ActionResult, error) {
	payload, err := json.Marshal(value)
	if err != nil {
		return ActionResult{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return ActionResult{}, err
	}
	if scope, ok := TraceScopeFromContext(ctx); ok {
		scope.PublishAfterCommit(ctx)
	}
	return ActionResult{Status: ActionSucceeded, Output: payload}, nil
}

// IsSubagent derives the no-descendants restriction from durable authority,
// never from a model-controlled argument or a prompt instruction.
func (r *PostgresRuntime) IsSubagent(ctx context.Context, attempt Attempt) (bool, error) {
	var child bool
	err := r.pool.QueryRow(ctx, `select exists(select 1 from agent_subagents where child_run_id=$1)`, attempt.RunID).Scan(&child)
	return child, err
}
