package models

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
)

func TestBifrostEmbeddingReturnsProviderNeutralOrderedVectors(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/embeddings" || r.Header.Get("X-Request-ID") == "" {
			t.Fatalf("request=%s %s request-id=%q", r.Method, r.URL.Path, r.Header.Get("X-Request-ID"))
		}
		var request struct {
			Model      string   `json:"model"`
			Input      []string `json:"input"`
			Dimensions int      `json:"dimensions"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatal(err)
		}
		if request.Model != "openai/text-embedding-3-small" || !reflect.DeepEqual(request.Input, []string{"first", "second"}) || request.Dimensions != 3 {
			t.Fatalf("embedding request=%+v", request)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"provider":"openai","model":"text-embedding-3-small",
			"data":[{"index":1,"embedding":[0,1,0]},{"index":0,"embedding":[1,0,0]}],
			"usage":{"prompt_tokens":4,"total_tokens":4},"cost":0.0001,"cost_currency":"USD","cost_source":"provider"
		}`))
	}))
	defer server.Close()
	client := NewBifrostClient(server.URL, server.Client(), 128)
	outcome, err := client.Embed(context.Background(), EmbeddingRequest{
		Model: "openai/text-embedding-3-small", Inputs: []string{"first", "second"}, Dimensions: 3,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(outcome.Vectors, [][]float32{{1, 0, 0}, {0, 1, 0}}) || outcome.Metadata.Provider != "openai" ||
		outcome.Metadata.Model != "text-embedding-3-small" || outcome.Metadata.InputTokens == nil || *outcome.Metadata.InputTokens != 4 || !outcome.Metadata.Cost.Known {
		t.Fatalf("embedding outcome=%+v", outcome)
	}
}

func TestBifrostRerankerReturnsCandidateIdentitiesWithoutProviderDocuments(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/rerank" {
			t.Fatalf("request=%s %s", r.Method, r.URL.Path)
		}
		var request struct {
			Model           string `json:"model"`
			Query           string `json:"query"`
			TopN            int    `json:"top_n"`
			ReturnDocuments bool   `json:"return_documents"`
			Documents       []struct {
				ID   string `json:"id"`
				Text string `json:"text"`
			} `json:"documents"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatal(err)
		}
		if request.Model != "cohere/rerank-v3.5" || request.Query != "evidence" || request.TopN != 2 || request.ReturnDocuments ||
			len(request.Documents) != 2 || request.Documents[0].ID != "unit_a" {
			t.Fatalf("rerank request=%+v", request)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"model":"rerank-v3.5","results":[{"index":1,"relevance_score":0.9},{"index":0,"relevance_score":0.4}],
			"usage":{"prompt_tokens":8,"total_tokens":8},"extra_fields":{"provider":"cohere"}
		}`))
	}))
	defer server.Close()
	client := NewBifrostClient(server.URL, server.Client(), 128)
	outcome, err := client.Rerank(context.Background(), RerankRequest{
		Model: "cohere/rerank-v3.5", Query: "evidence",
		Candidates: []RerankCandidate{{ID: "unit_a", Text: "first"}, {ID: "unit_b", Text: "second"}}, TopN: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(outcome.CandidateIDs, []string{"unit_b", "unit_a"}) || outcome.Metadata.Provider != "cohere" {
		t.Fatalf("rerank outcome=%+v", outcome)
	}
}

func TestBifrostCapabilitiesRejectMalformedProviderResults(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/v1/embeddings" {
			_, _ = w.Write([]byte(`{"data":[{"index":0,"embedding":[1,2]}]}`))
			return
		}
		_, _ = w.Write([]byte(`{"results":[{"index":0,"relevance_score":0.5},{"index":0,"relevance_score":0.4}]}`))
	}))
	defer server.Close()
	client := NewBifrostClient(server.URL, server.Client(), 128)
	if _, err := client.Embed(context.Background(), EmbeddingRequest{Model: "model", Inputs: []string{"a"}, Dimensions: 3}); err == nil {
		t.Fatal("Embed accepted the wrong dimension")
	}
	if _, err := client.Rerank(context.Background(), RerankRequest{
		Model: "model", Query: "q", Candidates: []RerankCandidate{{ID: "a", Text: "a"}, {ID: "b", Text: "b"}}, TopN: 2,
	}); err == nil {
		t.Fatal("Rerank accepted duplicate result indexes")
	}
}
