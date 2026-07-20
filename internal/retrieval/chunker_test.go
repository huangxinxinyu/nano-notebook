package retrieval_test

import (
	"reflect"
	"testing"
	"unicode/utf8"

	"github.com/huangxinxinyu/nano-notebook/internal/retrieval"
)

func TestChunkerIsDeterministicStructureAwareAndRevisionScoped(t *testing.T) {
	units := []retrieval.Unit{
		{ID: "unit_heading", Ordinal: 0, Kind: "heading", Text: "# Findings"},
		{ID: "unit_first", Ordinal: 1, Kind: "paragraph", Text: "First paragraph keeps its evidence identity."},
		{ID: "unit_second", Ordinal: 2, Kind: "paragraph", Text: "第二段包含中文证据，并保持字符边界。"},
		{ID: "unit_table", Ordinal: 3, Kind: "table", Text: "A | B\n---|---\n1 | 2"},
	}
	config := retrieval.ChunkConfig{MaxRunes: 64, OverlapRunes: 16, PreserveHeadingContext: true}
	first, err := retrieval.BuildChunks("riv_candidate_1", "evr_fixture", units, config)
	if err != nil {
		t.Fatal(err)
	}
	second, err := retrieval.BuildChunks("riv_candidate_1", "evr_fixture", units, config)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(first, second) || len(first) < 2 {
		t.Fatalf("chunks not deterministic or bounded: first=%+v second=%+v", first, second)
	}
	seen := map[string]bool{}
	for ordinal, chunk := range first {
		if chunk.ID == "" || chunk.Ordinal != ordinal || chunk.IndexVersionID != "riv_candidate_1" ||
			chunk.RevisionID != "evr_fixture" || utf8.RuneCountInString(chunk.Text) > config.MaxRunes || len(chunk.UnitRefs) == 0 {
			t.Fatalf("chunk %d = %+v", ordinal, chunk)
		}
		for _, ref := range chunk.UnitRefs {
			seen[ref.UnitID] = true
		}
	}
	for _, unit := range units {
		if !seen[unit.ID] {
			t.Errorf("unit %q absent from all chunks", unit.ID)
		}
	}
}

func TestChunkerSplitsOversizedUnitsOnlyByRuneBoundaries(t *testing.T) {
	text := "这是一个很长的证据单元，用来验证分块不会切坏 UTF-8 字符，并且保留稳定的源相对范围。"
	chunks, err := retrieval.BuildChunks("riv_candidate_2", "evr_large", []retrieval.Unit{
		{ID: "unit_large", Ordinal: 0, Kind: "paragraph", Text: text},
	}, retrieval.ChunkConfig{MaxRunes: 18, OverlapRunes: 4})
	if err != nil {
		t.Fatal(err)
	}
	if len(chunks) < 2 {
		t.Fatalf("oversized unit chunks = %+v", chunks)
	}
	previousEnd := 0
	for index, chunk := range chunks {
		if len(chunk.UnitRefs) != 1 || chunk.UnitRefs[0].UnitID != "unit_large" ||
			chunk.UnitRefs[0].EndRune-chunk.UnitRefs[0].StartRune > 18 || !utf8.ValidString(chunk.Text) {
			t.Fatalf("chunk %d = %+v", index, chunk)
		}
		if index > 0 && chunk.UnitRefs[0].StartRune != previousEnd-4 {
			t.Fatalf("chunk %d starts at %d, want overlap start %d", index, chunk.UnitRefs[0].StartRune, previousEnd-4)
		}
		previousEnd = chunk.UnitRefs[0].EndRune
	}
	if previousEnd != utf8.RuneCountInString(text) {
		t.Fatalf("final end=%d, want %d", previousEnd, utf8.RuneCountInString(text))
	}
}

func TestChunkerRejectsUnversionedOrInvalidConfiguration(t *testing.T) {
	unit := []retrieval.Unit{{ID: "unit", Kind: "paragraph", Text: "evidence"}}
	for _, test := range []struct {
		version string
		config  retrieval.ChunkConfig
	}{
		{"", retrieval.ChunkConfig{MaxRunes: 64}},
		{"riv", retrieval.ChunkConfig{MaxRunes: 0}},
		{"riv", retrieval.ChunkConfig{MaxRunes: 32, OverlapRunes: 32}},
	} {
		if _, err := retrieval.BuildChunks(test.version, "evr", unit, test.config); err == nil {
			t.Fatalf("BuildChunks accepted version=%q config=%+v", test.version, test.config)
		}
	}
}
