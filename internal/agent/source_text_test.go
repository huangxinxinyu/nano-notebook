package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/huangxinxinyu/nano-notebook/internal/agentcatalog"
	"github.com/huangxinxinyu/nano-notebook/internal/retrieval"
	"github.com/huangxinxinyu/nano-notebook/internal/sourcemap"
)

type sourceTextBackendStub struct {
	documents  []SourceTextDocument
	maps       map[string]sourcemap.SourceMap
	bodies     map[string][]byte
	err        error
	requested  string
	mapLoads   int
	bodyScopes []ToolResultScope
}

func (s *sourceTextBackendStub) LoadSourceTexts(_ context.Context, _ Attempt, sourceID string) ([]SourceTextDocument, error) {
	s.requested = sourceID
	if s.err != nil {
		return nil, s.err
	}
	if sourceID == "" {
		return s.documents, nil
	}
	for _, document := range s.documents {
		if document.SourceID == sourceID {
			return []SourceTextDocument{document}, nil
		}
	}
	return nil, ErrEvidenceScopeUnavailable
}

func (s *sourceTextBackendStub) LoadSourceMap(_ context.Context, document SourceTextDocument) (sourcemap.SourceMap, bool, error) {
	s.mapLoads++
	value, ok := s.maps[document.SourceID]
	return value, ok, nil
}

func (s *sourceTextBackendStub) LoadToolResultBody(_ context.Context, scope ToolResultScope, resultRef string) ([]byte, error) {
	s.bodyScopes = append(s.bodyScopes, scope)
	body, ok := s.bodies[resultRef]
	if !ok {
		return nil, ErrToolResultExpired
	}
	return body, nil
}

func pdfTextDocument(sourceID string, pages int, unitsPerPage int, text func(page, index int) string) SourceTextDocument {
	document := SourceTextDocument{SourceID: sourceID, RevisionID: "evr_" + sourceID, Title: "Paper " + sourceID}
	for page := 1; page <= pages; page++ {
		for index := 0; index < unitsPerPage; index++ {
			document.Units = append(document.Units, sourceInspectionUnit{
				ID: fmt.Sprintf("ev_%s_%02d_%d", sourceID, page, index), Ordinal: len(document.Units), Kind: "paragraph",
				Text: text(page, index), Coordinate: retrieval.EvidenceCoordinate{Kind: "pdf_region", Page: page},
			})
		}
	}
	return document
}

func researchTextRequest(input string) ActionRequest {
	return ActionRequest{
		ActionID: "act_1", UserID: "usr_1", ChatID: "chat_1", Input: json.RawMessage(input),
		Attempt:    Attempt{RunID: "run_research"},
		Definition: agentcatalog.MustParseReference("research.executor@39"),
	}
}

func decodeSearchTextOutput(t *testing.T, result ActionResult) searchTextOutput {
	t.Helper()
	if result.Status != ActionSucceeded {
		t.Fatalf("result=%+v", result)
	}
	var output searchTextOutput
	if err := json.Unmarshal(result.Output, &output); err != nil {
		t.Fatal(err)
	}
	return output
}

func decodeReadSourceOutput(t *testing.T, result ActionResult) readSourceOutput {
	t.Helper()
	if result.Status != ActionSucceeded {
		t.Fatalf("result=%+v", result)
	}
	var output readSourceOutput
	if err := json.Unmarshal(result.Output, &output); err != nil {
		t.Fatal(err)
	}
	return output
}

func TestSearchTextValidatesInputShape(t *testing.T) {
	action := NewSearchTextAction(&sourceTextBackendStub{})
	for _, valid := range []string{
		`{"pattern":"Table 3"}`,
		`{"pattern":"[23]","source_id":"src_a"}`,
		`{"pattern":"lat(ency)?","regex":true,"case_sensitive":true,"result_ref":"tr_abcdefghijkl"}`,
	} {
		if err := action.ValidateInput(json.RawMessage(valid)); err != nil {
			t.Fatalf("rejected %s: %v", valid, err)
		}
	}
	for _, invalid := range []string{
		`{}`,
		`{"pattern":""}`,
		`{"pattern":"` + strings.Repeat("x", 257) + `"}`,
		`{"pattern":"x","source_id":"src_a","result_ref":"tr_abcdefghijkl"}`,
		`{"pattern":"x","result_ref":"run:abc/checkpoint:decision:1/action:0"}`,
		`{"pattern":"x","query":"extra"}`,
	} {
		if err := action.ValidateInput(json.RawMessage(invalid)); err == nil {
			t.Fatalf("accepted %s", invalid)
		}
	}
}

func TestSearchTextFindsLiteralMetacharactersWithAnchors(t *testing.T) {
	document := pdfTextDocument("src_a", 3, 2, func(page, index int) string {
		switch {
		case page == 2 && index == 1:
			return "As reported in [23], accuracy reached 0.873 on the held-out split."
		case page == 3 && index == 0:
			return "We also cite [23] and [24] here; 0.8735 is a different number."
		}
		return fmt.Sprintf("Ordinary passage on page %d.", page)
	})
	backend := &sourceTextBackendStub{
		documents: []SourceTextDocument{document},
		maps: map[string]sourcemap.SourceMap{"src_a": {SourceID: "src_a", RevisionID: "evr_src_a", PageCount: 3, Entries: []sourcemap.NavigationEntry{
			{EntryID: "entry_intro", Heading: "Introduction", PageStart: 1, PageEnd: 3},
			{EntryID: "entry_results", ParentEntryID: "entry_intro", Heading: "Results", PageStart: 2, PageEnd: 2},
		}}},
	}
	action := NewSearchTextAction(backend)
	output := decodeSearchTextOutput(t, mustExecute(t, action, researchTextRequest(`{"pattern":"[23]","source_id":"src_a"}`)))
	if backend.requested != "src_a" || output.TotalMatches != 2 || output.Truncated || len(output.Matches) != 2 {
		t.Fatalf("output=%+v", output)
	}
	first := output.Matches[0]
	if first.SourceID != "src_a" || first.UnitID != "ev_src_a_02_1" || first.Page != 2 || first.EntryID != "entry_results" ||
		!strings.Contains(first.Snippet, "«[23]»") {
		t.Fatalf("first=%+v", first)
	}
	if output.Matches[1].EntryID != "entry_intro" {
		t.Fatalf("innermost entry=%+v", output.Matches[1])
	}
	if len(output.Sources) != 1 || output.Sources[0].EvidenceRevisionID != "evr_src_a" || output.Sources[0].Matches != 2 {
		t.Fatalf("sources=%+v", output.Sources)
	}
	numbers := decodeSearchTextOutput(t, mustExecute(t, action, researchTextRequest(`{"pattern":"0.873"}`)))
	if numbers.TotalMatches != 2 || backend.requested != "" {
		t.Fatalf("literal dot must not match arbitrary characters: %+v", numbers)
	}
}

func TestSearchTextCaseFoldingRegexAndPatternErrors(t *testing.T) {
	document := pdfTextDocument("src_a", 1, 1, func(int, int) string { return "Latency fell; latency budgets and LATENCY targets." })
	action := NewSearchTextAction(&sourceTextBackendStub{documents: []SourceTextDocument{document}})
	if output := decodeSearchTextOutput(t, mustExecute(t, action, researchTextRequest(`{"pattern":"latency"}`))); output.TotalMatches != 3 {
		t.Fatalf("case-insensitive=%+v", output)
	}
	if output := decodeSearchTextOutput(t, mustExecute(t, action, researchTextRequest(`{"pattern":"latency","case_sensitive":true}`))); output.TotalMatches != 1 {
		t.Fatalf("case-sensitive=%+v", output)
	}
	if output := decodeSearchTextOutput(t, mustExecute(t, action, researchTextRequest(`{"pattern":"LATENCY (budgets|targets)","regex":true}`))); output.TotalMatches != 2 {
		t.Fatalf("regex=%+v", output)
	}
	for input, code := range map[string]string{
		`{"pattern":"(unclosed","regex":true}`: "invalid_pattern",
		`{"pattern":"a*","regex":true}`:        "pattern_matches_empty",
	} {
		result := mustExecute(t, action, researchTextRequest(input))
		if result.Status != ActionDomainError || result.ErrorCode != code {
			t.Fatalf("%s result=%+v", input, result)
		}
	}
}

func TestSearchTextBoundsMatchesPerUnitAndInTotal(t *testing.T) {
	document := pdfTextDocument("src_a", 20, 1, func(page, _ int) string {
		return strings.Repeat(fmt.Sprintf("needle on page %d. ", page), 5)
	})
	action := NewSearchTextAction(&sourceTextBackendStub{documents: []SourceTextDocument{document}})
	output := decodeSearchTextOutput(t, mustExecute(t, action, researchTextRequest(`{"pattern":"needle"}`)))
	if output.TotalMatches != 100 || !output.Truncated || len(output.Matches) != searchTextMaxMatches {
		t.Fatalf("total=%d truncated=%v matches=%d", output.TotalMatches, output.Truncated, len(output.Matches))
	}
	perUnit := make(map[string]int)
	for _, match := range output.Matches {
		perUnit[match.UnitID]++
		if utf8Runes(match.Snippet) > 2*searchTextSnippetRunes+len("needle")+8 {
			t.Fatalf("snippet too long: %q", match.Snippet)
		}
	}
	for unit, count := range perUnit {
		if count > searchTextMaxMatchesPerUnit {
			t.Fatalf("unit %s returned %d matches", unit, count)
		}
	}
	if len(mustExecute(t, action, researchTextRequest(`{"pattern":"needle"}`)).Output) > searchTextMaxOutputBytes {
		t.Fatal("output exceeds byte bound")
	}
	chat := researchTextRequest(`{"pattern":"needle"}`)
	chat.Definition = agentcatalog.MustParseReference("chat.leader@7")
	if result := mustExecute(t, action, chat); len(result.Output) > searchTextChatMaxOutputBytes {
		t.Fatalf("chat output=%d bytes", len(result.Output))
	}
}

func TestSearchTextResultRefOffsetsRoundTripThroughReadToolResult(t *testing.T) {
	markdown := "# Results\n\nIntro with <tags> & \"quotes\" and é.\nTable 3 reports 41.2 BLEU.\n\nSee Table 3 again\tlater."
	body, err := json.Marshal(readURLOutput{Title: "Paper", FinalURL: "https://example.com/p", Markdown: markdown, Engine: "x", MediaType: "text/html"})
	if err != nil {
		t.Fatal(err)
	}
	backend := &sourceTextBackendStub{bodies: map[string][]byte{"tr_abcdefghijkl": body, "tr_notreadurlxx": []byte(`{"results":[]}`)}}
	action := NewSearchTextAction(backend)
	output := decodeSearchTextOutput(t, mustExecute(t, action, researchTextRequest(`{"pattern":"Table 3","result_ref":"tr_abcdefghijkl"}`)))
	if output.TotalMatches != 2 || len(output.Matches) != 2 || output.Scope.ResultRef != "tr_abcdefghijkl" {
		t.Fatalf("output=%+v", output)
	}
	if scope := backend.bodyScopes[0]; scope.UserID != "usr_1" || scope.ChatID != "chat_1" || scope.RunID != "run_research" {
		t.Fatalf("scope=%+v", scope)
	}
	for _, match := range output.Matches {
		if match.Offset == nil || match.UnitID != "" {
			t.Fatalf("match=%+v", match)
		}
		rest := string(body[*match.Offset:])
		if !strings.HasPrefix(rest, "Table 3 reports") && !strings.HasPrefix(rest, "See Table 3") {
			t.Fatalf("offset %d starts at %q", *match.Offset, rest[:min(len(rest), 30)])
		}
	}
	result := mustExecute(t, action, researchTextRequest(`{"pattern":"x","result_ref":"tr_notreadurlxx"}`))
	if result.Status != ActionDomainError || result.ErrorCode != "result_ref_not_searchable" {
		t.Fatalf("non read_url result=%+v", result)
	}
	result = mustExecute(t, action, researchTextRequest(`{"pattern":"x","result_ref":"tr_missingmissing"}`))
	if result.Status != ActionDomainError || result.ErrorCode != "tool_result_expired" {
		t.Fatalf("expired result=%+v", result)
	}
}

func TestSourceTextScopeErrorsDoNotLeakExistence(t *testing.T) {
	backend := &sourceTextBackendStub{documents: []SourceTextDocument{pdfTextDocument("src_a", 2, 1, func(int, int) string { return "text" })}}
	search := NewSearchTextAction(backend)
	read := NewReadSourceAction(backend)
	for _, request := range []struct {
		action Action
		input  string
	}{
		{search, `{"pattern":"text","source_id":"src_foreign"}`},
		{read, `{"source_id":"src_foreign","page_start":1,"page_end":1}`},
		{read, `{"source_id":"src_a","unit_id":"ev_other"}`},
		{read, `{"source_id":"src_a","entry_id":"entry_missing"}`},
	} {
		result := mustExecute(t, request.action, researchTextRequest(request.input))
		if result.Status != ActionDomainError || result.ErrorCode != "evidence_scope_unavailable" {
			t.Fatalf("%s result=%+v", request.input, result)
		}
	}
}

func TestReadSourceValidatesExactlyOneLocator(t *testing.T) {
	action := NewReadSourceAction(&sourceTextBackendStub{})
	for _, valid := range []string{
		`{"source_id":"src_a","entry_id":"entry_results"}`,
		`{"source_id":"src_a","page_start":3,"page_end":12}`,
		`{"source_id":"src_a","unit_id":"ev_1"}`,
		`{"source_id":"src_a","unit_id":"ev_1","before":0,"after":40}`,
		`{"source_id":"src_a","page_start":1,"page_end":2,"from_unit_id":"ev_2"}`,
	} {
		if err := action.ValidateInput(json.RawMessage(valid)); err != nil {
			t.Fatalf("rejected %s: %v", valid, err)
		}
	}
	for _, invalid := range []string{
		`{"source_id":"src_a"}`,
		`{"entry_id":"entry_results"}`,
		`{"source_id":"src_a","entry_id":"e","unit_id":"ev_1"}`,
		`{"source_id":"src_a","page_start":3}`,
		`{"source_id":"src_a","page_start":3,"page_end":13}`,
		`{"source_id":"src_a","page_start":4,"page_end":3}`,
		`{"source_id":"src_a","page_start":1,"page_end":2,"after":3}`,
		`{"source_id":"src_a","unit_id":"ev_1","after":41}`,
	} {
		if err := action.ValidateInput(json.RawMessage(invalid)); err == nil {
			t.Fatalf("accepted %s", invalid)
		}
	}
}

func TestReadSourceLocatorsReturnOrderedUnits(t *testing.T) {
	document := pdfTextDocument("src_a", 6, 2, func(page, index int) string { return fmt.Sprintf("page %d unit %d", page, index) })
	backend := &sourceTextBackendStub{
		documents: []SourceTextDocument{document},
		maps: map[string]sourcemap.SourceMap{"src_a": {SourceID: "src_a", RevisionID: "evr_src_a", PageCount: 6, Entries: []sourcemap.NavigationEntry{
			{EntryID: "entry_results", Heading: "Results", PageStart: 3, PageEnd: 4},
		}}},
	}
	action := NewReadSourceAction(backend)
	pages := decodeReadSourceOutput(t, mustExecute(t, action, researchTextRequest(`{"source_id":"src_a","page_start":2,"page_end":3}`)))
	if len(pages.Units) != 4 || pages.Units[0].UnitID != "ev_src_a_02_0" || pages.Units[3].Page != 3 || !pages.Complete || pages.Next != nil ||
		pages.Source.EvidenceRevisionID != "evr_src_a" || pages.Scope.PageStart != 2 || pages.Scope.PageEnd != 3 {
		t.Fatalf("pages=%+v", pages)
	}
	entry := decodeReadSourceOutput(t, mustExecute(t, action, researchTextRequest(`{"source_id":"src_a","entry_id":"entry_results"}`)))
	if len(entry.Units) != 4 || entry.Units[0].Text != "page 3 unit 0" || entry.Scope.EntryID != "entry_results" || entry.Scope.PageEnd != 4 {
		t.Fatalf("entry=%+v", entry)
	}
	around := decodeReadSourceOutput(t, mustExecute(t, action, researchTextRequest(`{"source_id":"src_a","unit_id":"ev_src_a_01_0","before":5,"after":1}`)))
	if len(around.Units) != 2 || around.Units[0].UnitID != "ev_src_a_01_0" || around.Units[1].UnitID != "ev_src_a_01_1" {
		t.Fatalf("around=%+v", around)
	}
	defaulted := decodeReadSourceOutput(t, mustExecute(t, action, researchTextRequest(`{"source_id":"src_a","unit_id":"ev_src_a_04_0"}`)))
	if len(defaulted.Units) != readSourceDefaultBefore+1+5 || defaulted.Units[0].UnitID != "ev_src_a_03_0" {
		t.Fatalf("defaulted=%+v", defaulted)
	}
	result := mustExecute(t, action, researchTextRequest(`{"source_id":"src_a","page_start":7,"page_end":8}`))
	if result.Status != ActionDomainError || result.ErrorCode != "invalid_page_range" {
		t.Fatalf("out of bounds=%+v", result)
	}
}

func TestReadSourcePageRangeRequiresPDFCoordinates(t *testing.T) {
	document := SourceTextDocument{SourceID: "src_html", RevisionID: "evr_html", Units: []sourceInspectionUnit{
		{ID: "ev_0", Ordinal: 0, Kind: "paragraph", Text: "html text", Coordinate: retrieval.EvidenceCoordinate{Kind: "html_block", Block: 1}},
		{ID: "ev_1", Ordinal: 1, Kind: "paragraph", Text: "more html"},
	}}
	action := NewReadSourceAction(&sourceTextBackendStub{documents: []SourceTextDocument{document}})
	result := mustExecute(t, action, researchTextRequest(`{"source_id":"src_html","page_start":1,"page_end":1}`))
	if result.Status != ActionDomainError || result.ErrorCode != "page_range_unsupported" {
		t.Fatalf("result=%+v", result)
	}
	around := decodeReadSourceOutput(t, mustExecute(t, action, researchTextRequest(`{"source_id":"src_html","unit_id":"ev_1"}`)))
	if len(around.Units) != 2 || around.Units[0].Page != 0 {
		t.Fatalf("around=%+v", around)
	}
}

func TestReadSourceContinuationCoversRangeExactlyOnceWithinBytes(t *testing.T) {
	document := pdfTextDocument("src_a", 10, 6, func(page, index int) string {
		return fmt.Sprintf("p%d-u%d ", page, index) + strings.Repeat("dense evidence text ", 60)
	})
	action := NewReadSourceAction(&sourceTextBackendStub{documents: []SourceTextDocument{document}})
	seen := make(map[string]int)
	result := mustExecute(t, action, researchTextRequest(`{"source_id":"src_a","page_start":1,"page_end":10}`))
	for calls := 0; ; calls++ {
		if calls > 30 {
			t.Fatal("continuation did not terminate")
		}
		if len(result.Output) > readSourceMaxOutputBytes {
			t.Fatalf("output=%d bytes", len(result.Output))
		}
		output := decodeReadSourceOutput(t, result)
		for _, unit := range output.Units {
			seen[unit.UnitID]++
		}
		if output.Next == nil {
			if !output.Complete {
				t.Fatalf("incomplete without next: %+v", output)
			}
			break
		}
		if output.Complete || output.Next.FromUnitID == "" || output.Next.SourceID != "src_a" || output.Next.PageEnd != 10 {
			t.Fatalf("next=%+v", output.Next)
		}
		input, _ := json.Marshal(output.Next)
		result = mustExecute(t, action, researchTextRequest(string(input)))
	}
	if len(seen) != len(document.Units) {
		t.Fatalf("read %d of %d units", len(seen), len(document.Units))
	}
	for unit, count := range seen {
		if count != 1 {
			t.Fatalf("unit %s read %d times", unit, count)
		}
	}
}

func TestReadSourceTruncatesAnOversizedUnit(t *testing.T) {
	document := pdfTextDocument("src_a", 1, 2, func(_ int, index int) string {
		if index == 0 {
			return strings.Repeat("长", readSourceMaxOutputBytes)
		}
		return "short"
	})
	action := NewReadSourceAction(&sourceTextBackendStub{documents: []SourceTextDocument{document}})
	result := mustExecute(t, action, researchTextRequest(`{"source_id":"src_a","page_start":1,"page_end":1}`))
	output := decodeReadSourceOutput(t, result)
	if len(result.Output) > readSourceMaxOutputBytes || len(output.Units) != 1 || !output.Units[0].Truncated || output.Next == nil ||
		output.Next.FromUnitID != "ev_src_a_01_1" {
		t.Fatalf("bytes=%d output=%+v", len(result.Output), output.Next)
	}
	chat := researchTextRequest(`{"source_id":"src_a","page_start":1,"page_end":1}`)
	chat.Definition = agentcatalog.MustParseReference("chat.leader@7")
	if result := mustExecute(t, action, chat); len(result.Output) > readSourceChatMaxOutputBytes {
		t.Fatalf("chat output=%d bytes", len(result.Output))
	}
}

func TestSourceTextResultsMakeSourcesClaimableAndCitable(t *testing.T) {
	readOutput, _ := json.Marshal(readSourceOutput{Source: readSourceSource{SourceID: "src_read", EvidenceRevisionID: "evr_read"}, Units: []readSourceUnit{{UnitID: "ev_1", Text: "x"}}})
	searchOutput, _ := json.Marshal(searchTextOutput{Sources: []searchTextSource{
		{SourceID: "src_hit", EvidenceRevisionID: "evr_hit", Matches: 2},
		{SourceID: "src_miss", EvidenceRevisionID: "evr_miss", Matches: 0},
	}})
	prefix := CheckpointPrefix{Proposals: []AcceptedProposal{{DecisionNo: 1, Actions: []AcceptedAction{
		{ActionID: "a1", Name: readSourceActionName, Result: &ActionResult{Status: ActionSucceeded, Output: readOutput}},
		{ActionID: "a2", Name: searchTextActionName, Result: &ActionResult{Status: ActionSucceeded, Output: searchOutput}},
	}}}}
	claimable := searchedResearchSourceEvidence(prefix)
	for _, want := range []researchSourceEvidenceReference{{SourceID: "src_read", RevisionID: "evr_read"}, {SourceID: "src_hit", RevisionID: "evr_hit"}} {
		if _, ok := claimable[want]; !ok {
			t.Fatalf("missing %+v in %+v", want, claimable)
		}
	}
	if _, ok := claimable[researchSourceEvidenceReference{SourceID: "src_miss", RevisionID: "evr_miss"}]; ok {
		t.Fatal("a Source without matches became claimable")
	}
	state, err := parseResearchState(prefix)
	if err != nil {
		t.Fatal(err)
	}
	sources := make(map[string]bool)
	for _, item := range state.evidence {
		sources[item.SourceID] = true
	}
	if !state.performed || !state.evidenceSeen || !sources["src_read"] || !sources["src_hit"] || sources["src_miss"] {
		t.Fatalf("state=%+v", state)
	}
}

func mustExecute(t *testing.T, action Action, request ActionRequest) ActionResult {
	t.Helper()
	if err := action.ValidateInput(request.Input); err != nil {
		t.Fatalf("validate %s: %v", request.Input, err)
	}
	result, err := action.Execute(context.Background(), request)
	if err != nil {
		t.Fatalf("execute %s: %v", request.Input, err)
	}
	return result
}

func utf8Runes(value string) int { return len([]rune(value)) }

func TestSourceTextPublicActivityReusesExistingKinds(t *testing.T) {
	context := publicActivityContext{sourceTitles: map[string]string{"src_a": "Transformer Paper"}, selectedTitles: []string{"Transformer Paper", "BERT"}}
	search := projectPublicActivity(AcceptedAction{Name: searchTextActionName, Input: json.RawMessage(`{"pattern":"Table 3"}`)}, time.Time{}, context)
	if search.Kind != "searching_sources" || search.Detail != "Transformer Paper、BERT" {
		t.Fatalf("search=%+v", search)
	}
	read := projectPublicActivity(AcceptedAction{Name: readSourceActionName, Input: json.RawMessage(`{"source_id":"src_a","page_start":3,"page_end":5}`)}, time.Time{}, context)
	if read.Kind != "inspecting_source" || read.Detail != "Transformer Paper · 3–5" {
		t.Fatalf("read=%+v", read)
	}
}
