package agent

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"

	"github.com/huangxinxinyu/nano-notebook/internal/models"
)

func TestLoopExecutesOneFixedModelPassAndPublishes(t *testing.T) {
	steps := make([]string, 0, 4)
	loader := loaderFunc(func(_ context.Context, attempt Attempt) (Execution, error) {
		steps = append(steps, "load")
		return Execution{Attempt: attempt, Model: "aliyun/qwen-flash"}, nil
	})
	builder := builderFunc(func(_ context.Context, execution Execution) (models.ChatRequest, error) {
		steps = append(steps, "context")
		return models.ChatRequest{Model: execution.Model, Messages: []models.ChatMessage{{Role: "user", Content: "hello"}}}, nil
	})
	runner := runnerFunc(func(_ context.Context, request models.ChatRequest) (models.ChatResult, error) {
		steps = append(steps, "model")
		if request.Model != "aliyun/qwen-flash" || len(request.Messages) != 1 {
			t.Fatalf("unexpected model request: %+v", request)
		}
		return models.ChatResult{Text: "hello back", FinishReason: "stop"}, nil
	})
	publisher := &recordingPublisher{steps: &steps}

	loop := NewLoop(loader, builder, runner, publisher)
	attempt := Attempt{JobID: "job_one", RunID: "run_one", LeaseToken: "lease_one"}
	if err := loop.Execute(context.Background(), attempt); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(steps, []string{"load", "context", "model", "publish"}) {
		t.Fatalf("steps = %v, want one fixed pass", steps)
	}
	if publisher.runID != "run_one" || publisher.result.Text != "hello back" {
		t.Fatalf("unexpected publication: %+v", publisher)
	}
}

func TestLoopTreatsLeaseLossDuringModelCallAsControlFlow(t *testing.T) {
	ctx, cancel := context.WithCancelCause(context.Background())
	cancel(ErrLeaseLost)
	publisher := &recordingPublisher{}
	loop := NewLoop(
		loaderFunc(func(context.Context, Attempt) (Execution, error) {
			return Execution{Model: "aliyun/qwen-flash"}, nil
		}),
		builderFunc(func(context.Context, Execution) (models.ChatRequest, error) {
			return models.ChatRequest{Model: "aliyun/qwen-flash"}, nil
		}),
		runnerFunc(func(context.Context, models.ChatRequest) (models.ChatResult, error) {
			return models.ChatResult{}, &models.ModelError{
				Kind: models.ErrorUnavailable,
				Err:  fmt.Errorf("Post model endpoint: %w", ErrLeaseLost),
			}
		}),
		publisher,
	)

	err := loop.Execute(ctx, Attempt{JobID: "job_one", RunID: "run_one", LeaseToken: "lease_one"})
	if err != ErrLeaseLost {
		t.Fatalf("error = %v, want canonical ErrLeaseLost", err)
	}
	if publisher.failCalls != 0 {
		t.Fatalf("failure publications = %d, want none", publisher.failCalls)
	}
	if !errors.Is(context.Cause(ctx), ErrLeaseLost) {
		t.Fatalf("context cause = %v, want ErrLeaseLost", context.Cause(ctx))
	}
}

type loaderFunc func(context.Context, Attempt) (Execution, error)

func (fn loaderFunc) Load(ctx context.Context, attempt Attempt) (Execution, error) {
	return fn(ctx, attempt)
}

type builderFunc func(context.Context, Execution) (models.ChatRequest, error)

func (fn builderFunc) Build(ctx context.Context, execution Execution) (models.ChatRequest, error) {
	return fn(ctx, execution)
}

type runnerFunc func(context.Context, models.ChatRequest) (models.ChatResult, error)

func (fn runnerFunc) Run(ctx context.Context, request models.ChatRequest) (models.ChatResult, error) {
	return fn(ctx, request)
}

type recordingPublisher struct {
	steps     *[]string
	runID     string
	result    models.ChatResult
	failCalls int
}

func (p *recordingPublisher) Publish(_ context.Context, attempt Attempt, result models.ChatResult) error {
	if p.steps != nil {
		*p.steps = append(*p.steps, "publish")
	}
	p.runID = attempt.RunID
	p.result = result
	return nil
}

func (p *recordingPublisher) Fail(context.Context, Attempt, string) error {
	p.failCalls++
	return nil
}
