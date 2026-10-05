package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
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
)

var (
	researchClaimIDPattern       = regexp.MustCompile(`^(?:[0-9a-f]{4}-)?c[1-9][0-9]*$`)
	researchClaimCitationPattern = regexp.MustCompile(`\[((?:[0-9a-f]{4}-)?c[1-9][0-9]*(?:\s*[,，;；、]\s*(?:[0-9a-f]{4}-)?c[1-9][0-9]*)*)\]`)
	researchClaimIDSplitPattern  = regexp.MustCompile(`\s*[,，;；、]\s*`)
	claimMarkdownLinkURLPattern  = regexp.MustCompile(`\]\([^)\s]*\)`)
	claimEllipsisPattern         = regexp.MustCompile(`\.{3,}|…+`)
)

type recordClaimInput struct {
	Source     string `json:"source"`
	Quote      string `json:"quote"`
	Claim      string `json:"claim"`
	Conditions string `json:"conditions,omitempty"`
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
		InputSchema: json.RawMessage(fmt.Sprintf(`{"type":"object","additionalProperties":false,"required":["source","quote","claim"],"properties":{"source":{"type":"string","minLength":1,"maxLength":4096,"description":"The read page URL, or the Notebook source_id."},"quote":{"type":"string","minLength":1,"maxLength":%d,"description":"Verbatim source text; use ... to elide inside a long passage."},"claim":{"type":"string","minLength":1,"maxLength":%d,"description":"What this quote establishes, in the report language."},"conditions":{"type":"string","maxLength":%d,"description":"Scope, setting, version, or caveat that limits the claim."}}}`,
			claimRecordInputMaxQuote, claimRecordInputMaxClaim, claimRecordInputMaxCondit)),
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
// carry a namespace so their cards never collide with the root's.
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
	return namespace + "-c" + strconv.Itoa(ordinal)
}

func researchClaimNamespace(runID string) string {
	digest := sha256.Sum256([]byte(runID))
	return hex.EncodeToString(digest[:2])
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
	want := normalizeClaimURL(url)
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
					if normalizeClaimURL(input.URL) != want {
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
				if normalizeClaimURL(input.URL) != want && normalizeClaimURL(output.FinalURL) != want && normalizeClaimURL(output.RequestedURL) != want {
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

func matchClaimQuote(sourceText, quote string) claimQuoteMatch {
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
					Claim: strings.TrimSpace(input.Claim), Conditions: strings.TrimSpace(input.Conditions),
				})
			}
		}
	}
	return claims
}

func renderResearchClaimsMarkdown(claims []researchClaim) string {
	var builder strings.Builder
	builder.WriteString("# Claim cards\n\nCite a card in report prose as [c3]; the published report turns it into a numbered source link.\n")
	if len(claims) == 0 {
		builder.WriteString("\nNo claim cards recorded yet.\n")
		return builder.String()
	}
	for _, claim := range claims {
		fmt.Fprintf(&builder, "\n- [%s] (%s) %s\n", claim.ID, claim.Status, claim.Claim)
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
		for _, id := range researchClaimIDSplitPattern.Split(report[match[2]:match[3]], -1) {
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
	return builder.String(), stats
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
	rows, err := tx.Query(ctx, `select child_run_id from agent_subagents where parent_run_id=$1 order by child_run_id`, rootID)
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
			namespace = researchClaimNamespace(id)
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
			ids = append(ids, researchClaimIDSplitPattern.Split(match[1], -1)...)
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
