package agent

import (
	"context"
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
	sourceMap, err := loadPinnedSourceMap(ctx, r.service, r.objects, scope.NotebookID, evidence)
	if err != nil {
		return entrySearchResolution{}, err
	}
	tx, err := r.service.workerTx(ctx)
	if err != nil {
		return entrySearchResolution{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	units, err := loadSourceInspectionUnits(ctx, tx, evidence.SourceID, evidence.RevisionID)
	if err != nil {
		return entrySearchResolution{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return entrySearchResolution{}, err
	}
	return resolveEntrySearchScope(
		evidence.SourceID, entryID, sourceMap, units, scope.Version.ID, scope.Version.Config.Chunk,
	)
}
