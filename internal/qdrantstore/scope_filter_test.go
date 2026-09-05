package qdrantstore

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestBuildFilterRestrictsCandidateSearchToAllowedChunkIDs(t *testing.T) {
	filter, _, err := buildFilter(Scope{
		NotebookID: "nb_ready", IndexVersionID: "riv_ready",
		Evidence:        []EvidenceRef{{SourceID: "src_ready", RevisionID: "evr_ready"}},
		AllowedChunkIDs: []string{"chunk_b", "chunk_a"},
	})
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(filter)
	if err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{`"key":"chunk_id"`, `"any":["chunk_a","chunk_b"]`} {
		if !strings.Contains(string(encoded), required) {
			t.Fatalf("chunk restriction %q missing from %s", required, encoded)
		}
	}
}

func TestBuildFilterRejectsInvalidAllowedChunkIDs(t *testing.T) {
	for _, ids := range [][]string{nil, {""}, {"chunk_a", "chunk_a"}} {
		if ids == nil {
			continue
		}
		_, _, err := buildFilter(Scope{
			NotebookID: "nb_ready", IndexVersionID: "riv_ready",
			Evidence:        []EvidenceRef{{SourceID: "src_ready", RevisionID: "evr_ready"}},
			AllowedChunkIDs: ids,
		})
		if err == nil {
			t.Fatalf("accepted invalid chunk IDs: %#v", ids)
		}
	}
}
