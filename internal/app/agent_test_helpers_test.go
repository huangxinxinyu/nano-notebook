package app_test

import (
	"context"
	"testing"

	"github.com/huangxinxinyu/nano-notebook/internal/agent"
	"github.com/huangxinxinyu/nano-notebook/internal/models"
)

type recordingModelClient struct {
	calls   int
	request models.ModelRequest
	result  models.ModelDecision
	err     error
}

func (c *recordingModelClient) Decide(_ context.Context, request models.ModelRequest) (models.ModelDecision, error) {
	c.calls++
	c.request = request
	return c.result, c.err
}

func appendFinalDraft(t *testing.T, runtime *agent.PostgresRuntime, attempt agent.Attempt, text string) models.FinalDraft {
	t.Helper()
	draft := models.FinalDraft{Text: text}
	checkpoint, err := agent.NewFinalDraftCheckpoint(1, draft)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.AppendCheckpoint(context.Background(), attempt, checkpoint); err != nil {
		t.Fatal(err)
	}
	return draft
}
