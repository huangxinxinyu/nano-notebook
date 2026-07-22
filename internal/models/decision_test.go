package models

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"
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

func TestModelCallMetadataKeepsUnknownCostDistinctFromZero(t *testing.T) {
	zero := 0.0
	knownZero := ModelCallMetadata{
		RequestedModel: "aliyun/qwen-flash",
		ResultKind:     ModelResultFinalDraft,
		Latency:        12 * time.Millisecond,
		Cost:           ModelCost{Known: true, Amount: &zero, Currency: "USD", Source: "gateway"},
	}
	if err := knownZero.Validate(); err != nil {
		t.Fatalf("known zero cost: %v", err)
	}
	unknown := ModelCallMetadata{
		RequestedModel: "aliyun/qwen-flash",
		ResultKind:     ModelResultActionProposal,
		Latency:        time.Millisecond,
		Cost:           ModelCost{Known: false},
	}
	if err := unknown.Validate(); err != nil {
		t.Fatalf("unknown cost: %v", err)
	}
	encoded, err := json.Marshal(unknown)
	if err != nil {
		t.Fatal(err)
	}
	if string(encoded) == "" || unknown.Cost.Amount != nil {
		t.Fatalf("unknown cost was represented as zero: %s", encoded)
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

func TestFinalDraftIsTextOnly(t *testing.T) {
	if _, exists := reflect.TypeOf(FinalDraft{}).FieldByName("Claims"); exists {
		t.Fatal("FinalDraft still exposes model-authored claims")
	}
	if err := (FinalDraft{Text: "The launch is 20 July [source:src_a]."}).Validate(); err != nil {
		t.Fatal(err)
	}
}
