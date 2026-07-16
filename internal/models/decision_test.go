package models

import (
	"encoding/json"
	"testing"
)

func TestModelDecisionRequiresExactlyOneVariant(t *testing.T) {
	final := &FinalDraft{Text: "A final answer."}
	proposal := &ActionProposalBatch{Actions: []ActionProposal{{Name: "current_time", Input: json.RawMessage(`{"time_zone":"UTC"}`)}}}
	tests := []struct {
		name    string
		value   ModelDecision
		wantErr bool
	}{
		{name: "final", value: ModelDecision{Final: final}},
		{name: "proposal", value: ModelDecision{Proposal: proposal}},
		{name: "both", value: ModelDecision{Final: final, Proposal: proposal}, wantErr: true},
		{name: "neither", value: ModelDecision{}, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.value.Validate()
			if (err != nil) != tt.wantErr {
				t.Fatalf("Validate() error = %v, wantErr %t", err, tt.wantErr)
			}
		})
	}
}

func TestModelDecisionRejectsEmptyVariantPayload(t *testing.T) {
	tests := []struct {
		name  string
		value ModelDecision
	}{
		{name: "blank final", value: ModelDecision{Final: &FinalDraft{Text: "  \n"}}},
		{name: "empty proposal", value: ModelDecision{Proposal: &ActionProposalBatch{}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := tt.value.Validate(); err == nil {
				t.Fatal("Validate() error = nil, want invalid payload")
			}
		})
	}
}
