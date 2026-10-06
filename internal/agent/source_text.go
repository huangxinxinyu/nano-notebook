package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"unicode/utf16"
	"unicode/utf8"

	"github.com/huangxinxinyu/nano-notebook/internal/agentcatalog"
	"github.com/huangxinxinyu/nano-notebook/internal/objectstore"
	"github.com/huangxinxinyu/nano-notebook/internal/sourcemap"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// search_text and read_source reach the same pinned Evidence Units that
// search_evidence chunks and record_claim verifies quotes against, so they
// add ways to locate and read Source text without adding a second evidence
// representation.

const (
	searchTextActionName = "search_text"
	readSourceActionName = "read_source"
)

var (
	errSourceTextResultNotSearchable = errors.New("result_ref_not_searchable")
	errSourceTextInvalidPattern      = errors.New("invalid_pattern")
)

// SourceTextDocument is one pinned Source revision and its ordered Evidence
// Units.
type SourceTextDocument struct {
	NotebookID string
	SourceID   string
	RevisionID string
	Title      string
	Units      []sourceInspectionUnit
}

type SourceTextBackend interface {
	// LoadSourceTexts loads every pinned Source when sourceID is empty, or
	// the one pinned Source it names; any other Source is
	// ErrEvidenceScopeUnavailable.
	LoadSourceTexts(context.Context, Attempt, string) ([]SourceTextDocument, error)
	// LoadSourceMap reports false when the Source has no usable Source Map.
	LoadSourceMap(context.Context, SourceTextDocument) (sourcemap.SourceMap, bool, error)
	LoadToolResultBody(context.Context, ToolResultScope, string) ([]byte, error)
}

type SourceTextService struct {
	search      *EvidenceSearchService
	objects     sourceInspectionObjectReader
	toolResults *ToolResultReader
}

func NewSourceTextService(pool *pgxpool.Pool, objects sourceInspectionObjectReader, toolResults *ToolResultReader) *SourceTextService {
	return &SourceTextService{search: &EvidenceSearchService{pool: pool}, objects: objects, toolResults: toolResults}
}

func (s *SourceTextService) LoadSourceTexts(ctx context.Context, attempt Attempt, sourceID string) ([]SourceTextDocument, error) {
	if s == nil || s.search == nil || s.search.pool == nil {
		return nil, ErrSourceInspectionUnavailable
	}
	scope, err := s.search.loadPinnedScope(ctx, attempt)
	if err != nil {
		return nil, err
	}
	selected := make([]pinnedEvidence, 0, len(scope.Evidence))
	for _, evidence := range scope.Evidence {
		if sourceID == "" || evidence.SourceID == sourceID {
			selected = append(selected, evidence)
		}
	}
	if len(selected) == 0 {
		return nil, ErrEvidenceScopeUnavailable
	}
	tx, err := s.search.workerTx(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	documents := make([]SourceTextDocument, 0, len(selected))
	for _, evidence := range selected {
		units, err := loadSourceInspectionUnits(ctx, tx, evidence.SourceID, evidence.RevisionID)
		if err != nil {
			return nil, err
		}
		documents = append(documents, SourceTextDocument{
			NotebookID: scope.NotebookID, SourceID: evidence.SourceID, RevisionID: evidence.RevisionID,
			Title: evidence.Title, Units: units,
		})
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return documents, nil
}

func (s *SourceTextService) LoadSourceMap(ctx context.Context, document SourceTextDocument) (sourcemap.SourceMap, bool, error) {
	if s == nil || s.objects == nil {
		return sourcemap.SourceMap{}, false, nil
	}
	value, err := loadPinnedSourceMap(ctx, s.search, s.objects, document.NotebookID, pinnedEvidence{
		SourceID: document.SourceID, RevisionID: document.RevisionID,
	})
	if errors.Is(err, ErrEvidenceScopeUnavailable) {
		return sourcemap.SourceMap{}, false, nil
	}
	return value, err == nil, err
}

// LoadToolResultBody reassembles one externalized Tool Result under the same
// user, chat, and Run checks read_tool_result applies.
func (s *SourceTextService) LoadToolResultBody(ctx context.Context, scope ToolResultScope, resultRef string) ([]byte, error) {
	if s == nil || s.toolResults == nil {
		return nil, ErrToolResultExpired
	}
	body := make([]byte, 0)
	offset := 0
	for {
		page, err := s.toolResults.Read(ctx, scope, resultRef, offset, s.toolResults.MaximumPageBytes)
		if err != nil {
			return nil, err
		}
		body = append(body, page.Content...)
		if page.Complete {
			return body, nil
		}
		if page.NextOffset <= offset {
			return nil, ErrToolResultCorrupt
		}
		offset = page.NextOffset
	}
}

// loadPinnedSourceMap loads and verifies the immutable Source Map of one
// pinned Source revision. A missing or mismatched map is
// ErrEvidenceScopeUnavailable.
func loadPinnedSourceMap(ctx context.Context, service *EvidenceSearchService, objects sourceInspectionObjectReader, notebookID string, evidence pinnedEvidence) (sourcemap.SourceMap, error) {
	if service == nil || service.pool == nil || objects == nil {
		return sourcemap.SourceMap{}, ErrEvidenceScopeUnavailable
	}
	tx, err := service.workerTx(ctx)
	if err != nil {
		return sourcemap.SourceMap{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var mapID, objectKey, artifactSHA256, originalSHA256, navigationKind, confidence string
	var artifactBytes, pageCount, entryCount int
	err = tx.QueryRow(ctx, `
		select m.id,m.artifact_object_key,m.artifact_sha256,m.artifact_bytes,
			m.navigation_kind,m.confidence,m.page_count,m.entry_count,m.original_sha256
		from source_maps m
		join source_sources source on source.id=m.source_id and source.notebook_id=m.notebook_id
			and source.state='ready' and source.content_sha256=m.original_sha256
		where m.source_id=$1 and m.revision_id=$2 and m.notebook_id=$3 and m.parser_policy_id=$4
	`, evidence.SourceID, evidence.RevisionID, notebookID, sourcemap.ParserPolicyNoOCR).Scan(
		&mapID, &objectKey, &artifactSHA256, &artifactBytes,
		&navigationKind, &confidence, &pageCount, &entryCount, &originalSHA256,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return sourcemap.SourceMap{}, ErrEvidenceScopeUnavailable
	}
	if err != nil {
		return sourcemap.SourceMap{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return sourcemap.SourceMap{}, err
	}
	payload, err := objects.Get(ctx, objectKey, int64(artifactBytes))
	if errors.Is(err, objectstore.ErrNotFound) || errors.Is(err, objectstore.ErrObjectTooLarge) {
		return sourcemap.SourceMap{}, ErrEvidenceScopeUnavailable
	}
	if err != nil {
		return sourcemap.SourceMap{}, err
	}
	sourceMap, err := sourcemap.DecodeArtifact(payload, sourcemap.ArtifactIdentity{
		SourceID: evidence.SourceID, RevisionID: evidence.RevisionID,
		SHA256: artifactSHA256, Bytes: artifactBytes,
	})
	if err != nil || sourceMap.MapID != mapID || sourceMap.OriginalSHA256 != originalSHA256 ||
		string(sourceMap.NavigationKind) != navigationKind || string(sourceMap.Confidence) != confidence ||
		sourceMap.PageCount != pageCount || len(sourceMap.Entries) != entryCount {
		return sourcemap.SourceMap{}, ErrEvidenceScopeUnavailable
	}
	return sourceMap, nil
}

// innermostSourceMapEntry returns the narrowest entry whose page range holds
// page; among equally narrow entries the later, more deeply nested one wins.
func innermostSourceMapEntry(entries []sourcemap.NavigationEntry, page int) string {
	best, bestSpan := "", 0
	for _, entry := range entries {
		if page < entry.PageStart || page > entry.PageEnd {
			continue
		}
		if span := entry.PageEnd - entry.PageStart; best == "" || span <= bestSpan {
			best, bestSpan = entry.EntryID, span
		}
	}
	return best
}

func unitPDFPage(unit sourceInspectionUnit) int {
	if unit.Coordinate.Kind == "pdf_region" && unit.Coordinate.Page > 0 {
		return unit.Coordinate.Page
	}
	return 0
}

// isChatDefinition reports whether the Action runs for the chat leader, whose
// per-Action result budget is far smaller than Research's.
func isChatDefinition(reference agentcatalog.Reference) bool {
	return reference.Identity == "chat.leader"
}

// readURLMarkdownWithOffsets extracts the markdown string of an externalized
// read_url result together with, for every decoded byte, the byte offset in
// body where it was encoded, plus one trailing offset for the closing quote.
// read_tool_result pages the raw body, so a decoded line start maps to an
// offset it accepts.
func readURLMarkdownWithOffsets(body []byte) (string, []int32, error) {
	decoder := json.NewDecoder(bytes.NewReader(body))
	if token, err := decoder.Token(); err != nil || token != json.Delim('{') {
		return "", nil, errSourceTextResultNotSearchable
	}
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			return "", nil, errSourceTextResultNotSearchable
		}
		key, ok := token.(string)
		if !ok {
			return "", nil, errSourceTextResultNotSearchable
		}
		if key == "markdown" {
			return decodeJSONStringWithOffsets(body, int(decoder.InputOffset()))
		}
		var skipped json.RawMessage
		if err := decoder.Decode(&skipped); err != nil {
			return "", nil, errSourceTextResultNotSearchable
		}
	}
	return "", nil, errSourceTextResultNotSearchable
}

func decodeJSONStringWithOffsets(body []byte, position int) (string, []int32, error) {
	for position < len(body) && (body[position] == ' ' || body[position] == '\t' || body[position] == '\n' || body[position] == '\r' || body[position] == ':') {
		position++
	}
	if position >= len(body) || body[position] != '"' {
		return "", nil, errSourceTextResultNotSearchable
	}
	position++
	decoded := make([]byte, 0, len(body)-position)
	offsets := make([]int32, 0, len(body)-position)
	emit := func(at int, value ...byte) {
		for range value {
			offsets = append(offsets, int32(at))
		}
		decoded = append(decoded, value...)
	}
	for position < len(body) {
		character := body[position]
		switch {
		case character == '"':
			offsets = append(offsets, int32(position))
			if !utf8.Valid(decoded) || len(decoded) == 0 {
				return "", nil, errSourceTextResultNotSearchable
			}
			return string(decoded), offsets, nil
		case character != '\\':
			emit(position, character)
			position++
		case position+1 >= len(body):
			return "", nil, errSourceTextResultNotSearchable
		default:
			escape := body[position+1]
			switch escape {
			case '"', '\\', '/':
				emit(position, escape)
				position += 2
			case 'b', 'f', 'n', 'r', 't':
				emit(position, map[byte]byte{'b': '\b', 'f': '\f', 'n': '\n', 'r': '\r', 't': '\t'}[escape])
				position += 2
			case 'u':
				value, ok := parseJSONHexEscape(body, position)
				if !ok {
					return "", nil, errSourceTextResultNotSearchable
				}
				width := 6
				if utf16.IsSurrogate(value) {
					if low, ok := parseJSONHexEscape(body, position+6); ok {
						if combined := utf16.DecodeRune(value, low); combined != utf8.RuneError {
							value, width = combined, 12
						}
					}
				}
				emit(position, []byte(string(value))...)
				position += width
			default:
				return "", nil, errSourceTextResultNotSearchable
			}
		}
	}
	return "", nil, errSourceTextResultNotSearchable
}

func parseJSONHexEscape(body []byte, position int) (rune, bool) {
	if position+6 > len(body) || body[position] != '\\' || body[position+1] != 'u' {
		return 0, false
	}
	value, err := strconv.ParseUint(string(body[position+2:position+6]), 16, 16)
	return rune(value), err == nil
}

func truncateSourceTextRunes(value string, limit int) (string, bool) {
	if utf8.RuneCountInString(value) <= limit {
		return value, false
	}
	return string([]rune(value)[:limit]), true
}

func collapseSourceTextWhitespace(value string) string {
	return strings.Join(strings.Fields(value), " ")
}
