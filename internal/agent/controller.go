package agent

import (
	"context"
	"errors"
	"fmt"

	"github.com/huangxinxinyu/nano-notebook/internal/agentobs"
	"github.com/huangxinxinyu/nano-notebook/internal/agentobs/instrumentation"
	"github.com/huangxinxinyu/nano-notebook/internal/models"
)

const (
	ErrorAgentBudgetExhausted = "agent_budget_exhausted"
	ErrorAgentTraceInvalid    = "agent_trace_invalid"
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

type FinalPreparationRuntime interface {
	PrepareFinal(context.Context, Attempt, Execution, CheckpointPrefix, models.FinalDraft) (models.FinalDraft, error)
}

type Controller struct {
	runtime  ControllerRuntime
	model    DecisionModel
	registry *ActionRegistry
}

var _ ControllerRuntime = (*PostgresRuntime)(nil)
var _ DecisionModel = (*models.BifrostClient)(nil)

func NewController(runtime ControllerRuntime, model DecisionModel, registry *ActionRegistry) *Controller {
	return &Controller{runtime: runtime, model: model, registry: registry}
}

func (c *Controller) Execute(ctx context.Context, attempt Attempt) error {
	if c.runtime == nil || c.model == nil || c.registry == nil {
		return errors.New("Agent Controller dependencies are incomplete")
	}
	execution, err := c.runtime.Load(ctx, attempt)
	if err != nil {
		return err
	}
	var tracer *agentobs.Tracer
	if traceRuntime, ok := c.runtime.(AttemptTraceRuntime); ok {
		ctx, tracer, err = traceRuntime.StartAttemptTrace(ctx, attempt)
		if err != nil {
			return err
		}
	}
	forceFinalDecision := false
	for {
		prefix, err := c.runtime.LoadCheckpointPrefix(ctx, attempt)
		if err != nil {
			return c.handleRuntimeError(ctx, attempt, err)
		}
		if prefix.Final != nil {
			return c.runtime.PublishFinal(ctx, attempt, *prefix.Final)
		}
		if proposal, action, ok := firstIncompleteAction(prefix); ok {
			if err := c.executeAction(ctx, tracer, attempt, execution, prefix, proposal, action); err != nil {
				return err
			}
			continue
		}

		remainingActions := execution.ActionLimit - prefix.AcceptedActions
		actionCapable := !forceFinalDecision && len(prefix.Proposals) < execution.ActionDecisionLimit && remainingActions > 0
		definitions := []models.ActionDefinition(nil)
		if actionCapable {
			definitions = c.registry.Definitions(ActionPolicy{RemainingActions: remainingActions, Execution: &execution})
			if len(definitions) == 0 {
				actionCapable = false
			}
		}
		if !actionCapable && execution.FinalDecisionLimit < 1 {
			return c.fail(ctx, attempt, ErrorAgentBudgetExhausted, errors.New("no reserved Final decision is available"))
		}
		request, err := c.runtime.BuildDecisionRequest(ctx, execution, prefix, definitions)
		if err != nil {
			return c.fail(ctx, attempt, "context_failed", err)
		}
		if err := c.runtime.CheckAuthority(ctx, attempt); err != nil {
			return c.handleRuntimeError(ctx, attempt, err)
		}
		var outcome models.ModelOutcome
		if tracer != nil {
			decisionNo := prefix.AcceptedDecisions + 1
			modelIdentity := TraceModelStartIdentity(attempt.RunID, attempt.AttemptNo, decisionNo)
			outcome, err = InvokeDecisionModel(ctx, tracer, c.model, request, decisionNo, ModelTraceOptions{
				StartIdentity: modelIdentity, RequestIdentity: modelIdentity + "/replay/request",
				DecisionIdentity: modelIdentity + "/replay/decision", ReplayStager: c.replayStager(),
			})
		} else {
			outcome, err = c.model.Decide(ctx, request)
		}
		if err != nil {
			return c.handleModelError(ctx, attempt, err)
		}
		decision := outcome.ModelDecision
		if err := decision.Validate(); err != nil {
			code := string(models.ErrorInvalidResponse)
			if !actionCapable {
				code = ErrorAgentBudgetExhausted
			}
			return c.fail(ctx, attempt, code, err)
		}
		if err := c.runtime.CheckAuthority(ctx, attempt); err != nil {
			return c.handleRuntimeError(ctx, attempt, err)
		}
		decisionNo := prefix.AcceptedDecisions + 1
		if decision.Final != nil {
			prepared := *decision.Final
			if runtime, ok := c.runtime.(FinalPreparationRuntime); ok {
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
			forceFinalDecision = false
			continue
		}
		if !actionCapable {
			return c.fail(ctx, attempt, ErrorAgentBudgetExhausted, errors.New("reserved Final decision proposed Actions"))
		}
		batch := *decision.Proposal
		if len(batch.Actions) > execution.ActionBatchLimit {
			return c.fail(ctx, attempt, string(models.ErrorInvalidResponse), errors.New("Action proposal exceeds batch limit"))
		}
		if err := c.registry.ValidateProposal(batch.Actions); err != nil {
			return c.fail(ctx, attempt, string(models.ErrorInvalidResponse), err)
		}
		if len(batch.Actions) > remainingActions {
			forceFinalDecision = true
			continue
		}
		checkpoint, err := NewProposalCheckpoint(decisionNo, batch)
		if err != nil {
			return c.fail(ctx, attempt, string(models.ErrorInvalidResponse), err)
		}
		if _, err := c.runtime.AppendCheckpoint(ctx, attempt, checkpoint); err != nil {
			return c.handleRuntimeError(ctx, attempt, err)
		}
		forceFinalDecision = false
	}
}

func (c *Controller) executeAction(
	ctx context.Context,
	tracer *agentobs.Tracer,
	attempt Attempt,
	execution Execution,
	prefix CheckpointPrefix,
	proposal AcceptedProposal,
	action AcceptedAction,
) error {
	if err := c.runtime.CheckAuthority(ctx, attempt); err != nil {
		return c.handleRuntimeError(ctx, attempt, err)
	}
	executor, ok := c.registry.Resolve(action.Name)
	if !ok {
		return c.fail(ctx, attempt, string(models.ErrorInvalidResponse), fmt.Errorf("accepted unknown Action %q", action.Name))
	}
	request := ActionRequest{Input: action.Input, DefaultTimeZone: execution.TimeZone, Attempt: attempt}
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
		if handled, result := c.handleRecordingError(ctx, attempt, err); handled {
			return result
		}
		if ctx.Err() != nil {
			return err
		}
		return c.fail(ctx, attempt, string(models.ErrorInvalidResponse), err)
	}
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
	if err := c.runtime.CheckAuthority(ctx, attempt); err != nil {
		return c.handleRuntimeError(ctx, attempt, err)
	}
	if _, err := c.runtime.AppendCheckpoint(ctx, attempt, checkpoint); err != nil {
		return c.handleRuntimeError(ctx, attempt, err)
	}
	return nil
}

func (c *Controller) replayStager() ReplayStager {
	runtime, ok := c.runtime.(ReplayTraceRuntime)
	if !ok {
		return nil
	}
	return runtime.ReplayStager()
}

func firstIncompleteAction(prefix CheckpointPrefix) (AcceptedProposal, AcceptedAction, bool) {
	if len(prefix.Proposals) == 0 {
		return AcceptedProposal{}, AcceptedAction{}, false
	}
	proposal := prefix.Proposals[len(prefix.Proposals)-1]
	for _, action := range proposal.Actions {
		if action.Result == nil {
			return proposal, action, true
		}
	}
	return AcceptedProposal{}, AcceptedAction{}, false
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
