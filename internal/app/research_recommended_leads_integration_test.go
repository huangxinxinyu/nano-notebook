package app_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/huangxinxinyu/nano-notebook/internal/agent"
	"github.com/huangxinxinyu/nano-notebook/internal/agentcatalog"
	"github.com/huangxinxinyu/nano-notebook/internal/jobs"
	"github.com/huangxinxinyu/nano-notebook/internal/models"
	"github.com/huangxinxinyu/nano-notebook/internal/objectstore"
	"github.com/huangxinxinyu/nano-notebook/internal/promptcatalog"
)

func TestScoutCandidatesBecomeRecommendedLeadsAtAssembly(t *testing.T) {
	api := newTestAPI(t)
	ctx := context.Background()
	parent, _, _, _ := admitResearchExecutionForRelease(t, api, "recommended-leads@example.com", "nano.default@45")
	runtime, err := agent.NewResearchRuntime(api.db.Pool(), promptcatalog.MustLoadEmbedded())
	if err != nil {
		t.Fatal(err)
	}
	attempt := attemptFromClaim(parent)
	execution, err := runtime.Load(ctx, attempt)
	if err != nil {
		t.Fatal(err)
	}
	if err := runtime.StartRun(ctx, attempt, execution); err != nil {
		t.Fatal(err)
	}
	var sessionID string
	if err := api.db.Pool().QueryRow(ctx, `select id from research_sessions where execution_run_id=$1`, parent.RunID).Scan(&sessionID); err != nil {
		t.Fatal(err)
	}
	// A plain search candidate seen first must rank after the scout's picks.
	if _, err := api.db.Pool().Exec(ctx, `insert into research_evidence_ledger(session_id,url,title,status,first_seen_at) values($1,'https://example.com/plain','Plain','discovered',now()-interval '1 hour')`, sessionID); err != nil {
		t.Fatal(err)
	}
	scout, ok, err := jobs.NewQueue(api.db.Pool()).ClaimNext(ctx)
	if err != nil || !ok || scout.RunID == parent.RunID {
		t.Fatalf("scout claim=%+v ok=%v err=%v", scout, ok, err)
	}
	if _, err := runtime.Load(ctx, attemptFromClaim(scout)); err != nil {
		t.Fatal(err)
	}
	draft := models.FinalDraft{Text: "## Q1\n| # | URL | Title |\n|---|---|---|\n| 1 | https://arxiv.org/abs/2401.14887 | The Power of Noise (critique) |\n| 2 | https://arxiv.org/abs/2212.10509 | IRCoT |\n"}
	checkpoint, _ := agent.NewFinalDraftCheckpoint(1, draft)
	if _, err := runtime.AppendCheckpoint(ctx, attemptFromClaim(scout), checkpoint); err != nil {
		t.Fatal(err)
	}
	if err := runtime.PublishFinal(ctx, attemptFromClaim(scout), draft); err != nil {
		t.Fatal(err)
	}

	workspaceActions, err := agent.NewResearchWorkspaceActions(api.db.Pool(), objectstore.NewMemoryStore())
	if err != nil {
		t.Fatal(err)
	}
	workspace := actionsByName(workspaceActions)
	definition := agentcatalog.MustParseReference("research.executor@32")
	for decision, step := range []struct {
		name  string
		input json.RawMessage
	}{
		{"write_research_file", json.RawMessage(`{"path":"sections/a.md","content":"## A\n\nDraft."}`)},
		{"assemble_research_report", json.RawMessage(`{"title":"A","section_paths":["sections/a.md"]}`)},
	} {
		appendResearchProposal(t, runtime, attempt, decision+1, []models.ActionProposal{{Name: step.name, Input: step.input}})
		result, err := workspace[step.name].Execute(ctx, agent.ActionRequest{ActionID: "decision:" + string(rune('1'+decision)) + "/action:0", Attempt: attempt, Definition: definition, Input: step.input})
		if err != nil || result.Status != agent.ActionSucceeded {
			t.Fatalf("%s=%+v err=%v", step.name, result, err)
		}
		appendResearchResult(t, runtime, attempt, decision+1, 0, result)
		if step.name != "assemble_research_report" {
			continue
		}
		var output struct {
			Guidance string `json:"guidance"`
			Coverage struct {
				UnreadLeads []struct {
					URL         string `json:"url"`
					Recommended bool   `json:"recommended"`
				} `json:"unread_leads"`
			} `json:"source_coverage"`
		}
		if err := json.Unmarshal(result.Output, &output); err != nil {
			t.Fatal(err)
		}
		leads := output.Coverage.UnreadLeads
		if len(leads) != 3 || leads[0].URL != "https://arxiv.org/abs/2401.14887" || !leads[0].Recommended || leads[1].URL != "https://arxiv.org/abs/2212.10509" ||
			leads[2].URL != "https://example.com/plain" || leads[2].Recommended || !strings.Contains(output.Guidance, "Recommended but unread: 2 sources") {
			t.Fatalf("assembly=%s", result.Output)
		}
	}
}
