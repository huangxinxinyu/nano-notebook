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
	draftHash, err := finalDraftSHA256(*expected)
	if err != nil {
		return "", err
	}
	var storedHash, outcome, verifierModel, verifierPrompt string
	var researchComplete, degraded bool
	err = tx.QueryRow(ctx, `
		select draft_sha256,outcome,research_complete,retrieval_degraded,verifier_model,verifier_prompt_version
		from agent_run_grounding_plans where run_id=$1
	`, runID).Scan(&storedHash, &outcome, &researchComplete, &degraded, &verifierModel, &verifierPrompt)
	if errors.Is(err, pgx.ErrNoRows) && selectedCount == 0 && len(expected.Claims) == 0 {
		return "source_less", nil
	}
	if err != nil {
		return "", err
	}
	if storedHash != draftHash {
		return "", ErrGroundingInvalid
	}
	switch outcome {
	case "source_less":
		if selectedCount != 0 || len(expected.Claims) != 0 || researchComplete || degraded || verifierModel != "" || verifierPrompt != "" {
			return "", ErrGroundingInvalid
		}
	case "source_free":
		if selectedCount == 0 || len(expected.Claims) != 0 || verifierModel != "" || verifierPrompt != "" {
			return "", ErrGroundingInvalid
		}
	case "zero_support":
		if selectedCount == 0 || len(expected.Claims) != 0 || !researchComplete || degraded || verifierModel != "" || verifierPrompt != "" {
			return "", ErrGroundingInvalid
		}
	case "supported":
		if selectedCount == 0 || len(expected.Claims) == 0 || verifierModel == "" || verifierPrompt == "" {
			return "", ErrGroundingInvalid
		}
		if err := validateSupportedDraft(ctx, tx, runID, notebookID, *expected); err != nil {
			return "", err
		}
	case "insufficient_evidence":
		if selectedCount == 0 || len(expected.Claims) != 0 || verifierModel == "" || verifierPrompt == "" {
			return "", ErrGroundingInvalid
		}
		var claims, citations int
		if err := tx.QueryRow(ctx, `select count(*) from agent_claim_support_records where run_id=$1`, runID).Scan(&claims); err != nil {
			return "", err
		}
		if err := tx.QueryRow(ctx, `select count(*) from agent_draft_citations where run_id=$1`, runID).Scan(&citations); err != nil {
			return "", err
		}
		if claims != 0 || citations != 0 {
			return "", ErrGroundingInvalid
		}
	default:
		return "", ErrGroundingInvalid
	}
	return outcome, nil
}

func validateSupportedDraft(ctx context.Context, tx pgx.Tx, runID, notebookID string, draft models.FinalDraft) error {
	var claimCount, citationCount int
	if err := tx.QueryRow(ctx, `select count(*) from agent_claim_support_records where run_id=$1 and verdict='supported'`, runID).Scan(&claimCount); err != nil {
		return err
	}
	if err := tx.QueryRow(ctx, `select count(*) from agent_draft_citations where run_id=$1`, runID).Scan(&citationCount); err != nil {
		return err
	}
	wantCitations := 0
	for claimOrdinal, claim := range draft.Claims {
		var storedText, verdict string
		if err := tx.QueryRow(ctx, `
			select claim_text,verdict from agent_claim_support_records where run_id=$1 and claim_ordinal=$2
		`, runID, claimOrdinal).Scan(&storedText, &verdict); err != nil {
			return ErrGroundingInvalid
		}
		if storedText != claim.Text || verdict != "supported" {
			return ErrGroundingInvalid
		}
		wantCitations += len(claim.Citations)
		for citationOrdinal, address := range claim.Citations {
			var storedID, storedNotebook, sourceID, revisionID, unitID string
			var startRune, endRune int
			if err := tx.QueryRow(ctx, `
				select citation_id,notebook_id,source_id,evidence_revision_id,unit_id,start_rune,end_rune
				from agent_draft_citations where run_id=$1 and claim_ordinal=$2 and citation_ordinal=$3
			`, runID, claimOrdinal, citationOrdinal).Scan(
				&storedID, &storedNotebook, &sourceID, &revisionID, &unitID, &startRune, &endRune,
			); err != nil {
				return ErrGroundingInvalid
			}
			if storedID != citationIdentity(runID, claimOrdinal, citationOrdinal, address) || storedNotebook != notebookID ||
				sourceID != address.SourceID || revisionID != address.EvidenceRevisionID || unitID != address.UnitID ||
				startRune != address.StartRune || endRune != address.EndRune {
				return ErrGroundingInvalid
			}
			var unitRunes int
			if err := tx.QueryRow(ctx, `
				select char_length(u.text_content)
				from agent_run_evidence_set e
				join source_sources s on s.id=e.source_id and s.state='ready'
				join source_evidence_revisions r on r.id=e.evidence_revision_id and r.status='active'
				join source_evidence_units u on u.id=$5 and u.revision_id=r.id and u.source_id=s.id
				where e.run_id=$1 and e.notebook_id=$2 and e.source_id=$3 and e.evidence_revision_id=$4
			`, runID, notebookID, sourceID, revisionID, unitID).Scan(&unitRunes); err != nil || endRune > unitRunes {
				return ErrGroundingInvalid
			}
		}
	}
	if claimCount != len(draft.Claims) || citationCount != wantCitations {
		return ErrGroundingInvalid
	}
	return nil
}
