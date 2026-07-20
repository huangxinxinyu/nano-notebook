package normalize

import (
	"crypto/sha256"
	"encoding/hex"
	"testing"
)

func TestValidatorAcceptsOnlyPreciselyBoundedNonPrimaryCoverageGaps(t *testing.T) {
	valid := partialPDFArtifact(t)
	if err := Validate(valid); err != nil {
		t.Fatalf("Validate(bounded non-primary gap): %v", err)
	}

	tests := []struct {
		name   string
		mutate func(*Artifact)
	}{
		{"partial without gap", func(artifact *Artifact) { artifact.Coverage.Gaps = nil }},
		{"unknown location", func(artifact *Artifact) { artifact.Coverage.Gaps[0].Coordinate = nil }},
		{"primary loss", func(artifact *Artifact) { artifact.Coverage.Gaps[0].Impact = "primary" }},
		{"unknown reason", func(artifact *Artifact) { artifact.Coverage.Gaps[0].Reason = "parser_did_something" }},
		{"invalid coordinate", func(artifact *Artifact) { artifact.Coverage.Gaps[0].Coordinate.Width = 0 }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			candidate := valid
			candidate.Coverage.Gaps = append([]Gap(nil), valid.Coverage.Gaps...)
			coordinate := *valid.Coverage.Gaps[0].Coordinate
			candidate.Coverage.Gaps[0].Coordinate = &coordinate
			test.mutate(&candidate)
			resignArtifact(t, &candidate)
			if err := Validate(candidate); err == nil {
				t.Fatal("Validate accepted unknown or primary Evidence Coverage loss")
			}
		})
	}
}

func partialPDFArtifact(t *testing.T) Artifact {
	t.Helper()
	artifact := Artifact{
		SchemaVersion: "nano.normalized-source.v1", SourceID: "src_partial_pdf",
		ExtractionConfigID: "extract-pdf-native-v1", Format: "pdf", Text: "Primary evidence.",
		Blocks: []Block{{
			ID: "block_000001", Ordinal: 0, Kind: "paragraph", Text: "Primary evidence.", StartRune: 0, EndRune: 17,
			Coordinate: &SourceCoordinate{Kind: "pdf_region", Page: 1, X: 72, Y: 700, Width: 110, Height: 12},
		}},
		Coverage: Coverage{Status: "partial", TotalRunes: 17, Gaps: []Gap{{
			Reason: "decorative_visual_skipped", Impact: "non_primary",
			Coordinate: &SourceCoordinate{Kind: "pdf_region", Page: 1, X: 300, Y: 500, Width: 80, Height: 60},
		}}},
	}
	resignArtifact(t, &artifact)
	return artifact
}

func resignArtifact(t *testing.T, artifact *Artifact) {
	t.Helper()
	canonical, err := canonicalArtifact(*artifact)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(canonical)
	artifact.SHA256 = hex.EncodeToString(digest[:])
	artifact.CanonicalJSON = canonical
}
