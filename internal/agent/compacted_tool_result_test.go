package agent

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

type recordingCheckpointResultStore struct {
	runID    string
	actionID string
	result   ActionResult
	err      error
}

func (s *recordingCheckpointResultStore) GetActionResult(_ context.Context, runID, actionID string) (ActionResult, error) {
	s.runID = runID
	s.actionID = actionID
	return s.result, s.err
}

func TestCompactedToolResultReaderPagesInlineCheckpointOutput(t *testing.T) {
	store := &recordingCheckpointResultStore{result: ActionResult{
		Status: ActionSucceeded,
		Output: json.RawMessage(`{"markdown":"alpha beta gamma"}`),
	}}
	reader := CompactedToolResultReader{Store: store, MaximumPageBytes: 12}
	ref := "run:run_a/checkpoint:decision:4/action:2"
	scope := ToolResultScope{UserID: "user_a", ChatID: "chat_a", RunID: "run_a"}

	var body []byte
	offset := 0
	for {
		page, err := reader.Read(context.Background(), scope, ref, offset, 12)
		if err != nil {
			t.Fatal(err)
		}
		body = append(body, page.Content...)
		if page.Complete {
			break
		}
		if page.ResultRef != ref || page.NextOffset <= offset {
			t.Fatalf("invalid continuation page: %#v", page)
		}
		offset = page.NextOffset
	}

	if got, want := string(body), string(store.result.Output); got != want {
		t.Fatalf("rehydrated body=%q want=%q", got, want)
	}
	if store.runID != "run_a" || store.actionID != "decision:4/action:2" {
		t.Fatalf("checkpoint lookup=%q/%q", store.runID, store.actionID)
	}
}

func TestCompactedToolResultReaderFollowsExternalizedCheckpointOutput(t *testing.T) {
	now := time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)
	raw := json.RawMessage(`{"markdown":"full externalized evidence"}`)
	redis := &recordingToolResultStore{envelopes: []ToolResultEnvelope{{
		SchemaVersion: 1, ResultRef: "tr_externalized_reference", UserID: "user_a", ChatID: "chat_a", RunID: "run_a",
		ActionID: "decision:4/action:2", ToolName: "read_url", MediaType: "application/json", Encoding: "json",
		ResultBytes: len(raw), SHA256: hashPayload(raw), CreatedAt: now, ExpiresAt: now.Add(30 * time.Minute), Body: raw,
	}}}
	projection, err := json.Marshal(ToolResultProjection{
		ActionID: "decision:4/action:2", ContentState: ToolResultExternalized,
		ResultRef: "tr_externalized_reference", ResultBytes: len(raw), SHA256: hashPayload(raw),
	})
	if err != nil {
		t.Fatal(err)
	}
	checkpoints := &recordingCheckpointResultStore{result: ActionResult{Status: ActionSucceeded, Output: projection}}
	reader := CompactedToolResultReader{
		Store:            checkpoints,
		Externalized:     ToolResultReader{Store: redis, MaximumPageBytes: 64, Now: func() time.Time { return now }},
		MaximumPageBytes: 64,
	}
	ref := "run:run_a/checkpoint:decision:4/action:2"

	page, err := reader.Read(context.Background(), ToolResultScope{
		UserID: "user_a", ChatID: "chat_a", RunID: "run_a",
	}, ref, 0, 64)
	if err != nil {
		t.Fatal(err)
	}
	if page.Content != string(raw) || page.ResultRef != ref || !page.Complete {
		t.Fatalf("rehydrated externalized page=%#v", page)
	}
}
