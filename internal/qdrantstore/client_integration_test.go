package qdrantstore_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/huangxinxinyu/nano-notebook/internal/qdrantstore"
	"github.com/huangxinxinyu/nano-notebook/internal/retrieval"
)

func TestQdrantProjectionEnforcesIndexedServerBuiltScope(t *testing.T) {
	baseURL := os.Getenv("NANO_TEST_QDRANT_URL")
	if baseURL == "" {
		t.Skip("NANO_TEST_QDRANT_URL is required")
	}
	collection := "nano_test_" + uuid.NewString()
	client, err := qdrantstore.New(qdrantstore.Config{
		BaseURL: baseURL, Collection: collection, DenseDimensions: 3, RequestTimeout: 5 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	t.Cleanup(func() { _ = client.DeleteCollection(context.Background()) })
	if err := client.EnsureCollection(ctx); err != nil {
		t.Fatalf("EnsureCollection: %v", err)
	}
	if err := client.EnsureCollection(ctx); err != nil {
		t.Fatalf("idempotent EnsureCollection: %v", err)
	}
	points := []qdrantstore.Point{
		projectionPoint("chunk_a", "nb_1", "src_1", "evr_1", "riv_1", []float32{1, 0, 0}, []uint32{1, 9}, []float32{2, 1}),
		projectionPoint("chunk_other_source", "nb_1", "src_2", "evr_2", "riv_1", []float32{1, 0, 0}, []uint32{1}, []float32{3}),
		projectionPoint("chunk_other_notebook", "nb_2", "src_1", "evr_foreign", "riv_1", []float32{1, 0, 0}, []uint32{1}, []float32{4}),
		projectionPoint("chunk_stale", "nb_1", "src_1", "evr_1", "riv_old", []float32{1, 0, 0}, []uint32{1}, []float32{5}),
	}
	if err := client.Upsert(ctx, points); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	scope := qdrantstore.Scope{
		NotebookID: "nb_1", IndexVersionID: "riv_1",
		Evidence: []qdrantstore.EvidenceRef{{SourceID: "src_1", RevisionID: "evr_1"}},
	}
	dense, err := client.SearchDense(ctx, []float32{1, 0, 0}, scope, 10)
	if err != nil {
		t.Fatalf("SearchDense: %v", err)
	}
	sparse, err := client.SearchSparse(ctx, retrieval.SparseVector{Indices: []uint32{1}, Values: []float32{1}}, scope, 10)
	if err != nil {
		t.Fatalf("SearchSparse: %v", err)
	}
	for name, candidates := range map[string][]retrieval.Candidate{"dense": dense, "sparse": sparse} {
		if len(candidates) != 1 || candidates[0].ID != "chunk_a" {
			t.Fatalf("%s candidates = %+v", name, candidates)
		}
	}
	count, err := client.Count(ctx, scope)
	if err != nil || count != 1 {
		t.Fatalf("Count=%d err=%v", count, err)
	}
	details, err := client.CollectionDetails(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if details.SparseModifier != "idf" {
		t.Fatalf("sparse modifier=%q", details.SparseModifier)
	}
	for _, field := range []string{"notebook_id", "source_id", "revision_id", "index_version_id"} {
		if details.PayloadIndexes[field] != "keyword" {
			t.Fatalf("payload index %q = %q", field, details.PayloadIndexes[field])
		}
	}
	if err := client.DeleteScope(ctx, scope); err != nil {
		t.Fatal(err)
	}
	count, err = client.Count(ctx, scope)
	if err != nil || count != 0 {
		t.Fatalf("post-delete Count=%d err=%v", count, err)
	}
}

func projectionPoint(chunkID, notebookID, sourceID, revisionID, versionID string, dense []float32, indices []uint32, values []float32) qdrantstore.Point {
	return qdrantstore.Point{
		ChunkID: chunkID, NotebookID: notebookID, SourceID: sourceID, RevisionID: revisionID,
		IndexVersionID: versionID, UnitIDs: []string{"unit_1"}, Dense: dense,
		Sparse: retrieval.SparseVector{Indices: indices, Values: values}, Checksum: "checksum_" + chunkID,
	}
}
