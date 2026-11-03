package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/huangxinxinyu/nano-notebook/internal/models"
)

func completionTestAction(t *testing.T, name string, status ActionResultStatus, output any) AcceptedAction {
	t.Helper()
	result := &ActionResult{Status: status}
	if status == ActionSucceeded {
		payload, err := json.Marshal(output)
		if err != nil {
			t.Fatal(err)
		}
		result.Output = payload
	} else {
		result.ErrorCode = "test_failure"
	}
	return AcceptedAction{ActionID: name, Name: name, Input: json.RawMessage(`{}`), Result: result}
}

func completionTestCard(t *testing.T, id, status string) AcceptedAction {
	t.Helper()
	return completionTestAction(t, recordClaimActionName, ActionSucceeded, recordClaimOutput{ID: id, Status: status, Source: "https://example.com/" + id})
}

func completionTestClaims() []researchClaim {
	return []researchClaim{
		{recordClaimOutput: recordClaimOutput{ID: "c1", Status: claimStatusVerified}, Question: 1},
		{recordClaimOutput: recordClaimOutput{ID: "c2", Status: claimStatusNearMatch}, Question: 1},
		{recordClaimOutput: recordClaimOutput{ID: "c3", Status: claimStatusNotFound}, Question: 2},
		{recordClaimOutput: recordClaimOutput{ID: "c4", Status: claimStatusVerified}, Question: 2},
	}
}

func TestCheckResearchCompletionAcceptsAnsweredQuestionsBackedByCitedCards(t *testing.T) {
	output := checkResearchCompletion(researchCompletionCheck{
		Input: completeResearchInput{Questions: []completeResearchQuestion{
			{Question: 2, Status: researchQuestionAnswered, Cards: []string{"c4"}},
			{Question: 1, Status: researchQuestionAnswered, Cards: []string{"c1", "c2"}},
		}},
		Report: "First [c1, c2]. Second [c4].", Claims: completionTestClaims(), PlanQuestions: 2,
	})
	if !output.Accepted || output.StopReason != researchStopAnswered || len(output.Problems) != 0 {
		t.Fatalf("output=%+v", output)
	}
	if output.Questions[0].Question != 1 || output.Questions[1].Question != 2 {
		t.Fatalf("questions are not in plan order: %+v", output.Questions)
	}
}

func TestCheckResearchCompletionRejectsUnbackedDeclarations(t *testing.T) {
	output := checkResearchCompletion(researchCompletionCheck{
		Input: completeResearchInput{Questions: []completeResearchQuestion{
			{Question: 1, Status: researchQuestionAnswered, Cards: []string{"c1", "c9"}},
			{Question: 2, Status: researchQuestionPartial, Cards: []string{"c3", "c4"}},
			{Question: 4, Status: researchQuestionPartial},
		}},
		Report:        "First [c1]. Second [c3].",
		Claims:        completionTestClaims(),
		PlanQuestions: 3,
		Running:       []researchRunningSubagent{{AgentID: "run_r", TaskName: "Read paper"}},
	})
	if output.Accepted || output.StopReason != "" {
		t.Fatalf("output=%+v", output)
	}
	problems := strings.Join(output.Problems, "\n")
	for _, want := range []string{
		"Read paper", "Missing from the declaration: Q3", "Q4 is not in the accepted plan",
		"Q1 names cards that do not exist: c9", "Q2 names cards that failed verification: c3 (not_found)",
		"Q2 names cards the report never cites: c4", "Q4 is partial but names no claim card",
	} {
		if !strings.Contains(problems, want) {
			t.Fatalf("problems miss %q:\n%s", want, problems)
		}
	}
}

func TestCheckResearchCompletionSendsAnUnsupportedNotFoundBackOnce(t *testing.T) {
	check := researchCompletionCheck{
		Input: completeResearchInput{Questions: []completeResearchQuestion{
			{Question: 1, Status: researchQuestionAnswered, Cards: []string{"c1"}},
			{Question: 2, Status: researchQuestionNotFound},
			{Question: 3, Status: researchQuestionNotFound},
		}},
		Report: "First [c1].", Claims: completionTestClaims(), PlanQuestions: 3,
	}
	output := checkResearchCompletion(check)
	// Q2 has a usable card tagged to it, so only Q3 has nothing at all.
	if output.Accepted || len(output.RetryQuestions) != 1 || output.RetryQuestions[0] != 3 {
		t.Fatalf("output=%+v", output)
	}
	check.Retried = map[int]bool{3: true}
	if output := checkResearchCompletion(check); !output.Accepted || output.StopReason != researchStopPartial {
		t.Fatalf("retried output=%+v", output)
	}
	check.Retried = nil
	check.BudgetExhausted = true
	if output := checkResearchCompletion(check); !output.Accepted || output.StopReason != researchStopBudget {
		t.Fatalf("budget output=%+v", output)
	}
}

func TestResearchCompletionRetryNeedsALaterSearch(t *testing.T) {
	rejected := completeResearchOutput{Problems: []string{"retry"}, RetryQuestions: []int{3}}
	prefix := CheckpointPrefix{Proposals: []AcceptedProposal{
		{DecisionNo: 1, Actions: []AcceptedAction{
			completionTestAction(t, "web_search", ActionSucceeded, map[string]any{"results": []string{}}),
			completionTestAction(t, completeResearchActionName, ActionSucceeded, rejected),
		}},
	}}
	if retried := researchCompletionRetriedQuestions(prefix); retried[3] {
		t.Fatal("a search in the rejecting decision counted as the extra round")
	}
	prefix.Proposals = append(prefix.Proposals, AcceptedProposal{DecisionNo: 2, Actions: []AcceptedAction{
		completionTestAction(t, "read_url", ActionDomainError, nil),
	}})
	if retried := researchCompletionRetriedQuestions(prefix); retried[3] {
		t.Fatal("a failed read counted as the extra round")
	}
	prefix.Proposals = append(prefix.Proposals, AcceptedProposal{DecisionNo: 3, Actions: []AcceptedAction{
		completionTestAction(t, "web_search", ActionSucceeded, map[string]any{"results": []string{}}),
	}})
	if retried := researchCompletionRetriedQuestions(prefix); !retried[3] {
		t.Fatal("a later successful search did not count as the extra round")
	}
}

func TestAcceptedResearchCompletionIsVoidedByReassembly(t *testing.T) {
	accepted := completeResearchOutput{Accepted: true, StopReason: researchStopAnswered}
	assembled := completionTestAction(t, "assemble_research_report", ActionSucceeded, researchWorkspaceFile{Path: "report.md"})
	prefix := CheckpointPrefix{Proposals: []AcceptedProposal{
		{DecisionNo: 1, Actions: []AcceptedAction{assembled}},
		{DecisionNo: 2, Actions: []AcceptedAction{completionTestAction(t, completeResearchActionName, ActionSucceeded, accepted)}},
	}}
	if _, ok := acceptedResearchCompletion(prefix); !ok {
		t.Fatal("declaration after assembly was not accepted")
	}
	prefix.Proposals = append(prefix.Proposals, AcceptedProposal{DecisionNo: 3, Actions: []AcceptedAction{assembled}})
	if _, ok := acceptedResearchCompletion(prefix); ok {
		t.Fatal("declaration survived a later assembly")
	}
}

func TestResearchFinalNeedsAnAcceptedDeclarationWhileActionsRemain(t *testing.T) {
	runtime := &ResearchRuntime{}
	report := researchWorkspaceFile{Path: "report.md", ObjectKey: "research-workspaces/run/report.md", SHA256: strings.Repeat("a", 64), Bytes: 10}
	prefix := CheckpointPrefix{Proposals: []AcceptedProposal{
		{DecisionNo: 1, Actions: []AcceptedAction{completionTestAction(t, "assemble_research_report", ActionSucceeded, report)}},
	}}
	final := models.ModelDecision{Final: &models.FinalDraft{Text: "done"}}
	execution := Execution{AgentConfigID: "research.executor@42", ActionLimit: 10, ActionDecisionLimit: 10}
	if _, err := runtime.PrepareDecisionResponse(context.Background(), execution, prefix, final); err == nil || !strings.Contains(err.Error(), "complete_research") {
		t.Fatalf("err=%v", err)
	}
	if _, err := runtime.PrepareDecisionResponse(context.Background(), Execution{AgentConfigID: "research.executor@41", ActionLimit: 10, ActionDecisionLimit: 10}, prefix, final); err != nil {
		t.Fatalf("v41 Final was gated: %v", err)
	}
	if _, err := runtime.PrepareDecisionResponse(context.Background(), Execution{AgentConfigID: "research.executor@42", ActionLimit: 1, ActionDecisionLimit: 10}, prefix, final); err != nil {
		t.Fatalf("Final was gated after the Action boundary closed: %v", err)
	}
	prefix.Proposals = append(prefix.Proposals, AcceptedProposal{DecisionNo: 2, Actions: []AcceptedAction{
		completionTestAction(t, completeResearchActionName, ActionSucceeded, completeResearchOutput{Accepted: true, StopReason: researchStopAnswered}),
	}})
	if _, err := runtime.PrepareDecisionResponse(context.Background(), execution, prefix, final); err != nil {
		t.Fatalf("accepted declaration still gated Final: %v", err)
	}
}

func TestMeasureResearchSaturationCountsCardsPerRound(t *testing.T) {
	tree := researchClaimTree{Runs: []researchClaimRun{
		{RunID: "run_root", Prefix: CheckpointPrefix{Proposals: []AcceptedProposal{
			{DecisionNo: 1, Actions: []AcceptedAction{completionTestAction(t, "web_search", ActionSucceeded, map[string]any{})}},
			{DecisionNo: 2, Actions: []AcceptedAction{
				completionTestAction(t, "read_url", ActionSucceeded, map[string]any{}),
				completionTestCard(t, "c1", claimStatusVerified),
				completionTestCard(t, "c2", claimStatusNotFound),
			}},
			{DecisionNo: 3, Actions: []AcceptedAction{completionTestAction(t, "wait_agent", ActionSucceeded, map[string]any{
				"agents": []runtimeSubagentObservation{{AgentID: "run_a", Status: "completed"}, {AgentID: "run_b", Status: "running"}},
			})}},
			{DecisionNo: 4, Actions: []AcceptedAction{completionTestAction(t, "wait_agent", ActionSucceeded, map[string]any{
				"agents": []runtimeSubagentObservation{{AgentID: "run_a", Status: "completed"}},
			})}},
		}}},
		{RunID: "run_a", Namespace: "a", Prefix: CheckpointPrefix{Proposals: []AcceptedProposal{{DecisionNo: 1, Actions: []AcceptedAction{
			completionTestCard(t, "a1", claimStatusVerified), completionTestCard(t, "a2", claimStatusNearMatch),
		}}}}},
		{RunID: "run_b", Namespace: "b", Prefix: CheckpointPrefix{Proposals: []AcceptedProposal{{DecisionNo: 1, Actions: []AcceptedAction{
			completionTestCard(t, "b1", claimStatusVerified),
		}}}}},
	}}
	saturation := measureResearchSaturation(tree, "run_root")
	want := []researchSaturationRound{
		{Decision: 2, Reads: 1, NewCards: 1},
		{Decision: 3, ReadersCollected: 1, NewCards: 2},
	}
	if len(saturation.Rounds) != len(want) {
		t.Fatalf("rounds=%+v", saturation.Rounds)
	}
	for index := range want {
		if saturation.Rounds[index] != want[index] {
			t.Fatalf("round %d=%+v want %+v", index, saturation.Rounds[index], want[index])
		}
	}
	if saturation.UncollectedChildCards != 1 {
		t.Fatalf("uncollected=%d", saturation.UncollectedChildCards)
	}
}

func TestBuildResearchCompletionRecordCountsRejections(t *testing.T) {
	assembled := completionTestAction(t, "assemble_research_report", ActionSucceeded, researchWorkspaceFile{Path: "report.md"})
	tree := researchClaimTree{Runs: []researchClaimRun{{RunID: "run_root", Prefix: CheckpointPrefix{Proposals: []AcceptedProposal{
		{DecisionNo: 1, Actions: []AcceptedAction{assembled}},
		{DecisionNo: 2, Actions: []AcceptedAction{completionTestAction(t, completeResearchActionName, ActionSucceeded, completeResearchOutput{Problems: []string{"x"}})}},
		{DecisionNo: 3, Actions: []AcceptedAction{completionTestAction(t, completeResearchActionName, ActionSucceeded, completeResearchOutput{
			Accepted: true, StopReason: researchStopPartial, Questions: []completeResearchQuestion{{Question: 1, Status: researchQuestionPartial, Cards: []string{"c1"}}},
		})}},
	}}}}}
	record := buildResearchCompletionRecord(tree, "run_root")
	if record.StopReason != researchStopPartial || record.RejectedDeclarations != 1 || len(record.Questions) != 1 {
		t.Fatalf("record=%+v", record)
	}
	if undeclared := buildResearchCompletionRecord(researchClaimTree{Runs: []researchClaimRun{{RunID: "run_root"}}}, "run_root"); undeclared.StopReason != researchStopUndeclared {
		t.Fatalf("undeclared=%+v", undeclared)
	}
}

func TestDecodeCompleteResearchInputNormalizesCardIDs(t *testing.T) {
	input, err := decodeCompleteResearchInput(json.RawMessage(`{"questions":[{"question":1,"status":"answered","cards":["[c3]"," 16d1c2 "]}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if got := input.Questions[0].Cards; got[0] != "c3" || got[1] != "16d1-c2" {
		t.Fatalf("cards=%v", got)
	}
	for _, raw := range []string{`{}`, `{"questions":[{"question":0,"status":"answered"}]}`, `{"questions":[{"question":1,"status":"done"}]}`, `{"questions":[],"stop":"x"}`} {
		if _, err := decodeCompleteResearchInput(json.RawMessage(raw)); err == nil {
			t.Fatalf("accepted %s", raw)
		}
	}
}
