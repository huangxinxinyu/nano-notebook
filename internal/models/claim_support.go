package models

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"unicode/utf8"
)

type ClaimEvidence struct {
	SourceID   string `json:"source_id"`
	RevisionID string `json:"evidence_revision_id"`
	UnitID     string `json:"unit_id"`
	StartRune  int    `json:"start_rune"`
	EndRune    int    `json:"end_rune"`
	Text       string `json:"text"`
}

type ClaimSupportInput struct {
	Ordinal  int             `json:"ordinal"`
	Text     string          `json:"text"`
	Evidence []ClaimEvidence `json:"evidence"`
}

type ClaimSupportRequest struct {
	Model         string
	PromptVersion string
	Claims        []ClaimSupportInput
}

type ClaimSupportVerdict struct {
	Ordinal   int  `json:"ordinal"`
	Supported bool `json:"supported"`
}

type ClaimSupportOutcome struct {
	Verdicts []ClaimSupportVerdict
	Metadata CapabilityMetadata
}

func (c *BifrostClient) VerifyClaimSupport(ctx context.Context, request ClaimSupportRequest) (ClaimSupportOutcome, error) {
	if strings.TrimSpace(request.Model) == "" || strings.TrimSpace(request.PromptVersion) == "" || len(request.Claims) == 0 || len(request.Claims) > 64 {
		return ClaimSupportOutcome{}, &ModelError{Kind: ErrorInvalidResponse, Err: errors.New("invalid Claim Support request")}
	}
	for ordinal, claim := range request.Claims {
		if claim.Ordinal != ordinal || strings.TrimSpace(claim.Text) == "" || !utf8.ValidString(claim.Text) || len(claim.Evidence) == 0 || len(claim.Evidence) > 8 {
			return ClaimSupportOutcome{}, &ModelError{Kind: ErrorInvalidResponse, Err: errors.New("invalid Claim Support claim")}
		}
		for _, evidence := range claim.Evidence {
			if strings.TrimSpace(evidence.SourceID) == "" || strings.TrimSpace(evidence.RevisionID) == "" || strings.TrimSpace(evidence.UnitID) == "" ||
				evidence.StartRune < 0 || evidence.EndRune <= evidence.StartRune || strings.TrimSpace(evidence.Text) == "" || !utf8.ValidString(evidence.Text) || utf8.RuneCountInString(evidence.Text) > 8000 {
				return ClaimSupportOutcome{}, &ModelError{Kind: ErrorInvalidResponse, Err: errors.New("invalid Claim Support evidence")}
			}
		}
	}
	payload, err := json.Marshal(struct {
		PromptVersion string              `json:"prompt_version"`
		Claims        []ClaimSupportInput `json:"claims"`
	}{request.PromptVersion, request.Claims})
	if err != nil {
		return ClaimSupportOutcome{}, err
	}
	system := "Independently verify whether each claim is fully entailed by only its supplied evidence. Return only JSON matching {\"claims\":[{\"ordinal\":integer,\"supported\":boolean}]}. Return every ordinal exactly once and no explanation or reasoning. Prompt version: " + request.PromptVersion
	body, err := json.Marshal(struct {
		Model    string `json:"model"`
		Messages []struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"messages"`
		Stream              bool `json:"stream"`
		MaxCompletionTokens int  `json:"max_completion_tokens"`
	}{
		Model: request.Model,
		Messages: []struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		}{{Role: "system", Content: system}, {Role: "user", Content: string(payload)}},
		Stream: false, MaxCompletionTokens: c.maxCompletionTokens,
	})
	if err != nil {
		return ClaimSupportOutcome{}, err
	}
	responseBody, latency, err := c.capabilityRequest(ctx, "/v1/chat/completions", body)
	if err != nil {
		return ClaimSupportOutcome{}, err
	}
	var decoded struct {
		Provider string `json:"provider"`
		Model    string `json:"model"`
		Choices  []struct {
			Message struct {
				Role    string  `json:"role"`
				Content *string `json:"content"`
			} `json:"message"`
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
	}
	if json.Unmarshal(responseBody, &decoded) != nil || len(decoded.Choices) != 1 || decoded.Choices[0].Message.Role != "assistant" ||
		decoded.Choices[0].Message.Content == nil || decoded.Choices[0].FinishReason == "tool_calls" {
		return ClaimSupportOutcome{}, &ModelError{Kind: ErrorInvalidResponse, Err: errors.New("invalid Claim Support response")}
	}
	var result struct {
		Claims []ClaimSupportVerdict `json:"claims"`
	}
	decoder := json.NewDecoder(bytes.NewBufferString(*decoded.Choices[0].Message.Content))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&result); err != nil {
		return ClaimSupportOutcome{}, &ModelError{Kind: ErrorInvalidResponse, Err: errors.New("invalid Claim Support verdict")}
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) || len(result.Claims) != len(request.Claims) {
		return ClaimSupportOutcome{}, &ModelError{Kind: ErrorInvalidResponse, Err: errors.New("invalid Claim Support verdict set")}
	}
	seen := make(map[int]struct{}, len(result.Claims))
	for _, verdict := range result.Claims {
		if verdict.Ordinal < 0 || verdict.Ordinal >= len(request.Claims) {
			return ClaimSupportOutcome{}, &ModelError{Kind: ErrorInvalidResponse, Err: errors.New("Claim Support expanded the claim set")}
		}
		if _, duplicate := seen[verdict.Ordinal]; duplicate {
			return ClaimSupportOutcome{}, &ModelError{Kind: ErrorInvalidResponse, Err: errors.New("duplicate Claim Support verdict")}
		}
		seen[verdict.Ordinal] = struct{}{}
	}
	ordered := make([]ClaimSupportVerdict, len(result.Claims))
	for _, verdict := range result.Claims {
		ordered[verdict.Ordinal] = verdict
	}
	return ClaimSupportOutcome{Verdicts: ordered, Metadata: CapabilityMetadata{
		RequestedModel: request.Model, Provider: strings.TrimSpace(decoded.Provider), Model: strings.TrimSpace(decoded.Model), Latency: latency,
	}}, nil
}
