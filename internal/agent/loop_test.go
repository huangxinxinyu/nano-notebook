package agent

import (
	"context"
	"reflect"
	"testing"

	"github.com/huangxinxinyu/nano-notebook/internal/models"
)

func TestLoopExecutesOneFixedModelPassAndPublishes(t *testing.T) {
	steps := make([]string, 0, 4)
	loader := loaderFunc(func(_ context.Context, runID string) (Execution, error) {
		steps = append(steps, "load")
		return Execution{RunID: runID, Model: "aliyun/qwen-flash"}, nil
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
	if err := loop.Execute(context.Background(), "run_one"); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(steps, []string{"load", "context", "model", "publish"}) {
		t.Fatalf("steps = %v, want one fixed pass", steps)
	}
	if publisher.runID != "run_one" || publisher.result.Text != "hello back" {
		t.Fatalf("unexpected publication: %+v", publisher)
	}
}

type loaderFunc func(context.Context, string) (Execution, error)

func (fn loaderFunc) Load(ctx context.Context, runID string) (Execution, error) {
	return fn(ctx, runID)
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
	steps  *[]string
	runID  string
	result models.ChatResult
}

func (p *recordingPublisher) Publish(_ context.Context, runID string, result models.ChatResult) error {
	*p.steps = append(*p.steps, "publish")
	p.runID = runID
	p.result = result
	return nil
}

func (p *recordingPublisher) Fail(context.Context, string, string) error {
	return nil
}
