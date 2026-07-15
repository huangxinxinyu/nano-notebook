package agent

import (
	"context"
	"errors"
	"time"

	"github.com/huangxinxinyu/nano-notebook/internal/models"
)

type Execution struct {
	RunID  string
	ChatID string
	UserID string
	Model  string
}

type Loader interface {
	Load(context.Context, string) (Execution, error)
}

type ContextBuilder interface {
	Build(context.Context, Execution) (models.ChatRequest, error)
}

type Runner interface {
	Run(context.Context, models.ChatRequest) (models.ChatResult, error)
}

type Publisher interface {
	Publish(context.Context, string, models.ChatResult) error
	Fail(context.Context, string, string) error
}

type Loop struct {
	loader    Loader
	builder   ContextBuilder
	runner    Runner
	publisher Publisher
}

func NewLoop(loader Loader, builder ContextBuilder, runner Runner, publisher Publisher) *Loop {
	return &Loop{loader: loader, builder: builder, runner: runner, publisher: publisher}
}

func (l *Loop) Execute(ctx context.Context, runID string) error {
	execution, err := l.loader.Load(ctx, runID)
	if err != nil {
		return err
	}
	request, err := l.builder.Build(ctx, execution)
	if err != nil {
		failCtx, cancel := terminalContext(ctx)
		defer cancel()
		if failErr := l.publisher.Fail(failCtx, runID, "context_failed"); failErr != nil {
			return errors.Join(err, failErr)
		}
		return err
	}
	result, err := l.runner.Run(ctx, request)
	if err != nil {
		code := string(models.ErrorUnavailable)
		var modelErr *models.ModelError
		if errors.As(err, &modelErr) {
			code = string(modelErr.Kind)
		}
		failCtx, cancel := terminalContext(ctx)
		defer cancel()
		if failErr := l.publisher.Fail(failCtx, runID, code); failErr != nil {
			return errors.Join(err, failErr)
		}
		return err
	}
	return l.publisher.Publish(ctx, runID, result)
}

func terminalContext(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
}

type ModelRunner struct {
	client models.ModelClient
}

func NewModelRunner(client models.ModelClient) *ModelRunner {
	return &ModelRunner{client: client}
}

func (r *ModelRunner) Run(ctx context.Context, request models.ChatRequest) (models.ChatResult, error) {
	return r.client.Complete(ctx, request)
}
