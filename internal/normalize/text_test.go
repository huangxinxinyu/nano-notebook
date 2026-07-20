package normalize_test

import (
	"bytes"
	"testing"

	"github.com/huangxinxinyu/nano-notebook/internal/normalize"
)

func TestTXTAdapterProducesDeterministicStructuredArtifact(t *testing.T) {
	input := normalize.Input{
		SourceID: "src_txt", ExtractionConfigID: "extract-text-v1", Format: "txt",
		Payload: []byte("\xef\xbb\xbfTitle\r\n\r\nFirst 段.\r\nSecond line.\r\n\r\nLast.\r\n"),
	}
	first, err := normalize.Text(input)
	if err != nil {
		t.Fatalf("Text: %v", err)
	}
	second, err := normalize.Text(input)
	if err != nil {
		t.Fatal(err)
	}
	if first.SchemaVersion != "nano.normalized-source.v1" || first.SourceID != input.SourceID ||
		first.ExtractionConfigID != input.ExtractionConfigID || first.Text != "Title\n\nFirst 段.\nSecond line.\n\nLast.\n" ||
		first.Coverage.Status != "complete" || len(first.Coverage.Gaps) != 0 || len(first.Blocks) != 3 {
		t.Fatalf("artifact = %+v", first)
	}
	if first.Blocks[1].Kind != "paragraph" || first.Blocks[1].Text != "First 段.\nSecond line." ||
		first.Blocks[1].StartRune != 7 || first.Blocks[1].EndRune != 28 {
		t.Fatalf("second block = %+v", first.Blocks[1])
	}
	if first.SHA256 == "" || first.SHA256 != second.SHA256 || !bytes.Equal(first.CanonicalJSON, second.CanonicalJSON) {
		t.Fatalf("determinism first=%q second=%q", first.SHA256, second.SHA256)
	}
}

func TestMarkdownAdapterRetainsHeadingAndListStructure(t *testing.T) {
	artifact, err := normalize.Text(normalize.Input{
		SourceID: "src_md", ExtractionConfigID: "extract-markdown-v1", Format: "markdown",
		Payload: []byte("# Evidence\n\nIntro text.\n\n- first\n- second\n\n## Detail\n\nFinal.\n"),
	})
	if err != nil {
		t.Fatal(err)
	}
	wantKinds := []string{"heading", "paragraph", "list", "heading", "paragraph"}
	if len(artifact.Blocks) != len(wantKinds) {
		t.Fatalf("blocks = %+v", artifact.Blocks)
	}
	for index, kind := range wantKinds {
		if artifact.Blocks[index].Kind != kind {
			t.Errorf("block %d kind=%q, want %q", index, artifact.Blocks[index].Kind, kind)
		}
	}
	if artifact.Blocks[0].HeadingLevel != 1 || artifact.Blocks[3].HeadingLevel != 2 {
		t.Fatalf("heading levels = %d, %d", artifact.Blocks[0].HeadingLevel, artifact.Blocks[3].HeadingLevel)
	}
}

func TestTextAdapterRejectsInvalidOrEmptyPrimaryContent(t *testing.T) {
	for _, payload := range [][]byte{{0xff, 0xfe}, []byte(" \n\t\n")} {
		if _, err := normalize.Text(normalize.Input{
			SourceID: "src_bad", ExtractionConfigID: "extract-text-v1", Format: "txt", Payload: payload,
		}); err == nil {
			t.Fatalf("Text accepted payload %x", payload)
		}
	}
}

func TestArtifactValidatorRejectsInvalidPublicationContracts(t *testing.T) {
	valid, err := normalize.Text(normalize.Input{
		SourceID: "src_validate", ExtractionConfigID: "extract-text-v1", Format: "txt",
		Payload: []byte("First.\n\nSecond.\n"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := normalize.Validate(valid); err != nil {
		t.Fatalf("Validate(valid): %v", err)
	}
	tests := []struct {
		name   string
		mutate func(*normalize.Artifact)
	}{
		{"unknown coverage", func(artifact *normalize.Artifact) { artifact.Coverage.Status = "unknown" }},
		{"out of bounds", func(artifact *normalize.Artifact) { artifact.Blocks[1].EndRune = 1000 }},
		{"overlap", func(artifact *normalize.Artifact) { artifact.Blocks[1].StartRune = 1 }},
		{"ordinal gap", func(artifact *normalize.Artifact) { artifact.Blocks[1].Ordinal = 4 }},
		{"invalid UTF-8", func(artifact *normalize.Artifact) { artifact.Blocks[0].Text = string([]byte{0xff}) }},
		{"checksum mismatch", func(artifact *normalize.Artifact) {
			artifact.SHA256 = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			candidate := valid
			candidate.Blocks = append([]normalize.Block(nil), valid.Blocks...)
			test.mutate(&candidate)
			if err := normalize.Validate(candidate); err == nil {
				t.Fatal("Validate accepted malformed artifact")
			}
		})
	}
}
