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

func (c *recordingModelClient) Decide(_ context.Context, request models.ModelRequest) (models.ModelOutcome, error) {
	c.calls++
	c.request = request
	resultKind := models.ModelResultFinalDraft
	if c.result.Proposal != nil {
		resultKind = models.ModelResultActionProposal
	}
	return models.ModelOutcome{ModelDecision: c.result, Metadata: models.ModelCallMetadata{
		RequestedModel: request.Model, ResultKind: resultKind,
	}}, c.err
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
