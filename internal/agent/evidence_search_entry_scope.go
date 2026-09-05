package agent

import (
	"context"
	"errors"

	"github.com/huangxinxinyu/nano-notebook/internal/objectstore"
	"github.com/huangxinxinyu/nano-notebook/internal/sourcemap"
	"github.com/jackc/pgx/v5"
)

type postgresEvidenceEntryScopeResolver struct {
	service *EvidenceSearchService
	objects sourceInspectionObjectReader
}

func (r *postgresEvidenceEntryScopeResolver) ResolveEntryScope(ctx context.Context, scope pinnedSearchScope, entryID string) (entrySearchResolution, error) {
	if r == nil || r.service == nil || r.service.pool == nil || r.objects == nil || len(scope.Evidence) != 1 {
		return entrySearchResolution{}, ErrEvidenceScopeUnavailable
	}
	evidence := scope.Evidence[0]
	tx, err := r.service.workerTx(ctx)
	if err != nil {
		return entrySearchResolution{}, err
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
	`, evidence.SourceID, evidence.RevisionID, scope.NotebookID, sourcemap.ParserPolicyNoOCR).Scan(
		&mapID, &objectKey, &artifactSHA256, &artifactBytes,
		&navigationKind, &confidence, &pageCount, &entryCount, &originalSHA256,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return entrySearchResolution{}, ErrEvidenceScopeUnavailable
	}
	if err != nil {
		return entrySearchResolution{}, err
	}
	units, err := loadSourceInspectionUnits(ctx, tx, evidence.SourceID, evidence.RevisionID)
	if err != nil {
		return entrySearchResolution{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return entrySearchResolution{}, err
	}
	payload, err := r.objects.Get(ctx, objectKey, int64(artifactBytes))
	if errors.Is(err, objectstore.ErrNotFound) || errors.Is(err, objectstore.ErrObjectTooLarge) {
		return entrySearchResolution{}, ErrEvidenceScopeUnavailable
	}
	if err != nil {
		return entrySearchResolution{}, err
	}
	sourceMap, err := sourcemap.DecodeArtifact(payload, sourcemap.ArtifactIdentity{
		SourceID: evidence.SourceID, RevisionID: evidence.RevisionID,
		SHA256: artifactSHA256, Bytes: artifactBytes,
	})
	if err != nil || sourceMap.MapID != mapID || sourceMap.OriginalSHA256 != originalSHA256 ||
		string(sourceMap.NavigationKind) != navigationKind || string(sourceMap.Confidence) != confidence ||
		sourceMap.PageCount != pageCount || len(sourceMap.Entries) != entryCount {
		return entrySearchResolution{}, ErrEvidenceScopeUnavailable
	}
	return resolveEntrySearchScope(
		evidence.SourceID, entryID, sourceMap, units, scope.Version.ID, scope.Version.Config.Chunk,
	)
}
