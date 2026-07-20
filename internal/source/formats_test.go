package source_test

import (
	"testing"

	"github.com/huangxinxinyu/nano-notebook/internal/source"
)

func TestValidFileAdmissionRequiresConsistentExtensionFormatAndMediaType(t *testing.T) {
	tests := []struct {
		title     string
		format    source.Format
		mediaType string
		want      bool
	}{
		{"notes.txt", source.FormatTXT, "text/plain", true},
		{"notes.PDF", source.FormatPDF, "application/pdf", true},
		{"photo.jpg", source.FormatJPEG, "image/jpeg", true},
		{"photo.jpeg", source.FormatJPEG, "image/jpeg", true},
		{"paper.exe", source.FormatPDF, "application/pdf", false},
		{"paper.pdf", source.FormatPDF, "text/plain", false},
		{"paper.pdf", source.FormatTXT, "application/pdf", false},
		{"page.html", source.FormatHTML, "text/html", false},
	}
	for _, test := range tests {
		if got := source.ValidFileAdmission(test.title, test.format, test.mediaType); got != test.want {
			t.Errorf("ValidFileAdmission(%q, %q, %q)=%v, want %v", test.title, test.format, test.mediaType, got, test.want)
		}
	}
}
