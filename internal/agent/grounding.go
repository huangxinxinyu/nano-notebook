package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

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
)

type GroundingService struct {
	pool *pgxpool.Pool
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

func NewGroundingService(pool *pgxpool.Pool) *GroundingService {
	return &GroundingService{pool: pool}
}

func (s *GroundingService) Prepare(ctx context.Context, attempt Attempt, prefix CheckpointPrefix, draft models.FinalDraft) (models.FinalDraft, error) {
	prepared, err := s.prepare(ctx, attempt, prefix, draft)
	return prepared.draft, err
}

type groundingPreparation struct {
	draft                models.FinalDraft
	outcome              string
	research             researchState
	eligibleSourceCount  int
	validReferenceCount  int
	discardedMarkerCount int
}

func (s *GroundingService) PrepareTraced(ctx context.Context, tracer *agentobs.Tracer, stager ReplayStager, attempt Attempt, prefix CheckpointPrefix, draft models.FinalDraft) (models.FinalDraft, error) {
	if tracer == nil {
		return s.Prepare(ctx, attempt, prefix, draft)
	}
	identity := fmt.Sprintf("run/%s/attempt/%d/grounding", attempt.RunID, attempt.AttemptNo)
	prepared, err := instrumentation.Invoke(ctx, tracer, agentobs.SpanStart{
		IdentityKey: identity + "/start", Name: TraceSpanGrounding,
	}, func(callContext context.Context) (groundingPreparation, error) {
		return s.prepare(callContext, attempt, prefix, draft)
	}, func(result groundingPreparation, callErr error) agentobs.SpanEnd {
		status := agentobs.StatusOK
		attributes := groundingTraceAttributes(result)
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

func groundingTraceAttributes(result groundingPreparation) []agentobs.Attribute {
	return []agentobs.Attribute{
		agentobs.String(TraceKeyGroundingOutcome, result.outcome),
		agentobs.Bool(TraceKeyGroundingResearchPerformed, result.research.performed),
		agentobs.Bool(TraceKeyGroundingResearchComplete, result.research.complete),
		agentobs.Bool(TraceKeyGroundingResearchDegraded, result.research.degraded),
		agentobs.Int64(TraceKeyEligibleSourceCount, int64(result.eligibleSourceCount)),
		agentobs.Int64(TraceKeyValidSourceReferenceCount, int64(result.validReferenceCount)),
		agentobs.Int64(TraceKeyDiscardedSourceMarkerCount, int64(result.discardedMarkerCount)),
	}
}

func groundingErrorKind(err error) string {
	switch {
	case errors.Is(err, ErrGroundingInvalid):
		return "grounding_invalid"
	case errors.Is(err, ErrGroundingIncomplete):
		return "grounding_incomplete"
	default:
		return "grounding_failed"
	}
}

func (s *GroundingService) prepare(ctx context.Context, attempt Attempt, prefix CheckpointPrefix, draft models.FinalDraft) (groundingPreparation, error) {
	result := groundingPreparation{draft: draft}
	if s == nil || s.pool == nil || draft.Validate() != nil {
		return groundingPreparation{}, ErrGroundingInvalid
	}
	selectedCount, err := s.selectedSourceCount(ctx, attempt)
	if err != nil {
		return groundingPreparation{}, err
	}
	if selectedCount == 0 {
		normalizedText, references, discarded := normalizeSourceMarkers(draft.Text, nil)
		draft.Text = normalizedText
		result.validReferenceCount = len(references)
		result.discardedMarkerCount = discarded
		if strings.TrimSpace(draft.Text) == "" {
			return groundingPreparation{}, ErrGroundingInvalid
		}
		if err := s.persistSourcePlan(ctx, attempt, draft, "source_less", researchState{}, nil); err != nil {
			return groundingPreparation{}, err
		}
		result.draft = draft
		result.outcome = "source_less"
		return result, nil
	}
	research, err := parseResearchState(prefix)
	if err != nil {
		return groundingPreparation{}, err
	}
	result.research = research
	if !research.performed {
		return result, ErrGroundingIncomplete
	}
	allowed := make(map[string]struct{})
	for _, item := range research.ranges {
		allowed[item.SourceID] = struct{}{}
	}
	result.eligibleSourceCount = len(allowed)
	normalizedText, references, discarded := normalizeSourceMarkers(draft.Text, allowed)
	result.validReferenceCount = len(references)
	result.discardedMarkerCount = discarded
	draft.Text = normalizedText
	if strings.TrimSpace(draft.Text) == "" {
		return result, ErrGroundingInvalid
	}
	outcome := "source_free"
	if len(references) > 0 {
		outcome = "source_cited"
	}
	if err := s.persistSourcePlan(ctx, attempt, draft, outcome, research, references); err != nil {
		return result, err
	}
	result.draft = draft
	result.outcome = outcome
	return result, nil
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

func (s *GroundingService) persistSourcePlan(ctx context.Context, attempt Attempt, draft models.FinalDraft, outcome string, research researchState, references []string) error {
	draftHash, err := sourceGroundingPlanSHA256(draft.Text, references)
	if err != nil {
		return err
	}
	tx, err := s.workerTx(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var notebookID string
	err = tx.QueryRow(ctx, `
		select c.notebook_id
		from agent_runs r
		join agent_jobs j on j.run_id=r.id
		join chat_chats c on c.id=r.chat_id
		where r.id=$1 and j.id=$2 and j.attempt_no=$3 and j.lease_token=$4::uuid
			and r.status='running' and r.output_message_id is null and r.deadline_at>now()
			and j.status='running' and j.lease_expires_at>now()
	`, attempt.RunID, attempt.JobID, attempt.AttemptNo, attempt.LeaseToken).Scan(&notebookID)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrLeaseLost
	}
	if err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `
		insert into agent_run_grounding_plans(
			run_id,draft_sha256,outcome,research_performed,research_complete,retrieval_degraded
		) values($1,$2,$3,$4,$5,$6) on conflict (run_id) do nothing
	`, attempt.RunID, draftHash, outcome, research.performed, research.complete, research.degraded); err != nil {
		return err
	}
	var storedHash, storedOutcome string
	var storedPerformed bool
	if err := tx.QueryRow(ctx, `
		select draft_sha256,outcome,research_performed from agent_run_grounding_plans where run_id=$1
	`, attempt.RunID).Scan(&storedHash, &storedOutcome, &storedPerformed); err != nil {
		return err
	}
	if storedHash != draftHash || storedOutcome != outcome || storedPerformed != research.performed {
		return ErrGroundingInvalid
	}
	for ordinal, sourceID := range references {
		var authorized bool
		if err := tx.QueryRow(ctx, `
			select exists(
				select 1 from agent_run_evidence_set e
				join source_sources s on s.id=e.source_id and s.notebook_id=e.notebook_id and s.state='ready'
				join source_evidence_revisions r on r.id=e.evidence_revision_id and r.source_id=e.source_id and r.status='active'
				where e.run_id=$1 and e.notebook_id=$2 and e.source_id=$3
			)
		`, attempt.RunID, notebookID, sourceID).Scan(&authorized); err != nil {
			return err
		}
		if !authorized {
			return ErrGroundingInvalid
		}
		citationID := sourceReferenceIdentity(attempt.RunID, ordinal, sourceID)
		if _, err := tx.Exec(ctx, `
			insert into agent_draft_source_references(
				run_id,reference_ordinal,citation_id,notebook_id,source_id
			) values($1,$2,$3,$4,$5) on conflict do nothing
		`, attempt.RunID, ordinal, citationID, notebookID, sourceID); err != nil {
			return err
		}
	}
	var storedReferences int
	if err := tx.QueryRow(ctx, `select count(*) from agent_draft_source_references where run_id=$1`, attempt.RunID).Scan(&storedReferences); err != nil {
		return err
	}
	if storedReferences != len(references) {
		return ErrGroundingInvalid
	}
	return tx.Commit(ctx)
}

func sourceGroundingPlanSHA256(text string, references []string) (string, error) {
	encoded, err := json.Marshal(struct {
		Text       string   `json:"text"`
		References []string `json:"source_references"`
	}{Text: text, References: references})
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}

func sourceReferenceIdentity(runID string, ordinal int, sourceID string) string {
	digest := sha256.Sum256([]byte(fmt.Sprintf("%s\x00%d\x00%s", runID, ordinal, sourceID)))
	return "cit_" + hex.EncodeToString(digest[:16])
}

func finalDraftSHA256(draft models.FinalDraft) (string, error) {
	encoded, err := json.Marshal(draft)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
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
