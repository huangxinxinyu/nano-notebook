package app_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/huangxinxinyu/nano-notebook/internal/agent"
	"github.com/huangxinxinyu/nano-notebook/internal/agentcatalog"
	"github.com/huangxinxinyu/nano-notebook/internal/models"
	"github.com/huangxinxinyu/nano-notebook/internal/promptcatalog"
)

func TestResearchTreeSharesOneInputTokenBudget(t *testing.T) {
	api := newTestAPI(t)
	ctx := context.Background()
	parent, _, _, _ := admitResearchExecutionForRelease(t, api, "token-budget@example.com", "nano.default@48")
	runtime, err := agent.NewResearchRuntime(api.db.Pool(), promptcatalog.MustLoadEmbedded())
	if err != nil {
		t.Fatal(err)
	}
	attempt := attemptFromClaim(parent)
	execution, err := runtime.Load(ctx, attempt)
	if err != nil {
		t.Fatal(err)
	}
	definition, _ := agentcatalog.MustLoadEmbedded().ResolveDefinition(agentcatalog.MustParseReference("research.executor@35"))
	f := runtimeSubagentFixture{api: api, parent: parent, runtime: runtime, execution: execution, definition: definition, tools: map[string]agent.Action{}, sink: &capturingDirectTraceSink{}}
	for _, registration := range agent.NewRuntimeSubagentToolRegistrations(api.db.Pool()) {
		f.tools[registration.Action.Definition().Name] = registration.Action
	}
	input := json.RawMessage(`{"message":"Independent check","task_name":"check"}`)
	appendResearchProposal(t, runtime, attempt, 1, []models.ActionProposal{{Name: "spawn_agent", Input: input}})
	spawned, err := f.tools["spawn_agent"].Execute(ctx, f.request("spawn_agent", "decision:1/action:0", input, attempt))
	if err != nil || spawned.Status != agent.ActionSucceeded {
		t.Fatalf("spawn=%+v err=%v", spawned, err)
	}
	var child struct {
		AgentID string `json:"agent_id"`
	}
	_ = json.Unmarshal(spawned.Output, &child)
	childAttempt := agent.Attempt{RunID: child.AgentID}

	usage := func(input, cached, output int64) models.ModelCallMetadata {
		return models.ModelCallMetadata{InputTokens: &input, CachedTokens: &cached, OutputTokens: &output}
	}
	if err := runtime.RecordModelUsage(ctx, attempt, usage(5_000_000, 4_000_000, 20_000)); err != nil {
		t.Fatal(err)
	}
	if exhausted, err := runtime.ModelTokenBudgetExhausted(ctx, childAttempt); err != nil || exhausted {
		t.Fatalf("exhausted early=%v err=%v", exhausted, err)
	}
	if err := runtime.RecordModelUsage(ctx, childAttempt, usage(1_000_000, 900_000, 5_000)); err != nil {
		t.Fatal(err)
	}
	for _, run := range []agent.Attempt{attempt, childAttempt} {
		if exhausted, err := runtime.ModelTokenBudgetExhausted(ctx, run); err != nil || !exhausted {
			t.Fatalf("run %s exhausted=%v err=%v", run.RunID, exhausted, err)
		}
	}
	var input64, cached, output int64
	if err := api.db.Pool().QueryRow(ctx, `select input_tokens_consumed,cached_input_tokens_consumed,output_tokens_consumed from agent_trees tree
		join agent_runs run on run.tree_id=tree.id where run.id=$1`, parent.RunID).Scan(&input64, &cached, &output); err != nil {
		t.Fatal(err)
	}
	if input64 != 6_000_000 || cached != 4_900_000 || output != 25_000 {
		t.Fatalf("tree usage input=%d cached=%d output=%d", input64, cached, output)
	}
}

func TestResearchTreeWithoutTokenLimitIsNeverExhausted(t *testing.T) {
	api := newTestAPI(t)
	ctx := context.Background()
	parent, _, _, _ := admitResearchExecutionForRelease(t, api, "no-token-budget@example.com", "nano.default@47")
	runtime, err := agent.NewResearchRuntime(api.db.Pool(), promptcatalog.MustLoadEmbedded())
	if err != nil {
		t.Fatal(err)
	}
	tokens := int64(50_000_000)
	if err := runtime.RecordModelUsage(ctx, attemptFromClaim(parent), models.ModelCallMetadata{InputTokens: &tokens}); err != nil {
		t.Fatal(err)
	}
	if exhausted, err := runtime.ModelTokenBudgetExhausted(ctx, attemptFromClaim(parent)); err != nil || exhausted {
		t.Fatalf("exhausted=%v err=%v", exhausted, err)
	}
}
