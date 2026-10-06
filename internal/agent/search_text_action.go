package agent

import (
	"context"
	"encoding/json"
	"errors"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/huangxinxinyu/nano-notebook/internal/models"
	"github.com/huangxinxinyu/nano-notebook/internal/retrieval"
	"golang.org/x/text/unicode/norm"
)

const (
	searchTextMaxPatternRunes    = 256
	searchTextMaxMatches         = 30
	searchTextMaxMatchesPerUnit  = 3
	searchTextSnippetRunes       = 160
	searchTextMaxShownMatchRunes = 200
	searchTextMaxOutputBytes     = 8 * 1024
	searchTextChatMaxOutputBytes = 6 * 1024
)

var searchTextResultRefPattern = regexp.MustCompile(`^tr_[A-Za-z0-9_-]{12,128}$`)

type searchTextAction struct{ backend SourceTextBackend }

type searchTextInput struct {
	Pattern       string `json:"pattern"`
	Regex         bool   `json:"regex,omitempty"`
	CaseSensitive bool   `json:"case_sensitive,omitempty"`
	SourceID      string `json:"source_id,omitempty"`
	ResultRef     string `json:"result_ref,omitempty"`
}

type searchTextScope struct {
	SourceID  string `json:"source_id,omitempty"`
	ResultRef string `json:"result_ref,omitempty"`
}

type searchTextMatch struct {
	SourceID string `json:"source_id,omitempty"`
	UnitID   string `json:"unit_id,omitempty"`
	Page     int    `json:"page,omitempty"`
	EntryID  string `json:"entry_id,omitempty"`
	Offset   *int   `json:"offset,omitempty"`
	Snippet  string `json:"snippet"`
}

type searchTextSource struct {
	SourceID           string `json:"source_id"`
	EvidenceRevisionID string `json:"evidence_revision_id"`
	Title              string `json:"title,omitempty"`
	Matches            int    `json:"matches"`
}

type searchTextOutput struct {
	Scope        searchTextScope    `json:"scope"`
	TotalMatches int                `json:"total_matches"`
	Truncated    bool               `json:"truncated"`
	Matches      []searchTextMatch  `json:"matches"`
	Sources      []searchTextSource `json:"sources,omitempty"`
	Note         string             `json:"note,omitempty"`
}

func NewSearchTextAction(backend SourceTextBackend) Action {
	return searchTextAction{backend: backend}
}

func (searchTextAction) CrashReplaySafe() bool { return true }

func (searchTextAction) Definition() models.ActionDefinition {
	return models.ActionDefinition{
		Name:        searchTextActionName,
		Description: "Find exact text: a literal string by default, or an RE2 regular expression with regex=true; case-insensitive unless case_sensitive=true. Searches every pinned Source, one source_id, or one read_url result_ref. Use it for exact names, numbers, labels such as Table 3, reference markers such as [23], and every-occurrence questions; use search_evidence for concepts. Source hits return unit_id for read_source; result_ref hits return an offset for read_tool_result.",
		InputSchema: json.RawMessage(`{"type":"object","additionalProperties":false,"required":["pattern"],"properties":{"pattern":{"type":"string","minLength":1,"maxLength":256},"regex":{"type":"boolean"},"case_sensitive":{"type":"boolean"},"source_id":{"type":"string","minLength":1,"maxLength":128},"result_ref":{"type":"string","pattern":"^tr_[A-Za-z0-9_-]{12,128}$"}}}`),
	}
}

func (searchTextAction) ValidateInput(raw json.RawMessage) error {
	_, err := decodeSearchTextInput(raw)
	return err
}

func decodeSearchTextInput(raw json.RawMessage) (searchTextInput, error) {
	var input searchTextInput
	if len(raw) == 0 || len(raw) > 4096 || decodeExactJSON(raw, &input) != nil {
		return searchTextInput{}, errors.New("invalid search_text input")
	}
	input.SourceID = strings.TrimSpace(input.SourceID)
	if strings.TrimSpace(input.Pattern) == "" || !utf8.ValidString(input.Pattern) ||
		utf8.RuneCountInString(input.Pattern) > searchTextMaxPatternRunes ||
		utf8.RuneCountInString(input.SourceID) > 128 ||
		(input.ResultRef != "" && !searchTextResultRefPattern.MatchString(input.ResultRef)) ||
		(input.SourceID != "" && input.ResultRef != "") {
		return searchTextInput{}, errors.New("invalid search_text input")
	}
	return input, nil
}

func compileSearchTextPattern(input searchTextInput, normalize bool) (*regexp.Regexp, string) {
	pattern := input.Pattern
	if normalize {
		pattern = norm.NFC.String(pattern)
	}
	if !input.Regex {
		pattern = regexp.QuoteMeta(pattern)
	}
	if !input.CaseSensitive {
		pattern = "(?i)" + pattern
	}
	compiled, err := regexp.Compile(pattern)
	if err != nil {
		return nil, "invalid_pattern"
	}
	if compiled.MatchString("") {
		return nil, "pattern_matches_empty"
	}
	return compiled, ""
}

func (a searchTextAction) Execute(ctx context.Context, request ActionRequest) (ActionResult, error) {
	if err := ctx.Err(); err != nil {
		return ActionResult{}, err
	}
	input, err := decodeSearchTextInput(request.Input)
	if err != nil {
		return ActionResult{}, err
	}
	if a.backend == nil || request.Attempt.RunID == "" {
		return ActionResult{Status: ActionDomainError, ErrorCode: "source_text_unavailable"}, nil
	}
	limit := searchTextMaxOutputBytes
	if isChatDefinition(request.Definition) {
		limit = searchTextChatMaxOutputBytes
	}
	var output searchTextOutput
	var code string
	if input.ResultRef != "" {
		output, code, err = a.searchResult(ctx, request, input)
	} else {
		output, code, err = a.searchSources(ctx, request, input)
	}
	if err != nil {
		return ActionResult{}, err
	}
	if code != "" {
		return ActionResult{Status: ActionDomainError, ErrorCode: code}, nil
	}
	encoded, err := fitSearchTextOutput(output, limit)
	if err != nil {
		return ActionResult{}, err
	}
	return ActionResult{Status: ActionSucceeded, Output: encoded}, nil
}

func (a searchTextAction) searchSources(ctx context.Context, request ActionRequest, input searchTextInput) (searchTextOutput, string, error) {
	pattern, code := compileSearchTextPattern(input, true)
	if code != "" {
		return searchTextOutput{}, code, nil
	}
	documents, err := a.backend.LoadSourceTexts(ctx, request.Attempt, input.SourceID)
	if code, ok := sourceTextErrorCode(err); ok {
		return searchTextOutput{}, code, nil
	}
	if err != nil {
		return searchTextOutput{}, "", err
	}
	output := searchTextOutput{Scope: searchTextScope{SourceID: input.SourceID}, Matches: make([]searchTextMatch, 0)}
	for _, document := range documents {
		if err := ctx.Err(); err != nil {
			return searchTextOutput{}, "", err
		}
		sourceMatches := 0
		firstShown := len(output.Matches)
		for _, unit := range document.Units {
			text := norm.NFC.String(unit.Text)
			found := pattern.FindAllStringIndex(text, -1)
			sourceMatches += len(found)
			for index, match := range found {
				if index >= searchTextMaxMatchesPerUnit || len(output.Matches) >= searchTextMaxMatches {
					break
				}
				output.Matches = append(output.Matches, searchTextMatch{
					SourceID: document.SourceID, UnitID: unit.ID, Page: unitPDFPage(unit),
					Snippet: searchTextSnippet(text, match[0], match[1]),
				})
			}
		}
		output.TotalMatches += sourceMatches
		if sourceMatches == 0 {
			continue
		}
		output.Sources = append(output.Sources, searchTextSource{
			SourceID: document.SourceID, EvidenceRevisionID: document.RevisionID,
			Title: truncateRunes(strings.TrimSpace(document.Title), 200), Matches: sourceMatches,
		})
		if err := a.annotateEntries(ctx, document, output.Matches[firstShown:]); err != nil {
			return searchTextOutput{}, "", err
		}
	}
	output.Truncated = output.TotalMatches > len(output.Matches)
	return output, "", nil
}

func (a searchTextAction) annotateEntries(ctx context.Context, document SourceTextDocument, matches []searchTextMatch) error {
	paged := false
	for _, match := range matches {
		paged = paged || match.Page > 0
	}
	if !paged {
		return nil
	}
	sourceMap, ok, err := a.backend.LoadSourceMap(ctx, document)
	if err != nil || !ok {
		return err
	}
	for index := range matches {
		if matches[index].Page > 0 {
			matches[index].EntryID = innermostSourceMapEntry(sourceMap.Entries, matches[index].Page)
		}
	}
	return nil
}

func (a searchTextAction) searchResult(ctx context.Context, request ActionRequest, input searchTextInput) (searchTextOutput, string, error) {
	pattern, code := compileSearchTextPattern(input, false)
	if code != "" {
		return searchTextOutput{}, code, nil
	}
	body, err := a.backend.LoadToolResultBody(ctx, ToolResultScope{
		UserID: request.UserID, ChatID: request.ChatID, RunID: request.Attempt.RunID,
	}, input.ResultRef)
	if err != nil {
		switch {
		case errors.Is(err, ErrToolResultExpired):
			return searchTextOutput{}, "tool_result_expired", nil
		case errors.Is(err, ErrToolResultUnauthorized):
			return searchTextOutput{}, "tool_result_unauthorized", nil
		case errors.Is(err, ErrToolResultCorrupt):
			return searchTextOutput{}, "tool_result_corrupt", nil
		}
		return searchTextOutput{}, "", err
	}
	markdown, offsets, err := readURLMarkdownWithOffsets(body)
	if err != nil {
		return searchTextOutput{}, "result_ref_not_searchable", nil
	}
	output := searchTextOutput{Scope: searchTextScope{ResultRef: input.ResultRef}, Matches: make([]searchTextMatch, 0)}
	found := pattern.FindAllStringIndex(markdown, -1)
	output.TotalMatches = len(found)
	for _, match := range found {
		if len(output.Matches) >= searchTextMaxMatches {
			break
		}
		lineStart := strings.LastIndexByte(markdown[:match[0]], '\n') + 1
		offset := int(offsets[lineStart])
		output.Matches = append(output.Matches, searchTextMatch{
			Offset: &offset, Snippet: searchTextSnippet(markdown, match[0], match[1]),
		})
	}
	output.Truncated = output.TotalMatches > len(output.Matches)
	return output, "", nil
}

// searchTextSnippet shows the match, marked «…», with up to
// searchTextSnippetRunes of context on each side.
func searchTextSnippet(text string, start, end int) string {
	before := []rune(text[:start])
	after := []rune(text[end:])
	match, _ := truncateSourceTextRunes(text[start:end], searchTextMaxShownMatchRunes)
	var builder strings.Builder
	if len(before) > searchTextSnippetRunes {
		builder.WriteString("…")
		before = before[len(before)-searchTextSnippetRunes:]
	}
	builder.WriteString(collapseSourceTextWhitespace(string(before)))
	if len(before) > 0 && isSpaceRune(before[len(before)-1]) {
		builder.WriteByte(' ')
	}
	builder.WriteString("«" + collapseSourceTextWhitespace(match) + "»")
	if len(after) > 0 && isSpaceRune(after[0]) {
		builder.WriteByte(' ')
	}
	if len(after) > searchTextSnippetRunes {
		builder.WriteString(collapseSourceTextWhitespace(string(after[:searchTextSnippetRunes])) + "…")
	} else {
		builder.WriteString(collapseSourceTextWhitespace(string(after)))
	}
	return builder.String()
}

func isSpaceRune(value rune) bool {
	return strings.TrimSpace(string(value)) == ""
}

func fitSearchTextOutput(output searchTextOutput, limit int) (json.RawMessage, error) {
	for {
		if output.Truncated {
			output.Note = "Showing the first matches only; narrow the pattern or scope to see the rest."
		}
		encoded, err := json.Marshal(output)
		if err != nil {
			return nil, err
		}
		if len(encoded) <= limit || len(output.Matches) == 0 {
			return encoded, nil
		}
		output.Matches = output.Matches[:len(output.Matches)-1]
		output.Truncated = true
	}
}

// sourceTextErrorCode maps scope and availability failures to stable domain
// errors that never reveal whether an out-of-scope Source exists.
func sourceTextErrorCode(err error) (string, bool) {
	switch {
	case err == nil:
		return "", false
	case errors.Is(err, ErrEvidenceScopeUnavailable):
		return "evidence_scope_unavailable", true
	case errors.Is(err, retrieval.ErrRetrievalUnavailable), errors.Is(err, ErrSourceInspectionUnavailable):
		return "source_text_unavailable", true
	}
	return "", false
}
