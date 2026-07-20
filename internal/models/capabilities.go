package models

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
)

type CapabilityMetadata struct {
	RequestedModel string
	Provider       string
	Model          string
	InputTokens    *int64
	TotalTokens    *int64
	Latency        time.Duration
	Cost           ModelCost
}

type EmbeddingRequest struct {
	Model      string
	Inputs     []string
	Dimensions int
}

type EmbeddingOutcome struct {
	Vectors  [][]float32
	Metadata CapabilityMetadata
}

type RerankCandidate struct {
	ID   string
	Text string
}

type RerankRequest struct {
	Model      string
	Query      string
	Candidates []RerankCandidate
	TopN       int
}

type RerankOutcome struct {
	CandidateIDs []string
	Metadata     CapabilityMetadata
}

func (c *BifrostClient) Embed(ctx context.Context, request EmbeddingRequest) (EmbeddingOutcome, error) {
	if strings.TrimSpace(request.Model) == "" || len(request.Inputs) == 0 || len(request.Inputs) > 64 || request.Dimensions < 1 || request.Dimensions > 8192 {
		return EmbeddingOutcome{}, &ModelError{Kind: ErrorInvalidResponse, Err: errors.New("invalid embedding request")}
	}
	for _, input := range request.Inputs {
		if strings.TrimSpace(input) == "" || !utf8.ValidString(input) || utf8.RuneCountInString(input) > 32_000 {
			return EmbeddingOutcome{}, &ModelError{Kind: ErrorInvalidResponse, Err: errors.New("invalid embedding input")}
		}
	}
	body, err := json.Marshal(struct {
		Model      string   `json:"model"`
		Input      []string `json:"input"`
		Dimensions int      `json:"dimensions"`
	}{request.Model, request.Inputs, request.Dimensions})
	if err != nil {
		return EmbeddingOutcome{}, err
	}
	responseBody, latency, err := c.capabilityRequest(ctx, "/v1/embeddings", body)
	if err != nil {
		return EmbeddingOutcome{}, err
	}
	var decoded struct {
		Provider string `json:"provider"`
		Model    string `json:"model"`
		Data     []struct {
			Index     int       `json:"index"`
			Embedding []float32 `json:"embedding"`
		} `json:"data"`
		Usage struct {
			PromptTokens *int64 `json:"prompt_tokens"`
			TotalTokens  *int64 `json:"total_tokens"`
		} `json:"usage"`
		Cost         *float64 `json:"cost"`
		CostCurrency string   `json:"cost_currency"`
		CostSource   string   `json:"cost_source"`
	}
	if json.Unmarshal(responseBody, &decoded) != nil || len(decoded.Data) != len(request.Inputs) {
		return EmbeddingOutcome{}, &ModelError{Kind: ErrorInvalidResponse, Err: errors.New("invalid embedding response")}
	}
	vectors := make([][]float32, len(request.Inputs))
	seen := make(map[int]struct{}, len(decoded.Data))
	for _, item := range decoded.Data {
		if item.Index < 0 || item.Index >= len(vectors) || len(item.Embedding) != request.Dimensions || !finiteFloats(item.Embedding) {
			return EmbeddingOutcome{}, &ModelError{Kind: ErrorInvalidResponse, Err: errors.New("invalid embedding vector")}
		}
		if _, duplicate := seen[item.Index]; duplicate {
			return EmbeddingOutcome{}, &ModelError{Kind: ErrorInvalidResponse, Err: errors.New("duplicate embedding index")}
		}
		seen[item.Index] = struct{}{}
		vectors[item.Index] = append([]float32(nil), item.Embedding...)
	}
	metadata, err := capabilityMetadata(request.Model, decoded.Provider, decoded.Model, decoded.Usage.PromptTokens, decoded.Usage.TotalTokens, latency, decoded.Cost, decoded.CostCurrency, decoded.CostSource)
	if err != nil {
		return EmbeddingOutcome{}, &ModelError{Kind: ErrorInvalidResponse, Err: err}
	}
	return EmbeddingOutcome{Vectors: vectors, Metadata: metadata}, nil
}

func (c *BifrostClient) Rerank(ctx context.Context, request RerankRequest) (RerankOutcome, error) {
	if strings.TrimSpace(request.Model) == "" || strings.TrimSpace(request.Query) == "" || !utf8.ValidString(request.Query) ||
		len(request.Candidates) == 0 || len(request.Candidates) > 100 || request.TopN < 1 || request.TopN > len(request.Candidates) {
		return RerankOutcome{}, &ModelError{Kind: ErrorInvalidResponse, Err: errors.New("invalid rerank request")}
	}
	type document struct {
		ID   string `json:"id"`
		Text string `json:"text"`
	}
	documents := make([]document, 0, len(request.Candidates))
	ids := make(map[string]struct{}, len(request.Candidates))
	for _, candidate := range request.Candidates {
		if strings.TrimSpace(candidate.ID) == "" || strings.TrimSpace(candidate.Text) == "" || !utf8.ValidString(candidate.Text) || utf8.RuneCountInString(candidate.Text) > 16_000 {
			return RerankOutcome{}, &ModelError{Kind: ErrorInvalidResponse, Err: errors.New("invalid rerank candidate")}
		}
		if _, duplicate := ids[candidate.ID]; duplicate {
			return RerankOutcome{}, &ModelError{Kind: ErrorInvalidResponse, Err: errors.New("duplicate rerank candidate")}
		}
		ids[candidate.ID] = struct{}{}
		documents = append(documents, document{ID: candidate.ID, Text: candidate.Text})
	}
	body, err := json.Marshal(struct {
		Model           string     `json:"model"`
		Query           string     `json:"query"`
		Documents       []document `json:"documents"`
		TopN            int        `json:"top_n"`
		ReturnDocuments bool       `json:"return_documents"`
	}{request.Model, request.Query, documents, request.TopN, false})
	if err != nil {
		return RerankOutcome{}, err
	}
	responseBody, latency, err := c.capabilityRequest(ctx, "/v1/rerank", body)
	if err != nil {
		return RerankOutcome{}, err
	}
	var decoded struct {
		Model   string `json:"model"`
		Results []struct {
			Index          int     `json:"index"`
			RelevanceScore float64 `json:"relevance_score"`
		} `json:"results"`
		Usage struct {
			PromptTokens *int64 `json:"prompt_tokens"`
			TotalTokens  *int64 `json:"total_tokens"`
		} `json:"usage"`
		ExtraFields struct {
			Provider string `json:"provider"`
		} `json:"extra_fields"`
		Cost         *float64 `json:"cost"`
		CostCurrency string   `json:"cost_currency"`
		CostSource   string   `json:"cost_source"`
	}
	if json.Unmarshal(responseBody, &decoded) != nil || len(decoded.Results) != request.TopN {
		return RerankOutcome{}, &ModelError{Kind: ErrorInvalidResponse, Err: errors.New("invalid rerank response")}
	}
	ordered := make([]string, 0, len(decoded.Results))
	seen := make(map[int]struct{}, len(decoded.Results))
	for _, result := range decoded.Results {
		if result.Index < 0 || result.Index >= len(request.Candidates) || math.IsNaN(result.RelevanceScore) || math.IsInf(result.RelevanceScore, 0) {
			return RerankOutcome{}, &ModelError{Kind: ErrorInvalidResponse, Err: errors.New("invalid rerank result")}
		}
		if _, duplicate := seen[result.Index]; duplicate {
			return RerankOutcome{}, &ModelError{Kind: ErrorInvalidResponse, Err: errors.New("duplicate rerank index")}
		}
		seen[result.Index] = struct{}{}
		ordered = append(ordered, request.Candidates[result.Index].ID)
	}
	metadata, err := capabilityMetadata(request.Model, decoded.ExtraFields.Provider, decoded.Model, decoded.Usage.PromptTokens, decoded.Usage.TotalTokens, latency, decoded.Cost, decoded.CostCurrency, decoded.CostSource)
	if err != nil {
		return RerankOutcome{}, &ModelError{Kind: ErrorInvalidResponse, Err: err}
	}
	return RerankOutcome{CandidateIDs: ordered, Metadata: metadata}, nil
}

func (c *BifrostClient) capabilityRequest(ctx context.Context, path string, body []byte) ([]byte, time.Duration, error) {
	startedAt := time.Now()
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path, bytes.NewReader(body))
	if err != nil {
		return nil, 0, &ModelError{Kind: ErrorInvalidResponse, Err: err}
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Request-ID", uuid.NewString())
	response, err := c.httpClient.Do(request)
	latency := time.Since(startedAt)
	if err != nil {
		kind := ErrorUnavailable
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
			kind = ErrorTimeout
		}
		return nil, latency, &ModelError{Kind: kind, Err: err}
	}
	defer response.Body.Close()
	const limit = 8 << 20
	responseBody, err := io.ReadAll(io.LimitReader(response.Body, limit+1))
	if err != nil {
		return nil, latency, &ModelError{Kind: ErrorInvalidResponse, Err: err}
	}
	if len(responseBody) > limit {
		return nil, latency, &ModelError{Kind: ErrorInvalidResponse, Err: errors.New("Bifrost capability response too large")}
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, latency, &ModelError{Kind: ErrorUnavailable, Err: fmt.Errorf("Bifrost status %d", response.StatusCode)}
	}
	return responseBody, latency, nil
}

func capabilityMetadata(requestedModel, provider, model string, inputTokens, totalTokens *int64, latency time.Duration, cost *float64, currency, source string) (CapabilityMetadata, error) {
	metadata := CapabilityMetadata{
		RequestedModel: requestedModel, Provider: strings.TrimSpace(provider), Model: strings.TrimSpace(model),
		InputTokens: inputTokens, TotalTokens: totalTokens, Latency: latency,
	}
	if cost != nil && strings.TrimSpace(currency) != "" && strings.TrimSpace(source) != "" {
		metadata.Cost = ModelCost{Known: true, Amount: cost, Currency: currency, Source: source}
	}
	if strings.TrimSpace(metadata.RequestedModel) == "" || metadata.Latency < 0 {
		return CapabilityMetadata{}, errors.New("incomplete capability metadata")
	}
	for _, count := range []*int64{metadata.InputTokens, metadata.TotalTokens} {
		if count != nil && *count < 0 {
			return CapabilityMetadata{}, errors.New("negative capability usage")
		}
	}
	if metadata.Cost.Known && (metadata.Cost.Amount == nil || math.IsNaN(*metadata.Cost.Amount) || math.IsInf(*metadata.Cost.Amount, 0) || *metadata.Cost.Amount < 0) {
		return CapabilityMetadata{}, errors.New("invalid capability cost")
	}
	return metadata, nil
}

func finiteFloats(values []float32) bool {
	for _, value := range values {
		if math.IsNaN(float64(value)) || math.IsInf(float64(value), 0) {
			return false
		}
	}
	return true
}
