package models

import (
	"encoding/json"
	"errors"
	"math"
	"strings"
	"time"
)

type FinalDraft struct {
	Text string `json:"text"`
}

func (d FinalDraft) Validate() error {
	if strings.TrimSpace(d.Text) == "" {
		return errors.New("final draft is invalid")
	}
	return nil
}

type ActionProposal struct {
	Name  string
	Input json.RawMessage
}

type ActionProposalBatch struct {
	Actions []ActionProposal
}

type ModelDecision struct {
	Final    *FinalDraft
	Proposal *ActionProposalBatch
}

type ModelResultKind string

const (
	ModelResultFinalDraft     ModelResultKind = "final_draft"
	ModelResultActionProposal ModelResultKind = "action_proposal"
	ModelResultInvalid        ModelResultKind = "invalid_response"
	ModelResultTimeout        ModelResultKind = "timeout"
	ModelResultUnavailable    ModelResultKind = "unavailable"
)

type ModelCost struct {
	Known    bool     `json:"known"`
	Amount   *float64 `json:"amount,omitempty"`
	Currency string   `json:"currency,omitempty"`
	Source   string   `json:"source,omitempty"`
}

type ModelCallMetadata struct {
	RequestedModel   string          `json:"requested_model"`
	SelectedProvider string          `json:"selected_provider,omitempty"`
	SelectedModel    string          `json:"selected_model,omitempty"`
	ResultKind       ModelResultKind `json:"result_kind"`
	FinishReason     string          `json:"finish_reason,omitempty"`
	InputTokens      *int64          `json:"input_tokens,omitempty"`
	OutputTokens     *int64          `json:"output_tokens,omitempty"`
	TotalTokens      *int64          `json:"total_tokens,omitempty"`
	CachedTokens     *int64          `json:"cached_tokens,omitempty"`
	ReasoningTokens  *int64          `json:"reasoning_tokens,omitempty"`
	GatewayRetries   *int64          `json:"gateway_retries,omitempty"`
	GatewayFallbacks *int64          `json:"gateway_fallbacks,omitempty"`
	Latency          time.Duration   `json:"latency"`
	Cost             ModelCost       `json:"cost"`
}

type ModelOutcome struct {
	ModelDecision
	Metadata ModelCallMetadata
}

func (m ModelCallMetadata) Validate() error {
	if strings.TrimSpace(m.RequestedModel) == "" || m.ResultKind == "" || m.Latency < 0 {
		return errors.New("model call metadata is incomplete")
	}
	for _, value := range []*int64{m.InputTokens, m.OutputTokens, m.TotalTokens, m.CachedTokens, m.ReasoningTokens, m.GatewayRetries, m.GatewayFallbacks} {
		if value != nil && *value < 0 {
			return errors.New("model call metadata count is negative")
		}
	}
	if m.Cost.Known {
		if m.Cost.Amount == nil || math.IsNaN(*m.Cost.Amount) || math.IsInf(*m.Cost.Amount, 0) || *m.Cost.Amount < 0 || strings.TrimSpace(m.Cost.Currency) == "" || strings.TrimSpace(m.Cost.Source) == "" {
			return errors.New("known model cost is incomplete")
		}
	} else if m.Cost.Amount != nil || m.Cost.Currency != "" || m.Cost.Source != "" {
		return errors.New("unknown model cost contains a value")
	}
	return nil
}

func (o ModelOutcome) Validate() error {
	if err := o.ModelDecision.Validate(); err != nil {
		return err
	}
	return o.Metadata.Validate()
}

func (d ModelDecision) Validate() error {
	if (d.Final == nil) == (d.Proposal == nil) {
		return errors.New("model decision must contain exactly one variant")
	}
	if d.Final != nil {
		if err := d.Final.Validate(); err != nil {
			return err
		}
	}
	if d.Proposal != nil && len(d.Proposal.Actions) == 0 {
		return errors.New("action proposal batch is empty")
	}
	return nil
}
