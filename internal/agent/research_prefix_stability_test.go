package agent

import (
	"fmt"
	"strings"
	"testing"
)

func repeatStep(decision int, name, code string) AcceptedProposal {
	return AcceptedProposal{DecisionNo: decision, Actions: []AcceptedAction{{
		ActionID: fmt.Sprintf("decision:%d/action:0", decision), Name: name,
		Result: &ActionResult{Status: ActionDomainError, ErrorCode: code},
	}}}
}

func TestResearchPausedToolNamesFollowTheTrailingRepeatRun(t *testing.T) {
	succeeded := AcceptedProposal{DecisionNo: 1, Actions: []AcceptedAction{{ActionID: "decision:1/action:0", Name: "web_search", Result: &ActionResult{Status: ActionSucceeded}}}}
	one := CheckpointPrefix{Proposals: []AcceptedProposal{succeeded, repeatStep(2, "web_search", "research_duplicate_action")}}
	if paused := researchPausedToolNames(one); len(paused) != 0 {
		t.Fatalf("one repeat paused %v", paused)
	}
	two := CheckpointPrefix{Proposals: append(one.Proposals, repeatStep(3, "read_url", "research_duplicate_action"))}
	if paused := researchPausedToolNames(two); strings.Join(paused, ",") != "read_url,web_search" {
		t.Fatalf("two repeats paused %v", paused)
	}
	// A call refused because its tool was paused extends the run.
	three := CheckpointPrefix{Proposals: append(two.Proposals, repeatStep(4, "web_search", researchToolPausedCode))}
	if got := consecutiveResearchDuplicateSteps(three); got != 3 {
		t.Fatalf("repeat run=%d", got)
	}
	recovered := CheckpointPrefix{Proposals: append(three.Proposals, AcceptedProposal{DecisionNo: 5, Actions: []AcceptedAction{{ActionID: "decision:5/action:0", Name: "record_claim", Result: &ActionResult{Status: ActionSucceeded}}}})}
	if paused := researchPausedToolNames(recovered); len(paused) != 0 {
		t.Fatalf("pause outlived a productive decision: %v", paused)
	}
}

func TestResearchRuntimeNoticesAreOneTaggedMessage(t *testing.T) {
	got := renderResearchRuntimeNotices([]string{"Budget: closed. ", "Pending Source Imports:\n- src_a: ready, searchable"})
	if got != "<runtime_notices>\nBudget: closed.\n\nPending Source Imports:\n- src_a: ready, searchable\n</runtime_notices>" {
		t.Fatalf("notices=%q", got)
	}
	if !strings.Contains(researchPausedToolsNotice([]string{"web_search"}, 2), "returns research_tool_paused without running") {
		t.Fatal("pause notice does not say how a paused call behaves")
	}
}

func TestReadersStopPageElisionAndAgeInSmallerBatchesFromV45(t *testing.T) {
	task := researchReaderTaskPrefix + " https://x (X)."
	v44 := Execution{ParentRunID: "p", SubagentTask: task, AgentConfigID: "research.executor@44"}
	v45 := Execution{ParentRunID: "p", SubagentTask: task, AgentConfigID: "research.executor@45"}
	if researchReaderKeptPages(v44) != 2 || researchReaderKeptPages(v45) != 0 {
		t.Fatalf("kept pages v44=%d v45=%d", researchReaderKeptPages(v44), researchReaderKeptPages(v45))
	}
	if policy, ok := toolResultAgingFor(v45); !ok || policy.KeepRecentSteps != 4 || policy.BatchSteps != 4 || !policy.Rehydratable {
		t.Fatalf("v45 reader policy=%+v ok=%v", policy, ok)
	}
	if policy, _ := toolResultAgingFor(v44); policy.KeepRecentSteps != 6 {
		t.Fatalf("v44 reader policy=%+v", policy)
	}
	root := Execution{AgentConfigID: "research.executor@45"}
	if policy, _ := toolResultAgingFor(root); policy.KeepRecentSteps != 6 || policy.BatchSteps != 6 {
		t.Fatalf("v45 root policy=%+v", policy)
	}
	if !isFrozenPrefixResearchExecution(root) || isFrozenPrefixResearchExecution(Execution{AgentConfigID: "research.executor@44"}) {
		t.Fatal("frozen-prefix gating is wrong")
	}
}
