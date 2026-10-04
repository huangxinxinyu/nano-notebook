package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/huangxinxinyu/nano-notebook/internal/agentcatalog"
	"github.com/huangxinxinyu/nano-notebook/internal/agentobs"
	"github.com/huangxinxinyu/nano-notebook/internal/models"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var ErrLeaseLost = errors.New("agent attempt lease lost")

type PostgresRuntime struct {
	pool         *pgxpool.Pool
	systemPrompt string
	newMessageID func() string
	commit       func(context.Context, pgx.Tx) error
	telemetry    agentobs.Exporter
	traceSink    TraceSink
	replayStager ReplayStager
	grounder     *GroundingService
	metrics      *TaskMetricsRecorder
	now          func() time.Time
}

type RuntimeOption func(*PostgresRuntime)

// WithTaskMetrics attaches the Sprint 12 task-lifecycle metrics recorder
// (docs/sprint/SPRINT-12-PRD.md section 4.3).
func WithTaskMetrics(recorder *TaskMetricsRecorder) RuntimeOption {
	return func(runtime *PostgresRuntime) {
		runtime.metrics = recorder
	}
}

func WithRuntimeClock(now func() time.Time) RuntimeOption {
	return func(runtime *PostgresRuntime) {
		if now != nil {
			runtime.now = now
		}
	}
}

func WithCommitFunc(commit func(context.Context, pgx.Tx) error) RuntimeOption {
	return func(runtime *PostgresRuntime) {
		if commit != nil {
			runtime.commit = commit
		}
	}
}

func WithBestEffortTraceExporter(exporter agentobs.Exporter) RuntimeOption {
	return func(runtime *PostgresRuntime) {
		runtime.telemetry = exporter
	}
}

func WithTraceSink(sink TraceSink) RuntimeOption {
	return func(runtime *PostgresRuntime) {
		runtime.traceSink = sink
	}
}

func (r *PostgresRuntime) beginTraceScope(ctx context.Context) (context.Context, *TraceScope, error) {
	sink := TraceSink(DiscardTraceSink{})
	if r != nil && r.traceSink != nil {
		sink = r.traceSink
	}
	scope, err := NewTraceScope(sink)
	if err != nil {
		return ctx, nil, err
	}
	return ContextWithTraceScope(ctx, scope), scope, nil
}

func publishCommittedTrace(ctx context.Context, scope *TraceScope) {
	if scope != nil {
		_ = scope.PublishAfterCommit(ctx)
	}
}

func WithReplayStager(stager ReplayStager) RuntimeOption {
	return func(runtime *PostgresRuntime) {
		runtime.replayStager = stager
	}
}

func WithGroundingService(grounder *GroundingService) RuntimeOption {
	return func(runtime *PostgresRuntime) {
		runtime.grounder = grounder
	}
}

func (r *PostgresRuntime) ReplayStager() ReplayStager {
	if r == nil {
		return nil
	}
	return r.replayStager
}

func (r *PostgresRuntime) PrepareFinal(ctx context.Context, attempt Attempt, execution Execution, prefix CheckpointPrefix, draft models.FinalDraft) (models.FinalDraft, error) {
	if r != nil && r.grounder != nil {
		return r.grounder.Prepare(ctx, attempt, prefix, draft)
	}
	if execution.SelectedSourceCount > 0 {
		return models.FinalDraft{}, ErrGroundingIncomplete
	}
	return draft, nil
}

func (r *PostgresRuntime) PrepareFinalTraced(ctx context.Context, tracer *agentobs.Tracer, attempt Attempt, execution Execution, prefix CheckpointPrefix, draft models.FinalDraft) (models.FinalDraft, error) {
	if r != nil && r.grounder != nil {
		return r.grounder.PrepareTraced(ctx, tracer, r.replayStager, attempt, prefix, draft)
	}
	if execution.SelectedSourceCount > 0 {
		return models.FinalDraft{}, ErrGroundingIncomplete
	}
	return draft, nil
}

func NewPostgresRuntime(pool *pgxpool.Pool, systemPrompt string, newMessageID func() string, options ...RuntimeOption) *PostgresRuntime {
	if systemPrompt == "" {
		systemPrompt = BareSystemPrompt
	}
	if newMessageID == nil {
		newMessageID = func() string { return "msg_" + uuid.NewString() }
	}
	runtime := &PostgresRuntime{
		pool: pool, systemPrompt: systemPrompt, newMessageID: newMessageID,
		commit: func(ctx context.Context, tx pgx.Tx) error { return tx.Commit(ctx) },
		now:    time.Now,
	}
	for _, option := range options {
		option(runtime)
	}
	return runtime
}

// executionSelectFrom is the shared column list and join graph behind both
// Load (live, lease-gated) and LoadForReplay (offline, no lease check).
// $1/$2 are always GroundedPromptVersion/BarePromptVersion; callers append
// their own WHERE clause starting at $3.
const executionSelectFrom = `
	select r.id, coalesce(r.chat_id,product.chat_id), coalesce(r.user_id,product.user_id),
		coalesce(r.input_message_id,product.input_message_id), coalesce(r.model,r.provider_model),
		coalesce(r.prompt_version,case when coalesce(r.selected_source_count,0)>0 then $1 else $2 end),
		coalesce(r.agent_config_id,r.definition_identity||'@'||r.definition_version::text),
		coalesce(r.time_zone,r.parent_context_manifest->>'time_zone','UTC'),
		coalesce(r.deadline_at,tree.absolute_deadline),
		coalesce(r.action_decision_limit,coalesce(nullif((definition.limits->>'action_decisions')::integer,0),greatest(0,(definition.limits->>'model_calls')::integer-1))),
		coalesce(r.final_decision_limit,1),
		coalesce(r.action_limit,(definition.limits->>'actions')::integer),
		coalesce(r.action_batch_limit,(definition.limits->>'action_batch')::integer),
		coalesce(r.action_result_byte_limit,(definition.limits->>'result_bytes')::integer),
		coalesce(r.action_results_byte_limit,(definition.limits->>'result_bytes')::integer),
		coalesce(r.selected_source_count,0),
		m.role,
		(select count(*) from agent_run_delegations child_link where child_link.parent_run_id=tree.root_agent_run_id),
		r.runtime_kind='configured',policy.temperature,policy.max_output_tokens,policy.timeout_ms,policy.enable_thinking,
		r.model_context_policy_identity,r.model_context_policy_version,r.model_context_policy_sha256,
		context_policy.model_policy_identity,context_policy.model_policy_version,
		r.provider_capability_identity,r.provider_capability_version,r.provider_capability_sha256,
		capability.resolved_model,capability.context_window_tokens,capability.max_input_tokens,capability.max_output_tokens,
		capability.tokenizer_identity,capability.tokenizer_version,capability.invocation_mode,
		context_policy.pinned_max_output_tokens,context_policy.soft_input_limit_tokens,
		context_policy.estimation_safety_tokens,context_policy.keep_recent_tokens,
		context_policy.summary_max_output_tokens,context_policy.overflow_retry_limit,
		coalesce(r.deadline_at,tree.absolute_deadline) > now(),coalesce(subagent.parent_run_id,''),coalesce(subagent.message,'')
	from agent_runs r
	join agent_jobs j on j.run_id = r.id
	left join agent_trees tree on tree.id=r.tree_id
	left join chat_runs product on product.root_agent_run_id=tree.root_agent_run_id
	left join agent_subagents subagent on subagent.child_run_id=r.id
	left join agent_definition_versions definition on definition.definition_identity=r.definition_identity and definition.definition_version=r.definition_version
	left join agent_model_policy_versions policy
		on policy.policy_identity=r.model_policy_identity and policy.policy_version=r.model_policy_version
		and policy.canonical_sha256=r.model_policy_sha256 and policy.provider_model=r.provider_model
	left join agent_model_context_policy_versions context_policy
		on context_policy.context_policy_identity=r.model_context_policy_identity
		and context_policy.context_policy_version=r.model_context_policy_version
		and context_policy.canonical_sha256=r.model_context_policy_sha256
		and context_policy.model_policy_identity=r.model_policy_identity
		and context_policy.model_policy_version=r.model_policy_version
	left join provider_model_capability_versions capability
		on capability.capability_identity=r.provider_capability_identity
		and capability.capability_version=r.provider_capability_version
		and capability.canonical_sha256=r.provider_capability_sha256
		and capability.capability_identity=context_policy.capability_identity
		and capability.capability_version=context_policy.capability_version
		and capability.provider_model=r.provider_model
	join chat_chats c on c.id=coalesce(r.chat_id,product.chat_id) and c.creator_user_id=coalesce(r.user_id,product.user_id)
	join notebook_memberships m on m.notebook_id=c.notebook_id and m.user_id=coalesce(r.user_id,product.user_id)
`

// loadExecutionRow runs executionSelectFrom+whereSQL (whereArgs bound
// starting at $3) and scans the common Execution columns. It returns the
// row's own deadlineValid flag so Load can enforce it while LoadForReplay
// ignores it (a historical run's original deadline is not relevant to
// replaying its decision today).
func (r *PostgresRuntime) loadExecutionRow(ctx context.Context, tx pgx.Tx, whereSQL string, whereArgs ...any) (Execution, bool, error) {
	var execution Execution
	var deadlineValid bool
	var configured bool
	var temperature *float64
	var enableThinking *bool
	var maxOutputTokens, timeoutMS *int
	var contextIdentity, contextSHA, capabilityIdentity, capabilitySHA *string
	var invocationPolicyIdentity *string
	var resolvedModel, tokenizerIdentity, tokenizerVersion, invocationMode *string
	var contextVersion, capabilityVersion, invocationPolicyVersion *int
	var contextWindow, providerMaxInput, providerMaxOutput *int
	var pinnedOutput, softInput, safety, keepRecent, summaryOutput, overflowLimit *int
	args := append([]any{GroundedPromptVersion, BarePromptVersion}, whereArgs...)
	err := tx.QueryRow(ctx, executionSelectFrom+whereSQL, args...).Scan(
		&execution.RunID, &execution.ChatID, &execution.UserID, &execution.InputMessageID, &execution.Model,
		&execution.PromptVersion, &execution.AgentConfigID, &execution.TimeZone, &execution.DeadlineAt,
		&execution.ActionDecisionLimit, &execution.FinalDecisionLimit,
		&execution.ActionLimit, &execution.ActionBatchLimit,
		&execution.ActionResultByteLimit, &execution.ActionResultsByteLimit,
		&execution.SelectedSourceCount,
		&execution.MemberRole, &execution.ExistingChildCount,
		&configured, &temperature, &maxOutputTokens, &timeoutMS, &enableThinking,
		&contextIdentity, &contextVersion, &contextSHA,
		&invocationPolicyIdentity, &invocationPolicyVersion,
		&capabilityIdentity, &capabilityVersion, &capabilitySHA,
		&resolvedModel, &contextWindow, &providerMaxInput, &providerMaxOutput,
		&tokenizerIdentity, &tokenizerVersion, &invocationMode,
		&pinnedOutput, &softInput, &safety, &keepRecent, &summaryOutput, &overflowLimit,
		&deadlineValid, &execution.ParentRunID, &execution.SubagentTask,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return Execution{}, false, ErrLeaseLost
	}
	if err != nil {
		return Execution{}, false, err
	}
	if configured {
		if temperature == nil || maxOutputTokens == nil || timeoutMS == nil || enableThinking == nil || contextIdentity == nil || contextVersion == nil ||
			contextSHA == nil || invocationPolicyIdentity == nil || invocationPolicyVersion == nil ||
			capabilityIdentity == nil || capabilityVersion == nil || capabilitySHA == nil ||
			resolvedModel == nil || contextWindow == nil || providerMaxInput == nil || providerMaxOutput == nil ||
			tokenizerIdentity == nil || tokenizerVersion == nil || invocationMode == nil || pinnedOutput == nil ||
			softInput == nil || safety == nil || keepRecent == nil || summaryOutput == nil || overflowLimit == nil {
			return Execution{}, false, errors.New("configured Model Policy pin is invalid")
		}
		execution.ModelInvocation = models.ModelInvocationPolicy{
			Temperature: temperature, MaxOutputTokens: *maxOutputTokens, Timeout: time.Duration(*timeoutMS) * time.Millisecond,
			EnableThinking: *enableThinking,
		}
		hardInput := *providerMaxInput
		if windowInput := *contextWindow - *pinnedOutput; windowInput < hardInput {
			hardInput = windowInput
		}
		safeInput := hardInput - *safety
		if *enableThinking != (*invocationMode == "thinking") || *pinnedOutput != *maxOutputTokens ||
			*pinnedOutput > *providerMaxOutput || hardInput < 1 || safeInput < 1 ||
			*softInput > safeInput || *keepRecent >= *softInput || *summaryOutput > *providerMaxOutput || *overflowLimit < 1 {
			return Execution{}, false, errors.New("configured Model Context Policy pin is invalid")
		}
		trigger := safeInput
		if *softInput < trigger {
			trigger = *softInput
		}
		execution.ModelContext = agentcatalog.ResolvedModelContextPolicy{
			Policy: agentcatalog.ModelContextPolicy{
				Identity: *contextIdentity, Version: *contextVersion,
				InvocationModelPolicy: agentcatalog.Reference{Identity: *invocationPolicyIdentity, Version: *invocationPolicyVersion},
				ProviderCapability:    agentcatalog.Reference{Identity: *capabilityIdentity, Version: *capabilityVersion},
				PinnedMaxOutputTokens: *pinnedOutput, SoftInputLimitTokens: *softInput,
				EstimationSafetyTokens: *safety, KeepRecentTokens: *keepRecent,
				SummaryMaxOutputTokens: *summaryOutput, OverflowRetryLimit: *overflowLimit, SHA256: *contextSHA,
			},
			Capability: agentcatalog.ProviderModelCapability{
				Identity: *capabilityIdentity, Version: *capabilityVersion, ProviderModel: execution.Model,
				ResolvedModel: *resolvedModel, ContextWindowTokens: *contextWindow, MaxInputTokens: *providerMaxInput,
				MaxOutputTokens: *providerMaxOutput, TokenizerIdentity: *tokenizerIdentity,
				TokenizerVersion: *tokenizerVersion, InvocationMode: *invocationMode, SHA256: *capabilitySHA,
			},
			Budgets: agentcatalog.ModelContextBudgets{
				HardInputTokens: hardInput, SafeInputTokens: safeInput, CompactionTriggerTokens: trigger,
			},
		}
	}
	return execution, deadlineValid, nil
}

func (r *PostgresRuntime) Load(ctx context.Context, attempt Attempt) (Execution, error) {
	tx, err := r.workerTx(ctx)
	if err != nil {
		return Execution{}, err
	}
	defer tx.Rollback(ctx)
	execution, deadlineValid, err := r.loadExecutionRow(ctx, tx,
		`where r.id = $3 and j.id = $4 and j.lease_token = $5::uuid
			and r.status = 'running' and j.status = 'running'
			and j.lease_expires_at > now() and r.output_message_id is null`,
		attempt.RunID, attempt.JobID, attempt.LeaseToken)
	if err != nil {
		return Execution{}, err
	}
	if !deadlineValid {
		return Execution{}, ErrRunDeadlineExceeded
	}
	execution.Attempt = attempt
	if err := tx.Commit(ctx); err != nil {
		return Execution{}, err
	}
	return execution, nil
}

// LoadForReplay reconstructs a Leader Run's Execution configuration
// without claiming worker authority over it — no lease, status, or
// deadline check, unlike Load. For internal/agenteval's offline
// decision-replay tooling only; must not be used by the live Controller
// loop, which depends on Load's lease guarantee for correctness.
func (r *PostgresRuntime) LoadForReplay(ctx context.Context, runID string) (Execution, error) {
	tx, err := r.workerTx(ctx)
	if err != nil {
		return Execution{}, err
	}
	defer tx.Rollback(ctx)
	execution, _, err := r.loadExecutionRow(ctx, tx, `where r.id = $3`, runID)
	if err != nil {
		return Execution{}, err
	}
	execution.Attempt = Attempt{RunID: runID}
	execution.ReplayOnly = true
	if err := tx.Commit(ctx); err != nil {
		return Execution{}, err
	}
	return execution, nil
}

func (r *PostgresRuntime) Build(ctx context.Context, execution Execution) (models.ModelRequest, error) {
	prefix, err := r.LoadCheckpointPrefix(ctx, execution.Attempt)
	if err != nil {
		return models.ModelRequest{}, err
	}
	return r.BuildDecisionRequest(ctx, execution, prefix, nil)
}

func (r *PostgresRuntime) PublishFinal(ctx context.Context, attempt Attempt, draft models.FinalDraft) error {
	if _, err := NewFinalDraftCheckpoint(1, draft); err != nil {
		return err
	}
	return r.publishResult(ctx, attempt, draft.Text, &draft)
}

func (r *PostgresRuntime) publishResult(ctx context.Context, attempt Attempt, text string, expectedFinal *models.FinalDraft) error {
	messageID := r.newMessageID()
	if messageID == "" {
		return errors.New("empty Assistant Message ID")
	}
	var publishErr error
	for publishTry := 0; publishTry < 2; publishTry++ {
		publishErr = r.publishOnce(ctx, attempt, messageID, text, expectedFinal)
		if publishErr == nil {
			return nil
		}
		if errors.Is(publishErr, ErrLeaseLost) || errors.Is(publishErr, ErrRunDeadlineExceeded) || errors.Is(publishErr, ErrCheckpointInvalid) {
			return publishErr
		}
		state, reconcileErr := r.reconcilePublication(ctx, attempt)
		if reconcileErr != nil {
			return errors.Join(publishErr, reconcileErr)
		}
		switch state {
		case publicationCompleted:
			return nil
		case publicationLeaseLost:
			return ErrLeaseLost
		case publicationDeadline:
			return ErrRunDeadlineExceeded
		case publicationCurrent:
			continue
		}
	}
	return publishErr
}

func (r *PostgresRuntime) publishOnce(ctx context.Context, attempt Attempt, messageID, text string, expectedFinal *models.FinalDraft) error {
	tx, err := r.workerTx(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	traceCtx, traceScope, err := r.beginTraceScope(ctx)
	if err != nil {
		return err
	}
	if traceScope != nil {
		defer traceScope.Rollback()
	}
	if err := lockCheckpointAuthority(ctx, tx, attempt); err != nil {
		return err
	}
	var chatID string
	if err := tx.QueryRow(ctx, `
		select coalesce(run.chat_id,product.chat_id)
		from agent_runs run
		left join agent_trees tree on tree.id=run.tree_id
		left join chat_runs product on product.root_agent_run_id=tree.root_agent_run_id
		where run.id=$1
	`, attempt.RunID).Scan(&chatID); err != nil {
		return err
	}
	if expectedFinal != nil {
		checkpoints, err := loadRunCheckpoints(ctx, tx, attempt.RunID)
		if err != nil {
			return err
		}
		prefix, err := LoadCheckpointPrefix(ctx, checkpoints)
		if err != nil {
			return err
		}
		prefixHash, prefixErr := finalDraftSHA256(valueOrEmptyFinal(prefix.Final))
		expectedHash, expectedErr := finalDraftSHA256(*expectedFinal)
		if prefix.Final == nil || prefixErr != nil || expectedErr != nil || prefixHash != expectedHash {
			return invalidCheckpoint("publication Final Draft does not match accepted prefix")
		}
	}
	groundingOutcome, err := validateGroundingPublication(ctx, tx, attempt.RunID, expectedFinal)
	if err != nil {
		return err
	}
	if err := storeConfiguredFinalResult(ctx, tx, attempt.RunID, text); err != nil {
		return err
	}
	recorder, err := NewRunTraceRecorder(traceCtx, tx, attempt.RunID)
	if err != nil {
		return err
	}
	tracer, err := agentobs.NewTracer(agentobs.TracerConfig{
		Recorder: recorder, SemanticConventionVersion: TraceSemanticConventionVersion,
	})
	if err != nil {
		return err
	}
	attemptSpan, err := recorder.SpanContextByIdentity(traceCtx, TraceAttemptStartIdentity(attempt.RunID, attempt.AttemptNo))
	if err != nil {
		return err
	}
	publicationContext, _, err := tracer.StartSpan(agentobs.ContextWithSpanContext(traceCtx, attemptSpan), agentobs.SpanStart{
		IdentityKey: fmt.Sprintf("run/%s/attempt/%d/publication/start", attempt.RunID, attempt.AttemptNo),
		Name:        TraceSpanPublication,
		Attributes:  []agentobs.Attribute{agentobs.String(TraceKeyGroundingOutcome, groundingOutcome)},
	})
	if err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `
		insert into chat_messages(id, chat_id, role, content)
		values($1, $2, 'assistant', $3)`, messageID, chatID, text); err != nil {
		return err
	}
	if groundingOutcome == "source_cited" {
		if _, err := tx.Exec(ctx, `
			insert into chat_citations(
				message_id,citation_id,run_id,reference_kind,reference_ordinal,notebook_id,source_id
			)
			select $1,c.citation_id,c.run_id,'source',c.reference_ordinal,c.notebook_id,c.source_id
			from agent_draft_source_references c
			where c.run_id=$2
			order by c.reference_ordinal
		`, messageID, attempt.RunID); err != nil {
			return err
		}
	}
	runTag, err := tx.Exec(ctx, `
		update agent_runs
		set output_message_id = $2,
			status = 'completed',
			error_code = null,
			finished_at = now(),
			updated_at = now()
		where id = $1 and status = 'running' and output_message_id is null`, attempt.RunID, messageID)
	if err != nil {
		return err
	}
	jobTag, err := tx.Exec(ctx, `
		update agent_jobs
		set status = 'succeeded', lease_token = null, lease_expires_at = null,
			finished_at = now(), updated_at = now()
		where id = $1 and status = 'running' and lease_token = $2::uuid`, attempt.JobID, attempt.LeaseToken)
	if err != nil {
		return err
	}
	if runTag.RowsAffected() != 1 || jobTag.RowsAffected() != 1 {
		return errors.New("Run publication did not transition Run and Job together")
	}
	if _, err := tx.Exec(ctx, `update chat_chats set updated_at = now() where id = $1`, chatID); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `select pg_notify('nano_agent_runs', $1)`, attempt.RunID); err != nil {
		return err
	}
	if err := tracer.Event(publicationContext, agentobs.Event{
		IdentityKey: fmt.Sprintf("run/%s/attempt/%d/publication/passed", attempt.RunID, attempt.AttemptNo),
		Name:        TraceEventPublicationPassed,
		Attributes:  []agentobs.Attribute{agentobs.String(TraceKeyGroundingOutcome, groundingOutcome)},
	}); err != nil {
		return err
	}
	if err := tracer.EndSpan(publicationContext, agentobs.SpanEnd{Name: TraceSpanPublication, Status: agentobs.StatusOK, Attributes: []agentobs.Attribute{
		agentobs.String(TraceKeyGroundingOutcome, groundingOutcome),
	}}); err != nil {
		return err
	}
	if err := RecordRunTerminalInTx(traceCtx, tx, attempt.RunID, RunTerminalTrace{
		RunStatus: "completed", SpanStatus: agentobs.StatusOK, AttemptNo: attempt.AttemptNo,
	}); err != nil {
		return err
	}
	if err := r.commit(ctx, tx); err != nil {
		return err
	}
	r.recordTerminalMetrics(ctx, attempt.RunID, attempt.AttemptNo, "completed", "")
	publishCommittedTrace(traceCtx, traceScope)
	return nil
}

// recordTerminalMetrics emits the Sprint 12 task-lifecycle metrics for a
// Run that just reached a terminal state, using a fresh short-lived lookup
// (not the just-committed transaction, which is already closed) — mirrors
// how postgres_runtime.go's own commit-then-notify calls already run after
// the transaction that performed the write. A nil recorder is a no-op.
func (r *PostgresRuntime) recordTerminalMetrics(ctx context.Context, runID string, attemptNo int, outcome, errorCode string) {
	if r.metrics == nil || r.pool == nil {
		return
	}
	var definitionIdentity *string
	var definitionVersion *int
	var admittedAt time.Time
	lookupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if err := r.pool.QueryRow(lookupCtx, `select definition_identity,definition_version,created_at from agent_runs where id=$1`, runID).
		Scan(&definitionIdentity, &definitionVersion, &admittedAt); err != nil {
		return
	}
	identity := ""
	if definitionIdentity != nil {
		identity = *definitionIdentity
	}
	version := 0
	if definitionVersion != nil {
		version = *definitionVersion
	}
	taskKind, taskVariant := ClassifyTask(identity, version)
	disposition := AttemptCompleted
	resolvedOutcome := outcome
	if outcome == "failed" {
		disposition = AttemptTerminal
		resolvedOutcome = TaskOutcomeForRun("failed", errorCode)
	}
	r.metrics.RecordAttempt(taskKind, taskVariant, string(disposition))
	r.metrics.RecordTerminal(taskKind, taskVariant, resolvedOutcome, attemptNo, admittedAt)
	if errorCode != "" {
		r.metrics.RecordError(taskKind, ErrorLayerForCode(errorCode), errorCode)
	}
}

func storeConfiguredFinalResult(ctx context.Context, tx pgx.Tx, runID, text string) error {
	var runtimeKind string
	if err := tx.QueryRow(ctx, `select runtime_kind from agent_runs where id=$1`, runID).Scan(&runtimeKind); err != nil {
		return err
	}
	if runtimeKind != "configured" {
		return nil
	}
	var contract agentcatalog.ContractVersion
	if err := tx.QueryRow(ctx, `
		select definition.result_contract_identity,definition.result_contract_version,contract.canonical_sha256
		from agent_runs run
		join agent_definition_versions definition on definition.definition_identity=run.definition_identity and definition.definition_version=run.definition_version
		join agent_contract_versions contract on contract.contract_identity=definition.result_contract_identity and contract.contract_version=definition.result_contract_version
		where run.id=$1
	`, runID).Scan(&contract.Identity, &contract.Version, &contract.SHA256); err != nil {
		return err
	}
	payload, err := json.Marshal(map[string]string{"content": text})
	if err != nil {
		return err
	}
	result, err := NewAgentResult("result_"+uuid.NewString(), runID, contract, payload)
	if err != nil {
		return err
	}
	if err := StoreAgentResultInTx(ctx, tx, result); err != nil {
		return err
	}
	tag, err := tx.Exec(ctx, `
		update agent_trees
		set result_bytes_consumed=result_bytes_consumed+$2,updated_at=now()
		where id=(select tree_id from agent_runs where id=$1)
		  and result_bytes_consumed+$2<=result_byte_limit
	`, runID, result.PayloadBytes)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return errors.New("Agent Tree Result budget exhausted")
	}
	return nil
}

func valueOrEmptyFinal(draft *models.FinalDraft) models.FinalDraft {
	if draft == nil {
		return models.FinalDraft{}
	}
	return *draft
}

type publicationState int

const (
	publicationLeaseLost publicationState = iota
	publicationCurrent
	publicationCompleted
	publicationDeadline
)

func (r *PostgresRuntime) reconcilePublication(ctx context.Context, attempt Attempt) (publicationState, error) {
	tx, err := r.workerTx(ctx)
	if err != nil {
		return publicationLeaseLost, err
	}
	defer tx.Rollback(ctx)
	var runStatus, jobStatus string
	var outputMessageID *string
	var currentLease, deadlineValid bool
	err = tx.QueryRow(ctx, `
		select r.status, r.output_message_id, j.status,
			coalesce(j.id = $2 and j.lease_token = $3::uuid and j.lease_expires_at > now(), false),
			coalesce(r.deadline_at,tree.absolute_deadline) > now()
		from agent_runs r
		join agent_jobs j on j.run_id = r.id
		left join agent_trees tree on tree.id=r.tree_id
		where r.id = $1`, attempt.RunID, attempt.JobID, attempt.LeaseToken).
		Scan(&runStatus, &outputMessageID, &jobStatus, &currentLease, &deadlineValid)
	if errors.Is(err, pgx.ErrNoRows) {
		return publicationLeaseLost, nil
	}
	if err != nil {
		return publicationLeaseLost, err
	}
	if err := tx.Commit(ctx); err != nil {
		return publicationLeaseLost, err
	}
	if runStatus == "completed" && outputMessageID != nil && jobStatus == "succeeded" {
		return publicationCompleted, nil
	}
	if runStatus == "running" && jobStatus == "running" && outputMessageID == nil && !deadlineValid {
		return publicationDeadline, nil
	}
	if runStatus == "running" && jobStatus == "running" && outputMessageID == nil && currentLease && deadlineValid {
		return publicationCurrent, nil
	}
	return publicationLeaseLost, nil
}

func (r *PostgresRuntime) Fail(ctx context.Context, attempt Attempt, errorCode string) error {
	if errorCode == "" {
		errorCode = string(models.ErrorUnavailable)
	}
	tx, err := r.workerTx(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	traceCtx, traceScope, err := r.beginTraceScope(ctx)
	if err != nil {
		return err
	}
	if traceScope != nil {
		defer traceScope.Rollback()
	}
	var jobID string
	err = tx.QueryRow(ctx, `
		select j.id
		from agent_runs r
		join agent_jobs j on j.run_id = r.id
		where r.id = $1 and j.id = $2 and j.lease_token = $3::uuid
			and j.lease_expires_at > now()
			and r.status = 'running' and j.status = 'running'
		for update of r, j`, attempt.RunID, attempt.JobID, attempt.LeaseToken).Scan(&jobID)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrLeaseLost
	}
	if err != nil {
		return err
	}
	runTag, err := tx.Exec(ctx, `
		update agent_runs
		set status = 'failed', error_code = $2, finished_at = now(), updated_at = now()
		where id = $1 and status = 'running' and output_message_id is null`, attempt.RunID, errorCode)
	if err != nil {
		return err
	}
	jobTag, err := tx.Exec(ctx, `
		update agent_jobs
		set status = 'failed', lease_token = null, lease_expires_at = null,
			last_error_code = $3, finished_at = now(), updated_at = now()
		where id = $1 and status = 'running' and lease_token = $2::uuid`, jobID, attempt.LeaseToken, errorCode)
	if err != nil {
		return err
	}
	if runTag.RowsAffected() != 1 || jobTag.RowsAffected() != 1 {
		return errors.New("Run failure did not transition Run and Job together")
	}
	if _, err := tx.Exec(ctx, `select pg_notify('nano_agent_runs', $1)`, attempt.RunID); err != nil {
		return err
	}
	if err := RecordRunTerminalInTx(traceCtx, tx, attempt.RunID, RunTerminalTrace{
		RunStatus: "failed", SpanStatus: agentobs.StatusError, ErrorCode: errorCode, AttemptNo: attempt.AttemptNo,
	}); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	r.recordTerminalMetrics(ctx, attempt.RunID, attempt.AttemptNo, "failed", errorCode)
	publishCommittedTrace(traceCtx, traceScope)
	return nil
}

func (r *PostgresRuntime) workerTx(ctx context.Context) (pgx.Tx, error) {
	if r.pool == nil {
		return nil, errors.New("nil PostgreSQL pool")
	}
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx, `set local role nano_worker`); err != nil {
		tx.Rollback(ctx)
		return nil, fmt.Errorf("set worker role: %w", err)
	}
	return tx, nil
}
