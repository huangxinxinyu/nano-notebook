package models

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestBifrostClaimSupportVerifierUsesIndependentStrictSchema(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			t.Fatalf("path=%s", r.URL.Path)
		}
		var body struct {
			Messages []struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if len(body.Messages) != 2 || body.Messages[0].Role != "system" || body.Messages[1].Role != "user" ||
			body.Messages[1].Content == "" {
			t.Fatalf("request=%+v", body)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"{\"claims\":[{\"ordinal\":0,\"supported\":true}]}"},"finish_reason":"stop"}]}`))
	}))
	defer server.Close()
	outcome, err := NewBifrostClient(server.URL, server.Client(), 2048).VerifyClaimSupport(context.Background(), ClaimSupportRequest{
		Model: "verifier", PromptVersion: "claim-support-v1",
		Claims: []ClaimSupportInput{{Ordinal: 0, Text: "The launch is 20 July.", Evidence: []ClaimEvidence{{
			SourceID: "src", RevisionID: "evr", UnitID: "unit", StartRune: 0, EndRune: 27, Text: "The launch date is 20 July.",
		}}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(outcome.Verdicts) != 1 || outcome.Verdicts[0] != (ClaimSupportVerdict{Ordinal: 0, Supported: true}) {
		t.Fatalf("outcome=%+v", outcome)
	}
}

func TestBifrostClaimSupportVerifierRejectsMissingDuplicateAndExpandedVerdicts(t *testing.T) {
	for _, response := range []string{
		`{"claims":[]}`,
		`{"claims":[{"ordinal":0,"supported":true},{"ordinal":0,"supported":true}]}`,
		`{"claims":[{"ordinal":1,"supported":true}]}`,
		`{"claims":[{"ordinal":0,"supported":true,"reasoning":"hidden"}]}`,
	} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			encoded, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"message": map[string]any{"role": "assistant", "content": response}, "finish_reason": "stop"}}})
			_, _ = w.Write(encoded)
		}))
		_, err := NewBifrostClient(server.URL, server.Client(), 2048).VerifyClaimSupport(context.Background(), ClaimSupportRequest{
			Model: "verifier", PromptVersion: "claim-support-v1",
			Claims: []ClaimSupportInput{{Ordinal: 0, Text: "claim", Evidence: []ClaimEvidence{{SourceID: "s", RevisionID: "r", UnitID: "u", EndRune: 1, Text: "x"}}}},
		})
		server.Close()
		if err == nil {
			t.Fatalf("accepted verifier response=%s", response)
		}
	}
}
