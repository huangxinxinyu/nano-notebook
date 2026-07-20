package app_test

import (
	"context"
	"errors"
	"testing"

	"github.com/huangxinxinyu/nano-notebook/internal/retrieval"
)

func TestRetrievalIndexPromotionRequiresPassingOfflineEval(t *testing.T) {
	api := newTestAPI(t)
	store := retrieval.NewVersionStore(api.db.Pool())
	config := retrieval.IndexConfig{
		Chunk:      retrieval.ChunkConfig{MaxRunes: 800, OverlapRunes: 120, PreserveHeadingContext: true},
		AnalyzerID: "nano-mixed-v1", BM25K1: 1.2, BM25B: 0.75, BM25AverageDocumentLength: 240,
		EmbeddingModel: "text-embedding-v1", EmbeddingDimensions: 1024,
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
