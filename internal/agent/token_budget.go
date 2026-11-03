package agent

import (
	"encoding/json"
	"errors"
	"unicode"

	"github.com/huangxinxinyu/nano-notebook/internal/models"
)

type TokenCountSource string

const (
	TokenCountEstimated TokenCountSource = "estimated"
	TokenCountObserved  TokenCountSource = "provider_observed"
)

type ContextTokenCount struct {
	Tokens       int
	CachedTokens int
	Source       TokenCountSource
}

// EstimateModelRequestTokens is the deterministic fallback used when the
// Provider has not reported usage for this exact context lineage. It counts
// the complete Provider-neutral request envelope, then applies Pi's
// characters/4 fallback with ceiling rather than treating bytes or Message
// count as tokens. CJK text runs near one token per character, so charging it
// at a quarter would start compaction several times too late.
func EstimateModelRequestTokens(request models.ModelRequest) (ContextTokenCount, error) {
	type actionCall struct {
		ID    string          `json:"id"`
		Name  string          `json:"name"`
		Input json.RawMessage `json:"input"`
	}
	type message struct {
		Role         models.ModelRole `json:"role"`
		Content      string           `json:"content,omitempty"`
		ActionCalls  []actionCall     `json:"action_calls,omitempty"`
		ActionCallID string           `json:"action_call_id,omitempty"`
	}
	type definition struct {
		Name        string          `json:"name"`
		Description string          `json:"description"`
		InputSchema json.RawMessage `json:"input_schema"`
	}
	payload := struct {
		Model              string       `json:"model"`
		Messages           []message    `json:"messages"`
		ActionDefinitions  []definition `json:"action_definitions,omitempty"`
		RequiredActionName string       `json:"required_action_name,omitempty"`
		MaxOutputTokens    int          `json:"max_output_tokens"`
	}{Model: request.Model, RequiredActionName: request.RequiredActionName, MaxOutputTokens: request.InvocationPolicy.MaxOutputTokens}
	for _, item := range request.Messages {
		projected := message{Role: item.Role, Content: item.Content, ActionCallID: item.ActionCallID}
		for _, call := range item.ActionCalls {
			if !json.Valid(call.Input) {
				return ContextTokenCount{}, errors.New("invalid Action call input while estimating context")
			}
			projected.ActionCalls = append(projected.ActionCalls, actionCall{ID: call.ID, Name: call.Name, Input: call.Input})
		}
		payload.Messages = append(payload.Messages, projected)
	}
	for _, item := range request.ActionDefinitions {
		if !json.Valid(item.InputSchema) {
			return ContextTokenCount{}, errors.New("invalid Action schema while estimating context")
		}
		payload.ActionDefinitions = append(payload.ActionDefinitions, definition{
			Name: item.Name, Description: item.Description, InputSchema: item.InputSchema,
		})
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return ContextTokenCount{}, err
	}
	characters, cjk := 0, 0
	for _, value := range string(encoded) {
		if unicode.In(value, unicode.Han, unicode.Hiragana, unicode.Katakana, unicode.Hangul) {
			cjk++
		} else {
			characters++
		}
	}
	return ContextTokenCount{Tokens: maxInt(1, (characters+3)/4+cjk), Source: TokenCountEstimated}, nil
}

func maxInt(left, right int) int {
	if left > right {
		return left
	}
	return right
}
