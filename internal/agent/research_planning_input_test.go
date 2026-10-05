package agent

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func planningTestQuestions() []planningQuestion {
	return []planningQuestion{
		{ID: "audience", Question: "Who reads the report?", Options: []planningQuestionOption{{Label: "Backend engineers"}, {Label: "Product leads"}}, Recommended: 0},
		{ID: "depth", Question: "How deep?", Options: []planningQuestionOption{{Label: "Survey"}, {Label: "Deep dive"}, {Label: "Benchmark replication"}}, Recommended: 1},
	}
}

func TestDecodeRequestUserInputValidatesQuestionShape(t *testing.T) {
	valid, _ := json.Marshal(requestUserInputInput{Questions: planningTestQuestions()})
	if _, err := decodeRequestUserInput(valid); err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*requestUserInputInput){
		"too many questions":  func(in *requestUserInputInput) { in.Questions = append(in.Questions, in.Questions[0], in.Questions[1]) },
		"duplicate id":        func(in *requestUserInputInput) { in.Questions[1].ID = "audience" },
		"one option":          func(in *requestUserInputInput) { in.Questions[0].Options = in.Questions[0].Options[:1] },
		"recommended outside": func(in *requestUserInputInput) { in.Questions[0].Recommended = 2 },
		"duplicate label":     func(in *requestUserInputInput) { in.Questions[0].Options[1].Label = "Backend engineers" },
		"bad id":              func(in *requestUserInputInput) { in.Questions[0].ID = "Audience" },
	} {
		input := requestUserInputInput{Questions: planningTestQuestions()}
		mutate(&input)
		raw, _ := json.Marshal(input)
		if _, err := decodeRequestUserInput(raw); err == nil {
			t.Fatalf("%s: accepted %s", name, raw)
		}
	}
}

func TestNormalizePlanningAnswersAcceptsChoicesTextAndRecommendedDefaults(t *testing.T) {
	answers, err := normalizePlanningAnswers(planningTestQuestions(), []PlanningAnswer{
		{ID: "audience", Text: " Security reviewers "},
	}, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(answers) != 2 || answers[0].Text != "Security reviewers" || answers[0].Recommended ||
		answers[1].Choice != "Deep dive" || !answers[1].Recommended {
		t.Fatalf("answers=%+v", answers)
	}
	if _, err := normalizePlanningAnswers(planningTestQuestions(), []PlanningAnswer{{ID: "audience", Choice: "Product leads"}}, false); !errors.Is(err, ErrPlanningAnswerInvalid) {
		t.Fatalf("missing answer without defaults err=%v", err)
	}
	for name, answers := range map[string][]PlanningAnswer{
		"unknown option":  {{ID: "audience", Choice: "Everyone"}, {ID: "depth", Choice: "Survey"}},
		"choice and text": {{ID: "audience", Choice: "Product leads", Text: "x"}, {ID: "depth", Choice: "Survey"}},
		"unknown id":      {{ID: "audience", Choice: "Product leads"}, {ID: "depth", Choice: "Survey"}, {ID: "budget", Text: "x"}},
		"duplicate id":    {{ID: "audience", Choice: "Product leads"}, {ID: "audience", Choice: "Product leads"}},
	} {
		if _, err := normalizePlanningAnswers(planningTestQuestions(), answers, true); !errors.Is(err, ErrPlanningAnswerInvalid) {
			t.Fatalf("%s err=%v", name, err)
		}
	}
}

func TestPlanningTurnContentCarriesTheRevisedPlan(t *testing.T) {
	if got := planningTurnContent(0, "original request", 0, ""); got != "original request" {
		t.Fatalf("turn 0 content=%q", got)
	}
	got := planningTurnContent(2, "把范围缩小到开源方案", 3, `{"title":"x"}`)
	for _, want := range []string{"version 3", `{"title":"x"}`, "把范围缩小到开源方案", "request_user_input"} {
		if !strings.Contains(got, want) {
			t.Fatalf("revision content missing %q: %s", want, got)
		}
	}
}

func TestRequestUserInputDefinitionRegisters(t *testing.T) {
	if _, err := NewActionRegistry(&requestUserInputAction{}); err != nil {
		t.Fatal(err)
	}
}
