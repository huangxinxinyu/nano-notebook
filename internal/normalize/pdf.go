package normalize

import (
	"bytes"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"unicode/utf8"

	pdfreader "github.com/ledongthuc/pdf"
)

const maxNativePDFPages = 500

type pdfLine struct {
	text                string
	x, y, width, height float64
}

// PDF extracts the native text layer from a PDF into the normalized evidence
// contract. A PDF without usable native text is rejected so callers can route
// it to a vision extraction adapter without silently publishing empty evidence.
func PDF(input Input) (Artifact, error) {
	input.SourceID = strings.TrimSpace(input.SourceID)
	input.ExtractionConfigID = strings.TrimSpace(input.ExtractionConfigID)
	input.Format = strings.TrimSpace(input.Format)
	if input.SourceID == "" || input.ExtractionConfigID == "" || input.Format != "pdf" || len(input.Payload) == 0 {
		return Artifact{}, errors.New("invalid PDF normalization input")
	}

	reader, err := pdfreader.NewReader(bytes.NewReader(input.Payload), int64(len(input.Payload)))
	if err != nil {
		return Artifact{}, fmt.Errorf("open PDF: %w", err)
	}
	pageCount := reader.NumPage()
	if pageCount < 1 || pageCount > maxNativePDFPages {
		return Artifact{}, fmt.Errorf("PDF page count %d is outside supported range", pageCount)
	}

	var text strings.Builder
	blocks := make([]Block, 0)
	runeCursor := 0
	for pageNumber := 1; pageNumber <= pageCount; pageNumber++ {
		content, err := safePDFContent(reader.Page(pageNumber))
		if err != nil {
			return Artifact{}, fmt.Errorf("extract PDF page %d: %w", pageNumber, err)
		}
		lines := groupPDFLines(content.Text)
		if len(lines) == 0 {
			return Artifact{}, fmt.Errorf("PDF page %d has no usable native text; vision extraction required", pageNumber)
		}
		for _, line := range lines {
			if text.Len() > 0 {
				text.WriteString("\n\n")
				runeCursor += 2
			}
			start := runeCursor
			text.WriteString(line.text)
			runeCursor += utf8.RuneCountInString(line.text)
			blocks = append(blocks, Block{
				ID: fmt.Sprintf("block_%06d", len(blocks)+1), Ordinal: len(blocks), Kind: "paragraph",
				Text: line.text, StartRune: start, EndRune: runeCursor,
				Coordinate: &SourceCoordinate{
					Kind: "pdf_region", Page: pageNumber, X: line.x, Y: line.y,
					Width: line.width, Height: line.height,
				},
			})
		}
	}

	artifact := Artifact{
		SchemaVersion: "nano.normalized-source.v1", SourceID: input.SourceID,
		ExtractionConfigID: input.ExtractionConfigID, Format: "pdf", Text: text.String(), Blocks: blocks,
		Coverage: Coverage{Status: "complete", TotalRunes: runeCursor, Gaps: make([]Gap, 0)},
	}
	return Finalize(artifact)
}

func safePDFContent(page pdfreader.Page) (content pdfreader.Content, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("PDF parser panic: %v", recovered)
		}
	}()
	return page.Content(), nil
}

func groupPDFLines(fragments []pdfreader.Text) []pdfLine {
	usable := make([]pdfreader.Text, 0, len(fragments))
	for _, fragment := range fragments {
		if fragment.S == "" || !utf8.ValidString(fragment.S) || invalidPDFNumber(fragment.X) || invalidPDFNumber(fragment.Y) ||
			invalidPDFNumber(fragment.W) || invalidPDFNumber(fragment.FontSize) || fragment.W < 0 || fragment.FontSize <= 0 {
			continue
		}
		usable = append(usable, fragment)
	}
	sort.SliceStable(usable, func(left, right int) bool {
		if usable[left].Y != usable[right].Y {
			return usable[left].Y > usable[right].Y
		}
		if usable[left].X != usable[right].X {
			return usable[left].X < usable[right].X
		}
		return false
	})

	lines := make([]pdfLine, 0)
	for cursor := 0; cursor < len(usable); {
		baseline := usable[cursor].Y
		end := cursor + 1
		for end < len(usable) && math.Abs(usable[end].Y-baseline) <= 1.5 {
			end++
		}
		lineFragments := usable[cursor:end]
		sort.SliceStable(lineFragments, func(left, right int) bool { return lineFragments[left].X < lineFragments[right].X })
		var value strings.Builder
		minX, maxX := lineFragments[0].X, lineFragments[0].X+effectivePDFWidth(lineFragments[0])
		minY, maxY := lineFragments[0].Y, lineFragments[0].Y+lineFragments[0].FontSize
		previousEnd := minX
		for index, fragment := range lineFragments {
			if index > 0 && fragment.X-previousEnd > math.Max(1, fragment.FontSize*0.2) &&
				!strings.HasSuffix(value.String(), " ") && !strings.HasPrefix(fragment.S, " ") {
				value.WriteByte(' ')
			}
			value.WriteString(fragment.S)
			fragmentEnd := fragment.X + effectivePDFWidth(fragment)
			previousEnd = math.Max(previousEnd, fragmentEnd)
			minX = math.Min(minX, fragment.X)
			maxX = math.Max(maxX, fragmentEnd)
			minY = math.Min(minY, fragment.Y)
			maxY = math.Max(maxY, fragment.Y+fragment.FontSize)
		}
		lineText := strings.TrimSpace(value.String())
		if lineText != "" && maxX > minX && maxY > minY {
			lines = append(lines, pdfLine{text: lineText, x: minX, y: minY, width: maxX - minX, height: maxY - minY})
		}
		cursor = end
	}
	return lines
}

func invalidPDFNumber(value float64) bool {
	return math.IsNaN(value) || math.IsInf(value, 0)
}

func effectivePDFWidth(fragment pdfreader.Text) float64 {
	if fragment.W > 0 {
		return fragment.W
	}
	return fragment.FontSize * 0.5 * float64(utf8.RuneCountInString(fragment.S))
}
