package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/huangxinxinyu/nano-notebook/internal/models"
)

func TestResearchPlanningContextSuppliesCurrentDateWithoutInventingScope(t *testing.T) {
	got := researchPlanningContext(time.Date(2026, 8, 22, 23, 30, 0, 0, time.UTC), "Asia/Shanghai")
	for _, required := range []string{"2026-08-23", "Asia/Shanghai", "does not create a date cutoff"} {
		if !strings.Contains(got, required) {
			t.Fatalf("planning context missing %q: %s", required, got)
		}
	}
}

func TestValidateResearchPlanJSONNormalizesScopeListFromModel(t *testing.T) {
	raw := `{
		"title":"Harness research",
		"objective":"Produce a decision report",
		"scope":["Current public implementations","DeepSeek, Claude Code, and Codex"],
		"research_questions":["How do their loops differ?"],
		"investigation_tracks":["Official source code"],
		"source_strategy":["Prefer current primary sources"],
		"analysis_method":["Compare shared dimensions"],
		"deliverable_outline":["Executive summary"],
		"completion_criteria":["Material claims are read-backed"],
		"clarifying_questions":[]
	}`

	canonical, err := ValidateResearchPlanJSON(raw)
	if err != nil {
		t.Fatal(err)
	}
	var plan map[string]any
	if err := json.Unmarshal(canonical, &plan); err != nil {
		t.Fatal(err)
	}
	if got, ok := plan["scope"].(string); !ok || got != "Current public implementations\nDeepSeek, Claude Code, and Codex" {
		t.Fatalf("scope=%#v", plan["scope"])
	}
}

func TestResearchPlannerFinalAcceptsAFencedOrIntroducedPlan(t *testing.T) {
	plan := `{"title":"T","objective":"O","scope":"S","research_questions":["Q"],"investigation_tracks":["I"],"source_strategy":["S"],"analysis_method":["A"],"deliverable_outline":["D"],"completion_criteria":["C"],"clarifying_questions":[]}`
	runtime := &ResearchPlanningRuntime{}
	for _, text := range []string{plan, "```json\n" + plan + "\n```", "以下是研究计划：\n\n" + plan} {
		prepared, err := runtime.PrepareDecisionResponse(context.Background(), Execution{}, CheckpointPrefix{}, models.ModelDecision{Final: &models.FinalDraft{Text: text}})
		if err != nil || !strings.HasPrefix(prepared.Final.Text, "{") {
			t.Fatalf("text=%q prepared=%+v err=%v", text, prepared.Final, err)
		}
	}
	_, err := runtime.PrepareDecisionResponse(context.Background(), Execution{}, CheckpointPrefix{}, models.ModelDecision{Final: &models.FinalDraft{Text: "我还需要再搜索一下。"}})
	if err == nil || !strings.Contains(err.Error(), `began with "我还需要再搜索一下。"`) {
		t.Fatalf("err=%v", err)
	}
}
