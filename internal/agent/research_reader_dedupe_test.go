package agent

import "testing"

func TestResearchDocumentKeyJoinsArxivURLVariants(t *testing.T) {
	key := researchDocumentKey("https://arxiv.org/abs/2510.22344")
	for _, url := range []string{"https://arxiv.org/html/2510.22344v1", "http://arxiv.org/pdf/2510.22344v2", "https://ar5iv.labs.arxiv.org/html/2510.22344", "https://www.arxiv.org/abs/2510.22344"} {
		if got := researchDocumentKey(url); got != key {
			t.Fatalf("%s key=%q want %q", url, got, key)
		}
	}
	if researchDocumentKey("https://arxiv.org/abs/2510.22345") == key || researchDocumentKey("https://example.com/arxiv.org/abs/2510.22344") == key {
		t.Fatal("different documents share a reader key")
	}
}

func TestResearchReaderTaskURLReadsTheDelegatedDocument(t *testing.T) {
	task := researchReaderSpawnInput("https://arxiv.org/abs/2510.22344", "FAIR-RAG (Faithful) https://x", 30)
	if got := researchReaderTaskURL(task.Message); got != "https://arxiv.org/abs/2510.22344" {
		t.Fatalf("url=%q", got)
	}
	if researchReaderTaskURL("Independently verify https://example.com (x).") != "" {
		t.Fatal("a non-reader task named a document")
	}
}
