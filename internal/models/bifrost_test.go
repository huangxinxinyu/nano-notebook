package models

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestBifrostClientCompletesANonStreamingChat(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/chat/completions" {
			t.Fatalf("request = %s %s, want POST /v1/chat/completions", r.Method, r.URL.Path)
		}
		var request struct {
			Model               string        `json:"model"`
			Messages            []ChatMessage `json:"messages"`
			Stream              bool          `json:"stream"`
			MaxCompletionTokens int           `json:"max_completion_tokens"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatal(err)
		}
		if request.Model != "aliyun/qwen-flash" || request.Stream || request.MaxCompletionTokens != 2048 {
			t.Fatalf("unexpected Bifrost request: %+v", request)
		}
		if len(request.Messages) != 2 || request.Messages[0].Role != "system" || request.Messages[1].Content != "What is a KV cache?" {
			t.Fatalf("unexpected messages: %+v", request.Messages)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"id":"chatcmpl-test",
			"choices":[{"index":0,"message":{"role":"assistant","content":"A KV cache reuses attention keys and values."},"finish_reason":"stop"}],
			"usage":{"prompt_tokens":18,"completion_tokens":9,"total_tokens":27}
		}`))
	}))
	defer server.Close()

	client := NewBifrostClient(server.URL, server.Client(), 2048)
	result, err := client.Complete(context.Background(), ChatRequest{
		Model: "aliyun/qwen-flash",
		Messages: []ChatMessage{
			{Role: "system", Content: "Answer directly."},
			{Role: "user", Content: "What is a KV cache?"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Text != "A KV cache reuses attention keys and values." || result.FinishReason != "stop" {
		t.Fatalf("unexpected result: %+v", result)
	}
	if result.PromptTokens == nil || *result.PromptTokens != 18 || result.CompletionTokens == nil || *result.CompletionTokens != 9 || result.TotalTokens == nil || *result.TotalTokens != 27 {
		t.Fatalf("unexpected usage: %+v", result)
	}
}
