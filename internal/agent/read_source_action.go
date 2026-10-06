package agent

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/huangxinxinyu/nano-notebook/internal/models"
)

const (
	readSourceMaxPages           = 10
	readSourceDefaultBefore      = 2
	readSourceDefaultAfter       = 6
	readSourceMaxNeighbors       = 40
	readSourceMaxOutputBytes     = 24 * 1024
	readSourceChatMaxOutputBytes = 8 * 1024
)

type readSourceAction struct{ backend SourceTextBackend }

// readSourceInput takes source_id plus exactly one locator: entry_id, a
// page_start/page_end range, or unit_id with optional before/after.
// from_unit_id resumes any locator at a unit a previous result named in next.
type readSourceInput struct {
	SourceID   string `json:"source_id"`
	EntryID    string `json:"entry_id,omitempty"`
	PageStart  int    `json:"page_start,omitempty"`
	PageEnd    int    `json:"page_end,omitempty"`
	UnitID     string `json:"unit_id,omitempty"`
	Before     *int   `json:"before,omitempty"`
	After      *int   `json:"after,omitempty"`
	FromUnitID string `json:"from_unit_id,omitempty"`
}

type readSourceSource struct {
	SourceID           string `json:"source_id"`
	EvidenceRevisionID string `json:"evidence_revision_id"`
	Title              string `json:"title,omitempty"`
}

type readSourceScope struct {
	EntryID   string `json:"entry_id,omitempty"`
	PageStart int    `json:"page_start,omitempty"`
	PageEnd   int    `json:"page_end,omitempty"`
	UnitID    string `json:"unit_id,omitempty"`
}

type readSourceUnit struct {
	UnitID    string `json:"unit_id"`
	Kind      string `json:"kind"`
	Page      int    `json:"page,omitempty"`
	Text      string `json:"text"`
	Truncated bool   `json:"truncated,omitempty"`
}

type readSourceOutput struct {
	Source   readSourceSource `json:"source"`
	Scope    readSourceScope  `json:"scope"`
	Units    []readSourceUnit `json:"units"`
	Complete bool             `json:"complete"`
	Next     *readSourceInput `json:"next,omitempty"`
}

func NewReadSourceAction(backend SourceTextBackend) Action {
	return readSourceAction{backend: backend}
}

func (readSourceAction) CrashReplaySafe() bool { return true }

func (readSourceAction) Available(execution Execution) (bool, string) {
	if execution.SelectedSourceCount <= 0 {
		return false, "no_sources_selected"
	}
	return true, ""
}

func (readSourceAction) Definition() models.ActionDefinition {
	return models.ActionDefinition{
		Name:        readSourceActionName,
		Description: "Read a pinned Source's original text in order, as Evidence Units. Give source_id and exactly one locator: entry_id from inspect_source, page_start and page_end (at most 10 pages, PDF only), or unit_id from search_text or search_evidence with optional before and after unit counts. When complete=false, call again with the returned next input. Units are verbatim text you may quote in record_claim.",
		InputSchema: json.RawMessage(`{"type":"object","additionalProperties":false,"required":["source_id"],"properties":{"source_id":{"type":"string","minLength":1,"maxLength":128},"entry_id":{"type":"string","minLength":1,"maxLength":128},"page_start":{"type":"integer","minimum":1},"page_end":{"type":"integer","minimum":1},"unit_id":{"type":"string","minLength":1,"maxLength":128},"before":{"type":"integer","minimum":0,"maximum":40},"after":{"type":"integer","minimum":0,"maximum":40},"from_unit_id":{"type":"string","minLength":1,"maxLength":128}}}`),
	}
}

func (readSourceAction) ValidateInput(raw json.RawMessage) error {
	_, err := decodeReadSourceInput(raw)
	return err
}

func decodeReadSourceInput(raw json.RawMessage) (readSourceInput, error) {
	var input readSourceInput
	invalid := errors.New("invalid read_source input")
	if len(raw) == 0 || len(raw) > 2048 || decodeExactJSON(raw, &input) != nil {
		return readSourceInput{}, invalid
	}
	input.SourceID = strings.TrimSpace(input.SourceID)
	input.EntryID = strings.TrimSpace(input.EntryID)
	input.UnitID = strings.TrimSpace(input.UnitID)
	input.FromUnitID = strings.TrimSpace(input.FromUnitID)
	for _, value := range []string{input.SourceID, input.EntryID, input.UnitID, input.FromUnitID} {
		if utf8.RuneCountInString(value) > 128 {
			return readSourceInput{}, invalid
		}
	}
	paged := input.PageStart != 0 || input.PageEnd != 0
	locators := 0
	for _, present := range []bool{input.EntryID != "", paged, input.UnitID != ""} {
		if present {
			locators++
		}
	}
	if input.SourceID == "" || locators != 1 {
		return readSourceInput{}, invalid
	}
	if paged && (input.PageStart < 1 || input.PageEnd < input.PageStart || input.PageEnd-input.PageStart+1 > readSourceMaxPages) {
		return readSourceInput{}, invalid
	}
	if input.UnitID == "" && (input.Before != nil || input.After != nil) {
		return readSourceInput{}, invalid
	}
	for _, count := range []*int{input.Before, input.After} {
		if count != nil && (*count < 0 || *count > readSourceMaxNeighbors) {
			return readSourceInput{}, invalid
		}
	}
	return input, nil
}

func (a readSourceAction) Execute(ctx context.Context, request ActionRequest) (ActionResult, error) {
	if err := ctx.Err(); err != nil {
		return ActionResult{}, err
	}
	input, err := decodeReadSourceInput(request.Input)
	if err != nil {
		return ActionResult{}, err
	}
	if a.backend == nil || request.Attempt.RunID == "" {
		return ActionResult{Status: ActionDomainError, ErrorCode: "source_text_unavailable"}, nil
	}
	documents, err := a.backend.LoadSourceTexts(ctx, request.Attempt, input.SourceID)
	if code, ok := sourceTextErrorCode(err); ok {
		return ActionResult{Status: ActionDomainError, ErrorCode: code}, nil
	}
	if err != nil {
		return ActionResult{}, err
	}
	if len(documents) != 1 || documents[0].SourceID != input.SourceID {
		return ActionResult{Status: ActionDomainError, ErrorCode: "evidence_scope_unavailable"}, nil
	}
	document := documents[0]
	indexes, scope, code, err := a.selectUnits(ctx, document, input)
	if err != nil {
		return ActionResult{}, err
	}
	if code == "" && input.FromUnitID != "" {
		position := -1
		for index, unitIndex := range indexes {
			if document.Units[unitIndex].ID == input.FromUnitID {
				position = index
				break
			}
		}
		if position < 0 {
			code = "evidence_scope_unavailable"
		} else {
			indexes = indexes[position:]
		}
	}
	if code != "" {
		return ActionResult{Status: ActionDomainError, ErrorCode: code}, nil
	}
	limit := readSourceMaxOutputBytes
	if isChatDefinition(request.Definition) {
		limit = readSourceChatMaxOutputBytes
	}
	encoded, err := fitReadSourceOutput(document, input, scope, indexes, limit)
	if err != nil {
		return ActionResult{}, err
	}
	return ActionResult{Status: ActionSucceeded, Output: encoded}, nil
}

// selectUnits resolves the locator to ordered unit indexes inside document.
func (a readSourceAction) selectUnits(ctx context.Context, document SourceTextDocument, input readSourceInput) ([]int, readSourceScope, string, error) {
	units := document.Units
	switch {
	case input.UnitID != "":
		anchor := -1
		for index, unit := range units {
			if unit.ID == input.UnitID {
				anchor = index
				break
			}
		}
		if anchor < 0 {
			return nil, readSourceScope{}, "evidence_scope_unavailable", nil
		}
		before, after := readSourceDefaultBefore, readSourceDefaultAfter
		if input.Before != nil {
			before = *input.Before
		}
		if input.After != nil {
			after = *input.After
		}
		indexes := make([]int, 0, before+after+1)
		for index := max(0, anchor-before); index <= min(len(units)-1, anchor+after); index++ {
			indexes = append(indexes, index)
		}
		return indexes, readSourceScope{UnitID: input.UnitID}, "", nil
	case input.EntryID != "":
		sourceMap, ok, err := a.backend.LoadSourceMap(ctx, document)
		if err != nil {
			return nil, readSourceScope{}, "", err
		}
		if !ok {
			return nil, readSourceScope{}, "evidence_scope_unavailable", nil
		}
		for _, entry := range sourceMap.Entries {
			if entry.EntryID != input.EntryID {
				continue
			}
			indexes := unitsOnPages(units, entry.PageStart, entry.PageEnd)
			if len(indexes) == 0 {
				return nil, readSourceScope{}, "evidence_scope_unavailable", nil
			}
			return indexes, readSourceScope{EntryID: entry.EntryID, PageStart: entry.PageStart, PageEnd: entry.PageEnd}, "", nil
		}
		return nil, readSourceScope{}, "evidence_scope_unavailable", nil
	default:
		pageCount := 0
		for _, unit := range units {
			pageCount = max(pageCount, unitPDFPage(unit))
		}
		if pageCount == 0 {
			return nil, readSourceScope{}, "page_range_unsupported", nil
		}
		if input.PageEnd > pageCount {
			return nil, readSourceScope{}, "invalid_page_range", nil
		}
		indexes := unitsOnPages(units, input.PageStart, input.PageEnd)
		if len(indexes) == 0 {
			return nil, readSourceScope{}, "invalid_page_range", nil
		}
		return indexes, readSourceScope{PageStart: input.PageStart, PageEnd: input.PageEnd}, "", nil
	}
}

func unitsOnPages(units []sourceInspectionUnit, start, end int) []int {
	indexes := make([]int, 0)
	for index, unit := range units {
		if page := unitPDFPage(unit); page >= start && page <= end {
			indexes = append(indexes, index)
		}
	}
	return indexes
}

// fitReadSourceOutput returns as many whole units as fit in limit bytes,
// truncating only a single unit that cannot fit on its own, and names the
// next unit to resume from when any remain.
func fitReadSourceOutput(document SourceTextDocument, input readSourceInput, scope readSourceScope, indexes []int, limit int) (json.RawMessage, error) {
	output := readSourceOutput{
		Source: readSourceSource{
			SourceID: document.SourceID, EvidenceRevisionID: document.RevisionID,
			Title: truncateRunes(strings.TrimSpace(document.Title), 200),
		},
		Scope: scope, Units: make([]readSourceUnit, 0),
	}
	resume := func(position int) *readSourceInput {
		if position >= len(indexes) {
			return nil
		}
		next := input
		next.FromUnitID = document.Units[indexes[position]].ID
		return &next
	}
	encode := func(units []readSourceUnit) (json.RawMessage, error) {
		candidate := output
		candidate.Units = units
		candidate.Next = resume(len(units))
		candidate.Complete = candidate.Next == nil
		return json.Marshal(candidate)
	}
	encoded, err := encode(output.Units)
	if err != nil {
		return nil, err
	}
	for _, unitIndex := range indexes {
		unit := document.Units[unitIndex]
		projected := readSourceUnit{UnitID: unit.ID, Kind: unit.Kind, Page: unitPDFPage(unit), Text: unit.Text}
		candidate, err := encode(append(output.Units, projected))
		if err != nil {
			return nil, err
		}
		if len(candidate) <= limit {
			output.Units, encoded = append(output.Units, projected), candidate
			continue
		}
		if len(output.Units) > 0 {
			break
		}
		runes := []rune(unit.Text)
		fits := sort.Search(len(runes)+1, func(count int) bool {
			trial := projected
			trial.Text, trial.Truncated = string(runes[:count]), true
			encodedTrial, err := encode([]readSourceUnit{trial})
			return err != nil || len(encodedTrial) > limit
		}) - 1
		if fits < 1 {
			break
		}
		projected.Text, projected.Truncated = string(runes[:fits]), true
		output.Units = append(output.Units, projected)
		if encoded, err = encode(output.Units); err != nil {
			return nil, err
		}
		break
	}
	return encoded, nil
}
