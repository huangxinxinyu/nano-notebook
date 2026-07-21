package app_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/huangxinxinyu/nano-notebook/internal/agent"
	"github.com/huangxinxinyu/nano-notebook/internal/retrieval"
)

func TestRetrievalIndexPromotionRequiresPassingOfflineEval(t *testing.T) {
	api := newTestAPI(t)
	store := retrieval.NewVersionStore(api.db.Pool())
	config := retrieval.IndexConfig{
		Chunk:      retrieval.ChunkConfig{MaxRunes: 800, OverlapRunes: 120, PreserveHeadingContext: true},
		AnalyzerID: "nano-mixed-v1", BM25K1: 1.2, BM25B: 0.75, BM25AverageDocumentLength: 240,
		EmbeddingModel: "text-embedding-v1", EmbeddingDimensions: 1024, EmbeddingProfileID: retrieval.EmbeddingProfileGeminiRetrievalV1,
		DenseCandidates: 40, SparseCandidates: 40, RRFK: 60,
		RerankerID: "qwen-rerank-v1", RerankCandidates: 20,
		DegradationPolicyID: "hybrid-required-v1",
	}
	first, err := store.CreateCandidate(context.Background(), "riv_first", config)
	if err != nil {
		t.Fatalf("CreateCandidate: %v", err)
	}
	if first.Status != retrieval.VersionCandidate || first.ConfigSHA256 == "" {
		t.Fatalf("first version = %+v", first)
	}
	if _, err := store.Promote(context.Background(), first.ID, "eval_missing"); !errors.Is(err, retrieval.ErrEvalGate) {
		t.Fatalf("promotion without Eval = %v, want Eval gate", err)
	}
	if err := store.RecordEval(context.Background(), retrieval.EvalRun{
		ID: "eval_failed", IndexVersionID: first.ID, FixtureSuiteSHA256: sixtyFour("a"),
		Status: retrieval.EvalFailed, MetricsJSON: []byte(`{"citation_precision":0.8}`),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Promote(context.Background(), first.ID, "eval_failed"); !errors.Is(err, retrieval.ErrEvalGate) {
		t.Fatalf("promotion with failed Eval = %v, want Eval gate", err)
	}
	if err := store.RecordEval(context.Background(), retrieval.EvalRun{
		ID: "eval_passed", IndexVersionID: first.ID, FixtureSuiteSHA256: sixtyFour("b"),
		Status: retrieval.EvalPassed, MetricsJSON: []byte(`{"citation_precision":1.0,"unsupported_answer_rate":0}`),
	}); err != nil {
		t.Fatal(err)
	}
	active, err := store.Promote(context.Background(), first.ID, "eval_passed")
	if err != nil || active.Status != retrieval.VersionActive || active.PromotedByEvalRunID != "eval_passed" {
		t.Fatalf("Promote = %+v, err=%v", active, err)
	}
	owner := api.register(t, "candidate-build-gate@example.com")
	notebookID := createSourceTestNotebook(t, api, owner, "candidate-build-gate")
	ownerID := sourceTestUserID(t, api, "candidate-build-gate@example.com")
	seedSourceProcessingJob(t, api, ownerID, notebookID, "src_candidate_gate", "srcjob_candidate_gate", "9")
	if _, err := api.db.Pool().Exec(context.Background(), `update source_sources set state='ready' where id='src_candidate_gate'`); err != nil {
		t.Fatal(err)
	}
	if _, err := api.db.Pool().Exec(context.Background(), `
		insert into source_evidence_revisions(
			id,source_id,notebook_id,revision_no,extraction_config_id,artifact_schema_version,artifact_object_key,artifact_sha256,status,activated_at
		) values('evr_candidate_gate','src_candidate_gate',$1,1,'extract-v1','nano.normalized-source.v1','candidate/gate.json',$2,'active',now())
	`, notebookID, strings.Repeat("d", 64)); err != nil {
		t.Fatal(err)
	}
	if _, err := api.db.Pool().Exec(context.Background(), `insert into source_evidence_coverage(revision_id,status,total_runes) values('evr_candidate_gate','complete',8)`); err != nil {
		t.Fatal(err)
	}
	if _, err := api.db.Pool().Exec(context.Background(), `
		insert into source_evidence_units(id,revision_id,source_id,notebook_id,ordinal,kind,text_content,start_rune,end_rune)
		values('unit_candidate_gate','evr_candidate_gate','src_candidate_gate',$1,0,'paragraph','Evidence',0,8)
	`, notebookID); err != nil {
		t.Fatal(err)
	}

	secondConfig := config
	secondConfig.Chunk.MaxRunes = 960
	second, err := store.CreateCandidate(context.Background(), "riv_second", secondConfig)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.RecordEval(context.Background(), retrieval.EvalRun{
		ID: "eval_second", IndexVersionID: second.ID, FixtureSuiteSHA256: sixtyFour("c"),
		Status: retrieval.EvalPassed, MetricsJSON: []byte(`{"citation_precision":1.0}`),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Promote(context.Background(), second.ID, "eval_second"); !errors.Is(err, retrieval.ErrEvalGate) {
		t.Fatalf("promotion without complete candidate builds=%v, want Eval gate", err)
	}
	if _, err := api.db.Pool().Exec(context.Background(), `
		insert into retrieval_source_index_builds(
			revision_id,index_version_id,source_id,notebook_id,expected_points,projection_sha256,status,verified_at
		) values('evr_candidate_gate',$1,'src_candidate_gate',$2,1,$3,'verified',now())
	`, second.ID, notebookID, strings.Repeat("e", 64)); err != nil {
		t.Fatal(err)
	}
	if _, err := api.db.Pool().Exec(context.Background(), `insert into chat_chats(id,notebook_id,creator_user_id,title) values('chat_candidate_eval',$1,$2,'Eval')`, notebookID, ownerID); err != nil {
		t.Fatal(err)
	}
	if _, err := api.db.Pool().Exec(context.Background(), `insert into chat_messages(id,chat_id,role,content) values('msg_candidate_eval','chat_candidate_eval','user','Evaluate candidate')`); err != nil {
		t.Fatal(err)
	}
	tx, err := api.db.Pool().Begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(context.Background())
	if _, err := tx.Exec(context.Background(), `set local role nano_worker`); err != nil {
		t.Fatal(err)
	}
	agentStore := agent.NewStore(tx)
	if err := agentStore.CreateQueued(context.Background(), "run_candidate_eval", ownerID, "chat_candidate_eval", "msg_candidate_eval", "composer", agent.GroundedPromptVersion, "UTC", agent.RunConfig{ID: "eval-agent-v1", ActionDecisionLimit: 2, FinalDecisionLimit: 1, ActionLimit: 2, ActionBatchLimit: 1, ActionResultByteLimit: 4096, ActionResultsByteLimit: 8192, Deadline: time.Minute}); err != nil {
		t.Fatal(err)
	}
	if err := agentStore.PinEvidenceSet(context.Background(), "run_candidate_eval", ownerID, []string{"src_candidate_gate"}); !errors.Is(err, agent.ErrEvidenceSetInvalid) {
		t.Fatalf("ordinary candidate pin=%v, want invalid", err)
	}
	if err := agentStore.PinEvidenceSetVersion(context.Background(), "run_candidate_eval", ownerID, second.ID, []string{"src_candidate_gate"}); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Promote(context.Background(), second.ID, "eval_second"); err != nil {
		t.Fatal(err)
	}
	current, err := store.Active(context.Background())
	if err != nil || current.ID != second.ID {
		t.Fatalf("Active = %+v, err=%v", current, err)
	}
	previous, err := store.ByID(context.Background(), first.ID)
	if err != nil || previous.Status != retrieval.VersionRetired {
		t.Fatalf("previous = %+v, err=%v", previous, err)
	}
}

func sixtyFour(character string) string {
	result := ""
	for len(result) < 64 {
		result += character
	}
	return result[:64]
}
