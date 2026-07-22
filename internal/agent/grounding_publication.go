package agent

import (
	"context"
	"errors"

	"github.com/huangxinxinyu/nano-notebook/internal/models"
	"github.com/jackc/pgx/v5"
)

func validateGroundingPublication(ctx context.Context, tx pgx.Tx, runID string, expected *models.FinalDraft) (string, error) {
	if expected == nil || expected.Validate() != nil {
		return "", ErrGroundingInvalid
	}
	var selectedCount int
	var notebookID string
	var authorized bool
	if err := tx.QueryRow(ctx, `
		select r.selected_source_count,c.notebook_id,
			exists(select 1 from notebook_memberships m where m.notebook_id=c.notebook_id and m.user_id=r.user_id)
		from agent_runs r join chat_chats c on c.id=r.chat_id and c.creator_user_id=r.user_id
		where r.id=$1
	`, runID).Scan(&selectedCount, &notebookID, &authorized); err != nil {
		return "", err
	}
	if !authorized {
		return "", ErrGroundingInvalid
	}
	if selectedCount > 0 {
		var validPins int
		if err := tx.QueryRow(ctx, `
			select count(*)
			from agent_run_evidence_set e
			join source_sources s on s.id=e.source_id and s.notebook_id=e.notebook_id and s.state='ready'
			join source_evidence_revisions r on r.id=e.evidence_revision_id and r.source_id=e.source_id and r.status='active'
			join retrieval_source_index_builds b on b.revision_id=e.evidence_revision_id and b.source_id=e.source_id
				and b.notebook_id=e.notebook_id and b.index_version_id=e.index_version_id and b.status='verified'
			join retrieval_index_versions v on v.id=e.index_version_id and v.status in ('candidate','active','retired')
			where e.run_id=$1 and e.notebook_id=$2
		`, runID, notebookID).Scan(&validPins); err != nil {
			return "", err
		}
		if validPins != selectedCount {
			return "", ErrGroundingInvalid
		}
	}
	var storedHash, outcome string
	var researchPerformed, researchComplete, degraded bool
	err := tx.QueryRow(ctx, `
		select draft_sha256,outcome,research_performed,research_complete,retrieval_degraded
		from agent_run_grounding_plans where run_id=$1
	`, runID).Scan(&storedHash, &outcome, &researchPerformed, &researchComplete, &degraded)
	if errors.Is(err, pgx.ErrNoRows) && selectedCount == 0 {
		return "source_less", nil
	}
	if err != nil {
		return "", err
	}
	if outcome != "source_less" && outcome != "source_free" && outcome != "source_cited" {
		return "", ErrGroundingInvalid
	}
	references, err := loadDraftSourceReferences(ctx, tx, runID, notebookID)
	if err != nil {
		return "", err
	}
	draftHash, err := sourceGroundingPlanSHA256(expected.Text, references)
	if err != nil || storedHash != draftHash {
		return "", ErrGroundingInvalid
	}
	if err := validateSourceReferenceDraft(ctx, tx, runID, outcome, selectedCount, researchPerformed, researchComplete, degraded, expected.Text, references); err != nil {
		return "", err
	}
	return outcome, nil
}

func loadDraftSourceReferences(ctx context.Context, tx pgx.Tx, runID, notebookID string) ([]string, error) {
	rows, err := tx.Query(ctx, `
		select reference_ordinal,source_id,notebook_id
		from agent_draft_source_references where run_id=$1 order by reference_ordinal
	`, runID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	references := make([]string, 0)
	for rows.Next() {
		var ordinal int
		var sourceID, storedNotebookID string
		if err := rows.Scan(&ordinal, &sourceID, &storedNotebookID); err != nil {
			return nil, err
		}
		if ordinal != len(references) || storedNotebookID != notebookID {
			return nil, ErrGroundingInvalid
		}
		references = append(references, sourceID)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return references, nil
}

func validateSourceReferenceDraft(
	ctx context.Context,
	tx pgx.Tx,
	runID, outcome string,
	selectedCount int,
	researchPerformed, researchComplete, degraded bool,
	text string,
	references []string,
) error {
	if outcome == "source_less" {
		if selectedCount != 0 || researchPerformed || researchComplete || degraded || len(references) != 0 {
			return ErrGroundingInvalid
		}
		normalized, _, _ := normalizeSourceMarkers(text, nil)
		if normalized != text {
			return ErrGroundingInvalid
		}
		return nil
	}
	if selectedCount == 0 || !researchPerformed {
		return ErrGroundingInvalid
	}
	checkpoints, err := loadRunCheckpoints(ctx, tx, runID)
	if err != nil {
		return err
	}
	prefix, err := LoadCheckpointPrefix(ctx, checkpoints)
	if err != nil {
		return err
	}
	research, err := parseResearchState(prefix)
	if err != nil || !research.performed || research.complete != researchComplete || research.degraded != degraded {
		return ErrGroundingInvalid
	}
	allowed := make(map[string]struct{})
	for _, item := range research.ranges {
		allowed[item.SourceID] = struct{}{}
	}
	normalized, parsedReferences, discarded := normalizeSourceMarkers(text, allowed)
	if normalized != text || discarded != 0 || !sameSourceReferences(parsedReferences, references) {
		return ErrGroundingInvalid
	}
	if (outcome == "source_free" && len(references) != 0) || (outcome == "source_cited" && len(references) == 0) {
		return ErrGroundingInvalid
	}
	return nil
}

func sameSourceReferences(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}
