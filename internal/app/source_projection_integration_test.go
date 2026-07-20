package app_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/huangxinxinyu/nano-notebook/internal/evidence"
	"github.com/huangxinxinyu/nano-notebook/internal/models"
	"github.com/huangxinxinyu/nano-notebook/internal/objectstore"
	"github.com/huangxinxinyu/nano-notebook/internal/qdrantstore"
	"github.com/huangxinxinyu/nano-notebook/internal/retrieval"
	"github.com/huangxinxinyu/nano-notebook/internal/source"
	"github.com/huangxinxinyu/nano-notebook/internal/sourcejobs"
	"github.com/huangxinxinyu/nano-notebook/internal/sourceprocessing"
	"github.com/huangxinxinyu/nano-notebook/internal/sourceprojection"
)

func TestSourceProjectionBuildsAndVerifiesRealQdrantBeforeReady(t *testing.T) {
	qdrantURL := os.Getenv("NANO_TEST_QDRANT_URL")
	if qdrantURL == "" {
		t.Skip("NANO_TEST_QDRANT_URL is required")
	}
	api := newTestAPI(t)
	config := retrieval.IndexConfig{
		Chunk:      retrieval.ChunkConfig{MaxRunes: 32, OverlapRunes: 4, PreserveHeadingContext: true},
		AnalyzerID: "nano-mixed-v1", BM25K1: 1.2, BM25B: 0.75, BM25AverageDocumentLength: 24,
		EmbeddingModel: "test/embed", EmbeddingDimensions: 3,
		DenseCandidates: 20, SparseCandidates: 20, RRFK: 60,
		RerankerID: "test/rerank", RerankCandidates: 10, DegradationPolicyID: "hybrid-required-v1",
	}
	versions := retrieval.NewVersionStore(api.db.Pool())
	version, err := versions.CreateCandidate(context.Background(), "riv_source_projection", config)
	if err != nil {
		t.Fatal(err)
	}
	if err := versions.RecordEval(context.Background(), retrieval.EvalRun{
		ID: "eval_source_projection", IndexVersionID: version.ID, FixtureSuiteSHA256: sixtyFour("e"),
		Status: retrieval.EvalPassed, MetricsJSON: []byte(`{"citation_precision":1}`),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := versions.Promote(context.Background(), version.ID, "eval_source_projection"); err != nil {
		t.Fatal(err)
	}

	owner := api.register(t, "source-projection@example.com")
	notebookID := createSourceTestNotebook(t, api, owner, "source-projection")
	ownerID := sourceTestUserID(t, api, "source-projection@example.com")
	payload := []byte("# Heading\n\nApple evidence.\n\n第二段证据。\n")
	objectKey := seedProcessableSource(t, api, ownerID, notebookID, "src_projection", "srcjob_projection", source.FormatMarkdown, payload)
	objects := objectstore.NewMemoryStore()
	if err := objects.Put(context.Background(), objectKey, payload); err != nil {
		t.Fatal(err)
	}
	qdrant, err := qdrantstore.New(qdrantstore.Config{
		BaseURL: qdrantURL, Collection: "nano_test_" + uuid.NewString(), DenseDimensions: 3, RequestTimeout: 5 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = qdrant.DeleteCollection(context.Background()) })
	if err := qdrant.EnsureCollection(context.Background()); err != nil {
		t.Fatal(err)
	}
	queue := sourcejobs.NewQueue(api.db.Pool(), time.Minute)
	lease, ok, err := queue.Claim(context.Background())
	if err != nil || !ok {
		t.Fatalf("Claim=%+v ok=%v err=%v", lease, ok, err)
	}
	projection := sourceprojection.New(api.db.Pool(), qdrant, deterministicEmbedder{})
	processor := sourceprocessing.NewProcessor(api.db.Pool(), queue, evidence.NewPublisher(api.db.Pool(), objects), objects, projection, sourceprocessing.Config{
		ExtractionConfigID: "extract-text-v1", MaxSourceBytes: 1 << 20, MaxNormalizedRunes: 10_000,
	})
	if err := processor.ProcessLease(context.Background(), lease); err != nil {
		t.Fatalf("ProcessLease: %v", err)
	}
	var revisionID, buildStatus string
	var expectedPoints int
	if err := api.db.Pool().QueryRow(context.Background(), `
		select b.revision_id, b.status, b.expected_points
		from retrieval_source_index_builds b where b.source_id='src_projection'
	`).Scan(&revisionID, &buildStatus, &expectedPoints); err != nil {
		t.Fatal(err)
	}
	if buildStatus != "verified" || expectedPoints < 1 {
		t.Fatalf("build revision=%s status=%s expected=%d", revisionID, buildStatus, expectedPoints)
	}
	scope := qdrantstore.Scope{
		NotebookID: notebookID, IndexVersionID: version.ID,
		Evidence: []qdrantstore.EvidenceRef{{SourceID: "src_projection", RevisionID: revisionID}},
	}
	dense, err := qdrant.SearchDense(context.Background(), []float32{1, 0, 0}, scope, 20)
	if err != nil || len(dense) != expectedPoints {
		t.Fatalf("dense=%+v err=%v expected=%d", dense, err, expectedPoints)
	}
	encoder, err := retrieval.NewSparseEncoder(retrieval.NewMixedAnalyzer(config.AnalyzerID), config.BM25K1, config.BM25B, config.BM25AverageDocumentLength)
	if err != nil {
		t.Fatal(err)
	}
	sparseQuery, err := encoder.Query("Apple 证据")
	if err != nil {
		t.Fatal(err)
	}
	sparse, err := qdrant.SearchSparse(context.Background(), sparseQuery, scope, 20)
	if err != nil || len(sparse) == 0 {
		t.Fatalf("sparse=%+v err=%v", sparse, err)
	}
}

type deterministicEmbedder struct{}

func (deterministicEmbedder) Embed(_ context.Context, request models.EmbeddingRequest) (models.EmbeddingOutcome, error) {
	vectors := make([][]float32, len(request.Inputs))
	for index := range request.Inputs {
		vectors[index] = []float32{1, float32(index) / 10, 0}
	}
	return models.EmbeddingOutcome{Vectors: vectors}, nil
}
