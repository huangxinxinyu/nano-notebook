package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/huangxinxinyu/nano-notebook/internal/models"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/text/unicode/norm"
)

// Claim cards bind one claim to a verbatim quote from a Source the Run has
// actually read. The Harness checks the quote mechanically against the stored
// source text, but never rejects a card: a mismatch is recorded with its status
// so the model can correct it and the report can still cite it.

const (
	recordClaimActionName = "record_claim"
	researchClaimsPath    = "claims.md"

	claimStatusVerified          = "verified"
	claimStatusNearMatch         = "near_match"
	claimStatusNotFound          = "not_found"
	claimStatusSourceUnavailable = "source_unavailable"

	claimQuoteMinRunes        = 8
	claimNearMatchScore       = 0.75
	claimApproximateScore     = 0.92
	claimNearestReportScore   = 0.4
	claimNearestExcerptRunes  = 360
	claimRecordInputMaxQuote  = 2000
	claimRecordInputMaxClaim  = 1000
	claimRecordInputMaxCondit = 500
	claimRecordMaxQuestion    = 30
)

// researchClaimCitationElement matches one cited card id or card range.
const researchClaimCitationElement = `(?:(?:[abd-z]{1,2}|c)[1-9][0-9]*\s*[-–—~]\s*(?:[abd-z]{1,2}|c)?[1-9][0-9]*|[abd-z]{1,2}[1-9][0-9]*|(?:c?[0-9a-f]{4}-?)?c[1-9][0-9]*)`

// researchClaimMaxRange bounds how many cards one cited range expands to.
const researchClaimMaxRange = 30

var (
	// Root cards are c1, c2, ...; each subagent's cards take a letter
	// namespace in spawn order (a1, b2, ..., aa3). Earlier runs used hex namespaces (16d1-c3).
	researchClaimIDPattern = regexp.MustCompile(`^(?:(?:[0-9a-f]{4}-)?c|[abd-z]{1,2})[1-9][0-9]*$`)
	// Citations also accept the variants models write for hex child ids, such
	// as c16d1-c3 or 16d1c3 for 16d1-c3; canonicalClaimID maps them back.
	researchClaimCitationPattern = regexp.MustCompile(`\[(` + researchClaimCitationElement + `(?:\s*[,，;；、]\s*` + researchClaimCitationElement + `)*)\]`)
	// A range such as [g2-g4] or [c3–5] cites every card between its ends.
	researchClaimRangePattern   = regexp.MustCompile(`^([abd-z]{1,2}|c)([1-9][0-9]*)\s*[-–—~]\s*(?:[abd-z]{1,2}|c)?([1-9][0-9]*)$`)
	childClaimIDVariantPattern  = regexp.MustCompile(`^c?([0-9a-f]{4})-?c([1-9][0-9]*)$`)
	researchClaimIDSplitPattern = regexp.MustCompile(`\s*[,，;；、]\s*`)
	claimMarkdownLinkURLPattern = regexp.MustCompile(`\]\([^)\s]*\)`)
	claimEllipsisPattern        = regexp.MustCompile(`\.{3,}|…+`)
	claimTeXCommandPattern      = regexp.MustCompile(`\\[A-Za-z]+`)
)

type recordClaimInput struct {
	Source     string `json:"source"`
	Quote      string `json:"quote"`
	Claim      string `json:"claim"`
	Conditions string `json:"conditions,omitempty"`
	Question   int    `json:"question,omitempty"`
}

type recordClaimOutput struct {
	ID             string `json:"id"`
	Status         string `json:"status"`
	Source         string `json:"source"`
	Title          string `json:"title,omitempty"`
	URL            string `json:"url,omitempty"`
	NearestExcerpt string `json:"nearest_excerpt,omitempty"`
	Note           string `json:"note"`
}

type researchClaim struct {
	recordClaimOutput
	Quote      string
	Claim      string
	Conditions string
	Question   int
}

type researchClaimRun struct {
	RunID     string
	Namespace string
	Prefix    CheckpointPrefix
}

// researchClaimTree is the root Research Run followed by its runtime
// subagents. Children cannot delegate, so the tree has at most two levels.
type researchClaimTree struct {
	Runs []researchClaimRun
}

type researchClaimSourceText struct {
	Title string
	URL   string
	Text  string
}

type researchClaimBackend interface {
	ClaimTree(ctx context.Context, runID string) (researchClaimTree, error)
	SourceText(ctx context.Context, sourceID, revisionID string) (researchClaimSourceText, bool, error)
}

type recordClaimAction struct {
	backend     researchClaimBackend
	toolResults *ToolResultReader
}

func NewRecordClaimAction(pool *pgxpool.Pool, toolResults *ToolResultReader) Action {
	return &recordClaimAction{backend: postgresResearchClaimBackend{pool: pool}, toolResults: toolResults}
}

func (*recordClaimAction) CrashReplaySafe() bool { return true }

func (a *recordClaimAction) Available(Execution) (bool, string) {
	return a != nil && a.backend != nil, "research_claims_unavailable"
}

func (*recordClaimAction) Definition() models.ActionDefinition {
	return models.ActionDefinition{
		Name:        recordClaimActionName,
		Description: "Record one claim card: a verbatim quote from a Source read in this Run (a read_url page URL or a search_evidence source_id) and the claim it supports. Returns a card id such as c3 and whether the quote was found in the stored source text; a mismatch is still recorded. Cite cards in report prose as [c3]. All cards are listed in claims.md.",
		InputSchema: json.RawMessage(fmt.Sprintf(`{"type":"object","additionalProperties":false,"required":["source","quote","claim"],"properties":{"source":{"type":"string","minLength":1,"maxLength":4096,"description":"The read page URL, or the Notebook source_id."},"quote":{"type":"string","minLength":1,"maxLength":%d,"description":"Verbatim source text; use ... to elide inside a long passage."},"claim":{"type":"string","minLength":1,"maxLength":%d,"description":"What this quote establishes, in the report language."},"conditions":{"type":"string","maxLength":%d,"description":"Scope, setting, version, or caveat that limits the claim."},"question":{"type":"integer","minimum":1,"maximum":%d,"description":"Number of the accepted plan's research question this card helps answer, counting from 1; assembly reports coverage per question."}}}`,
			claimRecordInputMaxQuote, claimRecordInputMaxClaim, claimRecordInputMaxCondit, claimRecordMaxQuestion)),
	}
}

func (*recordClaimAction) ValidateInput(raw json.RawMessage) error {
	_, err := decodeRecordClaimInput(raw)
	return err
}

func (a *recordClaimAction) Execute(ctx context.Context, request ActionRequest) (ActionResult, error) {
	input, err := decodeRecordClaimInput(request.Input)
	if err != nil {
		return ActionResult{}, err
	}
	if a == nil || a.backend == nil {
		return ActionResult{Status: ActionDomainError, ErrorCode: "research_claims_unavailable"}, nil
	}
	tree, err := a.backend.ClaimTree(ctx, request.Attempt.RunID)
	if err != nil {
		return ActionResult{}, err
	}
	current, ok := tree.run(request.Attempt.RunID)
	if !ok {
		return ActionResult{}, errors.New("Research claim tree does not contain the current Run")
	}
	output := recordClaimOutput{ID: researchClaimID(current, request.ActionID), Source: input.Source}
	source, found, err := a.resolveSource(ctx, request, tree, input.Source)
	if err != nil {
		return ActionResult{}, err
	}
	output.Title, output.URL = source.Title, source.URL
	if !found {
		output.Status = claimStatusSourceUnavailable
		output.Note = "No read text for this Source exists in this Run. Read the page with read_url (or retrieve passages with search_evidence) first; the card is kept but cannot be checked."
	} else {
		match := matchClaimQuote(source.Text, input.Quote)
		output.Status, output.NearestExcerpt = match.Status, match.Nearest
		switch match.Status {
		case claimStatusVerified:
			output.Note = "Quote found in the source text."
		case claimStatusNearMatch:
			output.Note = "Quote is close to, but not exactly, the source text. Compare with nearest_excerpt and record a corrected card if wording matters."
		default:
			output.Note = "Quote was not found in the read source text. Copy the wording from the source, or treat this claim as unsupported."
		}
	}
	payload, err := json.Marshal(output)
	if err != nil {
		return ActionResult{}, err
	}
	return ActionResult{Status: ActionSucceeded, Output: payload}, nil
}

func decodeRecordClaimInput(raw json.RawMessage) (recordClaimInput, error) {
	var input recordClaimInput
	if decodeExactJSON(raw, &input) != nil {
		return recordClaimInput{}, errors.New("invalid record_claim input")
	}
	input.Source = strings.TrimSpace(input.Source)
	input.Quote = strings.TrimSpace(input.Quote)
	input.Claim = strings.TrimSpace(input.Claim)
	input.Conditions = strings.TrimSpace(input.Conditions)
	if input.Source == "" || len(input.Source) > 4096 || input.Quote == "" || input.Claim == "" ||
		utf8.RuneCountInString(input.Quote) > claimRecordInputMaxQuote || utf8.RuneCountInString(input.Claim) > claimRecordInputMaxClaim ||
		utf8.RuneCountInString(input.Conditions) > claimRecordInputMaxCondit {
		return recordClaimInput{}, errors.New("invalid record_claim input")
	}
	if input.Question < 0 || input.Question > claimRecordMaxQuestion {
		return recordClaimInput{}, fmt.Errorf("invalid record_claim input: question must be a research question number from 1 to %d", claimRecordMaxQuestion)
	}
	return input, nil
}

func (t researchClaimTree) run(runID string) (researchClaimRun, bool) {
	for _, run := range t.Runs {
		if run.RunID == runID {
			return run, true
		}
	}
	return researchClaimRun{}, false
}

// researchClaimID numbers record_claim proposals in decision order, so the id
// is known before execution and identical across crash replay. Child Runs
// carry a one-letter namespace so their cards never collide with the root's.
func researchClaimID(run researchClaimRun, actionID string) string {
	ordinal := 0
	for _, proposal := range run.Prefix.Proposals {
		for _, action := range proposal.Actions {
			if action.Name != recordClaimActionName {
				continue
			}
			ordinal++
			if action.ActionID == actionID {
				return researchClaimNamespacedID(run.Namespace, ordinal)
			}
		}
	}
	return researchClaimNamespacedID(run.Namespace, ordinal+1)
}

func researchClaimNamespacedID(namespace string, ordinal int) string {
	if namespace == "" {
		return "c" + strconv.Itoa(ordinal)
	}
	return namespace + strconv.Itoa(ordinal)
}

// researchChildClaimLetters names subagents in spawn order, one letter for the
// first 25 and two letters (aa, ab, ...) after them; c is reserved for the
// root's cards.
const researchChildClaimLetters = "abdefghijklmnopqrstuvwxyz"

func researchClaimNamespace(childIndex int) string {
	letters := len(researchChildClaimLetters)
	switch {
	case childIndex < 0 || childIndex >= letters*(letters+1):
		return ""
	case childIndex < letters:
		return researchChildClaimLetters[childIndex : childIndex+1]
	default:
		first, second := childIndex/letters-1, childIndex%letters
		return researchChildClaimLetters[first:first+1] + researchChildClaimLetters[second:second+1]
	}
}

func (a *recordClaimAction) resolveSource(ctx context.Context, request ActionRequest, tree researchClaimTree, source string) (researchClaimSourceText, bool, error) {
	if strings.HasPrefix(source, "http://") || strings.HasPrefix(source, "https://") {
		return a.readURLSourceText(ctx, request, tree, source)
	}
	for _, run := range tree.Runs {
		for reference := range searchedResearchSourceEvidence(run.Prefix) {
			if reference.SourceID != source {
				continue
			}
			text, ok, err := a.backend.SourceText(ctx, reference.SourceID, reference.RevisionID)
			if err != nil || ok {
				return text, ok, err
			}
		}
	}
	return researchClaimSourceText{}, false, nil
}

// readURLSourceText rebuilds what read_url returned for this URL anywhere in
// the Research tree: the inline checkpoint body, the full externalized body
// while it is still cached, or else its checkpointed preview plus any pages the
// model read back with read_tool_result.
func (a *recordClaimAction) readURLSourceText(ctx context.Context, request ActionRequest, tree researchClaimTree, url string) (researchClaimSourceText, bool, error) {
	// A card may name any URL of the read document: readers read an arXiv
	// abs page but often cite the html page it resolved to.
	want := researchDocumentKey(url)
	var result researchClaimSourceText
	var parts []string
	for _, run := range tree.Runs {
		pages := researchClaimToolResultPages(run.Prefix)
		for _, proposal := range run.Prefix.Proposals {
			for _, action := range proposal.Actions {
				if action.Name != "read_url" || action.Result == nil || action.Result.Status != ActionSucceeded {
					continue
				}
				var input readURLInput
				_ = json.Unmarshal(action.Input, &input)
				var projection ToolResultProjection
				if json.Unmarshal(action.Result.Output, &projection) == nil && projection.ContentState != "" {
					if researchDocumentKey(input.URL) != want {
						continue
					}
					if hydrated, ok, err := a.hydrateReadURL(ctx, request, run.RunID, proposal.DecisionNo, action); err != nil {
						return researchClaimSourceText{}, false, err
					} else if ok {
						action = hydrated
					}
				}
				if json.Unmarshal(action.Result.Output, &projection) == nil && projection.ContentState != "" {
					body := projection.Preview + pages[projection.ResultRef]
					parts = append(parts, unescapeClaimJSONFragment(body))
					if result.URL == "" {
						result.URL = input.URL
					}
					continue
				}
				var output readURLOutput
				if json.Unmarshal(action.Result.Output, &output) != nil || output.Markdown == "" {
					continue
				}
				if researchDocumentKey(input.URL) != want && researchDocumentKey(output.FinalURL) != want && researchDocumentKey(output.RequestedURL) != want {
					continue
				}
				parts = append(parts, output.Title+"\n\n"+output.Markdown)
				if result.Title == "" {
					result.Title = output.Title
				}
				result.URL = input.URL
				if output.FinalURL != "" {
					result.URL = output.FinalURL
				}
			}
		}
	}
	if len(parts) == 0 {
		return researchClaimSourceText{URL: url}, false, nil
	}
	result.Text = strings.Join(parts, "\n\n")
	return result, true, nil
}

// hydrateReadURL fetches one externalized read_url body while it is still
// cached; an expired or corrupt cache entry falls back to the checkpoint.
func (a *recordClaimAction) hydrateReadURL(ctx context.Context, request ActionRequest, runID string, decisionNo int, action AcceptedAction) (AcceptedAction, bool, error) {
	if a.toolResults == nil {
		return action, false, nil
	}
	hydrated, err := hydrateExternalizedResearchProposal(ctx, *a.toolResults, ToolResultScope{
		UserID: request.UserID, ChatID: request.ChatID, RunID: runID,
	}, AcceptedProposal{DecisionNo: decisionNo, Actions: []AcceptedAction{action}})
	if errors.Is(err, ErrToolResultCorrupt) || errors.Is(err, ErrToolResultUnauthorized) {
		return action, false, nil
	}
	if err != nil {
		return action, false, err
	}
	return hydrated.Actions[0], true, nil
}

// researchClaimToolResultPages joins read_tool_result pages by result_ref in
// offset order, skipping overlapping or repeated ranges.
func researchClaimToolResultPages(prefix CheckpointPrefix) map[string]string {
	type page struct {
		offset  int
		content string
	}
	byRef := map[string][]page{}
	for _, proposal := range prefix.Proposals {
		for _, action := range proposal.Actions {
			if action.Name != "read_tool_result" || action.Result == nil || action.Result.Status != ActionSucceeded {
				continue
			}
			var output ToolResultPage
			if json.Unmarshal(action.Result.Output, &output) != nil || output.ResultRef == "" {
				continue
			}
			byRef[output.ResultRef] = append(byRef[output.ResultRef], page{offset: output.Offset, content: output.Content})
		}
	}
	joined := make(map[string]string, len(byRef))
	for ref, pages := range byRef {
		sort.SliceStable(pages, func(i, j int) bool { return pages[i].offset < pages[j].offset })
		var builder strings.Builder
		next := -1
		for _, page := range pages {
			if page.offset < next {
				continue
			}
			builder.WriteString(page.content)
			next = page.offset + len(page.content)
		}
		joined[ref] = builder.String()
	}
	return joined
}

func normalizeClaimURL(value string) string {
	value = strings.TrimSpace(value)
	if index := strings.IndexByte(value, '#'); index >= 0 {
		value = value[:index]
	}
	value = strings.TrimSuffix(value, "/")
	if scheme := strings.Index(value, "://"); scheme >= 0 {
		rest := value[scheme+3:]
		host, path := rest, ""
		if slash := strings.IndexByte(rest, '/'); slash >= 0 {
			host, path = rest[:slash], rest[slash:]
		}
		value = strings.ToLower(value[:scheme]) + "://" + strings.TrimPrefix(strings.ToLower(host), "www.") + path
	}
	return value
}

// unescapeClaimJSONFragment decodes JSON string escapes in a byte range of a
// serialized Tool Result. The range may start or end mid-escape; unknown or
// truncated escapes are dropped because matching ignores punctuation anyway.
func unescapeClaimJSONFragment(fragment string) string {
	var builder strings.Builder
	builder.Grow(len(fragment))
	for index := 0; index < len(fragment); index++ {
		character := fragment[index]
		if character != '\\' || index+1 >= len(fragment) {
			builder.WriteByte(character)
			continue
		}
		index++
		switch fragment[index] {
		case 'n', 'r', 't':
			builder.WriteByte(' ')
		case '"', '\\', '/':
			builder.WriteByte(fragment[index])
		case 'u':
			end := index + 5
			if end > len(fragment) {
				end = len(fragment)
			}
			if code, err := strconv.ParseUint(fragment[index+1:end], 16, 32); err == nil && end-index == 5 {
				builder.WriteRune(rune(code))
			}
			index = end - 1
		}
	}
	return builder.String()
}

type claimQuoteMatch struct {
	Status  string
	Nearest string
}

type normalizedClaimText struct {
	runes   []rune
	offsets []int // byte offset of each normalized rune in the original text
}

// normalizeClaimText keeps only lower-cased letters and digits, so whitespace,
// punctuation, Markdown syntax, and full-width forms never decide a match.
func normalizeClaimText(text string) normalizedClaimText {
	normalized := normalizedClaimText{runes: make([]rune, 0, len(text)), offsets: make([]int, 0, len(text))}
	for offset, character := range text {
		if character >= 0xFF01 && character <= 0xFF5E {
			character -= 0xFEE0
		}
		if !unicode.IsLetter(character) && !unicode.IsDigit(character) {
			continue
		}
		normalized.runes = append(normalized.runes, unicode.ToLower(character))
		normalized.offsets = append(normalized.offsets, offset)
	}
	return normalized
}

// cleanClaimMathText undoes how arXiv HTML extraction renders math: each
// formula appears twice, as rendered symbols and as its TeX source, so
// "0.182 nats" reads "0.1820.182 nats" and "in-house C" reads
// "in-house 𝒞\mathcal{C}". TeX commands and braces go, math alphanumerics
// fold to plain letters, and a token made of one run repeated twice keeps
// one copy. Quote and source are cleaned alike, so a genuinely doubled
// token such as 55 still matches itself.
func cleanClaimMathText(text string) string {
	text = claimTeXCommandPattern.ReplaceAllString(text, "")
	text = strings.NewReplacer("{", "", "}", "", "\\", "").Replace(norm.NFKC.String(text))
	fields := strings.Fields(text)
	for index, field := range fields {
		start := strings.IndexFunc(field, func(r rune) bool { return unicode.IsLetter(r) || unicode.IsDigit(r) })
		end := strings.LastIndexFunc(field, func(r rune) bool { return unicode.IsLetter(r) || unicode.IsDigit(r) || r == '%' })
		if start < 0 || end < start {
			continue
		}
		_, size := utf8.DecodeRuneInString(field[end:])
		core := field[start : end+size]
		if half := len(core) / 2; len(core)%2 == 0 && half > 0 && core[:half] == core[half:] {
			fields[index] = field[:start] + core[:half] + field[end+size:]
		}
	}
	return strings.Join(fields, " ")
}

func matchClaimQuote(sourceText, quote string) claimQuoteMatch {
	sourceText, quote = cleanClaimMathText(sourceText), cleanClaimMathText(quote)
	source := claimMarkdownLinkURLPattern.ReplaceAllString(sourceText, "]")
	haystack := normalizeClaimText(source)
	segments := make([][]rune, 0, 2)
	total := 0
	for _, segment := range claimEllipsisPattern.Split(claimMarkdownLinkURLPattern.ReplaceAllString(quote, "]"), -1) {
		runes := normalizeClaimText(segment).runes
		if len(runes) > 0 {
			segments = append(segments, runes)
			total += len(runes)
		}
	}
	if total < claimQuoteMinRunes || len(haystack.runes) == 0 {
		return claimQuoteMatch{Status: claimStatusNotFound}
	}
	if claimSegmentsInOrder(haystack.runes, segments) {
		return claimQuoteMatch{Status: claimStatusVerified}
	}
	whole := make([]rune, 0, total)
	for _, segment := range segments {
		whole = append(whole, segment...)
	}
	score, start, end := nearestClaimWindow(haystack.runes, whole)
	match := claimQuoteMatch{Status: claimStatusNotFound}
	switch {
	case score >= claimApproximateScore:
		match.Status = claimStatusVerified
	case score >= claimNearMatchScore:
		match.Status = claimStatusNearMatch
	}
	if match.Status != claimStatusVerified && score >= claimNearestReportScore {
		match.Nearest = claimExcerpt(source, haystack, start, end)
	}
	return match
}

func claimSegmentsInOrder(haystack []rune, segments [][]rune) bool {
	text := string(haystack)
	position := 0
	for _, segment := range segments {
		index := strings.Index(text[position:], string(segment))
		if index < 0 {
			return false
		}
		position += index + len(string(segment))
	}
	return true
}

// nearestClaimWindow slides a quote-sized window across the source and scores
// the share of the quote's character trigrams it contains.
func nearestClaimWindow(haystack, quote []rune) (float64, int, int) {
	if len(quote) < 3 || len(haystack) < 3 {
		return 0, 0, 0
	}
	want := make(map[string]struct{}, len(quote))
	for index := 0; index+3 <= len(quote); index++ {
		want[string(quote[index:index+3])] = struct{}{}
	}
	width := len(quote)
	step := width / 4
	if step < 1 {
		step = 1
	}
	bestScore, bestStart := 0.0, 0
	for start := 0; start < len(haystack); start += step {
		end := start + width
		if end > len(haystack) {
			end = len(haystack)
		}
		seen := make(map[string]struct{}, width)
		for index := start; index+3 <= end; index++ {
			trigram := string(haystack[index : index+3])
			if _, ok := want[trigram]; ok {
				seen[trigram] = struct{}{}
			}
		}
		if score := float64(len(seen)) / float64(len(want)); score > bestScore {
			bestScore, bestStart = score, start
		}
		if end == len(haystack) {
			break
		}
	}
	bestEnd := bestStart + width
	if bestEnd > len(haystack) {
		bestEnd = len(haystack)
	}
	return bestScore, bestStart, bestEnd
}

func claimExcerpt(source string, haystack normalizedClaimText, start, end int) string {
	if start >= len(haystack.offsets) || end <= start {
		return ""
	}
	from := haystack.offsets[start]
	to := len(source)
	if end < len(haystack.offsets) {
		to = haystack.offsets[end]
	}
	excerpt := strings.Join(strings.Fields(source[from:to]), " ")
	runes := []rune(excerpt)
	if len(runes) > claimNearestExcerptRunes {
		excerpt = string(runes[:claimNearestExcerptRunes]) + "…"
	}
	return excerpt
}

// collectResearchClaims returns every recorded card in the tree, root first.
func collectResearchClaims(tree researchClaimTree) []researchClaim {
	claims := make([]researchClaim, 0)
	for _, run := range tree.Runs {
		for _, proposal := range run.Prefix.Proposals {
			for _, action := range proposal.Actions {
				if action.Name != recordClaimActionName || action.Result == nil || action.Result.Status != ActionSucceeded {
					continue
				}
				var input recordClaimInput
				var output recordClaimOutput
				if json.Unmarshal(action.Input, &input) != nil || json.Unmarshal(action.Result.Output, &output) != nil || !researchClaimIDPattern.MatchString(output.ID) {
					continue
				}
				claims = append(claims, researchClaim{
					recordClaimOutput: output, Quote: strings.TrimSpace(input.Quote),
					Claim: strings.TrimSpace(input.Claim), Conditions: strings.TrimSpace(input.Conditions), Question: input.Question,
				})
			}
		}
	}
	return claims
}

// canonicalClaimID normalizes a cited card id to the form record_claim
// returned.
func canonicalClaimID(id string) string {
	if match := childClaimIDVariantPattern.FindStringSubmatch(id); match != nil {
		return match[1] + "-c" + match[2]
	}
	return id
}

func citedClaimIDs(list string) []string {
	parts := researchClaimIDSplitPattern.Split(list, -1)
	ids := make([]string, 0, len(parts))
	for _, part := range parts {
		if match := researchClaimRangePattern.FindStringSubmatch(strings.TrimSpace(part)); match != nil {
			first, _ := strconv.Atoi(match[2])
			last, _ := strconv.Atoi(match[3])
			if last >= first && last-first < researchClaimMaxRange {
				for ordinal := first; ordinal <= last; ordinal++ {
					ids = append(ids, researchClaimNamespacedID(strings.TrimPrefix(match[1], "c"), ordinal))
				}
				continue
			}
		}
		ids = append(ids, canonicalClaimID(part))
	}
	return ids
}

// supersededNearMatchClaims finds near_match cards that a later verified card
// of the same source corrects: its quote covers the earlier quote's trigrams.
func supersededNearMatchClaims(claims []researchClaim) map[string]bool {
	superseded := map[string]bool{}
	for index, earlier := range claims {
		if earlier.Status != claimStatusNearMatch {
			continue
		}
		want := normalizeClaimText(cleanClaimMathText(earlier.Quote)).runes
		for _, later := range claims[index+1:] {
			if later.Status != claimStatusVerified || normalizeClaimURL(firstNonEmpty(later.URL, later.Source)) != normalizeClaimURL(firstNonEmpty(earlier.URL, earlier.Source)) {
				continue
			}
			have := normalizeClaimText(cleanClaimMathText(later.Quote)).runes
			if score, _, _ := nearestClaimWindow(have, want); score >= claimNearMatchScore {
				superseded[earlier.ID] = true
				break
			}
		}
	}
	return superseded
}

func renderResearchClaimsMarkdown(claims []researchClaim) string {
	var builder strings.Builder
	builder.WriteString("# Claim cards\n\nCite each card in report prose by its exact id shown in brackets below, such as [a2] or [b1, c3]; never renumber cards. The published report turns them into numbered source links.\n")
	superseded := supersededNearMatchClaims(claims)
	shown := make([]researchClaim, 0, len(claims))
	hidden := 0
	for _, claim := range claims {
		switch {
		case superseded[claim.ID]:
		case claim.Status == claimStatusVerified || claim.Status == claimStatusNearMatch:
			shown = append(shown, claim)
		default:
			hidden++
		}
	}
	if len(shown) == 0 {
		if hidden == 0 {
			builder.WriteString("\nNo claim cards recorded yet.\n")
		} else {
			builder.WriteString("\nNo checked claim cards yet.\n")
		}
	}
	if hidden > 0 {
		fmt.Fprintf(&builder, "\n%d card(s) failed verification (quote not found in the read text, or source not read) and are hidden here. Do not cite them or state their claims; where evidence is thin, say so.\n", hidden)
	}
	for _, claim := range shown {
		question := ""
		if claim.Question > 0 {
			question = fmt.Sprintf(" Q%d", claim.Question)
		}
		fmt.Fprintf(&builder, "\n- [%s] (%s%s) %s\n", claim.ID, claim.Status, question, claim.Claim)
		source := claim.Source
		if claim.URL != "" {
			source = claim.URL
		}
		if claim.Title != "" {
			source = claim.Title + " — " + source
		}
		fmt.Fprintf(&builder, "  - Source: %s\n  - Quote: \"%s\"\n", source, claim.Quote)
		if claim.Conditions != "" {
			fmt.Fprintf(&builder, "  - Conditions: %s\n", claim.Conditions)
		}
	}
	return builder.String()
}

type researchClaimCitationStats struct {
	Cited   int
	Unknown []string
}

// renderResearchClaimCitations turns [c3] or [c3, c5] into numbered Markdown
// links, one number per distinct source URL in order of first citation.
// Unknown card ids are dropped rather than rejected; a card without a public
// URL (an uploaded Notebook file) renders as its title.
func renderResearchClaimCitations(report string, claims []researchClaim) (string, researchClaimCitationStats) {
	byID := make(map[string]researchClaim, len(claims))
	for _, claim := range claims {
		byID[claim.ID] = claim
	}
	var stats researchClaimCitationStats
	numbers := map[string]int{}
	var builder strings.Builder
	last := 0
	for _, match := range researchClaimCitationPattern.FindAllStringSubmatchIndex(report, -1) {
		start, end := match[0], match[1]
		if end < len(report) && report[end] == '(' {
			continue
		}
		builder.WriteString(report[last:start])
		last = end
		seen := map[string]bool{}
		rendered := make([]string, 0)
		for _, id := range citedClaimIDs(report[match[2]:match[3]]) {
			claim, ok := byID[id]
			if !ok {
				stats.Unknown = append(stats.Unknown, id)
				continue
			}
			stats.Cited++
			key, text := claim.URL, ""
			if key == "" {
				key = "source:" + claim.Source
				text = "[" + firstNonEmpty(claim.Title, claim.Source) + "]"
			}
			if seen[key] {
				continue
			}
			seen[key] = true
			number, ok := numbers[key]
			if !ok {
				number = len(numbers) + 1
				numbers[key] = number
			}
			if text == "" {
				text = fmt.Sprintf("[%d](%s)", number, claim.URL)
			}
			rendered = append(rendered, text)
		}
		builder.WriteString(strings.Join(rendered, ""))
	}
	builder.WriteString(report[last:])
	return collapseRepeatedCitationLinks(builder.String()), stats
}

var renderedCitationLinkPattern = regexp.MustCompile(`\[[0-9]+\]\([^)\s]+\)`)

// collapseRepeatedCitationLinks keeps one of adjacent identical source links,
// which appear when a sentence cites two cards of one source as [a1][a2].
func collapseRepeatedCitationLinks(text string) string {
	var builder strings.Builder
	last, previous, previousEnd := 0, "", -1
	for _, match := range renderedCitationLinkPattern.FindAllStringIndex(text, -1) {
		link := text[match[0]:match[1]]
		if match[0] == previousEnd && link == previous {
			builder.WriteString(text[last:match[0]])
			last, previousEnd = match[1], match[1]
			continue
		}
		previous, previousEnd = link, match[1]
	}
	builder.WriteString(text[last:])
	return builder.String()
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

type postgresResearchClaimBackend struct {
	pool *pgxpool.Pool
}

func (b postgresResearchClaimBackend) workerTx(ctx context.Context) (pgx.Tx, error) {
	if b.pool == nil {
		return nil, errors.New("Research claim store is unavailable")
	}
	tx, err := b.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx, `set local role nano_worker`); err != nil {
		_ = tx.Rollback(ctx)
		return nil, err
	}
	return tx, nil
}

func (b postgresResearchClaimBackend) ClaimTree(ctx context.Context, runID string) (researchClaimTree, error) {
	tx, err := b.workerTx(ctx)
	if err != nil {
		return researchClaimTree{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	return loadResearchClaimTree(ctx, tx, runID)
}

func loadResearchClaimTree(ctx context.Context, tx DBTX, runID string) (researchClaimTree, error) {
	rootID := runID
	var parentID string
	err := tx.QueryRow(ctx, `select parent_run_id from agent_subagents where child_run_id=$1`, runID).Scan(&parentID)
	if err == nil {
		rootID = parentID
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return researchClaimTree{}, err
	}
	runIDs := []string{rootID}
	rows, err := tx.Query(ctx, `select child_run_id from agent_subagents where parent_run_id=$1 order by created_at,child_run_id`, rootID)
	if err != nil {
		return researchClaimTree{}, err
	}
	for rows.Next() {
		var childID string
		if err := rows.Scan(&childID); err != nil {
			rows.Close()
			return researchClaimTree{}, err
		}
		runIDs = append(runIDs, childID)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return researchClaimTree{}, err
	}
	tree := researchClaimTree{Runs: make([]researchClaimRun, 0, len(runIDs))}
	for index, id := range runIDs {
		checkpoints, err := loadRunCheckpoints(ctx, tx, id)
		if err != nil {
			return researchClaimTree{}, err
		}
		prefix, err := LoadCheckpointPrefix(ctx, checkpoints)
		if err != nil {
			return researchClaimTree{}, err
		}
		namespace := ""
		if index > 0 {
			namespace = researchClaimNamespace(index - 1)
		}
		tree.Runs = append(tree.Runs, researchClaimRun{RunID: id, Namespace: namespace, Prefix: prefix})
	}
	return tree, nil
}

func (b postgresResearchClaimBackend) SourceText(ctx context.Context, sourceID, revisionID string) (researchClaimSourceText, bool, error) {
	tx, err := b.workerTx(ctx)
	if err != nil {
		return researchClaimSourceText{}, false, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var source researchClaimSourceText
	err = tx.QueryRow(ctx, `select title,coalesce(final_url,origin_url,'') from source_sources where id=$1`, sourceID).Scan(&source.Title, &source.URL)
	if errors.Is(err, pgx.ErrNoRows) {
		return researchClaimSourceText{}, false, nil
	}
	if err != nil {
		return researchClaimSourceText{}, false, err
	}
	rows, err := tx.Query(ctx, `select text_content from source_evidence_units where source_id=$1 and revision_id=$2 order by ordinal`, sourceID, revisionID)
	if err != nil {
		return researchClaimSourceText{}, false, err
	}
	defer rows.Close()
	var builder strings.Builder
	for rows.Next() {
		var text string
		if err := rows.Scan(&text); err != nil {
			return researchClaimSourceText{}, false, err
		}
		builder.WriteString(text)
		builder.WriteString("\n\n")
	}
	if err := rows.Err(); err != nil {
		return researchClaimSourceText{}, false, err
	}
	source.Text = builder.String()
	return source, source.Text != "", nil
}

// researchClaimsReader serves the virtual claims.md workspace file.
type researchClaimsReader interface {
	ClaimsMarkdown(ctx context.Context, runID string) (string, error)
}

func (b postgresResearchClaimBackend) ClaimsMarkdown(ctx context.Context, runID string) (string, error) {
	tree, err := b.ClaimTree(ctx, runID)
	if err != nil {
		return "", err
	}
	return renderResearchClaimsMarkdown(collectResearchClaims(tree)), nil
}

// ResearchClaims returns every card recorded in the Run's Research tree.
func (b postgresResearchClaimBackend) ResearchClaims(ctx context.Context, runID string) ([]researchClaim, error) {
	tree, err := b.ClaimTree(ctx, runID)
	if err != nil {
		return nil, err
	}
	return collectResearchClaims(tree), nil
}

type researchClaimsSource interface {
	ResearchClaims(ctx context.Context, runID string) ([]researchClaim, error)
}

const (
	citationCheckMaxFindings   = 10
	citationCheckExcerptRunes  = 160
	citationCheckGuidanceTitle = "Citation number check"
)

var (
	citationCheckNumberPattern    = regexp.MustCompile(`\d+(?:[.,]\d+)*%?`)
	citationCheckInlineCode       = regexp.MustCompile("`[^`]*`")
	citationCheckRenderedLink     = regexp.MustCompile(`\[[^\]]*\]\([^)\s]*\)`)
	citationCheckThousands        = regexp.MustCompile(`^\d{1,3}(?:,\d{3})+(?:\.\d+)?$`)
	citationCheckTableSeparator   = regexp.MustCompile(`^\|?\s*:?-{2,}`)
	citationCheckSentenceTerminal = "。！？!?；;"
)

type researchCitationFinding struct {
	Excerpt string   `json:"excerpt"`
	Numbers []string `json:"numbers"`
	Cards   []string `json:"cards,omitempty"`
}

type researchCitationCheck struct {
	CheckedStatements  int                       `json:"checked_statements"`
	UnsupportedNumbers []researchCitationFinding `json:"unsupported_numbers,omitempty"`
	UncitedNumbers     []researchCitationFinding `json:"uncited_numbers,omitempty"`
}

func (c researchCitationCheck) empty() bool {
	return len(c.UnsupportedNumbers) == 0 && len(c.UncitedNumbers) == 0
}

// checkResearchCitationNumbers flags numbers that a cited statement states but
// none of its cited cards' quotes contain, and numeric statements that cite
// nothing. It is advisory: the result guides revision and never blocks.
func checkResearchCitationNumbers(report string, claims []researchClaim) researchCitationCheck {
	quotes := make(map[string]map[string]bool, len(claims))
	for _, claim := range claims {
		numbers := map[string]bool{}
		for _, raw := range citationCheckNumberPattern.FindAllString(claim.Quote, -1) {
			numbers[normalizeCitationNumber(raw)] = true
		}
		quotes[claim.ID] = numbers
	}
	var check researchCitationCheck
	for _, statement := range researchCitationStatements(report) {
		ids := make([]string, 0)
		for _, match := range researchClaimCitationPattern.FindAllStringSubmatch(statement, -1) {
			ids = append(ids, citedClaimIDs(match[1])...)
		}
		numbers := citationStatementNumbers(statement)
		if len(numbers) == 0 {
			continue
		}
		check.CheckedStatements++
		if len(ids) == 0 {
			if len(check.UncitedNumbers) < citationCheckMaxFindings {
				check.UncitedNumbers = append(check.UncitedNumbers, researchCitationFinding{Excerpt: citationExcerpt(statement), Numbers: numbers})
			}
			continue
		}
		missing := make([]string, 0)
		for _, number := range numbers {
			found := false
			for _, id := range ids {
				if quotes[id][normalizeCitationNumber(number)] {
					found = true
					break
				}
			}
			if !found {
				missing = append(missing, number)
			}
		}
		if len(missing) > 0 && len(check.UnsupportedNumbers) < citationCheckMaxFindings {
			check.UnsupportedNumbers = append(check.UnsupportedNumbers, researchCitationFinding{Excerpt: citationExcerpt(statement), Numbers: missing, Cards: ids})
		}
	}
	return check
}

// researchCitationStatements splits a report into table rows and sentences,
// keeping a citation that follows sentence punctuation with its sentence.
func researchCitationStatements(report string) []string {
	statements := make([]string, 0)
	for _, line := range strings.Split(report, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") || citationCheckTableSeparator.MatchString(line) {
			continue
		}
		if strings.HasPrefix(line, "|") {
			statements = append(statements, line)
			continue
		}
		start := 0
		for index := 0; index < len(line); {
			character, size := utf8.DecodeRuneInString(line[index:])
			index += size
			if !strings.ContainsRune(citationCheckSentenceTerminal, character) && !(character == '.' && (index == len(line) || line[index] == ' ')) {
				continue
			}
			for index < len(line) {
				rest := line[index:]
				trimmed := strings.TrimLeft(rest, " ")
				if location := researchClaimCitationPattern.FindStringIndex(trimmed); location != nil && location[0] == 0 {
					index += len(rest) - len(trimmed) + location[1]
					continue
				}
				break
			}
			statements = append(statements, line[start:index])
			start = index
		}
		if strings.TrimSpace(line[start:]) != "" {
			statements = append(statements, line[start:])
		}
	}
	return statements
}

// citationStatementNumbers keeps the numbers a reader would take as facts. It
// skips identifiers glued to letters (P95, GPT-5, top-3, v0.11), lone digits,
// and calendar years, which are too ambiguous to check against a quote.
func citationStatementNumbers(statement string) []string {
	text := researchClaimCitationPattern.ReplaceAllString(statement, " ")
	text = citationCheckRenderedLink.ReplaceAllString(text, " ")
	text = citationCheckInlineCode.ReplaceAllString(text, " ")
	numbers := make([]string, 0)
	seen := map[string]bool{}
	for _, location := range citationCheckNumberPattern.FindAllStringIndex(text, -1) {
		raw := text[location[0]:location[1]]
		if citationNumberAttachedToIdentifier(text, location[0]) {
			continue
		}
		value := strings.TrimSuffix(raw, "%")
		if len(value) == 1 && !strings.HasSuffix(raw, "%") {
			continue
		}
		if year, err := strconv.Atoi(value); err == nil && len(value) == 4 && year >= 1990 && year <= 2039 {
			continue
		}
		if !seen[raw] {
			seen[raw] = true
			numbers = append(numbers, raw)
		}
	}
	return numbers
}

func citationNumberAttachedToIdentifier(text string, start int) bool {
	if start == 0 {
		return false
	}
	previous, size := utf8.DecodeLastRuneInString(text[:start])
	if previous == '-' || previous == '@' || previous == '_' {
		before, _ := utf8.DecodeLastRuneInString(text[:start-size])
		return previous != '-' || isASCIILetter(before)
	}
	return isASCIILetter(previous)
}

func isASCIILetter(character rune) bool {
	return (character >= 'a' && character <= 'z') || (character >= 'A' && character <= 'Z')
}

func normalizeCitationNumber(raw string) string {
	value := strings.TrimSuffix(raw, "%")
	if citationCheckThousands.MatchString(value) {
		value = strings.ReplaceAll(value, ",", "")
	}
	if parsed, err := strconv.ParseFloat(value, 64); err == nil {
		return strconv.FormatFloat(parsed, 'f', -1, 64)
	}
	return value
}

func citationExcerpt(statement string) string {
	excerpt := strings.Join(strings.Fields(statement), " ")
	if runes := []rune(excerpt); len(runes) > citationCheckExcerptRunes {
		excerpt = string(runes[:citationCheckExcerptRunes]) + "…"
	}
	return excerpt
}

func researchCitationCheckGuidance(check researchCitationCheck) string {
	if check.empty() {
		return ""
	}
	return fmt.Sprintf("%s: %d statement(s) state numbers that none of their cited cards' quotes contain (unsupported_numbers), and %d numeric statement(s) cite no card (uncited_numbers). For each, correct the number, cite the card whose quote contains it, or rewrite it as clearly labelled inference, then assemble again.",
		citationCheckGuidanceTitle, len(check.UnsupportedNumbers), len(check.UncitedNumbers))
}

const (
	// A source is listed for uncited cards once this many of them go unused.
	sourceCoverageMinUnusedCards = 3
	sourceCoverageMaxUnusedList  = 6
	sourceCoverageMaxUncited     = 8
	// researchBreadthTarget is the read-source count below which assembly
	// suggests another discovery round, before executor v31 judged breadth
	// per research question instead of by a count.
	researchBreadthTarget          = 8
	researchQuestionBreadthVersion = 31
	sourceCoverageMaxLeads         = 6
	// sourceCoverageMaxRecommendedLeads lists enough recommended leads for a
	// second reading round.
	sourceCoverageMaxRecommendedLeads = 10
)

type researchReadSource struct {
	URL         string `json:"url"`
	FinalURL    string `json:"-"`
	Title       string `json:"title,omitempty"`
	Recommended bool   `json:"recommended,omitempty"`
}

type researchSourceCoverage struct {
	ReadSources   int                      `json:"read_sources"`
	CitedSources  int                      `json:"cited_sources"`
	UncitedSample []researchReadSource     `json:"uncited_read_sources,omitempty"`
	UnreadLeads   []researchReadSource     `json:"unread_leads,omitempty"`
	Cards         int                      `json:"usable_cards"`
	CitedCards    int                      `json:"cited_cards"`
	UnusedCards   []researchUnusedCardStat `json:"sources_with_uncited_cards,omitempty"`
}

// researchUnusedCardStat counts one source's usable cards that the report
// never cites, which usually means its findings were cut, not checked.
type researchUnusedCardStat struct {
	URL     string `json:"url"`
	Title   string `json:"title,omitempty"`
	Cards   int    `json:"cards"`
	Uncited int    `json:"uncited"`
}

type researchReadSourceLister interface {
	ResearchReadSources(ctx context.Context, runID string) ([]researchReadSource, error)
	ResearchUnreadLeads(ctx context.Context, runID string, limit int) ([]researchReadSource, error)
}

// ResearchUnreadLeads lists discovered but never read or failed URLs: those a
// scout or reader recommended first, then in the order search surfaced them.
func (b postgresResearchClaimBackend) ResearchUnreadLeads(ctx context.Context, runID string, limit int) ([]researchReadSource, error) {
	tx, err := b.workerTx(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	return loadResearchUnreadLeads(ctx, tx, runID, limit)
}

func loadResearchUnreadLeads(ctx context.Context, tx DBTX, runID string, limit int) ([]researchReadSource, error) {
	// A paper read or failed under one URL is not unread under another, such
	// as an arXiv abs lead for a page read through its html URL; variants of
	// one unread document are listed once.
	rows, err := tx.Query(ctx, `
		select ledger.url,coalesce(ledger.final_url,''),ledger.title,ledger.status,ledger.recommended_at is not null
		from research_evidence_ledger ledger
		join research_sessions session on session.id=ledger.session_id
		where session.execution_run_id=nano_research_root_run($1)
		order by ledger.recommended_at nulls last,ledger.first_seen_at,ledger.url
	`, runID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	attempted := map[string]bool{}
	candidates := make([]researchReadSource, 0)
	for rows.Next() {
		var lead researchReadSource
		var status string
		if err := rows.Scan(&lead.URL, &lead.FinalURL, &lead.Title, &status, &lead.Recommended); err != nil {
			return nil, err
		}
		if status != "discovered" {
			attempted[researchDocumentKey(lead.URL)] = true
			if lead.FinalURL != "" {
				attempted[researchDocumentKey(lead.FinalURL)] = true
			}
			continue
		}
		candidates = append(candidates, lead)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	leads := make([]researchReadSource, 0, limit)
	for _, lead := range candidates {
		key := researchDocumentKey(lead.URL)
		if attempted[key] {
			continue
		}
		attempted[key] = true
		leads = append(leads, lead)
		if len(leads) == limit {
			break
		}
	}
	return leads, nil
}

// ResearchReadSources lists the session's successfully read URLs.
func (b postgresResearchClaimBackend) ResearchReadSources(ctx context.Context, runID string) ([]researchReadSource, error) {
	tx, err := b.workerTx(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	rows, err := tx.Query(ctx, `
		select ledger.url,coalesce(ledger.final_url,''),ledger.title
		from research_evidence_ledger ledger
		join research_sessions session on session.id=ledger.session_id
		where session.execution_run_id=nano_research_root_run($1) and ledger.status='read'
		order by ledger.first_seen_at,ledger.url
	`, runID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	sources := make([]researchReadSource, 0)
	for rows.Next() {
		var source researchReadSource
		if err := rows.Scan(&source.URL, &source.FinalURL, &source.Title); err != nil {
			return nil, err
		}
		sources = append(sources, source)
	}
	return sources, rows.Err()
}

// checkResearchSourceCoverage counts which read sources the report cites,
// through claim cards or direct links, and samples the read but uncited ones.
// Like the number check it only informs revision.
func checkResearchSourceCoverage(report string, claims []researchClaim, sources []researchReadSource) researchSourceCoverage {
	cited := map[string]bool{}
	citedCards := map[string]bool{}
	byID := make(map[string]researchClaim, len(claims))
	for _, claim := range claims {
		byID[claim.ID] = claim
	}
	for _, match := range researchClaimCitationPattern.FindAllStringSubmatch(report, -1) {
		for _, id := range citedClaimIDs(match[1]) {
			if claim, ok := byID[id]; ok {
				citedCards[id] = true
				cited[normalizeClaimURL(claim.URL)] = true
				cited[normalizeClaimURL(claim.Source)] = true
			}
		}
	}
	for _, match := range markdownLinkPattern.FindAllStringSubmatch(report, -1) {
		cited[normalizeClaimURL(match[1])] = true
	}
	coverage := researchSourceCoverage{ReadSources: len(sources)}
	for _, source := range sources {
		if cited[normalizeClaimURL(source.URL)] || (source.FinalURL != "" && cited[normalizeClaimURL(source.FinalURL)]) {
			coverage.CitedSources++
			continue
		}
		if len(coverage.UncitedSample) < sourceCoverageMaxUncited {
			coverage.UncitedSample = append(coverage.UncitedSample, source)
		}
	}
	bySource := map[string]*researchUnusedCardStat{}
	order := make([]string, 0)
	for _, claim := range claims {
		if claim.Status != claimStatusVerified && claim.Status != claimStatusNearMatch {
			continue
		}
		coverage.Cards++
		if citedCards[claim.ID] {
			coverage.CitedCards++
		}
		url := firstNonEmpty(claim.URL, claim.Source)
		key := normalizeClaimURL(url)
		stat, ok := bySource[key]
		if !ok {
			stat = &researchUnusedCardStat{URL: url, Title: claim.Title}
			bySource[key] = stat
			order = append(order, key)
		}
		stat.Cards++
		if !citedCards[claim.ID] {
			stat.Uncited++
		}
	}
	for _, key := range order {
		if stat := bySource[key]; stat.Uncited >= sourceCoverageMinUnusedCards {
			coverage.UnusedCards = append(coverage.UnusedCards, *stat)
		}
	}
	sort.SliceStable(coverage.UnusedCards, func(i, j int) bool { return coverage.UnusedCards[i].Uncited > coverage.UnusedCards[j].Uncited })
	if len(coverage.UnusedCards) > sourceCoverageMaxUnusedList {
		coverage.UnusedCards = coverage.UnusedCards[:sourceCoverageMaxUnusedList]
	}
	return coverage
}

// researchSourceCoverageGuidance advises on uncited sources and cards; with
// countBreadth it also applies the older read-source count target.
func researchSourceCoverageGuidance(coverage researchSourceCoverage, countBreadth bool) string {
	parts := make([]string, 0, 2)
	if coverage.ReadSources > 0 && coverage.CitedSources < coverage.ReadSources {
		parts = append(parts, fmt.Sprintf("Source coverage: the report cites %d of the %d sources read in this Run. Cite an uncited read source where it supports or qualifies a claim, record a card from it if needed, or leave it out deliberately.", coverage.CitedSources, coverage.ReadSources))
	}
	if len(coverage.UnusedCards) > 0 {
		parts = append(parts, fmt.Sprintf("Card use: the report cites %d of %d usable claim cards; sources_with_uncited_cards lists sources whose verified findings the report leaves out. Plan source lists and reference lists are a starting map, not a limit: use these cards wherever they support, qualify, or contradict a claim. If the Member set a length limit, make room by tightening prose and replacing weaker evidence rather than dropping a source's main findings.", coverage.CitedCards, coverage.Cards))
	}
	if countBreadth && coverage.ReadSources < researchBreadthTarget {
		advice := fmt.Sprintf("Source breadth: only %d sources have been read, while a substantial report usually rests on about %d-15 across several source families. Before Final, consider another round of discovery and parallel reading, especially independent evaluations, critiques, and alternatives, then revise and assemble again.", coverage.ReadSources, researchBreadthTarget)
		if len(coverage.UnreadLeads) > 0 {
			advice += " Unread leads already discovered are listed in unread_leads."
		}
		parts = append(parts, advice)
	}
	return strings.Join(parts, " ")
}

type researchRunningSubagent struct {
	AgentID  string `json:"agent_id"`
	TaskName string `json:"task_name"`
}

type researchSubagentLister interface {
	RunningResearchSubagents(ctx context.Context, runID string) ([]researchRunningSubagent, error)
}

// RunningResearchSubagents lists the Run's subagents that have not finished,
// such as readers still extracting claim cards from long documents.
func (b postgresResearchClaimBackend) RunningResearchSubagents(ctx context.Context, runID string) ([]researchRunningSubagent, error) {
	tx, err := b.workerTx(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	rows, err := tx.Query(ctx, `
		select s.child_run_id,s.task_name from agent_subagents s join agent_runs child on child.id=s.child_run_id
		where s.parent_run_id=$1 and child.status in ('queued','running') order by s.created_at
	`, runID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	running := make([]researchRunningSubagent, 0)
	for rows.Next() {
		var agent researchRunningSubagent
		if err := rows.Scan(&agent.AgentID, &agent.TaskName); err != nil {
			return nil, err
		}
		running = append(running, agent)
	}
	return running, rows.Err()
}

func researchRunningSubagentsGuidance(running []researchRunningSubagent) string {
	if len(running) == 0 {
		return ""
	}
	return fmt.Sprintf("Subagents still running: %d (listed in running_subagents). Their claim cards are not in this report yet; collect them with wait_agent, revise with their cards, and assemble again before Final.", len(running))
}
