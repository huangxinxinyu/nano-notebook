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

type ChatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type ChatRequest struct {
	Model    string
	Messages []ChatMessage
}

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

func (c *BifrostClient) Complete(ctx context.Context, request ChatRequest) (ChatResult, error) {
	body, err := json.Marshal(struct {
		Model               string        `json:"model"`
		Messages            []ChatMessage `json:"messages"`
		Stream              bool          `json:"stream"`
		MaxCompletionTokens int           `json:"max_completion_tokens"`
	}{
		Model:               request.Model,
		Messages:            request.Messages,
		Stream:              false,
		MaxCompletionTokens: c.maxCompletionTokens,
	})
	if err != nil {
		return ChatResult{}, &ModelError{Kind: ErrorInvalidResponse, Err: err}
	}
	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/v1/chat/completions", bytes.NewReader(body))
	if err != nil {
		return ChatResult{}, &ModelError{Kind: ErrorInvalidResponse, Err: err}
	}
	httpRequest.Header.Set("Content-Type", "application/json")
	response, err := c.httpClient.Do(httpRequest)
	if err != nil {
		kind := ErrorUnavailable
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
			kind = ErrorTimeout
		}
		return ChatResult{}, &ModelError{Kind: kind, Err: err}
	}
	defer response.Body.Close()

	const maxResponseBytes = 2 << 20
	responseBody, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes+1))
	if err != nil {
		return ChatResult{}, &ModelError{Kind: ErrorInvalidResponse, Err: err}
	}
	if len(responseBody) > maxResponseBytes {
		return ChatResult{}, &ModelError{Kind: ErrorInvalidResponse, Err: errors.New("Bifrost response too large")}
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return ChatResult{}, &ModelError{Kind: ErrorUnavailable, Err: fmt.Errorf("Bifrost status %d", response.StatusCode)}
	}
	var decoded struct {
		Choices []struct {
			Message struct {
				Role    string `json:"role"`
				Content string `json:"content"`
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
		return ChatResult{}, &ModelError{Kind: ErrorInvalidResponse, Err: err}
	}
	for _, choice := range decoded.Choices {
		if choice.Message.Role == "assistant" && strings.TrimSpace(choice.Message.Content) != "" {
			return ChatResult{
				Text:             choice.Message.Content,
				FinishReason:     choice.FinishReason,
				PromptTokens:     choiceUsage(decoded.Usage.PromptTokens),
				CompletionTokens: choiceUsage(decoded.Usage.CompletionTokens),
				TotalTokens:      choiceUsage(decoded.Usage.TotalTokens),
			}, nil
		}
	}
	return ChatResult{}, &ModelError{Kind: ErrorInvalidResponse, Err: errors.New("Bifrost response has no assistant content")}
}

func choiceUsage(value *int) *int {
	if value == nil || *value < 0 {
		return nil
	}
	copy := *value
	return &copy
}
