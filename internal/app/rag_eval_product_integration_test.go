package app_test

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/huangxinxinyu/nano-notebook/internal/agent"
	"github.com/huangxinxinyu/nano-notebook/internal/models"
	"github.com/huangxinxinyu/nano-notebook/internal/qdrantstore"
	"github.com/huangxinxinyu/nano-notebook/internal/rageval"
	"github.com/huangxinxinyu/nano-notebook/internal/retrieval"
	"github.com/huangxinxinyu/nano-notebook/internal/sourceprojection"
)

func TestProductRunExecutorDerivesObservationFromDurableProductFacts(t *testing.T) {
	api := newTestAPI(t)
	ctx := context.Background()
	owner := api.register(t, "rag-product-observer@example.com")
	notebookID := createSourceTestNotebook(t, api, owner, "rag-product-observer")
	ownerID := sourceTestUserID(t, api, "rag-product-observer@example.com")
	config := retrieval.IndexConfig{
		Chunk: retrieval.ChunkConfig{MaxRunes: 64, OverlapRunes: 8, PreserveHeadingContext: true}, AnalyzerID: "nano-mixed-v1",
		BM25K1: 1.2, BM25B: .75, BM25AverageDocumentLength: 24, EmbeddingModel: "embed-eval", EmbeddingDimensions: 3,
		DenseCandidates: 8, SparseCandidates: 8, RRFK: 60, RerankerID: "rerank-eval", RerankCandidates: 8, DegradationPolicyID: "strict-v1",
	}
	version, err := retrieval.NewVersionStore(api.db.Pool()).CreateCandidate(ctx, "riv_product_observer", config)
	if err != nil {
		t.Fatal(err)
	}
	fixtureSHA := strings.Repeat("a", 64)
	statements := []struct {
		query string
		args  []any
	}{
		{`insert into chat_chats(id,notebook_id,creator_user_id,title) values('chat_product_observer',$1,$2,'Eval')`, []any{notebookID, ownerID}},
		{`insert into chat_messages(id,chat_id,role,content) values('msg_product_question','chat_product_observer','user','What is the launch date?')`, nil},
		{`insert into chat_messages(id,chat_id,role,content) values('msg_product_answer','chat_product_observer','assistant','The launch date is 20 July.')`, nil},
		{`insert into source_sources(id,notebook_id,input_kind,format,title,media_type,byte_size,content_sha256,original_object_key,state) values('src_product_fixture',$1,'file','txt','Fixture','text/plain',10,$2,'sources/src_product_fixture/original','ready')`, []any{notebookID, fixtureSHA}},
		{`insert into source_evidence_revisions(id,source_id,notebook_id,revision_no,extraction_config_id,artifact_schema_version,artifact_object_key,artifact_sha256,status,activated_at) values('evr_product_fixture','src_product_fixture',$1,1,'extract-eval-v1','nano.normalized-source.v1','sources/src_product_fixture/evidence/normalized.json',$2,'active',now())`, []any{notebookID, strings.Repeat("b", 64)}},
		{`insert into source_evidence_coverage(revision_id,status,total_runes) values('evr_product_fixture','complete',27)`, nil},
		{`insert into source_evidence_units(id,revision_id,source_id,notebook_id,ordinal,kind,text_content,start_rune,end_rune) values('unit_product_launch','evr_product_fixture','src_product_fixture',$1,0,'paragraph','The launch date is 20 July.',0,27)`, []any{notebookID}},
		{`insert into retrieval_source_index_builds(revision_id,index_version_id,source_id,notebook_id,expected_points,projection_sha256,status,verified_at) values('evr_product_fixture',$1,'src_product_fixture',$2,1,$3,'verified',now())`, []any{version.ID, notebookID, strings.Repeat("c", 64)}},
		{`insert into agent_runs(id,user_id,chat_id,input_message_id,output_message_id,status,model,prompt_version,agent_config_id,started_at,finished_at) values('run_product_observer',$1,'chat_product_observer','msg_product_question','msg_product_answer','completed','composer-eval',$2,'agent-eval-v1',now()-interval '120 milliseconds',now())`, []any{ownerID, agent.GroundedPromptVersion}},
		{`insert into agent_run_evidence_set(run_id,ordinal,notebook_id,source_id,evidence_revision_id,index_version_id) values('run_product_observer',0,$1,'src_product_fixture','evr_product_fixture',$2)`, []any{notebookID, version.ID}},
		{`insert into agent_run_grounding_plans(run_id,draft_sha256,outcome,research_complete,retrieval_degraded,verifier_model,verifier_prompt_version) values('run_product_observer',$1,'supported',true,false,'verifier-eval','claim-support-v1')`, []any{strings.Repeat("d", 64)}},
		{`insert into agent_claim_support_records(run_id,claim_ordinal,claim_text,verdict) values('run_product_observer',0,'The launch date is 20 July.','supported')`, nil},
		{`insert into chat_citations(message_id,citation_id,run_id,claim_ordinal,citation_ordinal,claim_text,notebook_id,source_id,evidence_revision_id,unit_id,start_rune,end_rune) values('msg_product_answer','cite_product','run_product_observer',0,0,'The launch date is 20 July.',$1,'src_product_fixture','evr_product_fixture','unit_product_launch',0,27)`, []any{notebookID}},
		{`insert into agent_run_checkpoints(run_id,sequence_no,identity_key,kind,decision_no,payload,payload_sha256) values('run_product_observer',1,'decision:1','action_proposal',1,$1,$2)`, []any{`{"actions":[{"action_id":"decision:1/action:0","index":0,"name":"search_evidence","input":{"query":"launch date","purpose":"answer"}}]}`, strings.Repeat("e", 64)}},
		{`insert into agent_run_checkpoints(run_id,sequence_no,identity_key,kind,decision_no,action_index,action_id,payload,payload_sha256) values('run_product_observer',2,'decision:1/action:0','action_result',1,0,'decision:1/action:0',$1,$2)`, []any{`{"action_id":"decision:1/action:0","status":"succeeded","output":{"complete_empty":false,"degraded":false,"degradations":[],"evidence":[{"source_id":"src_product_fixture","evidence_revision_id":"evr_product_fixture","source_title":"Fixture","preview":"The launch date is 20 July.","evidence_ranges":[{"unit_id":"unit_product_launch","start_rune":0,"end_rune":27}]}]}}`, strings.Repeat("f", 64)}},
	}
	for _, statement := range statements {
		if _, err := api.db.Pool().Exec(ctx, statement.query, statement.args...); err != nil {
			t.Fatal(err)
		}
	}
	executor, err := rageval.NewProductRunExecutor(api.db.Pool(), rageval.ProductRunManifest{
		SchemaVersion: 1, IndexVersionID: version.ID,
		Cases: []rageval.ProductRunCase{{
			CaseID: "product-observer", RunID: "run_product_observer",
			FixtureSources: map[string]string{"txt-eval-v1": "src_product_fixture"},
			EvidenceUnits:  map[string][]string{"txt-launch-date": {"unit_product_launch"}},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	evalCase := rageval.Case{
		ID: "product-observer", Families: []rageval.SourceFamily{rageval.FamilyTXT}, Language: rageval.LanguageEnglish,
		Question: "What is the launch date?", ExpectedEvidenceSets: [][]string{{"txt-launch-date"}}, RequiredFacts: []string{"20 July"},
		Fixtures: []rageval.Fixture{{ID: "txt-eval-v1", Family: rageval.FamilyTXT, URI: "fixture://txt-eval-v1", SHA256: fixtureSHA}},
	}
	observation, err := executor.ExecuteCase(ctx, evalCase, rageval.PinnedConfig{
		ExtractionConfigID: "extract-eval-v1", EvidenceSchemaVersion: 1, Index: config,
		ComposerModel: "composer-eval", VerifierModel: "verifier-eval", VerifierPromptVersion: "claim-support-v1", PromptVersion: agent.GroundedPromptVersion, AgentConfigID: "agent-eval-v1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !observation.CoveragePassed || len(observation.InvariantFailures) != 0 || len(observation.RetrievedEvidenceIDs) != 1 || observation.RetrievedEvidenceIDs[0] != "txt-launch-date" || len(observation.CitationEvidenceIDs) != 1 || observation.CitationEvidenceIDs[0] != "txt-launch-date" || observation.MaterialClaimCount != 1 || observation.CitedClaimCount != 1 || len(observation.RequiredFactsFound) != 1 || observation.LatencyMilliseconds < 1 {
		t.Fatalf("observation=%+v", observation)
	}
}

func TestLiveProductExecutorRunsCandidateThroughProductionAgentAndCitations(t *testing.T) {
	qdrantURL := os.Getenv("NANO_TEST_QDRANT_URL")
	if qdrantURL == "" {
		t.Skip("NANO_TEST_QDRANT_URL is required")
	}
	api := newTestAPI(t)
	ctx := context.Background()
	owner := api.register(t, "rag-live-product@example.com")
	notebookID := createSourceTestNotebook(t, api, owner, "rag-live-product")
	ownerID := sourceTestUserID(t, api, "rag-live-product@example.com")
	config := retrieval.IndexConfig{
		Chunk: retrieval.ChunkConfig{MaxRunes: 64, OverlapRunes: 8, PreserveHeadingContext: true}, AnalyzerID: "nano-mixed-v1",
		BM25K1: 1.2, BM25B: .75, BM25AverageDocumentLength: 24, EmbeddingModel: "embed-live", EmbeddingDimensions: 3,
		DenseCandidates: 8, SparseCandidates: 8, RRFK: 60, RerankerID: "rerank-live", RerankCandidates: 8, DegradationPolicyID: "strict-v1",
	}
	version, err := retrieval.NewVersionStore(api.db.Pool()).CreateCandidate(ctx, "riv_live_product", config)
	if err != nil {
		t.Fatal(err)
	}
	fixtureSHA := strings.Repeat("1", 64)
	for _, statement := range []struct {
		query string
		args  []any
	}{
		{`insert into source_sources(id,notebook_id,input_kind,format,title,media_type,byte_size,content_sha256,original_object_key,state) values('src_live_fixture',$1,'file','txt','Live fixture','text/plain',10,$2,'sources/src_live_fixture/original','ready')`, []any{notebookID, fixtureSHA}},
		{`insert into source_evidence_revisions(id,source_id,notebook_id,revision_no,extraction_config_id,artifact_schema_version,artifact_object_key,artifact_sha256,status,activated_at) values('evr_live_fixture','src_live_fixture',$1,1,'extract-live-v1','nano.normalized-source.v1','sources/src_live_fixture/evidence/normalized.json',$2,'active',now())`, []any{notebookID, strings.Repeat("2", 64)}},
		{`insert into source_evidence_coverage(revision_id,status,total_runes) values('evr_live_fixture','complete',27)`, nil},
		{`insert into source_evidence_units(id,revision_id,source_id,notebook_id,ordinal,kind,text_content,start_rune,end_rune) values('unit_live_launch','evr_live_fixture','src_live_fixture',$1,0,'paragraph','The launch date is 20 July.',0,27)`, []any{notebookID}},
	} {
		if _, err := api.db.Pool().Exec(ctx, statement.query, statement.args...); err != nil {
			t.Fatal(err)
		}
	}
	qdrant, err := qdrantstore.New(qdrantstore.Config{BaseURL: qdrantURL, Collection: "nano_test_" + uuid.NewString(), DenseDimensions: 3, RequestTimeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = qdrant.DeleteCollection(context.Background()) })
	if err := qdrant.EnsureCollection(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := sourceprojection.NewReindexer(api.db.Pool(), qdrant, liveEvalModel{}).ReindexVersion(ctx, version.ID); err != nil {
		t.Fatal(err)
	}
	model := &liveEvalModel{}
	executor, err := rageval.NewLiveProductExecutor(api.db.Pool(), qdrant, model, rageval.ProductSourceManifest{
		SchemaVersion: 1, IndexVersionID: version.ID, UserID: ownerID, NotebookID: notebookID,
		Cases: []rageval.ProductSourceCase{{CaseID: "live-product", FixtureSources: map[string]string{"txt-live-v1": "src_live_fixture"}, EvidenceUnits: map[string][]string{"txt-launch-date": {"unit_live_launch"}}}},
	}, "verifier-live", "claim-support-v1")
	if err != nil {
		t.Fatal(err)
	}
	evalCase := rageval.Case{
		ID: "live-product", Families: []rageval.SourceFamily{rageval.FamilyTXT}, Language: rageval.LanguageEnglish,
		Question: "What is the launch date?", ExpectedEvidenceSets: [][]string{{"txt-launch-date"}}, RequiredFacts: []string{"20 July"},
		Fixtures: []rageval.Fixture{{ID: "txt-live-v1", Family: rageval.FamilyTXT, URI: "fixture://txt-live-v1", SHA256: fixtureSHA}},
	}
	observation, err := executor.ExecuteCase(ctx, evalCase, rageval.PinnedConfig{
		ExtractionConfigID: "extract-live-v1", EvidenceSchemaVersion: 1, Index: config,
		ComposerModel: "composer-live", VerifierModel: "verifier-live", VerifierPromptVersion: "claim-support-v1",
		PromptVersion: agent.GroundedPromptVersion, AgentConfigID: "agent-live-v1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(observation.InvariantFailures) != 0 || !observation.CoveragePassed || observation.MaterialClaimCount != 1 || observation.CitedClaimCount != 1 || len(observation.RetrievedEvidenceIDs) != 1 || len(observation.CitationEvidenceIDs) != 1 {
		t.Fatalf("observation=%+v", observation)
	}
	stored, err := retrieval.NewVersionStore(api.db.Pool()).ByID(ctx, version.ID)
	if err != nil || stored.Status != retrieval.VersionCandidate {
		t.Fatalf("candidate=%+v err=%v", stored, err)
	}
}

type liveEvalModel struct{}

func (liveEvalModel) Embed(_ context.Context, request models.EmbeddingRequest) (models.EmbeddingOutcome, error) {
	vectors := make([][]float32, len(request.Inputs))
	for index := range vectors {
		vectors[index] = []float32{1, 0, 0}
	}
	return models.EmbeddingOutcome{Vectors: vectors, Metadata: liveEvalCapabilityMetadata()}, nil
}

func (liveEvalModel) Rerank(_ context.Context, request models.RerankRequest) (models.RerankOutcome, error) {
	ids := make([]string, 0, len(request.Candidates))
	for _, candidate := range request.Candidates {
		ids = append(ids, candidate.ID)
	}
	return models.RerankOutcome{CandidateIDs: ids, Metadata: liveEvalCapabilityMetadata()}, nil
}

func (liveEvalModel) Decide(_ context.Context, request models.ModelRequest) (models.ModelOutcome, error) {
	if len(request.Messages) == 2 {
		return models.ModelOutcome{ModelDecision: models.ModelDecision{Proposal: &models.ActionProposalBatch{Actions: []models.ActionProposal{{
			Name: "search_evidence", Input: json.RawMessage(`{"query":"launch date","purpose":"answer the question"}`),
		}}}}, Metadata: liveEvalDecisionMetadata(request.Model, models.ModelResultActionProposal)}, nil
	}
	claim := "The launch date is 20 July."
	return models.ModelOutcome{ModelDecision: models.ModelDecision{Final: &models.FinalDraft{Text: claim, Claims: []models.DraftClaim{{
		Text: claim, Citations: []models.EvidenceAddress{{SourceID: "src_live_fixture", EvidenceRevisionID: "evr_live_fixture", UnitID: "unit_live_launch", StartRune: 0, EndRune: 27}},
	}}}}, Metadata: liveEvalDecisionMetadata(request.Model, models.ModelResultFinalDraft)}, nil
}

func (liveEvalModel) VerifyClaimSupport(_ context.Context, request models.ClaimSupportRequest) (models.ClaimSupportOutcome, error) {
	verdicts := make([]models.ClaimSupportVerdict, len(request.Claims))
	for index := range verdicts {
		verdicts[index] = models.ClaimSupportVerdict{Ordinal: index, Supported: true}
	}
	return models.ClaimSupportOutcome{Verdicts: verdicts, Metadata: liveEvalCapabilityMetadata()}, nil
}

func liveEvalCapabilityMetadata() models.CapabilityMetadata {
	input, total, cost := int64(1), int64(2), 0.0
	return models.CapabilityMetadata{InputTokens: &input, TotalTokens: &total, Cost: models.ModelCost{Known: true, Amount: &cost, Currency: "USD", Source: "test"}}
}

func liveEvalDecisionMetadata(model string, kind models.ModelResultKind) models.ModelCallMetadata {
	input, total, cost := int64(1), int64(2), 0.0
	return models.ModelCallMetadata{RequestedModel: model, ResultKind: kind, InputTokens: &input, TotalTokens: &total, Cost: models.ModelCost{Known: true, Amount: &cost, Currency: "USD", Source: "test"}}
}
