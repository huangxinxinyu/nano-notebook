package exampleagent

import (
	"context"
	"errors"
	"fmt"

	"github.com/huangxinxinyu/nano-notebook/internal/agentobs"
	"github.com/huangxinxinyu/nano-notebook/internal/agentobs/instrumentation"
	"github.com/huangxinxinyu/nano-notebook/internal/agentobs/semconv"
)

type ResultKind string

const (
	ResultAction ResultKind = "action"
	ResultOutput ResultKind = "output"
)

type ModelMetadata struct {
	Provider     string
	Model        string
	InputTokens  int64
	OutputTokens int64
}

type ModelResult struct {
	Kind       ResultKind
	ActionName string
	Output     string
	Metadata   ModelMetadata
}

type Model interface {
	Decide(context.Context, string) (ModelResult, error)
}

type Action interface {
	Execute(context.Context, string) (string, error)
}

type Agent struct {
	tracer *agentobs.Tracer
	model  Model
	action Action
}

func New(tracer *agentobs.Tracer, model Model, action Action) (*Agent, error) {
	if tracer == nil || model == nil || action == nil {
		return nil, errors.New("example Agent dependencies are incomplete")
	}
	return &Agent{tracer: tracer, model: model, action: action}, nil
}

func (a *Agent) Run(ctx context.Context, input string) (string, error) {
	rootContext, _, err := a.tracer.StartTrace(ctx, agentobs.TraceStart{
		Name: semconv.AgentExecution,
		Attributes: []agentobs.Attribute{
			agentobs.String(semconv.InstrumentationScopeKey, "agentobs.exampleagent"),
			agentobs.String(semconv.InstrumentationVersionKey, "v0"),
		},
	})
	if err != nil {
		return "", err
	}

	first, err := a.callModel(rootContext, input, 1)
	if err != nil {
		return "", a.endRoot(rootContext, err)
	}
	if first.Kind != ResultAction || first.ActionName == "" {
		return "", a.endRoot(rootContext, errors.New("first model decision is not an Action"))
	}
	actionResult, err := a.callAction(rootContext, first.ActionName)
	if err != nil {
		return "", a.endRoot(rootContext, err)
	}
	second, err := a.callModel(rootContext, actionResult, 2)
	if err != nil {
		return "", a.endRoot(rootContext, err)
	}
	if second.Kind != ResultOutput {
		return "", a.endRoot(rootContext, errors.New("second model decision is not an output"))
	}
	if err := a.tracer.EndSpan(rootContext, agentobs.SpanEnd{Name: semconv.AgentExecution, Status: agentobs.StatusOK}); err != nil {
		return second.Output, err
	}
	return second.Output, nil
}

func (a *Agent) callModel(ctx context.Context, input string, ordinal int64) (ModelResult, error) {
	return instrumentation.Invoke(ctx, a.tracer,
		agentobs.SpanStart{
			Name: semconv.ModelCall,
			Attributes: []agentobs.Attribute{
				agentobs.String(semconv.OperationNameKey, "decide"),
				agentobs.Int64(semconv.DecisionOrdinalKey, ordinal),
			},
		},
		func(callContext context.Context) (ModelResult, error) {
			return a.model.Decide(callContext, input)
		},
		func(result ModelResult, _ error) agentobs.SpanEnd {
			attributes := []agentobs.Attribute{
				agentobs.String(semconv.ModelResultKindKey, string(result.Kind)),
			}
			if result.Metadata.Provider != "" {
				attributes = append(attributes, agentobs.String(semconv.ModelProviderKey, result.Metadata.Provider))
			}
			if result.Metadata.Model != "" {
				attributes = append(attributes, agentobs.String(semconv.ModelNameKey, result.Metadata.Model))
			}
			if result.Metadata.InputTokens > 0 {
				attributes = append(attributes, agentobs.Int64(semconv.TokenInputKey, result.Metadata.InputTokens))
			}
			if result.Metadata.OutputTokens > 0 {
				attributes = append(attributes, agentobs.Int64(semconv.TokenOutputKey, result.Metadata.OutputTokens))
			}
			if total := result.Metadata.InputTokens + result.Metadata.OutputTokens; total > 0 {
				attributes = append(attributes, agentobs.Int64(semconv.TokenTotalKey, total))
			}
			return agentobs.SpanEnd{Name: semconv.ModelCall, Attributes: attributes}
		},
	)
}

func (a *Agent) callAction(ctx context.Context, name string) (string, error) {
	return instrumentation.Invoke(ctx, a.tracer,
		agentobs.SpanStart{
			Name:       semconv.AgentAction,
			Attributes: []agentobs.Attribute{agentobs.String(semconv.OperationNameKey, name)},
		},
		func(callContext context.Context) (string, error) {
			return a.action.Execute(callContext, name)
		},
		func(_ string, _ error) agentobs.SpanEnd {
			return agentobs.SpanEnd{
				Name:       semconv.AgentAction,
				Attributes: []agentobs.Attribute{agentobs.String(semconv.OperationNameKey, name)},
			}
		},
	)
}

func (a *Agent) endRoot(ctx context.Context, runErr error) error {
	status := agentobs.StatusError
	if errors.Is(runErr, context.Canceled) {
		status = agentobs.StatusCancelled
	}
	endErr := a.tracer.EndSpan(ctx, agentobs.SpanEnd{
		Name:       semconv.AgentExecution,
		Status:     status,
		Attributes: []agentobs.Attribute{agentobs.String(semconv.ErrorKindKey, safeErrorKind(runErr))},
	})
	return errors.Join(runErr, endErr)
}

func safeErrorKind(err error) string {
	switch {
	case errors.Is(err, context.Canceled):
		return "cancelled"
	case errors.Is(err, context.DeadlineExceeded):
		return "deadline_exceeded"
	default:
		return fmt.Sprintf("%T", err)
	}
}
