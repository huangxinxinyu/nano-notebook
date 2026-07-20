package normalize_test

import (
	"bytes"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/huangxinxinyu/nano-notebook/internal/normalize"
)

func TestPDFAdapterExtractsNativePageTextAndCoordinatesDeterministically(t *testing.T) {
	payload := minimalTextPDF("First page evidence.", "Second page evidence.")
	input := normalize.Input{
		SourceID: "src_pdf", ExtractionConfigID: "extract-pdf-native-v1", Format: "pdf", Payload: payload,
	}
	first, err := normalize.PDF(input)
	if err != nil {
		t.Fatal(err)
	}
	second, err := normalize.PDF(input)
	if err != nil {
		t.Fatal(err)
	}
	if first.Format != "pdf" || first.Coverage.Status != "complete" || len(first.Coverage.Gaps) != 0 || len(first.Blocks) != 2 ||
		first.Blocks[0].Text != "First page evidence." || first.Blocks[0].Coordinate == nil || first.Blocks[0].Coordinate.Kind != "pdf_region" ||
		first.Blocks[0].Coordinate.Page != 1 || first.Blocks[1].Coordinate.Page != 2 || first.Blocks[0].Coordinate.Width <= 0 {
		t.Fatalf("PDF artifact=%+v", first)
	}
	if first.SHA256 != second.SHA256 || !bytes.Equal(first.CanonicalJSON, second.CanonicalJSON) {
		t.Fatal("PDF normalization is not deterministic")
	}
	if err := normalize.Validate(first); err != nil {
		t.Fatalf("Validate(PDF): %v", err)
	}
}

func TestPDFAdapterRejectsMalformedOrNativeTextlessDocuments(t *testing.T) {
	for _, payload := range [][]byte{[]byte("%PDF-1.7\nnot a complete PDF"), minimalTextPDF("")} {
		if _, err := normalize.PDF(normalize.Input{
			SourceID: "src_bad_pdf", ExtractionConfigID: "extract-pdf-native-v1", Format: "pdf", Payload: payload,
		}); err == nil {
			t.Fatalf("PDF accepted malformed/textless payload of %d bytes", len(payload))
		}
	}
}

func TestPDFAdapterUsesVisionOnlyForPagesWithoutUsableNativeText(t *testing.T) {
	payload := minimalTextPDF("Native evidence.", "")
	missing, err := normalize.PDFPagesRequiringVision(payload)
	if err != nil || !reflect.DeepEqual(missing, []int{2}) {
		t.Fatalf("missing=%v err=%v", missing, err)
	}
	artifact, err := normalize.PDFWithVisualPages(normalize.Input{
		SourceID: "src_mixed_pdf", ExtractionConfigID: "extract-mixed-v1", Format: "pdf", Payload: payload,
	}, []normalize.VisualPage{{
		Ordinal: 2, Width: 1224, Height: 1584,
		Regions: []normalize.ImageRegion{{Text: "Scanned page evidence.", X: 100, Y: 200, Width: 400, Height: 80}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if len(artifact.Blocks) != 2 || artifact.Blocks[0].Text != "Native evidence." || artifact.Blocks[0].Coordinate.Page != 1 ||
		artifact.Blocks[1].Text != "Scanned page evidence." || artifact.Blocks[1].Coordinate.Page != 2 || artifact.Blocks[1].Coordinate.X != 100 {
		t.Fatalf("artifact=%+v", artifact)
	}
	if _, err := normalize.PDFWithVisualPages(normalize.Input{
		SourceID: "src_mixed_pdf", ExtractionConfigID: "extract-mixed-v1", Format: "pdf", Payload: payload,
	}, nil); err == nil {
		t.Fatal("accepted missing visual page Evidence")
	}
}

func minimalTextPDF(pageTexts ...string) []byte {
	objects := make([]string, 3+2*len(pageTexts))
	objects[0] = "<< /Type /Catalog /Pages 2 0 R >>"
	kids := make([]string, 0, len(pageTexts))
	for index := range pageTexts {
		kids = append(kids, fmt.Sprintf("%d 0 R", 4+index*2))
	}
	objects[1] = fmt.Sprintf("<< /Type /Pages /Kids [%s] /Count %d >>", strings.Join(kids, " "), len(kids))
	objects[2] = "<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>"
	for index, text := range pageTexts {
		pageObject := 4 + index*2
		contentObject := pageObject + 1
		objects[pageObject-1] = fmt.Sprintf("<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Resources << /Font << /F1 3 0 R >> >> /Contents %d 0 R >>", contentObject)
		content := ""
		if text != "" {
			content = "BT /F1 12 Tf 72 720 Td (" + strings.NewReplacer("\\", "\\\\", "(", "\\(", ")", "\\)").Replace(text) + ") Tj ET"
		}
		objects[contentObject-1] = fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(content), content)
	}
	var document bytes.Buffer
	document.WriteString("%PDF-1.4\n")
	offsets := make([]int, len(objects)+1)
	for index, object := range objects {
		offsets[index+1] = document.Len()
		fmt.Fprintf(&document, "%d 0 obj\n%s\nendobj\n", index+1, object)
	}
	xref := document.Len()
	fmt.Fprintf(&document, "xref\n0 %d\n0000000000 65535 f \n", len(objects)+1)
	for index := 1; index < len(offsets); index++ {
		fmt.Fprintf(&document, "%010d 00000 n \n", offsets[index])
	}
	fmt.Fprintf(&document, "trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", len(objects)+1, xref)
	return document.Bytes()
}
