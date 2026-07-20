package app_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/huangxinxinyu/nano-notebook/internal/agent"
	"github.com/huangxinxinyu/nano-notebook/internal/jobs"
	"github.com/huangxinxinyu/nano-notebook/internal/models"
)

type claimVerifierStub struct {
	calls   int
	request models.ClaimSupportRequest
	pass    bool
}

func (s *claimVerifierStub) VerifyClaimSupport(_ context.Context, request models.ClaimSupportRequest) (models.ClaimSupportOutcome, error) {
	s.calls++
	s.request = request
	verdicts := make([]models.ClaimSupportVerdict, len(request.Claims))
	for index := range verdicts {
		verdicts[index] = models.ClaimSupportVerdict{Ordinal: index, Supported: s.pass}
	}
	return models.ClaimSupportOutcome{Verdicts: verdicts}, nil
}

type fallbackDecisionStub struct {
	calls   int
	request models.ModelRequest
}

func (s *fallbackDecisionStub) Decide(_ context.Context, request models.ModelRequest) (models.ModelOutcome, error) {
	s.calls++
	s.request = request
	return models.ModelOutcome{ModelDecision: models.ModelDecision{Final: &models.FinalDraft{Text: "General knowledge answer."}}, Metadata: models.ModelCallMetadata{
		RequestedModel: request.Model, ResultKind: models.ModelResultFinalDraft,
	}}, nil
}

func TestGroundingPreparesSupportedClaimsFromCheckpointedEvidence(t *testing.T) {
	api, attempt, notebookID, _ := groundingFixture(t, "supported-grounding@example.com", "src_ground", "evr_ground")
	verifier := &claimVerifierStub{pass: true}
	service := agent.NewGroundingService(api.db.Pool(), verifier, &fallbackDecisionStub{}, agent.GroundingConfig{
		VerifierModel: "verifier-test", VerifierPromptVersion: "claim-support-v1",
	})
	prefix := checkpointedEvidencePrefix("src_ground", "evr_ground", "unit_ground", 0, 27, false, false)
	draft := models.FinalDraft{Text: "The launch is 20 July.", Claims: []models.DraftClaim{{
		Text: "The launch is 20 July.", Citations: []models.EvidenceAddress{{
			SourceID: "src_ground", EvidenceRevisionID: "evr_ground", UnitID: "unit_ground", StartRune: 0, EndRune: 27,
		}},
	}}}
	prepared, err := service.Prepare(context.Background(), attempt, prefix, draft)
	if err != nil {
		t.Fatal(err)
	}
	if prepared.Text != draft.Text || verifier.calls != 1 || len(verifier.request.Claims) != 1 || verifier.request.Claims[0].Evidence[0].Text != "The launch date is 20 July." {
		t.Fatalf("prepared/verifier=%+v/%+v", prepared, verifier)
	}
	var outcome string
	var claims, citations int
	if err := api.db.Pool().QueryRow(context.Background(), `select outcome from agent_run_grounding_plans where run_id=$1`, attempt.RunID).Scan(&outcome); err != nil {
		t.Fatal(err)
	}
	if err := api.db.Pool().QueryRow(context.Background(), `select count(*) from agent_claim_support_records where run_id=$1 and verdict='supported'`, attempt.RunID).Scan(&claims); err != nil {
		t.Fatal(err)
	}
	if err := api.db.Pool().QueryRow(context.Background(), `select count(*) from agent_draft_citations where run_id=$1 and notebook_id=$2`, attempt.RunID, notebookID).Scan(&citations); err != nil {
		t.Fatal(err)
	}
	if outcome != "supported" || claims != 1 || citations != 1 {
		t.Fatalf("grounding plan=%s claims=%d citations=%d", outcome, claims, citations)
	}
}

func TestGroundingRejectsForgedCitationBeforeVerifier(t *testing.T) {
	api, attempt, _, _ := groundingFixture(t, "forged-grounding@example.com", "src_forged_ground", "evr_forged_ground")
	verifier := &claimVerifierStub{pass: true}
	service := agent.NewGroundingService(api.db.Pool(), verifier, &fallbackDecisionStub{}, agent.GroundingConfig{VerifierModel: "v", VerifierPromptVersion: "p"})
	prefix := checkpointedEvidencePrefix("src_forged_ground", "evr_forged_ground", "unit_ground", 0, 27, false, false)
	_, err := service.Prepare(context.Background(), attempt, prefix, models.FinalDraft{Text: "Forged claim.", Claims: []models.DraftClaim{{
		Text: "Forged claim.", Citations: []models.EvidenceAddress{{
			SourceID: "src_forged_ground", EvidenceRevisionID: "evr_forged_ground", UnitID: "unit_ground", StartRune: 0, EndRune: 28,
		}},
	}}})
	if !errors.Is(err, agent.ErrGroundingInvalid) || verifier.calls != 0 {
		t.Fatalf("error=%v verifier calls=%d", err, verifier.calls)
	}
}

func TestGroundingReplacesUnsupportedClaimsWithAnExplicitEvidenceGap(t *testing.T) {
	api, attempt, _, _ := groundingFixture(t, "unsupported-grounding@example.com", "src_unsupported", "evr_unsupported")
	service := agent.NewGroundingService(api.db.Pool(), &claimVerifierStub{pass: false}, &fallbackDecisionStub{}, agent.GroundingConfig{VerifierModel: "v", VerifierPromptVersion: "p"})
	prepared, err := service.Prepare(context.Background(), attempt,
		checkpointedEvidencePrefix("src_unsupported", "evr_unsupported", "unit_ground", 0, 27, false, false),
		models.FinalDraft{Text: "The unsupported launch claim.", Claims: []models.DraftClaim{{
			Text: "The unsupported launch claim.", Citations: []models.EvidenceAddress{{
				SourceID: "src_unsupported", EvidenceRevisionID: "evr_unsupported", UnitID: "unit_ground", StartRune: 0, EndRune: 27,
			}},
		}}},
	)
	if err != nil || len(prepared.Claims) != 0 || !strings.Contains(prepared.Text, "do not provide enough evidence") {
		t.Fatalf("prepared=%+v err=%v", prepared, err)
	}
	var outcome string
	if err := api.db.Pool().QueryRow(context.Background(), `select outcome from agent_run_grounding_plans where run_id=$1`, attempt.RunID).Scan(&outcome); err != nil {
		t.Fatal(err)
	}
	if outcome != "insufficient_evidence" {
		t.Fatalf("outcome=%s", outcome)
	}
}

func TestGroundingUsesFreshSourceFreeFallbackOnlyForCompleteNonDegradedZeroSupport(t *testing.T) {
	api, attempt, _, _ := groundingFixture(t, "zero-grounding@example.com", "src_zero_ground", "evr_zero_ground")
	verifier := &claimVerifierStub{pass: true}
	fallback := &fallbackDecisionStub{}
	service := agent.NewGroundingService(api.db.Pool(), verifier, fallback, agent.GroundingConfig{VerifierModel: "v", VerifierPromptVersion: "p"})
	prepared, err := service.Prepare(context.Background(), attempt, checkpointedEvidencePrefix("", "", "", 0, 0, true, false), models.FinalDraft{Text: "No source evidence.", Claims: []models.DraftClaim{}})
	if err != nil {
		t.Fatal(err)
	}
	if fallback.calls != 1 || !strings.HasPrefix(prepared.Text, "The following answer is not based on the selected Sources.") || len(prepared.Claims) != 0 {
		t.Fatalf("prepared=%+v fallback=%+v", prepared, fallback)
	}
	encoded, _ := json.Marshal(fallback.request)
	if strings.Contains(string(encoded), "source_id") || strings.Contains(string(encoded), "evidence_revision_id") || strings.Contains(string(encoded), "No source evidence") {
		t.Fatalf("fallback leaked Source research context: %s", encoded)
	}

	api2, attempt2, _, _ := groundingFixture(t, "degraded-grounding@example.com", "src_degraded_ground", "evr_degraded_ground")
	service2 := agent.NewGroundingService(api2.db.Pool(), verifier, fallback, agent.GroundingConfig{VerifierModel: "v", VerifierPromptVersion: "p"})
	_, err = service2.Prepare(context.Background(), attempt2, checkpointedEvidencePrefix("", "", "", 0, 0, false, true), models.FinalDraft{Text: "No source evidence."})
	if !errors.Is(err, agent.ErrGroundingIncomplete) || fallback.calls != 1 {
		t.Fatalf("degraded error=%v fallback calls=%d", err, fallback.calls)
	}
}

func TestPublicationAtomicallyCopiesVerifiedCitationsAndRejectsSourceDeletionRace(t *testing.T) {
	for _, removeBeforePublish := range []bool{false, true} {
		t.Run(map[bool]string{false: "publishes", true: "deletion fenced"}[removeBeforePublish], func(t *testing.T) {
			api, attempt, _, sessionCookie := groundingFixture(t, "publication-grounding-"+map[bool]string{false: "ok", true: "deleted"}[removeBeforePublish]+"@example.com", "src_publish", "evr_publish")
			service := agent.NewGroundingService(api.db.Pool(), &claimVerifierStub{pass: true}, &fallbackDecisionStub{}, agent.GroundingConfig{VerifierModel: "v", VerifierPromptVersion: "p"})
			prefix := checkpointedEvidencePrefix("src_publish", "evr_publish", "unit_ground", 0, 27, false, false)
			draft := models.FinalDraft{Text: "The launch is 20 July.", Claims: []models.DraftClaim{{
				Text: "The launch is 20 July.", Citations: []models.EvidenceAddress{{SourceID: "src_publish", EvidenceRevisionID: "evr_publish", UnitID: "unit_ground", StartRune: 0, EndRune: 27}},
			}}}
			prepared, err := service.Prepare(context.Background(), attempt, prefix, draft)
			if err != nil {
				t.Fatal(err)
			}
			runtime := agent.NewPostgresRuntime(api.db.Pool(), "", nil)
			checkpoint, err := agent.NewFinalDraftCheckpoint(1, prepared)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := runtime.AppendCheckpoint(context.Background(), attempt, checkpoint); err != nil {
				t.Fatal(err)
			}
			if removeBeforePublish {
				if _, err := api.db.Pool().Exec(context.Background(), `delete from source_sources where id='src_publish'`); err != nil {
					t.Fatal(err)
				}
			}
			err = runtime.PublishFinal(context.Background(), attempt, prepared)
			var messages, citations int
			if scanErr := api.db.Pool().QueryRow(context.Background(), `select count(*) from chat_messages where role='assistant'`).Scan(&messages); scanErr != nil {
				t.Fatal(scanErr)
			}
			if scanErr := api.db.Pool().QueryRow(context.Background(), `select count(*) from chat_citations where run_id=$1`, attempt.RunID).Scan(&citations); scanErr != nil {
				t.Fatal(scanErr)
			}
			if removeBeforePublish {
				if !errors.Is(err, agent.ErrGroundingInvalid) || messages != 0 || citations != 0 {
					t.Fatalf("deleted publication err=%v messages=%d citations=%d", err, messages, citations)
				}
			} else if err != nil || messages != 1 || citations != 1 {
				t.Fatalf("publication err=%v messages=%d citations=%d", err, messages, citations)
			} else {
				var citationID string
				if err := api.db.Pool().QueryRow(context.Background(), `select citation_id from chat_citations where run_id=$1`, attempt.RunID).Scan(&citationID); err != nil {
					t.Fatal(err)
				}
				resolved := api.getWithCookie(t, "/api/v1/citations/"+citationID, sessionCookie)
				if resolved.Code != http.StatusOK || !strings.Contains(resolved.Body.String(), "The launch date is 20 July.") ||
					strings.Contains(resolved.Body.String(), "object_key") || strings.Contains(resolved.Body.String(), "sha256") {
					t.Fatalf("Citation resolution=%d %s", resolved.Code, resolved.Body.String())
				}
				if _, err := api.db.Pool().Exec(context.Background(), `delete from source_sources where id='src_publish'`); err != nil {
					t.Fatal(err)
				}
				unavailable := api.getWithCookie(t, "/api/v1/citations/"+citationID, sessionCookie)
				if unavailable.Code != http.StatusGone || strings.Contains(unavailable.Body.String(), "launch date") {
					t.Fatalf("deleted Citation resolution=%d %s", unavailable.Code, unavailable.Body.String())
				}
			}
		})
	}
}

func groundingFixture(t *testing.T, email, sourceID, revisionID string) (*testAPI, agent.Attempt, string, *http.Cookie) {
	t.Helper()
	api := newTestAPI(t)
	sessionCookie, csrfCookie := api.registerWithCSRF(t, email)
	notebookID, chatID := createNotebookAndChatForEvidenceSet(t, api, sessionCookie, csrfCookie)
	installReadyEvidenceSetFixture(t, api, notebookID, sourceID, revisionID, "", "")
	if _, err := api.db.Pool().Exec(context.Background(), `
		insert into source_evidence_units(id,revision_id,source_id,notebook_id,ordinal,kind,text_content,start_rune,end_rune)
		values('unit_ground',$1,$2,$3,0,'paragraph','The launch date is 20 July.',0,27)
	`, revisionID, sourceID, notebookID); err != nil {
		t.Fatal(err)
	}
	response := api.postJSONWithCookieAndCSRF(t, "/api/v1/chats/"+chatID+"/messages", map[string]any{
		"id": "0190cdd2-5f2d-7ad8-b3f5-1b588788c094", "content": "When is launch?", "source_ids": []string{sourceID},
	}, sessionCookie, csrfCookie, csrfCookie.Value, "")
	if response.Code != http.StatusAccepted {
		t.Fatalf("admission=%d %s", response.Code, response.Body.String())
	}
	claimed, ok, err := jobs.NewQueue(api.db.Pool()).ClaimNext(context.Background())
	if err != nil || !ok {
		t.Fatalf("claim=%+v ok=%v err=%v", claimed, ok, err)
	}
	return api, attemptFromClaim(claimed), notebookID, sessionCookie
}

func checkpointedEvidencePrefix(sourceID, revisionID, unitID string, start, end int, completeEmpty, degraded bool) agent.CheckpointPrefix {
	output, _ := json.Marshal(map[string]any{
		"complete_empty": completeEmpty, "degraded": degraded, "degradations": []string{},
		"evidence": []any{map[string]any{
			"source_id": sourceID, "evidence_revision_id": revisionID, "source_title": "Fixture", "preview": "The launch date is 20 July.",
			"evidence_ranges": []any{map[string]any{"unit_id": unitID, "start_rune": start, "end_rune": end}},
		}},
	})
	if completeEmpty || degraded {
		output, _ = json.Marshal(map[string]any{
			"complete_empty": completeEmpty, "degraded": degraded, "degradations": []string{"reranker_unavailable"}, "evidence": []any{},
		})
	}
	result := agent.ActionResult{Status: agent.ActionSucceeded, Output: output}
	return agent.CheckpointPrefix{Proposals: []agent.AcceptedProposal{{DecisionNo: 1, Actions: []agent.AcceptedAction{{
		ActionID: "decision:1/action:0", Index: 0, Name: "search_evidence", Result: &result,
	}}}}, AcceptedDecisions: 1, AcceptedActions: 1}
}
