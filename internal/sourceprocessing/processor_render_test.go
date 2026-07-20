package sourceprocessing_test

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"image"
	"image/png"
	"testing"

	"github.com/huangxinxinyu/nano-notebook/internal/documentrender"
	"github.com/huangxinxinyu/nano-notebook/internal/models"
	"github.com/huangxinxinyu/nano-notebook/internal/source"
	"github.com/huangxinxinyu/nano-notebook/internal/sourceprocessing"
)

type selectiveVisionModels struct{ calls int }

func (m *selectiveVisionModels) DescribeImage(_ context.Context, request models.VisionRequest) (models.VisionOutcome, error) {
	m.calls++
	return models.VisionOutcome{Regions: []models.VisionRegion{{Text: "Visual slide evidence", X: 0, Y: 0, Width: float64(request.Width), Height: float64(request.Height)}}}, nil
}

func (*selectiveVisionModels) Transcribe(context.Context, models.TranscriptionRequest) (models.TranscriptionOutcome, error) {
	return models.TranscriptionOutcome{}, fmt.Errorf("unexpected transcription")
}

func TestNativeExtractorCallsVisionOnlyForNativeTextlessPPTXSlides(t *testing.T) {
	payload := mixedPPTX(t)
	first, second := renderPNG(t, 64, 32), renderPNG(t, 64, 32)
	rendered := renderedSlides(payload, first, second)
	media := &selectiveVisionModels{}
	extractor := sourceprocessing.NewNativeExtractor(media, sourceprocessing.NativeExtractorConfig{
		VisionModel: "vision-model", VisionPromptVersion: "vision-v1", MaxVisionPages: 2,
	})
	artifact, err := extractor.ExtractRendered(context.Background(), source.Source{ID: "src_pptx", Format: source.FormatPPTX}, payload, "extract-mixed-v1", rendered)
	if err != nil {
		t.Fatal(err)
	}
	if media.calls != 1 || len(artifact.Blocks) != 2 || artifact.Blocks[0].Text != "Native slide evidence" ||
		artifact.Blocks[1].Text != "Visual slide evidence" || artifact.Blocks[1].Coordinate.Slide != 2 {
		t.Fatalf("calls=%d artifact=%+v", media.calls, artifact)
	}
}

func mixedPPTX(t *testing.T) []byte {
	t.Helper()
	var output bytes.Buffer
	archive := zip.NewWriter(&output)
	files := map[string]string{
		"[Content_Types].xml":   `<Types><Override PartName="/ppt/slides/slide1.xml" ContentType="application/vnd.openxmlformats-officedocument.presentationml.slide+xml"/><Override PartName="/ppt/slides/slide2.xml" ContentType="application/vnd.openxmlformats-officedocument.presentationml.slide+xml"/></Types>`,
		"ppt/slides/slide1.xml": `<p:sld xmlns:p="p" xmlns:a="a"><p:cSld><p:spTree><p:sp><p:spPr><a:xfrm><a:off x="1" y="1"/><a:ext cx="12700" cy="12700"/></a:xfrm></p:spPr><p:txBody><a:p><a:r><a:t>Native slide evidence</a:t></a:r></a:p></p:txBody></p:sp></p:spTree></p:cSld></p:sld>`,
		"ppt/slides/slide2.xml": `<p:sld xmlns:p="p" xmlns:a="a"><p:cSld><p:spTree/></p:cSld></p:sld>`,
	}
	for name, value := range files {
		entry, err := archive.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := entry.Write([]byte(value)); err != nil {
			t.Fatal(err)
		}
	}
	if err := archive.Close(); err != nil {
		t.Fatal(err)
	}
	return output.Bytes()
}

func renderPNG(t *testing.T, width, height int) []byte {
	t.Helper()
	var output bytes.Buffer
	if err := png.Encode(&output, image.NewRGBA(image.Rect(0, 0, width, height))); err != nil {
		t.Fatal(err)
	}
	return output.Bytes()
}

func renderedSlides(input, first, second []byte) documentrender.Result {
	inputDigest := sha256.Sum256(input)
	manifest := documentrender.Manifest{SchemaVersion: 1, SourceID: "src_pptx", Format: documentrender.FormatPPTX, InputSHA256: hex.EncodeToString(inputDigest[:]), RenderConfigID: "render-v1"}
	result := documentrender.Result{}
	for index, payload := range [][]byte{first, second} {
		digest := sha256.Sum256(payload)
		page := documentrender.Page{Ordinal: index + 1, Width: 64, Height: 32, MediaType: "image/png", Bytes: int64(len(payload)), SHA256: hex.EncodeToString(digest[:]), Filename: fmt.Sprintf("slide-%06d.png", index+1)}
		manifest.Pages = append(manifest.Pages, page)
		result.Assets = append(result.Assets, documentrender.Asset{Page: page, Payload: payload})
	}
	result.Manifest = manifest
	return result
}
