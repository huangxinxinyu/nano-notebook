package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode"

	"github.com/huangxinxinyu/nano-notebook/internal/agentobs"
	"github.com/huangxinxinyu/nano-notebook/internal/agentobs/instrumentation"
	"github.com/huangxinxinyu/nano-notebook/internal/agentobs/semconv"
	"github.com/huangxinxinyu/nano-notebook/internal/models"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var (
	ErrGroundingInvalid    = errors.New("grounding evidence or citation is invalid")
	ErrGroundingIncomplete = errors.New("grounding research is incomplete")
	ErrClaimUnsupported    = errors.New("a material claim is not supported")
)

type ClaimSupportVerifier interface {
	VerifyClaimSupport(context.Context, models.ClaimSupportRequest) (models.ClaimSupportOutcome, error)
}

type GroundingConfig struct {
	VerifierModel         string
	VerifierPromptVersion string
}

type GroundingService struct {
	pool     *pgxpool.Pool
	verifier ClaimSupportVerifier
	config   GroundingConfig
}

type researchRange struct {
	SourceID   string
	RevisionID string
	UnitID     string
	StartRune  int
	EndRune    int
}

type researchState struct {
	performed    bool
	complete     bool
	degraded     bool
	evidenceSeen bool
	ranges       []researchRange
}

func NewGroundingService(pool *pgxpool.Pool, verifier ClaimSupportVerifier, config GroundingConfig) *GroundingService {
	return &GroundingService{pool: pool, verifier: verifier, config: config}
}

func (s *GroundingService) Prepare(ctx context.Context, attempt Attempt, prefix CheckpointPrefix, draft models.FinalDraft) (models.FinalDraft, error) {
	prepared, err := s.prepare(ctx, nil, nil, attempt, prefix, draft)
	return prepared.draft, err
}

type groundingPreparation struct {
	draft    models.FinalDraft
	outcome  string
	research researchState
}

func (s *GroundingService) PrepareTraced(ctx context.Context, tracer *agentobs.Tracer, stager ReplayStager, attempt Attempt, prefix CheckpointPrefix, draft models.FinalDraft) (models.FinalDraft, error) {
	if tracer == nil {
		return s.Prepare(ctx, attempt, prefix, draft)
	}
	identity := fmt.Sprintf("run/%s/attempt/%d/grounding", attempt.RunID, attempt.AttemptNo)
	prepared, err := instrumentation.Invoke(ctx, tracer, agentobs.SpanStart{
		IdentityKey: identity + "/start", Name: TraceSpanGrounding,
		Attributes: []agentobs.Attribute{agentobs.Int64(TraceKeyVerifierClaimCount, int64(len(draft.Claims)))},
	}, func(callContext context.Context) (groundingPreparation, error) {
		return s.prepare(callContext, tracer, stager, attempt, prefix, draft)
	}, func(result groundingPreparation, callErr error) agentobs.SpanEnd {
		status := agentobs.StatusOK
		attributes := []agentobs.Attribute{
			agentobs.String(TraceKeyGroundingOutcome, result.outcome),
			agentobs.Bool(TraceKeyGroundingResearchComplete, result.research.complete),
			agentobs.Bool(TraceKeyGroundingResearchDegraded, result.research.degraded),
			agentobs.Int64(TraceKeyVerifierSupportedCount, int64(len(result.draft.Claims))),
		}
		if callErr != nil {
			status = agentobs.StatusError
			if errors.Is(callErr, context.Canceled) {
				status = agentobs.StatusCancelled
			}
			attributes = append(attributes, agentobs.String(semconv.ErrorKindKey, groundingErrorKind(callErr)))
		}
		return agentobs.SpanEnd{Name: TraceSpanGrounding, Status: status, Attributes: attributes}
	})
	return prepared.draft, err
}

func groundingErrorKind(err error) string {
	switch {
	case errors.Is(err, ErrGroundingInvalid):
		return "grounding_invalid"
	case errors.Is(err, ErrGroundingIncomplete):
		return "grounding_incomplete"
	case errors.Is(err, ErrClaimUnsupported):
		return "claim_unsupported"
	default:
		return "grounding_failed"
	}
}

func (s *GroundingService) prepare(ctx context.Context, tracer *agentobs.Tracer, stager ReplayStager, attempt Attempt, prefix CheckpointPrefix, draft models.FinalDraft) (groundingPreparation, error) {
	result := groundingPreparation{draft: draft}
	if s == nil || s.pool == nil || draft.Validate() != nil {
		return groundingPreparation{}, ErrGroundingInvalid
	}
	selectedCount, err := s.selectedSourceCount(ctx, attempt)
	if err != nil {
		return groundingPreparation{}, err
	}
	if selectedCount == 0 {
		if len(draft.Claims) != 0 {
			return groundingPreparation{}, ErrGroundingInvalid
		}
		if err := s.persistPlan(ctx, attempt, draft, "source_less", false, false, nil, "", ""); err != nil {
			return groundingPreparation{}, err
		}
		result.outcome = "source_less"
		return result, nil
	}
	research, err := parseResearchState(prefix)
	if err != nil {
		return groundingPreparation{}, err
	}
	result.research = research
	if len(draft.Claims) == 0 {
		if research.evidenceSeen {
			return result, ErrGroundingIncomplete
		}
		if err := s.persistPlan(ctx, attempt, draft, "source_free", research.complete, research.degraded, nil, "", ""); err != nil {
			return result, err
		}
		result.outcome = "source_free"
		return result, nil
	}
	if !research.evidenceSeen || s.verifier == nil || strings.TrimSpace(s.config.VerifierModel) == "" || strings.TrimSpace(s.config.VerifierPromptVersion) == "" {
		return result, ErrGroundingIncomplete
	}
	claims, notebookID, err := s.authoritativeClaims(ctx, attempt, draft, research)
	if err != nil {
		return result, err
	}
	request := models.ClaimSupportRequest{
		Model: s.config.VerifierModel, PromptVersion: s.config.VerifierPromptVersion, Answer: draft.Text, Claims: claims,
	}
	var verified models.ClaimSupportOutcome
	if tracer != nil {
		identity := fmt.Sprintf("run/%s/attempt/%d/grounding/verifier", attempt.RunID, attempt.AttemptNo)
		verified, err = InvokeClaimSupportVerifier(ctx, tracer, s.verifier, request, ClaimSupportTraceOptions{
			StartIdentity: identity + "/start", RequestIdentity: identity + "/replay/request",
			VerdictIdentity: identity + "/replay/verdict", ReplayStager: stager,
		})
	} else {
		verified, err = s.verifier.VerifyClaimSupport(ctx, request)
	}
	if err != nil {
		return result, err
	}
	if len(verified.Verdicts) != len(claims) {
		return result, ErrGroundingInvalid
	}
	if !validUncoveredClaims(draft, verified.UncoveredClaims) {
		return result, ErrGroundingInvalid
	}
	unsupported := make(map[int]struct{})
	for ordinal, verdict := range verified.Verdicts {
		if verdict.Ordinal != ordinal {
			return result, ErrGroundingInvalid
		}
		if !verdict.Supported {
			unsupported[ordinal] = struct{}{}
		}
	}
	if len(unsupported) > 0 {
		draft = removeUnsupportedClaims(draft, unsupported)
	}
	draft = discloseUncoveredClaims(draft, verified.UncoveredClaims)
	groundingOutcome := "supported"
	var planNotebook *string = &notebookID
	if len(draft.Claims) == 0 {
		groundingOutcome = "insufficient_evidence"
		planNotebook = nil
	}
	if err := s.persistPlan(ctx, attempt, draft, groundingOutcome, research.complete, research.degraded, planNotebook, s.config.VerifierModel, s.config.VerifierPromptVersion); err != nil {
		return result, err
	}
	result.draft = draft
	result.outcome = groundingOutcome
	return result, nil
}

func removeUnsupportedClaims(draft models.FinalDraft, unsupported map[int]struct{}) models.FinalDraft {
	message := insufficientEvidenceMessage(draft.Text)
	claims := make([]models.DraftClaim, 0, len(draft.Claims)-len(unsupported))
	for ordinal, claim := range draft.Claims {
		if _, remove := unsupported[ordinal]; remove {
			draft.Text = strings.ReplaceAll(draft.Text, claim.Text, message)
			continue
		}
		claims = append(claims, claim)
	}
	draft.Claims = claims
	return draft
}

func validUncoveredClaims(draft models.FinalDraft, uncovered []string) bool {
	if len(uncovered) > 64 {
		return false
	}
	seen := make(map[string]struct{}, len(uncovered))
	for _, claim := range uncovered {
		if strings.TrimSpace(claim) != claim || claim == "" || len([]rune(claim)) > 4000 || !strings.Contains(draft.Text, claim) {
			return false
		}
		if _, duplicate := seen[claim]; duplicate {
			return false
		}
		for _, declared := range draft.Claims {
			if strings.Contains(claim, declared.Text) || strings.Contains(declared.Text, claim) {
				return false
			}
		}
		seen[claim] = struct{}{}
	}
	return true
}

func discloseUncoveredClaims(draft models.FinalDraft, uncovered []string) models.FinalDraft {
	message := insufficientEvidenceMessage(draft.Text)
	for _, claim := range uncovered {
		draft.Text = strings.ReplaceAll(draft.Text, claim, message)
	}
	return draft
}

func insufficientEvidenceMessage(text string) string {
	if containsHan(text) {
		return "所选来源没有为这一点提供足够证据。"
	}
	return "The selected Sources do not provide enough evidence for this point."
}

func parseResearchState(prefix CheckpointPrefix) (researchState, error) {
	state := researchState{complete: true}
	for _, proposal := range prefix.Proposals {
		for _, action := range proposal.Actions {
			if action.Name != "search_evidence" {
				continue
			}
			state.performed = true
			if action.Result == nil || action.Result.Status != ActionSucceeded {
				state.complete = false
				state.degraded = true
				continue
			}
			var output struct {
				CompleteEmpty bool `json:"complete_empty"`
				Degraded      bool `json:"degraded"`
				Evidence      []struct {
					SourceID           string `json:"source_id"`
					EvidenceRevisionID string `json:"evidence_revision_id"`
					EvidenceRanges     []struct {
						UnitID    string `json:"unit_id"`
						StartRune int    `json:"start_rune"`
						EndRune   int    `json:"end_rune"`
					} `json:"evidence_ranges"`
				} `json:"evidence"`
			}
			if json.Unmarshal(action.Result.Output, &output) != nil {
				return researchState{}, ErrGroundingInvalid
			}
			state.degraded = state.degraded || output.Degraded
			if len(output.Evidence) == 0 && !output.CompleteEmpty {
				state.complete = false
			}
			for _, evidence := range output.Evidence {
				for _, item := range evidence.EvidenceRanges {
					if evidence.SourceID == "" || evidence.EvidenceRevisionID == "" || item.UnitID == "" || item.StartRune < 0 || item.EndRune <= item.StartRune {
						return researchState{}, ErrGroundingInvalid
					}
					state.evidenceSeen = true
					state.ranges = append(state.ranges, researchRange{
						SourceID: evidence.SourceID, RevisionID: evidence.EvidenceRevisionID, UnitID: item.UnitID,
						StartRune: item.StartRune, EndRune: item.EndRune,
					})
				}
			}
		}
	}
	if !state.performed {
		state.complete = false
	}
	return state, nil
}

func (s *GroundingService) selectedSourceCount(ctx context.Context, attempt Attempt) (int, error) {
	tx, err := s.workerTx(ctx)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var count int
	err = tx.QueryRow(ctx, `
		select r.selected_source_count from agent_runs r join agent_jobs j on j.run_id=r.id
		where r.id=$1 and j.id=$2 and j.attempt_no=$3 and j.lease_token=$4::uuid
			and r.status='running' and r.output_message_id is null and r.deadline_at>now()
			and j.status='running' and j.lease_expires_at>now()
	`, attempt.RunID, attempt.JobID, attempt.AttemptNo, attempt.LeaseToken).Scan(&count)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, ErrLeaseLost
	}
	if err != nil {
		return 0, err
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, err
	}
	return count, nil
}

func (s *GroundingService) authoritativeClaims(ctx context.Context, attempt Attempt, draft models.FinalDraft, research researchState) ([]models.ClaimSupportInput, string, error) {
	tx, err := s.workerTx(ctx)
	if err != nil {
		return nil, "", err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var notebookID string
	if err := tx.QueryRow(ctx, `
		select c.notebook_id from agent_runs r join chat_chats c on c.id=r.chat_id
		where r.id=$1
	`, attempt.RunID).Scan(&notebookID); err != nil {
		return nil, "", err
	}
	claims := make([]models.ClaimSupportInput, 0, len(draft.Claims))
	for ordinal, claim := range draft.Claims {
		input := models.ClaimSupportInput{Ordinal: ordinal, Text: claim.Text, Evidence: make([]models.ClaimEvidence, 0, len(claim.Citations))}
		for _, citation := range claim.Citations {
			if !addressWasRetrieved(citation, research.ranges) {
				return nil, "", ErrGroundingInvalid
			}
			var text string
			err := tx.QueryRow(ctx, `
				select u.text_content
				from agent_run_evidence_set e
				join source_sources s on s.id=e.source_id and s.notebook_id=e.notebook_id and s.state='ready'
				join source_evidence_revisions r on r.id=e.evidence_revision_id and r.source_id=e.source_id and r.status='active'
				join source_evidence_units u on u.id=$5 and u.revision_id=r.id and u.source_id=s.id and u.notebook_id=s.notebook_id
				where e.run_id=$1 and e.notebook_id=$2 and e.source_id=$3 and e.evidence_revision_id=$4
			`, attempt.RunID, notebookID, citation.SourceID, citation.EvidenceRevisionID, citation.UnitID).Scan(&text)
			if errors.Is(err, pgx.ErrNoRows) {
				return nil, "", ErrGroundingInvalid
			}
			if err != nil {
				return nil, "", err
			}
			runes := []rune(text)
			if citation.EndRune > len(runes) {
				return nil, "", ErrGroundingInvalid
			}
			input.Evidence = append(input.Evidence, models.ClaimEvidence{
				SourceID: citation.SourceID, RevisionID: citation.EvidenceRevisionID, UnitID: citation.UnitID,
				StartRune: citation.StartRune, EndRune: citation.EndRune, Text: string(runes[citation.StartRune:citation.EndRune]),
			})
		}
		claims = append(claims, input)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, "", err
	}
	return claims, notebookID, nil
}

func addressWasRetrieved(address models.EvidenceAddress, ranges []researchRange) bool {
	for _, item := range ranges {
		if address.SourceID == item.SourceID && address.EvidenceRevisionID == item.RevisionID && address.UnitID == item.UnitID &&
			address.StartRune >= item.StartRune && address.EndRune <= item.EndRune {
			return true
		}
	}
	return false
}

func containsHan(value string) bool {
	for _, character := range value {
		if unicode.Is(unicode.Han, character) {
			return true
		}
	}
	return false
}

func (s *GroundingService) persistPlan(ctx context.Context, attempt Attempt, draft models.FinalDraft, outcome string, complete, degraded bool, notebookID *string, verifierModel, verifierPrompt string) error {
	draftHash, err := finalDraftSHA256(draft)
	if err != nil {
		return err
	}
	tx, err := s.workerTx(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var authoritative bool
	if err := tx.QueryRow(ctx, `
		select exists(
			select 1 from agent_runs r join agent_jobs j on j.run_id=r.id
			where r.id=$1 and j.id=$2 and j.attempt_no=$3 and j.lease_token=$4::uuid
				and r.status='running' and r.output_message_id is null and r.deadline_at>now()
				and j.status='running' and j.lease_expires_at>now()
		)
	`, attempt.RunID, attempt.JobID, attempt.AttemptNo, attempt.LeaseToken).Scan(&authoritative); err != nil {
		return err
	}
	if !authoritative {
		return ErrLeaseLost
	}
	if _, err := tx.Exec(ctx, `
		insert into agent_run_grounding_plans(
			run_id,draft_sha256,outcome,research_complete,retrieval_degraded,verifier_model,verifier_prompt_version
		) values($1,$2,$3,$4,$5,$6,$7) on conflict (run_id) do nothing
	`, attempt.RunID, draftHash, outcome, complete, degraded, verifierModel, verifierPrompt); err != nil {
		return err
	}
	var storedHash, storedOutcome string
	if err := tx.QueryRow(ctx, `select draft_sha256,outcome from agent_run_grounding_plans where run_id=$1`, attempt.RunID).Scan(&storedHash, &storedOutcome); err != nil {
		return err
	}
	if storedHash != draftHash || storedOutcome != outcome {
		return ErrGroundingInvalid
	}
	if outcome == "supported" {
		if notebookID == nil {
			return ErrGroundingInvalid
		}
		for claimOrdinal, claim := range draft.Claims {
			if _, err := tx.Exec(ctx, `
				insert into agent_claim_support_records(run_id,claim_ordinal,claim_text,verdict)
				values($1,$2,$3,'supported') on conflict do nothing
			`, attempt.RunID, claimOrdinal, claim.Text); err != nil {
				return err
			}
			for citationOrdinal, citation := range claim.Citations {
				citationID := citationIdentity(attempt.RunID, claimOrdinal, citationOrdinal, citation)
				if _, err := tx.Exec(ctx, `
					insert into agent_draft_citations(
						run_id,claim_ordinal,citation_ordinal,citation_id,notebook_id,source_id,evidence_revision_id,unit_id,start_rune,end_rune
					) values($1,$2,$3,$4,$5,$6,$7,$8,$9,$10) on conflict do nothing
				`, attempt.RunID, claimOrdinal, citationOrdinal, citationID, *notebookID, citation.SourceID, citation.EvidenceRevisionID, citation.UnitID, citation.StartRune, citation.EndRune); err != nil {
					return err
				}
			}
		}
	}
	return tx.Commit(ctx)
}

func finalDraftSHA256(draft models.FinalDraft) (string, error) {
	encoded, err := json.Marshal(draft)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}

func citationIdentity(runID string, claimOrdinal, citationOrdinal int, address models.EvidenceAddress) string {
	value := fmt.Sprintf("%s\x00%d\x00%d\x00%s\x00%s\x00%s\x00%d\x00%d", runID, claimOrdinal, citationOrdinal,
		address.SourceID, address.EvidenceRevisionID, address.UnitID, address.StartRune, address.EndRune)
	digest := sha256.Sum256([]byte(value))
	return "cit_" + hex.EncodeToString(digest[:16])
}

func (s *GroundingService) workerTx(ctx context.Context) (pgx.Tx, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx, `set local role nano_worker`); err != nil {
		_ = tx.Rollback(ctx)
		return nil, err
	}
	return tx, nil
}
