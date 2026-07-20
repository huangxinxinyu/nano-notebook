package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"unicode/utf8"

	"github.com/huangxinxinyu/nano-notebook/internal/models"
	"github.com/huangxinxinyu/nano-notebook/internal/retrieval"
)

var ErrSearchEvidenceUnavailable = errors.New("Search Evidence backend is unavailable")

type EvidenceSearchBackend interface {
	SearchEvidence(context.Context, Attempt, string, string) (retrieval.SearchResult, error)
}

type searchEvidenceAction struct {
	backend EvidenceSearchBackend
}

type searchEvidenceInput struct {
	Query   string `json:"query"`
	Purpose string `json:"purpose"`
}

func NewSearchEvidenceAction(backend EvidenceSearchBackend) Action {
	return searchEvidenceAction{backend: backend}
}

func (searchEvidenceAction) Available(execution Execution) bool {
	return execution.SelectedSourceCount > 0
}

func (searchEvidenceAction) Definition() models.ActionDefinition {
	return models.ActionDefinition{
		Name:        "search_evidence",
		Description: "Search the Run's server-pinned Sources for evidence supporting a stated research purpose. Refine and call again when needed.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"query":{"type":"string","minLength":1,"maxLength":2000},"purpose":{"type":"string","minLength":1,"maxLength":512}},"required":["query","purpose"],"additionalProperties":false}`),
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
	result, err := a.backend.SearchEvidence(ctx, request.Attempt, input.Query, input.Purpose)
	if err != nil {
		if errors.Is(err, retrieval.ErrRetrievalUnavailable) {
			return ActionResult{Status: ActionDomainError, ErrorCode: "retrieval_unavailable"}, nil
		}
		return ActionResult{}, err
	}
	type evidenceOutput struct {
		SourceID           string              `json:"source_id"`
		EvidenceRevisionID string              `json:"evidence_revision_id"`
		SourceTitle        string              `json:"source_title"`
		Preview            string              `json:"preview"`
		EvidenceRanges     []retrieval.UnitRef `json:"evidence_ranges"`
	}
	output := struct {
		CompleteEmpty bool             `json:"complete_empty"`
		Degraded      bool             `json:"degraded"`
		Degradations  []string         `json:"degradations"`
		Evidence      []evidenceOutput `json:"evidence"`
	}{
		CompleteEmpty: result.CompleteEmpty, Degraded: result.Degraded,
		Degradations: append([]string(nil), result.Degradations...), Evidence: make([]evidenceOutput, 0, len(result.Candidates)),
	}
	for _, candidate := range result.Candidates {
		output.Evidence = append(output.Evidence, evidenceOutput{
			SourceID: candidate.SourceID, EvidenceRevisionID: candidate.RevisionID,
			SourceTitle: candidate.SourceTitle, Preview: candidate.Preview,
			EvidenceRanges: append([]retrieval.UnitRef(nil), candidate.UnitRefs...),
		})
	}
	encoded, err := json.Marshal(output)
	if err != nil {
		return ActionResult{}, err
	}
	return ActionResult{Status: ActionSucceeded, Output: encoded}, nil
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
	if input.Query == "" || input.Purpose == "" || !utf8.ValidString(input.Query) || !utf8.ValidString(input.Purpose) ||
		utf8.RuneCountInString(input.Query) > 2000 || utf8.RuneCountInString(input.Purpose) > 512 {
		return searchEvidenceInput{}, errors.New("invalid search_evidence input")
	}
	return input, nil
}
