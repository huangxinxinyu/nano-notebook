package models

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
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
		if !strings.Contains(body.Messages[1].Content, `"answer":"The launch is 20 July."`) {
			t.Fatalf("full Answer missing from verifier request: %s", body.Messages[1].Content)
		}
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"{\"claims\":[{\"ordinal\":0,\"supported\":true}],\"uncovered_claims\":[]}"},"finish_reason":"stop"}]}`))
	}))
	defer server.Close()
	outcome, err := NewBifrostClient(server.URL, server.Client(), 2048).VerifyClaimSupport(context.Background(), ClaimSupportRequest{
		Model: "verifier", PromptVersion: "claim-support-v1", Answer: "The launch is 20 July.",
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
		`{"claims":[],"uncovered_claims":[]}`,
		`{"claims":[{"ordinal":0,"supported":true},{"ordinal":0,"supported":true}],"uncovered_claims":[]}`,
		`{"claims":[{"ordinal":1,"supported":true}],"uncovered_claims":[]}`,
		`{"claims":[{"ordinal":0,"supported":true,"reasoning":"hidden"}],"uncovered_claims":[]}`,
		`{"claims":[{"ordinal":0,"supported":true}]}`,
	} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			encoded, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"message": map[string]any{"role": "assistant", "content": response}, "finish_reason": "stop"}}})
			_, _ = w.Write(encoded)
		}))
		_, err := NewBifrostClient(server.URL, server.Client(), 2048).VerifyClaimSupport(context.Background(), ClaimSupportRequest{
			Model: "verifier", PromptVersion: "claim-support-v1", Answer: "claim",
			Claims: []ClaimSupportInput{{Ordinal: 0, Text: "claim", Evidence: []ClaimEvidence{{SourceID: "s", RevisionID: "r", UnitID: "u", EndRune: 1, Text: "x"}}}},
		})
		server.Close()
		if err == nil {
			t.Fatalf("accepted verifier response=%s", response)
		}
	}
}

func TestBifrostClaimSupportVerifierReturnsOnlyVerbatimUncoveredMaterialClaims(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"{\"claims\":[{\"ordinal\":0,\"supported\":true}],\"uncovered_claims\":[\"The budget is $5M.\"]}"},"finish_reason":"stop"}]}`))
	}))
	defer server.Close()
	outcome, err := NewBifrostClient(server.URL, server.Client(), 2048).VerifyClaimSupport(context.Background(), ClaimSupportRequest{
		Model: "verifier", PromptVersion: "claim-support-v2", Answer: "The launch is 20 July. The budget is $5M.",
		Claims: []ClaimSupportInput{{Ordinal: 0, Text: "The launch is 20 July.", Evidence: []ClaimEvidence{{SourceID: "s", RevisionID: "r", UnitID: "u", EndRune: 1, Text: "launch"}}}},
	})
	if err != nil || len(outcome.UncoveredClaims) != 1 || outcome.UncoveredClaims[0] != "The budget is $5M." {
		t.Fatalf("outcome=%+v err=%v", outcome, err)
	}
}
