package agent

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/huangxinxinyu/nano-notebook/internal/models"
)

func mustSteering(t *testing.T, decisionNo int, messageID, content string) PendingCheckpoint {
	t.Helper()
	checkpoint, err := NewSteeringCheckpoint(decisionNo, messageID, content)
	if err != nil {
		t.Fatal(err)
	}
	return checkpoint
}

func TestSteeringCheckpointIsAcceptedOnlyBetweenCompleteSteps(t *testing.T) {
	checkpoint := mustSteering(t, 2, "msg_steer", "focus on 2025 papers")
	if checkpoint.IdentityKey != "decision:2/steer:msg_steer" || checkpoint.Kind != CheckpointSteeringMessage ||
		checkpoint.DecisionNo != 2 || checkpoint.ActionIndex != nil || checkpoint.ActionID != "" {
		t.Fatalf("checkpoint metadata = %+v", checkpoint)
	}
	if _, err := NewSteeringCheckpoint(0, "msg", "x"); err == nil {
		t.Fatal("decision 0 accepted")
	}
	if _, err := NewSteeringCheckpoint(1, "msg", "   "); err == nil {
		t.Fatal("blank content accepted")
	}
	if _, err := NewSteeringCheckpoint(1, "bad id", "x"); err == nil {
		t.Fatal("invalid message id accepted")
	}

	toCheckpoints := func(pending ...PendingCheckpoint) []Checkpoint {
		checkpoints := make([]Checkpoint, 0, len(pending))
		for index, item := range pending {
			checkpoints = append(checkpoints, Checkpoint{SequenceNo: index + 1, PendingCheckpoint: item})
		}
		return checkpoints
	}
	prefix, err := LoadCheckpointPrefix(context.Background(), toCheckpoints(
		mustSteering(t, 1, "msg_a", "first"),
		mustProposal(t, 1, "tool_a"),
		mustResult(t, 1, 0, ActionSucceeded, `{"value":"a"}`, ""),
		mustSteering(t, 2, "msg_b", "second"),
		mustSteering(t, 2, "msg_c", "third"),
		mustFinal(t, 2, "done"),
	))
	if err != nil {
		t.Fatal(err)
	}
	if prefix.AcceptedDecisions != 2 || prefix.AcceptedActions != 1 || len(prefix.Steering) != 3 {
		t.Fatalf("prefix counters = %+v", prefix)
	}
	if prefix.Steering[0] != (AcceptedSteering{DecisionNo: 1, MessageID: "msg_a", Content: "first"}) ||
		prefix.Steering[2] != (AcceptedSteering{DecisionNo: 2, MessageID: "msg_c", Content: "third"}) {
		t.Fatalf("steering = %+v", prefix.Steering)
	}

	rejected := map[string][]Checkpoint{
		"inside incomplete batch": toCheckpoints(
			mustProposal(t, 1, "tool_a", "tool_b"),
			mustResult(t, 1, 0, ActionSucceeded, `{"value":"a"}`, ""),
			mustSteering(t, 2, "msg_a", "x"),
		),
		"wrong decision number": toCheckpoints(
			mustProposal(t, 1, "tool_a"),
			mustResult(t, 1, 0, ActionSucceeded, `{"value":"a"}`, ""),
			mustSteering(t, 1, "msg_a", "x"),
		),
		"after final": toCheckpoints(
			mustFinal(t, 1, "done"),
			mustSteering(t, 2, "msg_a", "x"),
		),
		"same message twice": toCheckpoints(
			mustSteering(t, 1, "msg_a", "x"),
			mustSteering(t, 1, "msg_a", "x"),
		),
	}
	for name, checkpoints := range rejected {
		if _, err := LoadCheckpointPrefix(context.Background(), checkpoints); !errors.Is(err, ErrCheckpointInvalid) {
			t.Fatalf("%s: err = %v, want checkpoint_invalid", name, err)
		}
	}
}

func TestProjectChatLanePlacesSteeringBeforeTheDecisionItPreceded(t *testing.T) {
	run := chatLaneRunFromPending(t, "run_1", []PendingCheckpoint{
		mustProposal(t, 1, "tool_a"),
		mustResult(t, 1, 0, ActionSucceeded, `{"value":"a"}`, ""),
		mustSteering(t, 2, "msg_steer", "drop the 2019 sources"),
		mustProposal(t, 2, "tool_b"),
		mustResult(t, 2, 0, ActionSucceeded, `{"value":"b"}`, ""),
		mustSteering(t, 3, "msg_late", "wrap up"),
		mustFinal(t, 3, "answer"),
	})
	units, err := ProjectChatLane(context.Background(), ChatLane{Turns: []ChatLaneTurn{
		{MessageID: "msg_1", Content: "question", Runs: []ChatLaneRun{run}},
	}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	keys := make([]string, 0, len(units))
	for _, unit := range units {
		keys = append(keys, ContextUnitKey(unit))
	}
	want := "message:msg_1,run:run_1/decision:1,message:msg_steer,run:run_1/decision:2,message:msg_late,run:run_1/decision:3"
	if got := strings.Join(keys, ","); got != want {
		t.Fatalf("unit keys = %s, want %s", got, want)
	}
	steer := units[2]
	if steer.Kind != ContextUnitUserMessage || steer.RunID != "run_1" || steer.DecisionNo != 2 || len(steer.Messages) != 1 ||
		steer.Messages[0].Role != models.RoleUser || !strings.HasSuffix(steer.Messages[0].Content, "\ndrop the 2019 sources") ||
		!strings.Contains(steer.Messages[0].Content, "Steering from the Member") {
		t.Fatalf("steering unit = %+v", steer)
	}
	flattened := FlattenContextUnits(units)
	if len(flattened) != 8 || flattened[3].Role != models.RoleUser || flattened[4].Role != models.RoleAssistant {
		t.Fatalf("flattened roles = %+v", flattened)
	}

	stripped := withoutSteering(mustPrefix(t, run.Checkpoints))
	if len(stripped.Steering) != 0 || stripped.AcceptedDecisions != 3 || len(stripped.Proposals) != 2 {
		t.Fatalf("withoutSteering = %+v", stripped)
	}
	plain, err := ProjectChatLane(context.Background(), ChatLane{Turns: []ChatLaneTurn{
		{MessageID: "msg_1", Content: "question", Runs: []ChatLaneRun{{RunID: "run_1", Prefix: &stripped}}},
	}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(plain) != 4 {
		t.Fatalf("stripped projection has %d units, want 4", len(plain))
	}
}

func mustPrefix(t *testing.T, checkpoints []Checkpoint) CheckpointPrefix {
	t.Helper()
	prefix, err := LoadCheckpointPrefix(context.Background(), checkpoints)
	if err != nil {
		t.Fatal(err)
	}
	return prefix
}

func TestResearchSteeringRecitationListsDeliveredMessagesInOrder(t *testing.T) {
	if researchSteeringRecitation(nil) != "" {
		t.Fatal("empty steering produced a recitation")
	}
	text := researchSteeringRecitation([]AcceptedSteering{
		{DecisionNo: 4, MessageID: "a", Content: "  prefer primary sources  "},
		{DecisionNo: 9, MessageID: "b", Content: "skip the survey papers"},
	})
	if !strings.Contains(text, "1. (before decision 4) prefer primary sources\n") ||
		!strings.HasSuffix(text, "2. (before decision 9) skip the survey papers") ||
		!strings.Contains(text, "Follow the latest entry") {
		t.Fatalf("recitation = %q", text)
	}
}

// steeringRuntimeStub answers PendingSteering from its own checkpoints, the
// way PostgresRuntime derives delivery, and projects the Chat lane so the
// model request shows where steering landed.
type steeringRuntimeStub struct {
	*controllerRuntimeStub
	inbox     []SteeringMessage
	pendingAt []int
}

func (r *steeringRuntimeStub) PendingSteering(ctx context.Context, _ Attempt) ([]SteeringMessage, error) {
	prefix, err := LoadCheckpointPrefix(ctx, r.checkpoints)
	if err != nil {
		return nil, err
	}
	r.pendingAt = append(r.pendingAt, prefix.AcceptedDecisions)
	pending := make([]SteeringMessage, 0)
	for _, message := range r.inbox {
		delivered := false
		for _, steering := range prefix.Steering {
			if steering.MessageID == message.ID {
				delivered = true
			}
		}
		if !delivered {
			pending = append(pending, message)
		}
	}
	return pending, nil
}

func (r *steeringRuntimeStub) BuildDecisionRequest(ctx context.Context, execution Execution, prefix CheckpointPrefix, definitions []models.ActionDefinition) (models.ModelRequest, error) {
	units, err := ProjectChatLane(ctx, ChatLane{Turns: []ChatLaneTurn{{
		MessageID: "msg_1", Content: "question", Runs: []ChatLaneRun{{RunID: execution.RunID, Prefix: &prefix}},
	}}}, nil)
	if err != nil {
		return models.ModelRequest{}, err
	}
	return models.ModelRequest{Model: execution.Model, Messages: FlattenContextUnits(units), ActionDefinitions: cloneActionDefinitions(definitions)}, nil
}

func TestControllerDeliversSteeringOnceAfterTheCurrentBatch(t *testing.T) {
	executionOrder := make([]string, 0, 1)
	registry, err := NewActionRegistry(&recordingAction{name: "record", order: &executionOrder})
	if err != nil {
		t.Fatal(err)
	}
	runtime := &steeringRuntimeStub{controllerRuntimeStub: &controllerRuntimeStub{execution: defaultControllerExecution()}}
	model := &decisionModelStub{decisions: []models.ModelDecision{
		{Proposal: &models.ActionProposalBatch{Actions: []models.ActionProposal{{Name: "record", Input: json.RawMessage(`{"value":"first"}`)}}}},
		{Final: &models.FinalDraft{Text: "adjusted"}},
	}}
	// The Member's message arrives while the batch is executing: the stub
	// receives it when the Action runs.
	action := registry.byName["record"].(*recordingAction)
	action.onExecute = func() {
		runtime.inbox = append(runtime.inbox, SteeringMessage{ID: "msg_steer", Content: "actually compare against 2024 only"})
	}

	if err := NewController(runtime, model, registry).Execute(context.Background(), runtime.execution.Attempt); err != nil {
		t.Fatal(err)
	}
	wantKinds := []CheckpointKind{CheckpointActionProposal, CheckpointActionResult, CheckpointSteeringMessage, CheckpointFinalDraft}
	if len(runtime.checkpoints) != len(wantKinds) {
		t.Fatalf("checkpoints = %+v", runtime.checkpoints)
	}
	for index, want := range wantKinds {
		if runtime.checkpoints[index].Kind != want {
			t.Fatalf("checkpoint %d kind = %q, want %q", index, runtime.checkpoints[index].Kind, want)
		}
	}
	if runtime.checkpoints[2].DecisionNo != 2 || runtime.checkpoints[2].IdentityKey != "decision:2/steer:msg_steer" {
		t.Fatalf("steering checkpoint = %+v", runtime.checkpoints[2])
	}
	if len(model.requests) != 2 {
		t.Fatalf("model requests = %d", len(model.requests))
	}
	first := model.requests[0].Messages
	if len(first) != 1 {
		t.Fatalf("first request saw %d messages before any steering", len(first))
	}
	second := model.requests[1].Messages
	if len(second) != 4 || second[2].Role != models.RoleAction || second[3].Role != models.RoleUser ||
		!strings.HasSuffix(second[3].Content, "actually compare against 2024 only") {
		t.Fatalf("second request messages = %+v", second)
	}
	if len(runtime.published) != 1 || runtime.published[0].Text != "adjusted" || len(runtime.failed) != 0 {
		t.Fatalf("published=%v failed=%v", runtime.published, runtime.failed)
	}
	// Steering is polled at every decision boundary and the message is never
	// re-delivered once checkpointed.
	if len(runtime.pendingAt) < 3 {
		t.Fatalf("pending polls = %v", runtime.pendingAt)
	}
}

func TestControllerDeliversSteeringPendingBeforeTheFirstDecision(t *testing.T) {
	registry, err := NewActionRegistry(NewCalculateAction())
	if err != nil {
		t.Fatal(err)
	}
	runtime := &steeringRuntimeStub{
		controllerRuntimeStub: &controllerRuntimeStub{execution: defaultControllerExecution()},
		inbox:                 []SteeringMessage{{ID: "msg_early", Content: "keep it short"}},
	}
	model := &decisionModelStub{decisions: []models.ModelDecision{{Final: &models.FinalDraft{Text: "short"}}}}
	if err := NewController(runtime, model, registry).Execute(context.Background(), runtime.execution.Attempt); err != nil {
		t.Fatal(err)
	}
	if len(runtime.checkpoints) != 2 || runtime.checkpoints[0].Kind != CheckpointSteeringMessage || runtime.checkpoints[0].DecisionNo != 1 ||
		runtime.checkpoints[1].Kind != CheckpointFinalDraft || runtime.checkpoints[1].DecisionNo != 1 {
		t.Fatalf("checkpoints = %+v", runtime.checkpoints)
	}
	if len(model.requests) != 1 || len(model.requests[0].Messages) != 2 || model.requests[0].Messages[1].Role != models.RoleUser {
		t.Fatalf("model request = %+v", model.requests)
	}
}
