package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/huangxinxinyu/nano-notebook/internal/models"
)

func TestSelectResearchArchivalStepsKeepsExactCompleteSuffixAndCapsBatch(t *testing.T) {
	units := make([]ContextUnit, 30)
	for index := range units {
		decision := index + 1
		units[index] = ContextUnit{
			Kind: ContextUnitAgentStep, RunID: "run_v10", DecisionNo: decision,
			Messages: []models.ModelMessage{
				{Role: models.RoleAssistant, ActionCalls: []models.ModelActionCall{
					{ID: fmt.Sprintf("decision:%d/action:0", decision), Name: "arbitrary_tool", Input: json.RawMessage(`{"complete":"input"}`)},
					{ID: fmt.Sprintf("decision:%d/action:1", decision), Name: "another_tool", Input: json.RawMessage(`{"other":true}`)},
				}},
				{Role: models.RoleAction, ActionCallID: fmt.Sprintf("decision:%d/action:0", decision), Content: strings.Repeat("old-result", 100)},
				{Role: models.RoleAction, ActionCallID: fmt.Sprintf("decision:%d/action:1", decision), Content: strings.Repeat("sibling-result", 100)},
			},
		}
	}
	selected, suffixStart, err := selectResearchArchivalSteps(units, map[int]researchArchivalCapsule{}, 2_000, 24)
	if err != nil {
		t.Fatal(err)
	}
	if len(selected) != 24 || selected[0].DecisionNo != 1 || selected[23].DecisionNo != 24 || suffixStart <= 24 {
		t.Fatalf("selected=%d range=%d..%d suffix=%d", len(selected), selected[0].DecisionNo, selected[len(selected)-1].DecisionNo, suffixStart)
	}
	for _, unit := range selected {
		if len(unit.Messages) != 3 || len(unit.Messages[0].ActionCalls) != 2 {
			t.Fatalf("selection split a complete multi-Action Step: %+v", unit)
		}
	}
}

func TestApplyResearchArchivalCapsulesUsesGenericShellsAndPreservesCheckpointInputs(t *testing.T) {
	result := &ActionResult{Status: ActionSucceeded, Output: json.RawMessage(`{"large_body":"` + strings.Repeat("x", 1000) + `"}`)}
	prefix := CheckpointPrefix{Proposals: []AcceptedProposal{{DecisionNo: 1, Actions: []AcceptedAction{
		{ActionID: "decision:1/action:0", Index: 0, Name: "future_unknown_tool", Input: json.RawMessage(`{"query":"full exact parameter","limit":17}`), Result: result},
		{ActionID: "decision:1/action:1", Index: 1, Name: "another_unknown_tool", Input: json.RawMessage(`{"source_id":"src_exact"}`), Result: &ActionResult{Status: ActionDomainError, Error: &ActionError{
			Kind: "domain", Code: "bounded_failure", Message: "sensitive failure body must leave the compacted shell", Suggestion: "sensitive remediation detail", Retryable: true,
		}}},
	}}}}
	before, _ := json.Marshal(prefix)
	units, err := ProjectChatLane(nilContext(), ChatLane{Turns: []ChatLaneTurn{{
		MessageID: "msg_research", Content: "request", Runs: []ChatLaneRun{{RunID: "run_v10", Prefix: &prefix}},
	}}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	projected, err := applyResearchArchivalCapsules(units[1:], map[int]researchArchivalCapsule{1: {
		DecisionNo: 1, CapsuleJSON: json.RawMessage(`{"schema_version":"nano.research-capsule@1","decision_no":1,"objective_advanced":"tested generic projection","conclusions":[],"decisions":[],"constraints":[],"durable_refs":[],"contradictions":[],"verification":[],"follow_up":[]}`),
	}})
	if err != nil {
		t.Fatal(err)
	}
	after, _ := json.Marshal(prefix)
	if !bytes.Equal(before, after) {
		t.Fatal("archival projection mutated accepted checkpoints")
	}
	if len(projected) != 1 || len(projected[0].Messages) != 4 {
		t.Fatalf("projected=%+v", projected)
	}
	proposal := projected[0].Messages[1]
	if proposal.ActionCalls[0].Name != "future_unknown_tool" || string(proposal.ActionCalls[0].Input) != `{"query":"full exact parameter","limit":17}` ||
		proposal.ActionCalls[1].Name != "another_unknown_tool" || string(proposal.ActionCalls[1].Input) != `{"source_id":"src_exact"}` {
		t.Fatalf("Tool Calls changed: %+v", proposal.ActionCalls)
	}
	for _, message := range projected[0].Messages[2:] {
		if strings.Contains(message.Content, "large_body") || !strings.Contains(message.Content, `"content_state":"compacted"`) ||
			!strings.Contains(message.Content, `"action_id":"`+message.ActionCallID+`"`) || !strings.Contains(message.Content, `"result_ref":"`) {
			t.Fatalf("invalid generic Result shell: %s", message.Content)
		}
		if strings.Contains(message.Content, "sensitive failure body") || strings.Contains(message.Content, "sensitive remediation") {
			t.Fatalf("generic Result shell retained structured error prose: %s", message.Content)
		}
	}
	if !strings.Contains(projected[0].Messages[3].Content, `"error_code":"bounded_failure"`) {
		t.Fatalf("generic Result shell lost stable error code: %s", projected[0].Messages[3].Content)
	}
	if !strings.Contains(projected[0].Messages[2].Content, `"read_tool":"read_tool_result"`) ||
		!strings.Contains(projected[0].Messages[2].Content, `"rehydratable":true`) {
		t.Fatalf("successful Result shell is not model-readable: %s", projected[0].Messages[2].Content)
	}
	if strings.Contains(projected[0].Messages[3].Content, `"read_tool"`) ||
		!strings.Contains(projected[0].Messages[3].Content, `"rehydratable":false`) {
		t.Fatalf("failed Result shell incorrectly promises rehydration: %s", projected[0].Messages[3].Content)
	}
}

func TestDecodeResearchCapsuleBatchRequiresExactOrderedRangeAndEightKiBPerCapsule(t *testing.T) {
	steps := []researchArchivalStep{
		{DecisionNo: 4, StartCheckpointSeq: 10, EndCheckpointSeq: 12},
		{DecisionNo: 5, StartCheckpointSeq: 13, EndCheckpointSeq: 14},
	}
	valid := `{"schema_version":"nano.research-capsules@1","capsules":[` +
		`{"schema_version":"nano.research-capsule@1","decision_no":4,"start_checkpoint_seq":10,"end_checkpoint_seq":12,"objective_advanced":"A","conclusions":[],"decisions":[],"constraints":[],"durable_refs":[],"contradictions":[],"verification":[],"follow_up":[]},` +
		`{"schema_version":"nano.research-capsule@1","decision_no":5,"start_checkpoint_seq":13,"end_checkpoint_seq":14,"objective_advanced":"B","conclusions":[],"decisions":[],"constraints":[],"durable_refs":[],"contradictions":[],"verification":[],"follow_up":[]}]}`
	decoded, err := decodeResearchCapsuleBatch([]byte(valid), steps)
	if err != nil || len(decoded) != 2 || decoded[0].DecisionNo != 4 || decoded[1].DecisionNo != 5 {
		t.Fatalf("decoded=%+v err=%v", decoded, err)
	}
	for name, invalid := range map[string]string{
		"wrong order":   strings.Replace(valid, `"decision_no":4`, `"decision_no":5`, 1),
		"unknown field": strings.Replace(valid, `"objective_advanced":"A"`, `"objective_advanced":"A","raw_body":"forbidden"`, 1),
		"oversized":     strings.Replace(valid, `"objective_advanced":"A"`, `"objective_advanced":"`+strings.Repeat("x", researchArchivalCapsuleMaxBytes)+`"`, 1),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := decodeResearchCapsuleBatch([]byte(invalid), steps); err == nil {
				t.Fatal("accepted invalid Capsule batch")
			}
		})
	}
}

func TestApplyResearchTaskMemoriesEvictsCoveredCallsInputsShellsAndCapsules(t *testing.T) {
	units := make([]ContextUnit, 0, 4)
	archives := make(map[int]researchArchivalCapsule)
	for decision := 1; decision <= 4; decision++ {
		units = append(units, ContextUnit{Kind: ContextUnitAgentStep, RunID: "run_v10", DecisionNo: decision, Messages: []models.ModelMessage{
			{Role: models.RoleAssistant, ActionCalls: []models.ModelActionCall{{ID: fmt.Sprintf("decision:%d/action:0", decision), Name: "tool", Input: json.RawMessage(fmt.Sprintf(`{"secret_input":%d}`, decision))}}},
			{Role: models.RoleAction, ActionCallID: fmt.Sprintf("decision:%d/action:0", decision), Content: fmt.Sprintf(`{"action_id":"decision:%d/action:0","status":"succeeded"}`, decision)},
		}})
		archives[decision] = researchArchivalCapsule{DecisionNo: decision, CapsuleJSON: json.RawMessage(fmt.Sprintf(`{"decision_no":%d}`, decision))}
	}
	memories := []researchTaskMemory{{FirstDecisionNo: 1, LastDecisionNo: 3, MemoryJSON: json.RawMessage(`{"schema_version":"nano.research-task-memory@1","goal":"continue"}`)}}
	projected, err := applyResearchTaskMemories(units, archives, memories)
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(projected)
	if len(projected) != 2 || projected[0].Kind != ContextUnitResearchTaskMemory || strings.Contains(string(encoded), "secret_input\":1") || strings.Contains(string(encoded), "decision_no\":2") {
		t.Fatalf("covered trajectory leaked: %s", encoded)
	}
	if string(projected[1].Messages[1].ActionCalls[0].Input) != `{"secret_input":4}` || !strings.Contains(string(encoded), "research_step_capsule") {
		t.Fatalf("uncovered archived Step was lost: %s", encoded)
	}
}

func TestDecodeResearchTaskMemoryRequiresExactCapsuleRangeAnd32KiBBudget(t *testing.T) {
	capsules := []researchArchivalCapsule{
		{DecisionNo: 7, StartCheckpointSeq: 19, EndCheckpointSeq: 20, CapsuleSHA256: strings.Repeat("a", 64)},
		{DecisionNo: 8, StartCheckpointSeq: 21, EndCheckpointSeq: 23, CapsuleSHA256: strings.Repeat("b", 64)},
	}
	valid := `{"schema_version":"nano.research-task-memory@1","first_decision_no":7,"last_decision_no":8,"start_checkpoint_seq":19,"end_checkpoint_seq":23,"goal":"finish report","phase":"execution","conclusions":[],"decisions":[],"constraints":[],"durable_refs":[],"contradictions":[],"failed_paths":[],"verification":[],"report_state":[],"follow_up":[]}`
	memory, err := decodeResearchTaskMemory([]byte(valid), capsules)
	if err != nil || memory.FirstDecisionNo != 7 || memory.LastDecisionNo != 8 {
		t.Fatalf("memory=%+v err=%v", memory, err)
	}
	for name, invalid := range map[string]string{
		"range":     strings.Replace(valid, `"last_decision_no":8`, `"last_decision_no":9`, 1),
		"unknown":   strings.Replace(valid, `"goal":"finish report"`, `"goal":"finish report","raw_excerpt":"forbidden"`, 1),
		"oversized": strings.Replace(valid, `"goal":"finish report"`, `"goal":"`+strings.Repeat("x", researchTaskMemoryMaxBytes)+`"`, 1),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := decodeResearchTaskMemory([]byte(invalid), capsules); err == nil {
				t.Fatal("accepted invalid Task Memory")
			}
		})
	}
}

func nilContext() context.Context { return context.Background() }
