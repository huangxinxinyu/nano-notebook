package normalize_test

import (
	"archive/zip"
	"bytes"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/huangxinxinyu/nano-notebook/internal/normalize"
)

func TestDOCXAdapterPreservesHeadingParagraphAndTableOrder(t *testing.T) {
	payload := ooxmlFixture(map[string]string{
		"[Content_Types].xml": `<Types><Override PartName="/word/document.xml" ContentType="application/vnd.openxmlformats-officedocument.wordprocessingml.document.main+xml"/></Types>`,
		"word/document.xml": `<w:document xmlns:w="w"><w:body>
			<w:p><w:pPr><w:pStyle w:val="Heading1"/></w:pPr><w:r><w:t>Contract</w:t></w:r></w:p>
			<w:p><w:r><w:t>First paragraph.</w:t></w:r></w:p>
			<w:tbl><w:tr><w:tc><w:p><w:r><w:t>A1</w:t></w:r></w:p></w:tc><w:tc><w:p><w:r><w:t>B1</w:t></w:r></w:p></w:tc></w:tr></w:tbl>
		</w:body></w:document>`,
	})
	artifact, err := normalize.OOXML(normalize.Input{
		SourceID: "src_docx", ExtractionConfigID: "extract-ooxml-native-v1", Format: "docx", Payload: payload,
	})
	if err != nil {
		t.Fatal(err)
	}
	if artifact.Text != "Contract\n\nFirst paragraph.\n\nA1 | B1" || len(artifact.Blocks) != 3 {
		t.Fatalf("DOCX artifact=%+v", artifact)
	}
	wantKinds := []string{"heading", "paragraph", "table"}
	for index, block := range artifact.Blocks {
		if block.Kind != wantKinds[index] || block.Coordinate == nil || block.Coordinate.Kind != "document_block" ||
			block.Coordinate.Block != index+1 {
			t.Fatalf("block %d=%+v", index, block)
		}
	}
	if artifact.Blocks[0].HeadingLevel != 1 || artifact.Coverage.Status != "complete" {
		t.Fatalf("DOCX structure=%+v", artifact)
	}
}

func TestPPTXAdapterPreservesSlideAndRegionCoordinates(t *testing.T) {
	payload := ooxmlFixture(map[string]string{
		"[Content_Types].xml":   `<Types><Override PartName="/ppt/slides/slide1.xml" ContentType="application/vnd.openxmlformats-officedocument.presentationml.slide+xml"/><Override PartName="/ppt/slides/slide2.xml" ContentType="application/vnd.openxmlformats-officedocument.presentationml.slide+xml"/></Types>`,
		"ppt/slides/slide1.xml": pptxSlide("First slide", 914400, 1828800, 3657600, 914400),
		"ppt/slides/slide2.xml": pptxSlide("Second slide", 457200, 914400, 2743200, 914400),
	})
	first, err := normalize.OOXML(normalize.Input{
		SourceID: "src_pptx", ExtractionConfigID: "extract-ooxml-native-v1", Format: "pptx", Payload: payload,
	})
	if err != nil {
		t.Fatal(err)
	}
	second, err := normalize.OOXML(normalize.Input{
		SourceID: "src_pptx", ExtractionConfigID: "extract-ooxml-native-v1", Format: "pptx", Payload: payload,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Blocks) != 2 || first.Blocks[0].Text != "First slide" || first.Blocks[1].Text != "Second slide" ||
		first.Blocks[0].Coordinate == nil || first.Blocks[0].Coordinate.Kind != "slide_region" ||
		first.Blocks[0].Coordinate.Slide != 1 || first.Blocks[1].Coordinate.Slide != 2 ||
		first.Blocks[0].Coordinate.X != 72 || first.Blocks[0].Coordinate.Width != 288 || first.SHA256 != second.SHA256 {
		t.Fatalf("PPTX artifact=%+v", first)
	}
}

func TestOOXMLAdapterRejectsMalformedWrongContainerAndEmptyPrimaryContent(t *testing.T) {
	tests := []normalize.Input{
		{SourceID: "bad", ExtractionConfigID: "extract-ooxml-native-v1", Format: "docx", Payload: []byte("not zip")},
		{SourceID: "wrong", ExtractionConfigID: "extract-ooxml-native-v1", Format: "docx", Payload: ooxmlFixture(map[string]string{
			"[Content_Types].xml": `<Types/>`, "word/document.xml": `<w:document xmlns:w="w"><w:body><w:p><w:r><w:t>Text</w:t></w:r></w:p></w:body></w:document>`,
		})},
		{SourceID: "empty", ExtractionConfigID: "extract-ooxml-native-v1", Format: "pptx", Payload: ooxmlFixture(map[string]string{
			"[Content_Types].xml":   `<Types><Override PartName="/ppt/slides/slide1.xml" ContentType="application/vnd.openxmlformats-officedocument.presentationml.slide+xml"/></Types>`,
			"ppt/slides/slide1.xml": pptxSlide("", 1, 1, 1, 1),
		})},
	}
	for _, input := range tests {
		if _, err := normalize.OOXML(input); err == nil {
			t.Fatalf("OOXML accepted invalid %s payload", input.SourceID)
		}
	}
}

func TestOOXMLAdapterEnforcesEntryAndExpansionBudgets(t *testing.T) {
	manyParts := make(map[string]string, maxTestOOXMLEntries+1)
	for index := 0; index < maxTestOOXMLEntries+1; index++ {
		manyParts[fmt.Sprintf("custom/item-%05d.xml", index)] = "x"
	}
	oversized := strings.Repeat("x", (16<<20)+1)
	for name, test := range map[string]struct {
		payload []byte
		budget  bool
	}{
		"entry count": {payload: ooxmlFixture(manyParts), budget: true},
		"part size": {payload: ooxmlFixture(map[string]string{
			"[Content_Types].xml": `<Types><Override PartName="/word/document.xml" ContentType="application/vnd.openxmlformats-officedocument.wordprocessingml.document.main+xml"/></Types>`,
			"word/document.xml":   oversized,
		}), budget: true},
		"unsafe path": {payload: ooxmlFixture(map[string]string{"../word/document.xml": "x"})},
	} {
		_, err := normalize.OOXML(normalize.Input{
			SourceID: "src_budget", ExtractionConfigID: "extract-ooxml-native-v1", Format: "docx", Payload: test.payload,
		})
		if err == nil {
			t.Fatalf("OOXML accepted %s violation", name)
		}
		if errors.Is(err, normalize.ErrProcessingBudget) != test.budget {
			t.Fatalf("OOXML %s budget classification=%v err=%v", name, test.budget, err)
		}
	}
}

const maxTestOOXMLEntries = 4096

func ooxmlFixture(files map[string]string) []byte {
	var payload bytes.Buffer
	archive := zip.NewWriter(&payload)
	for name, value := range files {
		entry, err := archive.Create(name)
		if err != nil {
			panic(err)
		}
		if _, err := entry.Write([]byte(value)); err != nil {
			panic(err)
		}
	}
	if err := archive.Close(); err != nil {
		panic(err)
	}
	return payload.Bytes()
}

func pptxSlide(text string, x, y, width, height int) string {
	return fmt.Sprintf(`<p:sld xmlns:p="p" xmlns:a="a"><p:cSld><p:spTree><p:sp>
		<p:spPr><a:xfrm><a:off x="%d" y="%d"/><a:ext cx="%d" cy="%d"/></a:xfrm></p:spPr>
		<p:txBody><a:p><a:r><a:t>%s</a:t></a:r></a:p></p:txBody>
	</p:sp></p:spTree></p:cSld></p:sld>`, x, y, width, height, text)
}
