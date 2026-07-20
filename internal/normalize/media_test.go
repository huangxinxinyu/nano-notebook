package normalize_test

import (
	"bytes"
	"encoding/base64"
	"errors"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"testing"

	"github.com/huangxinxinyu/nano-notebook/internal/normalize"
)

func TestImageAdapterValidatesPixelsAndCanonicalizesRegionOrder(t *testing.T) {
	payload := encodedPNG(t, 320, 200)
	input := normalize.Input{SourceID: "src_image", ExtractionConfigID: "vision-normalize-v1", Format: "png", Payload: payload}
	regions := []normalize.ImageRegion{
		{Text: "Bottom chart.", X: 20, Y: 100, Width: 200, Height: 80},
		{Text: "Top title.", X: 10, Y: 5, Width: 100, Height: 20},
	}
	first, err := normalize.Image(input, regions)
	if err != nil {
		t.Fatal(err)
	}
	second, err := normalize.Image(input, append([]normalize.ImageRegion(nil), regions...))
	if err != nil {
		t.Fatal(err)
	}
	if first.Text != "Top title.\n\nBottom chart." || len(first.Blocks) != 2 ||
		first.Blocks[0].Coordinate == nil || first.Blocks[0].Coordinate.Kind != "image_region" ||
		first.Blocks[0].Coordinate.Width != 100 || first.SHA256 != second.SHA256 {
		t.Fatalf("image artifact=%+v", first)
	}
}

func TestImageDimensionsAcceptSupportedFormats(t *testing.T) {
	var jpegPayload bytes.Buffer
	if err := jpeg.Encode(&jpegPayload, image.NewRGBA(image.Rect(0, 0, 7, 5)), nil); err != nil {
		t.Fatal(err)
	}
	webpPayload, err := base64.StdEncoding.DecodeString("UklGRiIAAABXRUJQVlA4IBYAAAAwAQCdASoBAAEADsD+JaQAA3AAAA==")
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		format  string
		payload []byte
		width   int
		height  int
	}{
		{"png", encodedPNG(t, 3, 4), 3, 4},
		{"jpeg", jpegPayload.Bytes(), 7, 5},
		{"webp", webpPayload, 1, 1},
	}
	for _, test := range tests {
		width, height, err := normalize.ImageDimensions(test.format, test.payload)
		if err != nil || width != test.width || height != test.height {
			t.Fatalf("ImageDimensions(%s)=%dx%d err=%v", test.format, width, height, err)
		}
	}
}

func TestTranscriptAdapterProducesImmutableTimeIntervals(t *testing.T) {
	input := normalize.Input{SourceID: "src_audio", ExtractionConfigID: "transcribe-v1", Format: "m4a", Payload: []byte("audio")}
	artifact, err := normalize.Transcript(input, []normalize.TranscriptSegment{
		{StartMS: 0, EndMS: 1250, Text: "第一段。"},
		{StartMS: 1500, EndMS: 3000, Text: "Second segment."},
	})
	if err != nil {
		t.Fatal(err)
	}
	if artifact.Text != "第一段。\n\nSecond segment." || len(artifact.Blocks) != 2 ||
		artifact.Blocks[1].Coordinate == nil || artifact.Blocks[1].Coordinate.Kind != "time_interval" ||
		artifact.Blocks[1].Coordinate.StartMS != 1500 || artifact.Blocks[1].Coordinate.EndMS != 3000 {
		t.Fatalf("transcript artifact=%+v", artifact)
	}
}

func TestMediaAdaptersRejectOutOfBoundsRegionsAndInvalidIntervals(t *testing.T) {
	if _, err := normalize.Image(normalize.Input{
		SourceID: "src_bad_image", ExtractionConfigID: "vision-v1", Format: "png", Payload: encodedPNG(t, 100, 100),
	}, []normalize.ImageRegion{{Text: "outside", X: 90, Y: 0, Width: 20, Height: 10}}); err == nil {
		t.Fatal("Image accepted an out-of-bounds region")
	}
	if _, err := normalize.Transcript(normalize.Input{
		SourceID: "src_bad_audio", ExtractionConfigID: "transcribe-v1", Format: "wav", Payload: []byte("audio"),
	}, []normalize.TranscriptSegment{
		{StartMS: 0, EndMS: 2000, Text: "first"}, {StartMS: 1500, EndMS: 3000, Text: "overlap"},
	}); err == nil {
		t.Fatal("Transcript accepted overlapping intervals")
	}
}

func TestMediaAdaptersClassifyProviderOutputBudgets(t *testing.T) {
	regions := make([]normalize.ImageRegion, 257)
	_, err := normalize.Image(normalize.Input{SourceID: "image", ExtractionConfigID: "vision-v1", Format: "png", Payload: encodedPNG(t, 1, 1)}, regions)
	if !errors.Is(err, normalize.ErrProcessingBudget) {
		t.Fatalf("image region budget error=%v", err)
	}
	segments := make([]normalize.TranscriptSegment, 10_001)
	_, err = normalize.Transcript(normalize.Input{SourceID: "audio", ExtractionConfigID: "audio-v1", Format: "wav", Payload: []byte("audio")}, segments)
	if !errors.Is(err, normalize.ErrProcessingBudget) {
		t.Fatalf("transcript budget error=%v", err)
	}
}

func encodedPNG(t *testing.T, width, height int) []byte {
	t.Helper()
	value := image.NewRGBA(image.Rect(0, 0, width, height))
	value.Set(0, 0, color.RGBA{R: 255, A: 255})
	var payload bytes.Buffer
	if err := png.Encode(&payload, value); err != nil {
		t.Fatal(err)
	}
	return payload.Bytes()
}
