package agent

import (
	"context"
	"errors"
	"strings"

	"github.com/huangxinxinyu/nano-notebook/internal/retrieval"
	"github.com/huangxinxinyu/nano-notebook/internal/sourcemap"
)

type entrySearchResolution struct {
	Scope           EvidenceSearchResolvedScope
	AllowedChunkIDs []string
}

type evidenceEntryScopeResolver interface {
	ResolveEntryScope(context.Context, pinnedSearchScope, string) (entrySearchResolution, error)
}

func resolveRequestedSearchScope(
	ctx context.Context,
	scope pinnedSearchScope,
	request EvidenceSearchRequest,
	resolver evidenceEntryScopeResolver,
) (pinnedSearchScope, error) {
	if strings.TrimSpace(request.SourceID) == "" {
		if strings.TrimSpace(request.EntryID) != "" {
			return pinnedSearchScope{}, ErrEvidenceScopeUnavailable
		}
		return scope, nil
	}
	narrowed, err := narrowPinnedSearchScope(scope, request.SourceID)
	if err != nil {
		return pinnedSearchScope{}, err
	}
	narrowed.ResolvedScope = &EvidenceSearchResolvedScope{SourceID: narrowed.Evidence[0].SourceID}
	if strings.TrimSpace(request.EntryID) == "" {
		return narrowed, nil
	}
	if resolver == nil {
		return pinnedSearchScope{}, ErrEvidenceScopeUnavailable
	}
	resolution, err := resolver.ResolveEntryScope(ctx, narrowed, request.EntryID)
	if err != nil {
		if errors.Is(err, ErrEvidenceScopeUnavailable) {
			return pinnedSearchScope{}, ErrEvidenceScopeUnavailable
		}
		return pinnedSearchScope{}, err
	}
	if !validSearchEvidenceScope(resolution.Scope) || resolution.Scope.SourceID != narrowed.Evidence[0].SourceID || len(resolution.AllowedChunkIDs) == 0 {
		return pinnedSearchScope{}, ErrEvidenceScopeUnavailable
	}
	narrowed.AllowedChunkIDs = append([]string(nil), resolution.AllowedChunkIDs...)
	narrowed.ResolvedScope = cloneEvidenceSearchResolvedScope(&resolution.Scope)
	return narrowed, nil
}

func resolveEntrySearchScope(
	sourceID, entryID string,
	sourceMap sourcemap.SourceMap,
	units []sourceInspectionUnit,
	indexVersionID string,
	chunkConfig retrieval.ChunkConfig,
) (entrySearchResolution, error) {
	sourceID = strings.TrimSpace(sourceID)
	entryID = strings.TrimSpace(entryID)
	if sourceID == "" || entryID == "" || sourceMap.SourceID != sourceID || strings.TrimSpace(sourceMap.RevisionID) == "" || sourceMap.PageCount < 1 {
		return entrySearchResolution{}, ErrEvidenceScopeUnavailable
	}
	var selected *sourcemap.NavigationEntry
	for index := range sourceMap.Entries {
		if sourceMap.Entries[index].EntryID == entryID {
			selected = &sourceMap.Entries[index]
			break
		}
	}
	if selected == nil || selected.PageStart < 1 || selected.PageEnd < selected.PageStart || selected.PageEnd > sourceMap.PageCount {
		return entrySearchResolution{}, ErrEvidenceScopeUnavailable
	}
	allowedUnits := make(map[string]bool)
	retrievalUnits := make([]retrieval.Unit, 0, len(units))
	for _, unit := range units {
		retrievalUnits = append(retrievalUnits, retrieval.Unit{
			ID: unit.ID, Ordinal: unit.Ordinal, Kind: unit.Kind, Text: unit.Text,
		})
		if unit.Coordinate.Kind == "pdf_region" && unit.Coordinate.Page >= selected.PageStart && unit.Coordinate.Page <= selected.PageEnd {
			allowedUnits[unit.ID] = true
		}
	}
	if len(allowedUnits) == 0 {
		return entrySearchResolution{}, ErrEvidenceScopeUnavailable
	}
	chunks, err := retrieval.BuildChunks(indexVersionID, sourceMap.RevisionID, retrievalUnits, chunkConfig)
	if err != nil {
		return entrySearchResolution{}, ErrEvidenceScopeUnavailable
	}
	allowedChunks := make([]string, 0)
	for _, chunk := range chunks {
		for _, ref := range chunk.UnitRefs {
			if allowedUnits[ref.UnitID] {
				allowedChunks = append(allowedChunks, chunk.ID)
				break
			}
		}
	}
	if len(allowedChunks) == 0 {
		return entrySearchResolution{}, ErrEvidenceScopeUnavailable
	}
	return entrySearchResolution{
		Scope: EvidenceSearchResolvedScope{
			SourceID: sourceID, EntryID: entryID, PageStart: selected.PageStart, PageEnd: selected.PageEnd,
		},
		AllowedChunkIDs: allowedChunks,
	}, nil
}
