package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"unicode/utf8"

	"github.com/huangxinxinyu/nano-notebook/internal/agentobs"
	"github.com/huangxinxinyu/nano-notebook/internal/agentobs/instrumentation"
	"github.com/huangxinxinyu/nano-notebook/internal/models"
	"github.com/jackc/pgx/v5"
)

const (
	CompactionSchemaVersion  = 1
	CompactionPromptVersion  = "agent-context-compaction-v1"
	maxCompactionResultChars = 2000
)

const CompactionSystemPrompt = `Summarize the supplied older Agent context for continuation.
Preserve the user goal, current request state, constraints, preferences, decisions, important facts and identifiers learned from tools, completed work, accepted failures, unresolved work, and next steps.
Never claim that a failed or interrupted tool succeeded. Do not include hidden reasoning. Return only the continuation summary.`

func (r *PostgresRuntime) PrepareDecisionRequest(
	ctx context.Context,
	execution Execution,
	prefix CheckpointPrefix,
	definitions []models.ActionDefinition,
	model DecisionModel,
	triggerReason string,
) (models.ModelRequest, error) {
	return r.prepareDecisionRequest(ctx, nil, execution, prefix, definitions, model, triggerReason)
}

func (r *PostgresRuntime) PrepareDecisionRequestTraced(
	ctx context.Context,
	tracer *agentobs.Tracer,
	execution Execution,
	prefix CheckpointPrefix,
	definitions []models.ActionDefinition,
	model DecisionModel,
	triggerReason string,
) (models.ModelRequest, error) {
	return r.prepareDecisionRequest(ctx, tracer, execution, prefix, definitions, model, triggerReason)
}

func (r *PostgresRuntime) prepareDecisionRequest(
	ctx context.Context,
	tracer *agentobs.Tracer,
	execution Execution,
	prefix CheckpointPrefix,
	definitions []models.ActionDefinition,
	model DecisionModel,
	triggerReason string,
) (prepared models.ModelRequest, prepareErr error) {
	rawUnits, err := r.projectChatLane(ctx, execution, prefix)
	if err != nil {
		return models.ModelRequest{}, err
	}
	latest, hasLatest, err := r.loadLatestContextCompaction(ctx, execution.ChatID)
	if err != nil {
		return models.ModelRequest{}, err
	}
	projected := rawUnits
	if hasLatest {
		projected, err = ApplyContextCompaction(rawUnits, latest)
		if err != nil {
			return models.ModelRequest{}, err
		}
	}
	systemPrompt := r.contextSystemPrompt(execution)
	projected, request := buildAgedProjectedRequest(execution, systemPrompt, projected, definitions)
	request.InvocationPolicy = execution.ModelInvocation
	request, err = r.FinalizeDecisionRequest(ctx, execution, prefix, request)
	if err != nil {
		return models.ModelRequest{}, err
	}
	before, err := EstimateModelRequestTokens(request)
	if err != nil {
		return models.ModelRequest{}, err
	}
	attachContextTelemetry(&request, execution, projected, before, optionalCompaction(latest, hasLatest))
	if triggerReason == "" && before.Tokens <= execution.ModelContext.Budgets.CompactionTriggerTokens {
		return request, nil
	}
	if triggerReason == "" {
		triggerReason = CompactionTriggerThreshold
	}
	if triggerReason != CompactionTriggerThreshold && triggerReason != CompactionTriggerProviderOverflow {
		return models.ModelRequest{}, errors.New("invalid Compaction trigger")
	}
	if model == nil || execution.ModelContext.Policy.KeepRecentTokens < 1 {
		return models.ModelRequest{}, ErrContextBudgetExceeded
	}
	if tracer != nil {
		identity := TraceCompactionStartIdentity(execution.RunID, execution.AttemptNo, latest.ID, triggerReason)
		traceCtx, _, traceErr := tracer.StartSpan(ctx, agentobs.SpanStart{
			IdentityKey: identity,
			Name:        TraceSpanContextCompaction,
			Attributes: []agentobs.Attribute{
				agentobs.String(TraceKeyProviderCapability, execution.ModelContext.Capability.Identity),
				agentobs.String(TraceKeyContextPolicy, execution.ModelContext.Policy.Identity),
				agentobs.String(TraceKeyCompactionTriggerReason, triggerReason),
				agentobs.Int64(TraceKeyBeforeCompactionTokens, int64(before.Tokens)),
				agentobs.Int64(TraceKeyCompactionTriggerTokens, int64(execution.ModelContext.Budgets.CompactionTriggerTokens)),
			},
		})
		if traceErr != nil {
			return models.ModelRequest{}, &instrumentation.RecordingError{Phase: instrumentation.RecordingStart, Err: traceErr}
		}
		ctx = traceCtx
		defer func() {
			status := agentobs.StatusOK
			attributes := []agentobs.Attribute(nil)
			if prepareErr != nil {
				status = agentobs.StatusError
			} else {
				attributes = modelContextTraceAttributes(prepared.ContextTelemetry)
			}
			if endErr := tracer.EndSpan(ctx, agentobs.SpanEnd{Name: TraceSpanContextCompaction, Status: status, Attributes: attributes}); endErr != nil {
				prepareErr = errors.Join(prepareErr, &instrumentation.RecordingError{Phase: instrumentation.RecordingTerminal, Err: endErr})
			}
		}()
	}
	cut, err := SelectCompactionCut(rawUnits, execution.ModelContext.Policy.KeepRecentTokens)
	if err != nil {
		return models.ModelRequest{}, err
	}
	summaryUnits := rawUnits[:cut]
	if hasLatest {
		oldSuffix := contextUnitIndex(rawUnits, latest.SuffixStart)
		if oldSuffix < 1 || cut <= oldSuffix {
			return models.ModelRequest{}, ErrContextBudgetExceeded
		}
		priorSummary := ContextUnit{
			Kind:     ContextUnitCompactionSummary,
			Messages: []models.ModelMessage{{Role: models.RoleUser, Content: "<summary>" + latest.Summary + "</summary>"}},
		}
		summaryUnits = append([]ContextUnit{priorSummary}, rawUnits[oldSuffix:cut]...)
	}
	if err := r.CheckAuthority(ctx, execution.Attempt); err != nil {
		return models.ModelRequest{}, err
	}
	summary, err := generateContextSummary(ctx, model, execution, summaryUnits)
	if err != nil {
		return models.ModelRequest{}, err
	}
	candidate := ContextCompaction{
		PredecessorID: latest.ID, Summary: summary,
		SummarizedThrough: ContextUnitKey(rawUnits[cut-1]), SuffixStart: ContextUnitKey(rawUnits[cut]),
		TriggerReason: triggerReason, BeforeTokens: before.Tokens,
	}
	candidate.ID = compactionIdentity(execution.ChatID, candidate, execution.ModelContext.Policy.SHA256)
	candidateUnits, err := ApplyContextCompaction(rawUnits, candidate)
	if err != nil {
		return models.ModelRequest{}, err
	}
	_, afterRequest := buildAgedProjectedRequest(execution, systemPrompt, candidateUnits, definitions)
	afterRequest.InvocationPolicy = execution.ModelInvocation
	afterRequest, err = r.FinalizeDecisionRequest(ctx, execution, prefix, afterRequest)
	if err != nil {
		return models.ModelRequest{}, err
	}
	after, err := EstimateModelRequestTokens(afterRequest)
	if err != nil {
		return models.ModelRequest{}, err
	}
	candidate.AfterTokens = after.Tokens
	if after.Tokens > execution.ModelContext.Budgets.SafeInputTokens || after.Tokens >= before.Tokens {
		return models.ModelRequest{}, ErrContextBudgetExceeded
	}
	if err := r.CheckAuthority(ctx, execution.Attempt); err != nil {
		return models.ModelRequest{}, err
	}
	accepted, err := r.appendContextCompaction(ctx, execution, candidate)
	if err != nil {
		return models.ModelRequest{}, err
	}
	acceptedUnits, err := ApplyContextCompaction(rawUnits, accepted)
	if err != nil {
		return models.ModelRequest{}, err
	}
	acceptedUnits, acceptedRequest := buildAgedProjectedRequest(execution, systemPrompt, acceptedUnits, definitions)
	acceptedRequest.InvocationPolicy = execution.ModelInvocation
	acceptedRequest, err = r.FinalizeDecisionRequest(ctx, execution, prefix, acceptedRequest)
	if err != nil {
		return models.ModelRequest{}, err
	}
	acceptedCount, err := EstimateModelRequestTokens(acceptedRequest)
	if err != nil || acceptedCount.Tokens > execution.ModelContext.Budgets.SafeInputTokens {
		return models.ModelRequest{}, ErrContextBudgetExceeded
	}
	attachContextTelemetry(&acceptedRequest, execution, acceptedUnits, acceptedCount, &accepted)
	return acceptedRequest, nil
}

func (r *PostgresRuntime) contextSystemPrompt(execution Execution) string {
	systemPrompt := r.systemPrompt
	if execution.PromptVersion == GroundedPromptVersion && systemPrompt == BareSystemPrompt {
		return GroundedSystemPrompt
	}
	return systemPrompt
}

func (r *PostgresRuntime) loadLatestContextCompaction(ctx context.Context, chatID string) (ContextCompaction, bool, error) {
	tx, err := r.workerTx(ctx)
	if err != nil {
		return ContextCompaction{}, false, err
	}
	defer tx.Rollback(ctx)
	var value ContextCompaction
	err = tx.QueryRow(ctx, `
		select id,coalesce(predecessor_id,''),summary_text,summarized_through,suffix_start,
			trigger_reason,before_tokens,after_tokens
		from agent_context_compactions where chat_id=$1
		order by created_at desc,id desc limit 1
	`, chatID).Scan(&value.ID, &value.PredecessorID, &value.Summary, &value.SummarizedThrough,
		&value.SuffixStart, &value.TriggerReason, &value.BeforeTokens, &value.AfterTokens)
	if errors.Is(err, pgx.ErrNoRows) {
		return ContextCompaction{}, false, tx.Commit(ctx)
	}
	if err != nil {
		return ContextCompaction{}, false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return ContextCompaction{}, false, err
	}
	return value, true, nil
}

func (r *PostgresRuntime) appendContextCompaction(ctx context.Context, execution Execution, value ContextCompaction) (ContextCompaction, error) {
	tx, err := r.workerTx(ctx)
	if err != nil {
		return ContextCompaction{}, err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `select pg_advisory_xact_lock(hashtext($1))`, execution.ChatID); err != nil {
		return ContextCompaction{}, err
	}
	var current ContextCompaction
	currentErr := tx.QueryRow(ctx, `
		select id,coalesce(predecessor_id,''),summary_text,summarized_through,suffix_start,
			trigger_reason,before_tokens,after_tokens
		from agent_context_compactions where chat_id=$1
		order by created_at desc,id desc limit 1
	`, execution.ChatID).Scan(&current.ID, &current.PredecessorID, &current.Summary, &current.SummarizedThrough,
		&current.SuffixStart, &current.TriggerReason, &current.BeforeTokens, &current.AfterTokens)
	if currentErr != nil && !errors.Is(currentErr, pgx.ErrNoRows) {
		return ContextCompaction{}, currentErr
	}
	if currentErr == nil && current.ID != value.PredecessorID {
		if err := tx.Commit(ctx); err != nil {
			return ContextCompaction{}, err
		}
		return current, nil
	}
	if errors.Is(currentErr, pgx.ErrNoRows) && value.PredecessorID != "" {
		return ContextCompaction{}, projectionError("Compaction predecessor is missing")
	}
	idempotencyKey := strings.TrimPrefix(value.ID, "cmp_")
	_, err = tx.Exec(ctx, `
		insert into agent_context_compactions(
			id,chat_id,predecessor_id,idempotency_key,summarized_through,suffix_start,summary_text,
			schema_version,summarizer_model,prompt_version,
			provider_capability_identity,provider_capability_version,provider_capability_sha256,
			model_context_policy_identity,model_context_policy_version,model_context_policy_sha256,
			tokenizer_identity,tokenizer_version,hard_input_tokens,safe_input_tokens,soft_trigger_tokens,
			keep_recent_tokens,summary_max_output_tokens,before_tokens,after_tokens,trigger_reason
		) values(
			$1,$2,nullif($3,''),$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,
			$19,$20,$21,$22,$23,$24,$25,$26
		) on conflict(idempotency_key) do nothing
	`, value.ID, execution.ChatID, value.PredecessorID, idempotencyKey, value.SummarizedThrough, value.SuffixStart,
		value.Summary, CompactionSchemaVersion, execution.Model, CompactionPromptVersion,
		execution.ModelContext.Capability.Identity, execution.ModelContext.Capability.Version, execution.ModelContext.Capability.SHA256,
		execution.ModelContext.Policy.Identity, execution.ModelContext.Policy.Version, execution.ModelContext.Policy.SHA256,
		execution.ModelContext.Capability.TokenizerIdentity, execution.ModelContext.Capability.TokenizerVersion,
		execution.ModelContext.Budgets.HardInputTokens, execution.ModelContext.Budgets.SafeInputTokens,
		execution.ModelContext.Budgets.CompactionTriggerTokens, execution.ModelContext.Policy.KeepRecentTokens,
		execution.ModelContext.Policy.SummaryMaxOutputTokens, value.BeforeTokens, value.AfterTokens, value.TriggerReason)
	if err != nil {
		return ContextCompaction{}, err
	}
	var accepted ContextCompaction
	if err := tx.QueryRow(ctx, `
		select id,coalesce(predecessor_id,''),summary_text,summarized_through,suffix_start,
			trigger_reason,before_tokens,after_tokens
		from agent_context_compactions where idempotency_key=$1
	`, idempotencyKey).Scan(&accepted.ID, &accepted.PredecessorID, &accepted.Summary, &accepted.SummarizedThrough,
		&accepted.SuffixStart, &accepted.TriggerReason, &accepted.BeforeTokens, &accepted.AfterTokens); err != nil {
		return ContextCompaction{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return ContextCompaction{}, err
	}
	return accepted, nil
}

func generateContextSummary(ctx context.Context, model DecisionModel, execution Execution, units []ContextUnit) (string, error) {
	payload, err := serializeCompactionInput(units)
	if err != nil {
		return "", err
	}
	temperature := 0.0
	outcome, err := model.Decide(ctx, models.ModelRequest{
		Model: execution.Model,
		Messages: []models.ModelMessage{
			{Role: models.RoleSystem, Content: CompactionSystemPrompt},
			{Role: models.RoleUser, Content: payload},
		},
		InvocationPolicy: models.ModelInvocationPolicy{
			Temperature: &temperature, MaxOutputTokens: execution.ModelContext.Policy.SummaryMaxOutputTokens,
			Timeout: execution.ModelInvocation.Timeout, EnableThinking: execution.ModelInvocation.EnableThinking,
		},
	})
	if err != nil {
		return "", err
	}
	if err := outcome.ModelDecision.Validate(); err != nil || outcome.ModelDecision.Final == nil {
		return "", errors.New("Compaction summarizer did not return a Final")
	}
	summary := strings.TrimSpace(outcome.ModelDecision.Final.Text)
	if summary == "" {
		return "", errors.New("Compaction summary is empty")
	}
	return summary, nil
}

func serializeCompactionInput(units []ContextUnit) (string, error) {
	type serializedMessage struct {
		Role         models.ModelRole         `json:"role"`
		Content      string                   `json:"content,omitempty"`
		ActionCalls  []models.ModelActionCall `json:"action_calls,omitempty"`
		ActionCallID string                   `json:"action_call_id,omitempty"`
	}
	messages := make([]serializedMessage, 0)
	for _, unit := range units {
		for _, message := range unit.Messages {
			content := message.Content
			if message.Role == models.RoleAction && utf8.RuneCountInString(content) > maxCompactionResultChars {
				content = string([]rune(content)[:maxCompactionResultChars]) + "...[truncated for summary input]"
			}
			messages = append(messages, serializedMessage{
				Role: message.Role, Content: content, ActionCalls: message.ActionCalls, ActionCallID: message.ActionCallID,
			})
		}
	}
	encoded, err := json.Marshal(messages)
	return string(encoded), err
}

func compactionIdentity(chatID string, value ContextCompaction, policySHA string) string {
	digest := sha256.Sum256([]byte(strings.Join([]string{
		chatID, value.PredecessorID, value.SummarizedThrough, value.SuffixStart, policySHA,
	}, "\x00")))
	return "cmp_" + hex.EncodeToString(digest[:])
}

func contextUnitIndex(units []ContextUnit, key string) int {
	for index, unit := range units {
		if ContextUnitKey(unit) == key {
			return index
		}
	}
	return -1
}

func optionalCompaction(value ContextCompaction, ok bool) *ContextCompaction {
	if !ok {
		return nil
	}
	return &value
}

func attachContextTelemetry(request *models.ModelRequest, execution Execution, units []ContextUnit, count ContextTokenCount, compaction *ContextCompaction) {
	if request == nil || execution.ModelContext.Policy.Identity == "" || execution.ModelContext.Capability.Identity == "" {
		return
	}
	exactSuffixTokens := 0
	start := 0
	if len(units) > 0 && units[0].Kind == ContextUnitCompactionSummary {
		start = 1
	}
	for _, unit := range units[start:] {
		unitTokens, err := estimateContextUnitTokens(unit)
		if err == nil {
			exactSuffixTokens += unitTokens
		}
	}
	statusTelemetry := request.ContextTelemetry
	request.ContextTelemetry = models.ModelContextTelemetry{
		ProviderCapabilityIdentity: execution.ModelContext.Capability.Reference().String(),
		ContextPolicyIdentity:      execution.ModelContext.Policy.Reference().String(),
		ContextWindowTokens:        execution.ModelContext.Capability.ContextWindowTokens,
		ProviderMaxInputTokens:     execution.ModelContext.Capability.MaxInputTokens,
		ProviderMaxOutputTokens:    execution.ModelContext.Capability.MaxOutputTokens,
		PinnedMaxOutputTokens:      execution.ModelContext.Policy.PinnedMaxOutputTokens,
		EstimationSafetyTokens:     execution.ModelContext.Policy.EstimationSafetyTokens,
		HardInputTokens:            execution.ModelContext.Budgets.HardInputTokens,
		SafeInputTokens:            execution.ModelContext.Budgets.SafeInputTokens,
		CompactionTriggerTokens:    execution.ModelContext.Budgets.CompactionTriggerTokens,
		InputTokens:                count.Tokens,
		InputTokenSource:           string(count.Source),
		ExactSuffixTokens:          exactSuffixTokens,
		AgentStatusInjected:        statusTelemetry.AgentStatusInjected,
		AgentStatusBytes:           statusTelemetry.AgentStatusBytes,
		AgentStatusTokens:          statusTelemetry.AgentStatusTokens,
		TodoRevision:               statusTelemetry.TodoRevision,
		TodoPendingCount:           statusTelemetry.TodoPendingCount,
		TodoInProgressCount:        statusTelemetry.TodoInProgressCount,
		TodoCompletedCount:         statusTelemetry.TodoCompletedCount,
		TodoCancelledCount:         statusTelemetry.TodoCancelledCount,
		MaxToolInputRepeatCount:    statusTelemetry.MaxToolInputRepeatCount,
		AgedResultCount:            statusTelemetry.AgedResultCount,
		AgedResultBytes:            statusTelemetry.AgedResultBytes,
	}
	if compaction != nil {
		request.ContextTelemetry.CompactionID = compaction.ID
		request.ContextTelemetry.SummarizedThrough = compaction.SummarizedThrough
		request.ContextTelemetry.CompactionTriggerReason = compaction.TriggerReason
		request.ContextTelemetry.BeforeCompactionTokens = compaction.BeforeTokens
		request.ContextTelemetry.AfterCompactionTokens = compaction.AfterTokens
	}
}

var _ ContextPreparationRuntime = (*PostgresRuntime)(nil)
