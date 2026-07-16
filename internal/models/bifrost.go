package models

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
)

type ModelRole string

const (
	RoleSystem    ModelRole = "system"
	RoleUser      ModelRole = "user"
	RoleAssistant ModelRole = "assistant"
	RoleAction    ModelRole = "action"
)

type ModelMessage struct {
	Role         ModelRole         `json:"role"`
	Content      string            `json:"content"`
	ActionCalls  []ModelActionCall `json:"-"`
	ActionCallID string            `json:"-"`
}

type ModelActionCall struct {
	ID    string
	Name  string
	Input json.RawMessage
}

type ActionDefinition struct {
	Name        string
	Description string
	InputSchema json.RawMessage
}

type ModelRequest struct {
	Model             string
	Messages          []ModelMessage
	ActionDefinitions []ActionDefinition
}

type ChatMessage = ModelMessage
type ChatRequest = ModelRequest

type ChatResult struct {
	Text             string
	FinishReason     string
	PromptTokens     *int
	CompletionTokens *int
	TotalTokens      *int
}

type ModelClient interface {
	Complete(context.Context, ChatRequest) (ChatResult, error)
}

type ErrorKind string

const (
	ErrorTimeout         ErrorKind = "model_timeout"
	ErrorUnavailable     ErrorKind = "model_unavailable"
	ErrorInvalidResponse ErrorKind = "model_invalid_response"
)

type ModelError struct {
	Kind ErrorKind
	Err  error
}

func (e *ModelError) Error() string {
	return fmt.Sprintf("%s: %v", e.Kind, e.Err)
}

func (e *ModelError) Unwrap() error {
	return e.Err
}

type BifrostClient struct {
	baseURL             string
	httpClient          *http.Client
	maxCompletionTokens int
}

func NewBifrostClient(baseURL string, httpClient *http.Client, maxCompletionTokens int) *BifrostClient {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	if maxCompletionTokens <= 0 {
		maxCompletionTokens = 2048
	}
	return &BifrostClient{
		baseURL:             strings.TrimRight(baseURL, "/"),
		httpClient:          httpClient,
		maxCompletionTokens: maxCompletionTokens,
	}
}

func (c *BifrostClient) Decide(ctx context.Context, request ModelRequest) (ModelDecision, error) {
	decision, _, err := c.request(ctx, request)
	if err != nil {
		return ModelDecision{}, err
	}
	return decision, nil
}

func (c *BifrostClient) Complete(ctx context.Context, request ChatRequest) (ChatResult, error) {
	decision, metadata, err := c.request(ctx, request)
	if err != nil {
		return ChatResult{}, err
	}
	if decision.Final == nil {
		return ChatResult{}, &ModelError{Kind: ErrorInvalidResponse, Err: errors.New("legacy completion received Action proposals")}
	}
	return ChatResult{
		Text:             decision.Final.Text,
		FinishReason:     metadata.finishReason,
		PromptTokens:     choiceUsage(metadata.promptTokens),
		CompletionTokens: choiceUsage(metadata.completionTokens),
		TotalTokens:      choiceUsage(metadata.totalTokens),
	}, nil
}

type responseMetadata struct {
	finishReason     string
	promptTokens     *int
	completionTokens *int
	totalTokens      *int
}

func (c *BifrostClient) request(ctx context.Context, request ModelRequest) (ModelDecision, responseMetadata, error) {
	type providerToolCall struct {
		ID       string `json:"id"`
		Type     string `json:"type"`
		Function struct {
			Name      string `json:"name"`
			Arguments string `json:"arguments"`
		} `json:"function"`
	}
	type providerMessage struct {
		Role       string             `json:"role"`
		Content    string             `json:"content,omitempty"`
		ToolCalls  []providerToolCall `json:"tool_calls,omitempty"`
		ToolCallID string             `json:"tool_call_id,omitempty"`
	}
	messages := make([]providerMessage, 0, len(request.Messages))
	for _, message := range request.Messages {
		provider := providerMessage{Content: message.Content}
		switch message.Role {
		case RoleSystem, RoleUser:
			if strings.TrimSpace(message.Content) == "" || len(message.ActionCalls) > 0 || message.ActionCallID != "" {
				return ModelDecision{}, responseMetadata{}, &ModelError{Kind: ErrorInvalidResponse, Err: errors.New("invalid text model message")}
			}
			provider.Role = string(message.Role)
		case RoleAssistant:
			provider.Role = "assistant"
			if len(message.ActionCalls) == 0 {
				if strings.TrimSpace(message.Content) == "" || message.ActionCallID != "" {
					return ModelDecision{}, responseMetadata{}, &ModelError{Kind: ErrorInvalidResponse, Err: errors.New("invalid assistant model message")}
				}
				break
			}
			if strings.TrimSpace(message.Content) != "" || message.ActionCallID != "" {
				return ModelDecision{}, responseMetadata{}, &ModelError{Kind: ErrorInvalidResponse, Err: errors.New("assistant proposal message has conflicting fields")}
			}
			provider.Content = ""
			provider.ToolCalls = make([]providerToolCall, 0, len(message.ActionCalls))
			for _, call := range message.ActionCalls {
				var input map[string]json.RawMessage
				if strings.TrimSpace(call.ID) == "" || strings.TrimSpace(call.Name) == "" || json.Unmarshal(call.Input, &input) != nil || input == nil {
					return ModelDecision{}, responseMetadata{}, &ModelError{Kind: ErrorInvalidResponse, Err: errors.New("invalid assistant Action call")}
				}
				var canonical bytes.Buffer
				if err := json.Compact(&canonical, call.Input); err != nil {
					return ModelDecision{}, responseMetadata{}, &ModelError{Kind: ErrorInvalidResponse, Err: err}
				}
				var toolCall providerToolCall
				toolCall.ID = call.ID
				toolCall.Type = "function"
				toolCall.Function.Name = call.Name
				toolCall.Function.Arguments = canonical.String()
				provider.ToolCalls = append(provider.ToolCalls, toolCall)
			}
		case RoleAction:
			if strings.TrimSpace(message.ActionCallID) == "" || strings.TrimSpace(message.Content) == "" || len(message.ActionCalls) > 0 {
				return ModelDecision{}, responseMetadata{}, &ModelError{Kind: ErrorInvalidResponse, Err: errors.New("invalid Action result message")}
			}
			provider.Role = "tool"
			provider.ToolCallID = message.ActionCallID
		default:
			return ModelDecision{}, responseMetadata{}, &ModelError{Kind: ErrorInvalidResponse, Err: errors.New("unsupported model message role")}
		}
		messages = append(messages, provider)
	}

	type providerTool struct {
		Type     string `json:"type"`
		Function struct {
			Name        string          `json:"name"`
			Description string          `json:"description"`
			Parameters  json.RawMessage `json:"parameters"`
		} `json:"function"`
	}
	tools := make([]providerTool, 0, len(request.ActionDefinitions))
	for _, definition := range request.ActionDefinitions {
		var schema map[string]json.RawMessage
		if strings.TrimSpace(definition.Name) == "" || strings.TrimSpace(definition.Description) == "" || json.Unmarshal(definition.InputSchema, &schema) != nil || schema == nil {
			return ModelDecision{}, responseMetadata{}, &ModelError{Kind: ErrorInvalidResponse, Err: errors.New("invalid Action definition")}
		}
		var tool providerTool
		tool.Type = "function"
		tool.Function.Name = definition.Name
		tool.Function.Description = definition.Description
		tool.Function.Parameters = definition.InputSchema
		tools = append(tools, tool)
	}
	toolChoice := ""
	if len(tools) > 0 {
		toolChoice = "auto"
	}
	body, err := json.Marshal(struct {
		Model               string            `json:"model"`
		Messages            []providerMessage `json:"messages"`
		Stream              bool              `json:"stream"`
		MaxCompletionTokens int               `json:"max_completion_tokens"`
		Tools               []providerTool    `json:"tools,omitempty"`
		ToolChoice          string            `json:"tool_choice,omitempty"`
	}{
		Model:               request.Model,
		Messages:            messages,
		Stream:              false,
		MaxCompletionTokens: c.maxCompletionTokens,
		Tools:               tools,
		ToolChoice:          toolChoice,
	})
	if err != nil {
		return ModelDecision{}, responseMetadata{}, &ModelError{Kind: ErrorInvalidResponse, Err: err}
	}
	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/v1/chat/completions", bytes.NewReader(body))
	if err != nil {
		return ModelDecision{}, responseMetadata{}, &ModelError{Kind: ErrorInvalidResponse, Err: err}
	}
	httpRequest.Header.Set("Content-Type", "application/json")
	response, err := c.httpClient.Do(httpRequest)
	if err != nil {
		kind := ErrorUnavailable
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
			kind = ErrorTimeout
		}
		return ModelDecision{}, responseMetadata{}, &ModelError{Kind: kind, Err: err}
	}
	defer response.Body.Close()

	const maxResponseBytes = 2 << 20
	responseBody, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes+1))
	if err != nil {
		return ModelDecision{}, responseMetadata{}, &ModelError{Kind: ErrorInvalidResponse, Err: err}
	}
	if len(responseBody) > maxResponseBytes {
		return ModelDecision{}, responseMetadata{}, &ModelError{Kind: ErrorInvalidResponse, Err: errors.New("Bifrost response too large")}
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return ModelDecision{}, responseMetadata{}, &ModelError{Kind: ErrorUnavailable, Err: fmt.Errorf("Bifrost status %d", response.StatusCode)}
	}
	var decoded struct {
		Choices []struct {
			Message struct {
				Role      string  `json:"role"`
				Content   *string `json:"content"`
				ToolCalls []struct {
					ID       string `json:"id"`
					Type     string `json:"type"`
					Function struct {
						Name      string `json:"name"`
						Arguments string `json:"arguments"`
					} `json:"function"`
				} `json:"tool_calls"`
			} `json:"message"`
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
		Usage struct {
			PromptTokens     *int `json:"prompt_tokens"`
			CompletionTokens *int `json:"completion_tokens"`
			TotalTokens      *int `json:"total_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(responseBody, &decoded); err != nil {
		return ModelDecision{}, responseMetadata{}, &ModelError{Kind: ErrorInvalidResponse, Err: err}
	}
	for _, choice := range decoded.Choices {
		if choice.Message.Role != "assistant" {
			continue
		}
		decision := ModelDecision{}
		if choice.Message.Content != nil && strings.TrimSpace(*choice.Message.Content) != "" {
			decision.Final = &FinalDraft{Text: *choice.Message.Content}
		}
		if len(choice.Message.ToolCalls) > 0 {
			actions := make([]ActionProposal, 0, len(choice.Message.ToolCalls))
			for _, call := range choice.Message.ToolCalls {
				var input map[string]json.RawMessage
				if call.Type != "function" || strings.TrimSpace(call.Function.Name) == "" || json.Unmarshal([]byte(call.Function.Arguments), &input) != nil || input == nil {
					return ModelDecision{}, responseMetadata{}, &ModelError{Kind: ErrorInvalidResponse, Err: errors.New("invalid Bifrost tool call")}
				}
				var canonical bytes.Buffer
				if err := json.Compact(&canonical, []byte(call.Function.Arguments)); err != nil {
					return ModelDecision{}, responseMetadata{}, &ModelError{Kind: ErrorInvalidResponse, Err: err}
				}
				actions = append(actions, ActionProposal{Name: call.Function.Name, Input: json.RawMessage(canonical.Bytes())})
			}
			decision.Proposal = &ActionProposalBatch{Actions: actions}
		}
		if err := decision.Validate(); err != nil {
			return ModelDecision{}, responseMetadata{}, &ModelError{Kind: ErrorInvalidResponse, Err: err}
		}
		return decision, responseMetadata{
			finishReason:     choice.FinishReason,
			promptTokens:     decoded.Usage.PromptTokens,
			completionTokens: decoded.Usage.CompletionTokens,
			totalTokens:      decoded.Usage.TotalTokens,
		}, nil
	}
	return ModelDecision{}, responseMetadata{}, &ModelError{Kind: ErrorInvalidResponse, Err: errors.New("Bifrost response has no assistant decision")}
}

func choiceUsage(value *int) *int {
	if value == nil || *value < 0 {
		return nil
	}
	copy := *value
	return &copy
}
