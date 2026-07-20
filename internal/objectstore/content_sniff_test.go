package objectstore_test

import (
	"archive/zip"
	"bytes"
	"errors"
	"testing"

	"github.com/huangxinxinyu/nano-notebook/internal/objectstore"
)

func TestValidateSourceContentDistinguishesDeclaredFormats(t *testing.T) {
	docx := ooxmlFixture(t, "word/document.xml")
	pptx := ooxmlFixture(t, "ppt/presentation.xml")
	tests := []struct {
		name, format string
		payload      []byte
		wantErr      bool
	}{
		{"PDF", "pdf", []byte("%PDF-1.7\nfixture"), false},
		{"fake PDF", "pdf", []byte("MZ executable"), true},
		{"DOCX", "docx", docx, false},
		{"PPTX as DOCX", "docx", pptx, true},
		{"PPTX", "pptx", pptx, false},
		{"PNG", "png", append([]byte("\x89PNG\r\n\x1a\n"), []byte("fixture")...), false},
		{"JPEG", "jpeg", []byte{0xff, 0xd8, 0xff, 0xe0, 0x00}, false},
		{"WebP", "webp", []byte("RIFF\x04\x00\x00\x00WEBP"), false},
		{"WAV", "wav", []byte("RIFF\x04\x00\x00\x00WAVE"), false},
		{"M4A", "m4a", []byte("\x00\x00\x00\x18ftypM4A \x00\x00\x00\x00"), false},
		{"UTF-8 text", "txt", []byte("有效 text\n"), false},
		{"binary text", "txt", []byte{0xff, 0xfe, 0x00}, true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := objectstore.ValidateSourceContent(test.format, bytes.NewReader(test.payload), int64(len(test.payload)))
			if test.wantErr && !errors.Is(err, objectstore.ErrUploadMismatch) {
				t.Fatalf("error = %v, want upload mismatch", err)
			}
			if !test.wantErr && err != nil {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

func ooxmlFixture(t *testing.T, primaryPart string) []byte {
	t.Helper()
	var output bytes.Buffer
	writer := zip.NewWriter(&output)
	for _, name := range []string{"[Content_Types].xml", primaryPart} {
		entry, err := writer.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := entry.Write([]byte("<fixture/>")); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return output.Bytes()
}
