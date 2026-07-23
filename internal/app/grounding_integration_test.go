package app_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/huangxinxinyu/nano-notebook/internal/agent"
	"github.com/huangxinxinyu/nano-notebook/internal/jobs"
	"github.com/huangxinxinyu/nano-notebook/internal/models"
	"github.com/huangxinxinyu/nano-notebook/internal/retrieval"
)

type sequenceDecisionModel struct {
	requests  []models.ModelRequest
	decisions []models.ModelDecision
}

func (m *sequenceDecisionModel) Decide(_ context.Context, request models.ModelRequest) (models.ModelOutcome, error) {
	m.requests = append(m.requests, request)
	if len(m.decisions) == 0 {
		return models.ModelOutcome{}, errors.New("unexpected model decision")
	}
	decision := m.decisions[0]
	m.decisions = m.decisions[1:]
	resultKind := models.ModelResultFinalDraft
	if decision.Proposal != nil {
		resultKind = models.ModelResultActionProposal
	}
	return models.ModelOutcome{ModelDecision: decision, Metadata: models.ModelCallMetadata{RequestedModel: request.Model, ResultKind: resultKind}}, nil
}

type emptyEvidenceBackend struct {
	result retrieval.SearchResult
}

func (b emptyEvidenceBackend) SearchEvidence(context.Context, agent.Attempt, string, string) (retrieval.SearchResult, error) {
	return b.result, nil
}

func TestGroundingPersistsAllowlistedInlineSourceReferencesWithoutVerifier(t *testing.T) {
	api, attempt, notebookID, _ := groundingFixture(t, "source-marker-grounding@example.com", "src_marker", "evr_marker")
	service := agent.NewGroundingService(api.db.Pool())
	draft := models.FinalDraft{Text: "The launch is 20 July [source:src_marker]. Unknown [source:src_forged]."}
	prepared, err := service.Prepare(
		context.Background(), attempt,
		checkpointedEvidencePrefix("src_marker", "evr_marker", "unit_ground", 0, 27, false, false),
		draft,
	)
	if err != nil {
		t.Fatal(err)
	}
	if prepared.Text != "The launch is 20 July [source:src_marker]. Unknown ." {
		t.Fatalf("prepared=%+v", prepared)
	}
	var outcome string
	var performed bool
	if err := api.db.Pool().QueryRow(context.Background(), `
		select outcome,research_performed from agent_run_grounding_plans where run_id=$1
	`, attempt.RunID).Scan(&outcome, &performed); err != nil {
		t.Fatal(err)
	}
	var sourceID string
	if err := api.db.Pool().QueryRow(context.Background(), `
		select source_id from agent_draft_source_references where run_id=$1 and reference_ordinal=0 and notebook_id=$2
	`, attempt.RunID, notebookID).Scan(&sourceID); err != nil {
		t.Fatal(err)
	}
	if outcome != "source_cited" || !performed || sourceID != "src_marker" {
		t.Fatalf("plan=%s performed=%t source=%s", outcome, performed, sourceID)
	}
}

func TestGroundingRequiresSearchAttemptForSelectedSources(t *testing.T) {
	api, attempt, _, _ := groundingFixture(t, "mandatory-search-grounding@example.com", "src_mandatory", "evr_mandatory")
	service := agent.NewGroundingService(api.db.Pool())
	_, err := service.Prepare(context.Background(), attempt, agent.CheckpointPrefix{}, models.FinalDraft{Text: "Hello."})
	if !errors.Is(err, agent.ErrGroundingIncomplete) {
		t.Fatalf("error=%v", err)
	}
}

func TestGroundingAcceptsUnmarkedPlainTextAfterEvidence(t *testing.T) {
	api, attempt, _, _ := groundingFixture(t, "unmarked-after-evidence@example.com", "src_unmarked", "evr_unmarked")
	service := agent.NewGroundingService(api.db.Pool())
	prepared, err := service.Prepare(
		context.Background(), attempt,
		checkpointedEvidencePrefix("src_unmarked", "evr_unmarked", "unit_ground", 0, 27, false, true),
		models.FinalDraft{Text: "Ordinary conversational answer."},
	)
	if err != nil || prepared.Text != "Ordinary conversational answer." {
		t.Fatalf("prepared=%+v err=%v", prepared, err)
	}
	var outcome string
	var performed, degraded bool
	if err := api.db.Pool().QueryRow(context.Background(), `
		select outcome,research_performed,retrieval_degraded from agent_run_grounding_plans where run_id=$1
	`, attempt.RunID).Scan(&outcome, &performed, &degraded); err != nil {
		t.Fatal(err)
	}
	if outcome != "source_free" || !performed || !degraded {
		t.Fatalf("plan=%s performed=%t degraded=%t", outcome, performed, degraded)
	}
}

func TestGroundingAcceptsSelectedSourceFinalWhenNoCiteableEvidence(t *testing.T) {
	tests := []struct {
		name     string
		prefix   agent.CheckpointPrefix
		complete bool
		degraded bool
	}{
		{name: "complete empty search", prefix: checkpointedEvidencePrefix("", "", "", 0, 0, true, false), complete: true},
		{name: "degraded empty search", prefix: checkpointedEvidencePrefix("", "", "", 0, 0, false, true), degraded: true},
	}
	for index, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			api, attempt, _, _ := groundingFixture(t, fmt.Sprintf("source-free-%d@example.com", index), fmt.Sprintf("src_source_free_%d", index), fmt.Sprintf("evr_source_free_%d", index))
			service := agent.NewGroundingService(api.db.Pool())
			draft := models.FinalDraft{Text: "Ordinary conversational answer."}
			prepared, err := service.Prepare(context.Background(), attempt, tt.prefix, draft)
			if err != nil || prepared.Text != draft.Text {
				t.Fatalf("prepared=%+v err=%v", prepared, err)
			}
			var outcome string
			var complete, degraded bool
			if err := api.db.Pool().QueryRow(context.Background(), `
				select outcome,research_complete,retrieval_degraded from agent_run_grounding_plans where run_id=$1
			`, attempt.RunID).Scan(&outcome, &complete, &degraded); err != nil {
				t.Fatal(err)
			}
			if outcome != "source_free" || complete != tt.complete || degraded != tt.degraded {
				t.Fatalf("plan=%s complete=%t degraded=%t", outcome, complete, degraded)
			}
		})
	}
}

func TestPublicationAcceptsSourceFreeFinalWithSelectedSource(t *testing.T) {
	api, attempt, _, _ := groundingFixture(t, "source-free-publication@example.com", "src_source_free_publish", "evr_source_free_publish")
	service := agent.NewGroundingService(api.db.Pool())
	draft, err := service.Prepare(context.Background(), attempt, checkpointedEvidencePrefix("", "", "", 0, 0, true, false), models.FinalDraft{Text: "Ordinary conversational answer."})
	if err != nil {
		t.Fatal(err)
	}
	runtime := agent.NewPostgresRuntime(api.db.Pool(), "", nil)
	appendSearchCheckpoints(t, runtime, attempt, checkpointedEvidencePrefix("", "", "", 0, 0, true, false))
	checkpoint, err := agent.NewFinalDraftCheckpoint(2, draft)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.AppendCheckpoint(context.Background(), attempt, checkpoint); err != nil {
		t.Fatal(err)
	}
	if err := runtime.PublishFinal(context.Background(), attempt, draft); err != nil {
		t.Fatal(err)
	}
	var content string
	if err := api.db.Pool().QueryRow(context.Background(), `
		select m.content from agent_runs r join chat_messages m on m.id=r.output_message_id where r.id=$1
	`, attempt.RunID).Scan(&content); err != nil {
		t.Fatal(err)
	}
	var citations int
	if err := api.db.Pool().QueryRow(context.Background(), `select count(*) from chat_citations where run_id=$1`, attempt.RunID).Scan(&citations); err != nil {
		t.Fatal(err)
	}
	if content != draft.Text || citations != 0 {
		t.Fatalf("content=%q citations=%d", content, citations)
	}
}

func TestControllerPublishesPlainTextAfterSearchReturnsNoEvidence(t *testing.T) {
	api, attempt, _, _ := groundingFixture(t, "empty-search-controller@example.com", "src_empty_search_controller", "evr_empty_search_controller")
	model := &sequenceDecisionModel{decisions: []models.ModelDecision{
		{Proposal: &models.ActionProposalBatch{Actions: []models.ActionProposal{{
			Name: "search_evidence", Input: json.RawMessage(`{"query":"capabilities","purpose":"check selected Sources"}`),
		}}}},
		{Final: &models.FinalDraft{Text: "Ordinary conversational answer after an empty search."}},
	}}
	grounder := agent.NewGroundingService(api.db.Pool())
	runtime := agent.NewPostgresRuntime(api.db.Pool(), agent.BareSystemPrompt, nil, agent.WithGroundingService(grounder))
	registry, err := agent.NewActionRegistry(agent.NewSearchEvidenceAction(emptyEvidenceBackend{result: retrieval.SearchResult{CompleteEmpty: true}}))
	if err != nil {
		t.Fatal(err)
	}
	if err := agent.NewController(runtime, model, registry).Execute(context.Background(), attempt); err != nil {
		t.Fatal(err)
	}
	if len(model.requests) != 2 || model.requests[0].RequiredActionName != "search_evidence" || model.requests[1].RequiredActionName != "" {
		t.Fatalf("requests=%+v", model.requests)
	}
	var content, outcome string
	var complete, degraded bool
	if err := api.db.Pool().QueryRow(context.Background(), `
		select m.content,p.outcome,p.research_complete,p.retrieval_degraded
		from agent_runs r join chat_messages m on m.id=r.output_message_id
		join agent_run_grounding_plans p on p.run_id=r.id where r.id=$1
	`, attempt.RunID).Scan(&content, &outcome, &complete, &degraded); err != nil {
		t.Fatal(err)
	}
	if content != "Ordinary conversational answer after an empty search." || outcome != "source_free" || !complete || degraded {
		t.Fatalf("content=%q plan=%s complete=%t degraded=%t", content, outcome, complete, degraded)
	}
}

func TestGroundedControllerBuildsQueryContextFromBoundedCompletedPairs(t *testing.T) {
	api, attempt, _, _ := groundingFixture(t, "query-context-grounding@example.com", "src_query_context", "evr_query_context")
	ctx := context.Background()
	var chatID, userID string
	var currentCreatedAt string
	if err := api.db.Pool().QueryRow(ctx, `
		select r.chat_id,r.user_id,to_char(m.created_at,'YYYY-MM-DD HH24:MI:SS.USOF')
		from agent_runs r join chat_messages m on m.id=r.input_message_id where r.id=$1
	`, attempt.RunID).Scan(&chatID, &userID, &currentCreatedAt); err != nil {
		t.Fatal(err)
	}
	for index := 1; index <= 4; index++ {
		userMessageID := fmt.Sprintf("msg_query_context_user_%d", index)
		assistantMessageID := fmt.Sprintf("msg_query_context_assistant_%d", index)
		answer := fmt.Sprintf("completed-answer-%d", index)
		if index == 4 {
			answer += " " + strings.Repeat("OLD_DEGREE_TOPIC ", 1000)
		}
		if _, err := api.db.Pool().Exec(ctx, `
			insert into chat_messages(id,chat_id,role,content,created_at) values
				($1,$3,'user',$4,$7::timestamptz-(($6::int+1)*interval '2 minutes')),
				($2,$3,'assistant',$5,$7::timestamptz-(($6::int+1)*interval '2 minutes')+interval '1 second')
		`, userMessageID, assistantMessageID, chatID, fmt.Sprintf("completed-question-%d", index), answer, 4-index, currentCreatedAt); err != nil {
			t.Fatal(err)
		}
		if _, err := api.db.Pool().Exec(ctx, `
			insert into agent_runs(id,user_id,chat_id,input_message_id,output_message_id,status,model,prompt_version,agent_config_id,created_at,started_at,finished_at)
			values($1,$2,$3,$4,$5,'completed','aliyun/qwen-flash',$6,'query-context-fixture',$7::timestamptz-(($8::int+1)*interval '2 minutes'),$7::timestamptz-(($8::int+1)*interval '2 minutes'),$7::timestamptz-(($8::int+1)*interval '2 minutes')+interval '1 second')
		`, fmt.Sprintf("run_query_context_%d", index), userID, chatID, userMessageID, assistantMessageID,
			agent.GroundedPromptVersion, currentCreatedAt, 4-index); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := api.db.Pool().Exec(ctx, `
		insert into chat_messages(id,chat_id,role,content,created_at)
		values('msg_query_context_unpaired',$1,'user','UNPAIRED_OLD_TOPIC',$2::timestamptz-interval '10 seconds')
	`, chatID, currentCreatedAt); err != nil {
		t.Fatal(err)
	}

	model := &sequenceDecisionModel{decisions: []models.ModelDecision{
		{Proposal: &models.ActionProposalBatch{Actions: []models.ActionProposal{{
			Name: "search_evidence", Input: json.RawMessage(`{"query":"When is launch?","purpose":"answer the current request"}`),
		}}}},
		{Final: &models.FinalDraft{Text: "Hello after retrieval."}},
	}}
	grounder := agent.NewGroundingService(api.db.Pool())
	runtime := agent.NewPostgresRuntime(api.db.Pool(), agent.BareSystemPrompt, nil, agent.WithGroundingService(grounder))
	registry, err := agent.NewActionRegistry(agent.NewSearchEvidenceAction(emptyEvidenceBackend{result: retrieval.SearchResult{CompleteEmpty: true}}))
	if err != nil {
		t.Fatal(err)
	}
	if err := agent.NewController(runtime, model, registry).Execute(ctx, attempt); err != nil {
		t.Fatal(err)
	}
	if len(model.requests) != 2 {
		t.Fatalf("model request count = %d, want contextualizer + composer", len(model.requests))
	}
	queryRequest := model.requests[0]
	if queryRequest.RequiredActionName != "search_evidence" || len(queryRequest.ActionDefinitions) != 1 ||
		queryRequest.ActionDefinitions[0].Name != "search_evidence" || len(queryRequest.Messages) != 2 {
		t.Fatalf("query contextualizer request = %+v", queryRequest)
	}
	queryContext := queryRequest.Messages[0].Content + "\n" + queryRequest.Messages[1].Content
	for _, required := range []string{
		"current Message is authoritative",
		"preserve its key terms",
		"Do not translate ambiguous terms",
		"copy it rather than choose an interpretation",
		"When is launch?",
		"completed-question-2",
		"completed-question-3",
		"completed-question-4",
	} {
		if !strings.Contains(queryContext, required) {
			t.Fatalf("query context is missing %q: %s", required, queryContext)
		}
	}
	for _, forbidden := range []string{"completed-question-1", "UNPAIRED_OLD_TOPIC"} {
		if strings.Contains(queryContext, forbidden) {
			t.Fatalf("query context contains %q: %s", forbidden, queryContext)
		}
	}
	if strings.Count(queryContext, "OLD_DEGREE_TOPIC") >= 1000 {
		t.Fatalf("long prior answer was not bounded")
	}
	var composerContext strings.Builder
	for _, message := range model.requests[1].Messages {
		composerContext.WriteString(message.Content)
		composerContext.WriteByte('\n')
	}
	for _, required := range []string{"When is launch?", "complete_empty"} {
		if !strings.Contains(composerContext.String(), required) {
			t.Fatalf("composer context is missing %q: %s", required, composerContext.String())
		}
	}
	for _, forbidden := range []string{"completed-question-1", "completed-question-2", "completed-question-3", "completed-question-4", "UNPAIRED_OLD_TOPIC", "OLD_DEGREE_TOPIC"} {
		if strings.Contains(composerContext.String(), forbidden) {
			t.Fatalf("composer context contains %q: %s", forbidden, composerContext.String())
		}
	}
}

func TestPublicationAtomicallyCopiesSourceReferencesAndRejectsSourceDeletionRace(t *testing.T) {
	for _, removeBeforePublish := range []bool{false, true} {
		t.Run(map[bool]string{false: "publishes", true: "deletion fenced"}[removeBeforePublish], func(t *testing.T) {
			api, attempt, notebookID, sessionCookie := groundingFixture(t, "publication-grounding-"+map[bool]string{false: "ok", true: "deleted"}[removeBeforePublish]+"@example.com", "src_publish", "evr_publish")
			service := agent.NewGroundingService(api.db.Pool())
			prefix := checkpointedEvidencePrefix("src_publish", "evr_publish", "unit_ground", 0, 27, false, false)
			draft := models.FinalDraft{Text: "The launch is 20 July [source:src_publish]."}
			prepared, err := service.Prepare(context.Background(), attempt, prefix, draft)
			if err != nil {
				t.Fatal(err)
			}
			runtime := agent.NewPostgresRuntime(api.db.Pool(), "", nil)
			appendSearchCheckpoints(t, runtime, attempt, prefix)
			checkpoint, err := agent.NewFinalDraftCheckpoint(2, prepared)
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
				var referenceKind string
				if err := api.db.Pool().QueryRow(context.Background(), `select citation_id,reference_kind from chat_citations where run_id=$1`, attempt.RunID).Scan(&citationID, &referenceKind); err != nil {
					t.Fatal(err)
				}
				if referenceKind != "source" {
					t.Fatalf("reference kind=%q", referenceKind)
				}
				var chatID string
				if err := api.db.Pool().QueryRow(context.Background(), `select chat_id from agent_runs where id=$1`, attempt.RunID).Scan(&chatID); err != nil {
					t.Fatal(err)
				}
				snapshot := api.getWithCookie(t, "/api/v1/chats/"+chatID, sessionCookie)
				if snapshot.Code != http.StatusOK || !strings.Contains(snapshot.Body.String(), `"source_title":"src_publish"`) {
					t.Fatalf("Chat snapshot=%d %s", snapshot.Code, snapshot.Body.String())
				}
				resolved := api.getWithCookie(t, "/api/v1/citations/"+citationID, sessionCookie)
				if resolved.Code != http.StatusOK || !strings.Contains(resolved.Body.String(), `"reference_kind":"source"`) ||
					!strings.Contains(resolved.Body.String(), `"source_id":"src_publish"`) || strings.Contains(resolved.Body.String(), "The launch date is 20 July.") ||
					strings.Contains(resolved.Body.String(), "object_key") || strings.Contains(resolved.Body.String(), "sha256") {
					t.Fatalf("Citation resolution=%d %s", resolved.Code, resolved.Body.String())
				}
				var userID string
				if err := api.db.Pool().QueryRow(context.Background(), `select user_id from agent_runs where id=$1`, attempt.RunID).Scan(&userID); err != nil {
					t.Fatal(err)
				}
				replacementEmail := "citation-replacement-owner@example.com"
				api.register(t, replacementEmail)
				replacementOwnerID := sourceTestUserID(t, api, replacementEmail)
				tx, err := api.db.Pool().Begin(context.Background())
				if err != nil {
					t.Fatal(err)
				}
				if _, err := tx.Exec(context.Background(), `update notebook_memberships set role='viewer' where notebook_id=$1 and user_id=$2`, notebookID, userID); err != nil {
					_ = tx.Rollback(context.Background())
					t.Fatal(err)
				}
				if _, err := tx.Exec(context.Background(), `insert into notebook_memberships(notebook_id,user_id,role) values($1,$2,'owner')`, notebookID, replacementOwnerID); err != nil {
					_ = tx.Rollback(context.Background())
					t.Fatal(err)
				}
				if err := tx.Commit(context.Background()); err != nil {
					t.Fatal(err)
				}
				if _, err := api.db.Pool().Exec(context.Background(), `delete from notebook_memberships where notebook_id=$1 and user_id=$2`, notebookID, userID); err != nil {
					t.Fatal(err)
				}
				unauthorized := api.getWithCookie(t, "/api/v1/citations/"+citationID, sessionCookie)
				if unauthorized.Code != http.StatusNotFound {
					t.Fatalf("unauthorized Citation resolution=%d %s", unauthorized.Code, unauthorized.Body.String())
				}
				if _, err := api.db.Pool().Exec(context.Background(), `insert into notebook_memberships(notebook_id,user_id,role) values($1,$2,'viewer')`, notebookID, userID); err != nil {
					t.Fatal(err)
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

func appendSearchCheckpoints(t *testing.T, runtime *agent.PostgresRuntime, attempt agent.Attempt, prefix agent.CheckpointPrefix) {
	t.Helper()
	proposal, err := agent.NewProposalCheckpoint(1, models.ActionProposalBatch{Actions: []models.ActionProposal{{
		Name: "search_evidence", Input: json.RawMessage(`{"query":"launch date","purpose":"answer the question"}`),
	}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.AppendCheckpoint(context.Background(), attempt, proposal); err != nil {
		t.Fatal(err)
	}
	result := prefix.Proposals[0].Actions[0].Result
	checkpoint, err := agent.NewActionResultCheckpoint(1, 0, "decision:1/action:0", *result)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.AppendCheckpoint(context.Background(), attempt, checkpoint); err != nil {
		t.Fatal(err)
	}
}
