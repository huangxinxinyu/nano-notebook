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

func TestCompletedResearchReaderRecordsItsLeadsAsUnreadLeads(t *testing.T) {
	api := newTestAPI(t)
	ctx := context.Background()
	parent, _, _, _ := admitResearchExecutionForRelease(t, api, "reader-leads@example.com", "nano.default@43")
	runtime, err := agent.NewResearchRuntime(api.db.Pool(), promptcatalog.MustLoadEmbedded())
	if err != nil {
		t.Fatal(err)
	}
	execution, err := runtime.Load(ctx, attemptFromClaim(parent))
	if err != nil {
		t.Fatal(err)
	}
	definition, ok := agentcatalog.MustLoadEmbedded().ResolveDefinition(agentcatalog.MustParseReference("research.executor@30"))
	if !ok {
		t.Fatal("missing research.executor@30")
	}
	f := runtimeSubagentFixture{api: api, parent: parent, runtime: runtime, execution: execution, definition: definition, tools: map[string]agent.Action{}, sink: &capturingDirectTraceSink{}}
	for _, registration := range agent.NewRuntimeSubagentToolRegistrations(api.db.Pool()) {
		f.tools[registration.Action.Definition().Name] = registration.Action
	}
	var sessionID string
	if err := api.db.Pool().QueryRow(ctx, `select id from research_sessions where execution_run_id=$1`, parent.RunID).Scan(&sessionID); err != nil {
		t.Fatal(err)
	}
	if _, err := api.db.Pool().Exec(ctx, `insert into research_evidence_ledger(session_id,url,title,status) values($1,'https://arxiv.org/abs/2305.15294','Iter-RetGen','read')`, sessionID); err != nil {
		t.Fatal(err)
	}

	input, _ := json.Marshal(map[string]string{"message": "Read one long document for the parent researcher, in full: FAIR-RAG (https://arxiv.org/abs/2510.22344).", "task_name": "Read: FAIR-RAG"})
	appendResearchProposal(t, runtime, attemptFromClaim(parent), 1, []models.ActionProposal{{Name: "spawn_agent", Input: input}})
	spawned, err := f.tools["spawn_agent"].Execute(ctx, f.request("spawn_agent", "decision:1/action:0", input, attemptFromClaim(parent)))
	if err != nil || spawned.Status != agent.ActionSucceeded {
		t.Fatalf("spawn=%+v err=%v", spawned, err)
	}
	appendResearchResult(t, runtime, attemptFromClaim(parent), 1, 0, spawned)
	var reader struct {
		AgentID string `json:"agent_id"`
	}
	_ = json.Unmarshal(spawned.Output, &reader)
	child := f.claimChild(t, reader.AgentID)
	if _, err := runtime.Load(ctx, attemptFromClaim(child)); err != nil {
		t.Fatal(err)
	}
	draft := models.FinalDraft{Text: "Recorded cards b1-b9.\n\n## Leads\n- Iter-RetGen arXiv:2305.15294 — the baseline FAIR-RAG beats.\n- Self-RAG (arXiv:2310.11511) — critique-based alternative.\n"}
	checkpoint, _ := agent.NewFinalDraftCheckpoint(1, draft)
	if _, err := runtime.AppendCheckpoint(ctx, attemptFromClaim(child), checkpoint); err != nil {
		t.Fatal(err)
	}
	if err := runtime.PublishFinal(ctx, attemptFromClaim(child), draft); err != nil {
		t.Fatal(err)
	}
	rows, err := api.db.Pool().Query(ctx, `select url,status,title from research_evidence_ledger where session_id=$1 order by url`, sessionID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	got := map[string][2]string{}
	for rows.Next() {
		var url, status, title string
		if err := rows.Scan(&url, &status, &title); err != nil {
			t.Fatal(err)
		}
		got[url] = [2]string{status, title}
	}
	if len(got) != 2 || got["https://arxiv.org/abs/2305.15294"] != [2]string{"read", "Iter-RetGen"} ||
		got["https://arxiv.org/abs/2310.11511"] != [2]string{"discovered", "Self-RAG (arXiv:2310.11511) — critique-based alternative."} {
		t.Fatalf("ledger=%v", got)
	}
}
