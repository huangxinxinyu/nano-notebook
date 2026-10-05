package app_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/huangxinxinyu/nano-notebook/internal/agent"
	"github.com/huangxinxinyu/nano-notebook/internal/agentcatalog"
	"github.com/huangxinxinyu/nano-notebook/internal/models"
	"github.com/huangxinxinyu/nano-notebook/internal/objectstore"
	"github.com/huangxinxinyu/nano-notebook/internal/promptcatalog"
)

func TestResearchAssemblyReportsCoveragePerPlanQuestion(t *testing.T) {
	api := newTestAPI(t)
	ctx := context.Background()
	claimed, _, _, _ := admitResearchExecutionForRelease(t, api, "question-coverage@example.com", "nano.default@40")
	runtime, err := agent.NewResearchRuntime(api.db.Pool(), promptcatalog.MustLoadEmbedded())
	if err != nil {
		t.Fatal(err)
	}
	attempt := attemptFromClaim(claimed)
	if _, err := runtime.Load(ctx, attempt); err != nil {
		t.Fatal(err)
	}
	definition := agentcatalog.MustParseReference("research.executor@27")
	execute := func(decision int, action agent.Action, input json.RawMessage) agent.ActionResult {
		t.Helper()
		appendResearchProposal(t, runtime, attempt, decision, []models.ActionProposal{{Name: action.Definition().Name, Input: input}})
		result, err := action.Execute(ctx, agent.ActionRequest{ActionID: "decision:" + string(rune('0'+decision)) + "/action:0", Attempt: attempt, Definition: definition, Input: input})
		if err != nil || result.Status != agent.ActionSucceeded {
			t.Fatalf("%s=%+v err=%v", action.Definition().Name, result, err)
		}
		appendResearchResult(t, runtime, attempt, decision, 0, result)
		return result
	}

	readInput := json.RawMessage(`{"url":"https://example.com/loops"}`)
	appendResearchProposal(t, runtime, attempt, 1, []models.ActionProposal{{Name: "read_url", Input: readInput}})
	page, _ := json.Marshal(map[string]any{"title": "Agent loops", "final_url": "https://example.com/loops", "engine": "lightweight", "word_count": 9,
		"markdown": "ReAct interleaves reasoning traces with actions inside one loop."})
	appendResearchResult(t, runtime, attempt, 1, 0, agent.ActionResult{Status: agent.ActionSucceeded, Output: page})

	card := execute(2, agent.NewRecordClaimAction(api.db.Pool(), nil),
		json.RawMessage(`{"source":"https://example.com/loops","quote":"ReAct interleaves reasoning traces with actions","claim":"ReAct mixes reasoning and acting","question":1}`))
	if !strings.Contains(string(card.Output), `"status":"verified"`) {
		t.Fatalf("card=%s", card.Output)
	}
	workspaceActions, err := agent.NewResearchWorkspaceActions(api.db.Pool(), objectstore.NewMemoryStore())
	if err != nil {
		t.Fatal(err)
	}
	workspace := actionsByName(workspaceActions)
	execute(3, workspace["write_research_file"], json.RawMessage(`{"path":"sections/loops.md","content":"## Loops\n\nReAct mixes reasoning and acting [c1]."}`))
	assembled := execute(4, workspace["assemble_research_report"], json.RawMessage(`{"title":"Loops","section_paths":["sections/loops.md"]}`))
	var output struct {
		Guidance  string `json:"guidance"`
		Questions struct {
			Questions []struct {
				Question     int    `json:"question"`
				Text         string `json:"text"`
				Cards        int    `json:"cards"`
				CitedCards   int    `json:"cited_cards"`
				Sources      int    `json:"sources"`
				CitedSources int    `json:"cited_sources"`
			} `json:"questions"`
		} `json:"question_coverage"`
	}
	if err := json.Unmarshal(assembled.Output, &output); err != nil {
		t.Fatal(err)
	}
	questions := output.Questions.Questions
	if len(questions) != 1 || questions[0].Text != "How do the loops differ?" || questions[0].Cards != 1 || questions[0].CitedCards != 1 ||
		questions[0].Sources != 1 || questions[0].CitedSources != 1 || !strings.Contains(output.Guidance, "Q1 rest on a single source") {
		t.Fatalf("assembly=%s", assembled.Output)
	}
}
