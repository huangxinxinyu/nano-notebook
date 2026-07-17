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
	"time"

	"github.com/google/uuid"
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

func (c *BifrostClient) Decide(ctx context.Context, request ModelRequest) (ModelOutcome, error) {
	return c.request(ctx, request)
}

func (c *BifrostClient) request(ctx context.Context, request ModelRequest) (outcome ModelOutcome, resultErr error) {
	startedAt := time.Now()
	defer func() {
		outcome.Metadata.RequestedModel = request.Model
		outcome.Metadata.Latency = time.Since(startedAt)
		if resultErr != nil {
			outcome.Metadata.ResultKind = modelErrorResultKind(resultErr)
		}
	}()
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
				return ModelOutcome{}, &ModelError{Kind: ErrorInvalidResponse, Err: errors.New("invalid text model message")}
			}
			provider.Role = string(message.Role)
		case RoleAssistant:
			provider.Role = "assistant"
			if len(message.ActionCalls) == 0 {
				if strings.TrimSpace(message.Content) == "" || message.ActionCallID != "" {
					return ModelOutcome{}, &ModelError{Kind: ErrorInvalidResponse, Err: errors.New("invalid assistant model message")}
				}
				break
			}
			if strings.TrimSpace(message.Content) != "" || message.ActionCallID != "" {
				return ModelOutcome{}, &ModelError{Kind: ErrorInvalidResponse, Err: errors.New("assistant proposal message has conflicting fields")}
			}
			provider.Content = ""
			provider.ToolCalls = make([]providerToolCall, 0, len(message.ActionCalls))
			for _, call := range message.ActionCalls {
				var input map[string]json.RawMessage
				if strings.TrimSpace(call.ID) == "" || strings.TrimSpace(call.Name) == "" || json.Unmarshal(call.Input, &input) != nil || input == nil {
					return ModelOutcome{}, &ModelError{Kind: ErrorInvalidResponse, Err: errors.New("invalid assistant Action call")}
				}
				var canonical bytes.Buffer
				if err := json.Compact(&canonical, call.Input); err != nil {
					return ModelOutcome{}, &ModelError{Kind: ErrorInvalidResponse, Err: err}
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
				return ModelOutcome{}, &ModelError{Kind: ErrorInvalidResponse, Err: errors.New("invalid Action result message")}
			}
			provider.Role = "tool"
			provider.ToolCallID = message.ActionCallID
		default:
			return ModelOutcome{}, &ModelError{Kind: ErrorInvalidResponse, Err: errors.New("unsupported model message role")}
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
			return ModelOutcome{}, &ModelError{Kind: ErrorInvalidResponse, Err: errors.New("invalid Action definition")}
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
		return ModelOutcome{}, &ModelError{Kind: ErrorInvalidResponse, Err: err}
	}
	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/v1/chat/completions", bytes.NewReader(body))
	if err != nil {
		return ModelOutcome{}, &ModelError{Kind: ErrorInvalidResponse, Err: err}
	}
	httpRequest.Header.Set("Content-Type", "application/json")
	httpRequest.Header.Set("X-Request-ID", uuid.NewString())
	response, err := c.httpClient.Do(httpRequest)
	if err != nil {
		kind := ErrorUnavailable
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
			kind = ErrorTimeout
		}
		return ModelOutcome{}, &ModelError{Kind: kind, Err: err}
	}
	defer response.Body.Close()

	const maxResponseBytes = 2 << 20
	responseBody, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes+1))
	if err != nil {
		return ModelOutcome{}, &ModelError{Kind: ErrorInvalidResponse, Err: err}
	}
	if len(responseBody) > maxResponseBytes {
		return ModelOutcome{}, &ModelError{Kind: ErrorInvalidResponse, Err: errors.New("Bifrost response too large")}
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return ModelOutcome{}, &ModelError{Kind: ErrorUnavailable, Err: fmt.Errorf("Bifrost status %d", response.StatusCode)}
	}
	var decoded struct {
		Provider string `json:"provider"`
		Model    string `json:"model"`
		Choices  []struct {
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
			PromptTokens     *int64 `json:"prompt_tokens"`
			CompletionTokens *int64 `json:"completion_tokens"`
			TotalTokens      *int64 `json:"total_tokens"`
			PromptDetails    struct {
				CachedTokens *int64 `json:"cached_tokens"`
			} `json:"prompt_tokens_details"`
			CompletionDetails struct {
				ReasoningTokens *int64 `json:"reasoning_tokens"`
			} `json:"completion_tokens_details"`
		} `json:"usage"`
		GatewayRetries   *int64   `json:"gateway_retries"`
		GatewayFallbacks *int64   `json:"gateway_fallbacks"`
		Cost             *float64 `json:"cost"`
		CostCurrency     string   `json:"cost_currency"`
		CostSource       string   `json:"cost_source"`
	}
	if err := json.Unmarshal(responseBody, &decoded); err != nil {
		return ModelOutcome{}, &ModelError{Kind: ErrorInvalidResponse, Err: err}
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
			providerCallIDs := make(map[string]struct{}, len(choice.Message.ToolCalls))
			for _, call := range choice.Message.ToolCalls {
				var input map[string]json.RawMessage
				providerCallID := strings.TrimSpace(call.ID)
				_, duplicateID := providerCallIDs[providerCallID]
				if providerCallID == "" || duplicateID || call.Type != "function" || strings.TrimSpace(call.Function.Name) == "" || json.Unmarshal([]byte(call.Function.Arguments), &input) != nil || input == nil {
					return ModelOutcome{}, &ModelError{Kind: ErrorInvalidResponse, Err: errors.New("invalid Bifrost tool call")}
				}
				providerCallIDs[providerCallID] = struct{}{}
				var canonical bytes.Buffer
				if err := json.Compact(&canonical, []byte(call.Function.Arguments)); err != nil {
					return ModelOutcome{}, &ModelError{Kind: ErrorInvalidResponse, Err: err}
				}
				actions = append(actions, ActionProposal{Name: call.Function.Name, Input: json.RawMessage(canonical.Bytes())})
			}
			decision.Proposal = &ActionProposalBatch{Actions: actions}
		}
		if err := decision.Validate(); err != nil {
			return ModelOutcome{}, &ModelError{Kind: ErrorInvalidResponse, Err: err}
		}
		if decision.Proposal != nil && choice.FinishReason != "tool_calls" {
			return ModelOutcome{}, &ModelError{Kind: ErrorInvalidResponse, Err: errors.New("Bifrost Action proposal has inconsistent finish reason")}
		}
		if decision.Final != nil && (strings.TrimSpace(choice.FinishReason) == "" || choice.FinishReason == "tool_calls") {
			return ModelOutcome{}, &ModelError{Kind: ErrorInvalidResponse, Err: errors.New("Bifrost Final Draft has inconsistent finish reason")}
		}
		resultKind := ModelResultFinalDraft
		if decision.Proposal != nil {
			resultKind = ModelResultActionProposal
		}
		metadata := ModelCallMetadata{
			RequestedModel:   request.Model,
			SelectedProvider: strings.TrimSpace(decoded.Provider),
			SelectedModel:    strings.TrimSpace(decoded.Model),
			ResultKind:       resultKind,
			FinishReason:     choice.FinishReason,
			InputTokens:      decoded.Usage.PromptTokens,
			OutputTokens:     decoded.Usage.CompletionTokens,
			TotalTokens:      decoded.Usage.TotalTokens,
			CachedTokens:     decoded.Usage.PromptDetails.CachedTokens,
			ReasoningTokens:  decoded.Usage.CompletionDetails.ReasoningTokens,
			GatewayRetries:   decoded.GatewayRetries,
			GatewayFallbacks: decoded.GatewayFallbacks,
		}
		if decoded.Cost != nil && strings.TrimSpace(decoded.CostCurrency) != "" && strings.TrimSpace(decoded.CostSource) != "" {
			metadata.Cost = ModelCost{Known: true, Amount: decoded.Cost, Currency: decoded.CostCurrency, Source: decoded.CostSource}
		}
		if err := metadata.Validate(); err != nil {
			return ModelOutcome{}, &ModelError{Kind: ErrorInvalidResponse, Err: err}
		}
		return ModelOutcome{ModelDecision: decision, Metadata: metadata}, nil
	}
	return ModelOutcome{}, &ModelError{Kind: ErrorInvalidResponse, Err: errors.New("Bifrost response has no assistant decision")}
}

func modelErrorResultKind(err error) ModelResultKind {
	var modelErr *ModelError
	if errors.As(err, &modelErr) {
		switch modelErr.Kind {
		case ErrorTimeout:
			return ModelResultTimeout
		case ErrorUnavailable:
			return ModelResultUnavailable
		}
	}
	return ModelResultInvalid
}
