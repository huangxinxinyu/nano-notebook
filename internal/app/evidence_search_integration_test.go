package app_test

import (
	"context"
	"net/http"
	"testing"

	"github.com/huangxinxinyu/nano-notebook/internal/agent"
	"github.com/huangxinxinyu/nano-notebook/internal/jobs"
	"github.com/huangxinxinyu/nano-notebook/internal/models"
	"github.com/huangxinxinyu/nano-notebook/internal/qdrantstore"
	"github.com/huangxinxinyu/nano-notebook/internal/retrieval"
)

type evidenceVectorSearchStub struct {
	candidateID string
	scopes      []qdrantstore.Scope
}

func (s *evidenceVectorSearchStub) SearchDense(_ context.Context, _ []float32, scope qdrantstore.Scope, _ int) ([]retrieval.Candidate, error) {
	s.scopes = append(s.scopes, scope)
	return []retrieval.Candidate{{ID: s.candidateID, Score: 0.9}}, nil
}

func (s *evidenceVectorSearchStub) SearchSparse(_ context.Context, _ retrieval.SparseVector, scope qdrantstore.Scope, _ int) ([]retrieval.Candidate, error) {
	s.scopes = append(s.scopes, scope)
	return []retrieval.Candidate{{ID: s.candidateID, Score: 4.2}}, nil
}

type evidenceModelsStub struct{}

func (evidenceModelsStub) Embed(_ context.Context, request models.EmbeddingRequest) (models.EmbeddingOutcome, error) {
	return models.EmbeddingOutcome{Vectors: [][]float32{{0.1, 0.2, 0.3}}}, nil
}

func (evidenceModelsStub) Rerank(_ context.Context, request models.RerankRequest) (models.RerankOutcome, error) {
	ids := make([]string, 0, len(request.Candidates))
	for _, candidate := range request.Candidates {
		ids = append(ids, candidate.ID)
	}
	return models.RerankOutcome{CandidateIDs: ids}, nil
}

func TestSearchEvidenceTraversesPinnedScopeAndReloadsAuthoritativeEvidenceRanges(t *testing.T) {
	api := newTestAPI(t)
	sessionCookie, csrfCookie := api.registerWithCSRF(t, "search-evidence@example.com")
	notebookID, chatID := createNotebookAndChatForEvidenceSet(t, api, sessionCookie, csrfCookie)
	installReadyEvidenceSetFixture(t, api, notebookID, "src_search", "evr_search", "", "")
	if _, err := api.db.Pool().Exec(context.Background(), `
		insert into source_evidence_units(
			id,revision_id,source_id,notebook_id,ordinal,kind,text_content,start_rune,end_rune
		) values('unit_search','evr_search','src_search',$1,0,'paragraph','The launch date is 20 July.',0,27)
	`, notebookID); err != nil {
		t.Fatal(err)
	}

	response := api.postJSONWithCookieAndCSRF(t, "/api/v1/chats/"+chatID+"/messages", map[string]any{
		"id": "0190cdd2-5f2d-7ad8-b3f5-1b588788c093", "content": "When is launch?", "source_ids": []string{"src_search"},
	}, sessionCookie, csrfCookie, csrfCookie.Value, "")
	if response.Code != http.StatusAccepted {
		t.Fatalf("admission status=%d body=%s", response.Code, response.Body.String())
	}
	claimed, ok, err := jobs.NewQueue(api.db.Pool()).ClaimNext(context.Background())
	if err != nil || !ok {
		t.Fatalf("claim=%+v ok=%v err=%v", claimed, ok, err)
	}
	chunks, err := retrieval.BuildChunks("riv_pin_active", "evr_search", []retrieval.Unit{{
		ID: "unit_search", Ordinal: 0, Kind: "paragraph", Text: "The launch date is 20 July.",
	}}, retrieval.ChunkConfig{MaxRunes: 512, OverlapRunes: 64, PreserveHeadingContext: true})
	if err != nil {
		t.Fatal(err)
	}
	vectors := &evidenceVectorSearchStub{candidateID: chunks[0].ID}
	service := agent.NewEvidenceSearchService(api.db.Pool(), vectors, evidenceModelsStub{})
	result, err := service.SearchEvidence(context.Background(), attemptFromClaim(claimed), "launch date", "answer the user's date question")
	if err != nil {
		t.Fatal(err)
	}
	if result.Degraded || result.CompleteEmpty || len(result.Candidates) != 1 {
		t.Fatalf("search result=%+v", result)
	}
	candidate := result.Candidates[0]
	if candidate.ID != chunks[0].ID || candidate.SourceID != "src_search" || candidate.RevisionID != "evr_search" ||
		candidate.SourceTitle != "src_search" || candidate.Preview != "The launch date is 20 July." ||
		len(candidate.UnitRefs) != 1 || candidate.UnitRefs[0].UnitID != "unit_search" {
		t.Fatalf("candidate=%+v", candidate)
	}
	if len(vectors.scopes) != 2 {
		t.Fatalf("Qdrant calls=%d", len(vectors.scopes))
	}
	for _, scope := range vectors.scopes {
		if scope.NotebookID != notebookID || scope.IndexVersionID != "riv_pin_active" || len(scope.Evidence) != 1 ||
			scope.Evidence[0] != (qdrantstore.EvidenceRef{SourceID: "src_search", RevisionID: "evr_search"}) {
			t.Fatalf("forged Qdrant scope=%+v", scope)
		}
	}
}
