package retrieval_test

import (
	"reflect"
	"testing"

	"github.com/huangxinxinyu/nano-notebook/internal/retrieval"
)

func TestMixedAnalyzerAndClassicBM25ProduceDeterministicSparseRanking(t *testing.T) {
	analyzer := retrieval.NewMixedAnalyzer("nano-mixed-v1")
	tokens := analyzer.Analyze("苹果 Banana研究, BANANA!")
	if !containsToken(tokens, "banana") || !containsToken(tokens, "苹果") || !containsToken(tokens, "研究") {
		t.Fatalf("mixed tokens = %v", tokens)
	}
	documents := []retrieval.Document{
		{ID: "doc_1", Text: "苹果 banana research"},
		{ID: "doc_2", Text: "banana only"},
		{ID: "doc_3", Text: "苹果 苹果"},
	}
	model, err := retrieval.BuildBM25(analyzer, documents, 1.2, 0.75)
	if err != nil {
		t.Fatal(err)
	}
	ranked := model.Search("苹果", 3)
	if len(ranked) != 3 || ranked[0].ID != "doc_3" || ranked[1].ID != "doc_1" || ranked[0].Score <= ranked[1].Score {
		t.Fatalf("BM25 ranking = %+v", ranked)
	}
	reversed := []retrieval.Document{documents[2], documents[1], documents[0]}
	second, err := retrieval.BuildBM25(analyzer, reversed, 1.2, 0.75)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(model.Vocabulary(), second.Vocabulary()) || !reflect.DeepEqual(ranked, second.Search("苹果", 3)) {
		t.Fatalf("BM25 depends on input order: vocab=%v/%v rank=%v/%v", model.Vocabulary(), second.Vocabulary(), ranked, second.Search("苹果", 3))
	}
	query := model.QuerySparseVector("苹果")
	document := model.DocumentSparseVector("doc_3")
	if len(query.Indices) == 0 || len(query.Indices) != len(query.Values) || len(document.Indices) == 0 {
		t.Fatalf("sparse vectors query=%+v document=%+v", query, document)
	}
}

func TestRRFUsesRanksAndDeterministicIdentityTieBreaks(t *testing.T) {
	fused, err := retrieval.FuseRRF(map[string][]retrieval.Candidate{
		"dense": {{ID: "b", Score: 0.99}, {ID: "a", Score: 0.70}, {ID: "c", Score: 0.10}},
		"bm25":  {{ID: "a", Score: 20}, {ID: "b", Score: 3}, {ID: "c", Score: 1}},
	}, 60)
	if err != nil {
		t.Fatal(err)
	}
	if len(fused) != 3 || fused[0].ID != "a" || fused[1].ID != "b" || fused[0].Score != fused[1].Score {
		t.Fatalf("RRF = %+v", fused)
	}
	if _, err := retrieval.FuseRRF(map[string][]retrieval.Candidate{"dense": {{ID: "a"}, {ID: "a"}}}, 60); err == nil {
		t.Fatal("RRF accepted duplicate candidate identity inside one channel")
	}
}

func TestVersionedSparseEncoderProducesClassicBM25Factors(t *testing.T) {
	encoder, err := retrieval.NewSparseEncoder(retrieval.NewMixedAnalyzer("nano-mixed-v1"), 1.2, 0.75, 24)
	if err != nil {
		t.Fatal(err)
	}
	document, err := encoder.Document("苹果 apple 苹果")
	if err != nil {
		t.Fatal(err)
	}
	query, err := encoder.Query("apple 苹果")
	if err != nil {
		t.Fatal(err)
	}
	if len(document.Indices) == 0 || len(document.Indices) != len(document.Values) || len(query.Indices) == 0 {
		t.Fatalf("document=%+v query=%+v", document, query)
	}
	for _, value := range query.Values {
		if value != 1 {
			t.Fatalf("query value=%f, want Qdrant IDF input 1", value)
		}
	}
	second, err := encoder.Document("苹果 apple 苹果")
	if err != nil || !reflect.DeepEqual(document, second) {
		t.Fatalf("sparse encoding is not deterministic: first=%+v second=%+v err=%v", document, second, err)
	}
	if _, err := retrieval.NewSparseEncoder(retrieval.NewMixedAnalyzer("nano-mixed-v1"), 1.2, 0.75, 0); err == nil {
		t.Fatal("Sparse Encoder accepted zero average document length")
	}
}

func containsToken(tokens []string, want string) bool {
	for _, token := range tokens {
		if token == want {
			return true
		}
	}
	return false
}
