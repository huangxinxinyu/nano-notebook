package agent

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/huangxinxinyu/nano-notebook/internal/agentcatalog"
	"github.com/huangxinxinyu/nano-notebook/internal/agentobs"
	"github.com/huangxinxinyu/nano-notebook/internal/agentobs/instrumentation"
	"github.com/huangxinxinyu/nano-notebook/internal/models"
)

const (
	ErrorAgentBudgetExhausted = "agent_budget_exhausted"
	ErrorAgentTraceInvalid    = "agent_trace_invalid"
	ErrorActionInterrupted    = "action_interrupted"
)

type ControllerRuntime interface {
	Load(context.Context, Attempt) (Execution, error)
	LoadCheckpointPrefix(context.Context, Attempt) (CheckpointPrefix, error)
	BuildDecisionRequest(context.Context, Execution, CheckpointPrefix, []models.ActionDefinition) (models.ModelRequest, error)
	CheckAuthority(context.Context, Attempt) error
	AppendCheckpoint(context.Context, Attempt, PendingCheckpoint) (Checkpoint, error)
	PublishFinal(context.Context, Attempt, models.FinalDraft) error
	Fail(context.Context, Attempt, string) error
}

type DecisionModel interface {
	Decide(context.Context, models.ModelRequest) (models.ModelOutcome, error)
}

type AttemptTraceRuntime interface {
	StartAttemptTrace(context.Context, Attempt) (context.Context, *agentobs.Tracer, error)
}

type ActionRetryTraceRuntime interface {
	PreviousActionSpan(context.Context, Attempt, string) (agentobs.SpanContext, bool, error)
}

type ReplayTraceRuntime interface {
	ReplayStager() ReplayStager
}

type ContextPreparationRuntime interface {
	PrepareDecisionRequest(context.Context, Execution, CheckpointPrefix, []models.ActionDefinition, DecisionModel, string) (models.ModelRequest, error)
}

type ContextPreparationTraceRuntime interface {
	PrepareDecisionRequestTraced(context.Context, *agentobs.Tracer, Execution, CheckpointPrefix, []models.ActionDefinition, DecisionModel, string) (models.ModelRequest, error)
}

// InvalidModelResponseRecoveryRuntime opts a runtime into bounded retries for a
// provider response that could not be decoded into the Agent decision contract.
// No checkpoint has been accepted at this point, so repeating the model call
// cannot replay an external Action.
type InvalidModelResponseRecoveryRuntime interface {
	InvalidModelResponseRetryLimit() int
}

// DecisionResponsePreparationRuntime validates or canonicalizes a model
// decision before the append-only checkpoint boundary. Returning an error
// classifies the unaccepted response as invalid and lets an opted-in runtime
// recover without persisting a bad Final or replaying an Action.
type DecisionResponsePreparationRuntime interface {
	PrepareDecisionResponse(context.Context, Execution, CheckpointPrefix, models.ModelDecision) (models.ModelDecision, error)
}

type DecisionRequestFinalizerRuntime interface {
	FinalizeDecisionRequest(context.Context, Execution, CheckpointPrefix, models.ModelRequest) (models.ModelRequest, error)
}

// QueryContextRuntime is implemented by runtimes that must force their
// decision loop's first Action to be a specific, isolated-query search
// before any free tool choice — today, only Studio Output generation
// (studioRuntime), which always needs exactly one evidence-gathering search
// before composing. PostgresRuntime (the Leader chat loop) intentionally
// does not implement this: chat's decision loop is fully free from decision
// 1 onward (see docs/superpowers/specs/2026-08-04-prompt-driven-leader-decision-loop-design.md).
type QueryContextRuntime interface {
	BuildQueryContextRequest(context.Context, Execution, models.ActionDefinition) (models.ModelRequest, models.ActionProposal, int, error)
}

type FinalPreparationRuntime interface {
	PrepareFinal(context.Context, Attempt, Execution, CheckpointPrefix, models.FinalDraft) (models.FinalDraft, error)
}

type FinalPreparationTraceRuntime interface {
	PrepareFinalTraced(context.Context, *agentobs.Tracer, Attempt, Execution, CheckpointPrefix, models.FinalDraft) (models.FinalDraft, error)
}

type Controller struct {
	runtime                   ControllerRuntime
	model                     DecisionModel
	registry                  *ActionRegistry
	mcpHost                   *MCPToolHost
	mcpDefinition             agentcatalog.Reference
	metrics                   *TaskMetricsRecorder
	toolResults               *ToolResultExternalizer
	toolResultInlineByteLimit int
}

// WithControllerMetrics attaches the Sprint 12 task-lifecycle metrics
// recorder and returns the same Controller, chainable onto either
// constructor below.
func (c *Controller) WithControllerMetrics(recorder *TaskMetricsRecorder) *Controller {
	c.metrics = recorder
	return c
}

func (c *Controller) WithToolResultCache(externalizer *ToolResultExternalizer, inlineByteLimit int) *Controller {
	c.toolResults = externalizer
	c.toolResultInlineByteLimit = inlineByteLimit
	return c
}

var _ ControllerRuntime = (*PostgresRuntime)(nil)
var _ DecisionModel = (*models.BifrostClient)(nil)

func NewController(runtime ControllerRuntime, model DecisionModel, registry *ActionRegistry) *Controller {
	return &Controller{runtime: runtime, model: model, registry: registry}
}

func NewMCPController(runtime ControllerRuntime, model DecisionModel, registry *ActionRegistry, host *MCPToolHost, definition agentcatalog.Reference) *Controller {
	return &Controller{runtime: runtime, model: model, registry: registry, mcpHost: host, mcpDefinition: definition}
}

func (c *Controller) actionDefinitions(ctx context.Context, session *MCPAttemptSession, policy ActionPolicy, tracer *agentobs.Tracer) ([]models.ActionDefinition, error) {
	if session == nil {
		return c.registry.Definitions(ctx, policy, tracer), nil
	}
	return session.ActionDefinitions(ctx, policy, tracer)
}

func (c *Controller) validateProposal(session *MCPAttemptSession, proposals []models.ActionProposal) error {
	if session == nil {
		return c.registry.ValidateProposal(proposals)
	}
	return session.ValidateProposal(proposals)
}

func (c *Controller) Execute(ctx context.Context, attempt Attempt) error {
	if c.runtime == nil || c.model == nil || c.registry == nil {
		return errors.New("Agent Controller dependencies are incomplete")
	}
	execution, err := c.runtime.Load(ctx, attempt)
	if err != nil {
		return err
	}
	if starter, ok := c.runtime.(RunStarter); ok {
		if err := starter.StartRun(ctx, attempt, execution); err != nil {
			return err
		}
	}
	var toolSession *MCPAttemptSession
	if c.mcpHost != nil {
		toolSession, err = c.mcpHost.OpenAttempt(ctx, AttemptToolScope{
			Definition: c.mcpDefinition, Attempt: attempt, DefaultTimeZone: execution.TimeZone,
			UserID: execution.UserID, ChatID: execution.ChatID,
			RemainingActions: execution.ActionLimit, Deadline: execution.DeadlineAt,
		})
		if err != nil {
			return err
		}
		defer toolSession.Close()
	}
	var tracer *agentobs.Tracer
	if traceRuntime, ok := c.runtime.(AttemptTraceRuntime); ok {
		ctx, tracer, err = traceRuntime.StartAttemptTrace(ctx, attempt)
		if err != nil {
			return err
		}
	}
	recoveryBoundary := true
	for {
		prefix, err := c.runtime.LoadCheckpointPrefix(ctx, attempt)
		if err != nil {
			return c.handleRuntimeError(ctx, attempt, err)
		}
		if prefix.Final != nil {
			return c.runtime.PublishFinal(ctx, attempt, *prefix.Final)
		}
		if proposal, pending, ok := incompleteActions(prefix); ok {
			if recoveryBoundary && attempt.AttemptNo > 1 {
				closed, closeErr := c.closeUnknownRecoveredActions(ctx, toolSession, attempt, proposal, pending)
				if closeErr != nil {
					return closeErr
				}
				recoveryBoundary = false
				if closed {
					continue
				}
			}
			recoveryBoundary = false
			action := pending[0]
			if c.isExclusiveDelegation(toolSession, action.Name) && execution.ExistingChildCount == 0 {
				return c.executeDelegationAction(ctx, tracer, toolSession, attempt, execution, action)
			}
			if len(pending) > 1 && c.allParallelEligible(toolSession, pending) {
				if err := c.executeParallelBatch(ctx, tracer, toolSession, attempt, execution, prefix, proposal, pending); err != nil {
					return err
				}
				continue
			}
			if err := c.executeAction(ctx, tracer, toolSession, attempt, execution, prefix, proposal, action); err != nil {
				return err
			}
			continue
		}
		recoveryBoundary = false

		remainingActions := execution.ActionLimit - acceptedBusinessActions(prefix)
		businessDecisionAvailable := acceptedBusinessDecisions(prefix) < execution.ActionDecisionLimit && remainingActions > 0
		advertisedBusinessActions := remainingActions
		if !businessDecisionAvailable {
			advertisedBusinessActions = 0
		}
		definitions, err := c.actionDefinitions(ctx, toolSession, ActionPolicy{
			RemainingActions: advertisedBusinessActions, Execution: &execution,
		}, tracer)
		if err != nil {
			return c.handleRuntimeError(ctx, attempt, err)
		}
		actionCapable := len(definitions) > 0
		if !actionCapable && execution.FinalDecisionLimit < 1 {
			return c.fail(ctx, attempt, ErrorAgentBudgetExhausted, errors.New("no reserved Final decision is available"))
		}
		if queryContextRuntime, ok := c.runtime.(QueryContextRuntime); ok {
			requiredAction, requiredErr := groundedRequiredAction(prefix)
			if requiredErr != nil {
				return c.fail(ctx, attempt, "context_failed", requiredErr)
			}
			if requiredAction != "" {
				if !actionCapable {
					return c.fail(ctx, attempt, ErrorAgentBudgetExhausted, ErrGroundingIncomplete)
				}
				definition, ok := actionDefinitionByName(definitions, requiredAction)
				if !ok {
					return c.fail(ctx, attempt, "context_failed", ErrGroundingIncomplete)
				}
				if err := c.acceptContextualizedSearch(ctx, tracer, queryContextRuntime, toolSession, attempt, execution, prefix, definition); err != nil {
					return err
				}
				continue
			}
		}
		var outcome models.ModelOutcome
		overflowRecoveryAttempt := 0
		invalidResponseRecoveryAttempt := 0
		invalidResponseRecoveryDetail := ""
		for {
			triggerReason := ""
			if overflowRecoveryAttempt > 0 {
				triggerReason = CompactionTriggerProviderOverflow
			}
			var request models.ModelRequest
			if contextRuntime, ok := c.runtime.(ContextPreparationTraceRuntime); ok && tracer != nil {
				request, err = contextRuntime.PrepareDecisionRequestTraced(ctx, tracer, execution, prefix, definitions, c.model, triggerReason)
			} else if contextRuntime, ok := c.runtime.(ContextPreparationRuntime); ok {
				request, err = contextRuntime.PrepareDecisionRequest(ctx, execution, prefix, definitions, c.model, triggerReason)
			} else {
				request, err = c.runtime.BuildDecisionRequest(ctx, execution, prefix, definitions)
			}
			if err != nil {
				if handled, result := c.handleRecordingError(ctx, attempt, err); handled {
					return result
				}
				return c.fail(ctx, attempt, "context_failed", err)
			}
			request.ContextTelemetry.OverflowRecoveryAttempt = overflowRecoveryAttempt
			request.InvocationPolicy = execution.ModelInvocation
			if invalidResponseRecoveryAttempt > 0 {
				detail := ""
				if invalidResponseRecoveryDetail != "" {
					detail = " Validation failure: " + invalidResponseRecoveryDetail + "."
				}
				request.Messages = append(request.Messages, models.ModelMessage{
					Role:    models.RoleSystem,
					Content: "The previous model response was invalid and was not accepted." + detail + " Retry this same decision now. Return exactly one valid decision matching the provided contract: either a tool-call batch with valid JSON arguments and unique call IDs, or a complete final answer. Do not describe the repair and do not repeat already completed tool calls.",
				})
			}
			if finalizer, ok := c.runtime.(DecisionRequestFinalizerRuntime); ok {
				request, err = finalizer.FinalizeDecisionRequest(ctx, execution, prefix, request)
				if err != nil {
					return c.fail(ctx, attempt, "context_failed", err)
				}
			}
			if err := c.runtime.CheckAuthority(ctx, attempt); err != nil {
				return c.handleRuntimeError(ctx, attempt, err)
			}
			if tracer != nil {
				decisionNo := prefix.AcceptedDecisions + 1
				modelIdentity := TraceModelStartIdentity(attempt.RunID, attempt.AttemptNo, decisionNo)
				if overflowRecoveryAttempt > 0 {
					modelIdentity = fmt.Sprintf("%s/overflow-recovery/%d", modelIdentity, overflowRecoveryAttempt)
				}
				if invalidResponseRecoveryAttempt > 0 {
					modelIdentity = fmt.Sprintf("%s/invalid-response-recovery/%d", modelIdentity, invalidResponseRecoveryAttempt)
				}
				outcome, err = InvokeDecisionModel(ctx, tracer, c.model, request, decisionNo, ModelTraceOptions{
					StartIdentity: modelIdentity, RequestIdentity: modelIdentity + "/replay/request",
					DecisionIdentity: modelIdentity + "/replay/decision", ReplayStager: c.replayStager(),
					Role: RoleLeader, Prompt: composerPromptTraceRef(execution.PromptVersion),
					Metrics: c.metrics,
				})
			} else {
				outcome, err = c.model.Decide(ctx, request)
			}
			if err == nil {
				if runtime, ok := c.runtime.(DecisionResponsePreparationRuntime); ok {
					prepared, prepareErr := runtime.PrepareDecisionResponse(ctx, execution, prefix, outcome.ModelDecision)
					if prepareErr != nil {
						invalidResponseRecoveryDetail = prepareErr.Error()
						err = &models.ModelError{Kind: models.ErrorInvalidResponse, Err: prepareErr}
					} else {
						outcome.ModelDecision = prepared
					}
				}
			}
			if err == nil {
				if validationErr := c.validateDecisionForRequest(toolSession, prefix, outcome.ModelDecision, definitions, actionCapable, execution, remainingActions, businessDecisionAvailable); validationErr != nil {
					invalidResponseRecoveryDetail = validationErr.Error()
					err = &models.ModelError{Kind: models.ErrorInvalidResponse, Err: validationErr}
				}
			}
			if isModelInvalidResponse(err) {
				slog.WarnContext(ctx, "Agent model response invalid", "run_id", attempt.RunID, "attempt", attempt.AttemptNo,
					"recovery_attempt", invalidResponseRecoveryAttempt, "detail", invalidResponseRecoveryDetail, "error", err)
				limit := 0
				if runtime, ok := c.runtime.(InvalidModelResponseRecoveryRuntime); ok {
					limit = runtime.InvalidModelResponseRetryLimit()
				}
				if invalidResponseRecoveryAttempt < limit {
					invalidResponseRecoveryAttempt++
					continue
				}
			}
			if !isModelContextOverflow(err) {
				break
			}
			limit := execution.ModelContext.Policy.OverflowRetryLimit
			if _, ok := c.runtime.(ContextPreparationRuntime); !ok || overflowRecoveryAttempt >= limit {
				return c.fail(ctx, attempt, ErrContextBudgetExceeded.Error(), ErrContextBudgetExceeded)
			}
			overflowRecoveryAttempt++
		}
		if err != nil {
			return c.handleModelError(ctx, attempt, err)
		}
		decision := outcome.ModelDecision
		if err := c.runtime.CheckAuthority(ctx, attempt); err != nil {
			return c.handleRuntimeError(ctx, attempt, err)
		}
		decisionNo := prefix.AcceptedDecisions + 1
		if decision.Final != nil {
			prepared := *decision.Final
			if runtime, ok := c.runtime.(FinalPreparationTraceRuntime); ok && tracer != nil {
				prepared, err = runtime.PrepareFinalTraced(ctx, tracer, attempt, execution, prefix, prepared)
				if err != nil {
					if handled, result := c.handleRecordingError(ctx, attempt, err); handled {
						return result
					}
					return c.fail(ctx, attempt, "grounding_failed", err)
				}
			} else if runtime, ok := c.runtime.(FinalPreparationRuntime); ok {
				prepared, err = runtime.PrepareFinal(ctx, attempt, execution, prefix, prepared)
				if err != nil {
					return c.fail(ctx, attempt, "grounding_failed", err)
				}
			}
			checkpoint, err := NewFinalDraftCheckpoint(decisionNo, prepared)
			if err != nil {
				code := string(models.ErrorInvalidResponse)
				if !actionCapable {
					code = ErrorAgentBudgetExhausted
				}
				return c.fail(ctx, attempt, code, err)
			}
			if _, err := c.runtime.AppendCheckpoint(ctx, attempt, checkpoint); err != nil {
				return c.handleRuntimeError(ctx, attempt, err)
			}
			continue
		}
		if !actionCapable {
			return c.fail(ctx, attempt, ErrorAgentBudgetExhausted, errors.New("reserved Final decision proposed Actions"))
		}
		batch := *decision.Proposal
		checkpoint, err := NewProposalCheckpoint(decisionNo, batch)
		if err != nil {
			return c.fail(ctx, attempt, string(models.ErrorInvalidResponse), err)
		}
		if _, err := c.runtime.AppendCheckpoint(ctx, attempt, checkpoint); err != nil {
			return c.handleRuntimeError(ctx, attempt, err)
		}
	}
}

func (c *Controller) validateDecisionForRequest(session *MCPAttemptSession, prefix CheckpointPrefix, decision models.ModelDecision, definitions []models.ActionDefinition, actionCapable bool, execution Execution, remainingActions int, businessDecisionAvailable bool) error {
	if err := decision.Validate(); err != nil {
		return err
	}
	if decision.Proposal == nil || !actionCapable {
		return nil
	}
	batch := *decision.Proposal
	if len(batch.Actions) > execution.ActionBatchLimit {
		return fmt.Errorf("Action proposal exceeds batch limit: %d tool calls proposed, at most %d allowed per decision; propose the first %d now and the rest in later decisions", len(batch.Actions), execution.ActionBatchLimit, execution.ActionBatchLimit)
	}
	available := make(map[string]bool, len(definitions))
	names := make([]string, 0, len(definitions))
	for _, definition := range definitions {
		available[definition.Name] = true
		names = append(names, definition.Name)
	}
	for index, action := range batch.Actions {
		if !available[action.Name] {
			return fmt.Errorf("Action proposal %d names Tool %q unavailable for this decision; available Tools: %s", index, action.Name, strings.Join(names, ", "))
		}
	}
	if err := c.validateProposal(session, batch.Actions); err != nil {
		return err
	}
	for _, action := range batch.Actions {
		if action.Name == "rewrite_todo_list" && completedActionHasCanonicalInput(prefix, action.Name, action.Input) {
			return fmt.Errorf("Tool %q with identical input already completed; choose another available Tool or submit a materially different TODO plan", action.Name)
		}
	}
	businessActions, planMutations := proposalBudgetUse(batch.Actions)
	if planMutations > 1 {
		return errors.New("Action proposal contains multiple TODO mutations")
	}
	if businessActions > remainingActions {
		return fmt.Errorf("Action proposal requests %d business Actions with %d remaining Action budget", businessActions, remainingActions)
	}
	if businessActions > 0 && !businessDecisionAvailable {
		return errors.New("Action proposal requests a business Action with no remaining business decision budget")
	}
	return nil
}

func completedActionHasCanonicalInput(prefix CheckpointPrefix, name string, input []byte) bool {
	wanted, err := CanonicalJSONObject(input)
	if err != nil {
		return false
	}
	for _, proposal := range prefix.Proposals {
		for _, action := range proposal.Actions {
			if action.Name != name || action.Result == nil || action.Result.Status != ActionSucceeded {
				continue
			}
			canonical, err := CanonicalJSONObject(action.Input)
			if err == nil && string(canonical) == string(wanted) {
				return true
			}
		}
	}
	return false
}

func isPlanMutationName(name string) bool {
	return name == "rewrite_todo_list" || name == "update_todo_status"
}

func proposalBudgetUse(actions []models.ActionProposal) (businessActions, planMutations int) {
	for _, action := range actions {
		if isPlanMutationName(action.Name) {
			planMutations++
		} else {
			businessActions++
		}
	}
	return businessActions, planMutations
}

func acceptedBusinessActions(prefix CheckpointPrefix) int {
	count := 0
	for _, proposal := range prefix.Proposals {
		for _, action := range proposal.Actions {
			if !isPlanMutationName(action.Name) {
				count++
			}
		}
	}
	return count
}

func acceptedBusinessDecisions(prefix CheckpointPrefix) int {
	count := 0
	for _, proposal := range prefix.Proposals {
		for _, action := range proposal.Actions {
			if !isPlanMutationName(action.Name) {
				count++
				break
			}
		}
	}
	return count
}

func isModelContextOverflow(err error) bool {
	var modelErr *models.ModelError
	return errors.As(err, &modelErr) && modelErr.Kind == models.ErrorContextOverflow
}

func isModelInvalidResponse(err error) bool {
	var modelErr *models.ModelError
	return errors.As(err, &modelErr) && modelErr.Kind == models.ErrorInvalidResponse
}

type CrashReplayPolicy interface {
	CrashReplaySafe() bool
}

func (c *Controller) closeUnknownRecoveredActions(
	ctx context.Context,
	session *MCPAttemptSession,
	attempt Attempt,
	proposal AcceptedProposal,
	pending []AcceptedAction,
) (bool, error) {
	closed := false
	for _, action := range pending {
		if c.actionCrashReplaySafe(session, action.Name) {
			continue
		}
		result := enrichActionDomainError(ActionResult{
			Status: ActionDomainError, ErrorCode: ErrorActionInterrupted,
		})
		checkpoint, err := NewActionResultCheckpoint(proposal.DecisionNo, action.Index, action.ActionID, result)
		if err != nil {
			return false, c.fail(ctx, attempt, string(models.ErrorInvalidResponse), err)
		}
		if _, err := c.runtime.AppendCheckpoint(ctx, attempt, checkpoint); err != nil {
			return false, c.handleRuntimeError(ctx, attempt, err)
		}
		closed = true
	}
	return closed, nil
}

func (c *Controller) actionCrashReplaySafe(session *MCPAttemptSession, name string) bool {
	if session != nil {
		tool, ok := session.byName[name]
		return ok && tool.CrashReplaySafe
	}
	action, ok := c.registry.Resolve(name)
	if !ok {
		return false
	}
	policy, ok := action.(CrashReplayPolicy)
	return ok && policy.CrashReplaySafe()
}

func (c *Controller) acceptContextualizedSearch(
	ctx context.Context,
	tracer *agentobs.Tracer,
	runtime QueryContextRuntime,
	toolSession *MCPAttemptSession,
	attempt Attempt,
	execution Execution,
	prefix CheckpointPrefix,
	definition models.ActionDefinition,
) error {
	request, fallback, historyPairCount, err := runtime.BuildQueryContextRequest(ctx, execution, definition)
	if err != nil {
		return c.fail(ctx, attempt, "context_failed", err)
	}
	request.InvocationPolicy = execution.ModelInvocation
	if err := c.validateProposal(toolSession, []models.ActionProposal{fallback}); err != nil || fallback.Name != "search_evidence" {
		if err == nil {
			err = errors.New("query contextualization fallback must call search_evidence")
		}
		return c.fail(ctx, attempt, "context_failed", err)
	}
	if err := c.runtime.CheckAuthority(ctx, attempt); err != nil {
		return c.handleRuntimeError(ctx, attempt, err)
	}
	contextualizationContext := ctx
	if tracer != nil {
		contextualizationContext, _, err = tracer.StartSpan(ctx, agentobs.SpanStart{
			IdentityKey: TraceQueryContextStartIdentity(attempt.RunID, attempt.AttemptNo),
			Name:        TraceSpanQueryContextualization,
			Attributes: []agentobs.Attribute{
				agentobs.Int64(TraceKeyQueryContextHistoryPairCount, int64(historyPairCount)),
			},
		})
		if err != nil {
			recordingErr := &instrumentation.RecordingError{Phase: instrumentation.RecordingStart, Err: err}
			if _, result := c.handleRecordingError(ctx, attempt, recordingErr); result != nil {
				return result
			}
			return recordingErr
		}
	}
	decisionNo := prefix.AcceptedDecisions + 1
	var outcome models.ModelOutcome
	if tracer != nil {
		modelIdentity := TraceQueryContextModelStartIdentity(attempt.RunID, attempt.AttemptNo, decisionNo)
		outcome, err = InvokeDecisionModel(contextualizationContext, tracer, c.model, request, decisionNo, ModelTraceOptions{
			StartIdentity: modelIdentity, RequestIdentity: modelIdentity + "/replay/request",
			DecisionIdentity: modelIdentity + "/replay/decision", ReplayStager: c.replayStager(),
			Phase: ModelPhaseQueryContextualization, Role: RoleLeader, Prompt: promptTraceRef("agent.query-contextualizer", 1),
		})
	} else {
		outcome, err = c.model.Decide(ctx, request)
	}
	if handled, result := c.handleRecordingError(ctx, attempt, err); handled {
		return result
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		if tracer != nil {
			_ = tracer.EndSpan(contextualizationContext, agentobs.SpanEnd{Name: TraceSpanQueryContextualization, Status: agentobs.StatusCancelled})
		}
		return ctxErr
	}
	proposal := fallback
	fallbackUsed := true
	if err == nil {
		decision := outcome.ModelDecision
		if decision.Validate() == nil && decision.Proposal != nil && len(decision.Proposal.Actions) == 1 &&
			decision.Proposal.Actions[0].Name == "search_evidence" && c.validateProposal(toolSession, decision.Proposal.Actions) == nil {
			if preserved, preserveErr := preserveCurrentSearchQuery(decision.Proposal.Actions[0], fallback); preserveErr == nil {
				proposal = preserved
				fallbackUsed = false
			}
		}
	}
	if tracer != nil {
		if traceErr := tracer.EndSpan(contextualizationContext, agentobs.SpanEnd{
			Name: TraceSpanQueryContextualization, Status: agentobs.StatusOK,
			Attributes: []agentobs.Attribute{agentobs.Bool(TraceKeyQueryContextFallbackUsed, fallbackUsed)},
		}); traceErr != nil {
			recordingErr := &instrumentation.RecordingError{Phase: instrumentation.RecordingTerminal, Err: traceErr}
			if _, result := c.handleRecordingError(ctx, attempt, recordingErr); result != nil {
				return result
			}
			return recordingErr
		}
	}
	if err := c.runtime.CheckAuthority(ctx, attempt); err != nil {
		return c.handleRuntimeError(ctx, attempt, err)
	}
	checkpoint, err := NewProposalCheckpoint(decisionNo, models.ActionProposalBatch{Actions: []models.ActionProposal{proposal}})
	if err != nil {
		return c.fail(ctx, attempt, "context_failed", err)
	}
	if _, err := c.runtime.AppendCheckpoint(ctx, attempt, checkpoint); err != nil {
		return c.handleRuntimeError(ctx, attempt, err)
	}
	return nil
}

func actionDefinitionByName(definitions []models.ActionDefinition, name string) (models.ActionDefinition, bool) {
	for _, definition := range definitions {
		if definition.Name == name {
			return definition, true
		}
	}
	return models.ActionDefinition{}, false
}

func (c *Controller) isExclusiveDelegation(session *MCPAttemptSession, name string) bool {
	if session == nil {
		return false
	}
	tool, ok := session.byName[name]
	return ok && tool.Scheduling == agentcatalog.ToolExclusiveDelegation
}

func (c *Controller) actionExecutionError(ctx context.Context, attempt Attempt, err error) error {
	if handled, result := c.handleRecordingError(ctx, attempt, err); handled {
		return result
	}
	if errors.Is(err, ErrLeaseLost) {
		return c.handleRuntimeError(ctx, attempt, err)
	}
	if ctx.Err() != nil {
		return err
	}
	var toolErr *ToolCallError
	if errors.As(err, &toolErr) {
		return err
	}
	return c.fail(ctx, attempt, string(models.ErrorInvalidResponse), err)
}

func (c *Controller) executeDelegationAction(
	ctx context.Context,
	tracer *agentobs.Tracer,
	toolSession *MCPAttemptSession,
	attempt Attempt,
	execution Execution,
	action AcceptedAction,
) error {
	if err := c.runtime.CheckAuthority(ctx, attempt); err != nil {
		return c.handleRuntimeError(ctx, attempt, err)
	}
	if toolSession == nil {
		return c.fail(ctx, attempt, string(models.ErrorInvalidResponse), errors.New("delegation requires the MCP Tool Plane"))
	}
	executor, ok := toolSession.actionAdapter(action.Name)
	if !ok {
		return c.fail(ctx, attempt, string(models.ErrorInvalidResponse), fmt.Errorf("accepted unknown MCP Tool %q", action.Name))
	}
	request := c.actionRequest(execution, action)
	var result ActionResult
	var err error
	if tracer != nil {
		startIdentity := TraceActionStartIdentity(attempt.RunID, attempt.AttemptNo, action.ActionID)
		options := ActionTraceOptions{
			StartIdentity: startIdentity, InputIdentity: startIdentity + "/replay/input",
			ResultIdentity: startIdentity + "/replay/result", ReplayStager: c.replayStager(),
		}
		if retryRuntime, ok := c.runtime.(ActionRetryTraceRuntime); ok {
			prior, found, priorErr := retryRuntime.PreviousActionSpan(ctx, attempt, action.ActionID)
			if priorErr != nil {
				return c.handleRuntimeError(ctx, attempt, priorErr)
			}
			if found {
				options.RetryTarget = &prior
				options.LinkIdentity = options.StartIdentity + "/retries"
			}
		}
		result, err = InvokeAgentAction(ctx, tracer, executor, action.ActionID, request, options)
	} else {
		result, err = executor.Execute(ctx, request)
	}
	if err != nil {
		return c.actionExecutionError(ctx, attempt, err)
	}
	result = enrichActionDomainError(result)
	if err := result.Validate(); err != nil {
		return c.fail(ctx, attempt, string(models.ErrorInvalidResponse), err)
	}
	return nil
}

func (c *Controller) executeAction(
	ctx context.Context,
	tracer *agentobs.Tracer,
	toolSession *MCPAttemptSession,
	attempt Attempt,
	execution Execution,
	prefix CheckpointPrefix,
	proposal AcceptedProposal,
	action AcceptedAction,
) error {
	executor, err := c.resolveActionExecutor(toolSession, action.Name)
	if err != nil {
		return c.fail(ctx, attempt, string(models.ErrorInvalidResponse), err)
	}
	request := c.actionRequest(execution, action)
	options, err := c.actionTraceOptions(ctx, tracer, attempt, action)
	if err != nil {
		return c.handleRuntimeError(ctx, attempt, err)
	}
	options.PrepareResult = c.toolResultPreparer(execution, action, executor)
	result, err := c.invokeActionExecutor(ctx, tracer, executor, action, request, options)
	if err != nil {
		return c.actionExecutionError(ctx, attempt, err)
	}
	result = enrichActionDomainError(result)
	if err := result.Validate(); err != nil {
		return c.fail(ctx, attempt, string(models.ErrorInvalidResponse), err)
	}
	checkpoint, err := NewActionResultCheckpoint(proposal.DecisionNo, action.Index, action.ActionID, result)
	if err != nil {
		return c.fail(ctx, attempt, string(models.ErrorInvalidResponse), err)
	}
	usedResultBytes, err := encodedActionResultBytes(prefix)
	if err != nil {
		return c.fail(ctx, attempt, string(ErrCheckpointInvalid.Error()), err)
	}
	if len(checkpoint.Payload) > execution.ActionResultByteLimit || usedResultBytes+len(checkpoint.Payload) > execution.ActionResultsByteLimit {
		return c.fail(ctx, attempt, ErrorAgentBudgetExhausted, errors.New("Action Result byte budget exceeded"))
	}
	if _, err := c.runtime.AppendCheckpoint(ctx, attempt, checkpoint); err != nil {
		return c.handleRuntimeError(ctx, attempt, err)
	}
	return nil
}

// executeParallelBatch runs every action in pending concurrently via
// ToolBatchExecutor. Callers must only invoke this once c.allParallelEligible
// has confirmed every pending action is registered ToolParallel — a batch
// with any ordered_sync (or unscheduled) action stays on the executeAction
// one-at-a-time path unchanged.
//
// Preflight (executor resolution, request construction, retry-span lookup)
// runs sequentially before any goroutine starts, mirroring Pi's two-phase
// design: serial preflight, concurrent execution.
//
// A failing action never blocks committing its successful siblings: the
// relaxed checkpoint ordering (checkpoint_prefix.go) allows Action Results
// to land in any index order, so every successful outcome is committed and
// only the failed index is left missing for the existing attempt-retry path
// to pick up. Byte-budget accounting is still applied in index order across
// the successful outcomes, since it is a cumulative resource rather than a
// per-action one; once it would be exceeded, that action and everything
// after it in index order are left uncommitted rather than partially
// spent. If both an execution failure and budget exhaustion occur in the
// same batch, the execution failure is surfaced — it is the more specific,
// actionable condition.
func (c *Controller) executeParallelBatch(
	ctx context.Context,
	tracer *agentobs.Tracer,
	toolSession *MCPAttemptSession,
	attempt Attempt,
	execution Execution,
	prefix CheckpointPrefix,
	proposal AcceptedProposal,
	pending []AcceptedAction,
) error {
	type preparedAction struct {
		action   AcceptedAction
		executor Action
		request  ActionRequest
		options  ActionTraceOptions
	}
	prepared := make([]preparedAction, 0, len(pending))
	for _, action := range pending {
		executor, err := c.resolveActionExecutor(toolSession, action.Name)
		if err != nil {
			return c.fail(ctx, attempt, string(models.ErrorInvalidResponse), err)
		}
		request := c.actionRequest(execution, action)
		options, err := c.actionTraceOptions(ctx, tracer, attempt, action)
		if err != nil {
			return c.handleRuntimeError(ctx, attempt, err)
		}
		options.PrepareResult = c.toolResultPreparer(execution, action, executor)
		prepared = append(prepared, preparedAction{action: action, executor: executor, request: request, options: options})
	}

	tasks := make([]BatchTask, len(prepared))
	for i, item := range prepared {
		item := item
		tasks[i] = BatchTask{Index: item.action.Index, Run: func(taskCtx context.Context) (ActionResult, error) {
			return c.invokeActionExecutor(taskCtx, tracer, item.executor, item.action, item.request, item.options)
		}}
	}
	outcomes := ToolBatchExecutor{}.RunParallel(ctx, tasks)

	usedResultBytes, err := encodedActionResultBytes(prefix)
	if err != nil {
		return c.fail(ctx, attempt, string(ErrCheckpointInvalid.Error()), err)
	}
	type batchFailure struct {
		invalidResponse bool
		err             error
	}
	var failure *batchFailure
	budgetExceeded := false
	toCommit := make([]PendingCheckpoint, 0, len(outcomes))
	for i, outcome := range outcomes {
		action := prepared[i].action
		if outcome.Err != nil {
			if failure == nil {
				failure = &batchFailure{err: outcome.Err}
			}
			continue
		}
		outcome.Result = enrichActionDomainError(outcome.Result)
		if err := outcome.Result.Validate(); err != nil {
			if failure == nil {
				failure = &batchFailure{invalidResponse: true, err: err}
			}
			continue
		}
		if budgetExceeded {
			continue
		}
		checkpoint, err := NewActionResultCheckpoint(proposal.DecisionNo, action.Index, action.ActionID, outcome.Result)
		if err != nil {
			if failure == nil {
				failure = &batchFailure{invalidResponse: true, err: err}
			}
			continue
		}
		if len(checkpoint.Payload) > execution.ActionResultByteLimit || usedResultBytes+len(checkpoint.Payload) > execution.ActionResultsByteLimit {
			budgetExceeded = true
			continue
		}
		usedResultBytes += len(checkpoint.Payload)
		toCommit = append(toCommit, checkpoint)
	}
	for _, checkpoint := range toCommit {
		if _, err := c.runtime.AppendCheckpoint(ctx, attempt, checkpoint); err != nil {
			return c.handleRuntimeError(ctx, attempt, err)
		}
	}
	if failure != nil {
		if failure.invalidResponse {
			return c.fail(ctx, attempt, string(models.ErrorInvalidResponse), failure.err)
		}
		return c.actionExecutionError(ctx, attempt, failure.err)
	}
	if budgetExceeded {
		return c.fail(ctx, attempt, ErrorAgentBudgetExhausted, errors.New("Action Result byte budget exceeded"))
	}
	return nil
}

type ToolResultCacheEligibility interface {
	CacheLongToolResults(agentcatalog.Reference) bool
}

func (c *Controller) actionRequest(execution Execution, action AcceptedAction) ActionRequest {
	request := ActionRequest{
		ActionID: action.ActionID, Input: action.Input, UserID: execution.UserID, ChatID: execution.ChatID,
		DefaultTimeZone: execution.TimeZone, Attempt: execution.Attempt,
	}
	if reference, err := agentcatalog.ParseReference(execution.AgentConfigID); err == nil {
		request.Definition = reference
	}
	return request
}

func (c *Controller) externalizeToolResult(ctx context.Context, execution Execution, action AcceptedAction, executor Action, result ActionResult) ActionResult {
	if c.toolResults == nil || c.toolResultInlineByteLimit < 1 || result.Status != ActionSucceeded {
		return result
	}
	reference, err := agentcatalog.ParseReference(execution.AgentConfigID)
	if err != nil {
		return result
	}
	eligibility, ok := executor.(ToolResultCacheEligibility)
	if !ok || !eligibility.CacheLongToolResults(reference) {
		return result
	}
	projected, _ := c.toolResults.Externalize(ctx, ToolResultScope{
		UserID: execution.UserID, ChatID: execution.ChatID, RunID: execution.RunID,
		ActionID: action.ActionID, ToolName: action.Name,
	}, result, c.toolResultInlineByteLimit)
	return projected
}

func (c *Controller) toolResultPreparer(execution Execution, action AcceptedAction, executor Action) func(context.Context, ActionResult) (ActionResult, error) {
	return func(ctx context.Context, result ActionResult) (ActionResult, error) {
		return c.externalizeToolResult(ctx, execution, action, executor, result), nil
	}
}

func (c *Controller) resolveActionExecutor(session *MCPAttemptSession, name string) (Action, error) {
	if session == nil {
		executor, ok := c.registry.Resolve(name)
		if !ok {
			return nil, fmt.Errorf("accepted unknown Action %q", name)
		}
		return executor, nil
	}
	executor, ok := session.actionAdapter(name)
	if !ok {
		return nil, fmt.Errorf("accepted unknown MCP Tool %q", name)
	}
	return executor, nil
}

func (c *Controller) actionTraceOptions(ctx context.Context, tracer *agentobs.Tracer, attempt Attempt, action AcceptedAction) (ActionTraceOptions, error) {
	if tracer == nil {
		return ActionTraceOptions{}, nil
	}
	startIdentity := TraceActionStartIdentity(attempt.RunID, attempt.AttemptNo, action.ActionID)
	options := ActionTraceOptions{
		StartIdentity: startIdentity, InputIdentity: startIdentity + "/replay/input",
		ResultIdentity: startIdentity + "/replay/result", ReplayStager: c.replayStager(),
	}
	if retryRuntime, ok := c.runtime.(ActionRetryTraceRuntime); ok {
		prior, found, err := retryRuntime.PreviousActionSpan(ctx, attempt, action.ActionID)
		if err != nil {
			return ActionTraceOptions{}, err
		}
		if found {
			options.RetryTarget = &prior
			options.LinkIdentity = options.StartIdentity + "/retries"
		}
	}
	return options, nil
}

func (c *Controller) invokeActionExecutor(ctx context.Context, tracer *agentobs.Tracer, executor Action, action AcceptedAction, request ActionRequest, options ActionTraceOptions) (ActionResult, error) {
	if tracer == nil {
		result, err := executor.Execute(ctx, request)
		if err == nil && options.PrepareResult != nil {
			result, err = options.PrepareResult(ctx, result)
		}
		return result, err
	}
	return InvokeAgentAction(ctx, tracer, executor, action.ActionID, request, options)
}

func (c *Controller) allParallelEligible(session *MCPAttemptSession, actions []AcceptedAction) bool {
	if session == nil {
		return false
	}
	for _, action := range actions {
		tool, ok := session.byName[action.Name]
		if !ok || tool.Scheduling != agentcatalog.ToolParallel {
			return false
		}
	}
	return true
}

func (c *Controller) replayStager() ReplayStager {
	runtime, ok := c.runtime.(ReplayTraceRuntime)
	if !ok {
		return nil
	}
	return runtime.ReplayStager()
}

// incompleteActions returns every action in the current proposal's batch
// that has not yet completed (in index order), so callers can decide
// between running the whole batch concurrently (executeParallelBatch) or
// falling back to one-at-a-time execution (executeAction).
func incompleteActions(prefix CheckpointPrefix) (AcceptedProposal, []AcceptedAction, bool) {
	if len(prefix.Proposals) == 0 {
		return AcceptedProposal{}, nil, false
	}
	proposal := prefix.Proposals[len(prefix.Proposals)-1]
	pending := make([]AcceptedAction, 0, len(proposal.Actions))
	for _, action := range proposal.Actions {
		if action.Result == nil {
			pending = append(pending, action)
		}
	}
	if len(pending) == 0 {
		return AcceptedProposal{}, nil, false
	}
	return proposal, pending, true
}

func encodedActionResultBytes(prefix CheckpointPrefix) (int, error) {
	total := 0
	for _, proposal := range prefix.Proposals {
		for _, action := range proposal.Actions {
			if action.Result == nil {
				continue
			}
			checkpoint, err := NewActionResultCheckpoint(proposal.DecisionNo, action.Index, action.ActionID, *action.Result)
			if err != nil {
				return 0, err
			}
			total += len(checkpoint.Payload)
		}
	}
	return total, nil
}

func (c *Controller) handleModelError(ctx context.Context, attempt Attempt, err error) error {
	if handled, result := c.handleRecordingError(ctx, attempt, err); handled {
		return result
	}
	if errors.Is(context.Cause(ctx), ErrLeaseLost) || errors.Is(err, ErrLeaseLost) {
		return ErrLeaseLost
	}
	if errors.Is(err, context.Canceled) {
		return err
	}
	code := string(models.ErrorUnavailable)
	var modelErr *models.ModelError
	if errors.As(err, &modelErr) {
		code = string(modelErr.Kind)
		if modelErr.Kind == models.ErrorTimeout || modelErr.Kind == models.ErrorUnavailable {
			slog.Warn("Agent model call failed; the Attempt will retry", "run_id", attempt.RunID, "attempt", attempt.AttemptNo, "kind", modelErr.Kind, "error", err)
			return err
		}
	}
	return c.fail(ctx, attempt, code, err)
}

func (c *Controller) handleRecordingError(ctx context.Context, attempt Attempt, err error) (bool, error) {
	var recordingErr *instrumentation.RecordingError
	if !errors.As(err, &recordingErr) {
		return false, nil
	}
	if errors.Is(recordingErr, agentobs.ErrLifecycle) || errors.Is(recordingErr, agentobs.ErrLimitExceeded) || errors.Is(recordingErr, agentobs.ErrIdentityConflict) || errors.Is(recordingErr, agentobs.ErrUnresolvedLink) {
		return true, c.fail(ctx, attempt, ErrorAgentTraceInvalid, recordingErr)
	}
	return true, err
}

func (c *Controller) handleRuntimeError(ctx context.Context, attempt Attempt, err error) error {
	if errors.Is(err, ErrLeaseLost) || errors.Is(err, ErrRunDeadlineExceeded) || errors.Is(err, context.Canceled) {
		return err
	}
	if errors.Is(err, ErrCheckpointInvalid) {
		return c.fail(ctx, attempt, ErrCheckpointInvalid.Error(), err)
	}
	return err
}

func (c *Controller) fail(ctx context.Context, attempt Attempt, code string, cause error) error {
	failCtx, cancel := terminalContext(ctx)
	defer cancel()
	if err := c.runtime.Fail(failCtx, attempt, code); err != nil {
		return errors.Join(cause, err)
	}
	return cause
}
