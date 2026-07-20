package documentrender_test

import (
	"strings"
	"testing"

	"github.com/huangxinxinyu/nano-notebook/internal/documentrender"
)

func TestManifestAcceptsCompleteOrderedPDFPages(t *testing.T) {
	request := documentrender.Request{
		SchemaVersion: 1, SourceID: "src_pdf", Format: documentrender.FormatPDF,
		InputSHA256: strings.Repeat("a", 64), InputBytes: 1024,
		RenderConfigID: "pdfium-7789-144dpi-v1", MaxPages: 10, DPI: 144,
		MaxPixelsPerPage: 20_000_000, MaxOutputBytes: 10 << 20,
	}
	manifest := documentrender.Manifest{
		SchemaVersion: 1, SourceID: request.SourceID, Format: request.Format,
		InputSHA256: request.InputSHA256, RenderConfigID: request.RenderConfigID,
		Pages: []documentrender.Page{
			{Ordinal: 1, Width: 1224, Height: 1584, MediaType: "image/png", Bytes: 1234, SHA256: strings.Repeat("b", 64), Filename: "page-000001.png"},
			{Ordinal: 2, Width: 1224, Height: 1584, MediaType: "image/png", Bytes: 2345, SHA256: strings.Repeat("c", 64), Filename: "page-000002.png"},
		},
	}
	if err := documentrender.Validate(request, manifest); err != nil {
		t.Fatalf("Validate: %v", err)
	}
}

func TestManifestRejectsIdentityDriftMissingPagesAndBudgetOverflow(t *testing.T) {
	baseRequest := documentrender.Request{
		SchemaVersion: 1, SourceID: "src_pptx", Format: documentrender.FormatPPTX,
		InputSHA256: strings.Repeat("a", 64), InputBytes: 1024,
		RenderConfigID: "libreoffice-pdfium-v1", MaxPages: 2, DPI: 144,
		MaxPixelsPerPage: 2_000_000, MaxOutputBytes: 2000,
	}
	baseManifest := documentrender.Manifest{
		SchemaVersion: 1, SourceID: baseRequest.SourceID, Format: baseRequest.Format,
		InputSHA256: baseRequest.InputSHA256, RenderConfigID: baseRequest.RenderConfigID,
		Pages: []documentrender.Page{{Ordinal: 1, Width: 1000, Height: 1000, MediaType: "image/png", Bytes: 1000, SHA256: strings.Repeat("b", 64), Filename: "slide-000001.png"}},
	}
	tests := map[string]func(*documentrender.Manifest){
		"identity": func(value *documentrender.Manifest) { value.InputSHA256 = strings.Repeat("d", 64) },
		"ordinal":  func(value *documentrender.Manifest) { value.Pages[0].Ordinal = 2 },
		"pixels":   func(value *documentrender.Manifest) { value.Pages[0].Width = 2001; value.Pages[0].Height = 1000 },
		"bytes":    func(value *documentrender.Manifest) { value.Pages[0].Bytes = 2001 },
		"filename": func(value *documentrender.Manifest) { value.Pages[0].Filename = "../escape.png" },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			manifest := baseManifest
			manifest.Pages = append([]documentrender.Page(nil), baseManifest.Pages...)
			mutate(&manifest)
			if err := documentrender.Validate(baseRequest, manifest); err == nil {
				t.Fatal("expected invalid renderer manifest")
			}
		})
	}
}

func TestRequestRejectsUnsupportedOrUnboundedRendering(t *testing.T) {
	for _, request := range []documentrender.Request{
		{},
		{SchemaVersion: 1, SourceID: "src", Format: "docx", InputSHA256: strings.Repeat("a", 64), InputBytes: 1, RenderConfigID: "v1", MaxPages: 1, DPI: 144, MaxPixelsPerPage: 1, MaxOutputBytes: 1},
		{SchemaVersion: 1, SourceID: "src", Format: documentrender.FormatPDF, InputSHA256: strings.Repeat("a", 64), InputBytes: 1, RenderConfigID: "v1"},
	} {
		if err := request.Validate(); err == nil {
			t.Fatalf("accepted Request %#v", request)
		}
	}
}
