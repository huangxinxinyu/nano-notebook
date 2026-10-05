package app_test

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/huangxinxinyu/nano-notebook/internal/agent"
	"github.com/huangxinxinyu/nano-notebook/internal/agentcatalog"
	"github.com/huangxinxinyu/nano-notebook/internal/models"
	"github.com/huangxinxinyu/nano-notebook/internal/promptcatalog"
)

func TestResearchWaitAgentDispatchesQueuedLongReadWhenReaderSlotFrees(t *testing.T) {
	api := newTestAPI(t)
	ctx := context.Background()
	parent, _, _, _ := admitResearchExecutionForRelease(t, api, "reader-queue@example.com", "nano.default@40")
	runtime, err := agent.NewResearchRuntime(api.db.Pool(), promptcatalog.MustLoadEmbedded())
	if err != nil {
		t.Fatal(err)
	}
	execution, err := runtime.Load(ctx, attemptFromClaim(parent))
	if err != nil {
		t.Fatal(err)
	}
	definition, ok := agentcatalog.MustLoadEmbedded().ResolveDefinition(agentcatalog.MustParseReference("research.executor@27"))
	if !ok {
		t.Fatal("missing research.executor@27")
	}
	f := runtimeSubagentFixture{api: api, parent: parent, runtime: runtime, execution: execution, definition: definition, tools: map[string]agent.Action{}, sink: &capturingDirectTraceSink{}}
	for _, registration := range agent.NewRuntimeSubagentToolRegistrations(api.db.Pool()) {
		f.tools[registration.Action.Definition().Name] = registration.Action
	}
	attempt := attemptFromClaim(parent)

	// Four readers fill every slot, so the next long document is excerpted.
	spawns := make([]models.ActionProposal, 4)
	for i := range spawns {
		input, _ := json.Marshal(map[string]string{"message": fmt.Sprintf("Read one long document for the parent researcher, in full: Paper %d (https://example.com/paper-%d).", i, i), "task_name": "reader"})
		spawns[i] = models.ActionProposal{Name: "spawn_agent", Input: input}
	}
	appendResearchProposal(t, runtime, attempt, 1, spawns)
	readers := make([]string, len(spawns))
	for i, spawn := range spawns {
		result, err := f.tools["spawn_agent"].Execute(ctx, f.request("spawn_agent", fmt.Sprintf("decision:1/action:%d", i), spawn.Input, attempt))
		if err != nil || result.Status != agent.ActionSucceeded {
			t.Fatalf("spawn %d=%+v err=%v", i, result, err)
		}
		appendResearchResult(t, runtime, attempt, 1, i, result)
		var output struct {
			AgentID string `json:"agent_id"`
		}
		_ = json.Unmarshal(result.Output, &output)
		readers[i] = output.AgentID
	}
	const queuedURL = "https://arxiv.org/abs/2511.09545"
	readInput := json.RawMessage(`{"url":"` + queuedURL + `"}`)
	appendResearchProposal(t, runtime, attempt, 2, []models.ActionProposal{{Name: "read_url", Input: readInput}})
	excerpt, _ := json.Marshal(map[string]any{
		"outcome": "reader_capacity_excerpt", "requested_url": queuedURL, "final_url": "https://arxiv.org/html/2511.09545",
		"title": "Queued paper", "markdown": "Opening excerpt", "engine": "lightweight", "word_count": 11861, "truncated": true,
	})
	appendResearchResult(t, runtime, attempt, 2, 0, agent.ActionResult{Status: agent.ActionSucceeded, Output: excerpt})

	// While every reader runs, waiting dispatches nothing.
	waitInput, _ := json.Marshal(map[string]any{"agent_ids": readers, "timeout_ms": 0})
	appendResearchProposal(t, runtime, attempt, 3, []models.ActionProposal{{Name: "wait_agent", Input: waitInput}})
	busy, err := f.tools["wait_agent"].Execute(ctx, f.request("wait_agent", "decision:3/action:0", waitInput, attempt))
	if err != nil || busy.Status != agent.ActionSucceeded || strings.Contains(string(busy.Output), "auto_dispatched_readers") {
		t.Fatalf("busy wait=%s err=%v", busy.Output, err)
	}
	appendResearchResult(t, runtime, attempt, 3, 0, busy)

	child := f.claimChild(t, readers[0])
	if _, err := runtime.Load(ctx, attemptFromClaim(child)); err != nil {
		t.Fatal(err)
	}
	draft := models.FinalDraft{Text: "Reader finished with cards a1-a3"}
	checkpoint, _ := agent.NewFinalDraftCheckpoint(1, draft)
	if _, err := runtime.AppendCheckpoint(ctx, attemptFromClaim(child), checkpoint); err != nil {
		t.Fatal(err)
	}
	if err := runtime.PublishFinal(ctx, attemptFromClaim(child), draft); err != nil {
		t.Fatal(err)
	}

	appendResearchProposal(t, runtime, attempt, 4, []models.ActionProposal{{Name: "wait_agent", Input: waitInput}})
	var dispatched struct {
		AutoReaders []struct {
			AgentID  string `json:"agent_id"`
			TaskName string `json:"task_name"`
			Status   string `json:"status"`
		} `json:"auto_dispatched_readers"`
	}
	for replay := 0; replay < 2; replay++ {
		result, err := f.tools["wait_agent"].Execute(ctx, f.request("wait_agent", "decision:4/action:0", waitInput, attempt))
		if err != nil || result.Status != agent.ActionSucceeded {
			t.Fatalf("wait %d=%+v err=%v", replay, result, err)
		}
		if err := json.Unmarshal(result.Output, &dispatched); err != nil || len(dispatched.AutoReaders) != 1 ||
			dispatched.AutoReaders[0].TaskName != "Read: Queued paper" || dispatched.AutoReaders[0].Status != "queued" {
			t.Fatalf("wait %d output=%s err=%v", replay, result.Output, err)
		}
	}
	var children int
	var actionID, message string
	if err := api.db.Pool().QueryRow(ctx, `select count(*) over (),action_id,message from agent_subagents
		where parent_run_id=$1 order by created_at desc,child_run_id limit 1`, parent.RunID).Scan(&children, &actionID, &message); err != nil {
		t.Fatal(err)
	}
	if children != 5 || actionID != "reader:decision:2/action:0" || !strings.Contains(message, queuedURL) {
		t.Fatalf("children=%d action=%q message=%q", children, actionID, message)
	}

}

func TestResearchReadersCoverEachArxivPaperOnce(t *testing.T) {
	api := newTestAPI(t)
	ctx := context.Background()
	parent, _, _, _ := admitResearchExecutionForRelease(t, api, "reader-dedupe@example.com", "nano.default@40")
	runtime, err := agent.NewResearchRuntime(api.db.Pool(), promptcatalog.MustLoadEmbedded())
	if err != nil {
		t.Fatal(err)
	}
	execution, err := runtime.Load(ctx, attemptFromClaim(parent))
	if err != nil {
		t.Fatal(err)
	}
	definition, _ := agentcatalog.MustLoadEmbedded().ResolveDefinition(agentcatalog.MustParseReference("research.executor@27"))
	f := runtimeSubagentFixture{api: api, parent: parent, runtime: runtime, execution: execution, definition: definition, tools: map[string]agent.Action{}, sink: &capturingDirectTraceSink{}}
	for _, registration := range agent.NewRuntimeSubagentToolRegistrations(api.db.Pool()) {
		f.tools[registration.Action.Definition().Name] = registration.Action
	}
	attempt := attemptFromClaim(parent)
	reader := func(url string) json.RawMessage {
		input, _ := json.Marshal(map[string]string{"message": "Read one long document for the parent researcher, in full: " + url + " (FAIR-RAG).\n\nRecord claim cards.", "task_name": "Read: FAIR-RAG"})
		return input
	}
	spawns := []models.ActionProposal{{Name: "spawn_agent", Input: reader("https://arxiv.org/abs/2510.22344")}, {Name: "spawn_agent", Input: reader("https://arxiv.org/html/2510.22344v1")}}
	appendResearchProposal(t, runtime, attempt, 1, spawns)
	ids := make([]string, len(spawns))
	for i, spawn := range spawns {
		result, err := f.tools["spawn_agent"].Execute(ctx, f.request("spawn_agent", fmt.Sprintf("decision:1/action:%d", i), spawn.Input, attempt))
		if err != nil || result.Status != agent.ActionSucceeded {
			t.Fatalf("spawn %d=%+v err=%v", i, result, err)
		}
		appendResearchResult(t, runtime, attempt, 1, i, result)
		var output struct {
			AgentID        string `json:"agent_id"`
			AlreadyReading bool   `json:"already_reading"`
		}
		_ = json.Unmarshal(result.Output, &output)
		ids[i] = output.AgentID
		if output.AlreadyReading != (i == 1) {
			t.Fatalf("spawn %d output=%s", i, result.Output)
		}
	}
	if ids[0] != ids[1] {
		t.Fatalf("html variant got its own reader: %v", ids)
	}

	// A queued pdf variant of the same paper is covered by that reader too.
	readInput := json.RawMessage(`{"url":"https://arxiv.org/pdf/2510.22344"}`)
	appendResearchProposal(t, runtime, attempt, 2, []models.ActionProposal{{Name: "read_url", Input: readInput}})
	excerpt, _ := json.Marshal(map[string]any{"outcome": "reader_capacity_excerpt", "requested_url": "https://arxiv.org/pdf/2510.22344", "final_url": "https://arxiv.org/pdf/2510.22344", "title": "FAIR-RAG", "markdown": "Opening", "word_count": 9000, "truncated": true})
	appendResearchResult(t, runtime, attempt, 2, 0, agent.ActionResult{Status: agent.ActionSucceeded, Output: excerpt})
	waitInput, _ := json.Marshal(map[string]any{"agent_ids": ids[:1], "timeout_ms": 0})
	appendResearchProposal(t, runtime, attempt, 3, []models.ActionProposal{{Name: "wait_agent", Input: waitInput}})
	result, err := f.tools["wait_agent"].Execute(ctx, f.request("wait_agent", "decision:3/action:0", waitInput, attempt))
	if err != nil || result.Status != agent.ActionSucceeded || strings.Contains(string(result.Output), "auto_dispatched_readers") {
		t.Fatalf("wait=%s err=%v", result.Output, err)
	}
	var children int
	if err := api.db.Pool().QueryRow(ctx, `select count(*) from agent_subagents where parent_run_id=$1`, parent.RunID).Scan(&children); err != nil || children != 1 {
		t.Fatalf("children=%d err=%v", children, err)
	}
}
