package app_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/huangxinxinyu/nano-notebook/internal/agent"
	"github.com/huangxinxinyu/nano-notebook/internal/agentcatalog"
	"github.com/huangxinxinyu/nano-notebook/internal/models"
	"github.com/huangxinxinyu/nano-notebook/internal/promptcatalog"
)

// From research.executor@45 the system prompt and the tool list are the same
// on every decision of a Run; a repeat pause appears in the trailing runtime
// notice, and the ledger is a user message.
func TestResearchV45KeepsSystemAndToolsFixedAcrossARepeatPause(t *testing.T) {
	api := newTestAPI(t)
	ctx := context.Background()
	root, _, _, _ := admitResearchExecutionForRelease(t, api, "prefix-stability@example.com", "nano.default@58")
	runtime, err := agent.NewResearchRuntime(api.db.Pool(), promptcatalog.MustLoadEmbedded())
	if err != nil {
		t.Fatal(err)
	}
	attempt := attemptFromClaim(root)
	execution, err := runtime.Load(ctx, attempt)
	if err != nil {
		t.Fatal(err)
	}
	if execution.AgentConfigID != "research.executor@45" {
		t.Fatalf("definition=%s", execution.AgentConfigID)
	}
	definitions := []models.ActionDefinition{
		{Name: "web_search", Description: "search", InputSchema: json.RawMessage(`{"type":"object"}`)},
		{Name: "read_url", Description: "read", InputSchema: json.RawMessage(`{"type":"object"}`)},
		{Name: "read_skill", Description: "skill", InputSchema: json.RawMessage(`{"type":"object"}`)},
	}
	build := func() models.ModelRequest {
		t.Helper()
		prefix, err := runtime.LoadCheckpointPrefix(ctx, attempt)
		if err != nil {
			t.Fatal(err)
		}
		request, err := runtime.BuildDecisionRequest(ctx, execution, prefix, definitions)
		if err != nil {
			t.Fatal(err)
		}
		return request
	}
	search := json.RawMessage(`{"queries":["agentic rag evaluation"]}`)
	appendResearchProposal(t, runtime, attempt, 1, []models.ActionProposal{{Name: "web_search", Input: search}})
	output, _ := json.Marshal(map[string]any{"results": []any{}})
	appendResearchResult(t, runtime, attempt, 1, 0, agent.ActionResult{Status: agent.ActionSucceeded, Output: output})
	before := build()

	for decision := 2; decision <= 3; decision++ {
		appendResearchProposal(t, runtime, attempt, decision, []models.ActionProposal{{Name: "web_search", Input: search}})
		appendResearchResult(t, runtime, attempt, decision, 0, agent.ActionResult{Status: agent.ActionDomainError, ErrorCode: "research_duplicate_action"})
	}
	paused := build()

	if before.Messages[0].Role != models.RoleSystem || paused.Messages[0].Content != before.Messages[0].Content {
		t.Fatalf("system prompt changed across the pause:\n%s\n----\n%s", before.Messages[0].Content, paused.Messages[0].Content)
	}
	if len(paused.ActionDefinitions) != len(definitions) {
		t.Fatalf("tool list narrowed to %d tools", len(paused.ActionDefinitions))
	}
	var notice, ledgerRole string
	for _, message := range paused.Messages {
		if strings.HasPrefix(message.Content, "<runtime_notices>") {
			notice = message.Content
		}
		if strings.Contains(message.Content, "Evidence Ledger:") {
			ledgerRole = string(message.Role)
		}
	}
	if !strings.Contains(notice, "Repeat circuit breaker: web_search is paused") || !strings.Contains(notice, "research_tool_paused") ||
		strings.Contains(notice, "Exact recent calls were removed") {
		t.Fatalf("notice=%q", notice)
	}
	if ledgerRole != string(models.RoleUser) {
		t.Fatalf("ledger role=%q", ledgerRole)
	}

	// The paused call in decision 4 returns a domain error without running;
	// a call in a decision that is not paused runs.
	ran := 0
	guarded := agent.NewResearchDeduplicatingAction(api.db.Pool(), countingAction{name: "web_search", ran: &ran})
	fresh := json.RawMessage(`{"queries":["a genuinely new query"]}`)
	appendResearchProposal(t, runtime, attempt, 4, []models.ActionProposal{{Name: "web_search", Input: fresh}})
	result, err := guarded.Execute(ctx, agent.ActionRequest{
		ActionID: "decision:4/action:0", Input: fresh, Attempt: attempt,
		Definition: agentcatalog.MustParseReference("research.executor@45"),
	})
	if err != nil || result.Status != agent.ActionDomainError || result.Error == nil || result.Error.Code != "research_tool_paused" || ran != 0 {
		t.Fatalf("paused result=%+v err=%v ran=%d", result, err, ran)
	}
	appendResearchResult(t, runtime, attempt, 4, 0, result)
	// A paused call keeps the repeat run going, so decision 5 is paused too.
	if again := build(); !strings.Contains(again.Messages[len(again.Messages)-2].Content+again.Messages[len(again.Messages)-1].Content, "web_search is paused") {
		t.Fatal("the pause lifted after a paused call")
	}
	legacy, err := guarded.Execute(ctx, agent.ActionRequest{
		ActionID: "decision:4/action:0", Input: fresh, Attempt: attempt,
		Definition: agentcatalog.MustParseReference("research.executor@44"),
	})
	if err != nil || legacy.Status != agent.ActionSucceeded || ran != 1 {
		t.Fatalf("v44 call result=%+v err=%v ran=%d", legacy, err, ran)
	}
}

type countingAction struct {
	name string
	ran  *int
}

func (a countingAction) Definition() models.ActionDefinition {
	return models.ActionDefinition{Name: a.name, InputSchema: json.RawMessage(`{"type":"object"}`)}
}

func (countingAction) ValidateInput(json.RawMessage) error { return nil }

func (a countingAction) Execute(context.Context, agent.ActionRequest) (agent.ActionResult, error) {
	*a.ran++
	return agent.ActionResult{Status: agent.ActionSucceeded, Output: json.RawMessage(`{"results":[]}`)}, nil
}
