package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"unicode/utf8"

	"github.com/huangxinxinyu/nano-notebook/internal/agentobs"
	"github.com/huangxinxinyu/nano-notebook/internal/models"
	"github.com/huangxinxinyu/nano-notebook/internal/retrieval"
)

var (
	ErrSearchEvidenceUnavailable = errors.New("Search Evidence backend is unavailable")
	ErrEvidenceScopeUnavailable  = errors.New("Evidence search scope is unavailable")
)

type EvidenceSearchRequest struct {
	Query    string
	Purpose  string
	SourceID string
	EntryID  string
}

type EvidenceSearchResolvedScope struct {
	SourceID  string `json:"source_id"`
	EntryID   string `json:"entry_id,omitempty"`
	PageStart int    `json:"page_start,omitempty"`
	PageEnd   int    `json:"page_end,omitempty"`
}

type EvidenceSearchResult struct {
	retrieval.SearchResult
	Scope *EvidenceSearchResolvedScope
}

type EvidenceSearchBackend interface {
	SearchEvidenceScoped(context.Context, Attempt, EvidenceSearchRequest) (EvidenceSearchResult, error)
}

type searchEvidenceAction struct {
	backend EvidenceSearchBackend
}

type searchEvidenceInput struct {
	Query    string `json:"query"`
	Purpose  string `json:"purpose"`
	SourceID string `json:"source_id,omitempty"`
	EntryID  string `json:"entry_id,omitempty"`
}

func NewSearchEvidenceAction(backend EvidenceSearchBackend) Action {
	return searchEvidenceAction{backend: backend}
}

func (searchEvidenceAction) Available(execution Execution) (bool, string) {
	if execution.SelectedSourceCount <= 0 {
		return false, "no_sources_selected"
	}
	return true, ""
}

func (searchEvidenceAction) Definition() models.ActionDefinition {
	return models.ActionDefinition{
		Name:        "search_evidence",
		Description: "Search the Run's server-pinned Sources for evidence supporting a stated research purpose. Optionally restrict retrieval to one inspect_source source_id and entry_id. Refine and call again when needed.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"query":{"type":"string","minLength":1,"maxLength":2000},"purpose":{"type":"string","minLength":1,"maxLength":512},"source_id":{"type":"string","minLength":1,"maxLength":128},"entry_id":{"type":"string","minLength":1,"maxLength":128}},"required":["query","purpose"],"additionalProperties":false}`),
	}
}

func (searchEvidenceAction) ValidateInput(raw json.RawMessage) error {
	_, err := decodeSearchEvidenceInput(raw)
	return err
}

func (a searchEvidenceAction) Execute(ctx context.Context, request ActionRequest) (ActionResult, error) {
	if err := ctx.Err(); err != nil {
		return ActionResult{}, err
	}
	input, err := decodeSearchEvidenceInput(request.Input)
	if err != nil {
		return ActionResult{}, err
	}
	if a.backend == nil || request.Attempt.RunID == "" {
		return ActionResult{}, ErrSearchEvidenceUnavailable
	}
	result, err := a.backend.SearchEvidenceScoped(ctx, request.Attempt, EvidenceSearchRequest{
		Query: input.Query, Purpose: input.Purpose, SourceID: input.SourceID, EntryID: input.EntryID,
	})
	if err != nil {
		if errors.Is(err, ErrEvidenceScopeUnavailable) {
			return ActionResult{Status: ActionDomainError, ErrorCode: "evidence_scope_unavailable"}, nil
		}
		if errors.Is(err, retrieval.ErrRetrievalUnavailable) {
			return ActionResult{Status: ActionDomainError, ErrorCode: "retrieval_unavailable"}, nil
		}
		return ActionResult{}, err
	}
	output, err := newSearchEvidenceResult(result.SearchResult, result.Scope)
	if err != nil {
		return ActionResult{}, err
	}
	encoded, err := json.Marshal(output)
	if err != nil {
		return ActionResult{}, err
	}
	return ActionResult{Status: ActionSucceeded, Output: encoded, traceAttributes: searchEvidenceTraceAttributes(result.SearchResult)}, nil
}

func searchEvidenceTraceAttributes(result retrieval.SearchResult) []agentobs.Attribute {
	diagnostics := result.Diagnostics
	return []agentobs.Attribute{
		agentobs.Bool(TraceKeyDenseCompleted, diagnostics.Dense.Completed),
		agentobs.Int64(TraceKeyDenseCandidateCount, int64(len(diagnostics.Dense.CandidateIDs))),
		agentobs.String(TraceKeyDenseCandidateIDs, traceIdentityList(diagnostics.Dense.CandidateIDs)),
		agentobs.Int64(TraceKeyDenseDuration, diagnostics.Dense.DurationNanoseconds),
		agentobs.Bool(TraceKeyBM25Completed, diagnostics.BM25.Completed),
		agentobs.Int64(TraceKeyBM25CandidateCount, int64(len(diagnostics.BM25.CandidateIDs))),
		agentobs.String(TraceKeyBM25CandidateIDs, traceIdentityList(diagnostics.BM25.CandidateIDs)),
		agentobs.Int64(TraceKeyBM25Duration, diagnostics.BM25.DurationNanoseconds),
		agentobs.String(TraceKeyRRFTransitionIDs, traceIdentityList(diagnostics.Fused.CandidateIDs)),
		agentobs.Int64(TraceKeyRRFDuration, diagnostics.Fused.DurationNanoseconds),
		agentobs.String(TraceKeyEvidenceLoadIDs, traceIdentityList(diagnostics.EvidenceLoad.CandidateIDs)),
		agentobs.Int64(TraceKeyEvidenceLoadDuration, diagnostics.EvidenceLoad.DurationNanoseconds),
		agentobs.String(TraceKeyRerankTransitionIDs, traceIdentityList(diagnostics.Rerank.CandidateIDs)),
		agentobs.Int64(TraceKeyRerankDuration, diagnostics.Rerank.DurationNanoseconds),
		agentobs.Int64(TraceKeyRelevanceFilteredCount, int64(len(diagnostics.RelevanceFiltered))),
		agentobs.String(TraceKeyRelevanceFilteredIDs, traceIdentityList(diagnostics.RelevanceFiltered)),
		agentobs.Int64(TraceKeySelectedEvidenceCount, int64(len(result.Candidates))),
		agentobs.Bool(TraceKeyRetrievalDegraded, result.Degraded),
		agentobs.String(TraceKeyRetrievalDegradations, traceIdentityList(result.Degradations)),
		agentobs.Bool(TraceKeyRetrievalCompleteEmpty, result.CompleteEmpty),
	}
}

func traceIdentityList(values []string) string {
	const maximum = 64
	if len(values) > maximum {
		values = values[:maximum]
	}
	encoded, _ := json.Marshal(values)
	return string(encoded)
}

func decodeSearchEvidenceInput(raw json.RawMessage) (searchEvidenceInput, error) {
	if len(raw) == 0 || len(raw) > 8*1024 {
		return searchEvidenceInput{}, errors.New("invalid search_evidence input")
	}
	var input searchEvidenceInput
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil {
		return searchEvidenceInput{}, errors.New("invalid search_evidence input")
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return searchEvidenceInput{}, errors.New("invalid search_evidence input")
	}
	input.Query = strings.TrimSpace(input.Query)
	input.Purpose = strings.TrimSpace(input.Purpose)
	input.SourceID = strings.TrimSpace(input.SourceID)
	input.EntryID = strings.TrimSpace(input.EntryID)
	if input.Query == "" || input.Purpose == "" || !utf8.ValidString(input.Query) || !utf8.ValidString(input.Purpose) ||
		utf8.RuneCountInString(input.Query) > 2000 || utf8.RuneCountInString(input.Purpose) > 512 ||
		!utf8.ValidString(input.SourceID) || utf8.RuneCountInString(input.SourceID) > 128 ||
		!utf8.ValidString(input.EntryID) || utf8.RuneCountInString(input.EntryID) > 128 ||
		(input.EntryID != "" && input.SourceID == "") {
		return searchEvidenceInput{}, errors.New("invalid search_evidence input")
	}
	return input, nil
}
