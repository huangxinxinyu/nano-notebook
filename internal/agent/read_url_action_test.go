package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/huangxinxinyu/nano-notebook/internal/agentcatalog"
	"github.com/huangxinxinyu/nano-notebook/internal/documentreading"
	"github.com/huangxinxinyu/nano-notebook/internal/objectstore"
	"github.com/huangxinxinyu/nano-notebook/internal/webreader"
)

type webReaderStub struct {
	page    webreader.Page
	err     error
	request webreader.Request
}

func TestReadURLDefinitionExplainsExternalizedContinuation(t *testing.T) {
	description := (&readURLAction{}).Definition().Description
	if !strings.Contains(description, "result_ref") || !strings.Contains(description, "read_tool_result") {
		t.Fatalf("description does not explain long-result continuation: %q", description)
	}
}

func TestResearchReadURLAndDocumentPagesActionsExposePDFEvidence(t *testing.T) {
	reader := NewResearchURLContentReader(&acquiringStub{content: webreader.Content{
		MediaType: webreader.MediaTypePDF, FinalURL: "https://example.com/paper.pdf",
		PDF: researchTextPDF("First action page.", "Second action page."),
	}}, &rendererStub{}, documentreading.NewPDFExtractor(nil, documentreading.PDFExtractorConfig{}), objectstore.NewMemoryStore(), ResearchURLReaderConfig{
		ExtractionConfigID: "research-pdf-v1", RenderConfigID: "render-v1", RenderMaxPages: 500,
		RenderDPI: 144, RenderMaxPixelsPerPage: 20_000_000, RenderMaxOutputBytes: 256 << 20,
		MaxNormalizedRunes: 20_000_000, MaxModelChars: ResearchReadURLMaxChars, MaxPageRead: 20,
	})
	actions := NewResearchURLActions(reader)
	if len(actions) != 2 || actions[0].Definition().Name != "read_url" || actions[1].Definition().Name != "read_document_pages" {
		t.Fatalf("definitions=%v %v", actions[0].Definition().Name, actions[1].Definition().Name)
	}
	readResult, err := actions[0].Execute(context.Background(), ActionRequest{
		ActionID: "decision:1/action:0", Attempt: Attempt{RunID: "run_pdf"},
		Definition: agentcatalog.MustParseReference("research.executor@8"),
		Input:      json.RawMessage(`{"url":"https://example.com/paper.pdf"}`),
	})
	if err != nil || readResult.Status != ActionSucceeded {
		t.Fatalf("result=%+v err=%v", readResult, err)
	}
	var readOutput readURLOutput
	if err := json.Unmarshal(readResult.Output, &readOutput); err != nil {
		t.Fatal(err)
	}
	if readOutput.MediaType != webreader.MediaTypePDF || readOutput.PageCount != 2 || readOutput.DocumentHandle == "" ||
		!strings.Contains(readOutput.Markdown, "nano-pdf-page:1") {
		t.Fatalf("output=%+v", readOutput)
	}

	pageInput, _ := json.Marshal(map[string]any{
		"document_handle": readOutput.DocumentHandle, "start_page": 2, "end_page": 2,
	})
	pageResult, err := actions[1].Execute(context.Background(), ActionRequest{
		Attempt: Attempt{RunID: "run_pdf"}, Input: pageInput,
	})
	if err != nil || pageResult.Status != ActionSucceeded {
		t.Fatalf("result=%+v err=%v", pageResult, err)
	}
	var pageOutput readDocumentPagesOutput
	if json.Unmarshal(pageResult.Output, &pageOutput) != nil || pageOutput.StartPage != 2 ||
		strings.Contains(pageOutput.Markdown, "First action page.") || !strings.Contains(pageOutput.Markdown, "Second action page.") {
		t.Fatalf("output=%+v", pageOutput)
	}
}

func TestResearchReadActionsCacheLongResultsFromVersion14(t *testing.T) {
	actions := NewVersionedResearchURLActions(nil, &webReaderStub{}, &acquiringStub{})
	for _, action := range actions {
		policy, ok := action.(ToolResultCacheEligibility)
		if !ok {
			t.Fatalf("%s has no Tool Result cache eligibility", action.Definition().Name)
		}
		if policy.CacheLongToolResults(agentcatalog.MustParseReference("research.executor@13")) {
			t.Fatalf("%s enabled cache for old Definition", action.Definition().Name)
		}
		if !policy.CacheLongToolResults(agentcatalog.MustParseReference("research.executor@14")) {
			t.Fatalf("%s did not enable cache for v14", action.Definition().Name)
		}
		if !policy.CacheLongToolResults(agentcatalog.MustParseReference("research.executor@15")) {
			t.Fatalf("%s did not preserve cache eligibility for v15", action.Definition().Name)
		}
	}
}

func TestSideEffectingSourceImportIsNeverToolResultCacheEligible(t *testing.T) {
	action := NewSaveURLAsSourceAction(nil)
	if _, ok := action.(ToolResultCacheEligibility); ok {
		t.Fatal("save_url_as_source must not externalize a result whose expiry could induce a repeated mutation")
	}
}

func TestPinnedPreV8ResearchDefinitionKeepsHTMLOnlyReadURLPath(t *testing.T) {
	legacy := &webReaderStub{page: webreader.Page{
		Title: "Legacy HTML", FinalURL: "https://example.com/article", Content: "Legacy evidence.",
		Engine: "lightweight", WordCount: 2,
	}}
	mediaAcquirer := &acquiringStub{err: errors.New("media-aware reader must not run for v7")}
	mediaReader := NewResearchURLContentReader(mediaAcquirer, nil, nil, nil, ResearchURLReaderConfig{})
	action := NewResearchURLActions(mediaReader, legacy)[0]

	result, err := action.Execute(context.Background(), ActionRequest{
		Definition: agentcatalog.MustParseReference("research.executor@7"),
		Input:      json.RawMessage(`{"url":"https://example.com/article"}`),
	})
	if err != nil || result.Status != ActionSucceeded || mediaAcquirer.calls != 0 || legacy.request.URL != "https://example.com/article" {
		t.Fatalf("result=%+v err=%v media_calls=%d legacy_request=%+v", result, err, mediaAcquirer.calls, legacy.request)
	}
	var output map[string]json.RawMessage
	if json.Unmarshal(result.Output, &output) != nil || output["media_type"] != nil || output["document_handle"] != nil {
		t.Fatalf("legacy output=%s", result.Output)
	}
}

func TestPinnedV9ResearchReadURLRedirectsPDFToPermanentSourceImportWithoutBodyOrHandle(t *testing.T) {
	pdf := researchTextPDF("This body must never enter the v9 read_url result.")
	acquirer := &acquiringStub{content: webreader.Content{
		MediaType: webreader.MediaTypePDF, FinalURL: "https://cdn.example.com/paper.pdf", PDF: pdf,
	}}
	v8Reader := NewResearchURLContentReader(acquirer, nil, nil, nil, ResearchURLReaderConfig{})
	action := NewVersionedResearchURLActions(v8Reader, &webReaderStub{}, acquirer)[0]

	result, err := action.Execute(context.Background(), ActionRequest{
		Definition: agentcatalog.MustParseReference("research.executor@9"),
		Input:      json.RawMessage(`{"url":"https://example.com/paper"}`),
	})
	if err != nil || result.Status != ActionSucceeded {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	var output map[string]json.RawMessage
	if json.Unmarshal(result.Output, &output) != nil || string(output["outcome"]) != `"pdf_requires_source_import"` ||
		string(output["requested_url"]) != `"https://example.com/paper"` ||
		string(output["final_url"]) != `"https://cdn.example.com/paper.pdf"` ||
		string(output["media_type"]) != `"application/pdf"` {
		t.Fatalf("output=%s", result.Output)
	}
	for _, forbidden := range [][]byte{
		[]byte("This body must never"), []byte("nano-pdf-page"), []byte(`"markdown"`),
		[]byte(`"document_handle"`), []byte(`"page_count"`), []byte(`"pdf"`),
	} {
		if bytes.Contains(result.Output, forbidden) {
			t.Fatalf("v9 read_url leaked %q: %s", forbidden, result.Output)
		}
	}
}

func TestPinnedV9ResearchReadURLKeepsBoundedHTMLBehavior(t *testing.T) {
	acquirer := &acquiringStub{content: webreader.Content{
		MediaType: webreader.MediaTypeHTML,
		Page: webreader.Page{
			Title: "HTML evidence", FinalURL: "https://example.com/article", Content: "# HTML\n\nBounded evidence.",
			Engine: "lightweight", WordCount: 3,
		},
	}}
	action := NewVersionedResearchURLActions(nil, &webReaderStub{}, acquirer)[0]
	result, err := action.Execute(context.Background(), ActionRequest{
		Definition: agentcatalog.MustParseReference("research.executor@9"),
		Input:      json.RawMessage(`{"url":"https://example.com/article"}`),
	})
	if err != nil || result.Status != ActionSucceeded || acquirer.calls != 1 {
		t.Fatalf("result=%+v calls=%d err=%v", result, acquirer.calls, err)
	}
	var output readURLOutput
	if json.Unmarshal(result.Output, &output) != nil || output.Markdown != "# HTML\n\nBounded evidence." ||
		output.MediaType != webreader.MediaTypeHTML || output.FinalURL != "https://example.com/article" {
		t.Fatalf("output=%s", result.Output)
	}
}

func (s *webReaderStub) Parse(_ context.Context, request webreader.Request) (webreader.Page, error) {
	s.request = request
	return s.page, s.err
}

func TestReadURLActionReturnsCleanMarkdownWithPinnedLimit(t *testing.T) {
	reader := &webReaderStub{page: webreader.Page{
		Title: "Harness", FinalURL: "https://example.com/final", Content: "# Harness\n\nUseful evidence.",
		Engine: "readability", WordCount: 3, Truncated: false,
	}}
	action := NewReadURLAction(reader)
	result, err := action.Execute(context.Background(), ActionRequest{Input: json.RawMessage(`{"url":"https://example.com/article"}`)})
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != ActionSucceeded || reader.request.MaxChars != ResearchReadURLMaxChars || reader.request.Format != webreader.FormatMarkdown {
		t.Fatalf("result=%+v request=%+v", result, reader.request)
	}
	var output struct {
		Title     string `json:"title"`
		FinalURL  string `json:"final_url"`
		Markdown  string `json:"markdown"`
		Engine    string `json:"engine"`
		WordCount int    `json:"word_count"`
		Truncated bool   `json:"truncated"`
	}
	if err := json.Unmarshal(result.Output, &output); err != nil {
		t.Fatal(err)
	}
	if output.Title != "Harness" || output.FinalURL != "https://example.com/final" || output.Markdown != "# Harness\n\nUseful evidence." || output.Engine != "readability" || output.WordCount != 3 || output.Truncated {
		t.Fatalf("output=%+v", output)
	}
}

func TestReadURLActionIsCheckpointReplaySafe(t *testing.T) {
	policy, ok := NewReadURLAction(&webReaderStub{}).(CrashReplayPolicy)
	if !ok || !policy.CrashReplaySafe() {
		t.Fatal("read_url must replay an incomplete checkpoint after a Worker crash")
	}
}

func TestReadURLActionMakesExternalFailureARecoverableToolResult(t *testing.T) {
	action := NewReadURLAction(&webReaderStub{err: errors.New("sidecar unavailable")})
	result, err := action.Execute(context.Background(), ActionRequest{Input: json.RawMessage(`{"url":"https://example.com/article"}`)})
	if err != nil {
		t.Fatalf("external failure escaped Action result: %v", err)
	}
	if result.Status != ActionDomainError || result.ErrorCode != "read_url_failed" {
		t.Fatalf("result=%+v", result)
	}
	if err := result.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestReadURLActionPreservesStableAcquisitionFailureReason(t *testing.T) {
	for _, test := range []struct {
		err  error
		code string
	}{
		{webreader.ErrUnsafeDestination, "read_url_unsafe_destination"},
		{webreader.ErrResponseTooLarge, "read_url_response_too_large"},
		{webreader.ErrDocumentTypeMismatch, "read_url_document_type_mismatch"},
		{webreader.ErrUnsupportedType, "read_url_unsupported_type"},
		{webreader.ErrResponseInvalid, "read_url_response_invalid"},
	} {
		result, err := NewReadURLAction(&webReaderStub{err: test.err}).Execute(
			context.Background(), ActionRequest{Input: json.RawMessage(`{"url":"https://example.com/article"}`)},
		)
		if err != nil || result.Status != ActionDomainError || result.ErrorCode != test.code {
			t.Fatalf("source=%v result=%+v err=%v", test.err, result, err)
		}
	}
}

func TestReadURLActionRejectsMutableOrUnsafeInput(t *testing.T) {
	action := NewReadURLAction(&webReaderStub{})
	for _, input := range []string{
		`{"url":"http://user:password@example.com/article"}`,
		`{"url":"https://example.com/article#fragment"}`,
		`{"url":"file:///tmp/article"}`,
		`{"url":"https://example.com","max_chars":250000}`,
	} {
		if err := action.ValidateInput(json.RawMessage(input)); err == nil {
			t.Fatalf("accepted %s", input)
		}
	}
}

type arxivAcquirer struct {
	readable map[string]bool
	requests []string
}

func (a *arxivAcquirer) Acquire(_ context.Context, request webreader.Request) (webreader.Content, error) {
	a.requests = append(a.requests, request.URL)
	if !a.readable[request.URL] {
		return webreader.Content{}, errors.New("page has no extractable main content")
	}
	return webreader.Content{MediaType: webreader.MediaTypeHTML, Page: webreader.Page{Title: "Self-RAG", FinalURL: request.URL, Content: "Self-RAG trains a single LM to retrieve, generate, and critique.", WordCount: 11}}, nil
}

func TestReadURLReadsArxivPapersThroughTheirHTMLRendering(t *testing.T) {
	request := func(url string) ActionRequest {
		return ActionRequest{Input: json.RawMessage(`{"url":"` + url + `"}`), Definition: agentcatalog.MustParseReference("research.executor@20")}
	}
	acquirer := &arxivAcquirer{readable: map[string]bool{"https://ar5iv.labs.arxiv.org/html/2310.11511": true}}
	action := &readURLAction{sourceFirst: acquirer}
	result, err := action.Execute(context.Background(), request("https://arxiv.org/abs/2310.11511"))
	if err != nil || result.Status != ActionSucceeded {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	var output readURLOutput
	_ = json.Unmarshal(result.Output, &output)
	if output.FinalURL != "https://ar5iv.labs.arxiv.org/html/2310.11511" || !strings.Contains(output.Markdown, "critique") {
		t.Fatalf("output=%+v", output)
	}
	if want := []string{"https://arxiv.org/html/2310.11511", "https://ar5iv.labs.arxiv.org/html/2310.11511"}; strings.Join(acquirer.requests, " ") != strings.Join(want, " ") {
		t.Fatalf("requests=%v", acquirer.requests)
	}

	for url, want := range map[string][]string{
		"https://arxiv.org/pdf/2510.22344v1":     {"https://arxiv.org/html/2510.22344v1", "https://ar5iv.labs.arxiv.org/html/2510.22344v1", "https://arxiv.org/pdf/2510.22344v1"},
		"https://arxiv.org/pdf/2510.22344v1.pdf": {"https://arxiv.org/html/2510.22344v1", "https://ar5iv.labs.arxiv.org/html/2510.22344v1", "https://arxiv.org/pdf/2510.22344v1.pdf"},
		"https://arxiv.org/abs/cs/0112017":       {"https://arxiv.org/html/cs/0112017", "https://ar5iv.labs.arxiv.org/html/cs/0112017", "https://arxiv.org/abs/cs/0112017"},
		"https://example.com/arxiv.org/abs/1":    {"https://example.com/arxiv.org/abs/1"},
	} {
		if got := arxivReadableCandidates(url); strings.Join(got, " ") != strings.Join(want, " ") {
			t.Fatalf("%s candidates=%v", url, got)
		}
	}

	failing := &arxivAcquirer{}
	result, err = (&readURLAction{sourceFirst: failing}).Execute(context.Background(), request("https://arxiv.org/abs/2310.11511"))
	if err != nil || result.Status != ActionDomainError || len(failing.requests) != 3 {
		t.Fatalf("all candidates failing result=%+v requests=%v err=%v", result, failing.requests, err)
	}
}
