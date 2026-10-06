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

func TestResearchReaderRequestSendsOnlyItsLatestPage(t *testing.T) {
	api := newTestAPI(t)
	ctx := context.Background()
	parent, _, _, _ := admitResearchExecutionForRelease(t, api, "reader-context@example.com", "nano.default@49")
	runtime, err := agent.NewResearchRuntime(api.db.Pool(), promptcatalog.MustLoadEmbedded())
	if err != nil {
		t.Fatal(err)
	}
	execution, err := runtime.Load(ctx, attemptFromClaim(parent))
	if err != nil {
		t.Fatal(err)
	}
	definition, _ := agentcatalog.MustLoadEmbedded().ResolveDefinition(agentcatalog.MustParseReference("research.executor@36"))
	f := runtimeSubagentFixture{api: api, parent: parent, runtime: runtime, execution: execution, definition: definition, tools: map[string]agent.Action{}, sink: &capturingDirectTraceSink{}}
	for _, registration := range agent.NewRuntimeSubagentToolRegistrations(api.db.Pool()) {
		f.tools[registration.Action.Definition().Name] = registration.Action
	}
	input, _ := json.Marshal(map[string]string{"message": "Read one long document for the parent researcher, in full: https://arxiv.org/abs/2510.22344 (FAIR-RAG).\n\nRecord cards.", "task_name": "Read: FAIR-RAG"})
	appendResearchProposal(t, runtime, attemptFromClaim(parent), 1, []models.ActionProposal{{Name: "spawn_agent", Input: input}})
	spawned, err := f.tools["spawn_agent"].Execute(ctx, f.request("spawn_agent", "decision:1/action:0", input, attemptFromClaim(parent)))
	if err != nil || spawned.Status != agent.ActionSucceeded {
		t.Fatalf("spawn=%+v err=%v", spawned, err)
	}
	var reader struct {
		AgentID string `json:"agent_id"`
	}
	_ = json.Unmarshal(spawned.Output, &reader)
	child := attemptFromClaim(f.claimChild(t, reader.AgentID))
	childExecution, err := runtime.Load(ctx, child)
	if err != nil {
		t.Fatal(err)
	}
	pages := []string{strings.Repeat("FIRSTPAGE evidence ", 400), strings.Repeat("SECONDPAGE evidence ", 400)}
	for index, step := range []struct {
		name  string
		input json.RawMessage
	}{
		{"read_url", json.RawMessage(`{"url":"https://arxiv.org/abs/2510.22344"}`)},
		{"read_tool_result", json.RawMessage(`{"result_ref":"run:x/checkpoint:decision:1/action:0","offset":8000}`)},
	} {
		appendResearchProposal(t, runtime, child, index+1, []models.ActionProposal{{Name: step.name, Input: step.input}})
		output, _ := json.Marshal(map[string]any{"title": "FAIR-RAG", "final_url": "https://arxiv.org/html/2510.22344", "markdown": pages[index], "engine": "lightweight", "word_count": 2000, "truncated": index == 0})
		appendResearchResult(t, runtime, child, index+1, 0, agent.ActionResult{Status: agent.ActionSucceeded, Output: output})
	}
	prefix, err := runtime.LoadCheckpointPrefix(ctx, child)
	if err != nil {
		t.Fatal(err)
	}
	request, err := runtime.BuildDecisionRequest(ctx, childExecution, prefix, nil)
	if err != nil {
		t.Fatal(err)
	}
	var joined strings.Builder
	for _, message := range request.Messages {
		joined.WriteString(message.Content)
	}
	text := joined.String()
	if strings.Count(text, "FIRSTPAGE") > 40 || !strings.Contains(text, "earlier page text omitted") || strings.Count(text, "SECONDPAGE") < 400 {
		t.Fatalf("first page occurrences=%d second=%d", strings.Count(text, "FIRSTPAGE"), strings.Count(text, "SECONDPAGE"))
	}
}
