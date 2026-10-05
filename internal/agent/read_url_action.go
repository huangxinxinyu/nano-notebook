package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/huangxinxinyu/nano-notebook/internal/agentcatalog"
	"github.com/huangxinxinyu/nano-notebook/internal/models"
	"github.com/huangxinxinyu/nano-notebook/internal/webreader"
)

const ResearchReadURLMaxChars = 120_000

type readURLAction struct {
	reader      researchURLReader
	htmlReader  webreader.Adapter
	sourceFirst webreader.Acquirer
	// readerSpawner is the spawn_agent Action used to hand a long document
	// to a reader subagent instead of returning its full text.
	readerSpawner Action
}

type researchURLReader interface {
	Read(context.Context, ResearchURLReadRequest) (ResearchURLReadResult, error)
	ReadPages(context.Context, ResearchPageReadRequest) (ResearchPageReadResult, error)
}

type legacyResearchURLReader struct{ adapter webreader.Adapter }

type readURLInput struct {
	URL string `json:"url"`
}

type readURLOutput struct {
	Outcome        string `json:"outcome,omitempty"`
	RequestedURL   string `json:"requested_url,omitempty"`
	ReaderAgentID  string `json:"reader_agent_id,omitempty"`
	Note           string `json:"note,omitempty"`
	Title          string `json:"title"`
	FinalURL       string `json:"final_url"`
	Markdown       string `json:"markdown"`
	Engine         string `json:"engine"`
	WordCount      int    `json:"word_count"`
	Truncated      bool   `json:"truncated"`
	MediaType      string `json:"media_type,omitempty"`
	PageCount      int    `json:"page_count,omitempty"`
	DocumentHandle string `json:"document_handle,omitempty"`
}

func isResearchPDFImportRequired(output readURLOutput) bool {
	return output.Outcome == "pdf_requires_source_import" &&
		output.MediaType == webreader.MediaTypePDF && output.Markdown == ""
}

func NewReadURLAction(reader webreader.Adapter) Action {
	return &readURLAction{reader: legacyResearchURLReader{adapter: reader}}
}

func NewResearchURLActions(reader *ResearchURLContentReader, legacyHTML ...webreader.Adapter) []Action {
	var htmlReader webreader.Adapter
	if len(legacyHTML) > 0 {
		htmlReader = legacyHTML[0]
	}
	return []Action{&readURLAction{reader: reader, htmlReader: htmlReader}, &readDocumentPagesAction{reader: reader}}
}

func NewVersionedResearchURLActions(reader *ResearchURLContentReader, legacyHTML webreader.Adapter, sourceFirst webreader.Acquirer) []Action {
	return []Action{
		&readURLAction{reader: reader, htmlReader: legacyHTML, sourceFirst: sourceFirst},
		&readDocumentPagesAction{reader: reader},
	}
}

func (*readURLAction) CrashReplaySafe() bool { return true }

func (*readURLAction) CacheLongToolResults(definition agentcatalog.Reference) bool {
	return definition.Identity == "research.executor" && definition.Version >= 14
}

func (*readURLAction) Definition() models.ActionDefinition {
	return models.ActionDefinition{
		Name:        "read_url",
		Description: "Fetch one public HTTP or HTTPS URL and return cleaned Markdown for substantive evidence analysis. A long successful result may include a bounded preview and result_ref; use read_tool_result with its next_offset to continue.",
		InputSchema: json.RawMessage(`{"type":"object","additionalProperties":false,"required":["url"],"properties":{"url":{"type":"string","minLength":1,"maxLength":4096,"pattern":"^https?://"}}}`),
	}
}

func (*readURLAction) ValidateInput(raw json.RawMessage) error {
	_, err := decodeReadURLInput(raw)
	return err
}

func (a *readURLAction) Execute(ctx context.Context, request ActionRequest) (ActionResult, error) {
	input, err := decodeReadURLInput(request.Input)
	if err != nil {
		return ActionResult{}, err
	}
	if err := ctx.Err(); err != nil {
		return ActionResult{}, err
	}
	if a == nil {
		return ActionResult{Status: ActionDomainError, ErrorCode: "read_url_unavailable"}, nil
	}
	if request.Definition.Identity == "research.executor" && request.Definition.Version >= 9 {
		return a.executeSourceFirst(ctx, request, input)
	}
	if a.reader == nil {
		return ActionResult{Status: ActionDomainError, ErrorCode: "read_url_unavailable"}, nil
	}
	reader := a.reader
	if a.htmlReader != nil && request.Definition.String() != "research.executor@8" {
		reader = legacyResearchURLReader{adapter: a.htmlReader}
	}
	read, err := reader.Read(ctx, ResearchURLReadRequest{
		URL: input.URL, RunID: request.Attempt.RunID, ActionID: request.ActionID,
	})
	if err != nil {
		if contextErr := ctx.Err(); contextErr != nil {
			return ActionResult{}, contextErr
		}
		return ActionResult{Status: ActionDomainError, ErrorCode: classifyReadURLError(err)}, nil
	}
	payload, err := json.Marshal(readURLOutput{
		Title: read.Title, FinalURL: read.FinalURL, Markdown: read.Markdown,
		Engine: read.Engine, WordCount: read.WordCount, Truncated: read.Truncated,
		MediaType: read.MediaType, PageCount: read.PageCount, DocumentHandle: read.DocumentHandle,
	})
	if err != nil {
		return ActionResult{}, err
	}
	return ActionResult{Status: ActionSucceeded, Output: payload}, nil
}

func (a *readURLAction) executeSourceFirst(ctx context.Context, request ActionRequest, input readURLInput) (ActionResult, error) {
	if a.sourceFirst == nil {
		return ActionResult{Status: ActionDomainError, ErrorCode: "read_url_unavailable"}, nil
	}
	content, err := a.acquireReadable(ctx, input.URL)
	if err != nil {
		if contextErr := ctx.Err(); contextErr != nil {
			return ActionResult{}, contextErr
		}
		return ActionResult{Status: ActionDomainError, ErrorCode: classifyReadURLError(err)}, nil
	}
	if content.MediaType == webreader.MediaTypePDF {
		payload, err := json.Marshal(struct {
			Outcome      string `json:"outcome"`
			RequestedURL string `json:"requested_url"`
			FinalURL     string `json:"final_url"`
			MediaType    string `json:"media_type"`
		}{
			Outcome: "pdf_requires_source_import", RequestedURL: input.URL,
			FinalURL: content.FinalURL, MediaType: webreader.MediaTypePDF,
		})
		if err != nil {
			return ActionResult{}, err
		}
		return ActionResult{Status: ActionSucceeded, Output: payload}, nil
	}
	if content.MediaType != webreader.MediaTypeHTML {
		return ActionResult{Status: ActionDomainError, ErrorCode: "read_url_response_invalid"}, nil
	}
	if delegated, ok, err := a.delegateLongRead(ctx, request, input.URL, content.Page); err != nil || ok {
		return delegated, err
	}
	payload, err := json.Marshal(readURLOutput{
		Title: content.Page.Title, FinalURL: content.Page.FinalURL, Markdown: content.Page.Content,
		Engine: content.Page.Engine, WordCount: content.Page.WordCount, Truncated: content.Page.Truncated,
		MediaType: webreader.MediaTypeHTML,
	})
	if err != nil {
		return ActionResult{}, err
	}
	return ActionResult{Status: ActionSucceeded, Output: payload}, nil
}

// arXiv abstract pages have no extractable main body, and arXiv PDFs need a
// slow Source import. Their HTML renderings read directly, so an arXiv
// abstract or PDF URL is read through arxiv.org/html, then ar5iv. A paper
// with neither rendering falls back to its PDF, which returns
// pdf_requires_source_import with an importable URL instead of a dead end.
// The result's requested_url stays the URL the model asked for.
var arxivPaperURLPattern = regexp.MustCompile(`^https?://(?:www\.|export\.)?arxiv\.org/(?:abs|pdf)/([^?#]+?)(?:\.pdf)?/?(?:[?#].*)?$`)

func arxivReadableCandidates(url string) []string {
	match := arxivPaperURLPattern.FindStringSubmatch(strings.TrimSpace(url))
	if match == nil {
		return []string{url}
	}
	id := match[1]
	return []string{"https://arxiv.org/html/" + id, "https://ar5iv.labs.arxiv.org/html/" + id, "https://arxiv.org/pdf/" + id}
}

func (a *readURLAction) acquireReadable(ctx context.Context, url string) (webreader.Content, error) {
	var lastErr error
	for _, candidate := range arxivReadableCandidates(url) {
		content, err := a.sourceFirst.Acquire(ctx, webreader.Request{
			URL: candidate, Format: webreader.FormatMarkdown, MaxChars: ResearchReadURLMaxChars,
		})
		if err == nil {
			return content, nil
		}
		if ctx.Err() != nil {
			return webreader.Content{}, err
		}
		lastErr = err
	}
	return webreader.Content{}, lastErr
}

const (
	researchReaderTaskPrefix = "Read one long document for the parent researcher, in full:"
	researchReaderOutcome    = "delegated_to_reader"
	// researchReaderCapacityOutcome marks an excerpt returned because every
	// reader slot was busy.
	researchReaderCapacityOutcome = "reader_capacity_excerpt"
	researchReaderExcerptRunes    = 8_000
	researchDelegatedReadMinRune  = 30_000
	// researchQueuedReaderVersion is the first executor version whose
	// capacity excerpts are queued for automatic reader dispatch.
	researchQueuedReaderVersion = 27
	// researchQuestionTagVersion is the first executor version whose readers
	// tag claim cards with plan research questions.
	researchQuestionTagVersion = 28
)

// SetResearchReaderSpawner lets a research read_url hand long documents to
// reader subagents through the given spawn_agent Action.
func SetResearchReaderSpawner(readURL, spawner Action) error {
	action, ok := readURL.(*readURLAction)
	if !ok || spawner == nil || spawner.Definition().Name != "spawn_agent" {
		return errors.New("research reader delegation requires read_url and spawn_agent")
	}
	action.readerSpawner = spawner
	return nil
}

// delegateLongRead keeps a long document's full text out of a root
// Researcher's context. From executor v22 the root hands it to a reader
// subagent that pages through it and records claim cards, while the root
// keeps discovering. Children cannot delegate, and a full subagent tree
// falls back to returning the text, so delegation never loses a read.
func (a *readURLAction) delegateLongRead(ctx context.Context, request ActionRequest, requestedURL string, page webreader.Page) (ActionResult, bool, error) {
	if a.readerSpawner == nil || request.Definition.Identity != "research.executor" || request.Definition.Version < 22 ||
		utf8.RuneCountInString(page.Content) < researchDelegatedReadMinRune {
		return ActionResult{}, false, nil
	}
	input, err := json.Marshal(researchReaderSpawnInput(requestedURL, page.Title, request.Definition.Version))
	if err != nil {
		return ActionResult{}, false, err
	}
	spawnRequest := request
	spawnRequest.Input = input
	spawned, err := a.readerSpawner.Execute(ctx, spawnRequest)
	if err != nil {
		return ActionResult{}, false, err
	}
	if spawned.Status != ActionSucceeded {
		if researchReaderCapacityExhausted(spawned) {
			return researchReaderCapacityExcerpt(requestedURL, page, request.Definition.Version >= researchQueuedReaderVersion)
		}
		return ActionResult{}, false, nil
	}
	var agent struct {
		AgentID string `json:"agent_id"`
	}
	if json.Unmarshal(spawned.Output, &agent) != nil || agent.AgentID == "" {
		return ActionResult{}, false, nil
	}
	payload, err := json.Marshal(readURLOutput{
		Outcome: researchReaderOutcome, RequestedURL: requestedURL, Title: page.Title, FinalURL: page.FinalURL,
		WordCount: page.WordCount, MediaType: webreader.MediaTypeHTML, ReaderAgentID: agent.AgentID,
		Note: "This long document was handed to a reader subagent, which reads it in full and records claim cards. Do not read it yourself. Keep discovering and reading other sources, collect the reader with wait_agent before drafting, and use its cards from claims.md.",
	})
	if err != nil {
		return ActionResult{}, false, err
	}
	return ActionResult{Status: ActionSucceeded, Output: payload}, true, nil
}

// researchReaderSpawnInput is the reader task for one long document, shared by
// immediate delegation and by readers dispatched later from the queue. From
// executor v28 readers also tag each card with its plan research question.
func researchReaderSpawnInput(requestedURL, title string, definitionVersion int) spawnAgentInput {
	title = strings.TrimSpace(title)
	if title == "" {
		title = requestedURL
	}
	taskName := title
	if runes := []rune(taskName); len(runes) > 72 {
		taskName = string(runes[:72])
	}
	message := fmt.Sprintf(researchReaderTaskPrefix+" %s (%s).\n\n"+
		"Call read_url on exactly that URL, then page through the rest with read_tool_result at each next_offset until complete=true. "+
		"For every fact relevant to the accepted Research Plan, such as methods, settings, results and numbers, comparisons, and stated limitations, record a claim card with record_claim using that URL as source and a verbatim quote. "+
		"A card exists only when record_claim returns its id; naming cards in your Final records nothing. "+
		"Do not search for or read other sources. Return Final with: what the document is, each recorded card id with the claim it supports, and anything relevant you could not capture.", requestedURL, title)
	if definitionVersion >= researchQuestionTagVersion {
		message += " Set question on each card to the number of the plan research question it helps answer, counting from 1, and leave it out only when no question fits."
	}
	if definitionVersion >= researchReaderLeadsVersion {
		message += researchReaderLeadsInstruction
	}
	return spawnAgentInput{Message: message, TaskName: "Read: " + taskName}
}

func researchReaderCapacityExhausted(result ActionResult) bool {
	return result.ErrorCode == "subagent_capacity_exhausted" || (result.Error != nil && result.Error.Code == "subagent_capacity_exhausted")
}

// researchReaderCapacityExcerpt keeps a long document out of the root's
// context even when every reader slot is busy: the root sees an excerpt. From
// executor v27 the document is queued and wait_agent dispatches its reader
// once a slot frees; earlier versions left delegation to the model.
func researchReaderCapacityExcerpt(requestedURL string, page webreader.Page, queued bool) (ActionResult, bool, error) {
	excerpt := []rune(page.Content)
	if len(excerpt) > researchReaderExcerptRunes {
		excerpt = excerpt[:researchReaderExcerptRunes]
	}
	payload, err := json.Marshal(readURLOutput{
		Outcome: researchReaderCapacityOutcome, RequestedURL: requestedURL, Title: page.Title, FinalURL: page.FinalURL,
		Markdown: string(excerpt), Engine: page.Engine, WordCount: page.WordCount, Truncated: true, MediaType: webreader.MediaTypeHTML,
		Note: researchReaderCapacityNote(queued),
	})
	if err != nil {
		return ActionResult{}, false, err
	}
	return ActionResult{Status: ActionSucceeded, Output: payload}, true, nil
}

func researchReaderCapacityNote(queued bool) string {
	if queued {
		return "This long document shows only its opening excerpt because every reader subagent slot is busy. It is queued: the next wait_agent that finds a finished reader hands it to a new reader and lists that reader in dispatched_readers. Do not spawn a reader for it yourself; keep working and collect its cards from claims.md after waiting for that reader."
	}
	return "This long document shows only its opening excerpt because every reader subagent slot is busy. Collect finished readers with wait_agent, then hand this URL to a reader with spawn_agent, asking it to read the document in full and record claim cards with record_claim."
}

func isResearchReaderTask(task string) bool {
	return strings.HasPrefix(task, researchReaderTaskPrefix)
}

func isResearchReaderDelegation(output readURLOutput) bool {
	return output.Outcome == researchReaderOutcome && output.Markdown == ""
}

func classifyReadURLError(err error) string {
	switch {
	case errors.Is(err, webreader.ErrUnsafeDestination):
		return "read_url_unsafe_destination"
	case errors.Is(err, webreader.ErrResponseTooLarge):
		return "read_url_response_too_large"
	case errors.Is(err, webreader.ErrDocumentTypeMismatch):
		return "read_url_document_type_mismatch"
	case errors.Is(err, webreader.ErrUnsupportedType):
		return "read_url_unsupported_type"
	case errors.Is(err, webreader.ErrResponseInvalid):
		return "read_url_response_invalid"
	default:
		return "read_url_failed"
	}
}

func (r legacyResearchURLReader) Read(ctx context.Context, request ResearchURLReadRequest) (ResearchURLReadResult, error) {
	if r.adapter == nil {
		return ResearchURLReadResult{}, errors.New("web reader is unavailable")
	}
	page, err := r.adapter.Parse(ctx, webreader.Request{
		URL: request.URL, Format: webreader.FormatMarkdown, MaxChars: ResearchReadURLMaxChars,
	})
	if err != nil {
		return ResearchURLReadResult{}, err
	}
	return ResearchURLReadResult{
		Title: page.Title, FinalURL: page.FinalURL, Markdown: page.Content, Engine: page.Engine,
		WordCount: page.WordCount, Truncated: page.Truncated,
	}, nil
}

func (legacyResearchURLReader) ReadPages(context.Context, ResearchPageReadRequest) (ResearchPageReadResult, error) {
	return ResearchPageReadResult{}, ErrResearchDocumentNotFound
}

type readDocumentPagesAction struct{ reader researchURLReader }

type readDocumentPagesInput struct {
	DocumentHandle string `json:"document_handle"`
	StartPage      int    `json:"start_page"`
	EndPage        int    `json:"end_page"`
}

type readDocumentPagesOutput struct {
	DocumentHandle string `json:"document_handle"`
	StartPage      int    `json:"start_page"`
	EndPage        int    `json:"end_page"`
	PageCount      int    `json:"page_count"`
	Markdown       string `json:"markdown"`
	Truncated      bool   `json:"truncated"`
}

func (*readDocumentPagesAction) CrashReplaySafe() bool { return true }

func (*readDocumentPagesAction) CacheLongToolResults(definition agentcatalog.Reference) bool {
	return definition.Identity == "research.executor" && definition.Version >= 14
}

func (*readDocumentPagesAction) Definition() models.ActionDefinition {
	return models.ActionDefinition{
		Name:        "read_document_pages",
		Description: "Read an inclusive range of at most 20 pages from a PDF document_handle returned by read_url in this same Research Run. This never performs another network fetch.",
		InputSchema: json.RawMessage(`{"type":"object","additionalProperties":false,"required":["document_handle","start_page","end_page"],"properties":{"document_handle":{"type":"string","pattern":"^rdoc_[a-f0-9]{32}$"},"start_page":{"type":"integer","minimum":1},"end_page":{"type":"integer","minimum":1}}}`),
	}
}

func (*readDocumentPagesAction) ValidateInput(raw json.RawMessage) error {
	_, err := decodeReadDocumentPagesInput(raw)
	return err
}

func (a *readDocumentPagesAction) Execute(ctx context.Context, request ActionRequest) (ActionResult, error) {
	input, err := decodeReadDocumentPagesInput(request.Input)
	if err != nil {
		return ActionResult{}, err
	}
	if err := ctx.Err(); err != nil {
		return ActionResult{}, err
	}
	if a == nil || a.reader == nil {
		return ActionResult{Status: ActionDomainError, ErrorCode: "read_document_unavailable"}, nil
	}
	pages, err := a.reader.ReadPages(ctx, ResearchPageReadRequest{
		RunID: request.Attempt.RunID, DocumentHandle: input.DocumentHandle,
		StartPage: input.StartPage, EndPage: input.EndPage,
	})
	if err != nil {
		if contextErr := ctx.Err(); contextErr != nil {
			return ActionResult{}, contextErr
		}
		code := "read_document_failed"
		if errors.Is(err, ErrResearchDocumentNotFound) {
			code = "research_document_not_found"
		} else if errors.Is(err, ErrResearchPageRangeInvalid) {
			code = "invalid_document_page_range"
		}
		return ActionResult{Status: ActionDomainError, ErrorCode: code}, nil
	}
	payload, err := json.Marshal(readDocumentPagesOutput{
		DocumentHandle: pages.DocumentHandle, StartPage: pages.StartPage, EndPage: pages.EndPage,
		PageCount: pages.PageCount, Markdown: pages.Markdown, Truncated: pages.Truncated,
	})
	if err != nil {
		return ActionResult{}, err
	}
	return ActionResult{Status: ActionSucceeded, Output: payload}, nil
}

func decodeReadDocumentPagesInput(raw json.RawMessage) (readDocumentPagesInput, error) {
	var input readDocumentPagesInput
	if decodeExactJSON(raw, &input) != nil || !researchDocumentHandlePattern.MatchString(input.DocumentHandle) ||
		input.StartPage < 1 || input.EndPage < input.StartPage || input.EndPage-input.StartPage+1 > 20 {
		return readDocumentPagesInput{}, errors.New("invalid read_document_pages input")
	}
	return input, nil
}

func decodeReadURLInput(raw json.RawMessage) (readURLInput, error) {
	var input readURLInput
	if err := decodeExactJSON(raw, &input); err != nil {
		return readURLInput{}, errors.New("invalid read_url input")
	}
	request := webreader.Request{URL: input.URL, Format: webreader.FormatMarkdown, MaxChars: ResearchReadURLMaxChars}
	if err := request.Validate(); err != nil {
		return readURLInput{}, errors.New("invalid read_url input")
	}
	return input, nil
}
