package agent

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/huangxinxinyu/nano-notebook/internal/models"
)

// agingStep builds one complete Agent Step whose single Result carries body
// bytes of output under the given tool name.
func agingStep(t *testing.T, runID string, decision int, tool string, body string) ContextUnit {
	t.Helper()
	actionID := fmt.Sprintf("decision:%d/action:0", decision)
	output, _ := json.Marshal(map[string]any{"title": "Doc " + fmt.Sprint(decision), "content": body})
	checkpoint, err := NewActionResultCheckpoint(decision, 0, actionID, ActionResult{Status: ActionSucceeded, Output: output})
	if err != nil {
		t.Fatal(err)
	}
	return ContextUnit{
		Kind: ContextUnitAgentStep, MessageID: "msg", RunID: runID, DecisionNo: decision,
		Messages: []models.ModelMessage{
			{Role: models.RoleAssistant, ActionCalls: []models.ModelActionCall{{ID: actionID, Name: tool, Input: json.RawMessage(`{"url":"https://x"}`)}}},
			{Role: models.RoleAction, ActionCallID: actionID, Content: string(checkpoint.Payload)},
		},
	}
}

func agingTrajectory(t *testing.T, steps int) []ContextUnit {
	t.Helper()
	units := []ContextUnit{{Kind: ContextUnitUserMessage, MessageID: "msg", Messages: []models.ModelMessage{{Role: models.RoleUser, Content: "question"}}}}
	for decision := 1; decision <= steps; decision++ {
		units = append(units, agingStep(t, "run_a", decision, "read_url", strings.Repeat("page text ", 600)))
	}
	return units
}

func TestToolResultAgingMovesInBatchesAndKeepsRecentStepsExact(t *testing.T) {
	policy := toolResultAgingPolicy{KeepRecentSteps: 6, BatchSteps: 6, MinBytes: 2048, Rehydratable: true}
	agedCount := func(units []ContextUnit) int {
		count := 0
		for _, unit := range units {
			for _, message := range unit.Messages {
				if message.Role == models.RoleAction && strings.Contains(message.Content, `"content_state":"aged"`) {
					count++
				}
			}
		}
		return count
	}
	for steps, want := range map[int]int{1: 0, 6: 0, 11: 0, 12: 6, 17: 6, 18: 12, 23: 12, 24: 18} {
		out, stats := ageOldToolResults(agingTrajectory(t, steps), policy)
		if got := agedCount(out); got != want || stats.AgedResults != want {
			t.Fatalf("steps=%d aged=%d stats=%+v want=%d", steps, got, stats, want)
		}
	}
	// Within one batch window the aged prefix is byte-identical, so the
	// provider's prefix cache survives each new decision.
	twelve, _ := ageOldToolResults(agingTrajectory(t, 12), policy)
	seventeen, _ := ageOldToolResults(agingTrajectory(t, 17), policy)
	for index := range twelve {
		if FlattenContextUnits(twelve[index : index+1])[0].Content != FlattenContextUnits(seventeen[index : index+1])[0].Content {
			t.Fatalf("unit %d changed between 12 and 17 steps", index)
		}
		for messageIndex, message := range twelve[index].Messages {
			if message.Content != seventeen[index].Messages[messageIndex].Content {
				t.Fatalf("unit %d message %d changed between 12 and 17 steps", index, messageIndex)
			}
		}
	}
}

func TestToolResultAgingShellNamesTheCheckpointAndKeepsTheCall(t *testing.T) {
	input := agingTrajectory(t, 12)
	original := input[1].Messages[1].Content
	out, stats := ageOldToolResults(input, toolResultAgingPolicy{KeepRecentSteps: 6, BatchSteps: 6, MinBytes: 2048, Rehydratable: true})
	if input[1].Messages[1].Content != original {
		t.Fatal("aging mutated its input")
	}
	var shell agedToolResultShellPayload
	if err := json.Unmarshal([]byte(out[1].Messages[1].Content), &shell); err != nil {
		t.Fatalf("shell is not JSON: %v", err)
	}
	if shell.ActionID != "decision:1/action:0" || shell.Status != ActionSucceeded || shell.ContentState != "aged" ||
		shell.ResultRef != "run:run_a/checkpoint:decision:1/action:0" || !shell.Rehydratable || shell.ReadTool != "read_tool_result" ||
		shell.ResultBytes < 6000 || !strings.HasPrefix(shell.Preview, `{"content":"page text`) || !strings.Contains(shell.Note, "claims.md") {
		t.Fatalf("shell=%+v", shell)
	}
	if !compactedToolResultReferencePattern.MatchString(shell.ResultRef) {
		t.Fatalf("result_ref %q is not readable by read_tool_result", shell.ResultRef)
	}
	if out[1].Messages[0].ActionCalls[0].Name != "read_url" || string(out[1].Messages[0].ActionCalls[0].Input) != `{"url":"https://x"}` {
		t.Fatalf("the Tool call changed: %+v", out[1].Messages[0])
	}
	if out[7].Messages[1].Content != input[7].Messages[1].Content || out[12].Messages[1].Content != input[12].Messages[1].Content {
		t.Fatal("a recent Step was aged")
	}
	if stats.AgedBytes < 6*5000 {
		t.Fatalf("stats=%+v", stats)
	}
	chat, _ := ageOldToolResults(input, toolResultAgingPolicy{KeepRecentSteps: 6, BatchSteps: 6, MinBytes: 2048, Rehydratable: false})
	var chatShell agedToolResultShellPayload
	_ = json.Unmarshal([]byte(chat[1].Messages[1].Content), &chatShell)
	if chatShell.Rehydratable || chatShell.ReadTool != "" || strings.Contains(chatShell.Note, "read_tool_result") {
		t.Fatalf("chat shell=%+v", chatShell)
	}
}

func TestToolResultAgingSkipsSmallExemptAndAlreadyCompactedResults(t *testing.T) {
	units := []ContextUnit{{Kind: ContextUnitUserMessage, MessageID: "msg", Messages: []models.ModelMessage{{Role: models.RoleUser, Content: "q"}}}}
	units = append(units, agingStep(t, "run_a", 1, "record_claim", "short"))
	units = append(units, agingStep(t, "run_a", 2, "read_skill", strings.Repeat("skill ", 1000)))
	units = append(units, agingStep(t, "run_a", 3, "wait_agent", strings.Repeat("finding ", 1000)))
	capsule := agingStep(t, "run_a", 4, "read_url", strings.Repeat("page ", 1000))
	capsule.Messages[1].Content = `{"action_id":"decision:4/action:0","status":"succeeded","content_state":"compacted","result_ref":"run:run_a/checkpoint:decision:4/action:0","rehydratable":true}`
	units = append(units, capsule)
	for decision := 5; decision <= 14; decision++ {
		units = append(units, agingStep(t, "run_a", decision, "read_url", strings.Repeat("page ", 1000)))
	}
	out, stats := ageOldToolResults(units, toolResultAgingPolicy{KeepRecentSteps: 6, BatchSteps: 6, MinBytes: 2048, Rehydratable: true})
	for index := 1; index <= 4; index++ {
		if out[index].Messages[1].Content != units[index].Messages[1].Content {
			t.Fatalf("step %d was aged: %s", index, out[index].Messages[1].Content)
		}
	}
	if stats.AgedResults != 2 || !strings.Contains(out[5].Messages[1].Content, `"content_state":"aged"`) || !strings.Contains(out[6].Messages[1].Content, `"content_state":"aged"`) {
		t.Fatalf("stats=%+v", stats)
	}
}

func TestToolResultAgingIsPinnedByDefinitionVersion(t *testing.T) {
	for config, want := range map[string]bool{
		"research.executor@44": true, "research.executor@50": true, "research.executor@43": false,
		"chat.leader@8": true, "chat.leader@7": false, "studio.report@2": false, "": false,
	} {
		policy, ok := toolResultAgingFor(Execution{AgentConfigID: config})
		if ok != want {
			t.Fatalf("config=%q ok=%v want=%v", config, ok, want)
		}
		if ok && policy.Rehydratable != strings.HasPrefix(config, "research.") {
			t.Fatalf("config=%q policy=%+v", config, policy)
		}
	}
	reader := Execution{AgentConfigID: "research.executor@44", ParentRunID: "run_parent", SubagentTask: researchReaderTaskPrefix + " https://x (X)."}
	if _, ok := toolResultAgingFor(reader); !ok {
		t.Fatal("readers age their Results too")
	}
}
