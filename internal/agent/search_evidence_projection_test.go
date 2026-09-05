package agent

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/huangxinxinyu/nano-notebook/internal/retrieval"
)

func TestBuildSearchEvidenceModelOutputIsBudgetedAndOmitsRecoveryMetadata(t *testing.T) {
	manifest := searchEvidenceResult{
		ResultVersion: SearchEvidenceResultVersion,
		Evidence:      make([]searchEvidenceReference, 0, maxSearchEvidenceCandidates),
	}
	candidates := make([]retrieval.EvidenceCandidate, 0, maxSearchEvidenceCandidates)
	for index := 0; index < maxSearchEvidenceCandidates; index++ {
		chunkID := fmt.Sprintf("chunk_%d", index)
		manifest.Evidence = append(manifest.Evidence, searchEvidenceReference{
			ChunkID: chunkID, SourceID: fmt.Sprintf("src_%d", index), EvidenceRevisionID: fmt.Sprintf("evr_%d", index),
		})
		candidates = append(candidates, retrieval.EvidenceCandidate{
			ID: chunkID, SourceID: fmt.Sprintf("src_%d", index), RevisionID: fmt.Sprintf("evr_%d", index),
			SourceTitle: fmt.Sprintf("Source %d", index), Preview: fmt.Sprintf("passage-%d %s", index, strings.Repeat("文", 900)),
			UnitRefs: []retrieval.UnitRef{{UnitID: fmt.Sprintf("unit_secret_%d", index), StartRune: 0, EndRune: 900}},
		})
	}

	const limit = 4 * 1024
	encoded, err := buildSearchEvidenceModelOutput(manifest, candidates, limit)
	if err != nil {
		t.Fatal(err)
	}
	if len(encoded) > limit {
		t.Fatalf("projection bytes=%d, limit=%d", len(encoded), limit)
	}
	for _, forbidden := range [][]byte{[]byte(`"chunk_id"`), []byte(`"evidence_ranges"`), []byte("unit_secret_")} {
		if bytes.Contains(encoded, forbidden) {
			t.Fatalf("projection leaked recovery metadata %q: %s", forbidden, encoded)
		}
	}
	var output struct {
		Evidence []struct {
			SourceID    string `json:"source_id"`
			SourceTitle string `json:"source_title"`
			Preview     string `json:"preview"`
		} `json:"evidence"`
		Truncated    bool `json:"truncated"`
		OmittedCount int  `json:"omitted_count"`
	}
	if err := json.Unmarshal(encoded, &output); err != nil {
		t.Fatal(err)
	}
	if len(output.Evidence) == 0 || output.Evidence[0].SourceID != "src_0" || output.Evidence[0].SourceTitle != "Source 0" ||
		!strings.HasPrefix(output.Evidence[0].Preview, "passage-0 ") || !output.Truncated || output.OmittedCount == 0 {
		t.Fatalf("projection=%s", encoded)
	}
}

func TestBuildSearchEvidenceModelOutputTruncatesTheTopPreviewBeforeDroppingAllEvidence(t *testing.T) {
	manifest := searchEvidenceResult{
		ResultVersion: SearchEvidenceResultVersion,
		Evidence:      []searchEvidenceReference{{ChunkID: "chunk_a", SourceID: "src_a", EvidenceRevisionID: "evr_a"}},
	}
	encoded, err := buildSearchEvidenceModelOutput(manifest, []retrieval.EvidenceCandidate{{
		ID: "chunk_a", SourceID: "src_a", RevisionID: "evr_a", SourceTitle: "Title", Preview: strings.Repeat("evidence ", 2000),
	}}, 512)
	if err != nil {
		t.Fatal(err)
	}
	var output struct {
		Evidence []struct {
			Preview          string `json:"preview"`
			PreviewTruncated bool   `json:"preview_truncated"`
		} `json:"evidence"`
		Truncated bool `json:"truncated"`
	}
	if err := json.Unmarshal(encoded, &output); err != nil {
		t.Fatal(err)
	}
	if len(encoded) > 512 || len(output.Evidence) != 1 || output.Evidence[0].Preview == "" || !output.Evidence[0].PreviewTruncated || !output.Truncated {
		t.Fatalf("projection=%s", encoded)
	}
}

func TestSourceFirstSearchEvidenceProjectionCarriesRevisionChunkRangeAndPDFPage(t *testing.T) {
	manifest := searchEvidenceResult{
		ResultVersion: SearchEvidenceResultVersion,
		Scope: &EvidenceSearchResolvedScope{
			SourceID: "src_pdf", EntryID: "entry_conclusion", PageStart: 7, PageEnd: 8,
		},
		Evidence: []searchEvidenceReference{{ChunkID: "chunk_pdf", SourceID: "src_pdf", EvidenceRevisionID: "evr_pdf"}},
	}
	raw, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	projected, err := projectSearchEvidenceForModel(Execution{
		AgentConfigID: "research.executor@9", ActionResultByteLimit: 8 * 1024,
	}, raw, []retrieval.EvidenceCandidate{{
		ID: "chunk_pdf", SourceID: "src_pdf", RevisionID: "evr_pdf", SourceTitle: "Paper", Preview: "Page-aware evidence.",
		UnitRefs:    []retrieval.UnitRef{{UnitID: "unit_pdf", StartRune: 0, EndRune: 20}},
		Coordinates: []retrieval.EvidenceCoordinate{{Kind: "pdf_region", Page: 7, X: 72, Y: 700, Width: 180, Height: 14}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	for _, required := range [][]byte{
		[]byte(`"source_id":"src_pdf"`), []byte(`"evidence_revision_id":"evr_pdf"`),
		[]byte(`"chunk_id":"chunk_pdf"`), []byte(`"unit_id":"unit_pdf"`),
		[]byte(`"kind":"pdf_region"`), []byte(`"page":7`), []byte("Page-aware evidence."),
		[]byte(`"entry_id":"entry_conclusion"`), []byte(`"page_start":7`), []byte(`"page_end":8`),
	} {
		if !bytes.Contains(projected, required) {
			t.Fatalf("source-first projection missing %s: %s", required, projected)
		}
	}
}

func TestDecodeSearchEvidenceResultAcceptsLegacyExpandedCheckpoint(t *testing.T) {
	decoded, err := decodeSearchEvidenceResult(json.RawMessage(`{
		"complete_empty":false,"degraded":false,"degradations":[],
		"evidence":[{"source_id":"src_old","evidence_revision_id":"evr_old","source_title":"Old title","preview":"Old passage",
		"evidence_ranges":[{"unit_id":"unit_old","start_rune":1,"end_rune":8}]}]
	}`))
	if err != nil {
		t.Fatal(err)
	}
	if !decoded.Legacy || len(decoded.Evidence) != 1 || decoded.Evidence[0].SourceID != "src_old" || decoded.Evidence[0].Preview != "Old passage" {
		t.Fatalf("decoded=%+v", decoded)
	}
}

func TestDecodeSearchEvidenceResultRejectsEvidenceOutsideEchoedSourceScope(t *testing.T) {
	_, err := decodeSearchEvidenceResult(json.RawMessage(`{
		"result_version":2,
		"scope":{"source_id":"src_scoped"},
		"complete_empty":false,"degraded":false,"degradations":[],
		"evidence":[{"chunk_id":"chunk_other","source_id":"src_other","evidence_revision_id":"evr_other"}]
	}`))
	if !errors.Is(err, ErrGroundingInvalid) {
		t.Fatalf("cross-source scoped result error=%v", err)
	}
}

func TestSourceFirstSearchEvidenceProjectionRejectsCandidateOutsideEntryPages(t *testing.T) {
	manifest := searchEvidenceResult{
		ResultVersion: SearchEvidenceResultVersion,
		Scope: &EvidenceSearchResolvedScope{
			SourceID: "src_pdf", EntryID: "entry_conclusion", PageStart: 7, PageEnd: 8,
		},
		Evidence: []searchEvidenceReference{{ChunkID: "chunk_pdf", SourceID: "src_pdf", EvidenceRevisionID: "evr_pdf"}},
	}
	_, err := buildSourceFirstSearchEvidenceModelOutput(manifest, []retrieval.EvidenceCandidate{{
		ID: "chunk_pdf", SourceID: "src_pdf", RevisionID: "evr_pdf", Preview: "Wrong page.",
		Coordinates: []retrieval.EvidenceCoordinate{{Kind: "pdf_region", Page: 2}},
	}}, 8*1024)
	if !errors.Is(err, ErrGroundingInvalid) {
		t.Fatalf("out-of-entry projection error=%v", err)
	}
}
