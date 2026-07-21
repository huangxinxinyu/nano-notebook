package retrieval

import "testing"

func TestGeminiRetrievalProfileFormatsAsymmetricInputsDeterministically(t *testing.T) {
	t.Parallel()

	query, err := FormatEmbeddingQuery(EmbeddingProfileGeminiRetrievalV1, "  launch date?\n")
	if err != nil {
		t.Fatal(err)
	}
	if query != "task: search result | query: launch date?" {
		t.Fatalf("query=%q", query)
	}

	document, err := FormatEmbeddingDocument(EmbeddingProfileGeminiRetrievalV1, "  Product Roadmap  ", "\nLaunch is 20 July.  ")
	if err != nil {
		t.Fatal(err)
	}
	if document != "title: Product Roadmap | text: Launch is 20 July." {
		t.Fatalf("document=%q", document)
	}

	untitled, err := FormatEmbeddingDocument(EmbeddingProfileGeminiRetrievalV1, " \n ", "Evidence")
	if err != nil {
		t.Fatal(err)
	}
	if untitled != "title: none | text: Evidence" {
		t.Fatalf("untitled=%q", untitled)
	}
}

func TestEmbeddingProfileRejectsUnknownOrEmptyInputs(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name string
		call func() error
	}{
		{name: "unknown query profile", call: func() error { _, err := FormatEmbeddingQuery("unknown", "query"); return err }},
		{name: "empty query", call: func() error { _, err := FormatEmbeddingQuery(EmbeddingProfileGeminiRetrievalV1, " \n "); return err }},
		{name: "unknown document profile", call: func() error { _, err := FormatEmbeddingDocument("unknown", "title", "text"); return err }},
		{name: "empty document", call: func() error {
			_, err := FormatEmbeddingDocument(EmbeddingProfileGeminiRetrievalV1, "title", " \n ")
			return err
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			if err := test.call(); err == nil {
				t.Fatal("expected an error")
			}
		})
	}
}

func TestIndexConfigRequiresKnownEmbeddingProfile(t *testing.T) {
	t.Parallel()

	config := IndexConfig{
		Chunk: ChunkConfig{MaxRunes: 800, OverlapRunes: 120}, AnalyzerID: "nano-mixed-v1",
		BM25K1: 1.2, BM25B: .75, BM25AverageDocumentLength: 240,
		EmbeddingModel: "gemini/gemini-embedding-2", EmbeddingDimensions: 768,
		EmbeddingProfileID: EmbeddingProfileGeminiRetrievalV1,
		DenseCandidates:    40, SparseCandidates: 40, RRFK: 60,
		RerankerID: "rerank-v1", RerankCandidates: 20, DegradationPolicyID: "hybrid-required-v1",
	}
	if !validIndexConfig(config) {
		t.Fatal("valid Gemini IndexConfig was rejected")
	}
	config.EmbeddingProfileID = ""
	if validIndexConfig(config) {
		t.Fatal("IndexConfig accepted an empty embedding profile")
	}
	config.EmbeddingProfileID = "unknown"
	if validIndexConfig(config) {
		t.Fatal("IndexConfig accepted an unknown embedding profile")
	}
}
