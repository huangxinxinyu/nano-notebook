package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestReadToolResultDefinitionExplainsPaginationContract(t *testing.T) {
	description := (&readToolResultAction{}).Definition().Description
	for _, term := range []string{"next_offset", "complete=false", "complete=true"} {
		if !strings.Contains(description, term) {
			t.Fatalf("description %q does not contain %q", description, term)
		}
	}
}

func TestReadToolResultActionReturnsScopedBoundedPage(t *testing.T) {
	envelope := testToolResultEnvelope([]byte(`{"markdown":"decision evidence 🛡️","source":"primary"}`))
	store := &recordingToolResultStore{envelopes: []ToolResultEnvelope{envelope}}
	action := NewReadToolResultAction(ToolResultReader{Store: store, MaximumPageBytes: 16, Now: testToolResultNow})
	input := json.RawMessage(`{"result_ref":"tr_test_reference","offset":0,"max_bytes":4096}`)

	result, err := action.Execute(context.Background(), ActionRequest{
		ActionID: "decision:2/action:0", Input: input, UserID: "user_a", ChatID: "chat_a",
		Attempt: Attempt{RunID: "run_a"},
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if result.Status != ActionSucceeded {
		t.Fatalf("result = %#v", result)
	}
	var page ToolResultPage
	if err := json.Unmarshal(result.Output, &page); err != nil {
		t.Fatalf("decode page: %v", err)
	}
	if page.ResultRef != envelope.ResultRef || page.Offset != 0 || page.NextOffset > 16 || page.Complete {
		t.Fatalf("page = %#v", page)
	}
}

func TestReadToolResultActionReadsCompactedCheckpointReference(t *testing.T) {
	checkpointStore := &recordingCheckpointResultStore{result: ActionResult{
		Status: ActionSucceeded, Output: json.RawMessage(`{"answer":"restored"}`),
	}}
	action := NewReadToolResultAction(
		ToolResultReader{MaximumPageBytes: 64, MaximumOutputBytes: 512},
		CompactedToolResultReader{Store: checkpointStore, MaximumPageBytes: 64},
	)
	ref := "run:run_a/checkpoint:decision:4/action:2"
	input, err := json.Marshal(readToolResultInput{ResultRef: ref, MaxBytes: 64})
	if err != nil {
		t.Fatal(err)
	}

	result, err := action.Execute(context.Background(), ActionRequest{
		ActionID: "decision:5/action:0", Input: input, UserID: "user_a", ChatID: "chat_a",
		Attempt: Attempt{RunID: "run_a"},
	})
	if err != nil || result.Status != ActionSucceeded {
		t.Fatalf("result=%#v err=%v", result, err)
	}
	var page ToolResultPage
	if err := json.Unmarshal(result.Output, &page); err != nil {
		t.Fatal(err)
	}
	if page.ResultRef != ref || page.Content != `{"answer":"restored"}` || !page.Complete {
		t.Fatalf("page=%#v", page)
	}
}

func TestReadToolResultActionCapsFinalModelVisibleJSON(t *testing.T) {
	payload, err := json.Marshal(map[string]string{"markdown": strings.Repeat(`<tag attr="value">\\path</tag>`, 300)})
	if err != nil {
		t.Fatal(err)
	}
	envelope := testToolResultEnvelope(payload)
	action := NewReadToolResultAction(ToolResultReader{
		Store: &recordingToolResultStore{envelopes: []ToolResultEnvelope{envelope}}, MaximumPageBytes: 512,
		MaximumOutputBytes: 512, Now: testToolResultNow,
	})
	result, err := action.Execute(context.Background(), ActionRequest{
		ActionID: "decision:2/action:0", Input: json.RawMessage(`{"result_ref":"tr_test_reference","offset":0,"max_bytes":512}`),
		UserID: "user_a", ChatID: "chat_a", Attempt: Attempt{RunID: "run_a"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Output) > 512 {
		t.Fatalf("model-visible page bytes=%d want <=512", len(result.Output))
	}
	checkpoint, err := NewActionResultCheckpoint(2, 0, "decision:2/action:0", result)
	if err != nil {
		t.Fatal(err)
	}
	if len(checkpoint.Payload) > 512 {
		t.Fatalf("model-visible checkpoint bytes=%d want <=512", len(checkpoint.Payload))
	}
	var page ToolResultPage
	if err := json.Unmarshal(result.Output, &page); err != nil {
		t.Fatal(err)
	}
	if page.NextOffset != len([]byte(page.Content)) || page.NextOffset <= 0 || page.Complete {
		t.Fatalf("bounded page=%+v", page)
	}
}

func TestReadToolResultActionPagesReconstructExactBodyWithinVisibleCap(t *testing.T) {
	payload, err := json.Marshal(map[string]string{
		"markdown": strings.Repeat("section <tag>\n证据🛡️\\path\n", 400),
	})
	if err != nil {
		t.Fatal(err)
	}
	envelope := testToolResultEnvelope(payload)
	store := &recordingRangeToolResultStore{envelope: envelope}
	action := NewReadToolResultAction(ToolResultReader{
		Store: store, MaximumPageBytes: 512, MaximumOutputBytes: 512, Now: testToolResultNow,
	})
	var reconstructed strings.Builder
	offset := 0
	for decision := 2; ; decision++ {
		actionID := "decision:" + itoa(decision) + "/action:0"
		input, marshalErr := json.Marshal(readToolResultInput{
			ResultRef: envelope.ResultRef, Offset: offset, MaxBytes: 512,
		})
		if marshalErr != nil {
			t.Fatal(marshalErr)
		}
		result, executeErr := action.Execute(context.Background(), ActionRequest{
			ActionID: actionID, Input: input, UserID: envelope.UserID, ChatID: envelope.ChatID,
			Attempt: Attempt{RunID: envelope.RunID},
		})
		if executeErr != nil || result.Status != ActionSucceeded {
			t.Fatalf("offset=%d result=%+v err=%v", offset, result, executeErr)
		}
		checkpoint, checkpointErr := NewActionResultCheckpoint(decision, 0, actionID, result)
		if checkpointErr != nil {
			t.Fatal(checkpointErr)
		}
		if len(checkpoint.Payload) > 512 {
			t.Fatalf("offset=%d checkpoint bytes=%d want <=512", offset, len(checkpoint.Payload))
		}
		var page ToolResultPage
		if err := json.Unmarshal(result.Output, &page); err != nil {
			t.Fatal(err)
		}
		if page.Offset != offset || page.NextOffset <= offset || !strings.Contains(page.Notice, "Showing bytes") && !page.Complete {
			t.Fatalf("non-contiguous page=%+v", page)
		}
		reconstructed.WriteString(page.Content)
		if page.Complete {
			break
		}
		offset = page.NextOffset
		if decision > 200 {
			t.Fatal("pagination did not terminate")
		}
	}
	if reconstructed.String() != string(payload) || store.fullGets != 0 {
		t.Fatalf("reconstructed_bytes=%d want=%d full_gets=%d", reconstructed.Len(), len(payload), store.fullGets)
	}
}

func TestReadToolResultActionReturnsStableExpiredDomainError(t *testing.T) {
	action := NewReadToolResultAction(ToolResultReader{Store: &recordingToolResultStore{}, MaximumPageBytes: 16, Now: testToolResultNow})
	result, err := action.Execute(context.Background(), ActionRequest{
		ActionID: "decision:2/action:0", Input: json.RawMessage(`{"result_ref":"tr_missing_reference"}`),
		UserID: "user_a", ChatID: "chat_a", Attempt: Attempt{RunID: "run_a"},
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if result.Status != ActionDomainError || result.ErrorCode != "tool_result_expired" {
		t.Fatalf("result = %#v", result)
	}
}

func TestReadToolResultActionRejectsCrossRunRead(t *testing.T) {
	envelope := testToolResultEnvelope([]byte(`{"ok":true}`))
	action := NewReadToolResultAction(ToolResultReader{
		Store: &recordingToolResultStore{envelopes: []ToolResultEnvelope{envelope}}, MaximumPageBytes: 16, Now: testToolResultNow,
	})
	result, err := action.Execute(context.Background(), ActionRequest{
		ActionID: "decision:2/action:0", Input: json.RawMessage(`{"result_ref":"tr_test_reference"}`),
		UserID: "user_a", ChatID: "chat_a", Attempt: Attempt{RunID: "run_b"},
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if result.Status != ActionDomainError || result.ErrorCode != "tool_result_unauthorized" {
		t.Fatalf("result = %#v", result)
	}
}
