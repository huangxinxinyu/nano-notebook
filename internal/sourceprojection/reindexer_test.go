package sourceprojection

import (
	"context"
	"reflect"
	"testing"

	"github.com/huangxinxinyu/nano-notebook/internal/models"
	"github.com/huangxinxinyu/nano-notebook/internal/retrieval"
)

type recordingEmbedder struct {
	requests []models.EmbeddingRequest
}

func (e *recordingEmbedder) Embed(_ context.Context, request models.EmbeddingRequest) (models.EmbeddingOutcome, error) {
	e.requests = append(e.requests, request)
	vectors := make([][]float32, len(request.Inputs))
	for index := range vectors {
		vectors[index] = []float32{1, 0, 0}
	}
	return models.EmbeddingOutcome{Vectors: vectors}, nil
}

func TestEmbedTextsUsesVersionedDocumentProfileWithoutBatchAggregation(t *testing.T) {
	t.Parallel()

	embedder := &recordingEmbedder{}
	version := retrieval.IndexVersion{Config: retrieval.IndexConfig{
		EmbeddingModel: "gemini/gemini-embedding-2", EmbeddingDimensions: 3,
		EmbeddingProfileID: retrieval.EmbeddingProfileGeminiRetrievalV1,
	}}
	vectors, err := embedTexts(context.Background(), embedder, version, " Product Roadmap ", []string{" First chunk. ", "第二段"})
	if err != nil {
		t.Fatal(err)
	}
	if len(vectors) != 2 || len(embedder.requests) != 2 {
		t.Fatalf("vectors=%d requests=%d", len(vectors), len(embedder.requests))
	}
	for index, want := range []string{"title: Product Roadmap | text: First chunk.", "title: Product Roadmap | text: 第二段"} {
		request := embedder.requests[index]
		if request.Model != "gemini/gemini-embedding-2" || request.Dimensions != 3 || !reflect.DeepEqual(request.Inputs, []string{want}) {
			t.Fatalf("request[%d]=%+v", index, request)
		}
	}
}
