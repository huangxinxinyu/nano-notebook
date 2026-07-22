package agent

import (
	"reflect"
	"testing"
)

func TestNormalizeSourceMarkersRetainsAllowedMarkersAndDeduplicatesReferences(t *testing.T) {
	text, references, discarded := normalizeSourceMarkers(
		"First fact [source:src_b]. Second fact [source:src_a], repeated [source:src_b].",
		map[string]struct{}{"src_a": {}, "src_b": {}},
	)
	if text != "First fact [source:src_b]. Second fact [source:src_a], repeated [source:src_b]." {
		t.Fatalf("text = %q", text)
	}
	if !reflect.DeepEqual(references, []string{"src_b", "src_a"}) {
		t.Fatalf("references = %#v", references)
	}
	if discarded != 0 {
		t.Fatalf("discarded = %d", discarded)
	}
}

func TestNormalizeSourceMarkersRemovesUnknownAndMalformedMarkers(t *testing.T) {
	text, references, discarded := normalizeSourceMarkers(
		"Keep [source:src_a]. Drop [source:src_unknown], [source:], and [source: src_a].",
		map[string]struct{}{"src_a": {}},
	)
	if text != "Keep [source:src_a]. Drop , , and ." {
		t.Fatalf("text = %q", text)
	}
	if !reflect.DeepEqual(references, []string{"src_a"}) {
		t.Fatalf("references = %#v", references)
	}
	if discarded != 3 {
		t.Fatalf("discarded = %d", discarded)
	}
}

func TestNormalizeSourceMarkersCapsAcceptedOccurrences(t *testing.T) {
	input := ""
	for index := 0; index < maxSourceMarkerOccurrences+1; index++ {
		input += "[source:src_a]"
	}
	text, references, discarded := normalizeSourceMarkers(input, map[string]struct{}{"src_a": {}})
	if len(references) != 1 || references[0] != "src_a" {
		t.Fatalf("references = %#v", references)
	}
	if discarded != 1 {
		t.Fatalf("discarded = %d", discarded)
	}
	want := ""
	for index := 0; index < maxSourceMarkerOccurrences; index++ {
		want += "[source:src_a]"
	}
	if text != want {
		t.Fatalf("retained marker count text length = %d, want %d", len(text), len(want))
	}
}
