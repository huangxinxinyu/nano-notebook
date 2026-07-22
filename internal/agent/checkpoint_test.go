package agent

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"github.com/huangxinxinyu/nano-notebook/internal/models"
)

func TestProposalCheckpointCanonicalizesPayloadAndAssignsStableActionIDs(t *testing.T) {
	first, err := NewProposalCheckpoint(1, models.ActionProposalBatch{Actions: []models.ActionProposal{
		{Name: "calculate", Input: json.RawMessage(`{"operands":["1","2"],"operation":"add"}`)},
	}})
	if err != nil {
		t.Fatal(err)
	}
	reordered, err := NewProposalCheckpoint(1, models.ActionProposalBatch{Actions: []models.ActionProposal{
		{Name: "calculate", Input: json.RawMessage(`{"operation":"add","operands":["1","2"]}`)},
	}})
	if err != nil {
		t.Fatal(err)
	}
	changed, err := NewProposalCheckpoint(1, models.ActionProposalBatch{Actions: []models.ActionProposal{
		{Name: "calculate", Input: json.RawMessage(`{"operation":"subtract","operands":["1","2"]}`)},
	}})
	if err != nil {
		t.Fatal(err)
	}

	if first.IdentityKey != "decision:1" || first.Kind != CheckpointActionProposal || first.DecisionNo != 1 || first.ActionIndex != nil || first.ActionID != "" {
		t.Fatalf("checkpoint metadata = %+v", first)
	}
	if string(first.Payload) != `{"actions":[{"action_id":"decision:1/action:0","index":0,"name":"calculate","input":{"operands":["1","2"],"operation":"add"}}]}` {
		t.Fatalf("canonical payload = %s", first.Payload)
	}
	if first.PayloadSHA256 == "" || first.PayloadSHA256 != reordered.PayloadSHA256 || string(first.Payload) != string(reordered.Payload) {
		t.Fatalf("equivalent payload hashes differ: %q/%q", first.PayloadSHA256, reordered.PayloadSHA256)
	}
	if changed.PayloadSHA256 == first.PayloadSHA256 {
		t.Fatal("different payload produced the same hash")
	}
}

func TestLoadCheckpointPrefixReconstructsAcceptedOutcomes(t *testing.T) {
	proposal, err := NewProposalCheckpoint(1, models.ActionProposalBatch{Actions: []models.ActionProposal{
		{Name: "current_time", Input: json.RawMessage(`{"time_zone":"UTC"}`)},
		{Name: "calculate", Input: json.RawMessage(`{"operation":"divide","operands":["1","0"]}`)},
	}})
	if err != nil {
		t.Fatal(err)
	}
	firstResult, err := NewActionResultCheckpoint(1, 0, "decision:1/action:0", ActionResult{Status: ActionSucceeded, Output: json.RawMessage(`{"time_zone":"UTC"}`)})
	if err != nil {
		t.Fatal(err)
	}
	secondResult, err := NewActionResultCheckpoint(1, 1, "decision:1/action:1", ActionResult{Status: ActionDomainError, ErrorCode: "division_by_zero"})
	if err != nil {
		t.Fatal(err)
	}
	final, err := NewFinalDraftCheckpoint(2, models.FinalDraft{Text: "The calculation cannot divide by zero."})
	if err != nil {
		t.Fatal(err)
	}

	checkpoints := []Checkpoint{
		{SequenceNo: 1, PendingCheckpoint: proposal},
		{SequenceNo: 2, PendingCheckpoint: firstResult},
		{SequenceNo: 3, PendingCheckpoint: secondResult},
		{SequenceNo: 4, PendingCheckpoint: final},
	}
	for length := 0; length <= len(checkpoints); length++ {
		if _, err := LoadCheckpointPrefix(context.Background(), checkpoints[:length]); err != nil {
			t.Fatalf("legal prefix length %d: %v", length, err)
		}
	}
	prefix, err := LoadCheckpointPrefix(context.Background(), checkpoints)
	if err != nil {
		t.Fatal(err)
	}
	if prefix.AcceptedDecisions != 2 || prefix.AcceptedActions != 2 || len(prefix.Proposals) != 1 || prefix.Final == nil || prefix.Final.Text != "The calculation cannot divide by zero." {
		t.Fatalf("prefix summary = %+v", prefix)
	}
	actions := prefix.Proposals[0].Actions
	if len(actions) != 2 || actions[0].ActionID != "decision:1/action:0" || actions[0].Result == nil || actions[0].Result.Status != ActionSucceeded || actions[1].Result == nil || actions[1].Result.ErrorCode != "division_by_zero" {
		t.Fatalf("reconstructed actions = %+v", actions)
	}
}

func TestLoadCheckpointPrefixRejectsIllegalDurableHistory(t *testing.T) {
	proposal, _ := NewProposalCheckpoint(1, models.ActionProposalBatch{Actions: []models.ActionProposal{
		{Name: "current_time", Input: json.RawMessage(`{}`)},
		{Name: "calculate", Input: json.RawMessage(`{"operation":"add","operands":["1","2"]}`)},
	}})
	result0, _ := NewActionResultCheckpoint(1, 0, "decision:1/action:0", ActionResult{Status: ActionSucceeded, Output: json.RawMessage(`{"ok":true}`)})
	result1, _ := NewActionResultCheckpoint(1, 1, "decision:1/action:1", ActionResult{Status: ActionSucceeded, Output: json.RawMessage(`{"value":"3"}`)})
	final1, _ := NewFinalDraftCheckpoint(1, models.FinalDraft{Text: "Done."})
	final2, _ := NewFinalDraftCheckpoint(2, models.FinalDraft{Text: "Done."})
	proposal2, _ := NewProposalCheckpoint(2, models.ActionProposalBatch{Actions: []models.ActionProposal{{Name: "current_time", Input: json.RawMessage(`{}`)}}})
	badHash := proposal
	badHash.PayloadSHA256 = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	tests := []struct {
		name        string
		checkpoints []Checkpoint
	}{
		{name: "sequence gap", checkpoints: []Checkpoint{{SequenceNo: 2, PendingCheckpoint: proposal}}},
		{name: "result without proposal", checkpoints: []Checkpoint{{SequenceNo: 1, PendingCheckpoint: result0}}},
		{name: "skipped action", checkpoints: []Checkpoint{{SequenceNo: 1, PendingCheckpoint: proposal}, {SequenceNo: 2, PendingCheckpoint: result1}}},
		{name: "next decision before completed batch", checkpoints: []Checkpoint{{SequenceNo: 1, PendingCheckpoint: proposal}, {SequenceNo: 2, PendingCheckpoint: final2}}},
		{name: "decision gap", checkpoints: []Checkpoint{{SequenceNo: 1, PendingCheckpoint: final2}}},
		{name: "outcome after final", checkpoints: []Checkpoint{{SequenceNo: 1, PendingCheckpoint: final1}, {SequenceNo: 2, PendingCheckpoint: proposal2}}},
		{name: "payload hash conflict", checkpoints: []Checkpoint{{SequenceNo: 1, PendingCheckpoint: badHash}}},
		{name: "duplicate result", checkpoints: []Checkpoint{{SequenceNo: 1, PendingCheckpoint: proposal}, {SequenceNo: 2, PendingCheckpoint: result0}, {SequenceNo: 3, PendingCheckpoint: result0}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := LoadCheckpointPrefix(context.Background(), tt.checkpoints)
			if !errors.Is(err, ErrCheckpointInvalid) {
				t.Fatalf("error = %v, want ErrCheckpointInvalid", err)
			}
		})
	}
}

func TestActionResultAndFinalDraftCheckpointsEncodeTypedPayloads(t *testing.T) {
	success, err := NewActionResultCheckpoint(2, 1, "decision:2/action:1", ActionResult{
		Status: ActionSucceeded, Output: json.RawMessage(`{"z":2,"a":1}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if success.IdentityKey != "decision:2/action:1" || success.Kind != CheckpointActionResult || success.DecisionNo != 2 || success.ActionIndex == nil || *success.ActionIndex != 1 || success.ActionID != "decision:2/action:1" {
		t.Fatalf("success metadata = %+v", success)
	}
	if string(success.Payload) != `{"action_id":"decision:2/action:1","status":"succeeded","output":{"a":1,"z":2}}` || success.PayloadSHA256 == "" {
		t.Fatalf("success payload = %s hash=%q", success.Payload, success.PayloadSHA256)
	}
	domainError, err := NewActionResultCheckpoint(2, 1, "decision:2/action:1", ActionResult{
		Status: ActionDomainError, ErrorCode: "division_by_zero",
	})
	if err != nil {
		t.Fatal(err)
	}
	if string(domainError.Payload) != `{"action_id":"decision:2/action:1","status":"domain_error","error_code":"division_by_zero"}` || domainError.PayloadSHA256 == success.PayloadSHA256 {
		t.Fatalf("domain-error payload = %s hash=%q", domainError.Payload, domainError.PayloadSHA256)
	}

	final, err := NewFinalDraftCheckpoint(3, models.FinalDraft{Text: "Final answer."})
	if err != nil {
		t.Fatal(err)
	}
	if final.IdentityKey != "decision:3/final" || final.Kind != CheckpointFinalDraft || final.DecisionNo != 3 || final.ActionIndex != nil || final.ActionID != "" || string(final.Payload) != `{"text":"Final answer."}` || final.PayloadSHA256 == "" {
		t.Fatalf("final checkpoint = %+v", final)
	}
}

func TestFinalCheckpointPreservesInlineSourceMarkersAsText(t *testing.T) {
	draft := models.FinalDraft{Text: "The launch is 20 July [source:src_a]."}
	pending, err := NewFinalDraftCheckpoint(1, draft)
	if err != nil {
		t.Fatal(err)
	}
	prefix, err := LoadCheckpointPrefix(context.Background(), []Checkpoint{{SequenceNo: 1, PendingCheckpoint: pending}})
	if err != nil {
		t.Fatal(err)
	}
	if prefix.Final == nil || !reflect.DeepEqual(*prefix.Final, draft) {
		t.Fatalf("reloaded Final Draft=%+v want=%+v", prefix.Final, draft)
	}
}
