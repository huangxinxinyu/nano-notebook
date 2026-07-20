package normalize

import (
	"bytes"
	"errors"
	"fmt"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"math"
	"sort"
	"strings"
	"unicode/utf8"

	"golang.org/x/image/webp"
)

const (
	maxImageDimension = 32_768
	maxImagePixels    = 100_000_000
)

type ImageRegion struct {
	Text                string
	X, Y, Width, Height float64
}

type TranscriptSegment struct {
	StartMS int64
	EndMS   int64
	Text    string
}

func ImageDimensions(format string, payload []byte) (int, int, error) {
	format = strings.TrimSpace(format)
	if len(payload) == 0 || (format != "png" && format != "jpeg" && format != "webp") {
		return 0, 0, errors.New("invalid image input")
	}
	var config image.Config
	var err error
	if format == "webp" {
		config, err = webp.DecodeConfig(bytes.NewReader(payload))
	} else {
		config, _, err = image.DecodeConfig(bytes.NewReader(payload))
	}
	if err != nil {
		return 0, 0, fmt.Errorf("decode %s dimensions: %w", format, err)
	}
	if config.Width < 1 || config.Height < 1 || config.Width > maxImageDimension || config.Height > maxImageDimension ||
		int64(config.Width)*int64(config.Height) > maxImagePixels {
		return 0, 0, errors.New("image dimensions exceed processing budget")
	}
	return config.Width, config.Height, nil
}

// Image converts already validated provider-neutral OCR/vision regions into
// stable Nano Evidence. The original encoded image remains the bounds authority.
func Image(input Input, regions []ImageRegion) (Artifact, error) {
	input.SourceID = strings.TrimSpace(input.SourceID)
	input.ExtractionConfigID = strings.TrimSpace(input.ExtractionConfigID)
	input.Format = strings.TrimSpace(input.Format)
	if input.SourceID == "" || input.ExtractionConfigID == "" || len(regions) == 0 || len(regions) > 256 {
		return Artifact{}, errors.New("invalid image normalization input")
	}
	width, height, err := ImageDimensions(input.Format, input.Payload)
	if err != nil {
		return Artifact{}, err
	}
	ordered := append([]ImageRegion(nil), regions...)
	for index := range ordered {
		region := &ordered[index]
		region.Text = strings.TrimSpace(region.Text)
		if region.Text == "" || !utf8.ValidString(region.Text) || utf8.RuneCountInString(region.Text) > 8_000 ||
			invalidMediaNumber(region.X) || invalidMediaNumber(region.Y) || invalidMediaNumber(region.Width) || invalidMediaNumber(region.Height) ||
			region.X < 0 || region.Y < 0 || region.Width <= 0 || region.Height <= 0 ||
			region.X+region.Width > float64(width) || region.Y+region.Height > float64(height) {
			return Artifact{}, errors.New("invalid image Evidence region")
		}
	}
	sort.SliceStable(ordered, func(left, right int) bool {
		if ordered[left].Y != ordered[right].Y {
			return ordered[left].Y < ordered[right].Y
		}
		if ordered[left].X != ordered[right].X {
			return ordered[left].X < ordered[right].X
		}
		return ordered[left].Text < ordered[right].Text
	})
	sourceBlocks := make([]officeBlock, 0, len(ordered))
	for _, region := range ordered {
		sourceBlocks = append(sourceBlocks, officeBlock{kind: "paragraph", text: region.Text, coordinate: SourceCoordinate{
			Kind: "image_region", X: region.X, Y: region.Y, Width: region.Width, Height: region.Height,
		}})
	}
	return finalizeOfficeArtifact(input, sourceBlocks)
}

// Transcript converts timestamped speech/caption output into immutable ordered
// intervals. Silence between intervals is not an extraction gap.
func Transcript(input Input, segments []TranscriptSegment) (Artifact, error) {
	input.SourceID = strings.TrimSpace(input.SourceID)
	input.ExtractionConfigID = strings.TrimSpace(input.ExtractionConfigID)
	input.Format = strings.TrimSpace(input.Format)
	if input.SourceID == "" || input.ExtractionConfigID == "" || len(input.Payload) == 0 ||
		(input.Format != "mp3" && input.Format != "wav" && input.Format != "m4a" && input.Format != "youtube") ||
		len(segments) == 0 || len(segments) > 10_000 {
		return Artifact{}, errors.New("invalid transcript normalization input")
	}
	sourceBlocks := make([]officeBlock, 0, len(segments))
	var previousEnd, previousStart int64
	for _, segment := range segments {
		segment.Text = strings.TrimSpace(segment.Text)
		if segment.Text == "" || !utf8.ValidString(segment.Text) || utf8.RuneCountInString(segment.Text) > 8_000 ||
			segment.EndMS <= segment.StartMS || (input.Format == "youtube" && segment.StartMS < previousStart) ||
			(input.Format != "youtube" && segment.StartMS < previousEnd) {
			return Artifact{}, errors.New("invalid transcript Evidence interval")
		}
		sourceBlocks = append(sourceBlocks, officeBlock{kind: "paragraph", text: segment.Text, coordinate: SourceCoordinate{
			Kind: "time_interval", StartMS: segment.StartMS, EndMS: segment.EndMS,
		}})
		previousEnd = segment.EndMS
		previousStart = segment.StartMS
	}
	return finalizeOfficeArtifact(input, sourceBlocks)
}

func invalidMediaNumber(value float64) bool {
	return math.IsNaN(value) || math.IsInf(value, 0)
}
