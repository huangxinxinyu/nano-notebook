package models

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestBifrostClientReturnsANonStreamingFinalDecision(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/chat/completions" {
			t.Fatalf("request = %s %s, want POST /v1/chat/completions", r.Method, r.URL.Path)
		}
		var request struct {
			Model               string         `json:"model"`
			Messages            []ModelMessage `json:"messages"`
			Stream              bool           `json:"stream"`
			MaxCompletionTokens int            `json:"max_completion_tokens"`
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
	decision, err := client.Decide(context.Background(), ModelRequest{
		Model: "aliyun/qwen-flash",
		Messages: []ModelMessage{
			{Role: "system", Content: "Answer directly."},
			{Role: "user", Content: "What is a KV cache?"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if decision.Final == nil || decision.Final.Text != "A KV cache reuses attention keys and values." || decision.Proposal != nil {
		t.Fatalf("final decision = %+v err=%v", decision, err)
	}
}

func TestBifrostClientEncodesDefinitionsAndDecodesOrderedActionProposals(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Tools []struct {
				Type     string `json:"type"`
				Function struct {
					Name        string          `json:"name"`
					Description string          `json:"description"`
					Parameters  json.RawMessage `json:"parameters"`
				} `json:"function"`
			} `json:"tools"`
			ToolChoice string `json:"tool_choice"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatal(err)
		}
		if len(request.Tools) != 2 || request.ToolChoice != "auto" || request.Tools[0].Type != "function" || request.Tools[0].Function.Name != "calculate" || request.Tools[1].Function.Name != "current_time" {
			t.Fatalf("encoded tools = %+v choice=%q", request.Tools, request.ToolChoice)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"choices":[{"message":{"role":"assistant","content":null,"tool_calls":[
				{"id":"provider-call-a","type":"function","function":{"name":"current_time","arguments":"{\"time_zone\":\"Asia/Shanghai\"}"}},
				{"id":"provider-call-b","type":"function","function":{"name":"calculate","arguments":"{\"operation\":\"subtract\",\"operands\":[\"12.5\",\"3.2\"]}"}}
			]},"finish_reason":"tool_calls"}]
		}`))
	}))
	defer server.Close()

	client := NewBifrostClient(server.URL, server.Client(), 2048)
	decision, err := client.Decide(context.Background(), ModelRequest{
		Model:    "aliyun/qwen-flash",
		Messages: []ModelMessage{{Role: RoleUser, Content: "Compare time and calculate."}},
		ActionDefinitions: []ActionDefinition{
			{Name: "calculate", Description: "Perform bounded decimal arithmetic.", InputSchema: json.RawMessage(`{"type":"object"}`)},
			{Name: "current_time", Description: "Read the current time.", InputSchema: json.RawMessage(`{"type":"object"}`)},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if decision.Proposal == nil || decision.Final != nil || len(decision.Proposal.Actions) != 2 {
		t.Fatalf("decision = %+v", decision)
	}
	first, second := decision.Proposal.Actions[0], decision.Proposal.Actions[1]
	if first.Name != "current_time" || string(first.Input) != `{"time_zone":"Asia/Shanghai"}` || second.Name != "calculate" || string(second.Input) != `{"operation":"subtract","operands":["12.5","3.2"]}` {
		t.Fatalf("ordered actions = %+v", decision.Proposal.Actions)
	}
}

func TestBifrostClientEncodesProposalAndActionResultMessages(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Messages []struct {
				Role       string `json:"role"`
				Content    string `json:"content"`
				ToolCallID string `json:"tool_call_id"`
				ToolCalls  []struct {
					ID       string `json:"id"`
					Type     string `json:"type"`
					Function struct {
						Name      string `json:"name"`
						Arguments string `json:"arguments"`
					} `json:"function"`
				} `json:"tool_calls"`
			} `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatal(err)
		}
		if len(request.Messages) != 4 {
			t.Fatalf("messages = %+v", request.Messages)
		}
		proposal := request.Messages[2]
		if proposal.Role != "assistant" || len(proposal.ToolCalls) != 1 || proposal.ToolCalls[0].ID != "decision:1/action:0" || proposal.ToolCalls[0].Function.Name != "current_time" || proposal.ToolCalls[0].Function.Arguments != `{"time_zone":"UTC"}` {
			t.Fatalf("proposal message = %+v", proposal)
		}
		result := request.Messages[3]
		if result.Role != "tool" || result.ToolCallID != "decision:1/action:0" || result.Content != `{"status":"succeeded","output":{"time_zone":"UTC"}}` {
			t.Fatalf("result message = %+v", result)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"Done."},"finish_reason":"stop"}]}`))
	}))
	defer server.Close()

	client := NewBifrostClient(server.URL, server.Client(), 2048)
	decision, err := client.Decide(context.Background(), ModelRequest{
		Model: "aliyun/qwen-flash",
		Messages: []ModelMessage{
			{Role: RoleSystem, Content: "Use Actions when needed."},
			{Role: RoleUser, Content: "What time is it?"},
			{Role: RoleAssistant, ActionCalls: []ModelActionCall{{ID: "decision:1/action:0", Name: "current_time", Input: json.RawMessage(`{"time_zone":"UTC"}`)}}},
			{Role: RoleAction, ActionCallID: "decision:1/action:0", Content: `{"status":"succeeded","output":{"time_zone":"UTC"}}`},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if decision.Final == nil || decision.Final.Text != "Done." {
		t.Fatalf("decision = %+v", decision)
	}
}

func TestBifrostClientDecodesOneActionProposal(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":null,"tool_calls":[{"id":"provider-only","type":"function","function":{"name":"current_time","arguments":"{}"}}]},"finish_reason":"tool_calls"}]}`))
	}))
	defer server.Close()
	decision, err := NewBifrostClient(server.URL, server.Client(), 2048).Decide(context.Background(), ModelRequest{
		Model: "aliyun/qwen-flash", Messages: []ModelMessage{{Role: RoleUser, Content: "Current time?"}},
	})
	if err != nil || decision.Proposal == nil || len(decision.Proposal.Actions) != 1 || decision.Proposal.Actions[0].Name != "current_time" || string(decision.Proposal.Actions[0].Input) != `{}` {
		t.Fatalf("one Action decision = %+v err=%v", decision, err)
	}
}

func TestBifrostClientRejectsInvalidDecisions(t *testing.T) {
	tests := []struct {
		name     string
		response string
	}{
		{name: "malformed arguments", response: `{"choices":[{"message":{"role":"assistant","content":null,"tool_calls":[{"id":"bad","type":"function","function":{"name":"calculate","arguments":"{"}}]}}]}`},
		{name: "both variants", response: `{"choices":[{"message":{"role":"assistant","content":"text","tool_calls":[{"id":"both","type":"function","function":{"name":"current_time","arguments":"{}"}}]}}]}`},
		{name: "neither variant", response: `{"choices":[{"message":{"role":"assistant","content":null}}]}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = w.Write([]byte(tt.response))
			}))
			defer server.Close()
			_, err := NewBifrostClient(server.URL, server.Client(), 2048).Decide(context.Background(), ModelRequest{
				Model: "aliyun/qwen-flash", Messages: []ModelMessage{{Role: RoleUser, Content: "Decide."}},
			})
			requireModelErrorKind(t, err, ErrorInvalidResponse)
		})
	}
}

func TestBifrostClientRejectsUnsupportedMessageRoleBeforeRequest(t *testing.T) {
	called := false
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called = true }))
	defer server.Close()
	_, err := NewBifrostClient(server.URL, server.Client(), 2048).Decide(context.Background(), ModelRequest{
		Model: "aliyun/qwen-flash", Messages: []ModelMessage{{Role: ModelRole("developer"), Content: "unsupported"}},
	})
	requireModelErrorKind(t, err, ErrorInvalidResponse)
	if called {
		t.Fatal("unsupported role reached Bifrost")
	}
}

func TestBifrostClientRejectsOversizedResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(bytes.Repeat([]byte("x"), (2<<20)+1))
	}))
	defer server.Close()
	_, err := NewBifrostClient(server.URL, server.Client(), 2048).Decide(context.Background(), ModelRequest{
		Model: "aliyun/qwen-flash", Messages: []ModelMessage{{Role: RoleUser, Content: "Too large."}},
	})
	requireModelErrorKind(t, err, ErrorInvalidResponse)
}

func TestBifrostClientMapsNonSuccessStatusToUnavailable(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "provider unavailable", http.StatusServiceUnavailable)
	}))
	defer server.Close()
	_, err := NewBifrostClient(server.URL, server.Client(), 2048).Decide(context.Background(), ModelRequest{
		Model: "aliyun/qwen-flash", Messages: []ModelMessage{{Role: RoleUser, Content: "Unavailable."}},
	})
	requireModelErrorKind(t, err, ErrorUnavailable)
}

func requireModelErrorKind(t *testing.T, err error, want ErrorKind) {
	t.Helper()
	var modelErr *ModelError
	if !errors.As(err, &modelErr) || modelErr.Kind != want {
		t.Fatalf("error = %v, want ModelError kind %q", err, want)
	}
}
